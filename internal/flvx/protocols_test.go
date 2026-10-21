package flvx

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type protocolFixture struct {
	Nodes      []Node `json:"nodes"`
	ECKeyPEM   string `json:"ec_private_key_pem"`
	OtherKey   string `json:"other_key_pem"`
	anytls     Node
	snell      Node
	mieru      Node
	snellObfs  Node
	allCaps    map[string]ProtocolCap
	shareRange Config
}

func loadProtocolFixture(t *testing.T) protocolFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/landing-protocols-nodes.json")
	if err != nil {
		t.Fatal(err)
	}
	var f protocolFixture
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	f.anytls, f.snell, f.mieru, f.snellObfs = f.Nodes[0], f.Nodes[1], f.Nodes[2], f.Nodes[3]
	f.allCaps = map[string]ProtocolCap{ProtocolAnyTLS: {UDP: true}, ProtocolSnell: {UDP: true, Versions: []int{4, 5}, Obfs: []string{"none", "http", "tls"}}, ProtocolMieru: {UDP: true, Transports: []string{"TCP", "UDP"}}}
	f.shareRange = Config{PortMin: 30000, PortMax: 30999, ResetDay: 1, MemoryMB: 128, CPUPercent: 50}
	return f
}

// clone deep-copies a node so a test case can mutate its protocol object.
func clone(t *testing.T, n Node) Node {
	t.Helper()
	raw, _ := json.Marshal(n)
	var c Node
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// Contract §3: the field rules, each failing case reported on its field path.
func TestExtraProtocolValidationTable(t *testing.T) {
	f := loadProtocolFixture(t)
	options := DesiredOptions{Protocols: f.allCaps, Now: time.Now()}
	if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: f.Nodes}, f.shareRange, []int{30002}, options); len(e) > 0 {
		t.Fatalf("valid nodes rejected: %+v", e)
	}
	ecKey := clone(t, f.anytls)
	ecKey.AnyTLS.TLS.KeyPEM = f.ECKeyPEM
	ecKey.AnyTLS.TLS.ServerName = ""
	if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{ecKey}}, f.shareRange, nil, options); len(e) > 0 {
		t.Fatalf("EC PRIVATE KEY / empty server_name rejected: %+v", e)
	}
	cases := []struct {
		name, field string
		base        Node
		mutate      func(*Node)
	}{
		{"anytls short password", "anytls.password", f.anytls, func(n *Node) { n.AnyTLS.Password = "short" }},
		{"anytls symbol password", "anytls.password", f.anytls, func(n *Node) { n.AnyTLS.Password = "Xh3k9PqR2mWn7Lz4Vb8Tc1Y!" }},
		{"anytls long password", "anytls.password", f.anytls, func(n *Node) { n.AnyTLS.Password = strings.Repeat("a", 65) }},
		{"anytls padding", "anytls.padding", f.anytls, func(n *Node) { n.AnyTLS.Padding = "stop=8" }},
		{"anytls IP server_name", "anytls.tls", f.anytls, func(n *Node) { n.AnyTLS.TLS.ServerName = "154.3.39.138" }},
		{"anytls mismatched key", "anytls.tls", f.anytls, func(n *Node) { n.AnyTLS.TLS.KeyPEM = f.OtherKey }},
		{"anytls two certificates", "anytls.tls", f.anytls, func(n *Node) { n.AnyTLS.TLS.CertificatePEM += n.AnyTLS.TLS.CertificatePEM }},
		{"anytls trailing garbage", "anytls.tls", f.anytls, func(n *Node) { n.AnyTLS.TLS.CertificatePEM += "junk" }},
		{"anytls key as certificate", "anytls.tls", f.anytls, func(n *Node) { n.AnyTLS.TLS.CertificatePEM = n.AnyTLS.TLS.KeyPEM }},
		{"anytls oversize", "anytls.tls", f.anytls, func(n *Node) { n.AnyTLS.TLS.KeyPEM += strings.Repeat(" ", 4096) }},
		{"anytls no tls", "anytls.tls", f.anytls, func(n *Node) { n.AnyTLS.TLS = AnyTLSTLS{} }},
		{"anytls extra object", "anytls", f.anytls, func(n *Node) { n.Mieru = &Mieru{Username: "abcd", Password: strings.Repeat("a", 16)} }},
		{"anytls object missing", "anytls", f.anytls, func(n *Node) { n.AnyTLS = nil }},
		{"snell psk", "snell.psk", f.snell, func(n *Node) { n.Snell.PSK = "tooshort" }},
		{"snell version 6", "snell.version", f.snell, func(n *Node) { n.Snell.Version = 6 }},
		{"snell version 3", "snell.version", f.snell, func(n *Node) { n.Snell.Version = 3 }},
		{"snell obfs mode", "snell.obfs", f.snell, func(n *Node) { n.Snell.Obfs = "shadow-tls" }},
		{"snell host without obfs", "snell.obfs", f.snell, func(n *Node) { n.Snell.ObfsHost = "www.bing.com" }},
		{"snell obfs without host", "snell.obfs", f.snellObfs, func(n *Node) { n.Snell.ObfsHost = "" }},
		{"snell obfs IP host", "snell.obfs", f.snellObfs, func(n *Node) { n.Snell.ObfsHost = "1.1.1.1" }},
		{"snell ss2022 object", "snell", f.snell, func(n *Node) { n.SS2022 = &SS2022{} }},
		{"mieru username short", "mieru.username", f.mieru, func(n *Node) { n.Mieru.Username = "abc" }},
		{"mieru username symbol", "mieru.username", f.mieru, func(n *Node) { n.Mieru.Username = "k3j9-m2x7" }},
		{"mieru password", "mieru.password", f.mieru, func(n *Node) { n.Mieru.Password = strings.Repeat("a", 15) }},
		{"official port", "port", f.mieru, func(n *Node) { n.Port = 30002 }},
		{"outside range", "port", f.snell, func(n *Node) { n.Port = 31000 }},
		{"unknown protocol", "protocol", f.snell, func(n *Node) { n.Protocol = "snell6" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := clone(t, c.base)
			c.mutate(&n)
			e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{n}}, f.shareRange, []int{30002}, options)
			if len(e) == 0 || e[0].Field != c.field || e[0].NodeID != n.ID {
				t.Fatalf("errors %+v, want field %q", e, c.field)
			}
		})
	}
}

// A share advertises a protocol only when its Helper and Core can account
// for it; an unadvertised protocol or option is refused.
func TestExtraProtocolCapabilityGate(t *testing.T) {
	f := loadProtocolFixture(t)
	for _, n := range f.Nodes {
		if e := ValidateDesired(Desired{Revision: 1, Nodes: []Node{n}}, f.shareRange, nil); len(e) != 1 || e[0].Field != "protocol" {
			t.Fatalf("%s accepted without capability: %+v", n.Protocol, e)
		}
	}
	tcpOnly := map[string]ProtocolCap{ProtocolSnell: {UDP: false, Versions: []int{4}, Obfs: []string{"none"}}}
	if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{f.snell}}, f.shareRange, nil, DesiredOptions{Protocols: tcpOnly}); len(e) > 0 {
		t.Fatalf("Snell v4 rejected: %+v", e)
	}
	if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{f.snellObfs}}, f.shareRange, nil, DesiredOptions{Protocols: tcpOnly}); len(e) != 2 {
		t.Fatalf("v5 + tls obfs outside the capability: %+v", e)
	}
	if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{f.anytls}}, f.shareRange, nil, DesiredOptions{Protocols: tcpOnly}); len(e) != 1 || e[0].Field != "protocol" {
		t.Fatalf("AnyTLS accepted on a share advertising only Snell: %+v", e)
	}
}

// The certificate must be valid now (with clock skew), checked only where
// Custom accepts the state, never by the Helper (Now unset).
func TestAnyTLSCertificateValidity(t *testing.T) {
	f := loadProtocolFixture(t)
	d := Desired{Revision: 1, Nodes: []Node{f.anytls}}
	for name, now := range map[string]time.Time{"expired": time.Now().AddDate(11, 0, 0), "not yet valid": time.Now().AddDate(0, 0, -2)} {
		if e := ValidateDesiredWith(d, f.shareRange, nil, DesiredOptions{Protocols: f.allCaps, Now: now}); len(e) != 1 || e[0].Field != "anytls.tls" {
			t.Fatalf("%s certificate accepted: %+v", name, e)
		}
	}
	if e := ValidateDesiredWith(d, f.shareRange, nil, DesiredOptions{Protocols: f.allCaps}); len(e) > 0 {
		t.Fatalf("structural check failed: %+v", e)
	}
}

// Port rules: busy TCP blocks every protocol; busy UDP blocks only the
// protocols that also listen on UDP (Mieru, SS2022).
func TestBusyPortsByNetwork(t *testing.T) {
	f := loadProtocolFixture(t)
	busy := &ListeningPorts{TCP: []int{30003}, UDP: []int{30004, 30005}}
	e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{f.anytls, f.snell, f.mieru}}, f.shareRange, nil, DesiredOptions{Protocols: f.allCaps, Busy: busy})
	got := map[string]string{}
	for _, v := range e {
		got[v.NodeID] = v.Field
	}
	if !reflect.DeepEqual(got, map[string]string{"41": "port", "43": "port"}) {
		t.Fatalf("busy port errors %+v", e)
	}
	ss := testNode()
	ss.Port = 30005
	if e := ValidateDesiredWith(Desired{Revision: 1, Nodes: []Node{ss}}, f.shareRange, nil, DesiredOptions{Busy: busy}); len(e) != 1 || e[0].Field != "port" {
		t.Fatalf("SS2022 on a busy UDP port: %+v", e)
	}
}

// Old FLVX keeps working: a v1 node encodes exactly as before (no new keys),
// so the signed Helper payload of a v1-only share is byte-identical.
func TestV1NodeEncodingUnchanged(t *testing.T) {
	raw, _ := json.Marshal(testNode())
	for _, key := range []string{"anytls", "snell", "mieru"} {
		if bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Fatalf("v1 node gained %q: %s", key, raw)
		}
	}
	want := `{"id":"123","owner":{"id":"7","name":"Alice"},"name":"SS","protocol":"ss2022","port":30001,"enabled":true,"max_connections":0,"max_online_ips":0,"expires_at":null,"ss2022":{"method":"2022-blake3-aes-128-gcm","server_key":"AAAAAAAAAAAAAAAAAAAAAA==","user_key":"AAAAAAAAAAAAAAAAAAAAAA=="}}`
	if string(raw) != want {
		t.Fatalf("v1 node encoding changed:\n%s\n%s", raw, want)
	}
	var s Status
	raw, _ = json.Marshal(s)
	if bytes.Contains(raw, []byte("flvx_protocols")) || bytes.Contains(raw, []byte("listening_ports")) {
		t.Fatalf("empty status gained new keys: %s", raw)
	}
}

// Contract §6, golden: the inbounds the Helper hands to the FLVX Core.
func TestExtraProtocolRenderGolden(t *testing.T) {
	f := loadProtocolFixture(t)
	sniffing := map[string]any{"enabled": true, "destOverride": []any{"http", "tls"}, "routeOnly": true, "domainsExcluded": []any{"regexp:.*"}}
	cert, key := strings.Split(strings.TrimSpace(f.anytls.AnyTLS.TLS.CertificatePEM), "\n"), strings.Split(strings.TrimSpace(f.anytls.AnyTLS.TLS.KeyPEM), "\n")
	toAny := func(lines []string) []any {
		out := []any{}
		for _, l := range lines {
			out = append(out, l)
		}
		return out
	}
	want := map[string]map[string]any{
		"41": {"tag": "flvxl-41", "listen": "0.0.0.0", "port": float64(30003), "protocol": "anytls", "sniffing": sniffing,
			"settings": map[string]any{"users": []any{map[string]any{"password": "Xh3k9PqR2mWn7Lz4Vb8Tc1Yd", "email": "flvxl-41"}}},
			"streamSettings": map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"minVersion": "1.2", "alpn": []any{"h2", "http/1.1"},
				"certificates": []any{map[string]any{"certificate": toAny(cert), "key": toAny(key)}}}}},
		"42": {"tag": "flvxl-42", "listen": "0.0.0.0", "port": float64(30004), "protocol": "snell", "sniffing": sniffing,
			"settings": map[string]any{"users": []any{map[string]any{"psk": "Qm7Zp2Xc9Vb4Nn1Lk8Jh5Gf3Dd6Ss0Aa", "version": float64(4), "email": "flvxl-42"}}}},
		"43": {"tag": "flvxl-43", "listen": "0.0.0.0", "port": float64(30005), "protocol": "mieru", "sniffing": sniffing,
			"settings": map[string]any{"users": []any{map[string]any{"username": "k3j9m2x7q1", "password": "Zp4Kx8Wq2Lm6Nb9Vc3Rt7Yh1", "email": "flvxl-43"}}}},
		"44": {"tag": "flvxl-44", "listen": "0.0.0.0", "port": float64(30006), "protocol": "snell", "sniffing": sniffing,
			"settings": map[string]any{"users": []any{map[string]any{"psk": "Hh2Jj4Kk6Ll8Mm0Nn2Pp4Qq6Rr8Ss0Tt", "version": float64(5), "obfsMode": "tls", "obfsHost": "www.bing.com", "email": "flvxl-44"}}}},
	}
	for _, n := range f.Nodes {
		raw, _ := json.Marshal(Inbound(n))
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want[n.ID]) {
			t.Fatalf("node %s rendered\n%s", n.ID, raw)
		}
	}
	if len(cert) < 3 || cert[0] != "-----BEGIN CERTIFICATE-----" || key[0] != "-----BEGIN PRIVATE KEY-----" {
		t.Fatalf("PEM lines %q / %q", cert[0], key[0])
	}
}

func TestSanitizeProtocolsAndBusyPorts(t *testing.T) {
	got := SanitizeProtocols(map[string]ProtocolCap{
		"anytls": {UDP: true}, "snell": {Versions: []int{4, 6, 4}, Obfs: []string{"tls", "bogus"}}, "mieru": {Transports: []string{"QUIC"}}, "socks": {UDP: true},
	})
	want := map[string]ProtocolCap{"anytls": {UDP: true}, "snell": {Versions: []int{4}, Obfs: []string{"tls"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitized %+v", got)
	}
	if SanitizeProtocols(map[string]ProtocolCap{"socks": {}}) != nil || SanitizeProtocols(nil) != nil {
		t.Fatal("nothing usable must sanitize to nil")
	}
	busy := BusyPorts(30000, 30999, map[int]bool{30004: true}, []int{30005, 443, 30002}, []int{30002, 30004, 31000})
	if !reflect.DeepEqual(busy, []int{30002, 30005}) {
		t.Fatalf("busy %v", busy)
	}
	if (&ListeningPorts{TCP: []int{0}}).Valid() || (&ListeningPorts{UDP: make([]int, MaxBusyPorts+1)}).Valid() || !(*ListeningPorts)(nil).Valid() {
		t.Fatal("listening bounds")
	}
}
