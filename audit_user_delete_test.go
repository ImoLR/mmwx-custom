package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Lifecycle regressions use an isolated loopback schema, never public or production.
func auditUserDeleteDB(t *testing.T, id string) *sql.DB {
	t.Helper()

	dsn := os.Getenv("MMWXC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MMWXC_TEST_POSTGRES_DSN is not configured")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") {
		t.Fatal("audit requires a loopback PostgreSQL URL")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("mmwxc_audit_delete_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Errorf("clean isolated audit schema: %v", err)
		}
		db.Close()
	})
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE packages(id bigint PRIMARY KEY,name text,nodes text)`,
		`CREATE TABLE users(username text PRIMARY KEY,role text DEFAULT 'user',email text DEFAULT '',package_id bigint REFERENCES packages(id) ON DELETE SET NULL)`,
		`CREATE TABLE remote_servers(id bigint PRIMARY KEY,name text)`,
		`CREATE TABLE user_package_assignments(id bigint PRIMARY KEY,username text REFERENCES users(username) ON DELETE CASCADE,package_id bigint REFERENCES packages(id) ON DELETE CASCADE,status text)`,
		`CREATE TABLE package_assignment_inbound_configs(assignment_id bigint REFERENCES user_package_assignments(id) ON DELETE CASCADE,username text REFERENCES users(username) ON DELETE CASCADE,server_id bigint,inbound_tag text,protocol text,credential_json text)`,
		userLifecycleSchema,
		userManagementCascadeSchema,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	return db
}

func auditDeleteExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func TestAuditUAD02DeleteRetryRetainsFailedInboundAfterPackageCascade(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-D02")
	credential := map[string]any{"id": "11111111-1111-4111-8111-111111111111", "email": "alice@example.test"}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	auditDeleteExec(t, db, `INSERT INTO packages VALUES(1,'ordinary template','[]')`)
	auditDeleteExec(t, db, `INSERT INTO users(username,email,package_id) VALUES('alice','alice@example.test',1)`)
	auditDeleteExec(t, db, `INSERT INTO remote_servers VALUES(5,'server-5')`)
	auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(1,'alice',1,'active')`)
	auditDeleteExec(t, db, `INSERT INTO package_assignment_inbound_configs VALUES(1,'alice',5,'assignment-inbound','vless',$1)`, string(raw))
	store := &postgresAdminSessionStore{db: db}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("assignment-inbound", "vless", credential))})
	defer server.Close()
	fixture.failTagOnce = "assignment-inbound"
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	application := &app{adminStore: store, officialInternalTarget: target}
	ctx := context.Background()
	plan, err := application.buildDeletionPlan(ctx, "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDeletePlan(ctx, "alice", "audit-delete-retry", plan); err != nil {
		t.Fatal(err)
	}
	first := application.executeDeletePlan(ctx, "session", "alice", "audit-delete-retry", plan)
	if first.UserDeleted || first.PendingCount != 2 {
		t.Fatalf("expected failed inbound and waiting package: %+v", first)
	}
	var configs, assignments int
	var packageID sql.NullInt64
	if err := db.QueryRow(`SELECT COUNT(*) FROM package_assignment_inbound_configs`).Scan(&configs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_package_assignments`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT package_id FROM users WHERE username='alice'`).Scan(&packageID); err != nil {
		t.Fatal(err)
	}
	if configs != 1 || assignments != 1 || !packageID.Valid {
		t.Fatalf("business relations removed before inbound: configs=%d assignments=%d package=%v", configs, assignments, packageID)
	}
	// Simulate another writer removing the binding; the persisted credential
	// snapshot must still drive the retry after a process restart.
	auditDeleteExec(t, db, `DELETE FROM packages WHERE id=1`)
	application = &app{adminStore: &postgresAdminSessionStore{db: db}, officialInternalTarget: target}
	plan, err = application.buildDeletionPlan(ctx, "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after partial delete: configs=%d assignments=%d direct_package_valid=%t retry_items=%d", configs, assignments, packageID.Valid, len(plan))
	if err := store.SaveDeletePlan(ctx, "alice", "audit-delete-retry", plan); err != nil {
		t.Fatal(err)
	}
	second := application.executeDeletePlan(ctx, "session", "alice", "audit-delete-retry", plan)
	if !second.UserDeleted || findConfigInbound(fixture.configs[5], "assignment-inbound") != nil {
		t.Fatal("UA-D02: retry deleted the user while its original working credential remains on the server")
	}
}

func TestAuditUAD04DeleteRemovesPrivateForwardChainOwnership(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-D04")
	auditDeleteExec(t, db, `CREATE TABLE forward_chain_nodes(node_id bigint PRIMARY KEY,owner_username text,billing_assignment_id bigint)`)
	auditDeleteExec(t, db, `CREATE TABLE nodes(id bigint PRIMARY KEY,username text)`)
	auditDeleteExec(t, db, `INSERT INTO packages VALUES(1,'alice package','[]'),(2,'bob package','[]')`)
	auditDeleteExec(t, db, `INSERT INTO users(username,package_id) VALUES('alice',1),('bob',2)`)
	auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(1,'alice',1,'active'),(2,'alice',1,'inactive'),(3,'bob',2,'active')`)
	auditDeleteExec(t, db, `INSERT INTO nodes VALUES(10,'admin'),(11,'admin'),(12,'admin'),(13,'bob'),(14,'bob')`)
	auditDeleteExec(t, db, `INSERT INTO forward_chain_nodes VALUES(10,'alice',3),(11,'bob',1),(12,'bob',2),(13,'bob',3),(14,NULL,NULL)`)
	store := &postgresAdminSessionStore{db: db}
	if err := store.FinalizeManagementUserDeletion(context.Background(), "alice", "audit-delete-forward"); err != nil {
		t.Fatal(err)
	}
	var remainingPrivate, preserved, otherAssignments int
	if err := db.QueryRow(`SELECT COUNT(*) FROM forward_chain_nodes WHERE node_id IN (10,11,12)`).Scan(&remainingPrivate); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM forward_chain_nodes WHERE node_id IN (13,14)`).Scan(&preserved); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_package_assignments WHERE id=3 AND username='bob'`).Scan(&otherAssignments); err != nil {
		t.Fatal(err)
	}
	if remainingPrivate != 0 || preserved != 2 || otherAssignments != 1 {
		t.Fatalf("UA-D04: owner/billing cleanup damaged unrelated relations: private=%d preserved=%d bob assignments=%d", remainingPrivate, preserved, otherAssignments)
	}
}

func auditDeletionApplication(t *testing.T, db *sql.DB, configs map[int64]map[string]any) (*app, *lifecycleAgentFixture) {
	t.Helper()
	fixture, server := newLifecycleAgentFixture(configs)
	t.Cleanup(server.Close)
	fixture.removeNodes = func(serverID int64, tag string) error {
		_, err := db.Exec(`DELETE FROM nodes WHERE original_server=(SELECT name FROM remote_servers WHERE id=$1) AND inbound_tag=$2`, serverID, tag)
		return err
	}
	fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/api/admin/packages" {
			rows, err := db.Query(`SELECT id,name,nodes,node_traffic_limits,node_name_overrides FROM packages ORDER BY id`)
			if err != nil {
				t.Error(err)
				writeJSON(w, 500, map[string]any{"success": false})
				return true
			}
			defer rows.Close()
			var packages []map[string]any
			for rows.Next() {
				var id int64
				var name, nodes, limits, names string
				if err := rows.Scan(&id, &name, &nodes, &limits, &names); err != nil {
					t.Error(err)
					return true
				}
				pkg := map[string]any{"id": id, "name": name}
				for key, raw := range map[string]string{"nodes": nodes, "node_traffic_limits": limits, "node_name_overrides": names} {
					var value any
					if err := json.Unmarshal([]byte(raw), &value); err != nil {
						t.Error(err)
						return true
					}
					pkg[key] = value
				}
				packages = append(packages, pkg)
			}
			writeJSON(w, 200, map[string]any{"packages": packages})
			return true
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v3" {
			var request struct {
				Op      string         `json:"op"`
				Payload map[string]any `json:"payload"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.Op != "f9bed75c75a38c5f" {
				t.Error("invalid package update")
				writeJSON(w, 400, map[string]any{"success": false})
				return true
			}
			nodes, _ := json.Marshal(request.Payload["nodes"])
			limits, _ := json.Marshal(request.Payload["node_traffic_limits"])
			names, _ := json.Marshal(request.Payload["node_name_overrides"])
			_, err := db.Exec(`UPDATE packages SET nodes=$2,node_traffic_limits=$3,node_name_overrides=$4 WHERE id=$1`, request.Payload["id"], string(nodes), string(limits), string(names))
			if err != nil {
				t.Error(err)
				writeJSON(w, 500, map[string]any{"success": false})
				return true
			}
			writeJSON(w, 200, map[string]any{"success": true})
			return true
		}
		return false
	}
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &app{adminStore: &postgresAdminSessionStore{db: db}, officialInternalTarget: target}, fixture
}

func seedDeletionNodes(t *testing.T, db *sql.DB) map[int64]map[string]any {
	t.Helper()
	for _, statement := range []string{
		`ALTER TABLE packages ADD COLUMN node_traffic_limits text DEFAULT '{}', ADD COLUMN node_name_overrides text DEFAULT '{}'`,
		`CREATE TABLE nodes(id bigint PRIMARY KEY,node_name text,username text,original_server text,inbound_tag text,protocol text)`,
		`CREATE TABLE user_inbound_configs(username text,server_id bigint,inbound_tag text,protocol text,credential_json text)`,
		`CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,source text,created_at timestamptz DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE mmwxc_package_traffic_groups(id bigint PRIMARY KEY,package_id bigint REFERENCES packages(id) ON DELETE CASCADE,node_ids jsonb)`,
		`CREATE TABLE forward_chain_nodes(node_id bigint PRIMARY KEY,owner_username text,billing_assignment_id bigint)`,
		`INSERT INTO users(username,role,email) VALUES('admin','admin','admin@example.test'),('alice','user','alice@example.test'),('bob','user','bob@example.test')`,
		`INSERT INTO remote_servers VALUES(5,'server-5')`,
		`INSERT INTO nodes VALUES(10,'Alice','admin','server-5','alice','vless'),(11,'Alice with admin','admin','server-5','coowned','vless'),(12,'Admin only','admin','server-5','admin-only','vless'),(13,'External','admin','','','vless'),(14,'Bob','admin','server-5','bob','vless')`,
		`INSERT INTO user_inbound_configs VALUES('alice',5,'alice','vless','{"id":"alice-secret","email":"alice@example.test"}'),('alice',5,'coowned','vless','{"id":"alice-coowned","email":"alice@example.test"}'),('bob',5,'bob','vless','{"id":"bob-secret","email":"bob@example.test"}')`,
	} {
		auditDeleteExec(t, db, statement)
	}
	admin := map[string]any{"id": "admin-default", "email": "admin@example.test"}
	config := lifecycleConfig(
		lifecycleInbound("alice", "vless", map[string]any{"id": "alice-secret", "email": "alice@example.test"}),
		lifecycleInbound("coowned", "vless", map[string]any{"id": "alice-coowned", "email": "alice@example.test"}, admin),
		lifecycleInbound("admin-only", "vless", admin),
		lifecycleInbound("bob", "vless", map[string]any{"id": "bob-secret", "email": "bob@example.test"}),
	)
	raw, _ := json.Marshal(config)
	auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots(id,server_id,config_json,source) VALUES(1,5,$1,'master_write')`, string(raw))
	return map[int64]map[string]any{5: config}
}

func runDeletionRegression(t *testing.T, application *app, username string) ([]lifecyclePlanItem, lifecycleDeleteResult) {
	t.Helper()
	ctx := context.Background()
	plan, err := application.buildDeletionPlan(ctx, "session", username)
	if err != nil {
		t.Fatal(err)
	}
	store := application.adminStore.(lifecycleStore)
	if err := store.SaveDeletePlan(ctx, username, "regression-delete", plan); err != nil {
		t.Fatal(err)
	}
	return plan, application.executeDeletePlan(ctx, "session", username, "regression-delete", plan)
}

func TestAuditUAD01DeletePackageFollowsNodeOwnership(t *testing.T) {
	for _, test := range []struct {
		name, nodes    string
		keep, conflict bool
	}{
		{"own only", "[10,11]", false, false},
		{"own admin external missing", "[10,11,12,13,999]", false, false},
		{"another users node", "[10,11,14]", true, false},
		{"unexpected inactive binding", "[10,11]", true, true},
		{"unknown credential", "[10,11,15]", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := auditUserDeleteDB(t, "UA-D01")
			configs := seedDeletionNodes(t, db)
			auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes,node_traffic_limits,node_name_overrides) VALUES(1,'Alice package',$1,'{"10":1,"11":2,"14":3}','{"10":"own","14":"other"}')`, test.nodes)
			auditDeleteExec(t, db, `UPDATE users SET package_id=1 WHERE username='alice'`)
			auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(1,'alice',1,'active')`)
			auditDeleteExec(t, db, `INSERT INTO forward_chain_nodes VALUES(13,'bob',1),(14,'bob',NULL)`)
			auditDeleteExec(t, db, `INSERT INTO mmwxc_package_traffic_groups VALUES(1,1,'[10,11,14]')`)
			if test.name == "unexpected inactive binding" {
				auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(2,'bob',1,'inactive')`)
			}
			if test.name == "unknown credential" {
				auditDeleteExec(t, db, `INSERT INTO nodes VALUES(15,'Unknown','admin','server-5','unknown','vless')`)
				configs[5]["inbounds"] = append(configs[5]["inbounds"].([]any), lifecycleInbound("unknown", "vless", map[string]any{"id": "unclaimed"}))
			}
			application, fixture := auditDeletionApplication(t, db, configs)
			plan, result := runDeletionRegression(t, application, "alice")
			var pkg lifecyclePlanItem
			for _, item := range plan {
				if item.PackageID == 1 {
					pkg = item
				}
			}
			want := lifecycleActionDeletePackage
			if test.keep {
				want = lifecycleActionKeepPackage
			}
			if test.conflict {
				want = lifecycleActionConflict
			}
			if pkg.Action != want || len(pkg.OwnNodes) != 2 || pkg.OwnNodes[0].Name == "" {
				t.Fatalf("package classification=%+v", pkg)
			}
			if test.conflict {
				if result.UserDeleted || pkg.DecisionNote == "" {
					t.Fatalf("conflict lost: %+v", result)
				}
				return
			}
			if !result.UserDeleted {
				t.Fatalf("delete failed: %+v", result)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM packages WHERE id=1`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if (count == 1) != test.keep {
				t.Fatalf("package remains=%d wantKeep=%t", count, test.keep)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id IN(10,11)`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("own node rows=%d err=%v", count, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM forward_chain_nodes WHERE node_id=13`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("billing-only relation remains=%d err=%v", count, err)
			}
			if findConfigInbound(fixture.configs[5], "alice") != nil || findConfigInbound(fixture.configs[5], "coowned") != nil || findConfigInbound(fixture.configs[5], "bob") == nil || findConfigInbound(fixture.configs[5], "admin-only") == nil {
				t.Fatal("wrong inbound deletion")
			}
			if test.keep {
				assertDeletionPackagePruned(t, db, 1)
			}
		})
	}
}

func assertDeletionPackagePruned(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	var nodes, limits, names, groups string
	if err := db.QueryRow(`SELECT nodes,node_traffic_limits,node_name_overrides FROM packages WHERE id=$1`, id).Scan(&nodes, &limits, &names); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if json.Unmarshal([]byte(nodes), &ids) != nil || !reflect.DeepEqual(ids, []int64{14}) {
		t.Fatalf("remaining package nodes=%s", nodes)
	}
	for _, raw := range []string{limits, names} {
		var values map[string]any
		if json.Unmarshal([]byte(raw), &values) != nil || values["10"] != nil || values["11"] != nil || values["14"] == nil {
			t.Fatalf("per-node data=%s", raw)
		}
	}
	if err := db.QueryRow(`SELECT node_ids::text FROM mmwxc_package_traffic_groups WHERE package_id=$1`, id).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if strings.ReplaceAll(groups, " ", "") != "[14]" {
		t.Fatalf("group nodes=%s", groups)
	}
}

func TestAuditUAD05DeletePrunesOtherUsersPackage(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-D05")
	configs := seedDeletionNodes(t, db)
	auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes,node_traffic_limits,node_name_overrides) VALUES(2,'Bob package','[10,11,14]','{"10":1,"11":2,"14":3}','{"10":"own","14":"other"}')`)
	auditDeleteExec(t, db, `UPDATE users SET package_id=2 WHERE username='bob'`)
	auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(2,'bob',2,'active')`)
	auditDeleteExec(t, db, `INSERT INTO mmwxc_package_traffic_groups VALUES(2,2,'[10,11,14]')`)
	auditDeleteExec(t, db, `INSERT INTO forward_chain_nodes VALUES(10,'bob',2),(14,'bob',2)`)
	application, fixture := auditDeletionApplication(t, db, configs)
	_, result := runDeletionRegression(t, application, "alice")
	if !result.UserDeleted {
		t.Fatalf("delete failed: %+v", result)
	}
	assertDeletionPackagePruned(t, db, 2)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id IN(10,11)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted nodes=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM forward_chain_nodes`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("forward node references=%d err=%v", count, err)
	}
	if findConfigInbound(fixture.configs[5], "bob") == nil {
		t.Fatal("Bob credential lost")
	}
}

func TestDeletePackageRechecksOwnershipAndBindings(t *testing.T) {
	for _, change := range []string{"other node", "unknown node", "second binding"} {
		t.Run(change, func(t *testing.T) {
			db := auditUserDeleteDB(t, "UA-D01")
			configs := seedDeletionNodes(t, db)
			auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes) VALUES(1,'Alice','[13]')`)
			auditDeleteExec(t, db, `UPDATE users SET package_id=1 WHERE username='alice'`)
			application, _ := auditDeletionApplication(t, db, configs)
			plan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
			if err != nil {
				t.Fatal(err)
			}
			var item lifecyclePlanItem
			for _, candidate := range plan {
				if candidate.PackageID == 1 {
					item = candidate
				}
			}
			if item.Action != lifecycleActionDeletePackage {
				t.Fatalf("initial plan=%+v", item)
			}
			switch change {
			case "other node":
				auditDeleteExec(t, db, `UPDATE packages SET nodes='[13,14]' WHERE id=1`)
			case "unknown node":
				auditDeleteExec(t, db, `INSERT INTO nodes VALUES(15,'Unknown','admin','missing-server','tag','vless')`)
				auditDeleteExec(t, db, `UPDATE packages SET nodes='[13,15]' WHERE id=1`)
			case "second binding":
				auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(2,'bob',1,'inactive')`)
			}
			if err := application.executeLifecycleDeleteItem(context.Background(), "session", "alice", &item); err == nil {
				t.Fatal("changed ownership/binding was not rejected inside delete transaction")
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM packages WHERE id=1`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("package removed: count=%d err=%v", count, err)
			}
		})
	}
}

func TestDeleteRetryPrunesPackageAfterOfficialAlreadyRemovedNodes(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-D02/UA-D05")
	configs := seedDeletionNodes(t, db)
	auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes,node_traffic_limits,node_name_overrides) VALUES(2,'Bob package','[10,11,14]','{"10":1,"11":2,"14":3}','{"10":"own","14":"other"}')`)
	auditDeleteExec(t, db, `UPDATE users SET package_id=2 WHERE username='bob'`)
	auditDeleteExec(t, db, `INSERT INTO mmwxc_package_traffic_groups VALUES(2,2,'[10,11,14]')`)
	application, fixture := auditDeletionApplication(t, db, configs)
	original := fixture.extraHandler
	failed := false
	fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/v3" && !failed {
			failed = true
			writeJSON(w, 503, map[string]any{"success": false})
			return true
		}
		return original(w, r)
	}
	_, first := runDeletionRegression(t, application, "alice")
	if first.UserDeleted || first.PendingCount != 1 {
		t.Fatalf("first=%+v", first)
	}
	// Official remove prunes its node JSON before Custom finishes traffic groups.
	auditDeleteExec(t, db, `UPDATE packages SET nodes='[14]' WHERE id=2`)
	plan, second := runDeletionRegression(t, application, "alice")
	if !second.UserDeleted {
		t.Fatalf("retry=%+v", second)
	}
	for _, item := range plan {
		if item.PackageID == 2 && len(item.DeletedNodeIDs) != 2 {
			t.Fatalf("cleanup snapshot lost: %+v", item)
		}
	}
	assertDeletionPackagePruned(t, db, 2)
}

func TestDeleteDoesNotFinalizeUnconfirmedPackageCleanup(t *testing.T) {
	for _, failure := range []string{"ignored package update", "credential restored during package update"} {
		t.Run(failure, func(t *testing.T) {
			db := auditUserDeleteDB(t, "UA-D02/UA-D05")
			configs := seedDeletionNodes(t, db)
			auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes) VALUES(2,'Bob package','[10,11,14]')`)
			auditDeleteExec(t, db, `UPDATE users SET package_id=2 WHERE username='bob'`)
			application, fixture := auditDeletionApplication(t, db, configs)
			original := fixture.extraHandler
			fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/api/v3" {
					if failure == "ignored package update" {
						writeJSON(w, 200, map[string]any{"success": true})
						return true
					}
					inbounds := fixture.configs[5]["inbounds"].([]any)
					fixture.configs[5]["inbounds"] = append(inbounds, lifecycleInbound("alice", "vless", map[string]any{"id": "alice-secret", "email": "alice@example.test"}))
				}
				return original(w, r)
			}
			_, result := runDeletionRegression(t, application, "alice")
			if result.UserDeleted || result.PendingCount == 0 || result.State != lifecycleStateDeletePartial {
				t.Fatalf("unconfirmed cleanup finalized user: %+v", result)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE username='alice'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("user deleted: count=%d err=%v", count, err)
			}
		})
	}
}
