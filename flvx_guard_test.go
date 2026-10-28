package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// flvxTestGuardHelper marks the server's Helper as a v0.7.12 one, plus
// flvx.reality_guard.v1 when guard is set.
func flvxTestGuardHelper(a *app, id int64, guard bool) {
	key := flvxID(id)
	status := agentStatus{Helper: componentStatus{Installed: true, Version: "v0.7.12"}, Architecture: "amd64", Capabilities: []string{"helper.update", "flvx.share", "flvx.egress.v1", "flvx.protocols.v1"}}
	if guard {
		status.Helper.Version = "v0.7.13"
		status.Capabilities = append(status.Capabilities, "flvx.reality_guard.v1")
	}
	a.helperState.mu.Lock()
	a.helperState.data.Servers[key] = helperServerIdentity{OfficialServerID: key, HelperTokenHash: strings.Repeat("a", 64)}
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
}

func flvxTestRealityNode(dest string) flvx.Node {
	enabled := true
	return flvx.Node{ID: "12", Owner: flvx.Owner{ID: "2", Name: "Alice"}, Name: "Reality", Protocol: "vless_reality_vision", Port: 30005, Enabled: &enabled,
		VLESS: &flvx.VLESS{UUID: "12345678-1234-4234-8234-123456789abc", Flow: "xtls-rprx-vision", Reality: flvx.Reality{Dest: dest, ServerNames: []string{"www.example.com"}, PrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortIDs: []string{"a1b2"}}}}
}

// Contract landing-reality-guard-v1 on Custom: capability only with a guard
// Helper, the switch stored and forwarded only to such a Helper (old Helpers
// verify an unchanged payload), the user dest still validated, and the
// Helper status relayed without its operator-only reason.
func TestFLVXRealityGuardPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	db := a.flvxDB()
	auditDeleteExec(t, db, `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	c := flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50}
	flvxTestGuardHelper(a, 7, false)
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	list := func(path, token string) map[string]any {
		t.Helper()
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

	// v0.7.12 Helper: no capability; an old FLVX payload is accepted as is.
	if s := list("/api/custom/flvx/v1/shares", "flvx_fixture"); s["reality_guard"] != nil {
		t.Fatalf("capability without a guard Helper: %v", s["reality_guard"])
	}
	ss, reality := flvxTestDesired().Nodes[0], flvxTestRealityNode("www.example.com:443")
	if code, body := put(flvx.Desired{Revision: 1, Nodes: []flvx.Node{ss, reality}}); code != 200 {
		t.Fatalf("old FLVX payload: %d %s", code, body)
	}
	oldPayload, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(oldPayload.Payload.Desired)
	if strings.Contains(string(raw), "reality_guard") || !oldPayload.Verify("helper", "7", time.Now()) {
		t.Fatalf("old FLVX payload changed: %s", raw)
	}

	flvxTestGuardHelper(a, 7, true)
	cap, _ := json.Marshal(list("/api/custom/flvx/v1/shares", "flvx_fixture")["reality_guard"])
	if string(cap) != `{"versions":[1]}` {
		t.Fatalf("capability %s", cap)
	}
	// The user's dest is still validated; the guard dest is Helper-internal.
	on := true
	for _, dest := range []string{"127.0.0.1:41001", "localhost:443", "10.0.0.1:443"} {
		if code, body := put(flvx.Desired{Revision: 2, Nodes: []flvx.Node{ss, flvxTestRealityNode(dest)}, RealityGuard: &on}); code != 422 || !strings.Contains(body, "vless.reality.dest") {
			t.Fatalf("dest %s accepted: %d %s", dest, code, body)
		}
	}
	if code, body := put(map[string]any{"revision": 2, "nodes": []any{}, "reality_guard": "yes"}); code != 422 {
		t.Fatalf("non-boolean switch accepted: %d %s", code, body)
	}
	if code, body := put(flvx.Desired{Revision: 2, Nodes: []flvx.Node{ss, reality}, RealityGuard: &on}); code != 200 {
		t.Fatalf("guard on: %d %s", code, body)
	}
	guardPayload, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, false, nil)
	if err != nil || !guardPayload.Payload.Desired.GuardOn() || !guardPayload.Verify("helper", "7", time.Now()) {
		t.Fatalf("guard Helper payload: %v %+v", err, guardPayload.Payload.Desired.RealityGuard)
	}
	// A Helper without the capability (e.g. downgraded) never sees the
	// switch: it would reject the payload on the unknown field.
	stripped, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, false, nil)
	raw, _ = json.Marshal(stripped.Payload)
	if err != nil || strings.Contains(string(raw), "reality_guard") || len(stripped.Payload.Desired.Nodes) != 2 || !stripped.Verify("helper", "7", time.Now()) {
		t.Fatalf("old Helper payload: %v %s", err, raw)
	}

	// Helper status: relayed to FLVX as state + nodes; the reason stays with
	// the operator.
	report := flvxProtocolsReport(1, nil, nil, 2)
	report.RealityGuard = &flvx.RealityGuardStatus{State: "on", Nodes: 1, Error: "Reality 防盗辅助入站（本机端口 41001）启动失败\n相关节点暂按未开启处理"}
	if _, err = a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, false, report); err != nil {
		t.Fatal(err)
	}
	status := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", "flvx_fixture", nil).Body.String()
	if !strings.Contains(status, `"reality_guard":{"nodes":1,"state":"on"}`) || strings.Contains(status, "41001") {
		t.Fatalf("status %s", status)
	}
	if s := list("/api/custom/flvx/v1/shares", "flvx_fixture"); s["reality_guard_plan"] != nil || strings.Contains(string(mustJSON(t, s)), "41001") {
		t.Fatalf("operator detail leaked to FLVX: %v", s)
	}
	admin := list("/api/custom/flvx/admin", "operator-token")
	plan, _ := json.Marshal(admin["reality_guard_plan"])
	if string(plan) != `{"error":"Reality 防盗辅助入站（本机端口 41001）启动失败 相关节点暂按未开启处理","reality_nodes":1,"requested":true}` {
		t.Fatalf("operator plan %s", plan)
	}
	if g, _ := json.Marshal(admin["report"].(map[string]any)["reality_guard"]); string(g) != `{"nodes":1,"state":"on"}` {
		t.Fatalf("operator report %s", g)
	}
	// Out-of-contract states are rejected with the whole report.
	bad := flvxProtocolsReport(2, nil, nil, 2)
	bad.RealityGuard = &flvx.RealityGuardStatus{State: "applied", Nodes: 1}
	if _, err = a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, false, bad); err == nil {
		t.Fatal("invalid guard status accepted")
	}
	// Switched off: the Helper stops reporting, and so does Custom.
	off := false
	if code, body := put(flvx.Desired{Revision: 3, Nodes: []flvx.Node{ss, reality}, RealityGuard: &off}); code != 200 {
		t.Fatalf("guard off: %d %s", code, body)
	}
	if _, err = a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, false, flvxProtocolsReport(3, nil, nil, 3)); err != nil {
		t.Fatal(err)
	}
	if status = flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", "flvx_fixture", nil).Body.String(); strings.Contains(status, "reality_guard") {
		t.Fatalf("status after off %s", status)
	}
	if plan, _ = json.Marshal(list("/api/custom/flvx/admin", "operator-token")["reality_guard_plan"]); string(plan) != `{"reality_nodes":1,"requested":false}` {
		t.Fatalf("operator plan after off %s", plan)
	}
}
