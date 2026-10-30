package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tc shaper against the real kernel, without a Core, in two private
// namespaces (the host network is never touched):
//
//	client 203.0.113.50 / 2001:db8::50 ── (20 ms RTT) ── landing .1 / ::1
//
// Owner 26 has a 20 Mbps 速率 on 31001 (TCP + UDP) and 31002; port 31004
// (another owner) has none. Plain TCP / UDP bulk transfers measure the split
// between the owner's connections, one connection alone, the other port, and
// the kernel objects through start (leftovers of an earlier process and of
// v0.7.15's nft table), restart, a deleted IFB, a live rate change and the
// release. A foreign clsact filter and the namespace's netem root qdiscs stay
// untouched. Opt-in: root, MMWXC_TEST_FLVX_NETNS=1; MMWXC_RATE_NETNS_OUT
// receives the evidence JSON.

const rateNetnsServer = `import socket, sys, threading, time
tcp = [int(p) for p in sys.argv[1].split(",")]
uport, counter = int(sys.argv[2]), sys.argv[3]
def handle(c):
    try:
        if c.recv(1) == b"d":
            buf = b"\0" * 65536
            while True:
                c.sendall(buf)
        while c.recv(1 << 16):
            pass
    except Exception:
        pass
    c.close()
def serve(fam, addr, port):
    s = socket.socket(fam)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    if fam == socket.AF_INET6:
        s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
    s.bind((addr, port)); s.listen(64)
    while True:
        c, _ = s.accept()
        threading.Thread(target=handle, args=(c,), daemon=True).start()
for p in tcp:
    threading.Thread(target=serve, args=(socket.AF_INET, "0.0.0.0", p), daemon=True).start()
    threading.Thread(target=serve, args=(socket.AF_INET6, "::", p), daemon=True).start()
u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
u.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4 << 20)
u.bind(("0.0.0.0", uport))
got = [0]
def blast(addr, seconds):
    buf, end = b"\0" * 1200, time.time() + seconds
    while time.time() < end:
        try:
            u.sendto(buf, addr)
        except OSError:
            pass
def write():
    while True:
        with open(counter + ".tmp", "w") as f:
            f.write("%d %f" % (got[0], time.time()))
        import os; os.replace(counter + ".tmp", counter)
        time.sleep(0.2)
threading.Thread(target=write, daemon=True).start()
while True:
    d, a = u.recvfrom(65536)
    if d.startswith(b"D"):
        threading.Thread(target=blast, args=(a, float(d[1:])), daemon=True).start()
    else:
        got[0] += len(d)
`

// rateNetnsClient: tcp-down / tcp-up host port,port seconds warm → Mbps per
// connection (FLVX units, after warm; uploads counted as sent), udp-down
// host port seconds warm → Mbps received, udp-up host port seconds → sends.
const rateNetnsClient = `import json, socket, sys, threading, time
op, host, ports, seconds, warm = sys.argv[1], sys.argv[2], [int(p) for p in sys.argv[3].split(",")], float(sys.argv[4]), float(sys.argv[5])
out = [0.0] * len(ports)
def tcp(i, port):
    n = 0
    c = socket.create_connection((host, port), timeout=5)
    c.settimeout(1)
    up = op == "tcp-up"
    c.sendall(b"u" if up else b"d")
    start = time.time(); end, counting = start + seconds, start + warm
    buf = b"\0" * 65536
    while time.time() < end:
        try:
            k = c.send(buf) if up else len(c.recv(1 << 16))
        except socket.timeout:
            continue
        if not up and k == 0:
            break
        if time.time() >= counting:
            n += k
    c.close()
    out[i] = n / (seconds - warm) / 131072
if op.startswith("tcp"):
    ts = [threading.Thread(target=tcp, args=(i, p)) for i, p in enumerate(ports)]
    [t.start() for t in ts]; [t.join() for t in ts]
elif op == "udp-down":
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4 << 20)
    s.settimeout(0.5)
    s.sendto(b"D%f" % seconds, (host, ports[0]))
    start = time.time(); n = 0
    while time.time() < start + seconds:
        try:
            k = len(s.recv(65536))
        except socket.timeout:
            continue
        if time.time() >= start + warm:
            n += k
    out[0] = n / (seconds - warm) / 131072
else:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    buf, end = b"\0" * 1200, time.time() + seconds
    while time.time() < end:
        s.sendto(buf, (host, ports[0]))
print(json.dumps(out))
`

func TestFLVXRateNetns(t *testing.T) {
	if os.Getenv("MMWXC_TEST_FLVX_NETNS") == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_TEST_FLVX_NETNS=1 and run as root")
	}
	for _, tool := range []string{"python3", "ip", "tc", "unshare"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " missing")
		}
	}
	base := t.TempDir()
	os.Chmod(base, 0755)
	n := flvxNetns{t, base}
	c, l := "mmwxrt-c", "mmwxrt-l"
	flvxNetnsCleanup(c, l)
	t.Cleanup(func() { flvxNetnsCleanup(c, l) })
	for _, ns := range []string{c, l} {
		n.sh("ip", "netns", "add", ns)
		n.sh("ip", "-n", ns, "link", "set", "lo", "up")
	}
	n.sh("ip", "link", "add", "mmwxrt-c0", "type", "veth", "peer", "name", "mmwxrt-l0")
	n.sh("ip", "link", "set", "mmwxrt-c0", "netns", c)
	n.sh("ip", "link", "set", "mmwxrt-l0", "netns", l)
	n.sh("ip", "-n", c, "addr", "add", "203.0.113.50/24", "dev", "mmwxrt-c0")
	n.sh("ip", "-n", c, "addr", "add", "2001:db8::50/64", "dev", "mmwxrt-c0", "nodad")
	n.sh("ip", "-n", l, "addr", "add", "203.0.113.1/24", "dev", "mmwxrt-l0")
	n.sh("ip", "-n", l, "addr", "add", "2001:db8::1/64", "dev", "mmwxrt-l0", "nodad")
	n.sh("ip", "-n", c, "link", "set", "mmwxrt-c0", "up")
	n.sh("ip", "-n", l, "link", "set", "mmwxrt-l0", "up")
	// A VLAN on the shaped device (skipped: its packets leave through it).
	n.sh("ip", "-n", l, "link", "add", "link", "mmwxrt-l0", "name", "mmwxrt-l0.7", "type", "vlan", "id", "7")
	n.sh("ip", "-n", l, "addr", "add", "198.51.100.1/24", "dev", "mmwxrt-l0.7")
	n.sh("ip", "-n", l, "link", "set", "mmwxrt-l0.7", "up")
	n.sh("ip", "netns", "exec", c, "tc", "qdisc", "add", "dev", "mmwxrt-c0", "root", "netem", "delay", "10ms")
	n.sh("ip", "netns", "exec", l, "tc", "qdisc", "add", "dev", "mmwxrt-l0", "root", "netem", "delay", "10ms")
	// Another program's clsact + filter, and what earlier Helpers left: an IFB
	// with a filter of ours pointing at it, v0.7.15's nft table.
	n.sh("ip", "netns", "exec", l, "tc", "qdisc", "add", "dev", "mmwxrt-l0", "clsact")
	n.sh("ip", "netns", "exec", l, "tc", "filter", "add", "dev", "mmwxrt-l0", "ingress", "pref", "100", "protocol", "ip", "flower", "ip_proto", "icmp", "action", "ok")
	n.sh("ip", "-n", l, "link", "add", flvxRateIfbUp, "type", "ifb")
	n.sh("ip", "-n", l, "link", "set", flvxRateIfbUp, "up")
	n.sh("ip", "netns", "exec", l, "tc", "filter", "add", "dev", "mmwxrt-l0", "egress", "pref", "61870", "protocol", "ip", "flower", "ip_proto", "tcp", "src_port", "31009", "action", "mirred", "egress", "redirect", "dev", flvxRateIfbUp)
	nftOld := exec.Command("ip", "netns", "exec", l, "nft", "add", "table", "inet", flvxRateNftTable).Run() == nil

	tc := func(args ...string) string {
		out, _ := exec.Command("ip", append([]string{"netns", "exec", l, "tc"}, args...)...).CombinedOutput()
		return string(out)
	}
	ours := func(dev, hook string) (mine, foreign int) {
		var filters []struct {
			Pref    int            `json:"pref"`
			Options map[string]any `json:"options"`
		}
		json.Unmarshal([]byte(tc("-j", "filter", "show", "dev", dev, hook)), &filters)
		for _, f := range filters {
			if f.Options == nil {
				continue // the tcf_proto header
			}
			if f.Pref == flvxRatePrefV4 || f.Pref == flvxRatePrefV6 {
				mine++
			} else {
				foreign++
			}
		}
		return
	}
	links := func() string {
		out, _ := exec.Command("ip", "-n", l, "-br", "link", "show", "type", "ifb").CombinedOutput()
		return strings.TrimSpace(string(out))
	}

	now := time.Unix(1793300000, 0)
	r := flvxRateLimiterIn(l, func() time.Time { return now })
	ctx := context.Background()
	if !r.supported(ctx) {
		t.Fatal("probe failed on this kernel")
	}
	owner := flvxRateOwner{Owner: "26", Bytes: 20 * 131072, TCP: []int{31001, 31002}, UDP: []int{31001}}
	ensure := func(owners ...flvxRateOwner) {
		t.Helper()
		now = now.Add(5 * time.Second)
		if err := r.ensure(ctx, owners); err != nil {
			t.Fatal(err)
		}
	}
	ensure(owner)
	evidence := map[string]any{}
	if exec.Command("ip", "netns", "exec", l, "nft", "list", "table", "inet", flvxRateNftTable).Run() == nil {
		t.Error("v0.7.15 nft table left")
	}
	evidence["nft_table_removed"] = nftOld
	mineIn, foreignIn := ours("mmwxrt-l0", "ingress")
	mineOut, _ := ours("mmwxrt-l0", "egress")
	// 3 (port, proto) pairs × IPv4 / IPv6 = 6 per direction; the leftover
	// filter on 31009 is gone, the foreign one stays.
	if mineIn != 6 || mineOut != 6 || foreignIn != 1 {
		t.Fatalf("filters: ours %d/%d foreign %d\n%s", mineIn, mineOut, foreignIn, tc("filter", "show", "dev", "mmwxrt-l0", "egress"))
	}
	if strings.Contains(tc("qdisc", "show", "dev", "mmwxrt-l0.7"), "clsact") || strings.Contains(tc("qdisc", "show", "dev", "lo"), "clsact") {
		t.Fatal("attached to the VLAN or lo")
	}
	for _, ifb := range []string{flvxRateIfbUp, flvxRateIfbDown} {
		if cls := tc("class", "show", "dev", ifb); !strings.Contains(cls, "class htb 7f1b:10 root leaf 1: prio 0 rate 22229Kbit ceil 22229Kbit") {
			t.Fatalf("class on %s: %s", ifb, cls)
		}
		if q := tc("qdisc", "show", "dev", ifb); !strings.Contains(q, "qdisc fq_codel 1: parent 7f1b:10") {
			t.Fatalf("leaf on %s: %s", ifb, q)
		}
	}
	evidence["objects"] = tc("qdisc", "show") + tc("class", "show", "dev", flvxRateIfbUp) + tc("filter", "show", "dev", "mmwxrt-l0", "ingress") + tc("filter", "show", "dev", "mmwxrt-l0", "egress")
	if root := tc("qdisc", "show", "dev", "mmwxrt-l0"); !strings.Contains(root, "qdisc netem") {
		t.Fatalf("root qdisc touched: %s", root)
	}

	os.WriteFile(filepath.Join(base, "server.py"), []byte(rateNetnsServer), 0644)
	os.WriteFile(filepath.Join(base, "client.py"), []byte(rateNetnsClient), 0644)
	counter := filepath.Join(base, "udp-counter")
	server := exec.Command("ip", "netns", "exec", l, "python3", filepath.Join(base, "server.py"), "31001,31002,31004", "31001", counter)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() })
	time.Sleep(time.Second)
	client := func(op, host string, seconds, warm float64, ports ...int) []float64 {
		t.Helper()
		list := make([]string, len(ports))
		for i, p := range ports {
			list[i] = strconv.Itoa(p)
		}
		out, err := exec.Command("ip", "netns", "exec", c, "python3", filepath.Join(base, "client.py"), op, host, strings.Join(list, ","), fmt.Sprint(seconds), fmt.Sprint(warm)).Output()
		var mbps []float64
		if err != nil || json.Unmarshal(out, &mbps) != nil {
			t.Fatalf("client %s %v: %v %s", op, ports, err, out)
		}
		return mbps
	}
	udpReceived := func() (int64, float64) {
		raw, _ := os.ReadFile(counter)
		var n int64
		var at float64
		fmt.Sscanf(string(raw), "%d %f", &n, &at)
		return n, at
	}
	fair := func(v []float64) float64 {
		lo, hi := v[0], v[0]
		for _, x := range v {
			lo, hi = min(lo, x), max(hi, x)
		}
		if hi == 0 {
			return 0
		}
		return lo / hi
	}

	down := client("tcp-down", "203.0.113.1", 12, 3, 31001, 31002)
	up := client("tcp-up", "203.0.113.1", 12, 3, 31001, 31002)
	down4 := client("tcp-down", "203.0.113.1", 12, 3, 31001, 31002, 31001, 31002)
	alone := client("tcp-down", "203.0.113.1", 8, 3, 31001)
	aloneUp := client("tcp-up", "203.0.113.1", 8, 3, 31002)
	v6 := client("tcp-down", "2001:db8::1", 8, 3, 31002)
	other := client("tcp-down", "203.0.113.1", 5, 1, 31004)
	udpDown := client("udp-down", "203.0.113.1", 6, 2, 31001)
	n0, t0 := udpReceived()
	client("udp-up", "203.0.113.1", 6, 0, 31001)
	time.Sleep(500 * time.Millisecond)
	n1, t1 := udpReceived()
	udpUp := float64(n1-n0) / (t1 - t0) / 131072
	evidence["mbps"] = map[string]any{"download_two": down, "upload_two": up, "download_four": down4, "download_alone": alone, "upload_alone": aloneUp, "download_ipv6_alone": v6, "other_port_download": other, "udp_download": udpDown, "udp_upload_received": udpUp}
	evidence["fairness"] = map[string]float64{"download_two": fair(down), "upload_two": fair(up), "download_four": fair(down4)}
	total := func(v []float64) float64 {
		s := 0.0
		for _, x := range v {
			s += x
		}
		return s
	}
	for name, v := range map[string][]float64{"download two": down, "upload two": up, "download four": down4} {
		if s := total(v); s < 18 || s > 22 || fair(v) < 0.9 {
			t.Errorf("%s: %v (sum %.2f, slowest/fastest %.3f)", name, v, s, fair(v))
		}
	}
	for name, v := range map[string]float64{"download alone": alone[0], "upload alone": aloneUp[0], "IPv6 download": v6[0]} {
		if v < 18 || v > 22 {
			t.Errorf("%s: %.2f (want ≥ 90 %% of 20)", name, v)
		}
	}
	if other[0] < 100 {
		t.Errorf("other port limited: %.1f", other[0])
	}
	// UDP: the shaper drops the excess (no window): what arrives ≈ the rate
	// (payload 1200 / 1242 bytes on the wire, + 6 %).
	if udpDown[0] < 17 || udpDown[0] > 23 || udpUp < 17 || udpUp > 23 {
		t.Errorf("UDP: down %.2f up %.2f", udpDown[0], udpUp)
	}

	// A Helper restart: the new process removes the old set and builds one.
	r = flvxRateLimiterIn(l, func() time.Time { return now })
	ensure(owner)
	if mine, _ := ours("mmwxrt-l0", "egress"); mine != 6 || links() == "" || strings.Count(links(), "\n") != 1 {
		t.Fatalf("after restart: %d filters, ifbs %q", mine, links())
	}
	// Someone deletes an IFB: a redirect into nothing drops packets, so the
	// next check (a minute later) rebuilds everything.
	n.sh("ip", "-n", l, "link", "del", flvxRateIfbDown)
	now = now.Add(time.Minute)
	ensure(owner)
	rebuilt := client("tcp-down", "203.0.113.1", 5, 2, 31001)
	if rebuilt[0] < 15 || rebuilt[0] > 22 || !strings.Contains(links(), flvxRateIfbDown) {
		t.Errorf("after IFB deleted: %v %q", rebuilt, links())
	}
	// Live change 20 → 40 on a running download: the class changes in place,
	// the connection keeps going.
	done := make(chan []float64, 1)
	go func() { done <- client("tcp-down", "203.0.113.1", 12, 6, 31001) }()
	time.Sleep(4 * time.Second)
	owner.Bytes = 40 * 131072
	ensure(owner)
	changed := <-done
	if changed[0] < 36 || changed[0] > 44 {
		t.Errorf("live change to 40: %v", changed)
	}
	evidence["after_restart_rebuild_mbps"], evidence["live_change_to_40_mbps"] = rebuilt, changed
	// Release: unlimited at once (queue kept), everything of ours gone at the
	// next pass; the foreign clsact filter and the root qdisc stay.
	ensure()
	freed := client("tcp-down", "203.0.113.1", 4, 1, 31001)
	ensure()
	mine, foreign := ours("mmwxrt-l0", "ingress")
	mineOut, _ = ours("mmwxrt-l0", "egress")
	if freed[0] < 100 || mine != 0 || mineOut != 0 || foreign != 1 || links() != "" || !strings.Contains(tc("qdisc", "show", "dev", "mmwxrt-l0"), "clsact") || !strings.Contains(tc("qdisc", "show", "dev", "mmwxrt-l0"), "netem") {
		t.Errorf("release: %v ours %d/%d foreign %d ifbs %q\n%s", freed, mine, mineOut, foreign, links(), tc("qdisc", "show"))
	}
	evidence["released_mbps"] = freed
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if out := os.Getenv("MMWXC_RATE_NETNS_OUT"); out != "" {
		os.WriteFile(out, raw, 0644)
	}
	t.Logf("%s", raw)
}
