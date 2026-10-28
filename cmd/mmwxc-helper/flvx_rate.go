package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Per-user 速率 on the FLVX Core instance (contract landing-user-limits-v1
// §7, version 2). The Core has no bandwidth limiter, so the Helper polices in
// the kernel (contract §7.4 alternative): one nftables table of its own with
// a named limit object per owner and direction, shared by the rules matching
// that owner's FLVX node ports — client → node (upload) at input by
// destination port, node → client (download) at output by source port, TCP
// and UDP. Relayed traffic arrives on the same ports and is included, as the
// contract asks. Only FLVX node ports are matched, never mmwx's own Core.
// The table is replaced atomically and only when its content changes, so a
// rate change needs no Core restart and closes no connection.
const (
	flvxRateTable = "mmwxc_flvx_rate"
	// The table's presence is re-checked with one nft call at this interval
	// (someone may flush the ruleset); otherwise steady state makes no call.
	flvxRateRecheck = time.Minute
	// A failed probe (nft missing, kernel without limit objects) is retried
	// this rarely; until it succeeds flvx.user_rate.v1 is not advertised.
	flvxRateProbeRetry = 10 * time.Minute
	// Burst: a quarter second of the rate (at least 64 KiB) keeps TCP close
	// to the rate without letting a burst run far over it.
	flvxRateMinBurst = 64 << 10
)

// flvxRateOwner is one owner's 速率 and the ports of its live nodes.
type flvxRateOwner struct {
	Owner string
	Bytes int64 // per second, each direction
	TCP   []int
	UDP   []int
}

type flvxRateNft struct {
	apply   func(context.Context, []byte) error
	remove  func(context.Context) error
	present func(context.Context) bool
	check   func(context.Context, []byte) error
}

type flvxRateLimiter struct {
	nft flvxRateNft
	now func() time.Time
	// applied is the script in force ("" = no table); unknown until the first
	// pass after a start, which removes a table a previous process left.
	applied   string
	known     bool
	checkedAt time.Time

	probeMu  sync.Mutex
	probed   bool
	probeOK  bool
	probedAt time.Time
}

func newFLVXRateLimiter() *flvxRateLimiter {
	return &flvxRateLimiter{nft: defaultFLVXRateNft(), now: time.Now}
}

func defaultFLVXRateNft() flvxRateNft {
	run := func(ctx context.Context, script []byte, args ...string) error {
		command := exec.CommandContext(ctx, "nft", args...)
		command.Stdin = bytes.NewReader(script)
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("nft %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	return flvxRateNft{
		apply: func(ctx context.Context, script []byte) error { return run(ctx, script, "-f", "-") },
		check: func(ctx context.Context, script []byte) error { return run(ctx, script, "-c", "-f", "-") },
		remove: func(ctx context.Context) error {
			// Absent is fine; only our own table is ever deleted.
			_ = exec.CommandContext(ctx, "nft", "delete", "table", "inet", flvxRateTable).Run()
			return nil
		},
		present: func(ctx context.Context) bool {
			return exec.CommandContext(ctx, "nft", "list", "table", "inet", flvxRateTable).Run() == nil
		},
	}
}

// flvxRateBurst is the token bucket depth in bytes for a rate.
func flvxRateBurst(rate int64) int64 {
	burst := rate / 4
	if burst < flvxRateMinBurst {
		burst = flvxRateMinBurst
	}
	if burst > 1<<32-1 {
		burst = 1<<32 - 1
	}
	return burst
}

func flvxRateObject(owner, direction string) string {
	return "u_" + hex.EncodeToString([]byte(owner)) + "_" + direction
}

func flvxPortList(ports []int) string {
	values := make([]string, len(ports))
	for i, p := range ports {
		values[i] = strconv.Itoa(p)
	}
	return strings.Join(values, ", ")
}

// renderFLVXRateNft builds one atomic batch that replaces the whole table.
// owners must be sorted with sorted, non-empty port lists; nil = no table.
func renderFLVXRateNft(owners []flvxRateOwner) string {
	if len(owners) == 0 {
		return ""
	}
	table := "inet " + flvxRateTable
	var out strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&out, format, args...)
		out.WriteByte('\n')
	}
	line("add table %s", table)
	line("delete table %s", table)
	line("add table %s", table)
	line("add chain %s input { type filter hook input priority 0; policy accept; }", table)
	line("add chain %s output { type filter hook output priority 0; policy accept; }", table)
	for _, o := range owners {
		burst := flvxRateBurst(o.Bytes)
		up, down := flvxRateObject(o.Owner, "up"), flvxRateObject(o.Owner, "down")
		line("add limit %s %s { rate over %d bytes/second burst %d bytes }", table, up, o.Bytes, burst)
		line("add limit %s %s { rate over %d bytes/second burst %d bytes }", table, down, o.Bytes, burst)
		for _, proto := range []struct {
			name  string
			ports []int
		}{{"tcp", o.TCP}, {"udp", o.UDP}} {
			if len(proto.ports) == 0 {
				continue
			}
			list := flvxPortList(proto.ports)
			line("add rule %s input %s dport { %s } limit name \"%s\" drop", table, proto.name, list, up)
			line("add rule %s output %s sport { %s } limit name \"%s\" drop", table, proto.name, list, down)
		}
	}
	return out.String()
}

// supported probes once (and again every flvxRateProbeRetry after a failure)
// whether nft and the kernel take this table: flvx.user_rate.v1 is
// advertised only then.
func (r *flvxRateLimiter) supported(ctx context.Context) bool {
	if r == nil {
		return false
	}
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	now := r.now()
	if r.probed && (r.probeOK || now.Sub(r.probedAt) < flvxRateProbeRetry) {
		return r.probeOK
	}
	r.probed, r.probedAt = true, now
	probe := renderFLVXRateNft([]flvxRateOwner{{Owner: "probe", Bytes: flvx.RateBytesPerMbps, TCP: []int{1}, UDP: []int{1}}})
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r.probeOK = r.nft.check(child, []byte(probe)) == nil
	return r.probeOK
}

// ensure puts the table in the wanted state with as few nft calls as
// possible: none in steady state apart from the periodic presence check.
func (r *flvxRateLimiter) ensure(ctx context.Context, owners []flvxRateOwner) error {
	script := renderFLVXRateNft(owners)
	now := r.now()
	if !r.known {
		// A previous Helper process may have left a table behind.
		if script == "" {
			if err := r.nft.remove(ctx); err != nil {
				return err
			}
			r.known, r.applied = true, ""
			return nil
		}
	} else if script == r.applied {
		if script == "" || now.Sub(r.checkedAt) < flvxRateRecheck {
			return nil
		}
		r.checkedAt = now
		if r.nft.present(ctx) {
			return nil
		}
	}
	if script == "" {
		if err := r.nft.remove(ctx); err != nil {
			return err
		}
		r.applied = ""
		return nil
	}
	if err := r.nft.apply(ctx, []byte(script)); err != nil {
		// Never leave a half-known table: drop it (fail open) and retry later.
		_ = r.nft.remove(ctx)
		r.known, r.applied = true, ""
		return err
	}
	r.known, r.applied, r.checkedAt = true, script, now
	return nil
}

// rateOwners are the owners with a 速率 in the budgets in force and the
// ports of their nodes that run in the FLVX Core now.
func (m *flvxManager) rateOwners() []flvxRateOwner {
	view := m.budgetView()
	if view == nil || m.removed || m.state.Payload.Status != "active" {
		return nil
	}
	byOwner := map[string]*flvxRateOwner{}
	for _, n := range m.state.Payload.Desired.Nodes {
		b, ok := view.users[n.Owner.ID]
		if !ok || b.MaxRateMbps == nil || *b.MaxRateMbps <= 0 || m.state.Applied[n.Tag()] == "" || n.Port <= 0 || n.Port > 65535 {
			continue
		}
		o := byOwner[string(n.Owner.ID)]
		if o == nil {
			o = &flvxRateOwner{Owner: string(n.Owner.ID), Bytes: int64(*b.MaxRateMbps) * flvx.RateBytesPerMbps}
			byOwner[o.Owner] = o
		}
		o.TCP = append(o.TCP, n.Port)
		if flvx.UDPProtocol(n.Protocol) {
			o.UDP = append(o.UDP, n.Port)
		}
	}
	owners := make([]flvxRateOwner, 0, len(byOwner))
	for _, o := range byOwner {
		sort.Ints(o.TCP)
		sort.Ints(o.UDP)
		owners = append(owners, *o)
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i].Owner < owners[j].Owner })
	return owners
}

// reconcileRates runs after every pass on the pass's goroutine.
func (m *flvxManager) reconcileRates(ctx context.Context) {
	if m.rate == nil {
		return
	}
	owners := m.rateOwners()
	if len(owners) > 0 && !m.rate.supported(ctx) {
		owners = nil
	}
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := m.rate.ensure(child, owners)
	cancel()
	m.logs.report("FLVX user rate", err)
	rates := map[string]bool{}
	if err == nil {
		for _, o := range owners {
			rates[o.Owner] = true
		}
	}
	m.mu.Lock()
	m.userLimits.rates = rates
	m.mu.Unlock()
}

// rateCapable is whether this Helper advertises flvx.user_rate.v1.
func (m *flvxManager) rateCapable(ctx context.Context) bool {
	return m != nil && m.rate.supported(ctx)
}
