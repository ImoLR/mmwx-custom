package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func lifecycleStatusFixture(t *testing.T, fixture *lifecycleAgentFixture, db *sql.DB, updateRuntime func(string, bool)) *[]bool {
	t.Helper()
	var calls []bool
	previous := fixture.extraHandler
	fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3" {
			return previous != nil && previous(w, r)
		}
		var request struct {
			Op      string `json:"op"`
			Payload struct {
				Username string `json:"username"`
				Active   bool   `json:"is_active"`
			} `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Op != "4b18ad3836973389" {
			t.Errorf("unexpected official account API request: %+v, %v", request, err)
			http.Error(w, "invalid status operation", http.StatusBadRequest)
			return true
		}
		calls = append(calls, request.Payload.Active)
		active := 0
		if request.Payload.Active {
			active = 1
		}
		if _, err := db.Exec(`UPDATE users SET is_active=$2 WHERE username=$1`, request.Payload.Username, active); err != nil {
			t.Errorf("fake official account write: %v", err)
			http.Error(w, "status write failed", http.StatusInternalServerError)
			return true
		}
		if updateRuntime != nil {
			updateRuntime(request.Payload.Username, request.Payload.Active)
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "updated"})
		return true
	}
	return &calls
}

func TestAccessOfficialStateRestoresPreviousState(t *testing.T) {
	for _, initiallyActive := range []bool{true, false} {
		t.Run(fmt.Sprintf("initially_active_%v", initiallyActive), func(t *testing.T) {
			db := auditUserDeleteDB(t, "persistent-official-state")
			auditDeleteExec(t, db, `CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamp)`)
			active := 0
			if initiallyActive {
				active = 1
			}
			auditDeleteExec(t, db, `INSERT INTO users(username,is_active) VALUES('alice',$1)`, active)
			auditDeleteExec(t, db, `INSERT INTO remote_servers VALUES(5,'server-5')`)
			credential := map[string]any{"email": "alice__tag", "id": "11111111-1111-4111-8111-111111111111"}
			ref := lifecycleRef(5, "tag", "vless", credential)
			auditDeleteExec(t, db, `INSERT INTO package_assignment_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES('alice',5,'tag','vless',$1)`, ref.CredentialRaw)
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("tag", "vless", credential))})
			defer server.Close()
			calls := lifecycleStatusFixture(t, fixture, db, func(_ string, active bool) {
				inbound := lifecycleInbound("tag", "vless")
				if active {
					inbound = lifecycleInbound("tag", "vless", credential)
				}
				fixture.configs[5] = lifecycleConfig(inbound)
			})
			store := &postgresAdminSessionStore{db: db}
			application := lifecycleTestApp(t, &lifecycleTestStore{}, server)
			application.adminStore = store
			application.apiToken = "test-operator"
			application.trafficGroupsReady = true
			for _, enabled := range []bool{false, true} {
				if enabled && !initiallyActive {
					fixture.configs[5] = lifecycleConfig(lifecycleInbound("tag", "vless"))
				}
				request := httptest.NewRequest(http.MethodPost, "/api/custom/users/alice/access", strings.NewReader(fmt.Sprintf(`{"enabled":%v}`, enabled)))
				request.Header.Set("MM-Authorization", "session")
				request.Header.Set("Authorization", "Bearer test-operator")
				response := httptest.NewRecorder()
				application.userManagementHandler(response, request)
				var body struct {
					Result lifecycleDeleteResult `json:"result"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK || body.Result.PendingCount != 0 {
					t.Fatalf("enable=%v HTTP %d: %s, %v", enabled, response.Code, response.Body.String(), err)
				}
				current, err := store.ManagedUserState(context.Background(), "alice")
				if err != nil || current.IsActive != (enabled && initiallyActive) {
					t.Fatalf("official state enable=%v: %+v, %v", enabled, current, err)
				}
				states, err := store.LifecycleStates(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				previous := states["alice"].OfficialWasActive
				if enabled && previous != nil || !enabled && (previous == nil || *previous != initiallyActive) {
					t.Fatalf("previous official state was not retained/retired: %+v", states["alice"])
				}
			}
			if initiallyActive && (len(*calls) != 2 || (*calls)[0] || !(*calls)[1]) || !initiallyActive && len(*calls) != 0 {
				t.Fatalf("wrong official state changes: %v", *calls)
			}
		})
	}
}

func TestAccessEnableWithoutBackupDoesNotRequireRuntimeCredential(t *testing.T) {
	credential := map[string]any{"email": "alice__new", "id": "11111111-1111-4111-8111-111111111111"}
	ref := lifecycleRef(5, "new", "vless", credential)
	item := analyzeAccessInbound("alice", true, lifecycleConfig(), []lifecycleCredentialRef{ref}, nil)
	if item.Status != lifecycleItemCompleted || item.replacementInbound != nil || len(item.accessCredentials) != 0 {
		t.Fatalf("never-swapped credential needs no runtime restoration: %+v", item)
	}
}
