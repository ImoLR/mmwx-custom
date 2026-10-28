package flvx

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }
func iptr(v int) *int    { return &v }

// The request decodes exactly the JSON of contract §4 / §7.2 (null = nil).
func TestUserLimitsWireContract(t *testing.T) {
	var r UserLimitsRequest
	body := `{"version": 2, "sequence": 1793200000456, "users": [
	 {"user_id": "26", "max_inbound_connections": 40, "server_max_inbound_connections": null, "max_inbound_online_ips": 3, "relay_ip_reserve": 1, "max_rate_mbps": 50},
	 {"user_id": "31", "max_inbound_connections": null, "server_max_inbound_connections": null, "max_inbound_online_ips": null, "relay_ip_reserve": 0, "max_rate_mbps": 20}]}`
	d := json.NewDecoder(strings.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		t.Fatal(err)
	}
	want := UserLimitsRequest{Version: 2, Sequence: 1793200000456, Users: []UserLimitBudget{
		{UserID: "26", MaxInboundConnections: i64(40), MaxInboundOnlineIPs: iptr(3), RelayIPReserve: 1, MaxRateMbps: iptr(50)},
		{UserID: "31", MaxRateMbps: iptr(20)},
	}}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("decoded %+v", r)
	}
	if problems := ValidateUserLimits(r, true); len(problems) != 0 {
		t.Fatalf("valid request rejected: %+v", problems)
	}
	// A version-1 body never carries the rate field.
	v1, _ := json.Marshal(UserLimitBudget{UserID: "26"})
	if string(v1) != `{"user_id":"26","max_inbound_connections":null,"server_max_inbound_connections":null,"max_inbound_online_ips":null,"relay_ip_reserve":0}` {
		t.Fatalf("v1 budget %s", v1)
	}
	report, _ := json.Marshal(UserLimitsReport{AppliedSequence: 7, CollectedAt: 9, Users: []UserLimitUsage{{UserID: "26", InboundConnections: 12, RelayConnections: 30, OnlineIPs: []string{"203.0.113.9"}}}})
	if string(report) != `{"applied_sequence":7,"collected_at":9,"users":[{"user_id":"26","inbound_connections":12,"relay_connections":30,"online_ips":["203.0.113.9"]}]}` {
		t.Fatalf("report %s", report)
	}
}

func TestValidateUserLimits(t *testing.T) {
	ok := func(users ...UserLimitBudget) UserLimitsRequest {
		return UserLimitsRequest{Version: 1, Sequence: 1, Users: append([]UserLimitBudget{}, users...)}
	}
	fields := func(r UserLimitsRequest, rate bool) string {
		out := []string{}
		for _, p := range ValidateUserLimits(r, rate) {
			out = append(out, p.Field)
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		name string
		r    UserLimitsRequest
		rate bool
		want string
	}{
		{"empty list", ok(), false, ""},
		{"zero budgets are full, not invalid", ok(UserLimitBudget{UserID: "26", MaxInboundConnections: i64(0), ServerMaxInboundConnections: i64(0), MaxInboundOnlineIPs: iptr(0)}), false, ""},
		{"version 0", UserLimitsRequest{Version: 0, Sequence: 1, Users: []UserLimitBudget{}}, false, "version"},
		{"version 2 without the rate capability", UserLimitsRequest{Version: 2, Sequence: 1, Users: []UserLimitBudget{}}, false, "version"},
		{"version 3", UserLimitsRequest{Version: 3, Sequence: 1, Users: []UserLimitBudget{}}, true, "version"},
		{"sequence", UserLimitsRequest{Version: 1, Sequence: 0, Users: []UserLimitBudget{}}, false, "sequence"},
		{"users null", UserLimitsRequest{Version: 1, Sequence: 1}, false, "users"},
		{"duplicate user", ok(UserLimitBudget{UserID: "26"}, UserLimitBudget{UserID: "26"}), false, "users"},
		{"empty user", ok(UserLimitBudget{UserID: ""}), false, "users"},
		{"bad user id", ok(UserLimitBudget{UserID: "a b"}), false, "users"},
		{"negative connections", ok(UserLimitBudget{UserID: "1", MaxInboundConnections: i64(-1)}), false, "users"},
		{"negative server cap", ok(UserLimitBudget{UserID: "1", ServerMaxInboundConnections: i64(-1)}), false, "users"},
		{"negative IPs", ok(UserLimitBudget{UserID: "1", MaxInboundOnlineIPs: iptr(-1)}), false, "users"},
		{"reserve", ok(UserLimitBudget{UserID: "1", RelayIPReserve: 4097}), false, "users"},
		{"rate with version 1", ok(UserLimitBudget{UserID: "1", MaxRateMbps: iptr(5)}), true, "users"},
		{"rate 0", UserLimitsRequest{Version: 2, Sequence: 1, Users: []UserLimitBudget{{UserID: "1", MaxRateMbps: iptr(0)}}}, true, "users"},
		{"rate too high", UserLimitsRequest{Version: 2, Sequence: 1, Users: []UserLimitBudget{{UserID: "1", MaxRateMbps: iptr(100001)}}}, true, "users"},
		{"rate ok", UserLimitsRequest{Version: 2, Sequence: 1, Users: []UserLimitBudget{{UserID: "1", MaxRateMbps: iptr(100000)}}}, true, ""},
	}
	for _, c := range cases {
		if got := fields(c.r, c.rate); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	many := make([]UserLimitBudget, MaxUserLimitUsers+1)
	if fields(ok(many...), false) != "users" {
		t.Fatal("more than 10000 users accepted")
	}
}

func TestConnModeValidation(t *testing.T) {
	yes := true
	node := Node{ID: "1", Owner: Owner{ID: "26", Name: "u"}, Name: "n", Protocol: "ss2022", Port: 30001, Enabled: &yes, MaxConnections: 10,
		SS2022: &SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: "AAAAAAAAAAAAAAAAAAAAAA==", UserKey: "AAAAAAAAAAAAAAAAAAAAAA=="}}
	cfg := Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	for mode, valid := range map[string]bool{"": true, "total": true, "inbound": true, "outbound": false, "Inbound": false} {
		n := node
		n.ConnMode = mode
		problems := ValidateDesired(Desired{Revision: 1, Nodes: []Node{n}}, cfg, nil)
		if (len(problems) == 0) != valid || !valid && problems[0].Field != "conn_mode" {
			t.Errorf("conn_mode %q: %+v", mode, problems)
		}
	}
	// Absent conn_mode keeps the node JSON (and the signed payload) unchanged.
	raw, _ := json.Marshal(node)
	if strings.Contains(string(raw), "conn_mode") {
		t.Fatalf("empty conn_mode serialised: %s", raw)
	}
	if status, _ := json.Marshal(NodeStatus{ID: "1", State: "active"}); strings.Contains(string(status), "inbound_connections") {
		t.Fatalf("absent inbound_connections serialised: %s", status)
	}
}

func TestUserLimitsEnvelope(t *testing.T) {
	now := time.Now()
	e := UserLimitsEnvelope{Payload: UserLimitsPayload{ServerID: "5", Sequence: 9, Users: []UserLimitBudget{{UserID: "26", MaxInboundConnections: i64(3)}}, RelaySources: []string{"127.0.0.1", "::1"}}, ExpiresAt: now.Add(UserLimitsTTL).UnixMilli()}
	e.Sign("tok")
	if !e.Verify("tok", "5", now) {
		t.Fatal("valid envelope rejected")
	}
	// Its own domain: a desired-envelope signature over the same bytes differs.
	d := Envelope{ExpiresAt: e.ExpiresAt}
	d.Sign("tok")
	if d.Signature == e.Signature {
		t.Fatal("user limits share the desired signing domain")
	}
	for name, bad := range map[string]func(*UserLimitsEnvelope){
		"server":   func(x *UserLimitsEnvelope) { x.Payload.ServerID = "6" },
		"budget":   func(x *UserLimitsEnvelope) { v := int64(4); x.Payload.Users[0].MaxInboundConnections = &v },
		"relay":    func(x *UserLimitsEnvelope) { x.Payload.RelaySources = []string{"203.0.113.1"} },
		"expired":  func(x *UserLimitsEnvelope) { x.ExpiresAt = now.Add(-time.Second).UnixMilli() },
		"too far":  func(x *UserLimitsEnvelope) { x.ExpiresAt = now.Add(12 * time.Minute).UnixMilli() },
		"sequence": func(x *UserLimitsEnvelope) { x.Payload.Sequence = 0 },
	} {
		x := e
		x.Payload.Users = append([]UserLimitBudget(nil), e.Payload.Users...)
		bad(&x)
		if name == "expired" || name == "too far" || name == "sequence" {
			x.Sign("tok")
		}
		if x.Verify("tok", "5", now) {
			t.Errorf("%s accepted", name)
		}
	}
	if e.Verify("other", "5", now) {
		t.Fatal("wrong token accepted")
	}
}

func TestRelaySourcesAndCanonicalIP(t *testing.T) {
	got := RelaySources([]string{"flvx.example.com", "::ffff:203.0.113.7", "203.0.113.7", "[2001:db8::1]", " 198.51.100.2 ", "127.0.0.1"})
	want := []string{"127.0.0.1", "198.51.100.2", "2001:db8::1", "203.0.113.7", "::1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("relay sources %v", got)
	}
	hosts := make([]string, 0, MaxRelaySources+10)
	for i := 0; i < MaxRelaySources+10; i++ {
		hosts = append(hosts, "10.1."+itoa(i/256)+"."+itoa(i%256))
	}
	capped := RelaySources(hosts)
	if len(capped) != MaxRelaySources+2 || !contains(capped, "127.0.0.1") || !contains(capped, "::1") {
		t.Fatalf("cap dropped loopback or size %d", len(capped))
	}
	if ip, ok := CanonicalIP("::ffff:10.0.0.1"); !ok || ip != "10.0.0.1" {
		t.Fatalf("mapped %q", ip)
	}
	if _, ok := CanonicalIP("example.com"); ok {
		t.Fatal("name accepted as IP")
	}
	if !ValidUserLimitsPayload(UserLimitsPayload{Sequence: 1, RelaySources: []string{"127.0.0.1"}}) {
		t.Fatal("empty payload rejected")
	}
	if ValidUserLimitsPayload(UserLimitsPayload{Sequence: 1, RelaySources: []string{"name"}}) {
		t.Fatal("non-IP relay source accepted")
	}
	if UserGroup("26") != "flvx-user-26" {
		t.Fatal("group name")
	}
}

func itoa(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
