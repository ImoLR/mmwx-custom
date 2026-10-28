package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func flvxSetCapabilities(a *app, id int64, capabilities ...string) {
	key := flvxID(id)
	a.helperState.mu.Lock()
	status := a.helperState.data.AgentStatuses[key]
	status.Capabilities = append([]string{"helper.update", "flvx.share"}, capabilities...)
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
}

func flvxUserLimitsApp(t *testing.T) (*app, string) {
	t.Helper()
	a := flvxTestApp(t)
	ctx := context.Background()
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/token", "operator-token", nil)
	var token struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &token)
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	d := flvxTestDesired()
	d.Nodes[0].MaxConnections = 10
	if err := a.putFLVXDesired(ctx, 7, d); err != nil {
		t.Fatal(err)
	}
	return a, token.Token
}

func flvxLimitsShareItem(t *testing.T, a *app, token string) map[string]any {
	t.Helper()
	w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares", token, nil)
	var list struct {
		Shares []map[string]any `json:"shares"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
		t.Fatalf("shares: %d %s", w.Code, w.Body.String())
	}
	for _, s := range list.Shares {
		if s["server_id"] == float64(7) {
			return s
		}
	}
	t.Fatal("share 7 missing")
	return nil
}

func putUserLimits(a *app, token string, id string, body any) *httptest.ResponseRecorder {
	return flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/"+id+"/user-limits", token, body)
}

// §2 / §7.1: advertised only while the share's Helper reports the capability.
func TestFLVXUserLimitsCapabilityPostgres(t *testing.T) {
	a, token := flvxUserLimitsApp(t)
	if _, ok := flvxLimitsShareItem(t, a, token)["user_limits"]; ok {
		t.Fatal("advertised without the Helper capability")
	}
	flvxSetCapabilities(a, 7, flvx.UserRateCapability)
	if _, ok := flvxLimitsShareItem(t, a, token)["user_limits"]; ok {
		t.Fatal("rate alone advertises user limits")
	}
	flvxSetCapabilities(a, 7, flvx.UserLimitsCapability)
	if got, _ := json.Marshal(flvxLimitsShareItem(t, a, token)["user_limits"]); string(got) != `{"versions":[1]}` {
		t.Fatalf("v1 capability %s", got)
	}
	flvxSetCapabilities(a, 7, flvx.UserLimitsCapability, flvx.UserRateCapability)
	if got, _ := json.Marshal(flvxLimitsShareItem(t, a, token)["user_limits"]); string(got) != `{"versions":[1,2]}` {
		t.Fatalf("v2 capability %s", got)
	}
}

// §3: conn_mode accepted and validated; stripped for a Helper without the
// capability, whose payload stays byte-identical to today's.
func TestFLVXConnModeDesiredPostgres(t *testing.T) {
	a, token := flvxUserLimitsApp(t)
	ctx := context.Background()
	before, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := flvxTestDesired()
	d.Revision = 2
	d.Nodes[0].MaxConnections = 10
	d.Nodes[0].ConnMode = "sideways"
	if w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d); w.Code != 422 || !strings.Contains(w.Body.String(), `"conn_mode"`) {
		t.Fatalf("invalid conn_mode: %d %s", w.Code, w.Body.String())
	}
	d.Nodes[0].ConnMode = flvx.ConnModeInbound
	if w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d); w.Code != 200 {
		t.Fatalf("conn_mode inbound: %d %s", w.Code, w.Body.String())
	}
	old, _ := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, false, nil)
	raw, _ := json.Marshal(old.Payload.Desired)
	if strings.Contains(string(raw), "conn_mode") {
		t.Fatalf("conn_mode reached an old Helper: %s", raw)
	}
	// Apart from the revision the old Helper's nodes are exactly as before.
	beforeNodes, _ := json.Marshal(before.Payload.Desired.Nodes)
	if oldNodes, _ := json.Marshal(old.Payload.Desired.Nodes); string(beforeNodes) != string(oldNodes) {
		t.Fatalf("old Helper nodes changed:\n%s\n%s", beforeNodes, oldNodes)
	}
	current, _ := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, nil)
	if current.Payload.Desired.Nodes[0].ConnMode != flvx.ConnModeInbound || !current.Verify("helper", "7", time.Now()) {
		t.Fatal("conn_mode not sent to a capable Helper")
	}
	// The stored desired state keeps it; stripping never touches storage.
	s, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if s.Desired.Nodes[0].ConnMode != flvx.ConnModeInbound {
		t.Fatal("stored conn_mode lost")
	}
}

// §4: errors, the sequence rule, usage round trip, budgets never stored.
func TestFLVXUserLimitsEndpointPostgres(t *testing.T) {
	a, token := flvxUserLimitsApp(t)
	ctx := context.Background()
	body := func(version int, seq int64, users ...flvx.UserLimitBudget) flvx.UserLimitsRequest {
		return flvx.UserLimitsRequest{Version: version, Sequence: seq, Users: append([]flvx.UserLimitBudget{}, users...)}
	}
	forty, three := int64(40), 3
	budget := flvx.UserLimitBudget{UserID: "2", MaxInboundConnections: &forty, MaxInboundOnlineIPs: &three, RelayIPReserve: 1}
	if w := putUserLimits(a, token, "7", body(1, 10, budget)); w.Code != 409 || !strings.Contains(w.Body.String(), "user_limits_unsupported") {
		t.Fatalf("Helper without capability: %d %s", w.Code, w.Body.String())
	}
	flvxSetCapabilities(a, 7, flvx.UserLimitsCapability)
	if w := putUserLimits(a, "flvx_wrong", "7", body(1, 10, budget)); w.Code != 401 {
		t.Fatalf("auth: %d", w.Code)
	}
	if w := putUserLimits(a, token, "99", body(1, 10, budget)); w.Code != 404 {
		t.Fatalf("unknown share: %d", w.Code)
	}
	if w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/user-limits", token, nil); w.Code != 405 {
		t.Fatalf("GET: %d", w.Code)
	}
	for name, c := range map[string]struct {
		r     flvx.UserLimitsRequest
		field string
	}{
		"version":         {body(3, 10, budget), "version"},
		"rate without v2": {body(2, 10, budget), "version"},
		"sequence":        {body(1, 0, budget), "sequence"},
		"duplicate":       {body(1, 10, budget, budget), "users"},
		"negative":        {body(1, 10, flvx.UserLimitBudget{UserID: "2", MaxInboundConnections: new(int64)}, flvx.UserLimitBudget{UserID: "3", ServerMaxInboundConnections: func() *int64 { v := int64(-1); return &v }()}), "users"},
	} {
		w := putUserLimits(a, token, "7", c.r)
		if w.Code != 422 || !strings.Contains(w.Body.String(), `"field":"`+c.field+`"`) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/user-limits", token, map[string]any{"version": 1, "sequence": 1, "users": []any{}, "extra": 1}); w.Code != 400 {
		t.Fatalf("unknown field: %d", w.Code)
	}
	revision, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	w := putUserLimits(a, token, "7", body(1, 10, budget))
	if w.Code != 200 || w.Body.String() != `{"applied_sequence":0,"collected_at":0,"users":[]}`+"\n" {
		t.Fatalf("first exchange: %d %q", w.Code, w.Body.String())
	}
	// The Helper reports what it applied; FLVX gets it with the next answer.
	usage := &flvx.UserLimitsUsage{HeldSequence: 10, AppliedSequence: 10, CollectedAt: 1793200001000, Users: []flvx.UserLimitUsage{
		{UserID: "2", InboundConnections: 12, RelayConnections: 30, OnlineIPs: []string{"::ffff:203.0.113.9", "198.51.100.1", "203.0.113.9", "not-an-ip"}},
		{UserID: "bad id", InboundConnections: 1}, {UserID: "4", InboundConnections: -1},
	}}
	if e, err := a.flvxUserLimitsForHelper(ctx, 7, "7", "helper", true, usage); err != nil || e != nil {
		t.Fatalf("Helper holding the sequence got budgets again: %v %v", e, err)
	}
	w = putUserLimits(a, token, "7", body(1, 11, budget))
	want := `{"applied_sequence":10,"collected_at":1793200001000,"users":[{"user_id":"2","inbound_connections":12,"relay_connections":30,"online_ips":["198.51.100.1","203.0.113.9"]}]}` + "\n"
	if w.Code != 200 || w.Body.String() != want {
		t.Fatalf("usage round trip: %d %s", w.Code, w.Body.String())
	}
	// A stale sequence changes nothing and still answers 200.
	five := int64(5)
	if w = putUserLimits(a, token, "7", body(1, 11, flvx.UserLimitBudget{UserID: "2", MaxInboundConnections: &five})); w.Code != 200 {
		t.Fatalf("stale: %d", w.Code)
	}
	if w = putUserLimits(a, token, "7", body(1, 3, flvx.UserLimitBudget{UserID: "2", MaxInboundConnections: &five})); w.Code != 200 {
		t.Fatalf("older: %d", w.Code)
	}
	a.flvxLimitsMu.Lock()
	stored := a.flvxLimits[7].request
	a.flvxLimitsMu.Unlock()
	if stored.Sequence != 11 || *stored.Users[0].MaxInboundConnections != 40 {
		t.Fatalf("stale body applied: %+v", stored)
	}
	// Budgets never touch the share row, the signed desired state or its version.
	after, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if after.Version != revision.Version || after.Desired.Revision != revision.Desired.Revision {
		t.Fatal("a budget change bumped the share version or revision")
	}
	var raw string
	a.flvxDB().QueryRow(`SELECT data_json::text FROM mmwxc_flvx_shares WHERE server_id=7`).Scan(&raw)
	if strings.Contains(raw, "max_inbound_connections") || strings.Contains(raw, "relay_ip_reserve") {
		t.Fatalf("budgets persisted: %s", raw)
	}
	e, _ := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, nil)
	if signed, _ := json.Marshal(e.Payload); strings.Contains(string(signed), "relay_ip_reserve") {
		t.Fatal("budgets in the desired payload")
	}
	// Paused share: 409 and the budgets are dropped.
	if err := a.saveFLVXShare(ctx, 7, flvx.Config{}, "paused", nil); err != nil {
		t.Fatal(err)
	}
	if w = putUserLimits(a, token, "7", body(1, 12, budget)); w.Code != 409 || !strings.Contains(w.Body.String(), "share_inactive") {
		t.Fatalf("paused: %d %s", w.Code, w.Body.String())
	}
	if e, _ := a.flvxUserLimitsForHelper(ctx, 7, "7", "helper", true, nil); e != nil {
		t.Fatal("paused share kept its budgets")
	}
}

// Version 2 (§7): only with the rate capability; the rate is forwarded.
func TestFLVXUserRateVersionPostgres(t *testing.T) {
	a, token := flvxUserLimitsApp(t)
	flvxSetCapabilities(a, 7, flvx.UserLimitsCapability)
	fifty := 50
	r := flvx.UserLimitsRequest{Version: 2, Sequence: 5, Users: []flvx.UserLimitBudget{{UserID: "2", MaxRateMbps: &fifty}}}
	if w := putUserLimits(a, token, "7", r); w.Code != 422 || !strings.Contains(w.Body.String(), `"field":"version"`) {
		t.Fatalf("v2 without rate capability: %d %s", w.Code, w.Body.String())
	}
	flvxSetCapabilities(a, 7, flvx.UserLimitsCapability, flvx.UserRateCapability)
	if w := putUserLimits(a, token, "7", r); w.Code != 200 {
		t.Fatalf("v2: %d %s", w.Code, w.Body.String())
	}
	e, err := a.flvxUserLimitsForHelper(context.Background(), 7, "7", "helper", true, &flvx.UserLimitsUsage{})
	if err != nil || e == nil || e.Payload.Users[0].MaxRateMbps == nil || *e.Payload.Users[0].MaxRateMbps != 50 {
		t.Fatalf("rate not forwarded: %+v %v", e, err)
	}
	summary := a.flvxUserLimitsSummary(7, a.flvxHelpers()[7])
	if summary["users"] != 1 || summary["rated_users"] != 1 || summary["rate_supported"] != true {
		t.Fatalf("summary %v", summary)
	}
}

// Delivery to the Helper: signed with its own domain, relay sources from the
// stored flvx_node_hosts, expiry 10 minutes after the exchange; nothing for
// a Helper that holds the sequence or lacks the capability; expired budgets
// are forgotten and an earlier sequence is accepted again.
func TestFLVXUserLimitsDeliveryPostgres(t *testing.T) {
	a, token := flvxUserLimitsApp(t)
	ctx := context.Background()
	flvxSetCapabilities(a, 7, flvx.UserLimitsCapability)
	if w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/landing-hosts", token, flvx.LandingHosts{Native: []string{}, Nodes: []string{"198.51.100.7", "::ffff:198.51.100.8"}}); w.Code != 200 {
		t.Fatalf("hosts: %d %s", w.Code, w.Body.String())
	}
	two := int64(2)
	if w := putUserLimits(a, token, "7", flvx.UserLimitsRequest{Version: 1, Sequence: 1793200000123, Users: []flvx.UserLimitBudget{{UserID: "2", MaxInboundConnections: &two}}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	e, err := a.flvxUserLimitsForHelper(ctx, 7, "7", "helper", true, &flvx.UserLimitsUsage{HeldSequence: 0})
	if err != nil || e == nil {
		t.Fatalf("no delivery: %v", err)
	}
	if !e.Verify("helper", "7", time.Now()) || e.Verify("other", "7", time.Now()) || e.Payload.Sequence != 1793200000123 {
		t.Fatalf("bad envelope %+v", e)
	}
	if !reflect.DeepEqual(e.Payload.RelaySources, []string{"127.0.0.1", "198.51.100.7", "198.51.100.8", "::1"}) {
		t.Fatalf("relay sources %v", e.Payload.RelaySources)
	}
	if left := time.Until(time.UnixMilli(e.ExpiresAt)); left < 9*time.Minute || left > flvx.UserLimitsTTL {
		t.Fatalf("expiry in %v", left)
	}
	// The desired envelope's verifier rejects it: separate signing domains.
	if (flvx.Envelope{Payload: flvx.Payload{ServerID: "7"}, ExpiresAt: e.ExpiresAt, Signature: e.Signature}).Verify("helper", "7", time.Now()) {
		t.Fatal("cross-domain signature")
	}
	if e, _ = a.flvxUserLimitsForHelper(ctx, 7, "7", "helper", true, &flvx.UserLimitsUsage{HeldSequence: 1793200000123}); e != nil {
		t.Fatal("steady state re-sends budgets")
	}
	// A Helper downgraded to one without the capability gets nothing, and
	// the share's budgets are forgotten.
	if e, _ = a.flvxUserLimitsForHelper(ctx, 7, "7", "helper", false, nil); e != nil {
		t.Fatal("budgets for an old Helper")
	}
	if w := putUserLimits(a, token, "7", flvx.UserLimitsRequest{Version: 1, Sequence: 1793200000124, Users: []flvx.UserLimitBudget{}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// FLVX gone for the whole TTL: nothing delivered, earlier sequences welcome.
	a.flvxLimitsMu.Lock()
	a.flvxLimits[7].exchangedAt = time.Now().Add(-flvx.UserLimitsTTL)
	a.flvxLimitsMu.Unlock()
	if e, _ = a.flvxUserLimitsForHelper(ctx, 7, "7", "helper", true, nil); e != nil {
		t.Fatal("expired budgets delivered")
	}
	if summary := a.flvxUserLimitsSummary(7, a.flvxHelpers()[7]); summary["users"] != 0 || summary["exchanged_at"] != int64(0) {
		t.Fatalf("expired summary %v", summary)
	}
	if w := putUserLimits(a, token, "7", flvx.UserLimitsRequest{Version: 1, Sequence: 5, Users: []flvx.UserLimitBudget{}}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if e, _ = a.flvxUserLimitsForHelper(ctx, 7, "7", "helper", true, nil); e == nil || e.Payload.Sequence != 5 || len(e.Payload.Users) != 0 {
		t.Fatalf("post-expiry sequence: %+v", e)
	}
}

// Report merge: node inbound_connections kept, negative dropped, removed
// nodes 0; FLVX's status shows it.
func TestFLVXInboundConnectionsReportPostgres(t *testing.T) {
	a, token := flvxUserLimitsApp(t)
	ctx := context.Background()
	four, negative := int64(4), int64(-3)
	r := flvx.Report{Generation: "g", Sequence: 1, Period: flvx.Period(time.Now(), 1), Status: flvx.Status{AppliedRevision: 1, Core: flvx.Core{State: "running"}, CollectedAt: time.Now().UnixMilli(),
		Nodes: []flvx.NodeStatus{{ID: "11", State: "active", Connections: 9, InboundConnections: &four}, {ID: "12", State: "removed", InboundConnections: &negative}}}}
	if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, &r); err != nil {
		t.Fatal(err)
	}
	w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", token, nil)
	if !strings.Contains(w.Body.String(), `"connections":9,"inbound_connections":4`) {
		t.Fatalf("status %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "-3") {
		t.Fatalf("negative inbound kept: %s", w.Body.String())
	}
	r.Sequence, r.Nodes = 2, []flvx.NodeStatus{{ID: "12", State: "removed"}}
	if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, &r); err != nil {
		t.Fatal(err)
	}
	s, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	for _, n := range s.Status.Nodes {
		if n.ID == "11" && (n.State != "removed" || n.InboundConnections == nil || *n.InboundConnections != 0) {
			t.Fatalf("unreported node %+v", n)
		}
	}
}
