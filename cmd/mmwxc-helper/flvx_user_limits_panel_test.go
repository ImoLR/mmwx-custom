package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// TestFLVXUserLimitsPanelE2E is the local end-to-end run against a real FLVX
// panel built from feature/3.0.27-limits-speed (MMWXC_UL_PANEL_BIN), this
// branch's Custom (MMWXC_UL_CUSTOM_LAB_BIN = the root package's test binary,
// TestFLVXUserLimitsLabServer, on MMWXC_TEST_POSTGRES_DSN) and the Helper's
// own upload / reconcile code driving the real Core in the lab namespaces
// (MMWXC_TEST_FLVX_CORE_FIXED). FLVX creates users, landing nodes on the
// shared server and an FLVX node at the relay address; the lab only flips
// the Helper's advertised capabilities:
//
//	A  old Helper: no user_limits capability → FLVX sends nothing new, the
//	   share is not counted in alice's total (3 of 3 admitted);
//	B  new Helper: alice's total 2 holds across her two nodes, relays from
//	   the FLVX node address do not use it, bob is unaffected, conn_mode
//	   reaches the node limit, alice's 速率 20 Mbps covers both nodes;
//	C  capability withdrawn → FLVX stops the exchange and conn_mode.
//
// MMWXC_UL_PANEL_E2E_OUT receives the evidence JSON.
func TestFLVXUserLimitsPanelE2E(t *testing.T) {
	panelBin, customBin, bin, dsn := os.Getenv("MMWXC_UL_PANEL_BIN"), os.Getenv("MMWXC_UL_CUSTOM_LAB_BIN"), os.Getenv("MMWXC_TEST_FLVX_CORE_FIXED"), os.Getenv("MMWXC_TEST_POSTGRES_DSN")
	if panelBin == "" || customBin == "" || bin == "" || dsn == "" || os.Geteuid() != 0 {
		t.Skip("set MMWXC_UL_PANEL_BIN, MMWXC_UL_CUSTOM_LAB_BIN, MMWXC_TEST_FLVX_CORE_FIXED, MMWXC_TEST_POSTGRES_DSN and run as root")
	}
	raiseNoFile()
	base := os.Getenv("MMWXC_UL_E2E_DIR")
	if base == "" {
		base = t.TempDir()
	}
	os.MkdirAll(filepath.Join(base, "logs"), 0755)
	c, l, _ := ulLabNetns(t, base)
	evidence := map[string]any{"started_at": time.Now().UTC().Format(time.RFC3339), "core_sha256": mustHash(t, bin), "panel_sha256": mustHash(t, panelBin)}
	result := func(name string, got, want any) {
		t.Helper()
		evidence[name] = fmt.Sprintf("%v (want %v)", got, want)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}

	// Custom (this branch) on loopback, isolated PostgreSQL database.
	customPort, panelPort := ulFreePort(t), ulFreePort(t)
	helperToken := "helper-lab-" + ulRandHex(8)
	info, stop := filepath.Join(base, "custom-info.json"), filepath.Join(base, "custom-stop")
	os.Remove(stop)
	os.Remove(info)
	custom := exec.Command(customBin, "-test.run", "^TestFLVXUserLimitsLabServer$", "-test.timeout", "60m", "-test.v")
	custom.Env = append(os.Environ(), "MMWXC_UL_LAB_LISTEN=127.0.0.1:"+strconv.Itoa(customPort), "MMWXC_UL_LAB_INFO="+info, "MMWXC_UL_LAB_STOP="+stop, "MMWXC_UL_LAB_HELPER_TOKEN="+helperToken)
	customLog, _ := os.Create(filepath.Join(base, "logs", "custom.log"))
	custom.Stdout, custom.Stderr = customLog, customLog
	if err := custom.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(stop, nil, 0600); custom.Wait() })
	var flvxToken string
	ulWaitFor(t, 60*time.Second, "Custom lab server", func() bool {
		raw, err := os.ReadFile(info)
		var v map[string]string
		if err != nil || json.Unmarshal(raw, &v) != nil {
			return false
		}
		flvxToken = v["flvx_token"]
		return flvxToken != ""
	})
	customURL := "http://127.0.0.1:" + strconv.Itoa(customPort)
	labCounts := func() map[string]any {
		var v map[string]any
		resp, err := http.Get(customURL + "/lab/counts")
		if err == nil {
			json.NewDecoder(resp.Body).Decode(&v)
			resp.Body.Close()
		}
		return v
	}

	// The FLVX panel (SQLite) on loopback.
	secret := ulRandHex(16)
	panelDB := filepath.Join(base, "panel.db")
	os.Remove(panelDB)
	panel := exec.Command(panelBin)
	panel.Env = append(os.Environ(), "SERVER_ADDR=127.0.0.1:"+strconv.Itoa(panelPort), "DB_PATH="+panelDB, "JWT_SECRET="+secret, "LOG_DIR="+filepath.Join(base, "logs"), "FLUX_VERSION=3.0.27-fork.95")
	panelLog, _ := os.Create(filepath.Join(base, "logs", "panel.out"))
	panel.Stdout, panel.Stderr = panelLog, panelLog
	if err := panel.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { panel.Process.Kill(); panel.Wait() })
	api := &ulPanel{t: t, base: "http://127.0.0.1:" + strconv.Itoa(panelPort) + "/api/v1", secret: secret, db: panelDB}
	admin := api.token(1, "admin", 0)
	ulWaitFor(t, 60*time.Second, "FLVX panel", func() bool { _, err := api.try(admin, "/user/list", map[string]any{}); return err == nil })

	// The Helper: its own upload + reconcile, every 2 s, against Custom.
	m, core := flvxNetnsManager(t, l, bin, base, 39873, nil)
	m.state.Payload = flvx.Payload{}
	m.config = config{CustomAPIURL: customURL, ServerID: "uuid-7", Token: helperToken}
	m.rate = flvxRateLimiterIn(l, time.Now)
	oldCaps := []string{"helper.update", "flvx.share", "flvx.egress.v1", flvxProtocolsCapability, flvxGuardCapability}
	newCaps := append(append([]string{}, oldCaps...), flvx.UserLimitsCapability, flvx.UserRateCapability)
	var mu sync.Mutex
	caps := oldCaps
	var nodes []flvx.Node
	active := map[string]bool{}
	var tickErr error
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	t.Cleanup(func() { cancel(); <-loopDone })
	go func() {
		defer close(loopDone)
		for ctx.Err() == nil {
			mu.Lock()
			status := agentStatus{Helper: componentStatus{Installed: true, Version: "v0.7.15-lab", Ready: true}, Architecture: "amd64", Capabilities: append([]string{}, caps...)}
			mu.Unlock()
			resp, err := uploadDetailedMetrics(ctx, http.DefaultClient, m.config, detailedConnectionSnapshot{}, &managementReport{Status: status}, flvxUpload{Report: m.snapshotReport(), UserLimits: m.userLimitsUpload()})
			if err == nil {
				if e := m.submitUserLimits(resp.FLVXUserLimits); e != nil {
					err = e
				} else if resp.FLVX != nil {
					err = m.accept(*resp.FLVX)
				}
			}
			if err == nil && m.state.Payload.Version > 0 {
				if err = m.reconcile(ctx); err == nil {
					m.reconcileRates(ctx)
				}
			}
			mu.Lock()
			tickErr = err
			nodes = append([]flvx.Node{}, m.state.Payload.Desired.Nodes...)
			active = map[string]bool{}
			for _, s := range m.state.Report.Nodes {
				active[s.ID] = s.State == "active"
			}
			mu.Unlock()
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}()
	snapshotNodes := func() ([]flvx.Node, map[string]bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]flvx.Node{}, nodes...), active, tickErr
	}

	// FLVX: integration, an FLVX node at the relay address, users, grants,
	// landing nodes on the shared server.
	api.call(admin, "/landing/integration/save", map[string]any{"baseUrl": customURL, "token": flvxToken})
	var shared int64
	ulWaitFor(t, 60*time.Second, "shared server in FLVX", func() bool {
		api.try(admin, "/landing/integration/sync", map[string]any{})
		raw, err := api.try(admin, "/landing/server/list", map[string]any{})
		var servers []struct {
			ID             int64  `json:"id"`
			Source         string `json:"source"`
			RemoteServerID int64  `json:"remoteServerId"`
		}
		json.Unmarshal(raw, &servers)
		for _, s := range servers {
			if s.Source == "shared" && s.RemoteServerID == 7 {
				shared = s.ID
			}
		}
		return err == nil && shared > 0
	})
	api.call(admin, "/node/create", map[string]any{"name": "relay-node", "serverIp": ulRelay, "port": "20000-20100"})
	api.call(admin, "/user/create", map[string]any{"user": "alice", "pwd": "alice-lab-" + ulRandHex(4), "maxConnections": 2, "maxClientIps": 0, "num": 20, "flow": 1000})
	api.call(admin, "/user/create", map[string]any{"user": "bob", "pwd": "bob-lab-" + ulRandHex(4), "maxConnections": 0, "maxClientIps": 0, "num": 20, "flow": 1000})
	ids := map[string]int64{}
	var users []struct {
		ID   int64  `json:"id"`
		User string `json:"user"`
	}
	json.Unmarshal(api.call(admin, "/user/list", map[string]any{}), &users)
	for _, u := range users {
		ids[u.User] = u.ID
	}
	now := time.Now().UnixMilli()
	for _, name := range []string{"alice", "bob"} {
		api.sql(fmt.Sprintf("INSERT INTO landing_server_user(landing_server_id,user_id,created_time) VALUES (%d,%d,%d)", shared, ids[name], now))
	}
	alice, bob := api.token(ids["alice"], "alice", 1), api.token(ids["bob"], "bob", 1)
	for _, n := range []struct {
		tok, name string
	}{{alice, "A1"}, {alice, "A2"}, {bob, "B1"}} {
		api.call(n.tok, "/landing/node/create", map[string]any{"landingServerId": shared, "name": n.name, "protocol": "ss2022"})
	}
	evidence["flvx_ids"] = map[string]any{"shared_landing_server": shared, "alice": ids["alice"], "bob": ids["bob"]}

	// Wait for the three nodes to run in the FLVX Core.
	var labNodes []flvx.Node
	ulWaitFor(t, 90*time.Second, "FLVX nodes active in the Core", func() bool {
		ns, act, _ := snapshotNodes()
		if len(ns) != 3 {
			return false
		}
		for _, n := range ns {
			if !act[n.ID] {
				return false
			}
		}
		labNodes = ns
		return true
	})
	// Name them like the namespace test: alice's two by id, then bob's.
	sort.Slice(labNodes, func(i, j int) bool {
		if labNodes[i].Owner.ID != labNodes[j].Owner.ID {
			return labNodes[i].Owner.ID == flvx.OwnerID(strconv.FormatInt(ids["alice"], 10))
		}
		a, _ := strconv.Atoi(labNodes[i].ID)
		b, _ := strconv.Atoi(labNodes[j].ID)
		return a < b
	})
	named := make([]flvx.Node, len(labNodes))
	for i, n := range labNodes {
		named[i] = n
		named[i].ID = []string{"A1", "A2", "B1"}[i]
	}
	if string(named[0].Owner.ID) != strconv.FormatInt(ids["alice"], 10) || string(named[2].Owner.ID) != strconv.FormatInt(ids["bob"], 10) {
		t.Fatalf("node owners %+v", named)
	}
	dr, ports := ulLabClient(t, base, c, bin, named)
	conns := func(node, src string, count int) []int {
		out := make([]int, count)
		for i := range out {
			out[i] = ports[node+"@"+src]
		}
		return out
	}
	drain := func() {
		t.Helper()
		dr.closeAll()
		flvxWait30(t, func() bool {
			s, err := core2Snapshot(m)
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
	customShares := func() map[string]any {
		req, _ := http.NewRequest("GET", customURL+"/api/custom/flvx/v1/shares", nil)
		req.Header.Set("Authorization", "Bearer "+flvxToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v struct {
			Shares []map[string]any `json:"shares"`
		}
		json.NewDecoder(resp.Body).Decode(&v)
		for _, s := range v.Shares {
			if s["server_id"] == float64(7) {
				return s
			}
		}
		t.Fatal("share 7 missing")
		return nil
	}
	// alice's node A1 gets a node limit: FLVX sends conn_mode only to a
	// share with the capability.
	api.sql(fmt.Sprintf("UPDATE landing_node SET max_connections=5 WHERE id=%s", labNodes[0].ID))

	// A. Old Helper: nothing new, the share is outside alice's total.
	time.Sleep(12 * time.Second)
	counts := labCounts()
	_, advertised := customShares()["user_limits"]
	result("A_capability_advertised", advertised, false)
	result("A_user_limits_calls", counts["user_limits_calls"], float64(0))
	result("A_conn_mode_nodes", counts["conn_mode_nodes"], float64(0))
	result("A_alice_3_direct_over_two_nodes", dr.open(ports["A1@203.0.113.50"], ports["A2@203.0.113.51"], ports["A1@203.0.113.52"]), 3)
	drain()

	// B. New Helper with flvx.user_limits.v1 + flvx.user_rate.v1.
	mu.Lock()
	caps = newCaps
	mu.Unlock()
	time.Sleep(5 * time.Second)
	if got, _ := json.Marshal(customShares()["user_limits"]); string(got) != `{"versions":[1,2]}` {
		t.Errorf("capability not advertised: %s", got)
	}
	api.call(admin, "/landing/integration/sync", map[string]any{})
	ulWaitFor(t, 90*time.Second, "budgets held by the Helper", func() bool {
		v := labCounts()
		return v["user_limits_calls"].(float64) > 0 && m.heldBudgetSequence() > 0 && v["conn_mode_nodes"].(float64) == 1
	})
	evidence["B_budgets_from_flvx"] = labCounts()["budgets"]
	time.Sleep(4 * time.Second)
	result("B_conn_mode_nodes", labCounts()["conn_mode_nodes"], float64(1))
	// Relays from the FLVX node address arrive one per Helper pass (each pass
	// adds the live relays back), then direct connections: alice's total 2
	// holds across her two nodes and the relays do not use it.
	relays := 0
	for i, port := range []int{ports["A1@"+ulRelay], ports["A2@"+ulRelay], ports["A1@"+ulRelay]} {
		relays += dr.open(port)
		if i < 2 {
			time.Sleep(4500 * time.Millisecond)
		}
	}
	result("B_relays_admitted", relays, 3)
	time.Sleep(4500 * time.Millisecond)
	result("B_alice_direct_total_2_over_two_nodes", dr.open(ports["A1@203.0.113.50"], ports["A2@203.0.113.51"], ports["A1@203.0.113.52"], ports["A2@203.0.113.50"]), 2)
	result("B_bob_unaffected", dr.open(conns("B1", "203.0.113.50", 3)...), 3)
	time.Sleep(12 * time.Second)
	usage := labCounts()["helper_usage"]
	evidence["B_usage_reported_to_flvx"] = usage
	raw, _ := json.Marshal(usage)
	var u flvx.UserLimitsUsage
	json.Unmarshal(raw, &u)
	aliceUsage := flvx.UserLimitUsage{}
	for _, x := range u.Users {
		if x.UserID == strconv.FormatInt(ids["alice"], 10) {
			aliceUsage = x
		}
	}
	result("B_alice_usage_inbound", aliceUsage.InboundConnections, 2)
	result("B_alice_usage_relay", aliceUsage.RelayConnections, 3)
	result("B_alice_usage_ips", strings.Join(aliceUsage.OnlineIPs, ","), "203.0.113.50,203.0.113.51")
	drain()
	// alice's 速率 (user default 20 Mbps) covers both of her nodes; bob runs free.
	api.sql(fmt.Sprintf("UPDATE user SET server_rate_mbps=20 WHERE id=%d", ids["alice"]))
	ulWaitFor(t, 60*time.Second, "rate in force", func() bool {
		up := m.userLimitsUpload()
		for _, x := range up.Users {
			if x.UserID == strconv.FormatInt(ids["alice"], 10) && x.RateApplied {
				return true
			}
		}
		return false
	})
	down := dr.rate("download", 12, 3, ports["A1-down"], ports["A2-down"])
	other := dr.rate("download", 5, 1, ports["B1-down"])
	evidence["B_rate_mbps"] = map[string]any{"alice_two_nodes": down, "bob": other}
	if sum := down[0] + down[1]; sum < 17 || sum > 23 || other[0] < 100 {
		t.Errorf("alice 20 Mbps over two nodes: %v (sum %.1f), bob %v", down, sum, other)
	}

	// C. Capability withdrawn: FLVX stops the exchange and conn_mode.
	mu.Lock()
	caps = oldCaps
	mu.Unlock()
	time.Sleep(5 * time.Second)
	api.call(admin, "/landing/integration/sync", map[string]any{})
	ulWaitFor(t, 90*time.Second, "conn_mode withdrawn", func() bool { return labCounts()["conn_mode_nodes"].(float64) == 0 })
	before := labCounts()["user_limits_calls"].(float64)
	time.Sleep(25 * time.Second)
	after := labCounts()
	result("C_user_limits_calls_after_withdrawal", after["user_limits_calls"].(float64)-before, float64(0))
	_, advertised = customShares()["user_limits"]
	result("C_capability_advertised", advertised, false)
	evidence["custom_lab_counts_final"] = after
	if rss, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", core.Process.Pid)); err == nil {
		for _, line := range strings.Split(string(rss), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				evidence["core_rss"] = strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
			}
		}
	}
	out, _ := json.MarshalIndent(evidence, "", "  ")
	if path := os.Getenv("MMWXC_UL_PANEL_E2E_OUT"); path != "" {
		os.WriteFile(path, out, 0644)
	}
	t.Logf("evidence:\n%s", out)
}

func core2Snapshot(m *flvxManager) (coreSnapshotResponse, error) {
	return m.core.snapshot(context.Background())
}

type ulPanel struct {
	t      *testing.T
	base   string
	secret string
	db     string
}

// token is an FLVX panel JWT (HS256, the panel's own claims).
func (p *ulPanel) token(uid int64, user string, role int) string {
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	now := time.Now().Unix()
	head := enc(map[string]string{"alg": "HS256", "typ": "JWT"}) + "." + enc(map[string]any{"sub": strconv.FormatInt(uid, 10), "iat": now, "exp": now + 86400, "user": user, "name": user, "role_id": role})
	mac := hmac.New(sha256.New, []byte(p.secret))
	mac.Write([]byte(head))
	return head + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *ulPanel) try(token, path string, body any) (json.RawMessage, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", p.base+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var v struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("%s: HTTP %d %s", path, resp.StatusCode, data)
	}
	if v.Code != 0 {
		return nil, fmt.Errorf("%s: %d %s", path, v.Code, v.Msg)
	}
	return v.Data, nil
}

func (p *ulPanel) call(token, path string, body any) json.RawMessage {
	p.t.Helper()
	data, err := p.try(token, path, body)
	if err != nil {
		p.t.Fatal(err)
	}
	return data
}

// sql changes the panel's SQLite database directly (lab data only).
func (p *ulPanel) sql(statement string) {
	p.t.Helper()
	script := "import sqlite3,sys\nc=sqlite3.connect(sys.argv[1],timeout=10)\nc.execute(sys.argv[2])\nc.commit()\n"
	if out, err := exec.Command("python3", "-c", script, p.db, statement).CombinedOutput(); err != nil {
		p.t.Fatalf("%s: %v %s", statement, err, out)
	}
}

func ulFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func ulRandHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func ulWaitFor(t *testing.T, limit time.Duration, what string, fn func() bool) {
	t.Helper()
	until := time.Now().Add(limit)
	for time.Now().Before(until) {
		if fn() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}
