package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func helperVLESSEncNode(id string, port int) flvx.Node {
	enabled := true
	key := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	return flvx.Node{ID: id, Owner: flvx.Owner{ID: "7", Name: "Alice"}, Name: "VE " + id, Protocol: flvx.ProtocolVLESSEnc, Port: port, Enabled: &enabled, MaxConnections: 3, MaxOnlineIPs: 2,
		VLESSEnc: &flvx.VLESSEnc{UUID: "5783a3e7-e373-4100-a7d4-0aa7c8f6a4ab", Flow: "xtls-rprx-vision", Decryption: "mlkem768x25519plus.random.600s." + key}}
}

// vless_enc is advertised only after the Core build passed the config test,
// with the contract cap {"udp": true, "versions": [1]}.
func TestFLVXVLESSEncCapFromConfigTest(t *testing.T) {
	base := map[string]bool{coreFeatureAnyTLS: true, coreFeatureSnellUDP: true}
	if _, ok := flvxProtocolCaps(base)[flvx.ProtocolVLESSEnc]; ok {
		t.Fatal("vless_enc advertised without the config test")
	}
	if flvxProtocolProblem(flvx.ProtocolVLESSEnc, base) != "FLVX Core does not support VLESS Encryption; upgrade the FLVX Core" {
		t.Fatalf("problem %q", flvxProtocolProblem(flvx.ProtocolVLESSEnc, base))
	}
	base[coreFeatureVLESSEnc] = true
	if got := flvxProtocolCaps(base)[flvx.ProtocolVLESSEnc]; !reflect.DeepEqual(got, flvx.ProtocolCap{UDP: true, Versions: []int{1}}) {
		t.Fatalf("cap %+v", got)
	}
	if flvxProtocolProblem(flvx.ProtocolVLESSEnc, base) != "" || flvxProtocolProblem("vless_reality_vision", nil) != "" {
		t.Fatal("render gate")
	}
	found := false
	for _, c := range helperCapabilities {
		found = found || c == flvx.VLESSEncCapability
	}
	if !found {
		t.Fatal("Helper does not announce flvx.vless_enc.v1")
	}
}

// The config test runs once per Core build: not again on a Core restart or
// a Helper restart, again for a new build, and retried only when it could not
// run. A Core without /v1/features is never tested.
func TestFLVXVLESSEncTestedOncePerBuild(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	m.state.Payload.Artifact = flvx.Artifact{SHA256: strings.Repeat("a", 64), Version: "f6f5844"}
	features := []string{coreFeatureAnyTLS, coreFeatureSnellUDP}
	reads, tests := 0, 0
	result, failure := true, error(nil)
	m.readFeatures = func(context.Context) ([]string, error) { reads++; return features, nil }
	m.testVLESSEnc = func(context.Context) (bool, error) { tests++; return result, failure }
	ctx := context.Background()
	m.detectFeatures(ctx, "e1")
	m.detectFeatures(ctx, "e1")
	if reads != 1 || tests != 1 || !m.coreFeatures()[coreFeatureVLESSEnc] {
		t.Fatalf("first build: %d reads, %d tests, features %v", reads, tests, m.coreFeatures())
	}
	m.detectFeatures(ctx, "e2") // Core restart, same build
	m.featuresEpoch = ""        // Helper restart (persisted state kept)
	m.detectFeatures(ctx, "e2")
	if reads != 3 || tests != 1 || !m.coreFeatures()[coreFeatureVLESSEnc] {
		t.Fatalf("same build retested: %d reads, %d tests", reads, tests)
	}
	m.state.Payload.Artifact.SHA256 = strings.Repeat("b", 64) // new build, rejects it
	result = false
	m.detectFeatures(ctx, "e3")
	if tests != 2 || m.coreFeatures()[coreFeatureVLESSEnc] || m.state.CoreFeatures.VLESSEnc == nil || *m.state.CoreFeatures.VLESSEnc {
		t.Fatalf("rejecting build: %d tests, %+v", tests, m.state.CoreFeatures)
	}
	if _, ok := flvxProtocolCaps(m.coreFeatures())[flvx.ProtocolVLESSEnc]; ok {
		t.Fatal("advertised on a build that rejects it")
	}
	m.state.Payload.Artifact.SHA256 = strings.Repeat("c", 64) // the test cannot run
	failure = errors.New("signal: killed")
	m.detectFeatures(ctx, "e4")
	m.detectFeatures(ctx, "e4")
	if tests != 3 || m.state.CoreFeatures.VLESSEnc != nil {
		t.Fatalf("unknown result cached or retried within the epoch: %d tests, %+v", tests, m.state.CoreFeatures)
	}
	failure, result = nil, true
	m.detectFeatures(ctx, "e5") // next Core start retries
	if tests != 4 || !m.coreFeatures()[coreFeatureVLESSEnc] {
		t.Fatalf("retry: %d tests", tests)
	}
	m.state.Payload.Artifact.SHA256 = strings.Repeat("d", 64) // Core without /v1/features
	features = []string{}
	m.detectFeatures(ctx, "e6")
	if tests != 4 || m.coreFeatures() != nil {
		t.Fatalf("old Core tested: %d tests", tests)
	}
}

// End to end through reconcile: a supporting Core serves the node with the
// rendered inbound, its limits and a TCP-only port, and the share advertises
// vless_enc; a rejecting build keeps the node out with a reason.
func TestFLVXVLESSEncReconcile(t *testing.T) {
	m, f := fakeFLVXManager(t)
	node := helperVLESSEncNode("51", 30007)
	m.state.Payload.Artifact = flvx.Artifact{SHA256: strings.Repeat("a", 64), Version: "f6f5844"}
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, node)
	m.readOfficial = func() ([]int, error) { return []int{30099}, nil }
	m.readFeatures = func(context.Context) ([]string, error) { return []string{coreFeatureAnyTLS, coreFeatureSnellUDP}, nil }
	m.testVLESSEnc = func(context.Context) (bool, error) { return false, nil }
	m.readListening = func() ([]uint32, []uint32, error) { return nil, nil, nil }
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.present[node.Tag()] {
		t.Fatal("vless_enc served by a build that rejects it")
	}
	for _, s := range m.state.Report.Nodes {
		if s.ID == "51" && (s.State != "error" || !strings.Contains(s.Error, "VLESS Encryption")) {
			t.Fatalf("status %+v", s)
		}
	}
	if _, ok := m.state.Report.Protocols[flvx.ProtocolVLESSEnc]; ok {
		t.Fatalf("advertised %+v", m.state.Report.Protocols)
	}

	m.state.Payload.Artifact.SHA256 = strings.Repeat("b", 64)
	m.testVLESSEnc = func(context.Context) (bool, error) { return true, nil }
	f.epoch = f.epoch.Add(time.Minute)
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(flvx.Inbound(node))
	if !f.present[node.Tag()] || m.state.Applied[node.Tag()] != string(want) {
		t.Fatalf("not applied as rendered: %s", m.state.Applied[node.Tag()])
	}
	for _, s := range m.state.Report.Nodes {
		if s.State != "active" {
			t.Fatalf("status %+v", s)
		}
	}
	if got := m.state.Report.Protocols[flvx.ProtocolVLESSEnc]; !reflect.DeepEqual(got, flvx.ProtocolCap{UDP: true, Versions: []int{1}}) {
		t.Fatalf("advertised %+v", m.state.Report.Protocols)
	}
	ports := m.appliedPorts()
	if !reflect.DeepEqual(ports.TCP, []uint32{30001, 30007}) || !reflect.DeepEqual(ports.UDP, []uint32{30001}) {
		t.Fatalf("machine protection ports %+v (vless_enc is TCP only)", ports)
	}
	limited := false
	for _, p := range f.limits.PortLimits {
		limited = limited || p.InboundTag == node.Tag()
	}
	if !limited {
		t.Fatalf("node limits missing: %+v", f.limits.PortLimits)
	}

	// Disable: the identity is blocked, then the inbound removed.
	no := false
	m.state.Payload.Desired.Nodes[1].Enabled = &no
	m.state.Payload.Desired.Revision++
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.present[node.Tag()] || m.state.Applied[node.Tag()] != "" {
		t.Fatal("disabled vless_enc node still served")
	}
	raw, err := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved flvxLocalState
	if err = json.Unmarshal(raw, &saved); err != nil || saved.CoreFeatures == nil || saved.CoreFeatures.VLESSEnc == nil || !*saved.CoreFeatures.VLESSEnc {
		t.Fatalf("persisted %+v: %v", saved.CoreFeatures, err)
	}
}

// A signed payload carrying a vless_enc node verifies on this Helper.
func TestFLVXAcceptsVLESSEncPayload(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	p := m.state.Payload
	p.ServerID, p.Version = "7", 2
	p.Desired.Revision = 2
	p.Desired.Nodes = append(p.Desired.Nodes, helperVLESSEncNode("51", 30007))
	e := flvx.Envelope{Payload: p, ExpiresAt: time.Now().Add(5 * time.Minute).UnixMilli()}
	e.Sign("helper")
	if err := m.accept(e); err != nil {
		t.Fatal(err)
	}
	if n := m.state.Payload.Desired.Nodes; len(n) != 2 || n[1].VLESSEnc == nil || n[1].VLESSEnc.Decryption != p.Desired.Nodes[1].VLESSEnc.Decryption {
		t.Fatalf("accepted %+v", n)
	}
}

// The config test itself: exit 0 = supported, a config rejection (exit > 0)
// = unsupported, a killed run = unknown. With the real FLVX Core it must pass.
func TestFLVXVLESSEncConfigTest(t *testing.T) {
	dir := t.TempDir()
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	ctx := context.Background()
	captured := filepath.Join(dir, "stdin.json")
	m := &flvxManager{binary: script("ok", `[ "$1 $2 $3 $4" = "run -test -config stdin:" ] || exit 9; cat > `+captured)}
	if ok, err := m.vlessEncSupported(ctx); !ok || err != nil {
		t.Fatalf("ok: %v %v", ok, err)
	}
	var config struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	raw, _ := os.ReadFile(captured)
	if json.Unmarshal(raw, &config) != nil || len(config.Inbounds) != 1 || config.Inbounds[0]["protocol"] != "vless" || !strings.Contains(string(raw), `"decryption":"mlkem768x25519plus.random.600s.`) || !strings.Contains(string(raw), `"flow":"xtls-rprx-vision"`) {
		t.Fatalf("test config %s", raw)
	}
	m.binary = script("reject", "cat >/dev/null; exit 23")
	if ok, err := m.vlessEncSupported(ctx); ok || err != nil {
		t.Fatalf("reject: %v %v", ok, err)
	}
	m.binary = script("killed", "kill -9 $$")
	if ok, err := m.vlessEncSupported(ctx); ok || err == nil {
		t.Fatalf("killed: %v %v", ok, err)
	}
	m.binary = filepath.Join(dir, "missing")
	if _, err := m.vlessEncSupported(ctx); err == nil {
		t.Fatal("missing binary must be unknown")
	}
	core := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED")
	if core == "" {
		t.Skip("set MMWXC_TEST_FLVX_CORE_FIXED for the real Core")
	}
	m.binary = core
	start := time.Now()
	if ok, err := m.vlessEncSupported(ctx); !ok || err != nil {
		t.Fatalf("real Core: %v %v", ok, err)
	}
	t.Logf("real Core config test took %v", time.Since(start))
}
