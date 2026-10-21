package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func helperProtocolNodes(t *testing.T) []flvx.Node {
	t.Helper()
	raw, err := os.ReadFile("../../internal/flvx/testdata/landing-protocols-nodes.json")
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

// The running FLVX Core decides: a Core without /v1/features (released
// 8f5b614) gets no AnyTLS / Snell / Mieru inbound and nothing is advertised;
// the fixed Core serves all three with the contract §6 inbounds, their
// limits and machine-protection ports, and the share advertises them.
func TestFLVXExtraProtocolsFollowCoreFeatures(t *testing.T) {
	m, f := fakeFLVXManager(t)
	nodes := helperProtocolNodes(t)
	anytls, snell, mieru := nodes[0], nodes[1], nodes[2]
	m.state.Payload.Artifact = flvx.Artifact{SHA256: strings.Repeat("a", 64), Version: "8f5b614"}
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, anytls, snell, mieru)
	m.readOfficial = func() ([]int, error) { return []int{30099}, nil }
	features := []string{}
	m.readFeatures = func(context.Context) ([]string, error) { return features, nil }
	m.readListening = func() ([]uint32, []uint32, error) {
		// x-ui on 30500 (TCP+UDP); the Core's own Mieru and SS2022 sockets; outside the range.
		return []uint32{30001, 30003, 30004, 30005, 30500, 29000}, []uint32{30001, 30005, 30500, 40000}, nil
	}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.present["flvxl-one"] || len(f.present) != 1 || m.state.Report.Protocols != nil {
		t.Fatalf("old Core: present %v, protocols %v", f.present, m.state.Report.Protocols)
	}
	for _, n := range m.state.Report.Nodes {
		if n.ID != "one" && (n.State != "error" || !strings.Contains(n.Error, "upgrade the FLVX Core")) {
			t.Fatalf("extra node on an old Core: %+v", n)
		}
	}

	// The pinned Core moves to the fixed build: a new Core epoch.
	m.state.Payload.Artifact = flvx.Artifact{SHA256: strings.Repeat("b", 64), Version: "cfb0f59"}
	features = []string{coreFeatureAnyTLS, coreFeatureSnellUDP}
	f.epoch = f.epoch.Add(time.Minute)
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes[:3] {
		want, _ := json.Marshal(flvx.Inbound(n))
		if !f.present[n.Tag()] || m.state.Applied[n.Tag()] != string(want) {
			t.Fatalf("%s not applied as contract §6: %s", n.Tag(), m.state.Applied[n.Tag()])
		}
	}
	for _, n := range m.state.Report.Nodes {
		if n.State != "active" {
			t.Fatalf("node %+v", n)
		}
	}
	wantCaps := map[string]flvx.ProtocolCap{"anytls": {UDP: true}, "snell": {UDP: true, Versions: []int{4, 5}, Obfs: []string{"none", "http", "tls"}}, "mieru": {UDP: true, Transports: []string{"TCP", "UDP"}}}
	if !reflect.DeepEqual(m.state.Report.Protocols, wantCaps) {
		t.Fatalf("advertised %+v", m.state.Report.Protocols)
	}
	if want := (&flvx.ListeningPorts{TCP: []int{30500}, UDP: []int{30500}}); !reflect.DeepEqual(m.state.Report.Listening, want) {
		t.Fatalf("listening %+v", m.state.Report.Listening)
	}
	ports := m.appliedPorts()
	if !reflect.DeepEqual(ports.TCP, []uint32{30001, 30003, 30004, 30005}) || !reflect.DeepEqual(ports.UDP, []uint32{30001, 30005}) {
		t.Fatalf("machine protection ports %+v (Mieru needs TCP and UDP, AnyTLS / Snell TCP)", ports)
	}
	limits := map[string]bool{}
	for _, p := range f.limits.PortLimits {
		limits[p.InboundTag] = true
	}
	if !limits["flvxl-41"] || !limits["flvxl-42"] || !limits["flvxl-43"] {
		t.Fatalf("per-node limits missing: %+v", f.limits.PortLimits)
	}

	// Disabling an AnyTLS node blocks its identity, then removes the inbound.
	no := false
	m.state.Payload.Desired.Nodes[1].Enabled = &no
	m.state.Payload.Desired.Revision++
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.present["flvxl-41"] || m.state.Applied["flvxl-41"] != "" {
		t.Fatal("disabled AnyTLS node still served")
	}

	// The features survive a Helper restart (persisted with the build hash).
	raw, err := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved flvxLocalState
	if err = json.Unmarshal(raw, &saved); err != nil || saved.CoreFeatures == nil || saved.CoreFeatures.SHA256 != strings.Repeat("b", 64) || len(saved.CoreFeatures.Features) != 2 {
		t.Fatalf("persisted features %+v: %v", saved.CoreFeatures, err)
	}
}

// Snell UDP is advertised only with its fix; AnyTLS only with its fix.
func TestFLVXProtocolCapsFromFeatures(t *testing.T) {
	if flvxProtocolCaps(nil) != nil || flvxProtocolCaps(map[string]bool{}) != nil {
		t.Fatal("a Core without /v1/features must advertise nothing")
	}
	caps := flvxProtocolCaps(map[string]bool{"future.feature": true})
	if _, ok := caps["anytls"]; ok || caps["snell"].UDP || !caps["mieru"].UDP {
		t.Fatalf("partial features: %+v", caps)
	}
	if flvxProtocolProblem("ss2022", nil) != "" || flvxProtocolProblem("anytls", map[string]bool{coreFeatureSnellUDP: true}) == "" || flvxProtocolProblem("snell", map[string]bool{coreFeatureSnellUDP: true}) != "" {
		t.Fatal("render gate")
	}
	found := false
	for _, c := range helperCapabilities {
		found = found || c == "flvx.protocols.v1"
	}
	if !found {
		t.Fatal("Helper does not announce flvx.protocols.v1")
	}
}

// /v1/features: old Cores answer 404 (no features), errors are retried.
func TestCoreClientFeatures(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "c.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	status := http.StatusNotFound
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/features" || status != http.StatusOK {
			http.Error(w, "x", status)
			return
		}
		w.Write([]byte(`{"features":["anytls.downlink_stats","snell.udp_per_packet_target"]}`))
	})}
	go server.Serve(l)
	defer server.Close()
	client := newCoreClient(socket)
	if got, err := client.features(context.Background()); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("404: %v %v", got, err)
	}
	status = http.StatusOK
	if got, err := client.features(context.Background()); err != nil || !reflect.DeepEqual(got, []string{coreFeatureAnyTLS, coreFeatureSnellUDP}) {
		t.Fatalf("200: %v %v", got, err)
	}
	status = http.StatusServiceUnavailable
	if _, err := client.features(context.Background()); err == nil {
		t.Fatal("503 must be an error (retried), not 'no features'")
	}
}

// A signed payload with extra-protocol nodes is accepted by a v0.7.11 Helper.
func TestFLVXAcceptsExtraProtocolPayload(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	p := m.state.Payload
	p.ServerID, p.Version = "7", 2
	p.Desired.Revision = 2
	p.Desired.Nodes = append(p.Desired.Nodes, helperProtocolNodes(t)...)
	e := flvx.Envelope{Payload: p, ExpiresAt: time.Now().Add(5 * time.Minute).UnixMilli()}
	e.Sign("helper")
	if err := m.accept(e); err != nil {
		t.Fatal(err)
	}
	if len(m.state.Payload.Desired.Nodes) != 5 {
		t.Fatalf("accepted %d nodes", len(m.state.Payload.Desired.Nodes))
	}
}

// Ports of this Core never count as busy, and a change of its own inbounds
// rescans at once.
func TestFLVXListeningExcludesOwnPorts(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	scans := 0
	m.readListening = func() ([]uint32, []uint32, error) { scans++; return []uint32{30001, 30200}, []uint32{30001}, nil }
	if l := m.listening(); !reflect.DeepEqual(l, &flvx.ListeningPorts{TCP: []int{30001, 30200}, UDP: []int{30001}}) {
		t.Fatalf("before apply: %+v", l)
	}
	m.listening()
	if scans != 1 {
		t.Fatalf("rescanned within the refresh window: %d", scans)
	}
	m.state.Applied["flvxl-one"] = "{}"
	if l := m.listening(); scans != 2 || !reflect.DeepEqual(l, &flvx.ListeningPorts{TCP: []int{30200}, UDP: []int{}}) {
		t.Fatalf("after apply (%d scans): %+v", scans, l)
	}
}
