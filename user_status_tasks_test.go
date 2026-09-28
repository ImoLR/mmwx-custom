package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func startStatusTaskForTest(t *testing.T, application *app, expected bool) managedUserStatusTask {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/custom/user-status-tasks?username=alice&is_active="+strconv.FormatBool(expected), strings.NewReader("encrypted-official-operation"))
	request.Header.Set("MM-Authorization", "admin-session")
	request.Header.Set("X-Secure-Channel", "v2")
	request.Header.Set("X-Session-Id", "secure-session")
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	application.userStatusTaskHandler(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Task managedUserStatusTask `json:"task"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Task
}

func awaitStatusTaskForTest(t *testing.T, application *app, id string) managedUserStatusTask {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := application.userStatusTasks.get(id); ok && task.Status != "pending" {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not finish", id)
	return managedUserStatusTask{}
}

func TestUserStatusTaskReturnsImmediatelyAndConfirmsDatabase(t *testing.T) {
	store := &fakeUserManagementStore{state: managedUserState{Username: "alice", Exists: true, IsActive: true, Role: "user"}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3" || r.Header.Get("X-Session-Id") != "secure-session" || r.Header.Get("MM-Authorization") != "admin-session" {
			t.Errorf("unexpected forwarded request path=%s headers=%v", r.URL.Path, r.Header)
		}
		time.Sleep(120 * time.Millisecond)
		store.setStateActive(false)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	application := &app{adminStore: store, officialInternalTarget: target, userStatusTasks: newManagedUserStatusTaskStore()}

	startedAt := time.Now()
	task := startStatusTaskForTest(t, application, false)
	if elapsed := time.Since(startedAt); elapsed >= 100*time.Millisecond {
		t.Fatalf("start blocked for %s", elapsed)
	}
	finished := awaitStatusTaskForTest(t, application, task.ID)
	if finished.Status != "succeeded" || finished.UpstreamStatus != http.StatusOK || finished.ConfirmedActive == nil || *finished.ConfirmedActive {
		t.Fatalf("unexpected task: %#v", finished)
	}
}

func TestUserStatusTaskReportsUpstream502WithoutChangingDatabase(t *testing.T) {
	store := &fakeUserManagementStore{state: managedUserState{Username: "alice", Exists: true, IsActive: true, Role: "user"}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>large Cloudflare error must not escape</html>"))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	application := &app{adminStore: store, officialInternalTarget: target, userStatusTasks: newManagedUserStatusTaskStore()}

	finished := awaitStatusTaskForTest(t, application, startStatusTaskForTest(t, application, false).ID)
	if finished.Status != "failed" || finished.UpstreamStatus != http.StatusBadGateway {
		t.Fatalf("unexpected task: %#v", finished)
	}
	if strings.Contains(finished.Message, "<html>") || len(finished.Message) > 160 {
		t.Fatalf("unsafe error escaped: %q", finished.Message)
	}
}

func TestUserStatusTaskIsolatedPostgresComplexProfile(t *testing.T) {
	dsn := os.Getenv("MMWXC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MMWXC_TEST_POSTGRES_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE users (username text PRIMARY KEY, role text NOT NULL, is_active bigint NOT NULL)`,
		`CREATE TABLE sessions (token text PRIMARY KEY, username text NOT NULL, expires_at timestamp NOT NULL)`,
		`CREATE TABLE packages (id bigint PRIMARY KEY, name text NOT NULL, nodes text NOT NULL)`,
		`CREATE TABLE remote_servers (id bigint PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE user_inbound_configs (id bigint PRIMARY KEY, username text NOT NULL, server_id bigint NOT NULL, inbound_tag text NOT NULL)`,
		`CREATE TABLE user_subaccounts (id bigint PRIMARY KEY, username text NOT NULL, routed_node_id bigint NOT NULL)`,
		`CREATE TABLE nodes (id bigint PRIMARY KEY, username text NOT NULL, original_server text NOT NULL)`,
		`INSERT INTO users VALUES ('admin', 'admin', 1), ('alice', 'user', 1)`,
		`INSERT INTO sessions VALUES ('admin-session', 'admin', CURRENT_TIMESTAMP + interval '1 hour')`,
		`INSERT INTO packages VALUES (11, 'synthetic complex package', '[65,73,74,75]')`,
		`INSERT INTO remote_servers VALUES (5, 'synthetic-five'), (15, 'synthetic-fifteen')`,
		`INSERT INTO user_inbound_configs VALUES
			(31, 'alice', 5, 'synthetic-10016'),
			(33, 'alice', 5, 'synthetic-10017'),
			(34, 'alice', 15, 'synthetic-13002')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("isolated schema: %v", err)
		}
	}
	store := &postgresAdminSessionStore{db: db}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var servers int
		if err := db.QueryRow(`SELECT COUNT(DISTINCT server_id) FROM user_inbound_configs WHERE username='alice'`).Scan(&servers); err != nil || servers != 2 {
			http.Error(w, "invalid synthetic relation profile", http.StatusInternalServerError)
			return
		}
		// Model the real slow path: three bindings on two server transactions.
		time.Sleep(time.Duration(servers) * 60 * time.Millisecond)
		_, _ = db.Exec(`UPDATE users SET is_active=0 WHERE username='alice'`)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	application := &app{adminStore: store, officialInternalTarget: target, userStatusTasks: newManagedUserStatusTaskStore()}

	finished := awaitStatusTaskForTest(t, application, startStatusTaskForTest(t, application, false).ID)
	if finished.Status != "succeeded" || finished.DurationMS < 100 {
		t.Fatalf("slow complex task was not confirmed: %#v", finished)
	}
	state, err := store.ManagedUserState(context.Background(), "alice")
	if err != nil || state.IsActive {
		t.Fatalf("unexpected final database state: %#v err=%v", state, err)
	}
}
