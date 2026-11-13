package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func flvxTestVLESSEncNode(id string, port int) flvx.Node {
	enabled := true
	key := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	return flvx.Node{ID: id, Owner: flvx.Owner{ID: "7", Name: "Alice"}, Name: "VE " + id, Protocol: flvx.ProtocolVLESSEnc, Port: port, Enabled: &enabled,
		VLESSEnc: &flvx.VLESSEnc{UUID: "5783a3e7-e373-4100-a7d4-0aa7c8f6a4ab", Flow: "xtls-rprx-vision", Decryption: "mlkem768x25519plus.random.600s." + key}}
}

// flvxTestVLESSEncHelper marks the server's Helper as rendering
// landing-protocols-v1 and, with vlessEnc, VLESS Encryption (v0.7.27).
func flvxTestVLESSEncHelper(a *app, id int64, vlessEnc bool) {
	flvxTestProtocolsHelper(a, id, true)
	if !vlessEnc {
		return
	}
	key := flvxID(id)
	a.helperState.mu.Lock()
	status := a.helperState.data.AgentStatuses[key]
	status.Helper.Version = "v0.7.27"
	status.Capabilities = append(status.Capabilities, flvx.VLESSEncCapability)
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
}

// Withholding per Helper: no landing-protocols-v1 → base nodes only; v1
// without flvx.vless_enc.v1 → everything but vless_enc; both → unchanged.
func TestFLVXHelperProtocolsStripsVLESSEnc(t *testing.T) {
	ss := flvxTestDesired().Nodes[0]
	nodes := append([]flvx.Node{ss}, flvxProtocolNodes(t)...)
	nodes = append(nodes, flvxTestVLESSEncNode("51", 30007))
	d := flvx.Desired{Revision: 3, Nodes: nodes}
	ids := func(d flvx.Desired) []string {
		out := []string{}
		for _, n := range d.Nodes {
			out = append(out, n.ID)
		}
		return out
	}
	all := ids(d)
	if got := ids(flvxHelperProtocols(d, true, true)); !reflect.DeepEqual(got, all) {
		t.Fatalf("capable Helper: %v", got)
	}
	if got := ids(flvxHelperProtocols(d, true, false)); !reflect.DeepEqual(got, all[:len(all)-1]) {
		t.Fatalf("v0.7.26 Helper: %v", got)
	}
	if got := ids(flvxHelperProtocols(d, false, false)); !reflect.DeepEqual(got, []string{ss.ID}) {
		t.Fatalf("v0.7.10 Helper: %v", got)
	}
	// A stray vless_enc object on another protocol is withheld too.
	stray := ss
	stray.ID, stray.VLESSEnc = "52", flvxTestVLESSEncNode("52", 30008).VLESSEnc
	if got := ids(flvxHelperProtocols(flvx.Desired{Revision: 3, Nodes: []flvx.Node{ss, stray}}, true, false)); !reflect.DeepEqual(got, []string{ss.ID}) {
		t.Fatalf("stray object: %v", got)
	}
}

// Contract (FLVX plan 253) end to end on Custom: vless_enc is advertised
// only for a Helper with flvx.vless_enc.v1 whose Core passed the test,
// validated strictly, withheld from older Helpers, never echoed; a share
// without vless_enc nodes signs byte-identical payloads for every Helper.
func TestFLVXVLESSEncPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	db := a.flvxDB()
	auditDeleteExec(t, db, `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	c := flvx.Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50, MaxConnections: 500}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	put := func(revision int64, n ...flvx.Node) (int, string) {
		w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", "flvx_fixture", flvx.Desired{Revision: revision, Nodes: n})
		return w.Code, w.Body.String()
	}
	nodes := flvxProtocolNodes(t)
	ss, mieru, ve := flvxTestDesired().Nodes[0], nodes[2], flvxTestVLESSEncNode("51", 30007)
	v1Caps := map[string]flvx.ProtocolCap{"anytls": {UDP: true}, "snell": {UDP: true, Versions: []int{4, 5}, Obfs: []string{"none", "http", "tls"}}, "mieru": {UDP: true, Transports: []string{"TCP", "UDP"}}}
	withVE := map[string]flvx.ProtocolCap{"vless_enc": {UDP: true, Versions: []int{1}}}
	for k, v := range v1Caps {
		withVE[k] = v
	}
	report := func(seq int64, caps map[string]flvx.ProtocolCap, vlessEnc bool) {
		t.Helper()
		if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, false, false, false, false, false, vlessEnc, flvxProtocolsReport(seq, caps, &flvx.ListeningPorts{TCP: []int{}, UDP: []int{}}, 0)); err != nil {
			t.Fatal(err)
		}
	}

	// A share without vless_enc nodes: today's payload, byte for byte, for an
	// old and a new Helper.
	flvxTestVLESSEncHelper(a, 7, false)
	report(1, v1Caps, false)
	if code, body := put(1, ss, mieru); code != 200 {
		t.Fatalf("v1 nodes: %d %s", code, body)
	}
	payload := func(vlessEnc bool) (flvx.Envelope, []byte) {
		t.Helper()
		e, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, false, false, false, false, false, false, vlessEnc, nil)
		if err != nil || e == nil || !e.Verify("helper", "7", time.Now()) {
			t.Fatalf("payload: %v %+v", err, e)
		}
		raw, _ := json.Marshal(e.Payload)
		return *e, raw
	}
	_, oldRaw := payload(false)
	_, newRaw := payload(true)
	if string(oldRaw) != string(newRaw) || strings.Contains(string(newRaw), "vless_enc") {
		t.Fatalf("payload of a share without vless_enc changed:\n%s\n%s", oldRaw, newRaw)
	}

	// v0.7.26 (protocols.v1, no flvx.vless_enc.v1): even a forged vless_enc
	// cap is not advertised, and the node is refused.
	report(2, withVE, false)
	shares, _ := flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture")
	if s := shares[0]; s.ProtocolCaps == nil || !reflect.DeepEqual(s.ProtocolCaps.Protocols, v1Caps) {
		t.Fatalf("old Helper caps: %+v", s.ProtocolCaps)
	}
	if code, body := put(2, ss, mieru, ve); code != 422 || !strings.Contains(body, `"node_id":"51","field":"protocol"`) {
		t.Fatalf("vless_enc accepted without the cap: %d %s", code, body)
	}

	// New Helper, Core without VLESS Encryption: not advertised either.
	flvxTestVLESSEncHelper(a, 7, true)
	report(3, v1Caps, true)
	if code, body := put(2, ss, mieru, ve); code != 422 || !strings.Contains(body, `"field":"protocol"`) {
		t.Fatalf("vless_enc accepted on a Core without it: %d %s", code, body)
	}

	// New Helper + supporting Core: advertised with the contract cap.
	report(4, withVE, true)
	shares, listBody := flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture")
	if s := shares[0]; s.ProtocolCaps == nil || !reflect.DeepEqual(s.ProtocolCaps.Protocols["vless_enc"], flvx.ProtocolCap{UDP: true, Versions: []int{1}}) || !reflect.DeepEqual(s.Protocols, []string{"ss2022", "vless_reality_vision"}) {
		t.Fatalf("capable share: %+v", s)
	}
	if !strings.Contains(listBody, `"vless_enc":{"udp":true,"versions":[1]}`) {
		t.Fatalf("cap JSON shape: %s", listBody)
	}
	bad := ve
	bad.VLESSEnc = &flvx.VLESSEnc{UUID: ve.VLESSEnc.UUID, Flow: "", Decryption: "mlkem768x25519plus.random.600s.secretsecretsecret"}
	if code, body := put(2, ss, mieru, bad); code != 422 || !strings.Contains(body, `"field":"vless_enc.decryption"`) || strings.Contains(body, "secretsecret") {
		t.Fatalf("bad decryption: %d %s", code, body)
	}
	if code, body := put(2, ss, mieru, ve); code != 200 {
		t.Fatalf("vless_enc refused: %d %s", code, body)
	}

	// The capable Helper receives it; a v0.7.26 Helper gets the rest only,
	// signed, without the field.
	e, raw := payload(true)
	if len(e.Payload.Desired.Nodes) != 3 || e.Payload.Desired.Nodes[2].VLESSEnc == nil || e.Payload.Desired.Nodes[2].VLESSEnc.Decryption != ve.VLESSEnc.Decryption {
		t.Fatalf("capable payload: %s", raw)
	}
	e, raw = payload(false)
	if len(e.Payload.Desired.Nodes) != 2 || strings.Contains(string(raw), "vless_enc") {
		t.Fatalf("v0.7.26 payload: %s", raw)
	}

	// Never echoed to FLVX or the operator page.
	_, listBody = flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture")
	_, adminBody := flvxTestShares(t, a, "/api/custom/flvx/admin", "operator-token")
	status := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", "flvx_fixture", nil).Body.String()
	for _, body := range []string{listBody, adminBody, status} {
		if strings.Contains(body, ve.VLESSEnc.Decryption) || strings.Contains(body, "mlkem768x25519plus") {
			t.Fatalf("decryption string echoed: %s", body)
		}
	}
	if !strings.Contains(adminBody, `"protocol":"vless_enc"`) {
		t.Fatalf("admin node list lacks the protocol: %s", adminBody)
	}

	// Helper downgraded: the cap disappears at once, the node is withheld.
	flvxTestVLESSEncHelper(a, 7, false)
	shares, _ = flvxTestShares(t, a, "/api/custom/flvx/v1/shares", "flvx_fixture")
	if _, ok := shares[0].ProtocolCaps.Protocols["vless_enc"]; ok || len(shares[0].ProtocolCaps.Protocols) != 3 {
		t.Fatalf("cap kept after downgrade: %+v", shares[0].ProtocolCaps)
	}
	if e, raw := payload(false); len(e.Payload.Desired.Nodes) != 2 || strings.Contains(string(raw), "vless_enc") {
		t.Fatalf("downgraded payload: %s", raw)
	}
}
