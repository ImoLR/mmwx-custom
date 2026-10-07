package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOfficialAssignmentDeleteUsesJSONBody(t *testing.T) {
	fixture, server := newLifecycleAgentFixture(nil)
	defer server.Close()
	called := false
	fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/admin/package-assignments" {
			return false
		}
		called = true
		var body struct {
			Username     string `json:"username"`
			AssignmentID int64  `json:"assignment_id"`
		}
		if r.Method != http.MethodDelete || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Username != "alice" || body.AssignmentID != 7 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return true
	}
	target, _ := url.Parse(server.URL)
	application := &app{officialInternalTarget: target}
	if err := application.officialLifecycleJSON(context.Background(), "operator-session", http.MethodDelete, "/api/admin/package-assignments", map[string]any{"username": "alice", "assignment_id": 7}, nil); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("assignment deletion was not called")
	}
}

func TestUserFeatureLifecycleGuard(t *testing.T) {
	for _, state := range []string{"disabled", "partially_enabled", "partially_disabled", "enabling", "disabling"} {
		t.Run(state, func(t *testing.T) {
			current := managedUserLifecycle{EffectiveState: state}
			if err := userFeatureLifecycleError(current, true, false); err == nil || err.Error() != disabledUserFeatureWarning {
				t.Fatalf("credential write without confirmation = %v", err)
			}
			if err := userFeatureLifecycleError(current, true, true); err != nil {
				t.Fatalf("confirmed credential write = %v", err)
			}
			if err := userFeatureLifecycleError(current, false, false); err != nil {
				t.Fatalf("non-credential write = %v", err)
			}
		})
	}
	for _, state := range []string{"deleting", "delete_partial", "deleted"} {
		for _, credentials := range []bool{false, true} {
			if err := userFeatureLifecycleError(managedUserLifecycle{EffectiveState: state}, credentials, true); err == nil {
				t.Fatalf("%s allowed write after confirmation", state)
			}
		}
	}
	if err := userFeatureLifecycleError(managedUserLifecycle{DesiredState: "disabled", EffectiveState: "conflict"}, true, false); err == nil {
		t.Fatal("disabled intent bypassed confirmation")
	}
	if err := userFeatureLifecycleError(managedUserLifecycle{EffectiveState: "enabled"}, true, false); err != nil {
		t.Fatal(err)
	}
}

func TestManagedUserFeatureValidation(t *testing.T) {
	for _, days := range []int{1, 17, 3650} {
		if err := validateManagedUserFeature("renew", managedUserFeatureRequest{Days: days}); err != nil {
			t.Fatalf("days %d: %v", days, err)
		}
	}
	for _, days := range []int{-1, 0, 3651} {
		if err := validateManagedUserFeature("renew", managedUserFeatureRequest{Days: days}); err == nil {
			t.Fatalf("accepted days %d", days)
		}
	}
	for _, raw := range []string{"null", "0", "0.25", "1024"} {
		if err := validateManagedUserFeature("edit-assignment", managedUserFeatureRequest{AssignmentID: 1, TrafficLimitOverrideGB: json.RawMessage(raw)}); err != nil {
			t.Fatalf("override %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"-1", `"0"`, "true", "[]", "1e100"} {
		if err := validateManagedUserFeature("edit-assignment", managedUserFeatureRequest{AssignmentID: 1, TrafficLimitOverrideGB: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted override %s", raw)
		}
	}
	if err := validateManagedUserFeature("assign-package", managedUserFeatureRequest{PackageID: 1, ExpireDate: "2026-02-30"}); err == nil {
		t.Fatal("accepted invalid date")
	}
}

func TestManagedUserPackageEligibilityPostgres(t *testing.T) {
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
	schema := fmt.Sprintf("mmwxc_feature_binding_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE users(username text PRIMARY KEY,package_id bigint)`,
		`CREATE TABLE packages(id bigint PRIMARY KEY)`,
		`CREATE TABLE user_package_assignments(id bigint PRIMARY KEY,username text,package_id bigint,status text)`,
		`INSERT INTO packages VALUES(1),(2),(3),(4),(5)`,
		`INSERT INTO users VALUES('alice',1),('bob',2)`,
		`INSERT INTO user_package_assignments VALUES(1,'alice',1,'active'),(2,'bob',3,'inactive'),(3,'bob',4,'revoked')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	store := &postgresAdminSessionStore{db: db}
	ids, err := store.availableManagedUserPackages(ctx, "alice")
	if err != nil || !reflect.DeepEqual(ids, []int64{1, 5}) {
		t.Fatalf("eligible IDs %v, err %v", ids, err)
	}
	for _, id := range []int64{1, 5} {
		if err := managedUserPackageOwnerCheck(ctx, db, "alice", id); err != nil {
			t.Fatalf("own/unbound package %d rejected: %v", id, err)
		}
	}
	for _, id := range []int64{2, 3, 4} {
		if err := managedUserPackageOwnerCheck(ctx, db, "alice", id); err == nil || !strings.Contains(err.Error(), "一个套餐只能绑定一个用户") {
			t.Fatalf("other user's package %d: %v", id, err)
		}
	}
	if err := managedUserPackageOwnerCheck(ctx, db, "alice", 999); err == nil {
		t.Fatal("missing package accepted")
	}
}

func TestExpiredManagedUserPackagesPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "expired_packages")
	for _, statement := range []string{
		`ALTER TABLE users ADD COLUMN last_package_id bigint, ADD COLUMN last_package_end_date timestamp without time zone`,
		`INSERT INTO packages(id,name) VALUES(1,'free'),(2,'legacy owned'),(3,'assignment owned'),(4,'admin package')`,
		`INSERT INTO users(username,role,package_id,last_package_id,last_package_end_date) VALUES
		('free','user',NULL,1,'2026-08-25 00:00:00'),('legacy','user',NULL,2,'2026-08-25'),
		('assigned','user',NULL,3,'2026-08-25'),('deleted','user',NULL,999,'2026-08-25'),
		('admin','admin',NULL,4,'2026-08-25'),('never','user',NULL,NULL,NULL),
		('current','user',4,2,'2026-08-25'),('owner','user',2,NULL,NULL),
		('has_assignment','user',NULL,1,'2026-08-25'),('no_date','user',NULL,4,NULL),
		('no_id','user',NULL,NULL,'2026-08-25')`,
		`INSERT INTO user_package_assignments VALUES(1,'owner',3,'revoked'),(2,'has_assignment',4,'inactive')`,
		`SET default_transaction_read_only=on`,
	} {
		auditDeleteExec(t, db, statement)
	}
	defer db.Exec(`SET default_transaction_read_only=off`)
	store := &postgresAdminSessionStore{db: db}
	items, err := store.expiredManagedUserPackages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 7 {
		t.Fatalf("unexpected histories: %+v", items)
	}
	for _, name := range []string{"never", "current", "has_assignment"} {
		if _, ok := items[name]; ok {
			t.Fatalf("included bound/never-bound user %s", name)
		}
	}
	free := items["free"]
	if !free.Rebindable || free.Reason != "" || free.LastPackageID == nil || *free.LastPackageID != 1 || free.LastPackageName == nil || *free.LastPackageName != "free" || free.LastPackageEndDate == nil || *free.LastPackageEndDate != "2026-08-25T00:00:00" {
		t.Fatalf("free package history: %+v", free)
	}
	for name, reason := range map[string]string{"legacy": "已绑定其他用户", "assigned": "已绑定其他用户", "deleted": "已删除或不存在", "admin": "管理员不适用", "no_date": "没有上次套餐到期记录", "no_id": "已删除或不存在"} {
		if item := items[name]; item.Rebindable || !strings.Contains(item.Reason, reason) {
			t.Errorf("%s: %+v", name, item)
		}
	}
	if items["deleted"].LastPackageName != nil || items["admin"].Role != "admin" {
		t.Fatal("deleted/admin metadata incorrect")
	}
	application := &app{adminStore: store, apiToken: "operator"}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/user-lifecycle", nil)
	response := httptest.NewRecorder()
	application.userLifecycleIndexHandler(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated history: %d", response.Code)
	}
	request.Header.Set("Authorization", "Bearer operator")
	response = httptest.NewRecorder()
	application.userLifecycleIndexHandler(response, request)
	var result struct {
		ExpiredPackages      map[string]managedUserExpiredPackage `json:"expired_packages"`
		ExpiredPackagesError string                               `json:"expired_packages_error"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || !reflect.DeepEqual(result.ExpiredPackages, items) || result.ExpiredPackagesError != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("history response: %d %s", response.Code, response.Body.String())
	}
}

func TestUserLifecycleIndexPreservesStatesWhenExpiredPackagesFailPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "expired_packages_failure")
	auditDeleteExec(t, db, `INSERT INTO users(username,role) VALUES('alice','user')`)
	auditDeleteExec(t, db, `INSERT INTO mmwxc_user_lifecycle(username,desired_state,effective_state,operation) VALUES('alice','disabled','disabled','disable')`)
	store := &postgresAdminSessionStore{db: db}
	if _, err := store.expiredManagedUserPackages(context.Background()); err == nil || !strings.Contains(err.Error(), "last_package_") {
		t.Fatalf("expected missing history column: %v", err)
	}
	states, err := store.LifecycleStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	application := &app{adminStore: store, apiToken: "operator"}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/user-lifecycle", nil)
	request.Header.Set("Authorization", "Bearer operator")
	response := httptest.NewRecorder()
	application.userLifecycleIndexHandler(response, request)
	var result struct {
		Success              bool                                 `json:"success"`
		Users                map[string]managedUserLifecycle      `json:"users"`
		ExpiredPackages      map[string]managedUserExpiredPackage `json:"expired_packages"`
		ExpiredPackagesError string                               `json:"expired_packages_error"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || !result.Success || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("lifecycle response: %d %s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(result.Users, states) || result.ExpiredPackages == nil || len(result.ExpiredPackages) != 0 || result.ExpiredPackagesError != "读取上次套餐失败" {
		t.Fatalf("history failure lost lifecycle state or notice: %s", response.Body.String())
	}
}

func TestDisabledExpiredUserAssignPackageRemainsBlockedPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "expired_disabled_rebind")
	for _, statement := range []string{
		`ALTER TABLE remote_servers ADD COLUMN xray_mode text DEFAULT 'external'`,
		`ALTER TABLE users ADD COLUMN package_end_date timestamp, ADD COLUMN last_package_id bigint, ADD COLUMN last_package_end_date timestamp`,
		`CREATE TABLE nodes(id bigint PRIMARY KEY,node_name text,username text,original_server text,inbound_tag text,protocol text)`,
		`CREATE TABLE user_inbound_configs(username text,server_id bigint,inbound_tag text,protocol text,credential_json text)`,
		`CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamptz DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO packages VALUES(3,'last package','[1]')`,
		`INSERT INTO users(username,role,package_id) VALUES('alice','user',3),('admin','admin',NULL)`,
		`INSERT INTO remote_servers VALUES(1,'supported','external')`,
		`INSERT INTO nodes VALUES(1,'node','admin','supported','tag','vless')`,
		`INSERT INTO user_inbound_configs VALUES('alice',1,'tag','vless','{"email":"alice__tag","id":"alice-id"}'),('admin',1,'tag','vless','{"email":"admin__tag","id":"admin-id"}')`,
		`INSERT INTO mmwxc_user_lifecycle(username,desired_state,effective_state,operation,official_was_active) VALUES('alice','disabled','disabled','disable',true)`,
	} {
		auditDeleteExec(t, db, statement)
	}
	config := lifecycleConfig(lifecycleInbound("tag", "vless", map[string]any{"email": "alice__tag", "id": "alice-id"}, map[string]any{"email": "admin__tag", "id": "admin-id"}))
	raw, _ := json.Marshal(config)
	auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots VALUES(1,1,$1,'current',CURRENT_TIMESTAMP)`, string(raw))
	store := &postgresAdminSessionStore{db: db}
	application := &app{adminStore: store, apiToken: "operator", trafficGroupsReady: true, detailedConnections: map[string]serverDetailedConnectionRecord{"1": persistentTestRecord()}}
	ctx := context.Background()
	check := func() {
		t.Helper()
		got := application.trafficBlocksForHelper(defaultServerConnectionSettings(), "1", "v0.6.8")
		if got.BlockedIdentities == nil || !reflect.DeepEqual(*got.BlockedIdentities, []serverConnectionIdentity{{InboundTag: "tag", User: "alice__tag"}}) {
			t.Fatalf("disabled identity lost or admin blocked: %+v", got.BlockedIdentities)
		}
	}
	if err := application.refreshDisabledUsers(ctx); err != nil {
		t.Fatal(err)
	}
	check()
	auditDeleteExec(t, db, `UPDATE users SET package_id=NULL,package_end_date=NULL,last_package_id=3,last_package_end_date='2026-08-25',is_active=0 WHERE username='alice'`)
	auditDeleteExec(t, db, `UPDATE server_xray_config_snapshots SET config_json='{"inbounds":[]}'`)
	if err := application.refreshDisabledUsers(ctx); err != nil {
		t.Fatal(err)
	}
	check()
	// Official writes use a separate connection while Custom holds its package lock.
	var schema string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	dsn, _ := url.Parse(os.Getenv("MMWXC_TEST_POSTGRES_DSN"))
	params := dsn.Query()
	params.Set("search_path", schema)
	dsn.RawQuery = params.Encode()
	officialDB, err := sql.Open("pgx", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	defer officialDB.Close()
	fixture, server := newLifecycleAgentFixture(nil)
	defer server.Close()
	application.officialInternalTarget, _ = url.Parse(server.URL)
	calls := 0
	fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/admin/packages/assign" {
			return false
		}
		calls++
		var body map[string]any
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || body["username"] != "alice" || body["package_id"] != float64(3) || body["expire_date"] != "2026-11-06" || body["confirm_disabled"] != nil {
			t.Errorf("wrong official bind payload: %+v", body)
		}
		if _, err := officialDB.Exec(`UPDATE users SET package_id=3,package_end_date='2026-11-06',is_active=1 WHERE username='alice'`); err != nil {
			t.Error(err)
		}
		if _, err := officialDB.Exec(`UPDATE server_xray_config_snapshots SET config_json=$1`, string(raw)); err != nil {
			t.Error(err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return true
	}
	for _, confirm := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodPost, "/api/custom/users/alice/features/assign-package", strings.NewReader(fmt.Sprintf(`{"package_id":3,"start_date":"2026-10-07","expire_date":"2026-11-06","permanent":false,"is_reset":true,"reset_day":1,"inherit_expire_date":false,"inherit_traffic":false,"confirm_disabled":%t}`, confirm)))
		request.Header.Set("MM-Authorization", "operator-session")
		request.Header.Set("Authorization", "Bearer operator")
		response := httptest.NewRecorder()
		application.userManagementHandler(response, request)
		want := http.StatusConflict
		if confirm {
			want = http.StatusOK
		}
		if response.Code != want {
			t.Fatalf("confirm=%t: %d %s", confirm, response.Code, response.Body.String())
		}
		check() // Existing blocks survive immediately, before periodic reconciliation.
	}
	if calls != 1 {
		t.Fatalf("official binds: %d", calls)
	}
	if err := application.refreshDisabledUsers(ctx); err != nil {
		t.Fatal(err)
	}
	check()
	states, err := store.LifecycleStates(ctx)
	if err != nil || states["alice"].DesiredState != lifecycleStateDisabled || states["alice"].EffectiveState != lifecycleStateDisabled {
		t.Fatalf("disabled intent changed: %+v, %v", states, err)
	}
}
