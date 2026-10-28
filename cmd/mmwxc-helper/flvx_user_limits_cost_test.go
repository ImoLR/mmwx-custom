package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// ulCostFixture is a share with owners × perOwner nodes, budgets for every
// owner and a Core snapshot with one live lease per node.
func ulCostFixture(owners, perOwner int) (*flvxManager, coreSnapshotResponse, map[string]string) {
	m := &flvxManager{config: config{Token: "t", ServerID: "7"}, logs: newPeriodicLogLimiter()}
	var nodes []flvx.Node
	var snapshot coreSnapshotResponse
	wanted := map[string]string{}
	users := []flvx.UserLimitBudget{}
	for o := 1; o <= owners; o++ {
		owner := strconv.Itoa(1000 + o)
		limit, ips, rate := int64(20), 3, 50
		users = append(users, flvx.UserLimitBudget{UserID: owner, MaxInboundConnections: &limit, MaxInboundOnlineIPs: &ips, RelayIPReserve: 1, MaxRateMbps: &rate})
		group := managementGroupSnapshot{Username: flvx.UserGroupPrefix + owner, InboundCurrent: int64(perOwner)}
		for i := 0; i < perOwner; i++ {
			n := helperFLVXNode(fmt.Sprintf("%s-%d", owner, i), 30000+o*perOwner+i)
			n.Owner = flvx.Owner{ID: flvx.OwnerID(owner), Name: owner}
			nodes = append(nodes, n)
			wanted[n.Tag()] = "{}"
			ip := fmt.Sprintf("203.0.%d.%d", o%250, i+1)
			snapshot.Users = append(snapshot.Users, coreUserSnapshot{Identity: coreIdentity{InboundTag: n.Tag(), User: n.Tag()}, InboundCurrent: 1, InboundOnlineIPs: coreOnlineIPs{{IP: ip, Connections: 1}}})
			group.InboundOnlineIPs = append(group.InboundOnlineIPs, onlineIP{IP: ip, Connections: 1})
		}
		snapshot.ManagementGroups = append(snapshot.ManagementGroups, group)
	}
	m.state = flvxLocalState{Payload: flvx.Payload{Status: "active", Desired: flvx.Desired{Revision: 1, Nodes: nodes}}, Applied: wanted}
	e := &flvx.UserLimitsEnvelope{Payload: flvx.UserLimitsPayload{ServerID: "7", Sequence: 1, Users: users, RelaySources: flvx.RelaySources([]string{"198.51.100.7"})}, ExpiresAt: time.Now().Add(flvx.UserLimitsTTL).UnixMilli()}
	e.Sign("t")
	if err := m.submitUserLimits(e); err != nil {
		panic(err)
	}
	return m, snapshot, wanted
}

// The Helper work the budgets add to one pass (groups, limits, usage, rate
// owners) for 50 owners × 3 nodes.
func BenchmarkFLVXUserLimitsPass(b *testing.B) {
	m, snapshot, wanted := ulCostFixture(50, 3)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		settings := defaultConnectionSettings()
		m.applyUserLimits(&settings, m.budgetView(), snapshot, wanted)
		m.recordUserLimits(snapshot)
		m.rateOwners()
	}
}

// TestFLVXUserLimitsCoreCost measures the real Core with and without budgets
// (opt-in: root, MMWXC_TEST_FLVX_CORE_FIXED, MMWXC_UL_COST_OUT): RSS and CPU
// per Helper pass (one PUT /v1/config each, as in steady state) for 20 owners
// × 3 nodes, plus the upload size of the usage part.
func TestFLVXUserLimitsCoreCost(t *testing.T) {
	bin := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED")
	if bin == "" || os.Getenv("MMWXC_UL_COST_OUT") == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_FIXED, MMWXC_UL_COST_OUT and run as root")
	}
	base := t.TempDir()
	ns := "mmwxul-cost"
	flvxNetnsCleanup(ns)
	t.Cleanup(func() { flvxNetnsCleanup(ns) })
	n := flvxNetns{t, base}
	n.sh("ip", "netns", "add", ns)
	n.sh("ip", "-n", ns, "link", "set", "lo", "up")
	fixture, _, _ := ulCostFixture(20, 3)
	nodes := fixture.state.Payload.Desired.Nodes
	m, core := flvxNetnsManager(t, ns, bin, base, 39874, nodes)
	m.config = config{Token: "t", ServerID: "7"}
	m.rate = &flvxRateLimiter{nft: flvxRateNftCommand("ip", "netns", "exec", ns, "nft"), now: time.Now}
	ctx := context.Background()
	pass := func() {
		t.Helper()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		m.reconcileRates(ctx)
	}
	pass()
	pid := core.Process.Pid
	cpu := func() time.Duration {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			t.Fatal(err)
		}
		f := strings.Fields(string(raw[strings.LastIndexByte(string(raw), ')')+2:]))
		u, _ := strconv.ParseInt(f[11], 10, 64)
		s, _ := strconv.ParseInt(f[12], 10, 64)
		return time.Duration(u+s) * time.Second / 100
	}
	rss := func() string {
		raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
			}
		}
		return ""
	}
	self := func() time.Duration {
		var r syscall.Rusage
		syscall.Getrusage(syscall.RUSAGE_SELF, &r)
		return time.Duration(r.Utime.Nano() + r.Stime.Nano())
	}
	measure := func(passes int) map[string]any {
		c0, s0, w0 := cpu(), self(), time.Now()
		for i := 0; i < passes; i++ {
			pass()
		}
		return map[string]any{"passes": passes, "core_cpu_ms_per_pass": float64((cpu() - c0).Microseconds()) / 1000 / float64(passes), "helper_cpu_ms_per_pass": float64((self() - s0).Microseconds()) / 1000 / float64(passes), "wall_ms_per_pass": float64(time.Since(w0).Microseconds()) / 1000 / float64(passes), "core_rss": rss()}
	}
	evidence := map[string]any{"nodes": len(nodes), "owners": 20}
	evidence["without_budgets"] = measure(100)
	if err := m.submitUserLimits(fixture.budgets); err != nil {
		t.Fatal(err)
	}
	pass()
	snapshot, _ := m.core.snapshot(ctx)
	if len(snapshot.ManagementGroups) != 20 {
		t.Fatalf("groups %d", len(snapshot.ManagementGroups))
	}
	evidence["with_budgets_and_rates"] = measure(100)
	usage, _ := json.Marshal(m.userLimitsUpload())
	envelope, _ := json.Marshal(fixture.budgets)
	evidence["upload_usage_bytes"], evidence["budget_envelope_bytes"] = len(usage), len(envelope)
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	os.WriteFile(os.Getenv("MMWXC_UL_COST_OUT"), raw, 0644)
	os.WriteFile(filepath.Join(base, "cost.json"), raw, 0644)
	t.Logf("%s", raw)
}
