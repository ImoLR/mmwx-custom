package flvx

import (
	"bytes"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fixed keys and inputs are FLVX's (go-gost/x/landing/egress_test.go), so the
// rendered JSON can be compared byte for byte with FLVX's golden file.
const (
	goldenWGPrivate = "YFYOAdbw1bKTHlNNi+aEjBM3BO7unuFC5rOkMRAz9XY="
	goldenWGPeer    = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
	goldenSSKey     = "AAAAAAAAAAAAAAAAAAAAAA=="
	goldenPBK       = "Ak6Ci7Ogvl2t29y1MUSF4fuAtoXtt4EnCyU_cdhn4Ws"
)

func goldenPlan() *Egress {
	return &Egress{Version: 1, Outbounds: []EgressOutbound{
		{Tag: "flvxo-direct4", Type: "direct", IPVersion: "force_ipv4"},
		{Tag: "flvxo-direct6", Type: "direct", IPVersion: "force_ipv6"},
		{Tag: "flvxo-pref4", Type: "direct", IPVersion: "prefer_ipv4"},
		{Tag: "flvxo-warp", Type: "wireguard", IPVersion: "force_ipv4", WireGuard: &EgressWireGuard{PrivateKey: goldenWGPrivate, PeerPublicKey: goldenWGPeer, AddressV4: "172.16.0.2", AddressV6: "2606:4700:110:8a36::2", Reserved: []int{1, 2, 3}}},
		{Tag: "flvxo-socks", Type: "socks", IPVersion: "prefer_ipv6", Server: "proxy.example.com", Port: 1080, Username: "u", Password: "p"},
		{Tag: "flvxo-http", Type: "http", IPVersion: "system", Server: "203.0.113.10", Port: 8080},
		{Tag: "flvxo-ss", Type: "shadowsocks", IPVersion: "force_ipv6", Server: "2001:db8::10", Port: 8388, Method: "2022-blake3-aes-128-gcm", Password: goldenSSKey + ":" + goldenSSKey},
		{Tag: "flvxo-vless", Type: "vless", IPVersion: "force_ipv4", Server: "198.51.100.7", Port: 443, UUID: "d77c8b43-40ed-42cd-9150-f3f47d0f534d", Flow: "xtls-rprx-vision", Reality: &EgressReality{ServerName: "www.example.com", PublicKey: goldenPBK, ShortID: "0a1b"}},
	}, Rules: []EgressRule{
		{Name: "AI", InboundTags: []string{"flvxl-2", "flvxl-1"}, Domain: []string{"geosite:openai", "domain:claude.ai", "full:x.ai", "keyword:gemini", "regexp:^chat\\."}, Outbound: "flvxo-warp"},
		{Name: "IP", InboundTags: []string{"flvxl-1"}, IP: []string{"geoip:cn", "203.0.113.0/24", "2001:db8::/32"}, Port: "443,8000-9000", Network: "tcp", Outbound: "block"},
		{Name: "BT", InboundTags: []string{"flvxl-1"}, Protocol: []string{"bittorrent"}, SourcePort: "1024-65535", Outbound: "block"},
		{Name: "Src", InboundTags: []string{"flvxl-1"}, SourceIP: []string{"198.51.100.0/24"}, Outbound: "flvxo-direct4"},
		{Name: "Default", InboundTags: []string{"flvxl-1", "flvxl-2"}, Outbound: "flvxo-socks"},
	}}
}

var goldenLocal = LocalAddrs{V4: netip.MustParseAddr("203.0.113.1"), V6: netip.MustParseAddr("2001:db8:1::1")}

func renderGolden(local LocalAddrs) map[string]any {
	out := map[string]any{}
	for _, o := range goldenPlan().Outbounds {
		cfg, warn := RenderOutbound(o, local)
		out[o.Tag] = map[string]any{"config": cfg, "warning": warn}
	}
	return out
}

func TestEgressRenderMatchesFLVXGolden(t *testing.T) {
	rules, err := RenderRules(goldenPlan(), func(string) ([]netip.Prefix, error) { return nil, fmt.Errorf("no geo data needed") }, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.MarshalIndent(map[string]any{"outbounds": renderGolden(goldenLocal), "outbounds_no_local": renderGolden(LocalAddrs{}), "rules": rules}, "", "  ")
	// testdata/flvx_egress_golden.json is FLVX's go-gost/x/landing/testdata/
	// egress_golden.json at ff3d479f, copied unchanged.
	want, err := os.ReadFile(filepath.Join("testdata", "flvx_egress_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
		t.Fatalf("rendering differs from FLVX golden JSON:\n%s", got)
	}
}

func TestEgressForcedFamilies(t *testing.T) {
	byTag := map[string]EgressOutbound{}
	for _, o := range goldenPlan().Outbounds {
		byTag[o.Tag] = o
	}
	// Every non-WireGuard type under force_* binds to a local address of the
	// forced family and resolves names only in that family.
	for _, typ := range []string{"direct", "socks", "http", "shadowsocks", "vless"} {
		for _, family := range []string{"force_ipv4", "force_ipv6"} {
			o := byTag["flvxo-vless"]
			switch typ {
			case "direct":
				o = EgressOutbound{Tag: "flvxo-x", Type: "direct"}
			case "socks", "http":
				o = EgressOutbound{Tag: "flvxo-x", Type: typ, Server: "proxy.example.com", Port: 1080}
			case "shadowsocks":
				o = byTag["flvxo-ss"]
				o.Server = "proxy.example.com"
			case "vless":
				o.Server = "proxy.example.com"
			}
			o.IPVersion = family
			cfg, warn := RenderOutbound(o, goldenLocal)
			want, strategy := goldenLocal.V4.String(), "ForceIPv4"
			if family == "force_ipv6" {
				want, strategy = goldenLocal.V6.String(), "ForceIPv6"
			}
			sockopt := cfg["streamSettings"].(map[string]any)["sockopt"].(map[string]any)
			got := sockopt["domainStrategy"]
			if typ == "direct" {
				got = cfg["settings"].(map[string]any)["domainStrategy"]
			}
			if warn != "" || cfg["sendThrough"] != want || got != strategy || sockopt["customSockopt"].([]any)[0].(map[string]any)["opt"] != "24" {
				t.Fatalf("%s %s not bound to its family: %v", typ, family, cfg)
			}
			// Without a local address of that family nothing may leave.
			missing := LocalAddrs{V6: goldenLocal.V6}
			if family == "force_ipv6" {
				missing = LocalAddrs{V4: goldenLocal.V4}
			}
			if none, warn := RenderOutbound(o, missing); none["protocol"] != "blackhole" || warn == "" {
				t.Fatalf("%s %s without that family must be a blackhole", typ, family)
			}
		}
	}
	warp, _ := RenderOutbound(byTag["flvxo-warp"], goldenLocal)
	ws := warp["settings"].(map[string]any)
	if addr := ws["address"].([]string); len(addr) != 1 || addr[0] != "172.16.0.2/32" || ws["domainStrategy"] != "ForceIPv4" {
		t.Fatalf("forced IPv4 WARP must carry only the IPv4 tunnel address: %v", ws)
	}
	w6 := byTag["flvxo-warp"]
	w6.IPVersion = "force_ipv6"
	warp, _ = RenderOutbound(w6, LocalAddrs{V6: goldenLocal.V6})
	ws = warp["settings"].(map[string]any)
	if addr := ws["address"].([]string); len(addr) != 1 || addr[0] != "2606:4700:110:8a36::2/128" || ws["peers"].([]any)[0].(map[string]any)["endpoint"] != WARPEndpointV6 {
		t.Fatalf("forced IPv6 WARP on an IPv6-only server: %v", ws)
	}
	if none, warn := RenderOutbound(w6, LocalAddrs{}); none["protocol"] != "blackhole" || warn == "" {
		t.Fatal("WARP without any public address must be a blackhole")
	}
	for _, mode := range []string{"system", "prefer_ipv4", "prefer_ipv6"} {
		o := byTag["flvxo-socks"]
		o.IPVersion = mode
		cfg, _ := RenderOutbound(o, goldenLocal)
		if _, bound := cfg["sendThrough"]; bound {
			t.Fatalf("%s must not bind", mode)
		}
	}
}

func validNodeTags() map[string]bool { return map[string]bool{"flvxl-1": true, "flvxl-2": true} }

func TestEgressValidationTable(t *testing.T) {
	if errs := ValidateEgress(goldenPlan(), validNodeTags(), true, true); len(errs) > 0 {
		t.Fatal(errs)
	}
	cases := map[string]struct {
		mutate func(e *Egress)
		field  string
	}{
		"version":              {func(e *Egress) { e.Version = 2 }, "egress.version"},
		"reserved tag":         {func(e *Egress) { e.Outbounds[0].Tag = "direct" }, "egress.outbounds[0].tag"},
		"tag syntax":           {func(e *Egress) { e.Outbounds[0].Tag = "flvxo-UPPER" }, "egress.outbounds[0].tag"},
		"dup tag":              {func(e *Egress) { e.Outbounds[1].Tag = e.Outbounds[0].Tag }, "egress.outbounds[1].tag"},
		"ip version":           {func(e *Egress) { e.Outbounds[0].IPVersion = "ipv4" }, "egress.outbounds[0].ip_version"},
		"unknown type":         {func(e *Egress) { e.Outbounds[0].Type = "vmess" }, "egress.outbounds[0].type"},
		"direct with server":   {func(e *Egress) { e.Outbounds[0].Server = "203.0.113.5" }, "egress.outbounds[0].type"},
		"private upstream":     {func(e *Egress) { e.Outbounds[5].Server = "10.0.0.5" }, "egress.outbounds[5].server"},
		"cgnat upstream":       {func(e *Egress) { e.Outbounds[5].Server = "100.64.0.1" }, "egress.outbounds[5].server"},
		"metadata upstream":    {func(e *Egress) { e.Outbounds[5].Server = "169.254.169.254" }, "egress.outbounds[5].server"},
		"loopback v6 upstream": {func(e *Egress) { e.Outbounds[5].Server = "::1" }, "egress.outbounds[5].server"},
		"ula upstream":         {func(e *Egress) { e.Outbounds[5].Server = "fd00::1" }, "egress.outbounds[5].server"},
		"mapped private":       {func(e *Egress) { e.Outbounds[5].Server = "::ffff:192.168.1.1" }, "egress.outbounds[5].server"},
		"benchmark upstream":   {func(e *Egress) { e.Outbounds[5].Server = "198.18.0.1" }, "egress.outbounds[5].server"},
		"bare hostname":        {func(e *Egress) { e.Outbounds[4].Server = "localhost" }, "egress.outbounds[4].server"},
		"upper hostname":       {func(e *Egress) { e.Outbounds[4].Server = "Proxy.Example.com" }, "egress.outbounds[4].server"},
		"port":                 {func(e *Egress) { e.Outbounds[4].Port = 0 }, "egress.outbounds[4].port"},
		"credential newline":   {func(e *Egress) { e.Outbounds[4].Password = "a\nb" }, "egress.outbounds[4].password"},
		"forced v4 v6 literal": {func(e *Egress) { e.Outbounds[7].Server = "2001:db8::7" }, "egress.outbounds[7].server"},
		"forced v6 v4 literal": {func(e *Egress) { e.Outbounds[6].Server = "203.0.113.9" }, "egress.outbounds[6].server"},
		"ss2022 key":           {func(e *Egress) { e.Outbounds[6].Password = "short" }, "egress.outbounds[6].password"},
		"ss2022 256 key size":  {func(e *Egress) { e.Outbounds[6].Method = "2022-blake3-aes-256-gcm" }, "egress.outbounds[6].password"},
		"ss method":            {func(e *Egress) { e.Outbounds[6].Method = "rc4-md5" }, "egress.outbounds[6].method"},
		"vless uuid":           {func(e *Egress) { e.Outbounds[7].UUID = "nope" }, "egress.outbounds[7].uuid"},
		"vless flow":           {func(e *Egress) { e.Outbounds[7].Flow = "xtls-rprx-direct" }, "egress.outbounds[7].flow"},
		"vless no reality":     {func(e *Egress) { e.Outbounds[7].Reality = nil }, "egress.outbounds[7].reality"},
		"reality key":          {func(e *Egress) { e.Outbounds[7].Reality.PublicKey = "abc" }, "egress.outbounds[7].reality.public_key"},
		"reality sid":          {func(e *Egress) { e.Outbounds[7].Reality.ShortID = "xyz" }, "egress.outbounds[7].reality.short_id"},
		"reality odd sid":      {func(e *Egress) { e.Outbounds[7].Reality.ShortID = "abc" }, "egress.outbounds[7].reality.short_id"},
		"reality sni":          {func(e *Egress) { e.Outbounds[7].Reality.ServerName = "" }, "egress.outbounds[7].reality.server_name"},
		"reality fingerprint":  {func(e *Egress) { e.Outbounds[7].Reality.Fingerprint = "netscape" }, "egress.outbounds[7].reality.fingerprint"},
		"wg private key":       {func(e *Egress) { e.Outbounds[3].WireGuard.PrivateKey = "AAAA" }, "egress.outbounds[3].wireguard.private_key"},
		"wg force v4 family":   {func(e *Egress) { e.Outbounds[3].WireGuard.AddressV4 = "" }, "egress.outbounds[3].wireguard.address_v4"},
		"wg force v6 family": {func(e *Egress) {
			e.Outbounds[3].IPVersion = "force_ipv6"
			e.Outbounds[3].WireGuard.AddressV6 = ""
		}, "egress.outbounds[3].wireguard.address_v6"},
		"wg reserved":         {func(e *Egress) { e.Outbounds[3].WireGuard.Reserved = []int{1, 2} }, "egress.outbounds[3].wireguard.reserved"},
		"wg reserved range":   {func(e *Egress) { e.Outbounds[3].WireGuard.Reserved = []int{1, 2, 256} }, "egress.outbounds[3].wireguard.reserved"},
		"wg mtu":              {func(e *Egress) { e.Outbounds[3].WireGuard.MTU = 9000 }, "egress.outbounds[3].wireguard.mtu"},
		"wg private endpoint": {func(e *Egress) { e.Outbounds[3].WireGuard.Endpoint = "192.168.0.1:2408" }, "egress.outbounds[3].wireguard.endpoint"},
		"wg endpoint v4 fam":  {func(e *Egress) { e.Outbounds[3].WireGuard.EndpointV4 = "[2606:4700:d0::a29f:c006]:2408" }, "egress.outbounds[3].wireguard.endpoint_v4"},
		"wg endpoint v6 fam":  {func(e *Egress) { e.Outbounds[3].WireGuard.EndpointV6 = "162.159.192.6:2408" }, "egress.outbounds[3].wireguard.endpoint_v6"},
		"wg endpoint v6 name": {func(e *Egress) { e.Outbounds[3].WireGuard.EndpointV6 = "warp.example.com:2408" }, "egress.outbounds[3].wireguard.endpoint_v6"},
		"rule outbound":       {func(e *Egress) { e.Rules[0].Outbound = "flvxo-missing" }, "egress.rules[0].outbound"},
		"rule scope":          {func(e *Egress) { e.Rules[0].InboundTags = nil }, "egress.rules[0].inbound_tags"},
		"other share node":    {func(e *Egress) { e.Rules[0].InboundTags = []string{"flvxl-9"} }, "egress.rules[0].inbound_tags[0]"},
		"ext domain":          {func(e *Egress) { e.Rules[0].Domain = []string{"ext:evil.dat:x"} }, "egress.rules[0].domain[0]"},
		"bare domain":         {func(e *Egress) { e.Rules[0].Domain = []string{"example.com"} }, "egress.rules[0].domain[0]"},
		"wildcard domain":     {func(e *Egress) { e.Rules[0].Domain = []string{"domain:*.example.com"} }, "egress.rules[0].domain[0]"},
		"bad regexp":          {func(e *Egress) { e.Rules[0].Domain = []string{"regexp:("} }, "egress.rules[0].domain[0]"},
		"long regexp":         {func(e *Egress) { e.Rules[0].Domain = []string{"regexp:" + strings.Repeat("a", 257)} }, "egress.rules[0].domain[0]"},
		"unknown geosite":     {func(e *Egress) { e.Rules[0].Domain = []string{"geosite:no-such-list"} }, "egress.rules[0].domain[0]"},
		"ext ip":              {func(e *Egress) { e.Rules[1].IP = []string{"ext:evil.dat:x"} }, "egress.rules[1].ip[0]"},
		"ext-ip":              {func(e *Egress) { e.Rules[1].IP = []string{"ext-ip:evil.dat:x"} }, "egress.rules[1].ip[0]"},
		"unknown geoip":       {func(e *Egress) { e.Rules[1].IP = []string{"geoip:zz"} }, "egress.rules[1].ip[0]"},
		"bad cidr":            {func(e *Egress) { e.Rules[1].IP = []string{"1.2.3.4/40"} }, "egress.rules[1].ip[0]"},
		"bad source":          {func(e *Egress) { e.Rules[3].SourceIP = []string{"ext:x.dat:y"} }, "egress.rules[3].source_ip[0]"},
		"bad port":            {func(e *Egress) { e.Rules[1].Port = "0-70000" }, "egress.rules[1].port"},
		"too many ports":      {func(e *Egress) { e.Rules[1].Port = strings.Repeat("1,", 64) + "1" }, "egress.rules[1].port"},
		"bad source port":     {func(e *Egress) { e.Rules[2].SourcePort = "9-1" }, "egress.rules[2].source_port"},
		"bad network":         {func(e *Egress) { e.Rules[1].Network = "icmp" }, "egress.rules[1].network"},
		"bad protocol":        {func(e *Egress) { e.Rules[2].Protocol = []string{"ssh"} }, "egress.rules[2].protocol[0]"},
		"bad exempt":          {func(e *Egress) { e.ExemptSources = []string{"nope"} }, "egress.exempt_sources[0]"},
		"rule geo too large":  {func(e *Egress) { e.Rules[0].Domain = []string{"geosite:category-ads-all"} }, "egress.rules[0]"},
		"plan geo too large": {func(e *Egress) {
			e.Rules[0].Domain = []string{"geosite:cn"}
			e.Rules[1].Domain = []string{"geosite:cn"}
		}, "egress.rules"},
		"too many outbounds": {func(e *Egress) {
			for i := 0; i < 1; i++ {
				e.Outbounds = append(e.Outbounds, EgressOutbound{Tag: fmt.Sprintf("flvxo-x%d", i), Type: "direct", IPVersion: "system"})
			}
		}, "egress.outbounds"},
		"too many rules": {func(e *Egress) {
			for len(e.Rules) <= MaxEgressRules {
				e.Rules = append(e.Rules, e.Rules[4])
			}
		}, "egress.rules"},
		"too many items": {func(e *Egress) {
			for i := 0; i < MaxEgressItems; i++ {
				e.Rules[4].IP = append(e.Rules[4].IP, fmt.Sprintf("203.0.%d.%d", i/250, i%250))
			}
		}, "egress.rules"},
		"too many exempt": {func(e *Egress) {
			for i := 0; i <= MaxEgressExempt; i++ {
				e.ExemptSources = append(e.ExemptSources, fmt.Sprintf("203.0.%d.%d/32", i/250, i%250))
			}
		}, "egress.exempt_sources"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := goldenPlan()
			c.mutate(e)
			errs := ValidateEgress(e, validNodeTags(), true, true)
			found := false
			for _, v := range errs {
				found = found || v.Field == c.field && v.NodeID == ""
			}
			if !found {
				t.Fatalf("want error on %s, got %v", c.field, errs)
			}
		})
	}
	// Geo matchers need the Helper's reported geo capability.
	if errs := ValidateEgress(goldenPlan(), validNodeTags(), true, false); len(errs) != 1 || errs[0].Field != "egress.rules[0].domain[0]" {
		t.Fatalf("geosite without capability: %v", errs)
	}
	if errs := ValidateEgress(goldenPlan(), validNodeTags(), false, true); len(errs) != 1 || errs[0].Field != "egress.rules[1].ip[0]" {
		t.Fatalf("geoip without capability: %v", errs)
	}
	// SS2022 with an identity PSK and AEAD methods are accepted.
	e := goldenPlan()
	e.Outbounds[6].Method, e.Outbounds[6].Password = "2022-blake3-chacha20-poly1305", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	e.Outbounds[5] = EgressOutbound{Tag: "flvxo-http", Type: "shadowsocks", IPVersion: "system", Server: "203.0.113.10", Port: 8388, Method: "aes-256-gcm", Password: "secret"}
	e.Rules[1].Network = "tcp,udp"
	if errs := ValidateEgress(e, validNodeTags(), true, true); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func TestEgressDecoding(t *testing.T) {
	var d Desired
	raw := `{"revision":3,"nodes":[],"egress":{"version":1,"allow_core_restart":true,"future":{"x":1},"outbounds":[],"rules":[{"inbound_tags":["flvxl-1"],"outbound":"block","domain":[]}],"exempt_sources":[]}}`
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		t.Fatal("unknown top-level egress fields must be ignored:", err)
	}
	if d.Egress == nil || d.Egress.Outbounds != nil || d.Egress.ExemptSources != nil || d.Egress.Rules[0].Domain != nil {
		t.Fatalf("empty lists must normalize to nil: %+v", d.Egress)
	}
	again, _ := json.Marshal(d)
	var round Desired
	if err := json.Unmarshal(again, &round); err != nil || CanonicalJSON(round) != string(again) {
		t.Fatalf("round trip unstable: %s vs %s", again, CanonicalJSON(round))
	}
	if strings.Contains(string(again), "allow_core_restart") {
		t.Fatal("allow_core_restart must not be stored")
	}
	for _, bad := range []string{
		`{"version":1,"outbounds":[{"tag":"flvxo-1","type":"direct","ip_version":"system","sni":"x"}],"rules":[]}`,
		`{"version":1,"outbounds":[],"rules":[{"inbound_tags":["flvxl-1"],"outbound":"block","geo":"x"}]}`,
		`[]`,
	} {
		var e Egress
		if err := json.Unmarshal([]byte(bad), &e); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	// Absent egress keeps today's payload bytes (signature compatibility).
	plain, _ := json.Marshal(Desired{Revision: 1, Nodes: []Node{}})
	if string(plain) != `{"revision":1,"nodes":[]}` {
		t.Fatalf("desired without egress changed: %s", plain)
	}
}

func TestEgressRulesSafetyFirstAndExemption(t *testing.T) {
	cn := []netip.Prefix{netip.MustParsePrefix("1.0.1.0/24"), netip.MustParsePrefix("1.0.2.0/23"), netip.MustParsePrefix("240e::/20")}
	reads := 0
	read := func(code string) ([]netip.Prefix, error) {
		reads++
		if code != "cn" {
			return nil, fmt.Errorf("unknown %s", code)
		}
		return cn, nil
	}
	e := &Egress{Version: 1, Rules: []EgressRule{
		{InboundTags: []string{"flvxl-1"}, IP: []string{"geoip:cn"}, Outbound: "block"},
		{InboundTags: []string{"flvxl-1"}, Domain: []string{"geosite:cn"}, Outbound: "block"},
		{InboundTags: []string{"flvxl-1"}, SourceIP: []string{"geoip:cn"}, Outbound: "block"},
		{InboundTags: []string{"flvxl-1"}, SourceIP: []string{"1.0.1.7/32"}, Outbound: "block"},
		{InboundTags: []string{"flvxl-1"}, SourceIP: []string{"geoip:!cn"}, Outbound: "direct"},
	}, ExemptSources: []string{"1.0.1.7", "240e::1/128", "8.8.4.4"}}
	memo := &SourceMemo{}
	rules, err := RenderRules(e, read, memo)
	if err != nil {
		t.Fatal(err)
	}
	memo.Rotate()
	first := rules[0].(map[string]any)
	if first["ruleTag"] != "flvx-safety" || first["outboundTag"] != "block" || len(first["ip"].([]string)) != len(BlockCIDRs) {
		t.Fatal("safety rule must be first")
	}
	src := rules[3].(map[string]any)["sourceIP"].([]string)
	got := newDualSet(mustPrefixes(t, src))
	want := newDualSet(cn).subtract(newDualSet(mustPrefixes(t, e.ExemptSources)))
	if fmt.Sprint(got.cidrs()) != fmt.Sprint(want.cidrs()) || got.intersects(newDualSet(mustPrefixes(t, e.ExemptSources))) {
		t.Fatalf("CN minus exempt mismatch:\n%v\n%v", got.cidrs(), want.cidrs())
	}
	if len(rules) != 5 {
		t.Fatalf("fully exempt source rule not dropped: %d rules", len(rules))
	}
	rs := newDualSet(mustPrefixes(t, rules[4].(map[string]any)["sourceIP"].([]string)))
	if rs.intersects(newDualSet(cn)) || rs.intersects(newDualSet(mustPrefixes(t, e.ExemptSources))) || !rs.intersects(newDualSet(mustPrefixes(t, []string{"8.8.8.8"}))) {
		t.Fatal("reverse geoip source wrong")
	}
	// A periodic re-render reuses the expanded sets: no geoip.dat read.
	before := reads
	if _, err := RenderRules(e, read, memo); err != nil || reads != before {
		t.Fatalf("re-render read geo data again: %d -> %d", before, reads)
	}
	e.ExemptSources = []string{"8.8.8.8"}
	rules, _ = RenderRules(e, read, nil)
	if s := rules[3].(map[string]any)["sourceIP"].([]string); len(s) != 1 || s[0] != "geoip:cn" {
		t.Fatalf("non-overlapping exemption rewrote the source list: %v", s)
	}
}

func mustPrefixes(t *testing.T, in []string) []netip.Prefix {
	t.Helper()
	out := []netip.Prefix{}
	for _, s := range in {
		p, err := parsePrefix(s)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestCIDRSetBruteForce(t *testing.T) {
	r := mrand.New(mrand.NewSource(7))
	base := netip.MustParseAddr("10.20.0.0").As4()
	addr := func(i int) netip.Addr {
		b := base
		b[2] += byte(i >> 8)
		b[3] = byte(i)
		return netip.AddrFrom4(b)
	}
	for round := 0; round < 200; round++ {
		var a, b []netip.Prefix
		for i := 0; i < 6; i++ {
			a = append(a, netip.PrefixFrom(addr(r.Intn(4096)), 20+r.Intn(13)).Masked())
			b = append(b, netip.PrefixFrom(addr(r.Intn(4096)), 22+r.Intn(11)).Masked())
		}
		cover := mustPrefixes(t, newDualSet(a).subtract(newDualSet(b)).cidrs())
		for i := 0; i < 4096; i++ {
			x := addr(i)
			inA, inB, hit := false, false, false
			for _, p := range a {
				inA = inA || p.Contains(x)
			}
			for _, p := range b {
				inB = inB || p.Contains(x)
			}
			for _, p := range cover {
				hit = hit || p.Contains(x)
			}
			if hit != (inA && !inB) {
				t.Fatalf("round %d: %s membership %v", round, x, hit)
			}
		}
		if again := newDualSet(cover).cidrs(); len(again) != len(cover) {
			t.Fatalf("non-minimal cover %v vs %v", cover, again)
		}
	}
	if c := fullSet(128).cidrs(); len(c) != 1 || c[0] != "::/0" {
		t.Fatalf("full IPv6 space cover = %v", c)
	}
}

func writeVarint(buf *bytes.Buffer, v uint64) {
	for v >= 0x80 {
		buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
}

func geoEntry(code string, prefixes []netip.Prefix) []byte {
	var body bytes.Buffer
	body.WriteByte(0x0a)
	writeVarint(&body, uint64(len(code)))
	body.WriteString(code)
	for _, p := range prefixes {
		var c bytes.Buffer
		ip := p.Addr().AsSlice()
		c.WriteByte(0x0a)
		writeVarint(&c, uint64(len(ip)))
		c.Write(ip)
		c.WriteByte(0x10)
		writeVarint(&c, uint64(p.Bits()))
		body.WriteByte(0x12)
		writeVarint(&body, uint64(c.Len()))
		body.Write(c.Bytes())
	}
	var out bytes.Buffer
	out.WriteByte(0x0a)
	writeVarint(&out, uint64(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestReadGeoIP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.dat")
	data := append(geoEntry("US", []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")}), geoEntry("CN", []netip.Prefix{netip.MustParsePrefix("1.0.1.0/24"), netip.MustParsePrefix("240e::/20")})...)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGeoIP(path, "cn")
	if err != nil || len(got) != 2 || got[1].String() != "240e::/20" {
		t.Fatalf("ReadGeoIP = %v %v", got, err)
	}
	if _, err := ReadGeoIP(path, "jp"); err == nil {
		t.Fatal("missing category accepted")
	}
	// The pinned release (MMWXC_TEST_GEO_DIR) ships CN in both families and
	// matches the embedded code table.
	if dir := os.Getenv("MMWXC_TEST_GEO_DIR"); dir != "" {
		cn, err := ReadGeoIP(filepath.Join(dir, "geoip.dat"), "cn")
		if err != nil || len(cn) != geoIPTableItems("cn") {
			t.Fatalf("pinned geoip.dat cn: %d %v", len(cn), err)
		}
		set := newDualSet(cn)
		if len(set.v4.ranges) == 0 || len(set.v6.ranges) == 0 {
			t.Fatal("pinned geoip:cn lacks a family")
		}
		t.Logf("pinned geoip:cn = %d CIDRs", len(cn))
	}
}

func geoIPTableItems(code string) int {
	ip, _ := geoTables()
	return ip[code].items
}
