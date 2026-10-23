package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func flvxTestApp(t *testing.T) *app {
	db := auditUserDeleteDB(t, "flvx")
	for _, s := range []string{flvxSchema, `ALTER TABLE remote_servers ADD COLUMN ip_address text,ADD COLUMN domain text`,
		`CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamp)`,
		`INSERT INTO remote_servers VALUES(7,'shared','203.0.113.7',''),(8,'native','203.0.113.8','')`,
		`INSERT INTO server_xray_config_snapshots VALUES(1,7,'{"inbounds":[{"port":30002}]}','current',CURRENT_TIMESTAMP),(2,8,'{"inbounds":[]}','current',CURRENT_TIMESTAMP)`,
	} {
		auditDeleteExec(t, db, s)
	}
	a := &app{apiToken: "operator-token", adminStore: &postgresAdminSessionStore{db: db}}
	state, err := openHelperState(t.TempDir()+"/helper-state.json", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a.helperState = state
	flvxTestHelper(a, 7, "v0.7.5", true)
	flvxTestHelper(a, 8, "v0.7.5", true)
	return a
}

func flvxTestHelper(a *app, id int64, version string, flvxShare bool) {
	key := flvxID(id)
	status := agentStatus{Helper: componentStatus{Installed: true, Version: version}, Architecture: "amd64", Capabilities: []string{"helper.update"}}
	if flvxShare {
		status.Capabilities = append(status.Capabilities, "flvx.share")
	}
	a.helperState.mu.Lock()
	a.helperState.data.Servers[key] = helperServerIdentity{OfficialServerID: key, HelperTokenHash: strings.Repeat("a", 64)}
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
}

func flvxTestRequest(a *app, method, path, token string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	if strings.Contains(path, "/admin") {
		a.flvxAdminHandler(w, r)
	} else {
		a.flvxV1Handler(w, r)
	}
	return w
}

func flvxTestDesired() flvx.Desired {
	enabled := true
	key := base64.StdEncoding.EncodeToString(make([]byte, 16))
	return flvx.Desired{Revision: 1, Nodes: []flvx.Node{{ID: "11", Owner: flvx.Owner{ID: "2", Name: "Alice"}, Name: "SS", Protocol: "ss2022", Port: 30001, Enabled: &enabled, SS2022: &flvx.SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: key, UserKey: key}}}}
}

func TestFLVXPersistedReportFreshness(t *testing.T) {
	for _, check := range []struct {
		age  time.Duration
		want string
	}{{35 * time.Second, "running"}, {65 * time.Second, "error"}} {
		s := flvx.Status{Core: flvx.Core{State: "running"}, CollectedAt: time.Now().Add(-check.age).UnixMilli()}
		if flvxPublicStatus(s, true)["core"].(map[string]any)["state"] != check.want {
			t.Fatal("freshness does not allow the persisted report cadence")
		}
		if flvxPublicStatus(s, false)["core"].(map[string]any)["state"] != "helper_outdated" {
			t.Fatal("a Helper without the FLVX runtime must not look like a plain stopped/running Core")
		}
	}
}

func TestFLVXLinkHosts(t *testing.T) {
	for _, check := range []struct {
		name string
		row  map[string]any
		want flvx.Hosts
	}{
		// Server 5: DDNS name lives in pull_address, domain is empty.
		{"ddns", map[string]any{"ip_address": "114.41.205.96", "ip_address_v6": "2001:db8::5", "domain": "", "pull_address": "boilhinet2.imgamer.top"}, flvx.Hosts{IPv4: "114.41.205.96", IPv6: "2001:db8::5", Domain: "boilhinet2.imgamer.top"}},
		// Servers 9/17: pull_address is the public entry IP, ip_address the egress IP.
		{"nat", map[string]any{"ip_address": "143.203.126.34", "pull_address": "120.253.251.139"}, flvx.Hosts{IPv4: "120.253.251.139"}},
		// Server 15: an IP typed into 服务器域名 still wins over pull_address.
		{"domain-ip", map[string]any{"ip_address": "87.86.22.15", "ip_address_v6": "2001:db8::15", "domain": "120.253.251.132", "pull_address": "nat.example"}, flvx.Hosts{IPv4: "120.253.251.132", IPv6: "2001:db8::15"}},
		{"domain-first", map[string]any{"ip_address": "203.0.113.7", "domain": "node.example", "pull_address": "ddns.example"}, flvx.Hosts{IPv4: "203.0.113.7", Domain: "node.example"}},
		{"plain", map[string]any{"ip_address": "143.20.196.111", "ip_address_v6": "2001:db8::db", "pull_address": "143.20.196.111", "domain": nil}, flvx.Hosts{IPv4: "143.20.196.111", IPv6: "2001:db8::db"}},
		{"v6-entry", map[string]any{"ip_address": "203.0.113.9", "ip_address_v6": "2001:db8::1", "pull_address_v6": "2001:db8::2", "domain_v6": "v6.example"}, flvx.Hosts{IPv4: "203.0.113.9", IPv6: "2001:db8::2"}},
	} {
		if got := flvxLinkHosts(check.row); got != check.want {
			t.Errorf("%s: got %+v want %+v", check.name, got, check.want)
		}
	}
}

func TestFLVXServerHostsPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	auditDeleteExec(t, a.flvxDB(), `ALTER TABLE remote_servers ADD COLUMN pull_address text, ADD COLUMN ip_address_v6 text`)
	auditDeleteExec(t, a.flvxDB(), `UPDATE remote_servers SET pull_address='ddns.example', ip_address_v6='2001:db8::7' WHERE id=7`)
	auditDeleteExec(t, a.flvxDB(), `UPDATE remote_servers SET pull_address='198.51.100.8' WHERE id=8`)
	servers, err := flvxServers(ctx, a.flvxDB())
	if err != nil || len(servers) != 2 {
		t.Fatal(servers, err)
	}
	if servers[0].Hosts != (flvx.Hosts{IPv4: "203.0.113.7", IPv6: "2001:db8::7", Domain: "ddns.example"}) || servers[1].Hosts != (flvx.Hosts{IPv4: "198.51.100.8"}) {
		t.Fatalf("hosts do not follow official node links: %+v %+v", servers[0].Hosts, servers[1].Hosts)
	}
	for _, check := range []struct {
		s     flvxServer
		hosts []string
	}{{servers[0], []string{"203.0.113.7", "2001:db8::7", "ddns.example"}}, {servers[1], []string{"203.0.113.8", "198.51.100.8"}}} {
		for _, h := range check.hosts {
			found := false
			for _, v := range check.s.Addresses {
				found = found || v == h
			}
			if !found {
				t.Fatalf("dual-install detection lost %s: %v", h, check.s.Addresses)
			}
		}
	}
}

func TestFLVXHelperCapabilityPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	auditDeleteExec(t, a.flvxDB(), `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	flvxTestHelper(a, 7, "v0.7.4", false)
	w := flvxTestRequest(a, "PUT", "/api/custom/flvx/admin/shares/7", "operator-token", map[string]any{"port_min": 30000, "port_max": 30100, "reset_day": 1, "memory_mb": 256, "cpu_percent": 50, "status": "active"})
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"helper_outdated"`) {
		t.Fatalf("new share on a Helper without FLVX runtime accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := readFLVXShare(ctx, a.flvxDB(), 7, false); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("refused share was stored", err)
	}
	var list struct {
		Shares []struct {
			Core   map[string]string `json:"core"`
			Helper *flvxHelper       `json:"helper"`
			Nodes  []struct {
				ID     string          `json:"id"`
				Status flvx.NodeStatus `json:"status"`
			} `json:"nodes"`
		} `json:"shares"`
		Servers []flvxServer `json:"servers"`
	}
	read := func(path, token string) {
		t.Helper()
		w := flvxTestRequest(a, "GET", path, token, nil)
		list.Shares, list.Servers = nil, nil
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
			t.Fatalf("list %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	read("/api/custom/flvx/admin", "operator-token")
	if len(list.Servers) != 2 || list.Servers[0].Helper.Ready || list.Servers[0].Helper.Version != "v0.7.4" || !list.Servers[0].Helper.Installed || !list.Servers[1].Helper.Ready {
		t.Fatalf("share dialog cannot see Helper capability: %+v", list.Servers)
	}

	// Existing share whose Helper is (or becomes) too old: shown, not silently stopped.
	flvxTestHelper(a, 7, "v0.7.5", true)
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.putFLVXDesired(ctx, 7, flvxTestDesired()); err != nil {
		t.Fatal(err)
	}
	flvxTestHelper(a, 7, "v0.7.4", false)
	if err := a.saveFLVXShare(ctx, 7, c, "paused", nil); err != nil {
		t.Fatal("editing an existing share must only warn", err)
	}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal("resuming an existing share must only warn", err)
	}
	before := flvxTestXmin(t, a)
	read("/api/custom/flvx/admin", "operator-token")
	if len(list.Shares) != 1 || list.Shares[0].Core["state"] != "helper_outdated" || list.Shares[0].Helper == nil || list.Shares[0].Helper.Ready || len(list.Shares[0].Nodes) != 1 || list.Shares[0].Nodes[0].Status.State != "pending" {
		t.Fatalf("outdated Helper share: %+v", list.Shares)
	}
	read("/api/custom/flvx/v1/shares", "flvx_fixture")
	if len(list.Shares) != 1 || list.Shares[0].Core["state"] != "helper_outdated" || list.Shares[0].Helper != nil {
		t.Fatalf("FLVX caps must carry helper_outdated and no Helper details: %+v", list.Shares)
	}
	w = flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", "flvx_fixture", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"helper_outdated"`) {
		t.Fatalf("FLVX status: %d %s", w.Code, w.Body.String())
	}
	if flvxTestXmin(t, a) != before {
		t.Fatal("reading share status wrote the share row")
	}

	// Upgraded Helper: unreported nodes stay pending; only a report marks them.
	flvxTestHelper(a, 7, "v0.7.5", true)
	read("/api/custom/flvx/admin", "operator-token")
	if list.Shares[0].Core["state"] != "stopped" || list.Shares[0].Nodes[0].Status.State != "pending" {
		t.Fatalf("capable Helper before first report: %+v", list.Shares[0])
	}
	for i, state := range []string{"active", "removed"} {
		r := flvx.Report{Generation: "g", Sequence: int64(i + 1), Period: flvx.Period(time.Now(), 1), Status: flvx.Status{AppliedRevision: 1, Core: flvx.Core{State: "running"}, Nodes: []flvx.NodeStatus{{ID: "11", State: state}}, CollectedAt: time.Now().UnixMilli()}}
		if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", false, false, false, &r); err != nil {
			t.Fatal(err)
		}
		read("/api/custom/flvx/admin", "operator-token")
		if list.Shares[0].Core["state"] != "running" || list.Shares[0].Nodes[0].Status.State != state {
			t.Fatalf("reported %s: %+v", state, list.Shares[0])
		}
	}
}

func flvxTestXmin(t *testing.T, a *app) string {
	t.Helper()
	var xmin string
	if err := a.flvxDB().QueryRow(`SELECT xmin::text FROM mmwxc_flvx_shares WHERE server_id=7`).Scan(&xmin); err != nil {
		t.Fatal(err)
	}
	return xmin
}

func TestFLVXAPIContractPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/token", "operator-token", nil)
	var tokenResult struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokenResult)
	token := tokenResult.Token
	if w.Code != 200 || token == "" {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	var hash string
	a.flvxDB().QueryRow(`SELECT token_hash FROM mmwxc_flvx_integration`).Scan(&hash)
	if hash == token || hash != flvxTokenHash(token) {
		t.Fatal("token not hashed")
	}
	for _, wrong := range []string{"operator-token", "helper-token", "", "flvx_wrong"} {
		if w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares", wrong, nil); w.Code != 401 {
			t.Fatalf("wrong token accepted: %d", w.Code)
		}
	}
	auditDeleteExec(t, a.flvxDB(), `CREATE TABLE sessions(token text,username text,expires_at timestamp); INSERT INTO users(username,role) VALUES('operator','admin'),('ordinary','user'); INSERT INTO sessions VALUES('session-admin','operator',CURRENT_TIMESTAMP+INTERVAL '1 day'),('session-user','ordinary',CURRENT_TIMESTAMP+INTERVAL '1 day')`)
	for _, check := range []struct {
		session, path string
		want          int
	}{{"session-admin", "/api/custom/flvx/admin", 200}, {"session-user", "/api/custom/flvx/admin", 401}, {"session-admin", "/api/custom/flvx/v1/shares", 401}} {
		r := httptest.NewRequest("GET", check.path, nil)
		r.Header.Set("MM-Authorization", check.session)
		response := httptest.NewRecorder()
		if strings.Contains(check.path, "/admin") {
			a.flvxAdminHandler(response, r)
		} else {
			a.flvxV1Handler(response, r)
		}
		if response.Code != check.want {
			t.Fatalf("session authorization: %s %s = %d", check.session, check.path, response.Code)
		}
	}
	if w := flvxTestRequest(a, "GET", "/api/custom/flvx/admin", token, nil); w.Code != 401 {
		t.Fatal("integration token authorized as operator")
	}
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50, TrafficLimitBytes: 100, MaxConnections: 10}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	d := flvxTestDesired()
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"accepted_revision":1}` {
		t.Fatalf("desired %d %s", w.Code, w.Body.String())
	}
	if err := a.putFLVXDesired(ctx, 7, d); err != nil {
		t.Fatal("idempotent retry", err)
	}
	d.Nodes[0].Port = 30002
	d.Revision = 2
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"field":"port"`) {
		t.Fatalf("collision %d %s", w.Code, w.Body.String())
	}
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, map[string]any{"revision": 2, "nodes": []any{}, "config": map[string]any{"inbounds": []any{}}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"errors":[`) {
		t.Fatal("raw configuration injection must return the contracted validation envelope", w.Code, w.Body.String())
	}
	d = flvxTestDesired()
	d.Nodes[0].Name = "different"
	if err := a.putFLVXDesired(ctx, 7, d); err == nil {
		t.Fatal("same revision changed")
	}
	w = flvxTestRequest(a, "GET", "/api/custom/flvx/admin", "operator-token", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "server_key") || strings.Contains(w.Body.String(), token) {
		t.Fatal("admin credentials leaked")
	}
	for _, state := range []string{"paused", "active", "revoked"} {
		if err := a.saveFLVXShare(ctx, 7, c, state, nil); err != nil {
			t.Fatal(err)
		}
		err := a.putFLVXDesired(ctx, 7, flvxTestDesired())
		if (state == "active") != (err == nil) {
			t.Fatalf("state %s: %v", state, err)
		}
		if state == "paused" {
			narrow := c
			narrow.PortMax = 30000
			if err := a.saveFLVXShare(ctx, 7, narrow, "paused", nil); err == nil {
				t.Fatal("paused port edit would strand desired state and prevent resume")
			}
		}
	}
	w = flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", token, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"nodes":[]`) {
		t.Fatalf("status %s", w.Body.String())
	}
	w = flvxTestRequest(a, "DELETE", "/api/custom/flvx/admin/token", "operator-token", nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares", token, nil).Code != 401 {
		t.Fatal("revoked token works")
	}
}

func TestFLVXHelperChannelPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.putFLVXDesired(ctx, 7, flvxTestDesired()); err != nil {
		t.Fatal(err)
	}
	asset := releaseArtifact{URL: "https://github.com/ImoLR/mmwx-custom/releases/download/v1.4.59/mmwxc-core-linux-amd64", SHA256: strings.Repeat("a", 64)}
	a.releaseCache = &releaseCache{info: cachedReleaseInfo{CoreVersion: "8f5b614", CoreArtifacts: map[string]releaseArtifact{"amd64": asset}}}
	r := flvx.Report{Generation: "helper-one", Sequence: 1, Period: flvx.Period(time.Now(), 1), Status: flvx.Status{AppliedRevision: 1, Core: flvx.Core{State: "running", Epoch: "core-one"}, Nodes: []flvx.NodeStatus{{ID: "11", State: "active", UpBytes: 100, DownBytes: 20, Connections: 2}}, TrafficUsedBytes: 120, CollectedAt: time.Now().UnixMilli()}}
	e, err := a.flvxHelperDesired(ctx, 7, "helper-custom-uuid", "helper-secret", "amd64", true, false, false, &r)
	if err != nil || e == nil {
		t.Fatal("helper desired", err)
	}
	if !e.Verify("helper-secret", "helper-custom-uuid", time.Now()) || e.Verify("operator-token", "helper-custom-uuid", time.Now()) || e.Verify("helper-secret", "8", time.Now()) {
		t.Fatal("Helper desired not scoped and signed")
	}
	if e.Payload.AckSequence != 1 || e.Payload.AckGeneration != r.Generation || e.Payload.TrafficUsedBytes != 120 || e.Payload.Artifact.URL != asset.URL || e.Payload.Artifact.SHA256 != asset.SHA256 || len(e.Payload.OfficialPorts) != 1 || e.Payload.OfficialPorts[0] != 30002 {
		t.Fatalf("bad Helper envelope: %+v", e.Payload)
	}
	version := func() string {
		t.Helper()
		var xmin string
		if err := a.flvxDB().QueryRow(`SELECT xmin::text FROM mmwxc_flvx_shares WHERE server_id=7`).Scan(&xmin); err != nil {
			t.Fatal(err)
		}
		return xmin
	}
	previous := version()
	// A broken unrelated snapshot must not be read for this Helper's report.
	auditDeleteExec(t, a.flvxDB(), `UPDATE server_xray_config_snapshots SET config_json='invalid fixture' WHERE server_id=8`)
	for i := 0; i < 10; i++ {
		if _, err = a.flvxHelperDesired(ctx, 7, "helper-custom-uuid", "helper-secret", "amd64", true, false, false, &r); err != nil {
			t.Fatal(err)
		}
		if _, err = a.flvxHelperDesired(ctx, 7, "helper-custom-uuid", "helper-secret", "amd64", true, false, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	if version() != previous {
		t.Fatal("duplicate/absent Helper report rewrote an unchanged share")
	}
	// An acknowledged removal can disappear from Helper uploads; PostgreSQL
	// retains its final monotonic tombstone for FLVX's slower status polling.
	r.Sequence++
	r.Nodes[0].State = "removed"
	r.Nodes[0].Connections = 0
	r.Nodes[0].UpBytes = 110
	r.TrafficUsedBytes = 130
	if _, err = a.flvxHelperDesired(ctx, 7, "helper-custom-uuid", "helper-secret", "amd64", true, false, false, &r); err != nil {
		t.Fatal(err)
	}
	if version() == previous {
		t.Fatal("changed Helper report was not written")
	}
	r.Sequence++
	r.Nodes = []flvx.NodeStatus{}
	if _, err = a.flvxHelperDesired(ctx, 7, "helper-custom-uuid", "helper-secret", "amd64", true, false, false, &r); err != nil {
		t.Fatal(err)
	}
	s, err := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if err != nil || len(s.Status.Nodes) != 1 || s.Status.Nodes[0].State != "removed" || s.Status.Nodes[0].UpBytes != 110 || s.Status.TrafficUsedBytes != 130 {
		t.Fatalf("final counters not retained: %+v %v", s, err)
	}
	auditDeleteExec(t, a.flvxDB(), `DELETE FROM server_xray_config_snapshots WHERE server_id=7`)
	e, err = a.flvxHelperDesired(ctx, 7, "helper-custom-uuid", "helper-secret", "amd64", true, false, false, nil)
	if err != nil || e.Payload.Status != "paused" {
		t.Fatal("missing official inventory must prevent admission", err)
	}
}

func TestFLVXCountersPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.putFLVXDesired(ctx, 7, flvxTestDesired()); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		generation    string
		seq, up, want int64
	}{{"a", 1, 100, 100}, {"a", 1, 100, 100}, {"a", 2, 150, 150}, {"b", 1, 10, 160}, {"a", 3, 200, 160}, {"b", 2, 1, 161}} {
		err := flvxTransaction(ctx, a.flvxDB(), func(tx *sql.Tx) error {
			s, e := readFLVXShare(ctx, tx, 7, true)
			if e != nil {
				return e
			}
			r := flvx.Report{Status: flvx.Status{AppliedRevision: 1, Core: flvx.Core{State: "running", Epoch: v.generation}, Nodes: []flvx.NodeStatus{{ID: "11", State: "active", UpBytes: v.up}}, CollectedAt: time.Now().UnixMilli()}, Generation: v.generation, Sequence: v.seq, Period: flvx.Period(time.Now(), 1)}
			if e = mergeFLVXReport(ctx, tx, 7, &s, r, time.Now()); e != nil {
				return e
			}
			return writeFLVXShare(ctx, tx, 7, s)
		})
		if err != nil {
			t.Fatal(err)
		}
		s, err := readFLVXShare(ctx, a.flvxDB(), 7, false)
		if err != nil || len(s.Status.Nodes) != 1 || s.Status.Nodes[0].UpBytes != v.want || s.Status.TrafficUsedBytes != v.want {
			t.Fatalf("merge %v: %+v %v", v, s, err)
		}
	}
}

func TestFLVXRetiredGenerationsPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		r := flvx.Report{Generation: fmt.Sprintf("reset-%02d", i), Sequence: 1, Period: flvx.Period(time.Now(), 1)}
		if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, false, false, &r); err != nil {
			t.Fatal(err)
		}
	}
	s, err := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if err != nil || len(s.Retired) != 32 || s.Retired[0] != "reset-07" || s.Retired[31] != "reset-38" || s.Generation != "reset-39" {
		t.Fatalf("retired generations not bounded/newest: %+v %v", s, err)
	}
	r := flvx.Report{Generation: "reset-38", Sequence: 2}
	if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, false, false, &r); err != nil {
		t.Fatal(err)
	}
	s, err = readFLVXShare(ctx, a.flvxDB(), 7, false)
	if err != nil || s.Generation != "reset-39" || s.Sequence != 1 {
		t.Fatal("recent retired report was not ignored", err)
	}
}

func TestFLVXDualInstallPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	h := flvx.LandingHosts{Native: []string{"203.0.113.8"}, Nodes: []string{"203.0.113.7"}}
	if err := a.putFLVXHosts(ctx, h); err != nil {
		t.Fatal(err)
	}
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal("dual install must warn only", err)
	}
	if err := a.saveFLVXShare(ctx, 8, c, "active", nil); err == nil {
		t.Fatal("native landing accepted")
	}
	resolver := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("fixture DNS unavailable")
	}}
	t.Cleanup(func() { net.DefaultResolver = resolver })
	auditDeleteExec(t, a.flvxDB(), `UPDATE remote_servers SET domain='shared.invalid' WHERE id=7`)
	auditDeleteExec(t, a.flvxDB(), `UPDATE mmwxc_flvx_integration SET token_hash=$1`, flvxTokenHash("flvx_fixture"))
	before, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	w := flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/landing-hosts", "flvx_fixture", h)
	if w.Code != 200 {
		t.Fatalf("valid host report rejected: %d %s", w.Code, w.Body.String())
	}
	after, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if after.State != "active" || after.Version != before.Version {
		t.Fatal("server-domain DNS failure changed an active share")
	}
	w = flvxTestRequest(a, "GET", "/api/custom/flvx/admin", "operator-token", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"detection_error":true`) {
		t.Fatal("DNS warning missing from admin list", w.Body.String())
	}
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", "flvx_fixture", flvxTestDesired())
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"host_resolution"`) {
		t.Fatal("DNS failure no longer rejects desired pushes", w.Body.String())
	}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err == nil {
		t.Fatal("DNS failure no longer rejects activation")
	}
	auditDeleteExec(t, a.flvxDB(), `UPDATE remote_servers SET domain='' WHERE id=7`)
	h.Native = append(h.Native, "203.0.113.7")
	if err := a.putFLVXHosts(ctx, h); err != nil {
		t.Fatal(err)
	}
	s, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if s.State != "paused" {
		t.Fatal("late native conflict not paused")
	}
	server := flvxServer{Addresses: []string{"shared.example"}}
	detectFLVXDual(ctx, &server, flvx.LandingHosts{Native: []string{"203.0.113.7"}}, func(context.Context, string) ([]string, error) { return []string{"203.0.113.7"}, nil })
	if !server.NativeLanding {
		t.Fatal("hostname resolution not compared")
	}
}
