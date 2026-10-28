package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// landing-user-limits-v1 with the real FLVX Core driven by the Helper's own
// reconcile, in three private namespaces (the host network is never touched):
//
//	client 203.0.113.50-.52 (direct), .60 (an FLVX node = relay source)
//	  ── (20 ms RTT) ── 203.0.113.1 landing 11.77.0.1 ── 11.77.0.2 target
//
// Owner 26 has SS2022 nodes A1 31001 and A2 31002, owner 31 node B1 31004.
// Budgets reach the manager exactly as from an upload answer (signed
// envelope); per-user 速率 runs through the Helper's nft table in the landing
// namespace. The numbers mirror FLVX's own lab (go-gost/x/landing/
// user_limits_netns_test.go). Opt-in: root, MMWXC_TEST_FLVX_CORE_FIXED;
// MMWXC_UL_E2E_OUT receives the evidence JSON.
const (
	ulLanding = "203.0.113.1"
	ulTarget  = "11.77.0.2" // newer Cores refuse reserved targets (198.51.100.0/24)
	ulRelay   = "203.0.113.60"
)

// ulTargetServer: 8080 banner + hold, 9000 bulk download, 9002 discard.
const ulTargetServer = `import socket, threading
def serve(port, handler):
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("0.0.0.0", port)); s.listen(1024)
    while True:
        c, a = s.accept()
        threading.Thread(target=handler, args=(c, a), daemon=True).start()
def hold(c, a):
    try:
        c.sendall(("peer=%s\n" % a[0]).encode())
        while c.recv(64):
            pass
    except Exception:
        pass
    c.close()
def bulk(c, a):
    chunk = b"\0" * 65536
    try:
        while True:
            c.sendall(chunk)
    except Exception:
        pass
    c.close()
def discard(c, a):
    try:
        while c.recv(1 << 16):
            pass
    except Exception:
        pass
    c.close()
threading.Thread(target=serve, args=(9000, bulk), daemon=True).start()
threading.Thread(target=serve, args=(9002, discard), daemon=True).start()
serve(8080, hold)
`

// ulClientDriver runs in the client namespace and takes one JSON command per
// line on a unix socket: open (one connection, admitted = banner received;
// admitted ones stay open), close (all held), rate (parallel downloads or
// uploads for N seconds, Mbps per port in FLVX units: bytes / 131072).
const ulClientDriver = `import json, os, socket, sys, threading, time
path = sys.argv[1]
held = []
def open_one(port):
    try:
        c = socket.create_connection(("127.0.0.1", port), timeout=5)
        # The SS2022 client sends its request header with the first payload.
        c.sendall(b"hi\n")
        c.settimeout(4)
        data = b""
        while not data.endswith(b"\n"):
            chunk = c.recv(256)
            if not chunk:
                break
            data += chunk
        if data.startswith(b"peer="):
            c.settimeout(None); held.append(c); return True
        c.close()
    except Exception:
        pass
    return False
def transfer(port, seconds, up, out, i, warm):
    n = 0
    try:
        c = socket.create_connection(("127.0.0.1", port), timeout=5)
        end = time.time() + seconds
        if up:
            buf = b"\0" * 65536
            c.settimeout(1)
            while time.time() < end:
                try:
                    n += c.send(buf)
                except socket.timeout:
                    pass
        else:
            c.sendall(b"go\n")
            c.settimeout(1)
            counting = time.time() + warm
            while time.time() < end:
                try:
                    d = c.recv(1 << 16)
                except socket.timeout:
                    continue
                if not d:
                    break
                if time.time() >= counting:
                    n += len(d)
        c.close()
    except Exception:
        pass
    out[i] = n / (seconds - warm) / 131072
try:
    os.unlink(path)
except OSError:
    pass
s = socket.socket(socket.AF_UNIX); s.bind(path); s.listen(4)
while True:
    conn, _ = s.accept()
    f = conn.makefile("rw")
    for line in f:
        cmd = json.loads(line)
        if cmd["op"] == "open":
            res = {"admitted": [open_one(p) for p in cmd["ports"]]}
        elif cmd["op"] == "close":
            for c in held:
                try:
                    c.close()
                except Exception:
                    pass
            res = {"closed": len(held)}; held.clear()
        else:
            out = [0.0] * len(cmd["ports"])
            ts = [threading.Thread(target=transfer, args=(p, cmd["seconds"], cmd["op"] == "upload", out, i, cmd.get("warm", 0))) for i, p in enumerate(cmd["ports"])]
            [t.start() for t in ts]; [t.join() for t in ts]
            res = {"mbps": out}
        f.write(json.dumps(res) + "\n"); f.flush()
    conn.close()
`

type ulDriver struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func (d *ulDriver) call(cmd map[string]any, out any) {
	d.t.Helper()
	raw, _ := json.Marshal(cmd)
	d.conn.SetDeadline(time.Now().Add(2 * time.Minute))
	if _, err := d.conn.Write(append(raw, '\n')); err != nil {
		d.t.Fatal(err)
	}
	line, err := d.r.ReadString('\n')
	if err != nil || json.Unmarshal([]byte(line), out) != nil {
		d.t.Fatalf("driver %v: %v %q", cmd, err, line)
	}
}

// open opens the connections one after another; returns how many were admitted.
func (d *ulDriver) open(ports ...int) int {
	d.t.Helper()
	var res struct{ Admitted []bool }
	d.call(map[string]any{"op": "open", "ports": ports}, &res)
	n := 0
	for _, ok := range res.Admitted {
		if ok {
			n++
		}
	}
	return n
}

func (d *ulDriver) closeAll() {
	d.t.Helper()
	var res map[string]any
	d.call(map[string]any{"op": "close"}, &res)
}

// rate downloads (receiver-side Mbps after warm seconds, so the bucket's
// one-second burst is left out) or uploads for seconds.
func (d *ulDriver) rate(op string, seconds, warm int, ports ...int) []float64 {
	d.t.Helper()
	var res struct{ Mbps []float64 }
	d.call(map[string]any{"op": op, "ports": ports, "seconds": seconds, "warm": warm}, &res)
	return res.Mbps
}

func TestFLVXUserLimitsNetnsE2E(t *testing.T) {
	bin := os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED")
	if bin == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_CORE_FIXED and run as root")
	}
	for _, tool := range []string{"python3", "ip", "nft"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " missing")
		}
	}
	raiseNoFile()
	base := os.Getenv("MMWXC_UL_E2E_DIR") // keeps the logs for diagnosis
	if base == "" {
		base = t.TempDir()
	}
	os.MkdirAll(base, 0755)
	os.Chmod(base, 0755)
	c, l, _ := ulLabNetns(t, base)

	a1, a2, b1 := netnsNode("A1", 31001, 0), netnsNode("A2", 31002, 0), netnsNode("B1", 31004, 0)
	a1.Owner, a2.Owner, b1.Owner = flvx.Owner{ID: "26", Name: "alice"}, flvx.Owner{ID: "26", Name: "alice"}, flvx.Owner{ID: "31", Name: "bob"}
	nodes := []flvx.Node{a1, a2, b1}
	for i := range nodes {
		key := func() string {
			var b [16]byte
			rand.Read(b[:])
			return base64.StdEncoding.EncodeToString(b[:])
		}
		nodes[i].SS2022 = &flvx.SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: key(), UserKey: key()}
	}
	m, core := flvxNetnsManager(t, l, bin, base, 39872, nodes)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.config = config{Token: "helper-e2e", ServerID: "7"}
	m.rate = &flvxRateLimiter{nft: flvxRateNftCommand("ip", "netns", "exec", l, "nft"), now: m.now}
	ctx := context.Background()
	sequence := int64(1793200000000)
	budgets := func(users ...flvx.UserLimitBudget) {
		t.Helper()
		sequence++
		e := &flvx.UserLimitsEnvelope{Payload: flvx.UserLimitsPayload{ServerID: "7", Sequence: sequence, Users: append([]flvx.UserLimitBudget{}, users...), RelaySources: flvx.RelaySources([]string{ulRelay, "relay.flvx.test"})}, ExpiresAt: now.Add(flvx.UserLimitsTTL).UnixMilli()}
		e.Sign("helper-e2e")
		if err := m.submitUserLimits(e); err != nil {
			t.Fatal(err)
		}
	}
	pass := func() {
		t.Helper()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		m.reconcileRates(ctx)
	}
	pass()
	for _, s := range m.state.Report.Nodes {
		if s.State != "active" {
			t.Fatalf("node %s %s %s", s.ID, s.State, s.Error)
		}
	}

	evidence := map[string]any{"core_sha256": mustHash(t, bin), "started_at": time.Now().UTC().Format(time.RFC3339)}
	// The client side may run another build (MMWXC_UL_CLIENT_CORE).
	clientBin := bin
	if v := os.Getenv("MMWXC_UL_CLIENT_CORE"); v != "" {
		clientBin = v
	}
	evidence["client_core_sha256"] = mustHash(t, clientBin)
	dr, ports := ulLabClient(t, base, c, clientBin, nodes)
	conns := func(node, src string, count int) []int {
		out := make([]int, count)
		for i := range out {
			out[i] = ports[node+"@"+src]
		}
		return out
	}
	// Leases end at most the Core's downlinkOnly (5 s) after the client closed.
	drain := func() {
		t.Helper()
		dr.closeAll()
		flvxWait30(t, func() bool {
			s, err := m.core.snapshot(ctx)
			if err != nil {
				return false
			}
			for _, u := range s.Users {
				if strings.HasPrefix(u.Identity.InboundTag, "flvxl-") && u.InboundCurrent > 0 {
					return false
				}
			}
			return true
		})
	}
	result := func(name string, got, want int) {
		t.Helper()
		evidence[name] = fmt.Sprintf("%d (want %d)", got, want)
		if got != want {
			t.Errorf("%s: %d admitted, want %d", name, got, want)
		}
	}
	two, seven, zero, one, oneIP := int64(2), int64(7), int64(0), int64(1), 1

	// 0. Release day: leases open before the owner has budgets are not in the
	// Core's group; the Helper counts them from the identities, so budget 2
	// with 2 relays + 1 direct open admits exactly 1 more direct.
	result("release_day_open_before_budgets", dr.open(conns("A1", ulRelay, 2)[0], conns("A1", ulRelay, 2)[1], ports["A1@203.0.113.50"]), 3)

	budgets(flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: &two})
	pass()
	// (2 + 2 relays) − 3 leases the group counter does not hold.
	result("release_day_core_group_limit", int(deref64Value(coreGroup(t, m, ctx, "flvx-user-26").MaxInboundConnections)), 1)
	result("release_day_direct_budget_2", dr.open(ports["A2@203.0.113.51"], ports["A1@203.0.113.52"], ports["A2@203.0.113.50"]), 1)
	pass()
	if u := m.userLimitsUpload(); len(u.Users) != 1 || u.Users[0].InboundConnections != 2 || u.Users[0].RelayConnections != 2 || strings.Join(u.Users[0].OnlineIPs, ",") != "203.0.113.50,203.0.113.51" {
		t.Errorf("release day usage %+v", u)
	} else {
		evidence["release_day_usage"] = u.Users
	}
	drain()

	// 1. Relay dedupe across two nodes of one owner: 4 relays open, budget 2
	// → exactly 2 direct admitted over A1 + A2; the usage FLVX gets excludes
	// the relays.
	hundred := int64(100)
	budgets(flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: &hundred})
	pass()
	result("relays_open", dr.open(append(conns("A1", ulRelay, 2), conns("A2", ulRelay, 2)...)...), 4)
	budgets(flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: &two})
	pass()
	// What the Core itself holds for the group: 2 + 4 live relays.
	result("core_group_limit_with_relays", int(deref64Value(coreGroup(t, m, ctx, "flvx-user-26").MaxInboundConnections)), 6)
	result("direct_over_two_nodes_budget_2", dr.open(conns("A1", "203.0.113.50", 2)[0], conns("A2", "203.0.113.51", 1)[0], conns("A1", "203.0.113.52", 1)[0], conns("A2", "203.0.113.50", 1)[0]), 2)
	result("other_owner_unaffected", dr.open(conns("B1", "203.0.113.50", 3)...), 3)
	pass()
	usage := m.userLimitsUpload()
	evidence["usage_step1"] = usage
	if len(usage.Users) != 1 || usage.Users[0].UserID != "26" || usage.Users[0].InboundConnections != 2 || usage.Users[0].RelayConnections != 4 || strings.Join(usage.Users[0].OnlineIPs, ",") != "203.0.113.50,203.0.113.51" || usage.AppliedSequence != sequence {
		t.Errorf("usage %+v", usage)
	}
	inbound := map[string]int64{}
	for _, s := range m.state.Report.Nodes {
		inbound[s.ID] = deref64Value(s.InboundConnections)
	}
	evidence["node_inbound_connections"] = inbound
	if inbound["A1"] != 3 || inbound["A2"] != 3 || inbound["B1"] != 3 {
		t.Errorf("node inbound counts %v", inbound)
	}
	drain()

	// 2. 入站连接数 of the server (relays included): cap 7 with 6 open → 1 of 2.
	budgets(flvx.UserLimitBudget{UserID: "26", ServerMaxInboundConnections: &seven})
	pass()
	result("server_cap_first_6", dr.open(append(conns("A1", ulRelay, 3), conns("A2", "203.0.113.50", 3)...)...), 6)
	pass()
	result("server_cap_7_with_6_open", dr.open(conns("A1", "203.0.113.51", 2)...), 1)
	drain()

	// 3. Budget 0 with nothing open → identities blocked (0/1); budget 1 → 1/2.
	budgets(flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: &zero})
	pass()
	blocked := 0
	if cfg := lastCoreConfig(t, m, ctx); cfg.BlockedIdentities != nil {
		blocked = len(*cfg.BlockedIdentities)
	}
	result("blocked_identities_full_idle", blocked, 2)
	result("budget_0_idle", dr.open(conns("A1", "203.0.113.50", 1)...), 0)
	result("budget_0_other_owner", dr.open(conns("B1", "203.0.113.50", 1)...), 1)
	budgets(flvx.UserLimitBudget{UserID: "26", MaxInboundConnections: &one})
	pass()
	result("budget_1", dr.open(conns("A2", "203.0.113.50", 2)...), 1)
	drain()

	// 4. Online IPs: 1 direct IP + relay reserve. With a direct and a relay
	// IP active, a new direct IP is refused while the relay still gets in;
	// without the reserve the relay would not fit next to the direct IP.
	// First let the Core's 30 s online-IP grace of the earlier steps pass.
	time.Sleep(31 * time.Second)
	budgets(flvx.UserLimitBudget{UserID: "26", MaxInboundOnlineIPs: &oneIP})
	pass()
	result("ip_first_direct", dr.open(conns("A1", "203.0.113.50", 1)...), 1)
	result("ip_relay_without_reserve_refused", dr.open(conns("A2", ulRelay, 1)...), 0)
	budgets(flvx.UserLimitBudget{UserID: "26", MaxInboundOnlineIPs: &oneIP, RelayIPReserve: 1})
	pass()
	result("ip_relay_with_reserve", dr.open(conns("A2", ulRelay, 1)...), 1)
	pass()
	result("core_group_ip_limit", int(derefIntValue(coreGroup(t, m, ctx, "flvx-user-26").MaxInboundOnlineIPs)), 2)
	result("ip_new_direct_refused", dr.open(conns("A1", "203.0.113.52", 1)...), 0)
	result("ip_relay_still_admitted", dr.open(conns("A1", ulRelay, 1)...), 1)
	result("ip_same_direct_again", dr.open(conns("A2", "203.0.113.50", 1)...), 1)
	drain()

	// 5. conn_mode inbound: the node limit counts admitted inbound only.
	budgets()
	m.state.Payload.Desired.Nodes[2].MaxConnections = 2
	m.state.Payload.Desired.Nodes[2].ConnMode = flvx.ConnModeInbound
	m.state.Payload.Desired.Revision++
	m.state.Payload.Version++
	pass()
	result("conn_mode_inbound_limit_2", dr.open(conns("B1", "203.0.113.50", 3)...), 2)
	drain()

	// 6. 速率 per owner: A1 + A2 share 20 Mbps each direction; B1 unlimited;
	// removing the rate frees it within one pass; expiry fails open.
	twenty := 20
	budgets(flvx.UserLimitBudget{UserID: "26", MaxRateMbps: &twenty})
	pass()
	rates := map[string]any{}
	down := dr.rate("download", 12, 3, ports["A1-down"], ports["A2-down"])
	// Upload is measured where it arrives: the landing Core's uplink
	// counters of A1 / A2 between 3 s and 11 s of a 12 s upload (the client
	// side would count its socket buffers too).
	uplink := func() map[string]int64 {
		raw, err := m.api(ctx, "statsquery", "-pattern", "inbound>>>flvxl-A")
		var stats struct {
			Stat []struct {
				Name  string      `json:"name"`
				Value json.Number `json:"value"`
			} `json:"stat"`
		}
		if err != nil || json.Unmarshal(raw, &stats) != nil {
			t.Fatalf("stats: %v", err)
		}
		out := map[string]int64{}
		for _, st := range stats.Stat {
			if strings.HasSuffix(st.Name, ">>>uplink") {
				v, _ := st.Value.Int64()
				out[strings.Split(st.Name, ">>>")[1]] = v
			}
		}
		return out
	}
	done := make(chan []float64, 1)
	go func() { done <- dr.rate("upload", 12, 0, ports["A1-up"], ports["A2-up"]) }()
	time.Sleep(3 * time.Second)
	u0 := uplink()
	time.Sleep(8 * time.Second)
	u1 := uplink()
	senderSide := <-done
	up := []float64{float64(u1["flvxl-A1"]-u0["flvxl-A1"]) / 8 / flvx.RateBytesPerMbps, float64(u1["flvxl-A2"]-u0["flvxl-A2"]) / 8 / flvx.RateBytesPerMbps}
	rates["owner26_upload_sender_side"] = senderSide
	other := dr.rate("download", 5, 1, ports["B1-down"])
	rates["owner26_download_two_nodes"], rates["owner26_upload_two_nodes"], rates["owner31_download"] = down, up, other
	sumDown, sumUp := down[0]+down[1], up[0]+up[1]
	if sumDown < 17 || sumDown > 23 || sumUp < 17 || sumUp > 23 || down[0] < 4 || down[1] < 4 || up[0] < 4 || up[1] < 4 {
		t.Errorf("shared 20 Mbps: download %v (sum %.1f) upload %v (sum %.1f)", down, sumDown, up, sumUp)
	}
	if other[0] < 100 {
		t.Errorf("other owner limited: %v", other)
	}
	if usage := m.userLimitsUpload(); len(usage.Users) == 0 || !usage.Users[0].RateApplied {
		t.Errorf("rate_applied missing: %+v", usage)
	}
	table, _ := exec.Command("ip", "netns", "exec", l, "nft", "list", "table", "inet", flvxRateTable).CombinedOutput()
	evidence["nft_table"] = string(table)
	budgets(flvx.UserLimitBudget{UserID: "26"})
	pass()
	freed := dr.rate("download", 4, 1, ports["A1-down"])
	rates["after_rate_removed"] = freed
	if freed[0] < 100 {
		t.Errorf("rate kept after removal: %v", freed)
	}
	budgets(flvx.UserLimitBudget{UserID: "26", MaxRateMbps: &twenty})
	pass()
	limited := dr.rate("download", 6, 2, ports["A1-down"])
	now = now.Add(flvx.UserLimitsTTL)
	pass()
	expired := dr.rate("download", 4, 1, ports["A1-down"])
	rates["before_expiry"], rates["after_expiry"] = limited, expired
	if limited[0] > 23 || expired[0] < 100 {
		t.Errorf("expiry: before %v after %v", limited, expired)
	}
	if out, err := exec.Command("ip", "netns", "exec", l, "nft", "list", "table", "inet", flvxRateTable).CombinedOutput(); err == nil {
		t.Errorf("rate table left after expiry: %s", out)
	}
	if cfg := lastCoreConfig(t, m, ctx); len(cfg.ManagementMappings) != 0 {
		t.Errorf("groups left after expiry: %+v", cfg.ManagementMappings)
	}
	evidence["rates_mbps"] = rates
	if rss, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", core.Process.Pid)); err == nil {
		for _, line := range strings.Split(string(rss), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				evidence["core_rss"] = strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
			}
		}
	}
	keys := make([]string, 0, len(evidence))
	for k := range evidence {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if out := os.Getenv("MMWXC_UL_E2E_OUT"); out != "" {
		os.WriteFile(out, raw, 0644)
	}
	t.Logf("evidence (%d keys):\n%s", len(keys), raw)
}

// ulLabNetns builds the three namespaces (client with direct and relay
// sources, landing, target), a 20 ms client path (TCP's reaction to the rate
// limiter depends on it) and starts the target services.
func ulLabNetns(t *testing.T, base string) (c, l, d string) {
	t.Helper()
	n := flvxNetns{t, base}
	c, l, d = "mmwxul-c", "mmwxul-l", "mmwxul-t"
	flvxNetnsCleanup(c, l, d)
	t.Cleanup(func() { flvxNetnsCleanup(c, l, d) })
	for _, ns := range []string{c, l, d} {
		n.sh("ip", "netns", "add", ns)
		n.sh("ip", "-n", ns, "link", "set", "lo", "up")
	}
	n.sh("ip", "link", "add", "mmwxul-c0", "type", "veth", "peer", "name", "mmwxul-l0")
	n.sh("ip", "link", "set", "mmwxul-c0", "netns", c)
	n.sh("ip", "link", "set", "mmwxul-l0", "netns", l)
	n.sh("ip", "link", "add", "mmwxul-l1", "type", "veth", "peer", "name", "mmwxul-t0")
	n.sh("ip", "link", "set", "mmwxul-l1", "netns", l)
	n.sh("ip", "link", "set", "mmwxul-t0", "netns", d)
	for _, a := range []string{"203.0.113.50/24", "203.0.113.51/24", "203.0.113.52/24", ulRelay + "/24"} {
		n.sh("ip", "-n", c, "addr", "add", a, "dev", "mmwxul-c0")
	}
	n.sh("ip", "-n", l, "addr", "add", ulLanding+"/24", "dev", "mmwxul-l0")
	n.sh("ip", "-n", l, "addr", "add", "11.77.0.1/24", "dev", "mmwxul-l1")
	n.sh("ip", "-n", d, "addr", "add", ulTarget+"/24", "dev", "mmwxul-t0")
	for _, link := range [][2]string{{c, "mmwxul-c0"}, {l, "mmwxul-l0"}, {l, "mmwxul-l1"}, {d, "mmwxul-t0"}} {
		n.sh("ip", "-n", link[0], "link", "set", link[1], "up")
	}
	n.sh("ip", "netns", "exec", c, "tc", "qdisc", "add", "dev", "mmwxul-c0", "root", "netem", "delay", "10ms")
	n.sh("ip", "netns", "exec", l, "tc", "qdisc", "add", "dev", "mmwxul-l0", "root", "netem", "delay", "10ms")
	os.WriteFile(filepath.Join(base, "target.py"), []byte(ulTargetServer), 0644)
	target := exec.Command("ip", "netns", "exec", d, "python3", filepath.Join(base, "target.py"))
	if err := target.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Process.Kill(); target.Wait() })
	return c, l, d
}

// ulLabClient starts the client Core (dokodemo 127.0.0.1:port → SS2022 node
// from a chosen source) and the driver. Ports: "<node id>@<source>" hold a
// connection, "<node id>-down" / "-up" transfer.
func ulLabClient(t *testing.T, base, c, bin string, nodes []flvx.Node) (*ulDriver, map[string]int) {
	t.Helper()
	ss := func(n flvx.Node) string { return n.SS2022.ServerKey + ":" + n.SS2022.UserKey }
	ports := map[string]int{}
	var inbounds, outbounds, rules []any
	add := func(name string, node flvx.Node, source string, targetPort int) {
		p := 22001 + len(inbounds)
		ports[name] = p
		inbounds = append(inbounds, map[string]any{"tag": "in-" + name, "listen": "127.0.0.1", "port": p, "protocol": "dokodemo-door", "settings": map[string]any{"address": ulTarget, "port": targetPort, "network": "tcp"}})
		outbounds = append(outbounds, map[string]any{"tag": "out-" + name, "protocol": "shadowsocks", "sendThrough": source, "settings": map[string]any{"servers": []any{map[string]any{"address": ulLanding, "port": node.Port, "method": node.SS2022.Method, "password": ss(node)}}}})
		rules = append(rules, map[string]any{"type": "field", "inboundTag": []string{"in-" + name}, "outboundTag": "out-" + name})
	}
	for _, node := range nodes {
		for _, src := range []string{"203.0.113.50", "203.0.113.51", "203.0.113.52", ulRelay} {
			add(node.ID+"@"+src, node, src, 8080)
		}
		add(node.ID+"-down", node, "203.0.113.50", 9000)
		add(node.ID+"-up", node, "203.0.113.50", 9002)
	}
	flvxNetnsCore(t, c, bin, filepath.Join(base, "client.json"), "", map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": inbounds, "outbounds": outbounds, "routing": map[string]any{"rules": rules}}, true)
	os.WriteFile(filepath.Join(base, "driver.py"), []byte(ulClientDriver), 0644)
	ctl := filepath.Join(base, "driver.sock")
	driver := exec.Command("ip", "netns", "exec", c, "python3", filepath.Join(base, "driver.py"), ctl)
	driverLog, _ := os.Create(filepath.Join(base, "driver.log"))
	driver.Stdout, driver.Stderr = driverLog, driverLog
	if err := driver.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { driver.Process.Kill(); driver.Wait() })
	var dconn net.Conn
	flvxWait(t, func() bool {
		var err error
		dconn, err = net.Dial("unix", ctl)
		return err == nil
	})
	return &ulDriver{t: t, conn: dconn, r: bufio.NewReader(dconn)}, ports
}

// lastCoreConfig reads back what the pass sent: the Core echoes its limits
// in the snapshot, so rebuild the config the manager applied instead.
func lastCoreConfig(t *testing.T, m *flvxManager, ctx context.Context) coreConfig {
	t.Helper()
	snapshot, err := m.core.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings := defaultConnectionSettings()
	wanted := map[string]string{}
	for tag, config := range m.state.Applied {
		wanted[tag] = config
	}
	for _, identity := range m.applyUserLimits(&settings, m.budgetView(), snapshot, wanted) {
		settings.BlockedIdentities = append(settings.BlockedIdentities, identity)
	}
	return settings.coreConfig()
}

// coreGroup is the Core's own snapshot of a management group (its limits
// are the ones in force).
func coreGroup(t *testing.T, m *flvxManager, ctx context.Context, name string) managementGroupSnapshot {
	t.Helper()
	snapshot, err := m.core.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range snapshot.ManagementGroups {
		if g.Username == name {
			return g
		}
	}
	t.Fatalf("group %s not in the Core", name)
	return managementGroupSnapshot{}
}

func derefIntValue(v *int) int {
	if v == nil {
		return -1
	}
	return *v
}

func deref64Value(v *int64) int64 {
	if v == nil {
		return -1
	}
	return *v
}

func flvxWait30(t *testing.T, fn func() bool) {
	t.Helper()
	until := time.Now().Add(30 * time.Second)
	for time.Now().Before(until) {
		if fn() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("leases did not drain")
}
