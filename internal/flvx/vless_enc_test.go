package flvx

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var (
	testVLESSEncKey32 = base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	testVLESSEncKey64 = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("0123456789abcdef", 4)))
)

func vlessEncNode() Node {
	enabled := true
	return Node{ID: "51", Owner: Owner{ID: "7", Name: "Alice"}, Name: "VE", Protocol: ProtocolVLESSEnc, Port: 30007, Enabled: &enabled,
		VLESSEnc: &VLESSEnc{UUID: "5783a3e7-e373-4100-a7d4-0aa7c8f6a4ab", Flow: "xtls-rprx-vision", Decryption: "mlkem768x25519plus.random.600s." + testVLESSEncKey32}}
}

var vlessEncCaps = map[string]ProtocolCap{ProtocolVLESSEnc: {UDP: true, Versions: []int{VLESSEncVersion}}}

// Contract (FLVX plan 253): uuid, flow "" | xtls-rprx-vision and the server
// decryption string vlessenc emits; every failure on its field path, and the
// secret never appears in a message.
func TestVLESSEncValidation(t *testing.T) {
	shareRange := Config{PortMin: 30000, PortMax: 30999, ResetDay: 1}
	options := DesiredOptions{Protocols: vlessEncCaps}
	valid := map[string]func(*Node){
		"vision random":    func(*Node) {},
		"no flow":          func(n *Node) { n.VLESSEnc.Flow = "" },
		"native 0s":        func(n *Node) { n.VLESSEnc.Decryption = "mlkem768x25519plus.native.0s." + testVLESSEncKey32 },
		"xorpub range":     func(n *Node) { n.VLESSEnc.Decryption = "mlkem768x25519plus.xorpub.300-600s." + testVLESSEncKey32 },
		"ML-KEM-768 seed":  func(n *Node) { n.VLESSEnc.Decryption = "mlkem768x25519plus.random.600s." + testVLESSEncKey64 },
		"default versions": func(*Node) { options.Protocols = map[string]ProtocolCap{ProtocolVLESSEnc: {UDP: true}} },
	}
	for name, mutate := range valid {
		n := vlessEncNode()
		mutate(&n)
		if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{n}}, shareRange, nil, options); len(e) > 0 {
			t.Fatalf("%s rejected: %+v", name, e)
		}
		options.Protocols = vlessEncCaps
	}
	bad := map[string]string{
		"none":             "none",
		"empty":            "",
		"other prefix":     "mlkem768x25519.random.600s." + testVLESSEncKey32,
		"mode":             "mlkem768x25519plus.bogus.600s." + testVLESSEncKey32,
		"ticket no unit":   "mlkem768x25519plus.random.600." + testVLESSEncKey32,
		"client 0rtt":      "mlkem768x25519plus.random.0rtt." + testVLESSEncKey32,
		"padding segment":  "mlkem768x25519plus.random.600s.100-111-1111." + testVLESSEncKey32,
		"two keys":         "mlkem768x25519plus.random.600s." + testVLESSEncKey32 + "." + testVLESSEncKey32,
		"no key":           "mlkem768x25519plus.random.600s",
		"padded base64":    "mlkem768x25519plus.random.600s." + base64.URLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcde")),
		"std base64":       "mlkem768x25519plus.random.600s." + strings.ReplaceAll(testVLESSEncKey32, "M", "+"),
		"31 bytes":         "mlkem768x25519plus.random.600s." + base64.RawURLEncoding.EncodeToString(make([]byte, 31)),
		"non-canonical":    "mlkem768x25519plus.random.600s." + testVLESSEncKey32[:42] + "x",
		"oversize":         "mlkem768x25519plus.random." + strings.Repeat("6", 300) + "s." + testVLESSEncKey32,
		"control in key":   "mlkem768x25519plus.random.600s." + testVLESSEncKey32[:40] + "\n" + testVLESSEncKey32[41:],
		"upper-case mode":  "mlkem768x25519plus.RANDOM.600s." + testVLESSEncKey32,
		"ticket long":      "mlkem768x25519plus.random.6000000s." + testVLESSEncKey32,
		"ticket negative":  "mlkem768x25519plus.random.-600s." + testVLESSEncKey32,
		"leading dot":      ".mlkem768x25519plus.random.600s." + testVLESSEncKey32,
		"trailing garbage": "mlkem768x25519plus.random.600s." + testVLESSEncKey32 + " ",
	}
	for name, decryption := range bad {
		n := vlessEncNode()
		n.VLESSEnc.Decryption = decryption
		e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{n}}, shareRange, nil, options)
		if len(e) != 1 || e[0].Field != "vless_enc.decryption" || e[0].NodeID != "51" {
			t.Fatalf("%s: errors %+v", name, e)
		}
		if decryption != "" && strings.Contains(e[0].Message, decryption) || strings.Contains(e[0].Message, testVLESSEncKey32) {
			t.Fatalf("%s: the message echoes the secret: %q", name, e[0].Message)
		}
	}
	cases := []struct {
		name, field string
		mutate      func(*Node)
		protocols   map[string]ProtocolCap
	}{
		{"uuid", "vless_enc.uuid", func(n *Node) { n.VLESSEnc.UUID = "not-a-uuid" }, vlessEncCaps},
		{"flow", "vless_enc.flow", func(n *Node) { n.VLESSEnc.Flow = "xtls-rprx-direct" }, vlessEncCaps},
		{"object missing", "vless_enc", func(n *Node) { n.VLESSEnc = nil }, vlessEncCaps},
		{"Reality object too", "vless_enc", func(n *Node) { n.VLESS = &VLESS{} }, vlessEncCaps},
		{"version outside cap", "vless_enc", func(*Node) {}, map[string]ProtocolCap{ProtocolVLESSEnc: {UDP: true, Versions: []int{2}}}},
		{"no capability", "protocol", func(*Node) {}, map[string]ProtocolCap{ProtocolMieru: {UDP: true}}},
		{"v1-only share", "protocol", func(*Node) {}, nil},
		{"Reality node with the object", "vless", func(n *Node) { n.Protocol = "vless_reality_vision" }, vlessEncCaps},
	}
	for _, c := range cases {
		n := vlessEncNode()
		c.mutate(&n)
		e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{n}}, shareRange, nil, DesiredOptions{Protocols: c.protocols})
		if len(e) != 1 || e[0].Field != c.field {
			t.Fatalf("%s: errors %+v, want field %q", c.name, e, c.field)
		}
	}
}

// The inbound handed to the FLVX Core: VLESS on raw TCP without security,
// the decryption string per inbound, the client tagged like every FLVX node.
func TestVLESSEncRenderGolden(t *testing.T) {
	n := vlessEncNode()
	raw, _ := json.Marshal(Inbound(n))
	want := `{"listen":"0.0.0.0","port":30007,"protocol":"vless","settings":{"clients":[{"email":"flvxl-51","flow":"xtls-rprx-vision","id":"5783a3e7-e373-4100-a7d4-0aa7c8f6a4ab"}],"decryption":"mlkem768x25519plus.random.600s.` + testVLESSEncKey32 + `"},"sniffing":{"destOverride":["http","tls"],"domainsExcluded":["regexp:.*"],"enabled":true,"routeOnly":true},"streamSettings":{"network":"tcp","security":"none"},"tag":"flvxl-51"}`
	if string(raw) != want {
		t.Fatalf("rendered\n%s\nwant\n%s", raw, want)
	}
	n.VLESSEnc.Flow = ""
	raw, _ = json.Marshal(Inbound(n))
	if strings.Contains(string(raw), `"flow"`) || !strings.Contains(string(raw), `"clients":[{"email":"flvxl-51","id":"5783a3e7-e373-4100-a7d4-0aa7c8f6a4ab"}]`) {
		t.Fatalf("no-flow client rendered %s", raw)
	}
	if strings.Contains(string(raw), "fallbacks") || strings.Contains(string(raw), "reality") || strings.Contains(string(raw), "tlsSettings") {
		t.Fatalf("decryption excludes fallbacks / TLS / Reality: %s", raw)
	}
}

// UDP rides the stream: only a busy TCP port blocks a vless_enc node.
func TestVLESSEncBusyPorts(t *testing.T) {
	shareRange := Config{PortMin: 30000, PortMax: 30999, ResetDay: 1}
	n := vlessEncNode()
	if UDPProtocol(ProtocolVLESSEnc) {
		t.Fatal("vless_enc listens on TCP only")
	}
	if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{n}}, shareRange, nil, DesiredOptions{Protocols: vlessEncCaps, Busy: &ListeningPorts{UDP: []int{30007}}}); len(e) > 0 {
		t.Fatalf("busy UDP blocked a TCP-only node: %+v", e)
	}
	if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{n}}, shareRange, nil, DesiredOptions{Protocols: vlessEncCaps, Busy: &ListeningPorts{TCP: []int{30007}}}); len(e) != 1 || e[0].Field != "port" {
		t.Fatalf("busy TCP: %+v", e)
	}
}

// Shares without vless_enc nodes encode exactly as before, and the cap
// sanitizes to version 1 only.
func TestVLESSEncEncodingAndSanitize(t *testing.T) {
	f := loadProtocolFixture(t)
	for _, n := range append([]Node{testNode()}, f.Nodes...) {
		raw, _ := json.Marshal(n)
		if strings.Contains(string(raw), "vless_enc") {
			t.Fatalf("node %s gained vless_enc: %s", n.ID, raw)
		}
	}
	raw, _ := json.Marshal(vlessEncNode())
	if !strings.Contains(string(raw), `"protocol":"vless_enc"`) || !strings.Contains(string(raw), `"vless_enc":{"uuid":"5783a3e7-e373-4100-a7d4-0aa7c8f6a4ab","flow":"xtls-rprx-vision","decryption":"mlkem768x25519plus.random.600s.`) {
		t.Fatalf("vless_enc node encoding %s", raw)
	}
	got := SanitizeProtocols(map[string]ProtocolCap{"vless_enc": {UDP: true, Versions: []int{1, 3, 1}}, "mieru": {UDP: true}})
	if want := map[string]ProtocolCap{"vless_enc": {UDP: true, Versions: []int{1}}, "mieru": {UDP: true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized %+v", got)
	}
	if got := SanitizeProtocols(map[string]ProtocolCap{"vless_enc": {UDP: true, Versions: []int{2}}}); got != nil {
		t.Fatalf("unknown version kept: %+v", got)
	}
	if got := SanitizeProtocols(map[string]ProtocolCap{"vless_enc": {UDP: true}}); !reflect.DeepEqual(got, map[string]ProtocolCap{"vless_enc": {UDP: true}}) {
		t.Fatalf("default versions dropped: %+v", got)
	}
	if !IsExtraProtocol(ProtocolVLESSEnc) || ExtraProtocols[len(ExtraProtocols)-1] != ProtocolVLESSEnc {
		t.Fatal("vless_enc is a landing-protocols-v1 protocol")
	}
}
