package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

// Audit reproducers assert the intended safe outcome and deliberately fail on
// the audited implementation. They never use the public schema or production.
func auditUserDeleteDB(t *testing.T, id string) *sql.DB {
	t.Helper()
	if os.Getenv("MMWXC_RUN_USER_AUDIT") != "1" {
		t.Skip("audit: " + id)
	}
	dsn := os.Getenv("MMWXC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("set MMWXC_TEST_POSTGRES_DSN to the disposable local harness")
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

func TestAuditUAD01DeletePreservesOtherUsersInactiveAssignments(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-D01")
	auditDeleteExec(t, db, `INSERT INTO packages VALUES(1,'ordinary reusable template','[]')`)
	auditDeleteExec(t, db, `INSERT INTO users(username,package_id) VALUES('alice',1),('bob',NULL)`)
	auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(1,'alice',1,'active'),(2,'bob',1,'inactive')`)
	auditDeleteExec(t, db, `INSERT INTO package_assignment_inbound_configs VALUES(2,'bob',5,'bob-inbound','vless','{}')`)
	store := &postgresAdminSessionStore{db: db}
	bindings, err := store.LifecyclePackageBindings(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("package preview remaining users=%d (bob owns an inactive assignment)", bindings[0].RemainingUsers)
	err = store.DeleteExclusivePackage(context.Background(), 1, "alice")
	var assignments, configs int
	if scanErr := db.QueryRow(`SELECT COUNT(*) FROM user_package_assignments WHERE username='bob'`).Scan(&assignments); scanErr != nil {
		t.Fatal(scanErr)
	}
	if scanErr := db.QueryRow(`SELECT COUNT(*) FROM package_assignment_inbound_configs WHERE username='bob'`).Scan(&configs); scanErr != nil {
		t.Fatal(scanErr)
	}
	if assignments != 1 || configs != 1 {
		t.Fatalf("UA-D01: deleting alice's package removed bob's assignment/config: assignments=%d configs=%d error=%v", assignments, configs, err)
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
	if first.UserDeleted || first.PendingCount != 1 {
		t.Fatalf("expected a single failed remote item: %+v", first)
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
	plan, err = application.buildDeletionPlan(ctx, "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after partial delete: configs=%d assignments=%d direct_package_valid=%t retry_items=%d", configs, assignments, packageID.Valid, len(plan))
	if err := store.SaveDeletePlan(ctx, "alice", "audit-delete-retry", plan); err != nil {
		t.Fatal(err)
	}
	second := application.executeDeletePlan(ctx, "session", "alice", "audit-delete-retry", plan)
	if second.UserDeleted && findConfigInbound(fixture.configs[5], "assignment-inbound") != nil {
		t.Fatal("UA-D02: retry deleted the user while its original working credential remains on the server")
	}
}

func TestAuditUAD04DeleteRemovesPrivateForwardChainOwnership(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-D04")
	auditDeleteExec(t, db, `CREATE TABLE forward_chain_nodes(node_id bigint PRIMARY KEY,owner_username text,billing_assignment_id bigint)`)
	auditDeleteExec(t, db, `CREATE TABLE nodes(id bigint PRIMARY KEY,username text)`)
	auditDeleteExec(t, db, `INSERT INTO users(username) VALUES('alice')`)
	auditDeleteExec(t, db, `INSERT INTO nodes VALUES(10,'alice')`)
	auditDeleteExec(t, db, `INSERT INTO forward_chain_nodes VALUES(10,'alice',123)`)
	store := &postgresAdminSessionStore{db: db}
	if err := store.FinalizeManagementUserDeletion(context.Background(), "alice", "audit-delete-forward"); err != nil {
		t.Fatal(err)
	}
	var orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM forward_chain_nodes f LEFT JOIN users u ON u.username=f.owner_username LEFT JOIN nodes n ON n.id=f.node_id WHERE u.username IS NULL AND n.id IS NULL`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("UA-D04: private forward chain relation remains after its user and node are deleted: %d", orphans)
	}
}

func TestAuditUAD05DeletePreservesNodeUsedByAnotherUsersPackage(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-D05")
	auditDeleteExec(t, db, `CREATE TABLE nodes(id bigint PRIMARY KEY,username text,original_server text,inbound_tag text,protocol text)`)
	auditDeleteExec(t, db, `INSERT INTO packages VALUES(1,'bob package','[10]')`)
	auditDeleteExec(t, db, `INSERT INTO users(username,package_id) VALUES('alice',NULL),('bob',1)`)
	auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(1,'bob',1,'active')`)
	auditDeleteExec(t, db, `INSERT INTO nodes VALUES(10,'alice','','','vless')`)
	store := &postgresAdminSessionStore{db: db}
	application := &app{adminStore: store}
	ctx := context.Background()
	plan, err := application.buildDeletionPlan(ctx, "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDeletePlan(ctx, "alice", "audit-shared-node", plan); err != nil {
		t.Fatal(err)
	}
	result := application.executeDeletePlan(ctx, "session", "alice", "audit-shared-node", plan)
	if !result.UserDeleted {
		t.Fatalf("fixture deletion did not complete: %+v", result)
	}
	var missingNodes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_package_assignments a JOIN packages p ON p.id=a.package_id CROSS JOIN LATERAL jsonb_array_elements_text(p.nodes::jsonb) member(node_id) LEFT JOIN nodes n ON n.id=member.node_id::bigint WHERE a.username='bob' AND n.id IS NULL`).Scan(&missingNodes); err != nil {
		t.Fatal(err)
	}
	if missingNodes != 0 {
		t.Fatalf("UA-D05: deleting alice removed a node from bob's active package, leaving %d dangling node reference", missingNodes)
	}
}
