package flvx

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"
)

func testNode() Node {
	enabled := true
	return Node{ID: "123", Owner: Owner{ID: "7", Name: "Alice"}, Name: "SS", Protocol: "ss2022", Port: 30001, Enabled: &enabled, SS2022: &SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: base64.StdEncoding.EncodeToString(make([]byte, 16)), UserKey: base64.StdEncoding.EncodeToString(make([]byte, 16))}}
}

func TestValidation(t *testing.T) {
	c := Config{PortMin: 30000, PortMax: 31000, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := ValidateConfig(c); err != nil {
		t.Fatal(err)
	}
	n := testNode()
	d := Desired{Revision: 1, Nodes: []Node{n}}
	if e := ValidateDesired(d, c, nil); len(e) > 0 {
		t.Fatal(e)
	}
	for name, mutate := range map[string]func(*Node){
		"outside": func(n *Node) { n.Port = 29999 }, "official": func(n *Node) { n.Port = 30003 },
		"raw id": func(n *Node) { n.ID = "../evil" }, "protocol": func(n *Node) { n.Protocol = "socks" },
		"key":      func(n *Node) { n.SS2022 = &SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: "bad", UserKey: "bad"} },
		"required": func(n *Node) { n.Enabled = nil }, "owner": func(n *Node) { n.Owner.Name = "" },
	} {
		t.Run(name, func(t *testing.T) {
			n := testNode()
			mutate(&n)
			if len(ValidateDesired(Desired{Revision: 1, Nodes: []Node{n}}, c, []int{30003})) == 0 {
				t.Fatal("accepted invalid node")
			}
		})
	}
	if len(ValidateDesired(Desired{Revision: 1, Nodes: []Node{n, n}}, c, nil)) == 0 {
		t.Fatal("duplicate accepted")
	}
	n.Protocol = "vless_reality_vision"
	n.SS2022 = nil
	n.VLESS = &VLESS{UUID: "12345678-1234-4234-8234-123456789abc", Flow: "xtls-rprx-vision", Reality: Reality{Dest: "www.lovelive-anime.jp:443", ServerNames: []string{"www.lovelive-anime.jp"}, PrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortIDs: []string{""}}}
	if e := ValidateDesired(Desired{Revision: 1, Nodes: []Node{n}}, c, nil); len(e) > 0 {
		t.Fatal(e)
	}
	n.VLESS.Reality.Dest = "127.0.0.1:443"
	n.VLESS.UUID = "invalid"
	n.VLESS.Reality.PrivateKey = "invalid"
	if len(ValidateDesired(Desired{Revision: 1, Nodes: []Node{n}}, c, nil)) < 3 {
		t.Fatal("invalid Reality accepted")
	}
}

func TestCountersAndEnforcement(t *testing.T) {
	c := Counter{}
	for _, v := range []struct {
		epoch       string
		u, d, total int64
	}{{"a", 100, 20, 120}, {"a", 100, 20, 120}, {"a", 150, 30, 180}, {"b", 10, 2, 192}, {"b", 1, 1, 194}} {
		c.Merge(v.epoch, v.u, v.d)
		if c.Up+c.Down != v.total {
			t.Fatalf("counter = %+v", c)
		}
		b, _ := json.Marshal(c)
		c = Counter{}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
	}
	n := testNode()
	cfg := Config{TrafficLimitBytes: 100}
	now := time.Now()
	if !Active("active", cfg, 99, n, now) || Active("active", cfg, 100, n, now) || Active("paused", cfg, 0, n, now) || Active("revoked", cfg, 0, n, now) {
		t.Fatal("quota or pause bypass")
	}
	end := now.Add(-time.Second).UnixMilli()
	n.ExpiresAt = &end
	if Active("active", cfg, 0, n, now) {
		t.Fatal("expired node admitted")
	}
	if Period(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), 31) != "2026-02-28" {
		t.Fatal("month end reset")
	}
	c = Counter{Up: math.MaxInt64 - 1}
	c.Merge("new", 10, 0)
	if c.Up != math.MaxInt64 || AddBytes(c.Up, 100) != math.MaxInt64 {
		t.Fatal("byte counter overflow decreased usage")
	}
}

func TestEnvelopeAndRouting(t *testing.T) {
	now := time.Now()
	e := Envelope{Payload: Payload{ServerID: "7", Version: 1}, ExpiresAt: now.Add(time.Minute).UnixMilli()}
	e.Sign("helper")
	if !e.Verify("helper", "7", now) || e.Verify("operator", "7", now) || e.Verify("helper", "8", now) || e.Verify("helper", "7", now.Add(2*time.Minute)) {
		t.Fatal("invalid envelope accepted")
	}
	e.Payload.Config.PortMin = 1
	if e.Verify("helper", "7", now) {
		t.Fatal("tampering accepted")
	}
	rules := BaseConfig(40000)["routing"].(map[string]any)["rules"].([]any)
	if !reflect.DeepEqual(rules[0].(map[string]any)["ip"], BlockCIDRs) {
		t.Fatal("first rule must block all CIDRs")
	}
	for _, s := range []string{"10.1.2.3", "127.0.0.1", "169.254.169.254", "::ffff:127.0.0.1", "ff02::1", "::"} {
		if !PrivateHost(s) {
			t.Fatalf("not private: %s", s)
		}
	}
	ports, err := OfficialPorts([]byte(`{"inbounds":[{"port":"30001-30003"},{"port":443},{"port":"443"}]}`))
	if err != nil || !reflect.DeepEqual(ports, []int{30001, 30002, 30003, 443}) {
		t.Fatalf("ports %v: %v", ports, err)
	}
}
