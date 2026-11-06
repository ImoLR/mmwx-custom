package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// flvxTestRealityHelper marks the server's Helper as v0.7.21, plus
// flvx.reality_pin.v1 / flvx.reality_probe.v1 (v0.7.22) when pick is set.
func flvxTestRealityHelper(a *app, id int64, pick bool) {
	flvxTestNodeTotalHelper(a, id, true)
	key := flvxID(id)
	a.helperState.mu.Lock()
	status := a.helperState.data.AgentStatuses[key]
	status.Helper.Version = "v0.7.21"
	status.Capabilities = append(status.Capabilities, flvx.CoreUnlimitedCapability)
	if pick {
		status.Helper.Version = "v0.7.22"
		status.Capabilities = append(status.Capabilities, flvx.RealityPinCapability, flvx.RealityProbeCapability)
	}
	status.ReportedAt = time.Now()
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
}

// Contract reality-pick-v1 on Custom: both capabilities only with a v0.7.22
// Helper; the probe is queued, handed to the Helper signed, relayed back and
// pollable; reality_pins relayed in the status (bad entries dropped).
func TestFLVXRealityPickPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	db := a.flvxDB()
	auditDeleteExec(t, db, `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	flvxTestRealityHelper(a, 7, false)
	if err := a.saveFLVXShare(ctx, 7, flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1}, "active", nil); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		token := "flvx_fixture"
		if strings.Contains(path, "/admin") {
			token = "operator-token"
		}
		w := flvxTestRequest(a, method, path, token, body)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	share := func(path string) map[string]any {
		t.Helper()
		_, body := call("GET", path, nil)
		return body["shares"].([]any)[0].(map[string]any)
	}
	probeReq := map[string]any{"targets": []string{"WWW.Lovelive-Anime.jp", "www.lovelive-anime.jp:443", "dl.google.com:443"}}

	// v0.7.21: nothing advertised, a probe is refused.
	for _, path := range []string{"/api/custom/flvx/v1/shares", "/api/custom/flvx/admin"} {
		if s := share(path); s["reality_pin"] != nil || s["reality_probe"] != nil {
			t.Fatalf("%s: capability without a v0.7.22 Helper: %v %v", path, s["reality_pin"], s["reality_probe"])
		}
	}
	if code, body := call("POST", "/api/custom/flvx/v1/shares/7/reality-probe", probeReq); code != 409 || !strings.Contains(body["error"].(map[string]any)["code"].(string), "helper_outdated") {
		t.Fatalf("probe on an old Helper: %d %v", code, body)
	}

	flvxTestRealityHelper(a, 7, true)
	s := share("/api/custom/flvx/v1/shares")
	if pin, _ := json.Marshal(s["reality_pin"]); string(pin) != `{"versions":[1]}` {
		t.Fatalf("reality_pin %s", pin)
	}
	if probe, _ := json.Marshal(s["reality_probe"]); string(probe) != `{"concurrency":4,"max_targets":64,"versions":[1]}` {
		t.Fatalf("reality_probe %s", probe)
	}
	for _, bad := range []map[string]any{
		{"targets": []string{}},
		{"targets": []string{"10.0.0.1:443"}},
		{"targets": []string{"localhost:443"}},
		{"targets": []string{"a.example:99999"}},
		{"targets": []string{"a.example"}, "timeout_ms": 100},
		{"targets": []string{"a.example"}, "extra": 1},
	} {
		if code, body := call("POST", "/api/custom/flvx/v1/shares/7/reality-probe", bad); code != 422 && code != 400 {
			t.Fatalf("bad probe %v accepted: %d %v", bad, code, body)
		}
	}
	if code, _ := call("POST", "/api/custom/flvx/v1/shares/9/reality-probe", probeReq); code != 404 && code != 409 {
		t.Fatalf("probe on a server without share: %d", code)
	}
	code, started := call("POST", "/api/custom/flvx/v1/shares/7/reality-probe", probeReq)
	probeID, _ := started["probe_id"].(string)
	if code != 200 || started["state"] != "queued" || probeID == "" {
		t.Fatalf("start %d %v", code, started)
	}
	if targets, _ := json.Marshal(started["targets"]); string(targets) != `["www.lovelive-anime.jp:443","dl.google.com:443"]` || started["timeout_ms"] != float64(3000) {
		t.Fatalf("normalised %s %v", targets, started["timeout_ms"])
	}
	if code, body := call("POST", "/api/custom/flvx/v1/shares/7/reality-probe", probeReq); code != 409 || !strings.Contains(body["error"].(map[string]any)["message"].(string), probeID) {
		t.Fatalf("second probe while busy: %d %v", code, body)
	}

	// The Helper gets the signed job until it reports done.
	if a.flvxProbeForHelper(7, "7", "helper", false, nil) != nil {
		t.Fatal("job handed to a Helper without the capability")
	}
	e := a.flvxProbeForHelper(7, "7", "helper", true, nil)
	if e == nil || !e.Verify("helper", "7", time.Now()) || e.Verify("other", "7", time.Now()) || e.Job.ID != probeID || len(e.Job.Targets) != 2 {
		t.Fatalf("envelope %+v", e)
	}
	a.flvxProbeForHelper(7, "7", "helper", true, &flvx.ProbeReport{ID: "other", State: "done"})
	if _, body := call("GET", "/api/custom/flvx/v1/shares/7/reality-probe/"+probeID, nil); body["state"] != "queued" {
		t.Fatalf("foreign report applied: %v", body)
	}
	if e = a.flvxProbeForHelper(7, "7", "helper", true, &flvx.ProbeReport{ID: probeID, State: "running", StartedAt: time.Now().UnixMilli()}); e == nil {
		t.Fatal("running job not handed over again")
	}
	if _, body := call("GET", "/api/custom/flvx/v1/shares/7/reality-probe/"+probeID, nil); body["state"] != "running" || body["started_at"].(float64) == 0 {
		t.Fatalf("running %v", body)
	}
	results := []flvx.ProbeResult{
		{Target: "www.lovelive-anime.jp:443", Feasible: true, LatencyMS: 12.5, BestIP: "65.9.180.62", IPs: []flvx.ProbeIP{{IP: "65.9.180.62", TCPMS: 3.5}, {IP: "54.230.70.30", TCPMS: -1}}, TLS13: true, X25519: true, H2: true, CertValid: true, ServerNames: []string{"www.lovelive-anime.jp", "*.bad", "lovelive-anime.jp"}, CertChainBytes: 4012},
		{Target: "dl.google.com:443", Reason: "certificate not valid", IPs: []flvx.ProbeIP{}, ServerNames: []string{}},
	}
	if e = a.flvxProbeForHelper(7, "7", "helper", true, &flvx.ProbeReport{ID: probeID, State: "done", Results: results}); e != nil {
		t.Fatal("done job still handed over")
	}
	_, done := call("GET", "/api/custom/flvx/v1/shares/7/reality-probe/"+probeID, nil)
	got, _ := json.Marshal(done["results"])
	if done["state"] != "done" || !strings.Contains(string(got), `"best_ip":"65.9.180.62"`) || strings.Contains(string(got), "*.bad") || !strings.Contains(string(got), `"tcp_ms":-1`) {
		t.Fatalf("done %v %s", done["state"], got)
	}
	if code, _ := call("GET", "/api/custom/flvx/v1/shares/7/reality-probe/nope", nil); code != 404 {
		t.Fatalf("unknown probe: %d", code)
	}
	// A new probe after the last finished.
	if code, _ := call("POST", "/api/custom/flvx/v1/shares/7/reality-probe", probeReq); code != 200 {
		t.Fatalf("probe after done: %d", code)
	}
	// Never picked up: error after the pickup limit.
	a.flvxProbesMu.Lock()
	a.flvxProbes[7].createdAt = time.Now().Add(-flvxProbePickupLimit - time.Second)
	a.flvxProbesMu.Unlock()
	if _, body := call("GET", "/api/custom/flvx/v1/shares/7/reality-probe/"+a.flvxProbes[7].job.ID, nil); body["state"] != "error" || body["error"] != "Helper did not start the probe" {
		t.Fatalf("stale job %v", body)
	}

	// reality_pins: relayed as reported, malformed entries dropped.
	r := flvxProtocolsReport(1, nil, nil, 0)
	r.RealityPins = []flvx.RealityPinStatus{
		{Host: "www.lovelive-anime.jp:443", PinnedIP: "65.9.180.116", PinnedMS: 3.8, BestIP: "65.9.180.62", BestMS: 3.5, CheckedAt: 1, Nodes: []string{"132"}},
		{Host: "www.lovelive-anime.jp:443", PinnedIP: "127.0.0.1", PinnedMS: 1, BestMS: 1, Nodes: []string{"x"}},
		{Host: "bad host", PinnedIP: "203.0.113.1", Nodes: []string{"y"}},
	}
	if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, true, true, true, r); err != nil {
		t.Fatal(err)
	}
	_, status := call("GET", "/api/custom/flvx/v1/shares/7/status", nil)
	pins, _ := json.Marshal(status["reality_pins"])
	if string(pins) != `[{"best_ip":"65.9.180.62","best_ms":3.5,"checked_at":1,"host":"www.lovelive-anime.jp:443","nodes":["132"],"pinned_ip":"65.9.180.116","pinned_ms":3.8}]` {
		t.Fatalf("status reality_pins %s", pins)
	}
}

// GET /reality-pool never fails FLVX: cached for 12 h, an error leaves an
// empty list plus the reason (or the last good list), at most one refresh.
func TestFLVXRealityPool(t *testing.T) {
	a := flvxTestApp(t)
	auditDeleteExec(t, a.flvxDB(), `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	calls := 0
	fail := errors.New("official 「Reality 共享域名池」 is off")
	result := func() ([]map[string]any, int, error) { return nil, 97, fail }
	old := flvxPoolSource
	flvxPoolSource = func(*app, context.Context) ([]map[string]any, int, error) { calls++; return result() }
	t.Cleanup(func() { flvxPoolSource = old })
	get := func() map[string]any {
		t.Helper()
		w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/reality-pool", "flvx_fixture", nil)
		var out map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("pool %d %s", w.Code, w.Body.String())
		}
		return out
	}
	if out := get(); len(out["domains"].([]any)) != 0 || out["error"] != fail.Error() || out["pool_size"] != float64(97) || out["fetched_at"] != float64(0) {
		t.Fatalf("failed pool %v", out)
	}
	get()
	if calls != 1 {
		t.Fatalf("refresh not backed off: %d calls", calls)
	}
	result = func() ([]map[string]any, int, error) {
		return []map[string]any{{"domain": "addons.mozilla.org"}, {"domain": "www.apple.com"}}, 97, nil
	}
	a.flvxPool.attempted = time.Time{}
	out := get()
	if domains, _ := json.Marshal(out["domains"]); string(domains) != `[{"domain":"addons.mozilla.org"},{"domain":"www.apple.com"}]` || out["error"] != nil || out["fetched_at"].(float64) == 0 {
		t.Fatalf("pool %v", out)
	}
	get()
	if calls != 2 {
		t.Fatalf("pool not cached: %d calls", calls)
	}
	// Expired + failing: the last good list stays, with the reason.
	result = func() ([]map[string]any, int, error) { return nil, 0, errors.New("official share status unavailable") }
	a.flvxPool.fetchedAt = time.Now().Add(-flvxPoolTTL - time.Minute)
	a.flvxPool.attempted = time.Time{}
	get()
	for i := 0; i < 100; i++ {
		a.flvxPool.mu.Lock()
		busy := a.flvxPool.done != nil
		a.flvxPool.mu.Unlock()
		if !busy {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if out := get(); len(out["domains"].([]any)) != 2 || out["error"] != "official share status unavailable" {
		t.Fatalf("stale pool %v", out)
	}
}

// Official v0.5.5's domain list: only "shared_pool" sources, normalised,
// deduplicated, with official's own check attached.
func TestFLVXOfficialPoolParse(t *testing.T) {
	raw := `{"success":true,"domain_sources":{"addons.mozilla.org":"shared_pool","boilhkt99u.imgamer.top":"server","WWW.Apple.com":"shared_pool","www.apple.com":["custom","shared_pool"],"10.0.0.1":"shared_pool","bad_host!":"shared_pool","www.lovelive-anime.jp":"custom"},
		"domains":[{"domain":"addons.mozilla.org","success":true,"latency_ms":5,"reality_usable":true},{"domain":"www.apple.com","success":false}]}`
	var list officialRealityDomains
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(list.pool("2"))
	if string(got) != `[{"domain":"addons.mozilla.org","official_latency_ms":5,"official_ok":true,"official_probe_server_id":2,"official_reality_usable":true},{"domain":"www.apple.com","official_ok":false,"official_probe_server_id":2}]` {
		t.Fatalf("pool %s", got)
	}
}
