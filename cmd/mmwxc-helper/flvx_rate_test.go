package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// flvxRateLimiterIn runs ip / tc / nft inside a test namespace.
func flvxRateLimiterIn(ns string, now func() time.Time) *flvxRateLimiter {
	return &flvxRateLimiter{run: flvxRateCommand("ip", "netns", "exec", ns), loadModule: loadIfbModule, now: now}
}

// fakeRateKernel models what the limiter reads and writes: links, ingress
// slot qdiscs, filters (dev/hook → "proto pref handle" → slot class id) and
// classes, and records every call.
type fakeRateKernel struct {
	links   map[string]*fakeRateLink
	hooks   map[string]string            // dev → clsact / ingress
	filters map[string]map[string]string // "dev hook" → "proto pref handle" → classid
	classes map[string]string            // "ifb classid" → rate
	leaves  map[string]bool              // "ifb parent"
	nftGone bool
	calls   []string
	nextIdx int
	failTC  error
}

type fakeRateLink struct {
	idx            int
	up             bool
	kind, master   string
	parent, global string
}

func newFakeRateKernel() *fakeRateKernel {
	k := &fakeRateKernel{links: map[string]*fakeRateLink{}, hooks: map[string]string{}, filters: map[string]map[string]string{}, classes: map[string]string{}, leaves: map[string]bool{}, nextIdx: 1}
	k.link("lo", "", "", "")
	k.link("eth0", "", "", "203.0.113.7")
	return k
}

func (k *fakeRateKernel) link(name, kind, master, global string) *fakeRateLink {
	l := &fakeRateLink{idx: k.nextIdx, up: true, kind: kind, master: master, global: global}
	k.nextIdx++
	k.links[name] = l
	return l
}

func (k *fakeRateKernel) filterCount(dev string) int {
	return len(k.filters[dev+" ingress"]) + len(k.filters[dev+" egress"])
}

func (k *fakeRateKernel) run(_ context.Context, stdin []byte, tool string, args ...string) ([]byte, error) {
	call := strings.TrimSpace(tool + " " + strings.Join(args, " "))
	if len(stdin) > 0 {
		call += "\n" + strings.TrimSpace(string(stdin))
	}
	k.calls = append(k.calls, call)
	switch {
	case tool == "nft":
		k.nftGone = true
		return nil, errors.New("no such table")
	case tool == "unshare":
		return []byte(`[{"protocol":"ipv6","pref":61871,"kind":"flower","chain":0}]`), nil
	case tool == "ip" && args[0] == "-j":
		var out []map[string]any
		names := make([]string, 0, len(k.links))
		for name := range k.links {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			l := k.links[name]
			flags := []string{}
			if name == "lo" {
				flags = append(flags, "LOOPBACK")
			}
			if l.up {
				flags = append(flags, "UP")
			}
			e := map[string]any{"ifindex": l.idx, "ifname": name, "flags": flags, "addr_info": []any{}}
			if name == "lo" {
				e["addr_info"] = []any{map[string]any{"local": "127.0.0.1", "scope": "host"}}
			} else if l.global != "" {
				e["addr_info"] = []any{map[string]any{"local": l.global, "scope": "global"}}
			}
			if l.kind != "" {
				e["linkinfo"] = map[string]any{"info_kind": l.kind}
			}
			if l.master != "" {
				e["master"] = l.master
			}
			if l.parent != "" {
				e["link"] = l.parent
			}
			out = append(out, e)
		}
		return json.Marshal(out)
	case tool == "ip" && args[0] == "-batch":
		for _, line := range strings.Split(strings.TrimSpace(string(stdin)), "\n") {
			f := strings.Fields(line)
			switch {
			case f[1] == "add":
				if k.links[f[2]] != nil {
					return nil, errors.New("RTNETLINK answers: File exists")
				}
				k.link(f[2], "ifb", "", "").up = false
			case f[1] == "set":
				k.links[f[2]].up = true
			case f[1] == "del":
				delete(k.links, f[2])
				for key := range k.classes {
					if strings.HasPrefix(key, f[2]+" ") {
						delete(k.classes, key)
					}
				}
				for key := range k.leaves {
					if strings.HasPrefix(key, f[2]+" ") {
						delete(k.leaves, key)
					}
				}
			}
		}
		return nil, nil
	case tool == "tc" && args[0] == "-j" && args[1] == "qdisc":
		var out []map[string]any
		for dev, kind := range k.hooks {
			out = append(out, map[string]any{"kind": kind, "dev": dev, "parent": "ffff:fff1"})
		}
		out = append(out, map[string]any{"kind": "fq_codel", "dev": "eth0", "root": true})
		return json.Marshal(out)
	case tool == "tc" && args[0] == "-j" && args[1] == "filter":
		out := []map[string]any{}
		for spec := range k.filters[args[4]+" "+args[5]] {
			var proto string
			var pref int
			fmt.Sscanf(spec, "%s %d", &proto, &pref)
			out = append(out, map[string]any{"protocol": proto, "pref": pref, "kind": "flower"})
		}
		return json.Marshal(out)
	case tool == "tc" && args[0] == "-batch":
		if err := k.failTC; err != nil {
			k.failTC = nil // one batch fails
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(stdin)), "\n") {
			if err := k.tc(strings.Fields(line)); err != nil {
				return nil, fmt.Errorf("%s: %w", line, err)
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected call %q", call)
}

func (k *fakeRateKernel) tc(f []string) error {
	arg := func(name string) string {
		for i := range f[:len(f)-1] {
			if f[i] == name {
				return f[i+1]
			}
		}
		return ""
	}
	dev := arg("dev")
	if k.links[dev] == nil {
		return errors.New("Cannot find device")
	}
	switch f[0] + " " + f[1] {
	case "qdisc add":
		if k.hooks[dev] != "" {
			return errors.New("Exclusivity flag on, cannot modify")
		}
		k.hooks[dev] = "clsact"
	case "qdisc del":
		delete(k.hooks, dev)
		delete(k.filters, dev+" ingress")
		delete(k.filters, dev+" egress")
	case "qdisc replace":
		if arg("parent") != "" {
			k.leaves[dev+" "+arg("parent")] = true
		}
	case "class replace", "class change":
		if f[1] == "change" && k.classes[dev+" "+arg("classid")] == "" {
			return errors.New("class not found")
		}
		k.classes[dev+" "+arg("classid")] = arg("rate")
	case "class del":
		delete(k.classes, dev+" "+arg("classid"))
		delete(k.leaves, dev+" "+arg("classid"))
	case "filter replace":
		hook := f[4]
		if k.hooks[dev] == "" {
			return errors.New("Cannot find specified qdisc")
		}
		if k.links[arg("dev")] == nil || k.links[f[len(f)-1]] == nil {
			return errors.New("Cannot find device")
		}
		if k.filters[dev+" "+hook] == nil {
			k.filters[dev+" "+hook] = map[string]string{}
		}
		k.filters[dev+" "+hook][arg("protocol")+" "+arg("pref")+" "+arg("handle")] = arg("priority")
	case "filter del":
		hook := f[4]
		for spec := range k.filters[dev+" "+hook] {
			tp := arg("protocol") + " " + arg("pref") + " "
			if spec == tp+arg("handle") || (arg("handle") == "" && strings.HasPrefix(spec, tp)) {
				delete(k.filters[dev+" "+hook], spec)
			}
		}
	default:
		return fmt.Errorf("unexpected tc %v", f)
	}
	return nil
}

func TestFLVXRateRender(t *testing.T) {
	// 50 FLVX Mbps = 6553600 B/s payload → 55574528 bit/s on the wire (+6 %).
	lines := flvxRateClassLines(1, flvxRateWire(50*flvx.RateBytesPerMbps), false)
	lines = append(lines, flvxRateClassLines(18, flvxRateWire(1*flvx.RateBytesPerMbps), true)...)
	lines = append(lines,
		flvxRateFilterLine("eth0", flvxRateKey{port: 30001}, 1),
		flvxRateFilterLine("eth0", flvxRateKey{egress: true, v6: true, udp: true, port: 30001}, 18),
		flvxRateFilterDelLine("eth0", flvxRateKey{egress: true, port: 30001}))
	want := `class replace dev mmwxcr-up parent 7f1b: classid 7f1b:10 htb rate 55574528bit ceil 55574528bit quantum 60000
qdisc replace dev mmwxcr-up parent 7f1b:10 handle 1: fq_codel flows 128 memory_limit 4194304
class replace dev mmwxcr-dn parent 7f1b: classid 7f1b:10 htb rate 55574528bit ceil 55574528bit quantum 60000
qdisc replace dev mmwxcr-dn parent 7f1b:10 handle 1: fq_codel flows 128 memory_limit 4194304
class change dev mmwxcr-up parent 7f1b: classid 7f1b:120 htb rate 1111490bit ceil 1111490bit quantum 60000
class change dev mmwxcr-dn parent 7f1b: classid 7f1b:120 htb rate 1111490bit ceil 1111490bit quantum 60000
filter replace dev eth0 ingress protocol ip pref 61870 handle 0x1d4c5 flower ip_proto tcp dst_port 30001 action skbedit priority 7f1b:10 action mirred egress redirect dev mmwxcr-up
filter replace dev eth0 egress protocol ipv6 pref 61871 handle 0x1d4c6 flower ip_proto udp src_port 30001 action skbedit priority 7f1b:120 action mirred egress redirect dev mmwxcr-dn
filter del dev eth0 egress protocol ip pref 61870 handle 0x1d4c5 flower`
	if got := strings.Join(lines, "\n"); got != want {
		t.Fatalf("render:\n%s", got)
	}
	// The probe builds exactly these objects (a class, a filter per family).
	probe := flvxRateProbeScript()
	for _, line := range []string{flvxRateClassLines(1, flvxRateWire(flvx.RateBytesPerMbps), false)[1], flvxRateFilterLine("lo", flvxRateKey{port: 1}, 1), "tc -j filter show dev lo egress"} {
		if !strings.Contains(probe, line) {
			t.Fatalf("probe lacks %q", line)
		}
	}
}

func flvxRateCallsSince(k *fakeRateKernel, from int) []string { return k.calls[from:] }

func countLines(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		for _, line := range strings.Split(c, "\n") {
			if strings.HasPrefix(line, prefix) {
				n++
			}
		}
	}
	return n
}

// Start removes an earlier process's objects (and v0.7.15's nft table) but
// not foreign ones; devices are selected like FLVX's agent; steady state
// makes no call except one ip call a minute; changes are minimal; a removed
// owner is released (unlimited) before its filters and class go; the last
// one takes the IFBs and our clsact with it.
func TestFLVXRateReconcile(t *testing.T) {
	k := newFakeRateKernel()
	k.link("eth1", "", "", "2001:db8::7")
	k.link("docker0", "bridge", "", "172.17.0.1")
	k.link("veth1", "veth", "docker0", "")
	k.link("eth0.7", "vlan", "", "198.51.100.7").parent = "eth0"
	k.link("down0", "", "", "192.0.2.9").up = false
	k.link("flvxlr-up", "ifb", "", "")
	// Leftovers of an earlier Helper process, next to a foreign filter.
	k.link(flvxRateIfbUp, "ifb", "", "")
	k.hooks["eth0"] = "clsact"
	k.filters["eth0 ingress"] = map[string]string{"ip 61870 0x1d4c5": "7f1b:10", "ip 49152 0x1": "1:1"}
	k.filters["eth0 egress"] = map[string]string{"ipv6 61871 0x1d4c6": "7f1b:10"}
	now := time.Unix(1793300000, 0)
	r := &flvxRateLimiter{run: k.run, now: func() time.Time { return now }}
	pass := func(owners []flvxRateOwner) error {
		now = now.Add(5 * time.Second)
		return r.ensure(context.Background(), owners)
	}
	if err := pass(nil); err != nil {
		t.Fatal(err)
	}
	if !k.nftGone || k.links[flvxRateIfbUp] != nil || k.hooks["eth0"] != "clsact" || len(k.filters["eth0 ingress"]) != 1 || len(k.filters["eth0 egress"]) != 0 {
		t.Fatalf("start cleanup: nft=%v links=%v hooks=%v filters=%v", k.nftGone, k.links[flvxRateIfbUp], k.hooks, k.filters)
	}
	calls := len(k.calls)
	for i := 0; i < 20; i++ {
		pass(nil)
	}
	if len(k.calls) != calls {
		t.Fatalf("idle passes made calls: %v", k.calls[calls:])
	}

	alice := flvxRateOwner{Owner: "26", Bytes: 20 * flvx.RateBytesPerMbps, TCP: []int{30001, 30002}, UDP: []int{30001}}
	bob := flvxRateOwner{Owner: "31", Bytes: 5 * flvx.RateBytesPerMbps, TCP: []int{30004}}
	owners := []flvxRateOwner{alice, bob}
	if err := pass(owners); err != nil {
		t.Fatal(err)
	}
	// eth0, eth1 and docker0 (it has an address of its own); not lo, the bridge
	// port, the VLAN on eth0, a down device or FLVX's IFB. 4 (port, proto)
	// pairs × IPv4/IPv6 × both directions = 16 filters per device.
	for _, dev := range []string{"eth0", "eth1", "docker0"} {
		want := 16
		if dev == "eth0" {
			want++ // the foreign filter stays
		}
		if k.hooks[dev] != "clsact" || k.filterCount(dev) != want {
			t.Fatalf("%s: hook %q filters %d", dev, k.hooks[dev], k.filterCount(dev))
		}
	}
	for _, dev := range []string{"eth0.7", "veth1", "down0", "lo", "flvxlr-up"} {
		if k.hooks[dev] != "" {
			t.Fatalf("attached to %s", dev)
		}
	}
	if !k.links[flvxRateIfbUp].up || !k.links[flvxRateIfbDown].up || len(k.classes) != 4 || len(k.leaves) != 4 || k.classes["mmwxcr-up 7f1b:10"] != "22229811bit" || k.classes["mmwxcr-dn 7f1b:20"] != "5557452bit" {
		t.Fatalf("ifbs / classes %v %v", k.classes, k.leaves)
	}
	if k.filters["eth1 egress"]["ipv6 61871 0x1d4c6"] != "7f1b:10" || k.filters["docker0 ingress"]["ip 61870 0x1d4d1"] != "7f1b:20" {
		t.Fatalf("filters %v", k.filters)
	}

	// Steady state: nothing for a minute, then one ip call.
	calls = len(k.calls)
	for i := 0; i < 11; i++ {
		pass(owners)
	}
	if len(k.calls) != calls {
		t.Fatalf("steady passes made calls: %v", k.calls[calls:])
	}
	pass(owners)
	if got := flvxRateCallsSince(k, calls); len(got) != 1 || got[0] != "ip -j -d addr show" {
		t.Fatalf("recheck: %v", got)
	}

	// Rate change: the classes change in place, nothing else.
	alice.Bytes = 40 * flvx.RateBytesPerMbps
	owners = []flvxRateOwner{alice, bob}
	calls = len(k.calls)
	pass(owners)
	got := flvxRateCallsSince(k, calls)
	if countLines(got, "class change") != 2 || countLines(got, "filter") != 0 || countLines(got, "qdisc") != 0 || k.classes["mmwxcr-up 7f1b:10"] != "44459622bit" {
		t.Fatalf("rate change: %v", got)
	}

	// A node goes: its port's filters go at once (TCP only: 4 per device).
	alice.TCP = []int{30001}
	owners = []flvxRateOwner{alice, bob}
	calls = len(k.calls)
	pass(owners)
	if got := flvxRateCallsSince(k, calls); countLines(got, "filter del") != 12 || countLines(got, "class") != 0 || k.filterCount("eth1") != 12 {
		t.Fatalf("node removed: %v", got)
	}

	// Bob's rate goes: first unlimited (filters kept), a later pass removes
	// his filters and class.
	owners = []flvxRateOwner{alice}
	pass(owners)
	if k.classes["mmwxcr-up 7f1b:20"] != fmt.Sprintf("%dbit", flvxRateUnlimited) || k.filterCount("eth1") != 12 {
		t.Fatalf("release: %v / %d", k.classes, k.filterCount("eth1"))
	}
	pass(owners)
	if k.classes["mmwxcr-up 7f1b:20"] != "" || k.leaves["mmwxcr-dn 7f1b:20"] || k.filterCount("eth1") != 8 {
		t.Fatalf("released: %v / %d", k.classes, k.filterCount("eth1"))
	}
	calls = len(k.calls)
	pass(owners)
	if len(k.calls) != calls {
		t.Fatalf("calls after release: %v", k.calls[calls:])
	}

	// Someone deletes an IFB: the next check starts over (filters first).
	delete(k.links, flvxRateIfbDown)
	now = now.Add(time.Minute)
	if err := pass(owners); err != nil {
		t.Fatal(err)
	}
	if k.links[flvxRateIfbDown] == nil || k.filterCount("eth0") != 9 || len(k.classes) != 2 {
		t.Fatalf("rebuild: %v %d", k.classes, k.filterCount("eth0"))
	}

	// A failing batch: everything goes (fail open), retried a minute later or
	// at once when the owners change.
	k.failTC = errors.New("RTNETLINK answers: Operation not supported")
	alice.Bytes = 10 * flvx.RateBytesPerMbps
	owners = []flvxRateOwner{alice}
	if err := pass(owners); err == nil {
		t.Fatal("failure not reported")
	}
	if k.links[flvxRateIfbUp] != nil || k.hooks["eth1"] != "" {
		t.Fatalf("failed apply left objects: %v", k.hooks)
	}
	calls = len(k.calls)
	if err := pass(owners); err == nil || len(k.calls) != calls {
		t.Fatal("failure retried before a minute")
	}
	if err := pass([]flvxRateOwner{bob}); err != nil || k.classes["mmwxcr-up 7f1b:10"] != "5557452bit" {
		t.Fatalf("owner change not retried: %v %v", err, k.classes)
	}

	// The last rate goes: released, then filters, classes, IFBs and our
	// clsacts go; the foreign filter and its clsact stay.
	pass(nil)
	if k.links[flvxRateIfbUp] == nil {
		t.Fatal("IFBs removed before the release")
	}
	pass(nil)
	if k.links[flvxRateIfbUp] != nil || k.links[flvxRateIfbDown] != nil || k.hooks["eth1"] != "" || k.hooks["docker0"] != "" || k.hooks["eth0"] != "clsact" || k.filterCount("eth0") != 1 {
		t.Fatalf("last owner gone: links %v hooks %v filters %v", k.links[flvxRateIfbUp], k.hooks, k.filters)
	}
	calls = len(k.calls)
	pass(nil)
	pass(nil)
	if len(k.calls) != calls {
		t.Fatalf("calls with nothing in force: %v", k.calls[calls:])
	}
}

// A legacy ingress qdisc of another program: nothing is attached, the
// failure is reported, nothing is left behind.
func TestFLVXRateForeignIngress(t *testing.T) {
	k := newFakeRateKernel()
	k.hooks["eth0"] = "ingress"
	now := time.Unix(1793300000, 0)
	r := &flvxRateLimiter{run: k.run, now: func() time.Time { return now }, known: true}
	err := r.ensure(context.Background(), []flvxRateOwner{{Owner: "26", Bytes: flvx.RateBytesPerMbps, TCP: []int{30001}}})
	if err == nil || !strings.Contains(err.Error(), "ingress qdisc") {
		t.Fatalf("err %v", err)
	}
	if k.hooks["eth0"] != "ingress" || k.links[flvxRateIfbUp] != nil || len(k.classes) != 0 {
		t.Fatalf("left %v %v", k.hooks, k.classes)
	}
}

func TestFLVXRateRelease(t *testing.T) {
	k := newFakeRateKernel()
	r := &flvxRateLimiter{run: k.run, now: time.Now}
	if err := r.ensure(context.Background(), []flvxRateOwner{{Owner: "26", Bytes: flvx.RateBytesPerMbps, TCP: []int{30001}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if k.links[flvxRateIfbUp] != nil || k.hooks["eth0"] != "" || len(k.classes) != 0 {
		t.Fatalf("release left %v %v", k.hooks, k.classes)
	}
	var none *flvxRateLimiter
	if none.release(context.Background()) != nil {
		t.Fatal("nil release")
	}
}

func TestFLVXRateProbeCached(t *testing.T) {
	probes := 0
	answer := []byte("RTNETLINK answers: Operation not supported")
	var failure error = errors.New("exit status 2")
	now := time.Now()
	r := &flvxRateLimiter{now: func() time.Time { return now }, run: func(_ context.Context, _ []byte, tool string, args ...string) ([]byte, error) {
		probes++
		if tool != "unshare" || args[0] != "--net" {
			t.Fatalf("probe ran %s %v", tool, args)
		}
		return answer, failure
	}}
	for i := 0; i < 5; i++ {
		if r.supported(context.Background()) {
			t.Fatal("unsupported probe reported supported")
		}
	}
	if probes != 1 {
		t.Fatalf("probe ran %d times", probes)
	}
	answer, failure = []byte(`[{"protocol":"ipv6","pref":61871,"kind":"flower","chain":0},{"protocol":"ipv6","pref":61871,"kind":"flower","chain":0,"options":{}}]`), nil
	now = now.Add(flvxRateProbeRetry)
	if !r.supported(context.Background()) || !r.supported(context.Background()) || probes != 2 {
		t.Fatalf("probe retry: %d", probes)
	}
	var none *flvxManager
	if none.rateCapable(context.Background()) {
		t.Fatal("nil manager rate capable")
	}
}

// Only owners with a 速率, only nodes running in the FLVX Core now, UDP ports
// only for SS2022 / Mieru; another owner without a rate is untouched; the
// report says which owners' rates are in force; expiry releases the class
// and the next pass removes everything.
func TestFLVXRateOwnersAndExpiry(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	k := newFakeRateKernel()
	m.rate = &flvxRateLimiter{run: k.run, now: m.now}
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
	ports := map[string]bool{}
	for _, hook := range []string{"eth0 ingress", "eth0 egress"} {
		for spec := range k.filters[hook] {
			var proto, pref, handle string
			fmt.Sscan(spec, &proto, &pref, &handle)
			var h uint32
			fmt.Sscanf(handle, "0x%x", &h)
			ports[fmt.Sprint(h>>2)] = true
		}
	}
	if !reflect.DeepEqual(ports, map[string]bool{"30001": true, "30005": true}) || k.filterCount("eth0") != 12 || len(k.classes) != 2 {
		t.Fatalf("applied ports %v filters %d classes %v", ports, k.filterCount("eth0"), k.classes)
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
	// Budgets expire: unlimited at once, everything gone the pass after.
	now = now.Add(flvx.UserLimitsTTL)
	m.reconcileRates(context.Background())
	if m.userLimitsUpload().Users[0].RateApplied || k.classes["mmwxcr-up 7f1b:10"] != fmt.Sprintf("%dbit", flvxRateUnlimited) {
		t.Fatalf("expired rate kept: %v", k.classes)
	}
	now = now.Add(5 * time.Second)
	m.reconcileRates(context.Background())
	if k.links[flvxRateIfbUp] != nil || k.hooks["eth0"] != "" {
		t.Fatalf("objects left after expiry: %v", k.hooks)
	}
}
