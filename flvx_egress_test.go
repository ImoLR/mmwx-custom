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

func flvxTestEgress() *flvx.Egress {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	return &flvx.Egress{Version: 1, Outbounds: []flvx.EgressOutbound{
		{Tag: "flvxo-3", Type: "wireguard", IPVersion: "force_ipv4", WireGuard: &flvx.EgressWireGuard{PrivateKey: key, PeerPublicKey: key, AddressV4: "172.16.0.2", EndpointV4: "162.159.192.6:2408"}},
		{Tag: "flvxo-5", Type: "socks", IPVersion: "prefer_ipv4", Server: "proxy.example.com", Port: 1080, Username: "u", Password: "p"},
	}, Rules: []flvx.EgressRule{
		{Name: "屏蔽大陆 IP", InboundTags: []string{"flvxl-11"}, IP: []string{"geoip:cn"}, Outbound: "block"},
		{Name: "禁止大陆直连", InboundTags: []string{"flvxl-11"}, SourceIP: []string{"geoip:cn"}, Outbound: "block"},
		{Name: "默认出站", InboundTags: []string{"flvxl-11"}, Outbound: "flvxo-5"},
	}, ExemptSources: []string{"1.0.1.3/32", "2001:db8::1/128"}}
}

// flvxTestReport stores a Helper report (optionally with egress capability).
func flvxTestReport(t *testing.T, a *app, sequence int64, egress *flvx.EgressStatus) {
	t.Helper()
	r := flvx.Report{Generation: "helper-egress", Sequence: sequence, Period: flvx.Period(time.Now(), 1), Status: flvx.Status{AppliedRevision: 1, Core: flvx.Core{State: "running", Epoch: "e"}, Nodes: []flvx.NodeStatus{}, CollectedAt: time.Now().UnixMilli(), Egress: egress}}
	if _, err := a.flvxHelperDesired(context.Background(), 7, "helper-uuid", "helper-secret", "amd64", true, false, &r); err != nil {
		t.Fatal(err)
	}
}

func flvxShareItem(t *testing.T, a *app, token string) map[string]any {
	t.Helper()
	w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares", token, nil)
	var body struct {
		Shares []map[string]any `json:"shares"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Shares) != 1 {
		t.Fatalf("shares %d %s", w.Code, w.Body.String())
	}
	return body.Shares[0]
}

func TestFLVXEgressContractPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/token", "operator-token", nil)
	var tokenResult struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokenResult)
	token := tokenResult.Token
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	d := flvxTestDesired()
	if w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// Old Helper: no capability advertised, plans refused.
	flvxTestReport(t, a, 1, nil)
	if _, ok := flvxShareItem(t, a, token)["egress"]; ok {
		t.Fatal("egress advertised for a Helper without support")
	}
	d.Revision, d.Egress = 2, flvxTestEgress()
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"field":"egress"`) || !strings.Contains(w.Body.String(), "暂不支持自定义出站") {
		t.Fatalf("plan for an old Helper: %d %s", w.Code, w.Body.String())
	}
	// New Helper reports version 1 and geo data.
	flvxTestReport(t, a, 2, &flvx.EgressStatus{Versions: []int{1}, GeoIP: true, GeoSite: false, State: "none", AppliedRevision: 1})
	got, _ := json.Marshal(flvxShareItem(t, a, token)["egress"])
	if string(got) != `{"allowed":true,"geoip":true,"geosite":false,"versions":[1]}` {
		t.Fatalf("capability %s", got)
	}
	// Validation repeats the contract: geosite needs the Helper's geo data,
	// inbound tags must be nodes of this desired state, literals are checked.
	bad := flvxTestEgress()
	bad.Rules[0].Domain = []string{"geosite:openai"}
	bad.Rules[1].InboundTags = []string{"flvxl-99"}
	bad.Outbounds[1].Server = "10.1.2.3"
	d.Egress = bad
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d)
	for _, field := range []string{`"field":"egress.rules[0].domain[0]"`, `"field":"egress.rules[1].inbound_tags[0]"`, `"field":"egress.outbounds[1].server"`, `"node_id":""`} {
		if w.Code != 422 || !strings.Contains(w.Body.String(), field) {
			t.Fatalf("missing %s: %d %s", field, w.Code, w.Body.String())
		}
	}
	// A valid plan with FLVX's native-only allow_core_restart is accepted;
	// the flag is ignored and an identical retry stays idempotent.
	raw, _ := json.Marshal(map[string]any{"revision": 2, "nodes": d.Nodes, "egress": flvxTestEgress()})
	var generic map[string]any
	json.Unmarshal(raw, &generic)
	generic["egress"].(map[string]any)["allow_core_restart"] = true
	for i := 0; i < 2; i++ {
		if w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, generic); w.Code != 200 {
			t.Fatalf("valid plan %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	s, err := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if err != nil || !s.Desired.Egress.Active() || len(s.Desired.Egress.Rules) != 3 {
		t.Fatalf("plan not stored: %+v %v", s.Desired.Egress, err)
	}
	// The capable Helper receives the plan in its signed payload.
	e, err := a.flvxHelperDesired(ctx, 7, "helper-uuid", "helper-secret", "amd64", true, false, nil)
	if err != nil || e.Payload.Desired.Egress == nil || !e.Verify("helper-secret", "helper-uuid", time.Now()) {
		t.Fatal("plan not delivered", err)
	}
	// A Helper downgraded after acceptance must not route covered nodes
	// around the plan: payload without the field, covered nodes withheld.
	e, _ = a.flvxHelperDesired(ctx, 7, "helper-uuid", "helper-secret", "amd64", false, false, nil)
	encoded, _ := json.Marshal(e.Payload)
	if e.Payload.Desired.Egress != nil || strings.Contains(string(encoded), `"egress"`) || *e.Payload.Desired.Nodes[0].Enabled {
		t.Fatalf("old Helper payload: %s", encoded)
	}
	if s, _ = readFLVXShare(ctx, a.flvxDB(), 7, false); !*s.Desired.Nodes[0].Enabled {
		t.Fatal("withholding mutated the stored desired state")
	}
	// Status carries the Helper's egress state (§6).
	flvxTestReport(t, a, 3, &flvx.EgressStatus{Versions: []int{1}, GeoIP: true, State: "applied", AppliedRevision: 2, Warnings: []string{"flvxo-3: 服务器没有可用的 IPv6 地址"}})
	w = flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", token, nil)
	if !strings.Contains(w.Body.String(), `"egress":{"applied_revision":2,"error":"","state":"applied","warnings":["flvxo-3: 服务器没有可用的 IPv6 地址"]}`) {
		t.Fatalf("status %s", w.Body.String())
	}
	// Oversized Helper status is rejected rather than stored.
	r := flvx.Report{Generation: "helper-egress", Sequence: 4, Period: flvx.Period(time.Now(), 1), Status: flvx.Status{AppliedRevision: 2, Core: flvx.Core{State: "running"}, Nodes: []flvx.NodeStatus{}, Egress: &flvx.EgressStatus{State: "applied", Warnings: make([]string, 11)}}}
	if _, err = a.flvxHelperDesired(ctx, 7, "helper-uuid", "helper-secret", "amd64", true, false, &r); err == nil {
		t.Fatal("unbounded egress warnings accepted")
	}
	// Policy off: advertised as not allowed, plans refused, delivered payloads
	// fall back to the default direct outbound. A status-only edit keeps it.
	if w = flvxTestRequest(a, "PUT", "/api/custom/flvx/admin/shares/7", "operator-token", map[string]any{"status": "active", "allow_egress": false}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = flvxTestRequest(a, "PUT", "/api/custom/flvx/admin/shares/7", "operator-token", map[string]any{"status": "active"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	got, _ = json.Marshal(flvxShareItem(t, a, token)["egress"])
	if string(got) != `{"allowed":false,"geoip":true,"geosite":false,"versions":[1]}` {
		t.Fatalf("policy capability %s", got)
	}
	d.Revision, d.Egress = 3, flvxTestEgress()
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "妙妙屋共享方未允许自定义出站与路由") {
		t.Fatalf("plan with policy off: %d %s", w.Code, w.Body.String())
	}
	e, _ = a.flvxHelperDesired(ctx, 7, "helper-uuid", "helper-secret", "amd64", true, false, nil)
	if e.Payload.Desired.Egress != nil || !*e.Payload.Desired.Nodes[0].Enabled {
		t.Fatal("policy off must deliver the default plan without withholding nodes")
	}
	// Today's payload is still accepted, and the admin list summarizes the
	// stored plan without upstream addresses or credentials.
	d.Egress = nil
	if w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = flvxTestRequest(a, "GET", "/api/custom/flvx/admin", "operator-token", nil)
	if !strings.Contains(w.Body.String(), `"allow_egress":false`) || strings.Contains(w.Body.String(), "proxy.example.com") || strings.Contains(w.Body.String(), `"password"`) {
		t.Fatalf("admin list %s", w.Body.String())
	}
}
