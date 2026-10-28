package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

type fakeRateNft struct {
	applied  []string
	removes  int
	presents int
	checks   int
	missing  bool
	fail     error
	checkErr error
}

func (f *fakeRateNft) ops() flvxRateNft {
	return flvxRateNft{
		apply: func(_ context.Context, script []byte) error {
			if f.fail != nil {
				return f.fail
			}
			f.applied = append(f.applied, string(script))
			f.missing = false
			return nil
		},
		remove:  func(context.Context) error { f.removes++; f.missing = true; return nil },
		present: func(context.Context) bool { f.presents++; return !f.missing },
		check:   func(context.Context, []byte) error { f.checks++; return f.checkErr },
	}
}

func TestFLVXRateRender(t *testing.T) {
	got := renderFLVXRateNft([]flvxRateOwner{{Owner: "26", Bytes: 50 * flvx.RateBytesPerMbps, TCP: []int{30001, 30002}, UDP: []int{30001}}, {Owner: "31", Bytes: 1 * flvx.RateBytesPerMbps, TCP: []int{30004}}})
	want := `add table inet mmwxc_flvx_rate
delete table inet mmwxc_flvx_rate
add table inet mmwxc_flvx_rate
add chain inet mmwxc_flvx_rate input { type filter hook input priority 0; policy accept; }
add chain inet mmwxc_flvx_rate output { type filter hook output priority 0; policy accept; }
add limit inet mmwxc_flvx_rate u_3236_up { rate over 6553600 bytes/second }
add limit inet mmwxc_flvx_rate u_3236_down { rate over 6553600 bytes/second }
add rule inet mmwxc_flvx_rate input tcp dport { 30001, 30002 } limit name "u_3236_up" drop
add rule inet mmwxc_flvx_rate output tcp sport { 30001, 30002 } limit name "u_3236_down" drop
add rule inet mmwxc_flvx_rate input udp dport { 30001 } limit name "u_3236_up" drop
add rule inet mmwxc_flvx_rate output udp sport { 30001 } limit name "u_3236_down" drop
add limit inet mmwxc_flvx_rate u_3331_up { rate over 131072 bytes/second }
add limit inet mmwxc_flvx_rate u_3331_down { rate over 131072 bytes/second }
add rule inet mmwxc_flvx_rate input tcp dport { 30004 } limit name "u_3331_up" drop
add rule inet mmwxc_flvx_rate output tcp sport { 30004 } limit name "u_3331_down" drop
`
	if got != want {
		t.Fatalf("render:\n%s", got)
	}
	if renderFLVXRateNft(nil) != "" {
		t.Fatal("empty owners render a table")
	}
	// Owner ids are restricted to [A-Za-z0-9_-]; hex keeps every name valid.
	if name := flvxRateObject("a-B_9", "up"); name != "u_612d425f39_up" {
		t.Fatal(name)
	}
}

// Steady state makes no nft call except the presence check once a minute; a
// change replaces the table; no rates removes it; a failed apply fails open.
func TestFLVXRateEnsureCalls(t *testing.T) {
	f := &fakeRateNft{}
	now := time.Now()
	r := &flvxRateLimiter{nft: f.ops(), now: func() time.Time { return now }}
	// First pass after a start with nothing to do removes a leftover table once.
	for i := 0; i < 3; i++ {
		if err := r.ensure(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if f.removes != 1 || len(f.applied) != 0 {
		t.Fatalf("idle start: removes=%d applies=%d", f.removes, len(f.applied))
	}
	owners := []flvxRateOwner{{Owner: "26", Bytes: 6553600, TCP: []int{30001}}}
	for i := 0; i < 11; i++ {
		now = now.Add(5 * time.Second)
		if err := r.ensure(context.Background(), owners); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.applied) != 1 || f.presents != 0 {
		t.Fatalf("steady: applies=%d presence checks=%d", len(f.applied), f.presents)
	}
	now = now.Add(10 * time.Second)
	r.ensure(context.Background(), owners)
	if f.presents != 1 || len(f.applied) != 1 {
		t.Fatalf("recheck: %d %d", f.presents, len(f.applied))
	}
	// Someone flushed the ruleset: rebuilt at the next check.
	f.missing = true
	now = now.Add(time.Minute)
	r.ensure(context.Background(), owners)
	if len(f.applied) != 2 {
		t.Fatal("missing table not rebuilt")
	}
	// Rate change: one replacement.
	owners[0].Bytes = 131072
	r.ensure(context.Background(), owners)
	if len(f.applied) != 3 || !strings.Contains(f.applied[2], "rate over 131072 bytes/second") {
		t.Fatal("rate change not applied")
	}
	// Rates gone: table removed, then nothing more.
	removes := f.removes
	r.ensure(context.Background(), nil)
	r.ensure(context.Background(), nil)
	if f.removes != removes+1 {
		t.Fatal("table not removed exactly once")
	}
	// Apply failure: table dropped (fail open), retried next pass.
	f.fail = errors.New("nft: no such file")
	if err := r.ensure(context.Background(), owners); err == nil || f.removes != removes+2 {
		t.Fatal("failed apply not cleaned up")
	}
	f.fail = nil
	if err := r.ensure(context.Background(), owners); err != nil || len(f.applied) != 4 {
		t.Fatal("not retried after a failure")
	}
}

func TestFLVXRateProbeCached(t *testing.T) {
	f := &fakeRateNft{checkErr: errors.New("no nft")}
	now := time.Now()
	r := &flvxRateLimiter{nft: f.ops(), now: func() time.Time { return now }}
	for i := 0; i < 5; i++ {
		if r.supported(context.Background()) {
			t.Fatal("unsupported probe reported supported")
		}
	}
	if f.checks != 1 {
		t.Fatalf("probe ran %d times", f.checks)
	}
	f.checkErr = nil
	now = now.Add(flvxRateProbeRetry)
	if !r.supported(context.Background()) || !r.supported(context.Background()) || f.checks != 2 {
		t.Fatalf("probe retry: %d", f.checks)
	}
	var none *flvxManager
	if none.rateCapable(context.Background()) {
		t.Fatal("nil manager rate capable")
	}
}

// Only owners with a 速率, only nodes running in the FLVX Core now, UDP ports
// only for SS2022 / Mieru; another owner without a rate is untouched; the
// report says which owners' rates are in force; expiry removes the table.
func TestFLVXRateOwnersAndExpiry(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	f := &fakeRateNft{}
	m.rate = &flvxRateLimiter{nft: f.ops(), now: m.now}
	vless := helperRealityNode("a3", 30005, "203.0.113.80:443", "www.example.com")
	vless.Owner = flvx.Owner{ID: "26", Name: "u"}
	m.state.Payload.Desired.Nodes = []flvx.Node{ownedNode("a1", 30001, "26"), ownedNode("a2", 30002, "26"), vless, ownedNode("b1", 30004, "31")}
	m.state.Applied = map[string]string{"flvxl-a1": "{}", "flvxl-a3": "{}", "flvxl-b1": "{}"}
	m.submitUserLimits(signedBudgets(m, now, 1, nil, flvx.UserLimitBudget{UserID: "26", MaxRateMbps: ulInt(50)}, flvx.UserLimitBudget{UserID: "31", MaxInboundConnections: ulInt64(3)}))
	owners := m.rateOwners()
	if !reflect.DeepEqual(owners, []flvxRateOwner{{Owner: "26", Bytes: 6553600, TCP: []int{30001, 30005}, UDP: []int{30001}}}) {
		t.Fatalf("owners %+v", owners)
	}
	m.report = &flvx.Report{}
	m.reconcileRates(context.Background())
	if len(f.applied) != 1 || strings.Contains(f.applied[0], "30004") || strings.Contains(f.applied[0], "30002") {
		t.Fatalf("applied %v", f.applied)
	}
	m.userLimits.users = []flvx.UserLimitUsage{{UserID: "26", OnlineIPs: []string{}}, {UserID: "31", OnlineIPs: []string{}}}
	u := m.userLimitsUpload()
	if !u.Users[0].RateApplied || u.Users[1].RateApplied {
		t.Fatalf("rate_applied %+v", u.Users)
	}
	// Paused share: no rates.
	m.state.Payload.Status = "paused"
	if m.rateOwners() != nil {
		t.Fatal("paused share keeps rates")
	}
	m.state.Payload.Status = "active"
	// Budgets expire: the table goes in the next pass.
	now = now.Add(flvx.UserLimitsTTL)
	removes := f.removes
	m.reconcileRates(context.Background())
	if f.removes != removes+1 || m.userLimitsUpload().Users[0].RateApplied {
		t.Fatal("expired rate kept")
	}
}
