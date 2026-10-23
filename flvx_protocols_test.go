package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func flvxProtocolNodes(t *testing.T) []flvx.Node {
	t.Helper()
	raw, err := os.ReadFile("internal/flvx/testdata/landing-protocols-nodes.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Nodes []flvx.Node `json:"nodes"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture.Nodes
}

// flvxTestProtocolsHelper marks the server's Helper as rendering
// landing-protocols-v1 (or, with protocols=false, as a v0.7.10 Helper).
func flvxTestProtocolsHelper(a *app, id int64, protocols bool) {
	key := flvxID(id)
	status := agentStatus{Helper: componentStatus{Installed: true, Version: "v0.7.10"}, Architecture: "amd64", Capabilities: []string{"helper.update", "flvx.share", "flvx.egress.v1"}}
	if protocols {
		status.Helper.Version = "v0.7.11"
		status.Capabilities = append(status.Capabilities, "flvx.protocols.v1")
	}
	a.helperState.mu.Lock()
	a.helperState.data.Servers[key] = helperServerIdentity{OfficialServerID: key, HelperTokenHash: strings.Repeat("a", 64)}
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
}

type flvxTestShareItem struct {
	ServerID     int64              `json:"server_id"`
	Protocols    []string           `json:"protocols"`
	ProtocolCaps *flvx.ProtocolCaps `json:"protocol_caps"`
	ServerMode   string             `json:"server_mode"`
	BusyPorts    []int              `json:"busy_ports"`
	CorePin      map[string]any     `json:"core_pin"`
}

func flvxTestShares(t *testing.T, a *app, path, token string) ([]flvxTestShareItem, string) {
	t.Helper()
	w := flvxTestRequest(a, "GET", path, token, nil)
	var list struct {
		Shares []flvxTestShareItem `json:"shares"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Shares) != 1 {
		t.Fatalf("list %s: %d %s", path, w.Code, w.Body.String())
	}
	return list.Shares, w.Body.String()
}

func flvxProtocolsReport(seq int64, protocols map[string]flvx.ProtocolCap, listening *flvx.ListeningPorts, applied int64) *flvx.Report {
	return &flvx.Report{Generation: "g", Sequence: seq, Period: flvx.Period(time.Now(), 1), Status: flvx.Status{AppliedRevision: applied, Core: flvx.Core{State: "running"}, Nodes: []flvx.NodeStatus{}, CollectedAt: time.Now().UnixMilli(), Protocols: protocols, Listening: listening}}
}

// Contract landing-protocols-v1 end to end on Custom: capability, server
// mode and busy ports in the share list; strict validation of the new node
// objects; old Helpers never receive them; no secret is echoed.
func TestFLVXLandingProtocolsPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	db := a.flvxDB()
	auditDeleteExec(t, db, `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	auditDeleteExec(t, db, `ALTER TABLE remote_servers ADD COLUMN xray_mode text`)
	auditDeleteExec(t, db, `UPDATE remote_servers SET xray_mode='embedded' WHERE id=7`)
	c := flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50, MaxConnections: 500}
	flvxTestProtocolsHelper(a, 7, false)
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal("an Embedded server must be shareable", err)
	}
	nodes := flvxProtocolNodes(t)
	anytls, snell, mieru := nodes[0], nodes[1], nodes[2]
	put := func(revision int64, n ...flvx.Node) (int, string) {
		w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", "flvx_fixture", flvx.Desired{Revision: revision, Nodes: n})
		return w.Code, w.Body.String()
	}

	// v0.7.10 Helper: nothing advertised, extra protocols refused.
	shares, _ := flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture")
	if s := shares[0]; s.ProtocolCaps != nil || s.ServerMode != "embedded" || !reflect.DeepEqual(s.Protocols, []string{"ss2022", "vless_reality_vision"}) || !reflect.DeepEqual(s.BusyPorts, []int{30002}) {
		t.Fatalf("old Helper share: %+v", s)
	}
	if code, body := put(1, anytls); code != 422 || !strings.Contains(body, `"field":"protocol"`) {
		t.Fatalf("AnyTLS accepted without capability: %d %s", code, body)
	}

	// v0.7.11 Helper whose FLVX Core has the fixes reports the capability and
	// what else listens on the machine.
	flvxTestProtocolsHelper(a, 7, true)
	caps := map[string]flvx.ProtocolCap{"anytls": {UDP: true}, "snell": {UDP: true, Versions: []int{4, 5}, Obfs: []string{"none", "http", "tls"}}, "mieru": {UDP: true, Transports: []string{"TCP", "UDP"}}, "socks": {UDP: true}}
	listening := &flvx.ListeningPorts{TCP: []int{30010, 30999}, UDP: []int{30011}}
	if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, flvxProtocolsReport(1, caps, listening, 0)); err != nil {
		t.Fatal(err)
	}
	shares, _ = flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture")
	delete(caps, "socks")
	if s := shares[0]; s.ProtocolCaps == nil || !reflect.DeepEqual(s.ProtocolCaps.Versions, []int{1}) || !reflect.DeepEqual(s.ProtocolCaps.Protocols, caps) || !reflect.DeepEqual(s.BusyPorts, []int{30002, 30010, 30011, 30999}) || len(s.Protocols) != 2 {
		t.Fatalf("capable share: %+v", s)
	}

	ss := flvxTestDesired().Nodes[0]
	if code, body := put(1, ss, anytls, snell, mieru, nodes[3]); code != 200 {
		t.Fatalf("contract nodes refused: %d %s", code, body)
	}
	// Busy ports: TCP blocks every protocol, UDP only Mieru / SS2022.
	busyTCP, busyUDP, snellOnUDP := anytls, mieru, snell
	busyTCP.Port, busyUDP.Port, snellOnUDP.Port = 30010, 30011, 30011
	if code, body := put(2, ss, busyTCP); code != 422 || !strings.Contains(body, `"node_id":"41","field":"port"`) {
		t.Fatalf("AnyTLS on a busy TCP port: %d %s", code, body)
	}
	if code, body := put(2, ss, busyUDP); code != 422 || !strings.Contains(body, `"node_id":"43","field":"port"`) {
		t.Fatalf("Mieru on a busy UDP port: %d %s", code, body)
	}
	if code, body := put(2, ss, anytls, snellOnUDP, mieru); code != 200 {
		t.Fatalf("Snell (TCP) next to a UDP-only listener: %d %s", code, body)
	}
	// A node keeps its port when the Helper later sees it listening (own port).
	if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, flvxProtocolsReport(2, caps, &flvx.ListeningPorts{TCP: []int{30003, 30010}, UDP: []int{30005}}, 2)); err != nil {
		t.Fatal(err)
	}
	if code, body := put(3, ss, anytls, snellOnUDP, mieru); code != 200 {
		t.Fatalf("own ports reported as busy: %d %s", code, body)
	}
	shares, listBody := flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture")
	if !reflect.DeepEqual(shares[0].BusyPorts, []int{30002, 30010}) {
		t.Fatalf("busy_ports must exclude the share's own nodes: %v", shares[0].BusyPorts)
	}
	_, adminBody := flvxTestShares(t, a, "/api/custom/flvx/admin", "operator-token")
	status := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", "flvx_fixture", nil).Body.String()
	for _, body := range []string{listBody, adminBody, status} {
		for _, secret := range []string{"PRIVATE KEY", anytls.AnyTLS.Password, snell.Snell.PSK, mieru.Mieru.Password} {
			if strings.Contains(body, secret) {
				t.Fatalf("secret %q echoed: %s", secret[:8], body)
			}
		}
	}

	// The capable Helper receives the nodes, a v0.7.10 Helper never does
	// (it would reject the whole signed payload on the unknown fields).
	e, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, nil)
	if err != nil || len(e.Payload.Desired.Nodes) != 4 {
		t.Fatalf("capable payload: %v %+v", err, e)
	}
	e, err = a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, false, false, nil)
	if err != nil || len(e.Payload.Desired.Nodes) != 1 || e.Payload.Desired.Nodes[0].ID != ss.ID || !e.Verify("helper", "7", time.Now()) {
		t.Fatalf("old Helper payload: %v %+v", err, e)
	}
	raw, _ := json.Marshal(e.Payload)
	for _, key := range []string{`"anytls"`, `"snell"`, `"mieru"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("old Helper payload carries %s", key)
		}
	}

	// Capability withdrawn (Helper downgraded): caps disappear at once.
	flvxTestProtocolsHelper(a, 7, false)
	if shares, _ = flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture"); shares[0].ProtocolCaps != nil {
		t.Fatalf("caps kept for a Helper without flvx.protocols.v1: %+v", shares[0])
	}
	if code, body := put(4, ss, anytls); code != 422 || !strings.Contains(body, `"field":"protocol"`) {
		t.Fatalf("new extra node accepted after withdrawal: %d %s", code, body)
	}
	// The admin can still pause and resume the share holding those nodes.
	if err := a.saveFLVXShare(ctx, 7, c, "paused", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal("resume with stored extra nodes", err)
	}
	// Unknown fields are still a clean, whole rejection.
	w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", "flvx_fixture", map[string]any{"revision": 4, "nodes": []any{map[string]any{"id": "9", "protocol": "anytls", "anytls": map[string]any{"password": "x", "obfs": "y"}}}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"field":"desired"`) {
		t.Fatalf("unknown nested field: %d %s", w.Code, w.Body.String())
	}
}

// Publishing a release must not replace the FLVX Core of an existing share:
// the artifact is pinned at the first payload and moves only on request.
func TestFLVXCorePinPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	release := func(version, sha string) {
		a.releaseCache = &releaseCache{info: cachedReleaseInfo{CoreVersion: version, CoreArtifacts: map[string]releaseArtifact{
			"amd64": {URL: "https://github.com/ImoLR/mmwx-custom/releases/download/x/mmwxc-core-linux-amd64", SHA256: strings.Repeat(sha, 64)},
			"arm64": {URL: "https://github.com/ImoLR/mmwx-custom/releases/download/x/mmwxc-core-linux-arm64", SHA256: strings.Repeat("c", 64)}}}}
	}
	release("8f5b614", "a")
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	payload := func() flvx.Payload {
		t.Helper()
		e, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, nil)
		if err != nil || e == nil {
			t.Fatal(err)
		}
		return e.Payload
	}
	if p := payload(); p.Artifact.Version != "8f5b614" || p.Artifact.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("first payload artifact %+v", p.Artifact)
	}
	release("cfb0f59", "b")
	p := payload()
	if p.Artifact.Version != "8f5b614" || p.Artifact.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("a new release replaced the pinned FLVX Core: %+v", p.Artifact)
	}
	shares, _ := flvxTestShares(t, a, "/api/custom/flvx/admin", "operator-token")
	if pin := shares[0].CorePin; pin["pinned"] != "8f5b614" || pin["latest"] != "cfb0f59" || pin["upgradable"] != true {
		t.Fatalf("core pin summary %+v", pin)
	}
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/shares/7/core", "operator-token", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"cfb0f59"`) {
		t.Fatalf("upgrade: %d %s", w.Code, w.Body.String())
	}
	upgraded := payload()
	if upgraded.Artifact.Version != "cfb0f59" || upgraded.Artifact.SHA256 != strings.Repeat("b", 64) || upgraded.Version <= p.Version {
		t.Fatalf("upgraded payload %+v (version %d → %d)", upgraded.Artifact, p.Version, upgraded.Version)
	}
	if shares, _ = flvxTestShares(t, a, "/api/custom/flvx/admin", "operator-token"); shares[0].CorePin["upgradable"] != false {
		t.Fatalf("pin still upgradable: %+v", shares[0].CorePin)
	}
	if w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/shares/8/core", "operator-token", nil); w.Code != 404 {
		t.Fatalf("upgrade of a missing share: %d %s", w.Code, w.Body.String())
	}
	if err := a.saveFLVXShare(ctx, 7, c, "revoked", nil); err != nil {
		t.Fatal(err)
	}
	release("1111111", "d")
	if w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/shares/7/core", "operator-token", nil); w.Code != 409 {
		t.Fatalf("upgrade of a revoked share: %d %s", w.Code, w.Body.String())
	}
}

// A Helper that cannot start the shared Core (e.g. a parent directory the
// FLVX Core account cannot enter) reports why; the operator's share card shows
// it, FLVX never sees it.
func TestFLVXCoreErrorOperatorOnlyPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	auditDeleteExec(t, a.flvxDB(), `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	if err := a.saveFLVXShare(ctx, 7, flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}, "active", nil); err != nil {
		t.Fatal(err)
	}
	reason := "FLVX Core 账户 mmwxc-flvx 无法访问 /opt/mmwxc（0700 root:root）：目录带 ACL，Helper 不改动"
	report := flvxProtocolsReport(1, nil, nil, 0)
	report.Core = flvx.Core{State: "error", Error: reason + "\n\x1b[31m" + strings.Repeat("长", 400)}
	if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, report); err != nil {
		t.Fatal(err)
	}
	var admin struct {
		Shares []struct {
			CoreError string `json:"core_error"`
		} `json:"shares"`
	}
	w := flvxTestRequest(a, "GET", "/api/custom/flvx/admin", "operator-token", nil)
	if json.Unmarshal(w.Body.Bytes(), &admin) != nil || len(admin.Shares) != 1 {
		t.Fatalf("admin list: %s", w.Body.String())
	}
	got := admin.Shares[0].CoreError
	if !strings.HasPrefix(got, reason+"  [31m") || len([]rune(got)) != 300 || strings.ContainsAny(got, "\n\x1b") {
		t.Fatalf("core_error not one bounded line: %q", got)
	}
	_, list := flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture")
	status := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", "flvx_fixture", nil).Body.String()
	for _, body := range []string{list, status} {
		if strings.Contains(body, "mmwxc-flvx") || strings.Contains(body, "core_error") {
			t.Fatalf("FLVX sees the operator-only reason: %s", body)
		}
	}
	// A later healthy report clears it.
	report = flvxProtocolsReport(2, nil, nil, 0)
	if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, report); err != nil {
		t.Fatal(err)
	}
	if w = flvxTestRequest(a, "GET", "/api/custom/flvx/admin", "operator-token", nil); strings.Contains(w.Body.String(), "core_error") {
		t.Fatalf("stale core_error: %s", w.Body.String())
	}
}
