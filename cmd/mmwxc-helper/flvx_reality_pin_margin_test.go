package main

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// reality_pin v2 (FLVX plan 234): the pin moves only when the best IP is
// faster by at least Desired.reality_pin_margin_ms (absent = 20; 0 = any
// gain, a tie keeps the pin); a failed IP moves whatever the margin. The
// moved pin reports move_reason until the dest's next full check.
func TestFLVXRealityPinMargin(t *testing.T) {
	margin := func(v int) *int { return &v }
	for _, c := range []struct {
		name   string
		margin *int
		gap    float64 // pinned .2 minus best .1, ms; -1 = .2 failed
		moves  bool
		reason string
	}{
		{"default keeps 19", nil, 19, false, ""},
		{"default moves 20", nil, 20, true, flvx.RealityPinMoveFaster},
		{"35 keeps 34", margin(35), 34, false, ""},
		{"35 moves 35", margin(35), 35, true, flvx.RealityPinMoveFaster},
		{"0 keeps a tie", margin(0), 0, false, ""},
		{"0 moves 0.1", margin(0), 0.1, true, flvx.RealityPinMoveFaster},
		{"200 keeps 199", margin(200), 199, false, ""},
		{"200 moves 200", margin(200), 200, true, flvx.RealityPinMoveFaster},
		{"200 fails over", margin(200), -1, true, flvx.RealityPinMoveFailover},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
			m.state.Payload.Desired.RealityPinMarginMS = c.margin
			p.answer = []string{"203.0.113.1", "203.0.113.2"}
			p.ms = map[string]float64{"203.0.113.1": 500, "203.0.113.2": 10}
			reconcileOK(t, m)
			if appliedDest(t, m, "flvxl-r") != "203.0.113.2:443" || m.state.Report.RealityPins[0].MoveReason != "" {
				t.Fatalf("first pin %s %+v", appliedDest(t, m, "flvxl-r"), m.state.Report.RealityPins)
			}
			p.ms = map[string]float64{"203.0.113.1": 10, "203.0.113.2": 10 + c.gap}
			if c.gap < 0 {
				p.ms["203.0.113.2"] = -1
			}
			p.advance(flvxPinRecheck)
			reconcileOK(t, m)
			want := "203.0.113.2:443"
			if c.moves {
				want = "203.0.113.1:443"
			}
			pins := m.state.Report.RealityPins
			if got := appliedDest(t, m, "flvxl-r"); got != want || len(pins) != 1 || pins[0].MoveReason != c.reason {
				t.Fatalf("pinned %s, status %+v", got, pins)
			}
			// The next full check without a move clears move_reason.
			p.advance(flvxPinRecheck)
			reconcileOK(t, m)
			if pins := m.state.Report.RealityPins; appliedDest(t, m, "flvxl-r") != want || pins[0].MoveReason != "" {
				t.Fatalf("after the next check %+v", pins)
			}
		})
	}
}

// Leaving a fallback pin (nothing answered before) is a failover, not a
// margin decision; the Helper advertises flvx.reality_pin.v2.
func TestFLVXRealityPinMarginFallbackAndCapability(t *testing.T) {
	m, _, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
	v := 200
	m.state.Payload.Desired.RealityPinMarginMS = &v
	p.answer = []string{"203.0.113.9", "203.0.113.1"}
	reconcileOK(t, m)
	p.ms = map[string]float64{"203.0.113.9": 7, "203.0.113.1": 8}
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	if pins := m.state.Report.RealityPins; appliedDest(t, m, "flvxl-r") != "203.0.113.9:443" || pins[0].MoveReason != flvx.RealityPinMoveFailover {
		t.Fatalf("fallback recovery %+v", pins)
	}
	if !slices.Contains(helperCapabilities, flvx.RealityPinMarginCapability) || !slices.Contains(helperCapabilities, flvx.RealityPinCapability) {
		t.Fatal("reality_pin v1 + v2 not advertised")
	}
}

// A pin that left the DNS answer but still answers moves to the answer's
// fastest IP whatever the margin, as FLVX's agent does (choosePin keeps a pin
// only while it is in the answer); move_reason is "failover". The Helper
// waits until no full check's answer had it for 12 h (the first check 6 h
// later keeps it). A pin still in the answer within the margin stays.
func TestFLVXRealityPinLeftDNSAnswer(t *testing.T) {
	margin := func(v int) *int { return &v }
	for _, c := range []struct {
		name   string
		margin *int
		answer []string
		moves  bool
		reason string
	}{
		{"20 leaves the answer", margin(20), []string{"203.0.113.1", "203.0.113.3"}, true, flvx.RealityPinMoveFailover},
		{"200 leaves the answer", margin(200), []string{"203.0.113.1", "203.0.113.3"}, true, flvx.RealityPinMoveFailover},
		{"20 still in the answer", margin(20), []string{"203.0.113.1", "203.0.113.2"}, false, ""},
		{"200 still in the answer", margin(200), []string{"203.0.113.1", "203.0.113.2"}, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
			m.state.Payload.Desired.RealityPinMarginMS = c.margin
			p.answer = []string{"203.0.113.1", "203.0.113.2"}
			p.ms = map[string]float64{"203.0.113.1": 500, "203.0.113.2": 10}
			reconcileOK(t, m)
			// The pinned .2 is now 5 ms slower than .1 (under either margin);
			// .3 is the slowest.
			p.answer = c.answer
			p.ms = map[string]float64{"203.0.113.1": 10, "203.0.113.2": 15, "203.0.113.3": 40}
			p.advance(flvxPinRecheck)
			reconcileOK(t, m)
			if got := appliedDest(t, m, "flvxl-r"); got != "203.0.113.2:443" {
				t.Fatalf("moved 6 h after the last answer: %s", got)
			}
			p.advance(flvxPinRecheck)
			reconcileOK(t, m)
			want := "203.0.113.2:443"
			if c.moves {
				want = "203.0.113.1:443"
			}
			pins := m.state.Report.RealityPins
			if got := appliedDest(t, m, "flvxl-r"); got != want || len(pins) != 1 || pins[0].MoveReason != c.reason || pins[0].BestIP != "203.0.113.1" {
				t.Fatalf("pinned %s, status %+v", got, pins)
			}
		})
	}
}

// "Left the answer" needs the last full check's own answer: not after a failed
// lookup, not from a pre-v0.7.24 state (no dns_at), and an unguarded busy
// node waits for idle as for a faster IP.
func TestFLVXRealityPinLeftDNSAnswerNeedsAnswer(t *testing.T) {
	m, f, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
	p.answer = []string{"203.0.113.1", "203.0.113.2"}
	p.ms = map[string]float64{"203.0.113.1": 20, "203.0.113.2": 10}
	reconcileOK(t, m)
	// State written by v0.7.23: no dns_at, a DNS list without the pin.
	d := m.state.RealityDests["d.example:443"]
	d.DNSAt, d.DNS = 0, []string{"203.0.113.1"}
	m.state.RealityDests["d.example:443"] = d
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.2:443" {
		t.Fatal("moved on a pre-v0.7.24 state")
	}
	// The lookup fails: the old answer is not this check's, the pin stays.
	p.answer, p.dnsErr = nil, errors.New("timeout")
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.2:443" || m.state.RealityDests["d.example:443"].answer() != nil {
		t.Fatal("moved after a failed lookup")
	}
	// Busy and unguarded: waits until idle, then moves (failover).
	f.snap.Users = []coreUserSnapshot{{Identity: coreIdentity{InboundTag: "flvxl-r", User: "flvxl-r"}, CurrentTotal: 4}}
	reconcileOK(t, m)
	p.answer, p.dnsErr = []string{"203.0.113.1"}, nil
	f.adds, f.removes, f.blocks = 0, 0, 0
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	if appliedDest(t, m, "flvxl-r") != "203.0.113.2:443" || f.adds+f.removes+f.blocks != 0 {
		t.Fatalf("busy node re-added: adds=%d removes=%d blocks=%d", f.adds, f.removes, f.blocks)
	}
	f.snap.Users = nil
	reconcileOK(t, m)
	reconcileOK(t, m)
	if pins := m.state.Report.RealityPins; appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" || pins[0].MoveReason != flvx.RealityPinMoveFailover {
		t.Fatalf("idle node did not move: %+v", pins)
	}
}

// No IP of the answer answers: best falls back to any reachable candidate,
// so a failed pin still moves to a remembered IP that answers.
func TestFLVXRealityPinAnswerAllDown(t *testing.T) {
	m, _, p := pinFixture(t, helperRealityNode("r", 30010, "d.example:443", "d.example"))
	p.answer = []string{"203.0.113.1", "203.0.113.2"}
	p.ms = map[string]float64{"203.0.113.1": 10, "203.0.113.2": 20}
	reconcileOK(t, m)
	p.answer = []string{"203.0.113.3"}
	p.ms = map[string]float64{"203.0.113.1": -1, "203.0.113.2": 20, "203.0.113.3": -1}
	p.advance(flvxPinRecheck)
	reconcileOK(t, m)
	if pins := m.state.Report.RealityPins; appliedDest(t, m, "flvxl-r") != "203.0.113.2:443" || pins[0].MoveReason != flvx.RealityPinMoveFailover {
		t.Fatalf("failed pin kept: %s %+v", appliedDest(t, m, "flvxl-r"), pins)
	}
}

// The 12 h counts from the pin's last answer, not in checks: extra full checks
// (here forced as after a failed liveness check) up to 11 h 59 min keep it,
// the one at 12 h moves it. An unreachable pin moves at once.
func TestFLVXRealityPinLeftDNSAnswerAfter12h(t *testing.T) {
	const dest = "d.example:443"
	m, _, p := pinFixture(t, helperRealityNode("r", 30010, dest, "d.example"))
	p.answer = []string{"203.0.113.1", "203.0.113.2"}
	p.ms = map[string]float64{"203.0.113.1": 500, "203.0.113.2": 10}
	reconcileOK(t, m)
	seen := p.now
	p.answer = []string{"203.0.113.1", "203.0.113.3"}
	p.ms = map[string]float64{"203.0.113.1": 10, "203.0.113.2": 15, "203.0.113.3": 40}
	check := func(at time.Duration) []flvx.RealityPinStatus {
		t.Helper()
		p.advance(seen.Add(at).Sub(p.now))
		d := m.state.RealityDests[dest]
		d.Due = true
		m.state.RealityDests[dest] = d
		reconcileOK(t, m)
		if got := m.state.RealityDests[dest].CheckedAt; got != p.now.UnixMilli() {
			t.Fatalf("no full check at %s", at)
		}
		return m.state.Report.RealityPins
	}
	for _, at := range []time.Duration{flvxPinRecheck, 9 * time.Hour, flvxPinGone - time.Minute} {
		if pins := check(at); appliedDest(t, m, "flvxl-r") != "203.0.113.2:443" || pins[0].MoveReason != "" {
			t.Fatalf("moved %s after the last answer: %s %+v", at, appliedDest(t, m, "flvxl-r"), pins)
		}
	}
	if pins := check(flvxPinGone); appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" || pins[0].MoveReason != flvx.RealityPinMoveFailover {
		t.Fatalf("kept 12 h after the last answer: %s %+v", appliedDest(t, m, "flvxl-r"), pins)
	}

	// Under 12 h but unreachable: liveness fails, the full check moves it.
	m, _, p = pinFixture(t, helperRealityNode("r", 30010, dest, "d.example"))
	p.answer = []string{"203.0.113.1", "203.0.113.2"}
	p.ms = map[string]float64{"203.0.113.1": 500, "203.0.113.2": 10}
	reconcileOK(t, m)
	p.answer = []string{"203.0.113.1", "203.0.113.3"}
	p.ms = map[string]float64{"203.0.113.1": 10, "203.0.113.2": -1, "203.0.113.3": 40}
	p.advance(flvxPinLiveness)
	reconcileOK(t, m)
	if pins := m.state.Report.RealityPins; appliedDest(t, m, "flvxl-r") != "203.0.113.1:443" || pins[0].MoveReason != flvx.RealityPinMoveFailover {
		t.Fatalf("unreachable pin kept: %s %+v", appliedDest(t, m, "flvxl-r"), pins)
	}
}
