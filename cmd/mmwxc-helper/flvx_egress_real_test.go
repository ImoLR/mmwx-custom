package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

const realCoreSHA = "ae4b83f0cf77c4aa73857cb1de773e7ec89b1bc5ded272a677b46d417cf40212"

func realEgressCore(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("MMWXC_TEST_FLVX_CORE")
	if bin == "" {
		t.Skip("MMWXC_TEST_FLVX_CORE not configured")
	}
	if hash, err := flvxFileHash(bin); err != nil || hash != realCoreSHA {
		t.Fatal("real test requires the v1.4.59 amd64 release Core")
	}
	return bin
}

// realGeo copies the pinned geo files into dir/geo when MMWXC_TEST_GEO_DIR is
// set (verified against the pinned SHA-256 by the manager itself).
func realGeo(t *testing.T, dir string) bool {
	t.Helper()
	src := os.Getenv("MMWXC_TEST_GEO_DIR")
	if src == "" {
		return false
	}
	if err := os.MkdirAll(filepath.Join(dir, "geo"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if err := copyFile(filepath.Join(src, name), filepath.Join(dir, "geo", name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return true
}

func goldenEgressPlan(tags []string) *flvx.Egress {
	key := "YFYOAdbw1bKTHlNNi+aEjBM3BO7unuFC5rOkMRAz9XY="
	peer := "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
	ss := "AAAAAAAAAAAAAAAAAAAAAA=="
	return &flvx.Egress{Version: 1, Outbounds: []flvx.EgressOutbound{
		{Tag: "flvxo-direct4", Type: "direct", IPVersion: "force_ipv4"},
		{Tag: "flvxo-direct6", Type: "direct", IPVersion: "force_ipv6"},
		{Tag: "flvxo-pref6", Type: "direct", IPVersion: "prefer_ipv6"},
		{Tag: "flvxo-warp", Type: "wireguard", IPVersion: "force_ipv4", WireGuard: &flvx.EgressWireGuard{PrivateKey: key, PeerPublicKey: peer, AddressV4: "172.16.0.2", AddressV6: "2606:4700:110:8a36::2", Reserved: []int{1, 2, 3}}},
		{Tag: "flvxo-socks", Type: "socks", IPVersion: "prefer_ipv4", Server: "203.0.113.30", Port: 1080, Username: "u", Password: "p"},
		{Tag: "flvxo-http", Type: "http", IPVersion: "system", Server: "203.0.113.31", Port: 8080},
		{Tag: "flvxo-ss", Type: "shadowsocks", IPVersion: "force_ipv4", Server: "203.0.113.32", Port: 8388, Method: "2022-blake3-aes-128-gcm", Password: ss + ":" + ss},
		{Tag: "flvxo-vless", Type: "vless", IPVersion: "force_ipv4", Server: "198.51.100.7", Port: 443, UUID: "d77c8b43-40ed-42cd-9150-f3f47d0f534d", Flow: "xtls-rprx-vision", Reality: &flvx.EgressReality{ServerName: "www.example.com", PublicKey: "Ak6Ci7Ogvl2t29y1MUSF4fuAtoXtt4EnCyU_cdhn4Ws", ShortID: "0a1b"}},
	}, Rules: []flvx.EgressRule{
		{Name: "AI", InboundTags: tags, Domain: []string{"domain:claude.ai", "full:x.ai", "keyword:gemini", "regexp:^chat\\."}, Outbound: "flvxo-warp"},
		{Name: "BT", InboundTags: tags, Protocol: []string{"bittorrent"}, SourcePort: "1024-65535", Outbound: "block"},
		{Name: "Port", InboundTags: tags, IP: []string{"203.0.113.0/24"}, Port: "9", Network: "tcp", Outbound: "block"},
		{Name: "Src", InboundTags: tags, SourceIP: []string{"198.51.100.0/24"}, Outbound: "flvxo-direct6"},
		{Name: "proxies", InboundTags: tags, Port: "1-8", Outbound: "flvxo-socks"},
		{Name: "proxies2", InboundTags: tags, Port: "10-11", Outbound: "flvxo-http"},
		{Name: "proxies3", InboundTags: tags, Port: "12", Outbound: "flvxo-ss"},
		{Name: "proxies4", InboundTags: tags, Port: "13", Outbound: "flvxo-vless"},
		{Name: "pref", InboundTags: tags, Port: "14", Outbound: "flvxo-pref6"},
		{Name: "Default", InboundTags: tags, Outbound: "flvxo-direct4"},
	}}
}

func cnRules(tags []string) []flvx.EgressRule {
	return []flvx.EgressRule{
		{Name: "屏蔽大陆 IP", InboundTags: tags, IP: []string{"geoip:cn"}, Outbound: "block"},
		{Name: "屏蔽大陆网站", InboundTags: tags, Domain: []string{"geosite:cn"}, Outbound: "block"},
		{Name: "禁止大陆直连", InboundTags: tags, SourceIP: []string{"geoip:cn"}, Outbound: "block"},
		{Name: "AI", InboundTags: tags, Domain: []string{"geosite:openai", "geosite:anthropic"}, Outbound: "flvxo-warp"},
	}
}

func TestFLVXEgressRealCore(t *testing.T) {
	bin := realEgressCore(t)
	dir := t.TempDir()
	ctx := context.Background()
	hasGeo := realGeo(t, filepath.Join(dir, "state"))
	local := flvx.LocalAddrs{V4: netip.MustParseAddr("127.0.0.1"), V6: netip.MustParseAddr("::1")}
	tags := []string{"flvxl-real"}

	// 1. The Core accepts a config with every outbound type x IP version and
	// (with the pinned geo data) every matcher kind.
	plan := goldenEgressPlan(tags)
	if hasGeo {
		plan.Rules = append(cnRules(tags), plan.Rules...)
		plan.ExemptSources = []string{"1.0.1.3/32", "2001:db8::1/128"}
	}
	full := flvx.BaseConfig(flvxFreePort(t))
	outbounds := full["outbounds"].([]any)
	tagMap := map[string]string{"direct": "direct", "block": "block"}
	for _, o := range plan.Outbounds {
		for _, version := range []string{"system", "prefer_ipv4", "prefer_ipv6", "force_ipv4", "force_ipv6"} {
			variant := o
			variant.IPVersion = version
			if o.Type != "direct" && o.Type != "wireguard" && strings.Contains(o.Server, ".") && version == "force_ipv6" {
				variant.Server = "2001:db8::30"
			}
			cfg, _ := flvx.RenderOutbound(variant, local)
			cfg["tag"] = flvx.CoreTag(o.Tag+"-"+strings.ReplaceAll(version, "_", ""), cfg)
			outbounds = append(outbounds, cfg)
			if version == o.IPVersion {
				tagMap[o.Tag] = cfg["tag"].(string)
			}
		}
	}
	mapped := *plan
	mapped.Rules = nil
	for _, r := range plan.Rules {
		r.Outbound = tagMap[r.Outbound]
		mapped.Rules = append(mapped.Rules, r)
	}
	geoip := filepath.Join(dir, "state", "geo", "geoip.dat")
	rules, err := flvx.RenderRules(&mapped, func(code string) ([]netip.Prefix, error) { return flvx.ReadGeoIP(geoip, code) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	full["outbounds"] = outbounds
	full["routing"].(map[string]any)["rules"] = rules
	full["inbounds"] = []any{flvx.Inbound(helperFLVXNode("real", flvxFreePort(t)))}
	raw, _ := json.Marshal(full)
	path := filepath.Join(dir, "full.json")
	os.WriteFile(path, raw, 0600)
	test := exec.Command(bin, "run", "-test", "-config", path)
	test.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+filepath.Join(dir, "state", "geo"))
	if out, err := test.CombinedOutput(); err != nil || !strings.Contains(string(out), "Configuration OK") {
		t.Fatalf("run -test rejected the rendered config: %v\n%s", err, out)
	}
	t.Logf("run -test: %d outbounds (%d types x 5 IP versions), %d rules, geo=%t: Configuration OK", len(outbounds), len(plan.Outbounds), len(rules), hasGeo)

	// 2. Hot apply through the Helper on a Core started from the pre-egress
	// base config: the CLI reports RoutingService as Unimplemented, the Helper
	// restarts the Core exactly once, then every plan change is hot.
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	apiPort, nodePort, socksPort := flvxFreePort(t), flvxFreePort(t), flvxFreePort(t)
	base := flvx.BaseConfig(apiPort)
	base["outbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["redirect"] = echo.Addr().String()
	legacy := flvx.BaseConfig(apiPort)
	legacy["api"].(map[string]any)["services"] = []string{"HandlerService", "StatsService"}
	legacy["outbounds"] = base["outbounds"]
	socket := filepath.Join(dir, "control.sock")
	stopCore := flvxStartCore(t, bin, filepath.Join(dir, "core.json"), socket, legacy)
	core := newCoreClient(socket)
	flvxWait(t, func() bool { _, err := core.snapshot(ctx); return err == nil })
	node := helperFLVXNode("real", nodePort)
	node.MaxConnections, node.MaxOnlineIPs = 0, 0
	m := &flvxManager{dir: filepath.Join(dir, "state"), binary: bin, core: core, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 1024, PortMax: 65535, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}, Desired: flvx.Desired{Revision: 1, Nodes: []flvx.Node{node}}}, APIPort: apiPort, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "real-egress"}}
	m.setup = func(context.Context) error { return nil }
	m.stop = func(context.Context) error { stopCore(); return nil }
	m.readOfficial = func() ([]int, error) { return nil, nil }
	m.detectLocal = func() flvx.LocalAddrs { return flvx.LocalAddrs{V4: netip.MustParseAddr("127.0.0.1")} }
	m.memory = func() (int64, int64, bool) { return 30 << 20, 1 << 30, true }
	restarts := 0
	m.restartCore = func(context.Context) error {
		restarts++
		stopCore()
		stopCore = flvxStartCore(t, bin, filepath.Join(dir, "core.json"), socket, base)
		return nil
	}
	// Loopback fixture only: inbounds listen on 127.0.0.1 and plan freedom
	// outbounds redirect public test targets to the local echo server.
	m.apiCall = func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if command == "adi" || command == "ado" {
			raw, _ := os.ReadFile(args[0])
			var cfg map[string]any
			json.Unmarshal(raw, &cfg)
			if list, ok := cfg["inbounds"].([]any); ok {
				for _, v := range list {
					v.(map[string]any)["listen"] = "127.0.0.1"
				}
			}
			if list, ok := cfg["outbounds"].([]any); ok {
				for _, v := range list {
					if o := v.(map[string]any); o["protocol"] == "freedom" {
						o["settings"].(map[string]any)["redirect"] = echo.Addr().String()
					}
				}
			}
			raw, _ = json.Marshal(cfg)
			os.WriteFile(args[0], raw, 0600)
		}
		return m.api(ctx, command, args...)
	}
	reconcile := func() {
		t.Helper()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if e := m.state.Report.Egress; e == nil || e.State == "error" {
			t.Fatalf("egress status %+v", e)
		}
	}
	reconcile()
	if restarts != 0 || m.state.Report.Egress.State != "none" {
		t.Fatal("default plan must not restart the Core")
	}
	clientConfig := map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}, "outbounds": []any{map[string]any{"protocol": "shadowsocks", "settings": map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": nodePort, "method": node.SS2022.Method, "password": node.SS2022.ServerKey + ":" + node.SS2022.UserKey}}}}}}
	flvxStartCore(t, bin, filepath.Join(dir, "client.json"), "", clientConfig)
	flvxWait(t, func() bool {
		c, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(socksPort)), time.Second)
		if e == nil {
			c.Close()
		}
		return e == nil
	})
	works := func(target string) bool {
		c := flvxSOCKSPort(t, socksPort, target, 80)
		defer c.Close()
		c.SetDeadline(time.Now().Add(2 * time.Second))
		c.Write([]byte("ping"))
		got := make([]byte, 4)
		_, err := io.ReadFull(c, got)
		return err == nil && string(got) == "ping"
	}
	if !works("203.0.113.10") {
		t.Fatal("default direct path broken")
	}
	// An active plan on the legacy Core: one restart, then applied.
	m.state.Payload.Desired.Revision = 2
	m.state.Payload.Desired.Egress = &flvx.Egress{Version: 1, Outbounds: []flvx.EgressOutbound{{Tag: "flvxo-f4", Type: "direct", IPVersion: "force_ipv4"}}, Rules: []flvx.EgressRule{
		{Name: "block one", InboundTags: tags, IP: []string{"203.0.113.66"}, Outbound: "block"},
		{Name: "default", InboundTags: tags, Outbound: "flvxo-f4"},
	}}
	reconcile()
	snapshot, _ := core.snapshot(ctx)
	epoch := snapshot.StartedAt
	if restarts != 1 || m.state.Report.Egress.State != "applied" {
		t.Fatalf("restart once: restarts=%d %+v", restarts, m.state.Report.Egress)
	}
	listed, err := m.api(ctx, "lsrules")
	if err != nil || !strings.Contains(string(listed), "flvx-safety") || strings.Index(string(listed), "flvx-safety") > strings.Index(string(listed), "flvxr-1") {
		t.Fatalf("rules after apply: %s %v", listed, err)
	}
	// The forced-IPv4 outbound binds 127.0.0.1 here and is the node default;
	// the per-rule block wins first.
	if !works("203.0.113.10") || works("203.0.113.66") {
		t.Fatal("plan routing wrong")
	}
	// Plan changes stay hot: rules, outbounds, back to default.
	stream := flvxSOCKSPort(t, socksPort, "203.0.113.11", 80)
	defer stream.Close()
	exchange := func() {
		t.Helper()
		stream.SetDeadline(time.Now().Add(2 * time.Second))
		stream.Write([]byte("live"))
		got := make([]byte, 4)
		if _, err := io.ReadFull(stream, got); err != nil {
			t.Fatal("established stream interrupted by a plan change", err)
		}
	}
	exchange()
	m.state.Payload.Desired.Revision = 3
	m.state.Payload.Desired.Egress.Rules[0].IP = []string{"203.0.113.67"}
	m.state.Payload.Desired.Egress.Outbounds[0].IPVersion = "prefer_ipv4"
	reconcile()
	exchange()
	if !works("203.0.113.66") || works("203.0.113.67") {
		t.Fatal("hot rule replacement not effective")
	}
	if hasGeo {
		m.state.Payload.Desired.Revision = 4
		m.state.Payload.Desired.Egress.Outbounds = append(m.state.Payload.Desired.Egress.Outbounds, plan.Outbounds[3])
		m.state.Payload.Desired.Egress.Rules = append(cnRules(tags), m.state.Payload.Desired.Egress.Rules...)
		m.state.Payload.Desired.Egress.ExemptSources = []string{"1.0.1.3/32"}
		started := time.Now()
		reconcile()
		t.Logf("「屏蔽中国大陆」 + AI geosite applied hot in %s (geo payload %d bytes)", time.Since(started).Round(time.Millisecond), m.state.EgressGeoBytes)
		exchange()
		// 127.0.0.1 is no CN source; geoip:cn targets are blocked.
		if !works("203.0.113.66") || works("1.0.1.1") {
			t.Fatal("geo rules not effective")
		}
	}
	m.state.Payload.Desired.Revision = 5
	m.state.Payload.Desired.Egress = nil
	reconcile()
	exchange()
	if !works("203.0.113.67") || m.state.EgressApplied != "" {
		t.Fatal("default plan not restored")
	}
	listedOutbounds, _ := m.listFLVXOutbounds(ctx)
	for tag := range listedOutbounds {
		if strings.HasPrefix(tag, "flvxo-") {
			t.Fatalf("plan outbound %s left behind", tag)
		}
	}
	if after, _ := core.snapshot(ctx); !after.StartedAt.Equal(epoch) || restarts != 1 {
		t.Fatalf("plan changes restarted the Core (restarts=%d)", restarts)
	}
	t.Logf("real Core v1.4.59: legacy Core restarted once for RoutingService; 3 plan changes + default restore hot (epoch unchanged, live stream kept)")
}

func flvxSOCKSPort(t *testing.T, port int, target string, targetPort int) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err = io.ReadFull(c, reply); err != nil {
		t.Fatal(err)
	}
	c.Write(append([]byte{5, 1, 0, 1}, append(net.ParseIP(target).To4(), byte(targetPort>>8), byte(targetPort))...))
	header := make([]byte, 10)
	io.ReadFull(c, header)
	return c
}
