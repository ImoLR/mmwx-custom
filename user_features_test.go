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
