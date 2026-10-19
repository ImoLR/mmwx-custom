package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// fakeEgressCore extends the fake FLVX Core with outbound and routing state.
type fakeEgressCore struct {
	*fakeFLVXCore
	outbounds   map[string]string
	rules       []map[string]any
	log         []string
	failRules   bool
	failRemove  bool
	unsupported bool
}

func fakeEgressManager(t *testing.T) (*flvxManager, *fakeEgressCore) {
	t.Helper()
	m, base := fakeFLVXManager(t)
	f := &fakeEgressCore{fakeFLVXCore: base, outbounds: map[string]string{"direct": "", "block": ""}}
	inner := m.apiCall
	m.apiCall = func(ctx context.Context, command string, args ...string) ([]byte, error) {
		switch command {
		case "lsrules", "lso", "ado", "adrules", "rmo":
		default:
			if command == "adi" {
				f.log = append(f.log, "adi")
			}
			return inner(ctx, command, args...)
		}
		f.calls++
		f.log = append(f.log, command)
		if f.unsupported {
			return nil, flvxCLIError{command: command, unimplemented: true}
		}
		switch command {
		case "lsrules":
			return []byte(`{"rules":[]}`), nil
		case "lso":
			list := []map[string]string{}
			for tag := range f.outbounds {
				list = append(list, map[string]string{"tag": tag})
			}
			return json.Marshal(map[string]any{"outbounds": list})
		case "ado":
			raw, _ := os.ReadFile(args[0])
			var c struct {
				Outbounds []map[string]any `json:"outbounds"`
			}
			json.Unmarshal(raw, &c)
			for _, o := range c.Outbounds {
				b, _ := json.Marshal(o)
				f.outbounds[o["tag"].(string)] = string(b)
			}
		case "adrules":
			if f.failRules {
				return nil, flvxCLIError{command: command}
			}
			raw, _ := os.ReadFile(args[0])
			var c struct {
				Routing struct {
					Rules []map[string]any `json:"rules"`
				} `json:"routing"`
			}
			json.Unmarshal(raw, &c)
			for _, r := range c.Routing.Rules {
				if tag, _ := r["outboundTag"].(string); f.outbounds[tag] == "" && tag != "direct" && tag != "block" {
					t.Errorf("rule references outbound %s before it was added", tag)
				}
			}
			f.rules = c.Routing.Rules
		case "rmo":
			if f.failRemove {
				return nil, flvxCLIError{command: command}
			}
			for _, tag := range args {
				for _, r := range f.rules {
					if r["outboundTag"] == tag {
						t.Errorf("outbound %s removed while a rule still references it", tag)
					}
				}
				delete(f.outbounds, tag)
			}
		}
		return []byte(`{}`), nil
	}
	m.detectLocal = func() flvx.LocalAddrs {
		return flvx.LocalAddrs{V4: netip.MustParseAddr("203.0.113.1"), V6: netip.MustParseAddr("2001:db8:1::1")}
	}
	m.lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "proxy.example.com":
			return []netip.Addr{netip.MustParseAddr("198.51.100.20"), netip.MustParseAddr("2001:db8::20")}, nil
		case "rebind.example.com":
			return []netip.Addr{netip.MustParseAddr("198.51.100.21"), netip.MustParseAddr("127.0.0.1")}, nil
		}
		return nil, errors.New("no such host")
	}
	m.diskFree = func(string) (int64, error) { return 10 << 30, nil }
	m.memory = func() (int64, int64, bool) { return 40 << 20, 2 << 30, true }
	m.restartCore = func(context.Context) error { t.Fatal("unexpected Core restart"); return nil }
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, helperFLVXNode("two", 30002))
	return m, f
}

func egressPlan() *flvx.Egress {
	return &flvx.Egress{Version: 1, Outbounds: []flvx.EgressOutbound{
		{Tag: "flvxo-5", Type: "socks", IPVersion: "force_ipv4", Server: "proxy.example.com", Port: 1080},
		{Tag: "flvxo-7", Type: "direct", IPVersion: "force_ipv6"},
	}, Rules: []flvx.EgressRule{
		{Name: "BT", InboundTags: []string{"flvxl-one"}, Protocol: []string{"bittorrent"}, Outbound: "block"},
		{Name: "默认出站", InboundTags: []string{"flvxl-one"}, Outbound: "flvxo-5"},
	}}
}

func (f *fakeEgressCore) ruleTargets() []string {
	out := []string{}
	for _, r := range f.rules {
		out = append(out, r["outboundTag"].(string))
	}
	return out
}

func TestFLVXEgressDefaultPlanMakesNoCalls(t *testing.T) {
	m, f := fakeEgressManager(t)
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.log {
		if c != "adi" {
			t.Fatalf("default plan touched routing: %v", f.log)
		}
	}
	e := m.state.Report.Egress
	if e == nil || !e.Supports(1) || !e.GeoIP || !e.GeoSite || e.State != "none" || e.AppliedRevision != 1 {
		t.Fatalf("capability/status not reported: %+v", e)
	}
}

func TestFLVXEgressHotApplyOrderAndSteadyState(t *testing.T) {
	m, f := fakeEgressManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.state.Payload.Desired.Egress = egressPlan()
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Outbounds and rules before any inbound of a fresh Core.
	if got := strings.Join(f.log, ","); got != "lsrules,lso,ado,adrules,adi,adi" {
		t.Fatalf("apply order %s", got)
	}
	if f.rules[0]["ruleTag"] != "flvx-safety" || len(f.rules) != 3 || f.rules[1]["ruleTag"] != "flvxr-1" {
		t.Fatalf("safety rule must stay first: %v", f.rules)
	}
	socksTag := f.ruleTargets()[2]
	if !strings.HasPrefix(socksTag, "flvxo-5-") || len(socksTag) != len("flvxo-5-")+8 {
		t.Fatalf("Core tag must carry a content hash: %s", socksTag)
	}
	var socks map[string]any
	json.Unmarshal([]byte(f.outbounds[socksTag]), &socks)
	server := socks["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
	if server["address"] != "198.51.100.20" || socks["sendThrough"] != "203.0.113.1" {
		t.Fatalf("forced IPv4 upstream must be pinned to its validated A record and bound: %v", socks)
	}
	if e := m.state.Report.Egress; e.State != "applied" || e.AppliedRevision != 1 || len(e.Warnings) != 0 {
		t.Fatalf("status %+v", e)
	}
	// Steady state: no routing calls, also across the 5-minute re-render;
	// DNS checks and local-address probes only at that cadence.
	f.log, f.calls = nil, 0
	lookups, probes := 0, 0
	lookup, detect := m.lookupIP, m.detectLocal
	m.lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) { lookups++; return lookup(ctx, host) }
	m.detectLocal = func() flvx.LocalAddrs { probes++; return detect() }
	for i := 0; i < 70; i++ {
		now = now.Add(5 * time.Second)
		if err := m.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.log) != 0 || f.calls != 70 || lookups != 1 || probes != 1 {
		t.Fatalf("steady state: routing calls %v (calls %d), DNS %d, local probes %d", f.log, f.calls, lookups, probes)
	}
	// A plan change swaps through a new tag: add, replace rules, remove old.
	m.state.Payload.Desired.Revision = 2
	m.state.Payload.Desired.Egress.Outbounds[0].Server = "203.0.113.50"
	m.state.Payload.Desired.Egress.Rules[1].InboundTags = []string{"flvxl-one", "flvxl-two"}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.log, ","); got != "lso,ado,adrules,rmo" {
		t.Fatalf("swap order %s", got)
	}
	if _, old := f.outbounds[socksTag]; old || m.state.Report.Egress.AppliedRevision != 2 {
		t.Fatal("old outbound kept or revision not reported")
	}
	if len(f.present) != 2 {
		t.Fatal("rule changes must not touch inbounds")
	}
	// Back to the built-in default: safety rule only, plan outbounds removed.
	f.log = nil
	m.state.Payload.Desired.Revision = 3
	m.state.Payload.Desired.Egress = nil
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.log, ","); got != "lso,adrules,rmo" || len(f.rules) != 1 || len(f.outbounds) != 2 || m.state.EgressApplied != "" {
		t.Fatalf("default restore %s rules=%v outbounds=%v", got, f.rules, f.outbounds)
	}
	if e := m.state.Report.Egress; e.State != "none" || e.AppliedRevision != 3 {
		t.Fatalf("status %+v", e)
	}
}

func TestFLVXEgressFailClosed(t *testing.T) {
	m, f := fakeEgressManager(t)
	m.state.Payload.Desired.Egress = egressPlan()
	f.failRules = true
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Fresh Core, plan failed: the covered node stays offline, the other runs.
	if f.present["flvxl-one"] || !f.present["flvxl-two"] {
		t.Fatalf("covered node served without its plan: %v", f.present)
	}
	status := map[string]flvx.NodeStatus{}
	for _, n := range m.state.Report.Nodes {
		status[n.ID] = n
	}
	if status["one"].State != "error" || !strings.Contains(status["one"].Error, "出站与路由未生效") || m.state.Report.Egress.State != "error" {
		t.Fatalf("withheld node not reported: %+v %+v", status, m.state.Report.Egress)
	}
	// Errors retry on the 1-minute cadence, not every tick.
	f.log = nil
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.log) != 0 {
		t.Fatalf("failed apply retried immediately: %v", f.log)
	}
	f.failRules = false
	m.egress.at = time.Time{}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.present["flvxl-one"] || m.state.Report.Egress.State != "applied" {
		t.Fatal("node not admitted after the plan applied")
	}
	// A newer plan that fails keeps the older plan; nodes it covers stay up,
	// a node newly covered by the failing plan is withheld.
	applied, rules := m.state.EgressApplied, f.rules
	m.state.Payload.Desired.Revision = 2
	m.state.Payload.Desired.Egress.Rules[1].InboundTags = []string{"flvxl-one", "flvxl-two"}
	f.failRules = true
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.state.EgressApplied != applied || len(f.rules) != len(rules) || !f.present["flvxl-one"] || f.present["flvxl-two"] {
		t.Fatalf("older plan not kept: present=%v", f.present)
	}
	if e := m.state.Report.Egress; e.State != "error" || e.AppliedRevision != 1 {
		t.Fatalf("status %+v", e)
	}
}

func TestFLVXEgressUpstreamDNS(t *testing.T) {
	m, f := fakeEgressManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	plan := egressPlan()
	plan.Outbounds = append(plan.Outbounds, flvx.EgressOutbound{Tag: "flvxo-8", Type: "http", IPVersion: "system", Server: "rebind.example.com", Port: 8080}, flvx.EgressOutbound{Tag: "flvxo-9", Type: "socks", IPVersion: "system", Server: "down.example.com", Port: 1080})
	plan.Rules = append(plan.Rules, flvx.EgressRule{InboundTags: []string{"flvxl-two"}, Port: "80", Outbound: "flvxo-8"}, flvx.EgressRule{InboundTags: []string{"flvxl-two"}, Port: "81", Outbound: "flvxo-9"})
	m.state.Payload.Desired.Egress = plan
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	targets := f.ruleTargets()
	for _, tag := range targets[3:] {
		if !strings.Contains(f.outbounds[tag], `"protocol":"blackhole"`) {
			t.Fatalf("%s must be a blackhole: %s", tag, f.outbounds[tag])
		}
	}
	warnings := strings.Join(m.state.Report.Egress.Warnings, "\n")
	if !strings.Contains(warnings, "flvxo-8: 上游域名解析到内网") || !strings.Contains(warnings, "flvxo-9: 上游域名暂时无法解析") {
		t.Fatalf("warnings %s", warnings)
	}
	// The unresolved name is retried after one minute, not at every tick.
	f.log = nil
	now = now.Add(30 * time.Second)
	m.reconcile(context.Background())
	if len(f.log) != 0 {
		t.Fatal("re-rendered before the retry interval")
	}
	recovered := m.lookupIP
	m.lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host == "down.example.com" {
			return []netip.Addr{netip.MustParseAddr("198.51.100.30")}, nil
		}
		return recovered(ctx, host)
	}
	now = now.Add(31 * time.Second)
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.outbounds[f.ruleTargets()[4]], "198.51.100.30") {
		t.Fatal("recovered upstream not applied")
	}
}

func TestFLVXEgressRoutingRestartOnce(t *testing.T) {
	m, f := fakeEgressManager(t)
	restarts := 0
	f.unsupported = true
	m.restartCore = func(context.Context) error {
		restarts++
		f.unsupported = false
		f.epoch = f.epoch.Add(time.Second)
		f.present = map[string]bool{}
		return nil
	}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts != 0 {
		t.Fatal("default plan restarted the Core")
	}
	m.state.Payload.Desired.Revision = 2
	m.state.Payload.Desired.Egress = egressPlan()
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 || m.state.Report.Egress.State != "applied" || !f.present["flvxl-one"] || !strings.Contains(strings.Join(m.state.Report.Egress.Warnings, ""), "已重启 FLVX Core 一次") {
		t.Fatalf("one restart expected: restarts=%d status=%+v", restarts, m.state.Report.Egress)
	}
	// A Core that lacks the API again is never restarted a second time.
	f.unsupported = true
	f.epoch = f.epoch.Add(time.Second)
	m.state.Payload.Desired.Revision = 3
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 || m.state.Report.Egress.State != "error" || f.present["flvxl-one"] {
		t.Fatalf("second restart or unprotected node: %d %+v %v", restarts, m.state.Report.Egress, f.present)
	}
}

func TestFLVXEgressGeoInstallBackoffAndLimits(t *testing.T) {
	m, f := fakeEgressManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	// A synthetic pinned file: override the pinned digest for this test only.
	content := []byte("synthetic geosite data")
	sum := sha256.Sum256(content)
	oldSum, oldSize := flvx.GeoSHA256["geosite.dat"], flvx.GeoSize["geosite.dat"]
	flvx.GeoSHA256["geosite.dat"], flvx.GeoSize["geosite.dat"] = hex.EncodeToString(sum[:]), int64(len(content))
	t.Cleanup(func() { flvx.GeoSHA256["geosite.dat"], flvx.GeoSize["geosite.dat"] = oldSum, oldSize })
	downloads := 0
	fail := true
	m.geoDownload = func(_ context.Context, name, path string) error {
		downloads++
		if fail {
			return errors.New("mirror down")
		}
		return os.WriteFile(path, content, 0644)
	}
	plan := egressPlan()
	plan.Rules = append(plan.Rules, flvx.EgressRule{InboundTags: []string{"flvxl-two"}, Domain: []string{"geosite:openai"}, Outbound: "flvxo-5"})
	m.state.Payload.Desired.Egress = plan
	m.reconcile(context.Background())
	if downloads != 1 || m.state.Report.Egress.State != "error" || !strings.Contains(m.state.Report.Egress.Error, "1 分钟后重试") || f.present["flvxl-one"] {
		t.Fatalf("first failure: downloads=%d %+v", downloads, m.state.Report.Egress)
	}
	for _, step := range []struct {
		advance time.Duration
		want    int
	}{{30 * time.Second, 1}, {31 * time.Second, 2}, {time.Minute, 2}, {61 * time.Second, 3}} {
		now = now.Add(step.advance)
		m.reconcile(context.Background())
		if downloads != step.want {
			t.Fatalf("backoff: %d downloads, want %d", downloads, step.want)
		}
	}
	fail = false
	now = now.Add(5 * time.Minute)
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if downloads != 4 || m.state.Report.Egress.State != "applied" || len(m.geoRetry) != 0 {
		t.Fatalf("recovery: %d %+v", downloads, m.state.Report.Egress)
	}
	if _, err := os.Stat(filepath.Join(m.geoDir(), "geoip.dat")); !os.IsNotExist(err) {
		t.Fatal("unreferenced geoip.dat installed")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(m.geoDir(), "*.download")); len(leftovers) > 0 {
		t.Fatal("temporary download left behind")
	}
	// Verified files are not re-hashed or re-downloaded.
	now = now.Add(10 * time.Minute)
	m.reconcile(context.Background())
	if downloads != 4 {
		t.Fatal("verified geo data downloaded again")
	}
	// Disk and memory checks refuse with a clear message before any change.
	m2, _ := fakeEgressManager(t)
	m2.diskFree = func(string) (int64, error) { return 32 << 20, nil }
	m2.state.Payload.Desired.Egress = plan
	m2.reconcile(context.Background())
	if e := m2.state.Report.Egress; e.GeoSite || e.State != "error" || !strings.Contains(e.Error, "磁盘空间不足") {
		t.Fatalf("disk check: %+v", e)
	}
	m3, f3 := fakeEgressManager(t)
	m3.geoDownload = m.geoDownload
	m3.memory = func() (int64, int64, bool) { return 250 << 20, 2 << 30, true }
	m3.state.Payload.Desired.Egress = &flvx.Egress{Version: 1, Rules: []flvx.EgressRule{{InboundTags: []string{"flvxl-one"}, Domain: []string{"geosite:cn"}, Outbound: "block"}}}
	m3.reconcile(context.Background())
	if e := m3.state.Report.Egress; e.State != "error" || !strings.Contains(e.Error, "内存不足") || len(f3.rules) != 0 || f3.present["flvxl-one"] {
		t.Fatalf("memory check: %+v", e)
	}
}

func TestFLVXEgressStaleCleanupRetry(t *testing.T) {
	m, f := fakeEgressManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.state.Payload.Desired.Egress = egressPlan()
	m.reconcile(context.Background())
	f.failRemove = true
	m.state.Payload.Desired.Revision = 2
	m.state.Payload.Desired.Egress = nil
	m.reconcile(context.Background())
	if !m.state.EgressStale || !strings.Contains(strings.Join(m.state.Report.Egress.Warnings, ""), "旧出站清理失败") {
		t.Fatal("cleanup failure not recorded")
	}
	f.log = nil
	for i := 0; i < 20; i++ {
		now = now.Add(5 * time.Second)
		m.reconcile(context.Background())
	}
	if len(f.log) != 0 {
		t.Fatalf("cleanup retried before 2 minutes: %v", f.log)
	}
	f.failRemove = false
	now = now.Add(30 * time.Second)
	m.reconcile(context.Background())
	if got := strings.Join(f.log, ","); got != "lso,rmo" || m.state.EgressStale || len(f.outbounds) != 2 {
		t.Fatalf("cleanup retry %s %v", got, f.outbounds)
	}
}

func TestFLVXEgressAcceptRevalidates(t *testing.T) {
	m, _ := fakeEgressManager(t)
	sign := func(plan *flvx.Egress, version int64) flvx.Envelope {
		p := m.state.Payload
		p.ServerID, p.Version = "7", version
		p.Desired.Egress = plan
		e := flvx.Envelope{Payload: p, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
		e.Sign("helper")
		return e
	}
	if err := m.accept(sign(egressPlan(), 2)); err != nil {
		t.Fatal(err)
	}
	bad := egressPlan()
	bad.Outbounds[0].Server = "169.254.169.254"
	if err := m.accept(sign(bad, 3)); err == nil {
		t.Fatal("Helper accepted a plan with a private upstream")
	}
	foreign := egressPlan()
	foreign.Rules[0].InboundTags = []string{"flvxl-other"}
	if err := m.accept(sign(foreign, 3)); err == nil || m.state.Payload.Version != 2 {
		t.Fatal("Helper accepted a rule for a node outside this share")
	}
}
