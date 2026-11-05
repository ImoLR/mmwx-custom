package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// flvxTestScanHelper marks the server's Helper as v0.7.17, plus
// flvx.scan_guard.v1 (v0.7.18) when scan is set.
func flvxTestScanHelper(a *app, id int64, scan bool) {
	key := flvxID(id)
	status := agentStatus{Helper: componentStatus{Installed: true, Version: "v0.7.17"}, Architecture: "amd64", Capabilities: []string{"helper.update", "flvx.share", "flvx.egress.v1", "flvx.protocols.v1", "flvx.reality_guard.v1", flvx.UserLimitsCapability}}
	if scan {
		status.Helper.Version = "v0.7.18"
		status.Capabilities = append(status.Capabilities, flvx.ScanGuardCapability)
	}
	a.helperState.mu.Lock()
	a.helperState.data.Servers[key] = helperServerIdentity{OfficialServerID: key, HelperTokenHash: strings.Repeat("a", 64)}
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
}

// Contract landing-scan-guard-v1 §9 on Custom: the capability only with a
// v0.7.18 Helper whose FLVX Core build has scan_guard.v1; scan_guard
// validated, stored and forwarded only to such a Helper (old Helpers verify
// an unchanged payload); the Helper's hits relayed in the native shape.
func TestFLVXScanGuardPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	db := a.flvxDB()
	auditDeleteExec(t, db, `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	c := flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50}
	flvxTestScanHelper(a, 7, false)
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	list := func(path string) map[string]any {
		t.Helper()
		token := "flvx_fixture"
		if strings.Contains(path, "/admin") {
			token = "operator-token"
		}
		w := flvxTestRequest(a, "GET", path, token, nil)
		var body struct {
			Shares []map[string]any `json:"shares"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Shares) != 1 {
			t.Fatalf("list %s: %d %s", path, w.Code, w.Body.String())
		}
		return body.Shares[0]
	}
	put := func(body any) (int, string) {
		w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", "flvx_fixture", body)
		return w.Code, w.Body.String()
	}
	status := func() string {
		return flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", "flvx_fixture", nil).Body.String()
	}
	upload := func(seq int64, scan bool, st *flvx.ScanGuardStatus) {
		t.Helper()
		r := flvxProtocolsReport(seq, nil, nil, 0)
		r.ScanGuard = st
		if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, scan, false, false, r); err != nil {
			t.Fatal(err)
		}
	}
	node := flvxTestDesired().Nodes[0] // flvxl-11

	// v0.7.17 Helper: no capability, FLVX's payload accepted, the Helper's
	// payload unchanged.
	if s := list("/api/custom/flvx/v1/shares"); s["scan_guard"] != nil {
		t.Fatalf("capability without a scan Helper: %v", s["scan_guard"])
	}
	guard := &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-11"}, Horizontal: 100, HorizontalWeb: 500, WebPorts: []int{80, 443}, WindowS: 60, Vertical: 30, Total: 2000, TotalWindowS: 300}
	if code, body := put(flvx.Desired{Revision: 1, Nodes: []flvx.Node{node}, ScanGuard: guard}); code != 200 {
		t.Fatalf("desired with guard: %d %s", code, body)
	}
	stripped, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, false, false, false, nil)
	raw, _ := json.Marshal(stripped.Payload)
	if err != nil || strings.Contains(string(raw), "scan_guard") || !stripped.Verify("helper", "7", time.Now()) || len(stripped.Payload.Desired.Nodes) != 1 {
		t.Fatalf("old Helper payload: %v %s", err, raw)
	}

	// v0.7.18 Helper on an old FLVX Core build: still no capability.
	flvxTestScanHelper(a, 7, true)
	upload(1, true, &flvx.ScanGuardStatus{State: "core_update", Hits: []flvx.ScanHit{}})
	if s := list("/api/custom/flvx/v1/shares"); s["scan_guard"] != nil {
		t.Fatalf("capability on an old Core: %v", s["scan_guard"])
	}
	if st := status(); !strings.Contains(st, `"scan_guard":{"blocked":[],"hits":[],"state":"core_update"}`) {
		t.Fatalf("status %s", st)
	}
	// ... and with scan_guard.v1.
	upload(2, true, &flvx.ScanGuardStatus{State: "on", Hits: []flvx.ScanHit{}, CoreReady: true})
	if cap, _ := json.Marshal(list("/api/custom/flvx/v1/shares")["scan_guard"]); string(cap) != `{"versions":[1]}` {
		t.Fatalf("capability %s", cap)
	}
	payload, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, true, false, false, nil)
	if err != nil || !payload.Verify("helper", "7", time.Now()) || payload.Payload.Desired.ScanGuard == nil || payload.Payload.Desired.ScanGuard.Total != 2000 {
		t.Fatalf("scan Helper payload: %v %+v", err, payload.Payload.Desired.ScanGuard)
	}
	raw, _ = json.Marshal(payload.Payload.Desired.ScanGuard)
	if string(raw) != `{"enabled":true,"identities":["flvxl-11"],"horizontal":100,"horizontal_web":500,"web_ports":[80,443],"window_s":60,"vertical":30,"total":2000,"total_window_s":300}` {
		t.Fatalf("forwarded %s", raw)
	}

	// Validation (422) and the revision rule (an identical retry is fine,
	// a changed seen needs a new revision).
	for _, bad := range []*flvx.ScanGuard{{Enabled: true, Identities: []string{"flvxl-99"}}, {Enabled: true, Identities: []string{"flvxl-11"}, Horizontal: 100001}, {Enabled: true, WebPorts: []int{443, 443}}, {Seen: -1}} {
		if code, body := put(flvx.Desired{Revision: 2, Nodes: []flvx.Node{node}, ScanGuard: bad}); code != 422 || !strings.Contains(body, "scan_guard") {
			t.Fatalf("%+v accepted: %d %s", bad, code, body)
		}
	}
	if code, body := put(map[string]any{"revision": 2, "nodes": []any{}, "scan_guard": map[string]any{"enabled": true, "extra": 1}}); code != 422 {
		t.Fatalf("unknown field accepted: %d %s", code, body)
	}
	if code, body := put(flvx.Desired{Revision: 1, Nodes: []flvx.Node{node}, ScanGuard: guard}); code != 200 {
		t.Fatalf("identical retry: %d %s", code, body)
	}
	seen := *guard
	seen.Seen = 1793709225987084
	if code, _ := put(flvx.Desired{Revision: 1, Nodes: []flvx.Node{node}, ScanGuard: &seen}); code != 409 {
		t.Fatalf("changed seen without a new revision: %d", code)
	}

	// A hit: relayed in the native shape; out-of-contract entries dropped,
	// never failing the report (billing goes on).
	at := time.Date(2026, 11, 4, 8, 0, 0, 123, time.UTC)
	hit := flvx.ScanHit{ID: "11", InboundTag: "flvxl-11", Type: "horizontal", Count: 100, Port: 22, WindowS: 60, At: at, Seq: 1793709225987084}
	upload(3, true, &flvx.ScanGuardStatus{State: "on", CoreReady: true, Hits: []flvx.ScanHit{hit, {ID: "11", InboundTag: "flvxl-12", Type: "total", Seq: 5}}, Blocked: []string{"flvxl-11", "bogus"}})
	want := `"scan_guard":{"blocked":["flvxl-11"],"hits":[{"id":"11","inbound_tag":"flvxl-11","type":"horizontal","count":100,"port":22,"window_s":60,"at":"2026-11-04T08:00:00.000000123Z","seq":1793709225987084}],"state":"on"}`
	if st := status(); !strings.Contains(st, want) {
		t.Fatalf("status %s", st)
	}
	upload(4, true, &flvx.ScanGuardStatus{State: "sweeping"})
	if st := status(); strings.Contains(st, "scan_guard") {
		t.Fatalf("unknown state relayed: %s", st)
	}
	upload(5, true, &flvx.ScanGuardStatus{State: "on", CoreReady: true, Hits: []flvx.ScanHit{hit}, Blocked: []string{"flvxl-11"}})
	plan, _ := json.Marshal(list("/api/custom/flvx/admin")["scan_guard_plan"])
	if string(plan) != `{"blocked":1,"core_ready":true,"nodes":1,"pending":1,"requested":true,"state":"on","supported":true}` {
		t.Fatalf("operator plan %s", plan)
	}
	if s := list("/api/custom/flvx/v1/shares"); s["scan_guard_plan"] != nil {
		t.Fatal("operator plan sent to FLVX")
	}
	// FLVX records it (seen) and disables the node in a new revision.
	off := false
	recorded := node
	recorded.Enabled = &off
	if code, body := put(flvx.Desired{Revision: 2, Nodes: []flvx.Node{recorded}, ScanGuard: &seen}); code != 200 {
		t.Fatalf("recorded: %d %s", code, body)
	}
	payload, _ = a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, true, false, false, nil)
	if payload.Payload.Desired.ScanGuard.Seen != seen.Seen || *payload.Payload.Desired.Nodes[0].Enabled {
		t.Fatalf("recorded payload %+v", payload.Payload.Desired)
	}
	// Downgraded Helper: no scan_guard, no capability.
	flvxTestScanHelper(a, 7, false)
	if s := list("/api/custom/flvx/v1/shares"); s["scan_guard"] != nil {
		t.Fatal("capability after downgrade")
	}
}

// Nodes withheld from a Helper (e.g. an extra protocol it cannot render)
// leave the forwarded identities too; the stored desired state is untouched.
func TestFLVXHelperScanGuardDropsWithheldNodes(t *testing.T) {
	ss := flvxTestDesired().Nodes[0]
	extra := ss
	extra.ID, extra.Protocol, extra.SS2022, extra.Mieru = "12", flvx.ProtocolMieru, nil, &flvx.Mieru{Username: "u", Password: "p"}
	d := flvx.Desired{Revision: 1, Nodes: []flvx.Node{ss, extra}, ScanGuard: &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-11", "flvxl-12"}}}
	got := flvxHelperScanGuard(flvxHelperProtocols(d, false), true)
	if len(got.Nodes) != 1 || len(got.ScanGuard.Identities) != 1 || got.ScanGuard.Identities[0] != "flvxl-11" || len(flvx.ValidateScanGuard(got)) != 0 {
		t.Fatalf("forwarded %+v", got.ScanGuard)
	}
	if len(d.ScanGuard.Identities) != 2 {
		t.Fatal("stored desired state changed")
	}
	if flvxHelperScanGuard(d, false).ScanGuard != nil || flvxHelperScanGuard(flvx.Desired{}, true).ScanGuard != nil {
		t.Fatal("guard kept for an old Helper or invented")
	}
}
