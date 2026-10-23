package flvx

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The guard inbound, its rules and the re-pointed Reality inbound exactly as
// contract landing-reality-guard-v1 §3 (plus the FLVX plan 128 tls condition).
func TestGuardRenderContract(t *testing.T) {
	g := Guard{Dest: "203.0.113.80:443", Names: GuardNames([]string{" WWW.Example.com", "example.com", "www.example.com"})}
	in, _ := json.Marshal(g.Inbound(41001))
	if string(in) != `{"listen":"127.0.0.1","port":41001,"protocol":"dokodemo-door","settings":{"address":"203.0.113.80","network":"tcp","port":443},"sniffing":{"destOverride":["tls"],"enabled":true,"routeOnly":true},"tag":"flvxg-41001"}` {
		t.Fatalf("inbound %s", in)
	}
	rules, _ := json.Marshal(g.Rules(41001))
	if string(rules) != `[{"domain":["full:example.com","full:www.example.com"],"inboundTag":["flvxg-41001"],"outboundTag":"flvxg-direct","protocol":["tls"],"ruleTag":"flvxg-41001-allow","type":"field"},{"inboundTag":["flvxg-41001"],"outboundTag":"block","ruleTag":"flvxg-41001-block","type":"field"}]` {
		t.Fatalf("rules %s", rules)
	}
	out, _ := json.Marshal(GuardOutbound())
	if string(out) != `{"protocol":"freedom","settings":{"domainStrategy":"UseIP","userLevel":1},"tag":"flvxg-direct"}` {
		t.Fatalf("outbound %s", out)
	}
	v6 := Guard{Dest: "[2001:db8::80]:8443", Names: []string{"a.example"}}
	if s := v6.Inbound(41002)["settings"].(map[string]any); s["address"] != "2001:db8::80" || s["port"] != 8443 {
		t.Fatalf("IPv6 dest %v", s)
	}
	ordered, _ := json.Marshal(GuardRules(map[int]Guard{41003: g, 41001: g}))
	if strings.Index(string(ordered), "flvxg-41001-allow") > strings.Index(string(ordered), "flvxg-41003-allow") {
		t.Fatalf("rules not in port order: %s", ordered)
	}

	yes := true
	n := Node{ID: "7", Port: 30010, Protocol: "vless_reality_vision", Enabled: &yes, VLESS: &VLESS{UUID: "12345678-1234-4234-8234-123456789abc", Flow: "xtls-rprx-vision",
		Reality: Reality{Dest: "203.0.113.80:443", ServerNames: []string{"www.example.com"}, PrivateKey: "k", ShortIDs: []string{"", "a1"}}}}
	plain := Inbound(n)
	guarded, _ := json.Marshal(WithGuardDest(plain, 41001))
	if string(guarded) != `{"listen":"0.0.0.0","port":30010,"protocol":"vless","settings":{"clients":[{"email":"flvxl-7","flow":"xtls-rprx-vision","id":"12345678-1234-4234-8234-123456789abc"}],"decryption":"none"},"sniffing":{"destOverride":["http","tls"],"domainsExcluded":["regexp:.*"],"enabled":true,"routeOnly":true},"streamSettings":{"network":"tcp","realitySettings":{"dest":"127.0.0.1:41001","privateKey":"k","serverNames":["www.example.com"],"shortIds":["","a1"]},"security":"reality","sockopt":{"customSockopt":[{"level":"6","network":"tcp","opt":"9","system":"linux","type":"int","value":"5"}]}},"tag":"flvxl-7"}` {
		t.Fatalf("guarded inbound %s", guarded)
	}
	// The rendered node itself is not modified.
	if raw, _ := json.Marshal(plain); strings.Contains(string(raw), "127.0.0.1") || strings.Contains(string(raw), "sockopt") {
		t.Fatalf("WithGuardDest mutated its input: %s", raw)
	}
	if GuardPortOf(string(guarded)) != 41001 {
		t.Fatal("guard port not found")
	}
	if raw, _ := json.Marshal(plain); GuardPortOf(string(raw)) != 0 {
		t.Fatal("real dest read as a guard")
	}
	// Non-Reality inbounds are returned unchanged.
	ss := map[string]any{"tag": "flvxl-1", "protocol": "shadowsocks"}
	if out := WithGuardDest(ss, 41001); len(out) != 2 || out["streamSettings"] != nil {
		t.Fatalf("non-Reality inbound changed: %v", out)
	}
}

func TestGuardNamesAndKeys(t *testing.T) {
	a := Guard{Dest: "203.0.113.80:443", Names: GuardNames([]string{"B.example", "a.example", "b.example"})}
	b := Guard{Dest: "203.0.113.80:443", Names: GuardNames([]string{"a.example", "b.EXAMPLE"})}
	if a.Key() != b.Key() || strings.Join(a.Names, ",") != "a.example,b.example" {
		t.Fatalf("same group differs: %q %q", a.Key(), b.Key())
	}
	other := Guard{Dest: "203.0.113.81:443", Names: a.Names}
	if other.Key() == a.Key() {
		t.Fatal("different dests share a group")
	}
}

func TestRealityGuardStatusValid(t *testing.T) {
	var none *RealityGuardStatus
	if !none.Valid() {
		t.Fatal("absent status rejected")
	}
	for _, s := range []RealityGuardStatus{{State: "on", Nodes: 3}, {State: "restart_required"}, {State: "error", Error: "x"}} {
		if !s.Valid() {
			t.Fatalf("%+v rejected", s)
		}
	}
	for _, s := range []RealityGuardStatus{{State: "applied"}, {State: "on", Nodes: -1}, {State: "on", Nodes: MaxNodes + 1}, {State: "error", Error: strings.Repeat("错", 201)}} {
		if s.Valid() {
			t.Fatalf("%+v accepted", s)
		}
	}
}

// A payload without the switch signs exactly as before the contract, so
// Helpers that predate it keep verifying Custom's signature.
func TestGuardAbsentPayloadUnchanged(t *testing.T) {
	type oldDesired struct {
		Revision int64   `json:"revision"`
		Nodes    []Node  `json:"nodes"`
		Egress   *Egress `json:"egress,omitempty"`
	}
	d := Desired{Revision: 3, Nodes: []Node{}}
	now, _ := json.Marshal(d)
	old, _ := json.Marshal(oldDesired{Revision: 3, Nodes: []Node{}})
	if string(now) != string(old) || d.GuardOn() {
		t.Fatalf("absent switch changed the payload: %s vs %s", now, old)
	}
	off := false
	d.RealityGuard = &off
	if raw, _ := json.Marshal(d); !strings.Contains(string(raw), `"reality_guard":false`) || d.GuardOn() {
		t.Fatalf("explicit off: %s", raw)
	}
	on := true
	d.RealityGuard = &on
	if !d.GuardOn() {
		t.Fatal("on not reported")
	}
	e := Envelope{Payload: Payload{ServerID: "7", Desired: d}, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	e.Sign("token")
	if !e.Verify("token", "7", time.Now()) {
		t.Fatal("signature with the switch does not verify")
	}
}
