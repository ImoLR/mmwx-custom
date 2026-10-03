package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPackageTrafficGroupValidation(t *testing.T) {
	nodes := map[int64]trafficGroupNode{1: {ID: 1}, 2: {ID: 2}, 3: {ID: 3}}
	pkg := trafficGroupPackage{ID: 1, Limit: 10 << 30, NodeIDs: []int64{1, 2}, NodeLimits: map[string]float64{"1": 2}}
	valid := packageTrafficGroup{Name: "共享组", Limit: 3 << 30, NodeIDs: []int64{1}}
	for _, test := range []struct {
		name   string
		groups []packageTrafficGroup
		valid  bool
	}{
		{"valid", []packageTrafficGroup{valid}, true},
		{"clear", []packageTrafficGroup{}, true},
		{"empty name", []packageTrafficGroup{{Name: "  ", Limit: 3 << 30, NodeIDs: []int64{1}}}, false},
		{"zero", []packageTrafficGroup{{Name: "a", NodeIDs: []int64{1}}}, false},
		{"above package", []packageTrafficGroup{{Name: "a", Limit: 11 << 30, NodeIDs: []int64{1}}}, false},
		{"empty members", []packageTrafficGroup{{Name: "a", Limit: 3 << 30}}, false},
		{"nonmember", []packageTrafficGroup{{Name: "a", Limit: 3 << 30, NodeIDs: []int64{3}}}, false},
		{"missing node", []packageTrafficGroup{{Name: "a", Limit: 3 << 30, NodeIDs: []int64{4}}}, false},
		{"duplicate", []packageTrafficGroup{valid, valid}, false},
		{"duplicate within group", []packageTrafficGroup{{Name: "a", Limit: 3 << 30, NodeIDs: []int64{1, 1}}}, false},
		{"node quota", []packageTrafficGroup{{Name: "a", Limit: 1 << 30, NodeIDs: []int64{1}}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePackageTrafficGroups(test.groups, pkg, nodes); (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
	pkg.NodeIDs = nil
	if err := validatePackageTrafficGroups([]packageTrafficGroup{{Name: "all", Limit: 3 << 30, NodeIDs: []int64{3}}}, pkg, nodes); err != nil {
		t.Fatal("empty package selection means all nodes:", err)
	}
	pkg.Limit = 0
	if err := validatePackageTrafficGroups([]packageTrafficGroup{valid}, pkg, nodes); err != nil {
		t.Fatal("unlimited package should allow a positive group quota:", err)
	}
	pkg.Limit = -1
	if err := validatePackageTrafficGroups([]packageTrafficGroup{valid}, pkg, nodes); err == nil {
		t.Fatal("negative package quota must not be treated as unlimited")
	}
	pkg.Limit = 10 << 30
	pkg.NodeLimits["1"] = .01
	if err := validatePackageTrafficGroups([]packageTrafficGroup{{Name: "fraction", Limit: int64(math.Floor(.01 * (1 << 30))), NodeIDs: []int64{1}}}, pkg, nodes); err != nil {
		t.Fatal("equal fractional quota:", err)
	}
	groups := []packageTrafficGroup{{NodeIDs: []int64{1, 2, 3, 4}}}
	pkg.NodeIDs = []int64{1, 3}
	filtered := filterTrafficGroupNodes(groups, pkg, nodes)
	if fmt.Sprint(filtered[0].NodeIDs) != "[1 3]" || len(groups[0].NodeIDs) != 4 {
		t.Fatalf("filter mutated original or kept unavailable nodes: %+v", filtered)
	}
}

func TestPackageNodeTrafficUsage(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	baseline := 200.0
	for _, test := range []struct {
		name     string
		all      float64
		cycle    float64
		baseline *float64
		updated  time.Time
		want     int64
	}{
		{"weighted baseline", 800, 300, &baseline, start, 600},
		{"negative clamp", 100, 300, &baseline, start, 0},
		{"missing baseline", 800, 300, nil, time.Time{}, 300},
		{"prior cycle baseline", 800, 300, &baseline, start.Add(-time.Second), 300},
		{"empty", 0, 0, nil, time.Time{}, 0},
		{"saturate", math.Inf(1), 0, &baseline, start, math.MaxInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := packageNodeTrafficUsage(test.all, test.cycle, test.baseline, test.updated, start); got != test.want {
				t.Fatalf("got %d want %d", got, test.want)
			}
		})
	}
}

func TestTrafficGroupCycleAndBlockDecisions(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	assignment := trafficGroupAssignment{Start: time.Date(2026, 1, 12, 8, 0, 0, 0, time.UTC), MonthlyReset: true, ResetDay: 31}
	start, end := trafficGroupCycle(assignment, now)
	if start.Format("2006-01-02") != "2026-02-28" || end.Format("2006-01-02") != "2026-03-31" {
		t.Fatalf("short-month cycle %v %v", start, end)
	}
	assignment.LastReset = sql.NullTime{Time: now.Add(-time.Hour), Valid: true}
	start, end = trafficGroupCycle(assignment, now)
	if !start.Equal(assignment.LastReset.Time) {
		t.Fatal("manual reset should start a new cycle")
	}
	for _, test := range []struct {
		name        string
		used, limit int64
		nodes       int
		at          time.Time
		want        bool
	}{
		{"under", 99, 100, 1, now, false},
		{"hit", 100, 100, 1, now, true},
		{"raised quota", 100, 101, 1, now, false},
		{"removed members", 100, 100, 0, now, false},
		{"next cycle", 100, 100, 1, *end, false},
		{"not started", 100, 100, 1, start.Add(-time.Second), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := trafficGroupShouldBlock(test.used, test.limit, test.nodes, start, end, test.at); got != test.want {
				t.Fatalf("blocked=%v", got)
			}
		})
	}
	assignment.MonthlyReset, assignment.LastReset.Valid, assignment.End.Valid = false, false, false
	start, end = trafficGroupCycle(assignment, now)
	if !start.Equal(assignment.Start) || end != nil {
		t.Fatal("nonreset assignment should retain original cycle")
	}
}

func TestTrafficGroupDesiredSettingsCannotBeOverwritten(t *testing.T) {
	identity := serverConnectionIdentity{InboundTag: "node", User: "alice"}
	a := &app{trafficGroupsReady: true, trafficGroupBlocks: map[string][]serverConnectionIdentity{"1": {identity}}}
	settings := defaultServerConnectionSettings()
	for _, version := range []string{"v0.6.7", "v0.6.6", "", "garbage", "v0.6.8extra", "v-1.6.8"} {
		if a.trafficBlocksForHelper(settings, "1", version).BlockedIdentities != nil {
			t.Fatal("sent blocking field to old/invalid Helper:", version)
		}
	}
	merged := settingsForHelper(a.trafficBlocksForHelper(settings, "1", "v0.6.8"), "v0.6.8")
	if merged.BlockedIdentities == nil || len(*merged.BlockedIdentities) != 1 || (*merged.BlockedIdentities)[0] != identity || settings.BlockedIdentities != nil {
		t.Fatalf("incorrect merge: %+v", merged)
	}
	if validateServerConnectionSettings(merged) == nil {
		t.Fatal("admin can overwrite computed blocks")
	}
	store, err := openHelperState(filepath.Join(t.TempDir(), "state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.setConnectionSettings("1", merged); err != nil {
		t.Fatal(err)
	}
	if store.connectionSettings("1").BlockedIdentities != nil {
		t.Fatal("computed blocks persisted into admin settings")
	}
	clear := a.trafficBlocksForHelper(settings, "2", "v0.6.8")
	raw, _ := json.Marshal(clear)
	if !strings.Contains(string(raw), `"blocked_identities":[]`) {
		t.Fatalf("missing explicit clearing: %s", raw)
	}
	a.trafficGroupsReady = false
	if a.trafficBlocksForHelper(settings, "1", "v0.6.8").BlockedIdentities != nil {
		t.Fatal("startup/error must preserve Helper state until successful refresh")
	}
}

func TestTrafficGroupUnbindAndCycleTransitions(t *testing.T) {
	old := trafficGroupBlock{AssignmentID: 1, GroupID: 2, CycleStart: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	added, removed := trafficGroupBlockTransitions([]trafficGroupBlock{old}, nil)
	if len(added) != 0 || len(removed) != 1 {
		t.Fatal("unbinding/deleting must log cleared over-quota state even after database cascade")
	}
	next := old
	next.CycleStart = old.CycleStart.AddDate(0, 1, 0)
	added, removed = trafficGroupBlockTransitions([]trafficGroupBlock{old}, []trafficGroupBlock{next})
	if len(added) != 1 || len(removed) != 1 {
		t.Fatal("new cycle should replace old block state")
	}
	added, removed = trafficGroupBlockTransitions([]trafficGroupBlock{next}, []trafficGroupBlock{next})
	if len(added)+len(removed) != 0 {
		t.Fatal("unchanged blocks should not log repeated transitions")
	}
}

func TestTrafficGroupLogsDistinguishQuotaFromDesiredBlocks(t *testing.T) {
	var output bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previousOutput)
	block := trafficGroupBlock{AssignmentID: 1, GroupID: 2, Username: "alice"}
	logTrafficGroupTransitions([]trafficGroupBlock{block}, nil, nil, nil)
	if !strings.Contains(output.String(), "traffic group over-quota user=alice") || strings.Contains(output.String(), "traffic group desired") || strings.Contains(output.String(), "traffic group block") {
		t.Fatalf("unresolved identity was logged as a runtime block: %s", &output)
	}
	output.Reset()
	blocks := map[string][]serverConnectionIdentity{"4": {{InboundTag: "in-a", User: "alice-a"}}}
	logTrafficGroupTransitions(nil, nil, nil, blocks)
	if !strings.Contains(output.String(), "traffic group desired block server=4 inbound=in-a identity=alice-a") || !strings.Contains(output.String(), "pending Helper/Core application") {
		t.Fatalf("desired block should not claim applied enforcement: %s", &output)
	}
	output.Reset()
	logTrafficGroupTransitions(nil, nil, blocks, blocks)
	if output.Len() != 0 {
		t.Fatalf("unchanged desired state logged repeatedly: %s", &output)
	}
	logTrafficGroupTransitions(nil, []trafficGroupBlock{block}, blocks, nil)
	if !strings.Contains(output.String(), "traffic group over-quota cleared") || !strings.Contains(output.String(), "traffic group desired unblock") || strings.Contains(output.String(), "traffic group unblock") {
		t.Fatalf("clearing quota should be separate from desired unblocking: %s", &output)
	}
}

func TestTrafficGroupCycleUsesOfficialLocalWallTime(t *testing.T) {
	zone := time.FixedZone("official", 8*60*60)
	now := time.Date(2026, 10, 1, 1, 0, 0, 0, zone)
	assignment := trafficGroupAssignment{Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), MonthlyReset: true, ResetDay: 1}
	start, _ := trafficGroupCycle(assignment, now)
	if start.Day() != 1 || start.Month() != time.October || start.Location() != zone {
		t.Fatalf("wrong local reset boundary: %v", start)
	}
	baseline := 200.0
	updated := time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC)
	if got := packageNodeTrafficUsage(800, 100, &baseline, updated, start); got != 600 {
		t.Fatalf("timestamp-without-time-zone baseline misread: %d", got)
	}
}

func TestTrafficGroupCapability(t *testing.T) {
	now := time.Now()
	node := trafficGroupNode{ID: 1, ServerID: 1, Mode: "external"}
	record := serverDetailedConnectionRecord{HelperVersion: "v0.6.8", UpdatedAt: now, Snapshot: serverDetailedConnectionSnapshot{Core: serverCoreConnectionStatus{Version: 7, Available: true, TrafficBlockSupported: true}}}
	if trafficGroupCapability(node, record, now).Status != "enforced" {
		t.Fatal("current versions not enforceable")
	}
	record.Snapshot.Core.TrafficBlockSupported = false
	if trafficGroupCapability(node, record, now).Status != "core_outdated" {
		t.Fatal("failed Core config hidden")
	}
	record.HelperVersion = "v0.6.7"
	if trafficGroupCapability(node, record, now).Status != "helper_outdated" {
		t.Fatal("old Helper hidden")
	}
	node.Mode = "embedded"
	if trafficGroupCapability(node, record, now).Status != "embedded" {
		t.Fatal("embedded hidden")
	}
	node.ServerID = 0
	if trafficGroupCapability(node, record, now).Status != "external_node" {
		t.Fatal("external node hidden")
	}
}

func TestPackageTrafficGroupsRequireAdmin(t *testing.T) {
	a := &app{}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		response := httptest.NewRecorder()
		a.packageTrafficGroupsHandler(response, httptest.NewRequest(method, "/api/custom/packages/1/traffic-groups", nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthed %s: %d", method, response.Code)
		}
	}
}

func TestTrafficGroupsIsolatedPostgres(t *testing.T) {
	dsn := os.Getenv("MMWXC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MMWXC_TEST_POSTGRES_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("mmwxc_groups_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE users(username text PRIMARY KEY,is_active bigint,email text)`,
		`CREATE TABLE packages(id bigint PRIMARY KEY,traffic_limit_bytes bigint,nodes text,node_traffic_limits text,traffic_mode text)`,
		`CREATE TABLE user_package_assignments(id bigint PRIMARY KEY,username text,package_id bigint,package_start_date timestamp,package_end_date timestamp,last_reset_at timestamp,is_reset bigint,reset_day bigint,traffic_limit_override bigint,status text,created_at timestamp)`,
		`CREATE TABLE remote_servers(id bigint PRIMARY KEY,name text,xray_mode text)`,
		`CREATE TABLE nodes(id bigint PRIMARY KEY,node_name text,original_server text,inbound_tag text,username text,node_type text)`,
		`CREATE TABLE traffic_daily_user_nodes(server_id bigint,node_id bigint,username text,date text,weighted_uplink real,weighted_downlink real)`,
		`CREATE TABLE package_user_node_traffic_baselines(username text,package_id bigint,node_id bigint,baseline real,updated_at timestamp)`,
		`INSERT INTO users VALUES('alice',1,'alice@example.test')`,
		`INSERT INTO packages VALUES(1,10737418240,'[1,2]','{}','oneway'),(2,10737418240,'[]','{}','twoway')`,
		`INSERT INTO nodes VALUES(1,'one','','','admin','physical'),(2,'two','','','admin','physical'),(3,'three','','','admin','physical')`,
		`INSERT INTO user_package_assignments VALUES(1,'alice',1,'2026-10-01',NULL,NULL,0,1,100,'active','2026-10-01')`,
		`INSERT INTO traffic_daily_user_nodes VALUES(1,1,'alice','2026-09-01',100,200),(1,1,'alice','2026-10-01',200,300),(1,2,'alice','2026-09-01',999,999),(1,2,'alice','2026-10-01',50,150)`,
		`INSERT INTO package_user_node_traffic_baselines VALUES('alice',1,1,400,'2026-10-01 01:00:00')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture: %v\n%s", err, statement)
		}
	}
	store := &postgresAdminSessionStore{db: db}
	ctx := context.Background()
	if err := store.EnsurePackageTrafficGroupsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsurePackageTrafficGroupsSchema(ctx); err != nil {
		t.Fatal("schema not idempotent:", err)
	}
	groups, err := store.replaceTrafficGroups(ctx, 1, []packageTrafficGroup{{Name: "a", Limit: 500, NodeIDs: []int64{1, 2}}})
	if err != nil || len(groups) != 1 {
		t.Fatalf("save %+v %v", groups, err)
	}
	if _, err := store.replaceTrafficGroups(ctx, 2, groups); err == nil {
		t.Fatal("cross-package group takeover accepted")
	}
	assignments, err := store.trafficGroupAssignments(ctx, 1)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("assignments %+v %v", assignments, err)
	}
	usage, err := store.trafficGroupNodeUsage(ctx, assignments[0], assignments[0].Start)
	if err != nil || usage[1] != 400 || usage[2] != 200 {
		t.Fatalf("usage with baseline and fallback %+v %v", usage, err)
	}
	a := &app{adminStore: store, apiToken: "test", detailedConnections: map[string]serverDetailedConnectionRecord{}}
	if err := a.refreshTrafficGroupsLocked(ctx, store); err != nil {
		t.Fatal(err)
	}
	if got := a.trafficGroupUsage[1].Usage; len(got) != 1 || got[0].Used != 600 || got[0].Blocked || got[0].Nodes[0].Status != "external_node" {
		t.Fatalf("external node over-limit surfaced incorrectly: %+v", got)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM mmwxc_package_traffic_group_blocks`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("block count %d %v", count, err)
	}
	for _, statement := range []string{
		`ALTER TABLE nodes ADD protocol text DEFAULT 'vless', ADD raw_url text DEFAULT '', ADD parsed_config text DEFAULT '{}', ADD clash_config text DEFAULT '{}'`,
		`CREATE TABLE user_inbound_configs(username text,server_id bigint,inbound_tag text,credential_json text)`,
		`CREATE TABLE user_subaccounts(username text,routed_node_id bigint,email text,credential_json text,is_active bigint)`,
		`CREATE TABLE package_assignment_inbound_configs(assignment_id bigint,username text,server_id bigint,inbound_tag text,email text,credential_json text)`,
		`CREATE TABLE package_assignment_subaccounts(assignment_id bigint,username text,routed_node_id bigint,email text,credential_json text,is_active bigint)`,
		`CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamp)`,
		`INSERT INTO remote_servers VALUES(1,'managed','external')`,
		`UPDATE nodes SET original_server='managed',inbound_tag=CASE id WHEN 1 THEN 'one' ELSE 'two' END WHERE id IN(1,2)`,
		`INSERT INTO package_assignment_inbound_configs VALUES(1,'alice',1,'one','alice-one','{"id":"uuid-one","email":"alice-one"}'),(1,'alice',1,'two','alice-two','{"id":"uuid-two","email":"alice-two"}')`,
		`INSERT INTO server_xray_config_snapshots VALUES(1,1,'{"inbounds":[{"tag":"one","protocol":"vless","settings":{"clients":[{"id":"uuid-one","email":"alice-one"}]}},{"tag":"two","protocol":"vless","settings":{"clients":[{"id":"uuid-two","email":"alice-two"}]}}]}','current',CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("managed fixture: %v\n%s", err, statement)
		}
	}
	if err := store.EnsureConnectionOwnershipSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureUserLifecycleSchema(ctx); err != nil {
		t.Fatal(err)
	}
	a.detailedConnections["1"] = serverDetailedConnectionRecord{HelperVersion: "v0.6.8", UpdatedAt: time.Now(), Snapshot: serverDetailedConnectionSnapshot{Core: serverCoreConnectionStatus{Version: 7, Available: true, TrafficBlockSupported: true}}}
	if err := a.refreshTrafficGroupsLocked(ctx, store); err != nil {
		t.Fatal("managed identity database load:", err)
	}
	if len(a.trafficGroupBlocks["1"]) != 2 || !a.trafficGroupUsage[1].Usage[0].Blocked {
		t.Fatalf("managed group identities not blocked: %+v %+v", a.trafficGroupBlocks, a.trafficGroupUsage[1])
	}
	for _, statement := range []string{
		`INSERT INTO users VALUES('admin',1,'admin@example.test')`,
		`UPDATE nodes SET protocol='shadowsocks',original_server='managed',inbound_tag=CASE id WHEN 1 THEN 'one' WHEN 2 THEN 'two' ELSE 'three' END,clash_config=CASE id WHEN 1 THEN '{"cipher":"2022-blake3-aes-128-gcm","password":"server-key:owner-one-key"}' WHEN 2 THEN '{"cipher":"2022-blake3-aes-128-gcm","password":"server-key:owner-two-key"}' ELSE '{"cipher":"2022-blake3-aes-128-gcm","password":"server-key:owner-three-key"}' END`,
		`UPDATE package_assignment_inbound_configs SET credential_json=CASE inbound_tag WHEN 'one' THEN '{"email":"alice-one","password":"alice-one-key"}' ELSE '{"email":"alice-two","password":"alice-two-key"}' END`,
		`INSERT INTO package_assignment_inbound_configs VALUES(1,'alice',1,'three','alice-three','{"email":"alice-three","password":"alice-three-key"}')`,
		`INSERT INTO user_inbound_configs SELECT username,server_id,inbound_tag,credential_json FROM package_assignment_inbound_configs`,
		`UPDATE server_xray_config_snapshots SET config_json='{"inbounds":[{"tag":"one","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"server-key","clients":[{"email":"owner-one","password":"owner-one-key"},{"email":"alice-one","password":"alice-one-key"}]}},{"tag":"two","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"server-key","clients":[{"email":"owner-two","password":"owner-two-key"},{"email":"alice-two","password":"alice-two-key"}]}},{"tag":"three","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"server-key","clients":[{"email":"owner-three","password":"owner-three-key"},{"email":"alice-three","password":"alice-three-key"}]}}]}'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("SS2022 fixture: %v\n%s", err, statement)
		}
	}
	if err := a.refreshTrafficGroupsLocked(ctx, store); err != nil {
		t.Fatal("SS2022 identity database load:", err)
	}
	desired := a.trafficGroupBlocks["1"]
	if len(desired) != 2 || desired[0] != (serverConnectionIdentity{InboundTag: "one", User: "alice-one"}) || desired[1] != (serverConnectionIdentity{InboundTag: "two", User: "alice-two"}) || !a.trafficGroupUsage[1].Usage[0].Blocked {
		t.Fatalf("SS2022 group should block only the two group identities: %+v %+v", desired, a.trafficGroupUsage[1])
	}
	ownership, err := store.ConnectionOwnership(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	_, mappings := buildManagementView(ownershipSnapshot("one", 443, "owner-one", "alice-one"), defaultServerConnectionSettings(), ownership)
	if len(mappings) != 2 || mappings[0].Identity.User != "alice-one" || mappings[0].Group != "alice" || mappings[1].Identity.User != "owner-one" || mappings[1].Group != "admin" {
		t.Fatalf("SS2022 binding stole the existing owner's mapping: %+v", mappings)
	}
	groups[0].Limit = 1000
	if _, err := store.replaceTrafficGroups(ctx, 1, groups); err != nil {
		t.Fatal(err)
	}
	if err := a.refreshTrafficGroupsLocked(ctx, store); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM mmwxc_package_traffic_group_blocks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("raised limit did not unblock: %d %v", count, err)
	}
	if len(a.trafficGroupBlocks["1"]) != 0 {
		t.Fatal("Core desired blocks not lifted")
	}
	if _, err := db.Exec(`UPDATE packages SET nodes='[2]' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/packages/1/traffic-groups", nil)
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	a.packageTrafficGroupsHandler(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"node_ids":[2]`) {
		t.Fatalf("stale members not filtered: %d %s", response.Code, response.Body)
	}
	block := trafficGroupBlock{AssignmentID: 1, PackageID: 1, GroupID: groups[0].ID, Username: "alice", CycleStart: assignments[0].Start, Used: 600}
	if _, _, err := store.syncTrafficGroupBlocks(ctx, []trafficGroupBlock{block}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE username='alice'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM mmwxc_package_traffic_group_blocks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("user deletion failed to cascade %d %v", count, err)
	}
	if _, err := store.replaceTrafficGroups(ctx, 1, []packageTrafficGroup{}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM mmwxc_package_traffic_groups`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("full replace clear %d %v", count, err)
	}
}
