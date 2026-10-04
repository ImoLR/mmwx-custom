package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in audit regressions assert the desired behaviour and fail on the audited
// implementation. The fixtures contain synthetic credentials only.
func auditUserAccessRepro(t *testing.T, id string) {
	t.Helper()
	if os.Getenv("MMWXC_AUDIT_USER_ACCESS") != "1" {
		t.Skip("audit: " + id)
	}
}

func auditAccessRun(t *testing.T, application *app, store *lifecycleTestStore, enable bool) lifecycleDeleteResult {
	t.Helper()
	operation := lifecycleOperationDisable
	if enable {
		operation = lifecycleOperationEnable
	}
	plan, err := application.buildAccessPlan(context.Background(), "session", "alice", enable)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccessPlan(context.Background(), "alice", operation, operation, plan); err != nil {
		t.Fatal(err)
	}
	return application.executeAccessPlan(context.Background(), "session", "alice", operation, operation, plan)
}

func TestAuditUA_A01_DisablePreservesOtherUsersSharedCredential(t *testing.T) {
	for _, source := range []string{"user_inbound_configs", "user_subaccounts", "package_assignment_inbound_configs", "package_assignment_subaccounts", "user_outbounds", "admin_default"} {
		t.Run(source, func(t *testing.T) {
			shared := map[string]any{"email": "shared-in", "id": "11111111-1111-4111-8111-111111111111"}
			aliceRef := lifecycleRef(5, "shared", "vless", shared)
			aliceRef.Username = "alice"
			bobRef := aliceRef
			bobRef.Username, bobRef.Source = "bob", source
			if source == "user_outbounds" {
				bobRef.CredentialRaw = ""
			}
			store := &lifecycleTestStore{refs: []lifecycleCredentialRef{aliceRef}}
			if source == "admin_default" {
				store.defaults = map[string][]map[string]any{"5/shared": {shared}}
			} else {
				store.businessRefs = map[string][]lifecycleCredentialRef{"5/shared": {bobRef}}
			}
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("shared", "vless", shared))})
			defer server.Close()
			application := lifecycleTestApp(t, store, server)
			result := auditAccessRun(t, application, store, false)
			if result.PendingCount != 1 || result.State != lifecycleStatePartiallyDisabled || result.Items[0].Action != lifecycleActionConflict ||
				result.LastError == "" || len(fixture.actions) != 0 || len(store.backups) != 0 {
				t.Fatalf("shared credential was not refused before persistence or dispatch: %+v actions=%d backups=%d", result, len(fixture.actions), len(store.backups))
			}
			entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "shared"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if credentialsMatch(entry, shared, "vless") {
					return
				}
			}
			t.Fatalf("UA-A01: disabling alice removed the credential also used by %s; state=%s pending=%d", source, result.State, result.PendingCount)
		})
	}
}

func TestAuditUA_A02_EnableThenCredentialRotationDoesNotPoisonFutureAccess(t *testing.T) {
	for _, change := range []string{"primary", "metadata", "removed_inbound"} {
		t.Run(change, func(t *testing.T) {
			original := map[string]any{"email": "alice-in", "id": "11111111-1111-4111-8111-111111111111"}
			store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "tag", "vless", original)}}
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("tag", "vless", original))})
			defer server.Close()
			application := lifecycleTestApp(t, store, server)
			for _, enable := range []bool{false, true} {
				if result := auditAccessRun(t, application, store, enable); result.PendingCount != 0 {
					t.Fatalf("setup access enable=%v pending=%d", enable, result.PendingCount)
				}
			}
			if len(store.backups) != 0 {
				t.Fatal("successful enable did not retire its credential backups")
			}
			changed := cloneLifecycleMap(original)
			switch change {
			case "primary":
				changed["id"] = "22222222-2222-4222-8222-222222222222"
			case "metadata":
				changed["level"] = float64(2)
			case "removed_inbound":
				store.refs = nil
				fixture.configs[5] = lifecycleConfig()
			}
			if change != "removed_inbound" {
				store.refs = []lifecycleCredentialRef{lifecycleRef(5, "tag", "vless", changed)}
				fixture.configs[5] = lifecycleConfig(lifecycleInbound("tag", "vless", changed))
			}
			plan, err := application.buildAccessPlan(context.Background(), "session", "alice", false)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range plan {
				if item.Status == lifecycleItemFailed {
					t.Fatalf("UA-A02: completed enable left a stale backup blocking the next disable after %s: %s", change, item.LastError)
				}
			}
		})
	}
}

func TestAccessSuccessfulEnableRetiresOnlyItsBackupsPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-A02")
	auditDeleteExec(t, db, `INSERT INTO users(username) VALUES('alice'),('bob')`)
	store := &postgresAdminSessionStore{db: db}
	ctx := context.Background()
	for _, username := range []string{"alice", "bob"} {
		credential := map[string]any{"id": username + "-secret", "email": username + "-in"}
		item := analyzeAccessInbound(username, false, lifecycleConfig(lifecycleInbound("tag", "vless", credential)),
			[]lifecycleCredentialRef{lifecycleRef(5, "tag", "vless", credential)}, nil)
		if item.Status != lifecycleItemPending {
			t.Fatalf("disable setup: %+v", item)
		}
		if err := store.SaveAccessPlan(ctx, username, lifecycleOperationDisable, "disable-"+username, []lifecyclePlanItem{item}); err != nil {
			t.Fatal(err)
		}
	}
	for _, pending := range []int{1, 0} {
		if err := store.FinishAccessAttempt(ctx, "alice", "disable-alice", lifecycleOperationEnable, pending, ""); err != nil {
			t.Fatal(err)
		}
		for _, username := range []string{"alice", "bob"} {
			backups, err := store.LifecycleDisabledCredentials(ctx, username)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if username == "alice" && pending == 0 {
				want = 0
			}
			if len(backups) != want {
				t.Fatalf("enable pending=%d left %d backups for %s, want %d", pending, len(backups), username, want)
			}
		}
	}
}

func TestAccessDistinctCredentialsRemainIndependent(t *testing.T) {
	alice := map[string]any{"email": "alice-in", "id": "11111111-1111-4111-8111-111111111111"}
	bob := map[string]any{"email": "bob-in", "id": "22222222-2222-4222-8222-222222222222"}
	admin := map[string]any{"email": "admin-in", "id": "33333333-3333-4333-8333-333333333333"}
	bobRef := lifecycleRef(5, "shared", "vless", bob)
	bobRef.Username = "bob"
	store := &lifecycleTestStore{
		refs:         []lifecycleCredentialRef{lifecycleRef(5, "shared", "vless", alice)},
		businessRefs: map[string][]lifecycleCredentialRef{"5/shared": {bobRef}},
		defaults:     map[string][]map[string]any{"5/shared": {admin}},
	}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("shared", "vless", alice, bob, admin))})
	defer server.Close()
	result := auditAccessRun(t, lifecycleTestApp(t, store, server), store, false)
	if result.PendingCount != 0 || result.State != lifecycleStateDisabled || len(fixture.actions) != 1 {
		t.Fatalf("independent credentials should permit disabling only alice: %+v", result)
	}
	entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "shared"))
	if err != nil || len(entries) != 3 || credentialsMatch(entries[0], alice, "vless") || hashJSON(entries[1]) != hashJSON(bob) || hashJSON(entries[2]) != hashJSON(admin) {
		t.Fatal("disabling alice changed another user's distinct credential")
	}
}

func TestAuditUA_A03_EnableAfterNewNodeDoesNotRequireNeverCreatedBackup(t *testing.T) {
	auditUserAccessRepro(t, "UA-A03")
	for _, sameInbound := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_inbound_%v", sameInbound), func(t *testing.T) {
			original := map[string]any{"email": "alice-old", "id": "11111111-1111-4111-8111-111111111111"}
			added := map[string]any{"email": "alice-new", "id": "22222222-2222-4222-8222-222222222222"}
			store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "old", "vless", original)}}
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("old", "vless", original))})
			defer server.Close()
			application := lifecycleTestApp(t, store, server)
			if result := auditAccessRun(t, application, store, false); result.PendingCount != 0 {
				t.Fatal("setup disable failed")
			}
			tag := "new"
			if sameInbound {
				tag = "old"
				inbound := findConfigInbound(fixture.configs[5], tag)
				settings := inbound["settings"].(map[string]any)
				settings["clients"] = append(settings["clients"].([]any), added)
			} else {
				fixture.configs[5]["inbounds"] = append(fixture.configs[5]["inbounds"].([]any), lifecycleInbound(tag, "vless", added))
			}
			store.refs = append(store.refs, lifecycleRef(5, tag, "vless", added))
			plan, err := application.buildAccessPlan(context.Background(), "session", "alice", true)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range plan {
				if item.Status == lifecycleItemFailed {
					t.Fatalf("UA-A03: enabling a disabled user with a newly assigned credential cannot finish: %s", item.LastError)
				}
			}
		})
	}
}

func TestAuditUA_A04_ConcurrentUsersCannotRestoreEachOthersCredential(t *testing.T) {
	auditUserAccessRepro(t, "UA-A04")
	alice := map[string]any{"email": "alice-in", "id": "11111111-1111-4111-8111-111111111111"}
	bob := map[string]any{"email": "bob-in", "id": "22222222-2222-4222-8222-222222222222"}
	config := lifecycleConfig(lifecycleInbound("shared", "vless", alice, bob))
	aliceItem := analyzeAccessInbound("alice", false, config, []lifecycleCredentialRef{lifecycleRef(5, "shared", "vless", alice)}, nil)
	bobItem := analyzeAccessInbound("bob", false, config, []lifecycleCredentialRef{lifecycleRef(5, "shared", "vless", bob)}, nil)
	fixture, unusedServer := newLifecycleAgentFixture(map[int64]map[string]any{5: config})
	defer unusedServer.Close()
	prechecked := []chan struct{}{make(chan struct{}), make(chan struct{})}
	release := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var mu sync.Mutex
	checks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/admin/remote/xray/config" {
			mu.Lock()
			index := checks
			checks++
			mu.Unlock()
			if index < 2 {
				response := httptest.NewRecorder()
				fixture.serveHTTP(response, r)
				close(prechecked[index])
				select {
				case <-release[index]:
				case <-r.Context().Done():
					return
				}
				for key, values := range response.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(response.Code)
				_, _ = w.Write(response.Body.Bytes())
				return
			}
		}
		fixture.serveHTTP(w, r)
	}))
	defer server.Close()
	application := lifecycleTestApp(t, &lifecycleTestStore{}, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := func(username string, item *lifecyclePlanItem) <-chan error {
		done := make(chan error, 1)
		go func() {
			unlock := application.lockUserLifecycle(username)
			defer unlock()
			done <- application.executeAccessItem(ctx, "session", item)
		}()
		return done
	}
	waitCheck := func(index int) {
		select {
		case <-prechecked[index]:
		case <-ctx.Done():
			t.Fatal("concurrent preflight did not arrive")
		}
	}
	aliceDone := start("alice", &aliceItem)
	waitCheck(0)
	bobDone := start("bob", &bobItem)
	waitCheck(1)
	close(release[0])
	if err := <-aliceDone; err != nil {
		t.Fatalf("alice operation failed: %v", err)
	}
	close(release[1])
	if err := <-bobDone; err != nil {
		t.Fatalf("bob operation failed: %v", err)
	}
	entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "shared"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if credentialsMatch(entry, alice, "vless") || credentialsMatch(entry, bob, "vless") {
			t.Fatal("UA-A04: both operations returned success, but the later whole-inbound replacement restored the earlier user's original credential")
		}
	}
}

func TestAuditUA_A05_SocksNoAuthCannotReportSuccessfulDisable(t *testing.T) {
	for _, auth := range []string{"noauth", "", "missing"} {
		t.Run(auth, func(t *testing.T) {
			account := map[string]any{"user": "alice", "pass": "synthetic-secret"}
			inbound := map[string]any{"tag": "socks", "protocol": "socks", "settings": map[string]any{"auth": auth, "accounts": []any{account}}}
			if auth == "missing" {
				delete(inbound["settings"].(map[string]any), "auth")
			}
			item := analyzeAccessInbound("alice", false, lifecycleConfig(inbound), []lifecycleCredentialRef{lifecycleRef(5, "socks", "socks", account)}, nil)
			if item.Status != lifecycleItemFailed || !strings.Contains(item.LastError, "匿名 SOCKS") || item.replacementInbound != nil || len(item.accessCredentials) != 0 {
				t.Fatalf("UA-A05: anonymous SOCKS must be refused with a clear reason and no replacement: %+v", item)
			}
		})
	}
}

func TestAuditUA_A06_SingleSecretShadowsocksCanBeDisabled(t *testing.T) {
	for _, method := range []string{"aes-128-gcm", "2022-blake3-aes-128-gcm"} {
		t.Run(method, func(t *testing.T) {
			credential := map[string]any{"password": "c3ludGhldGljLWtleS0xNg==", "email": "alice-in"}
			inbound := map[string]any{"tag": "single", "protocol": "shadowsocks", "settings": map[string]any{
				"method": method, "password": credential["password"], "email": credential["email"],
			}}
			store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "single", "shadowsocks", credential)}}
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(inbound)})
			defer server.Close()
			application := lifecycleTestApp(t, store, server)
			for _, enable := range []bool{false, true} {
				result := auditAccessRun(t, application, store, enable)
				if result.PendingCount != 0 {
					t.Fatalf("UA-A06: valid single-secret Shadowsocks access change failed: %+v", result)
				}
				settings := findConfigInbound(fixture.configs[5], "single")["settings"].(map[string]any)
				if (settings["password"] == credential["password"]) != enable || settings["email"] != credential["email"] || settings["method"] != method {
					t.Fatalf("single password or unchanged settings incorrect after enable=%v", enable)
				}
				if _, exists := settings["clients"]; exists {
					t.Fatal("single-password replacement created a clients array")
				}
			}
		})
	}
}

func TestAccessSingleSecretShadowsocksRefusesSharedInbound(t *testing.T) {
	for _, method := range []string{"aes-128-gcm", "2022-blake3-aes-128-gcm"} {
		for _, owner := range []string{"bob", "admin_default"} {
			t.Run(method+"/"+owner, func(t *testing.T) {
				credential := map[string]any{"password": "c3ludGhldGljLWtleS0xNg==", "email": "alice-in"}
				inbound := map[string]any{"tag": "single", "protocol": "shadowsocks", "settings": map[string]any{
					"method": method, "password": credential["password"], "email": credential["email"],
				}}
				ref := lifecycleRef(5, "single", "shadowsocks", credential)
				store := &lifecycleTestStore{refs: []lifecycleCredentialRef{ref}}
				if owner == "admin_default" {
					store.defaults = map[string][]map[string]any{"5/single": {credential}}
				} else {
					ref.Username = owner
					store.businessRefs = map[string][]lifecycleCredentialRef{"5/single": {ref}}
				}
				fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(inbound)})
				defer server.Close()
				result := auditAccessRun(t, lifecycleTestApp(t, store, server), store, false)
				if result.PendingCount != 1 || result.Items[0].Action != lifecycleActionConflict || result.LastError == "" || len(fixture.actions) != 0 || len(store.backups) != 0 {
					t.Fatalf("shared single-password inbound was not safely refused: %+v", result)
				}
			})
		}
	}
}

type auditAccessStateStore struct {
	*lifecycleTestStore
	state managedUserLifecycle
}

func (s *auditAccessStateStore) LifecycleStates(context.Context) (map[string]managedUserLifecycle, error) {
	return map[string]managedUserLifecycle{"alice": s.state}, nil
}

func (s *auditAccessStateStore) FinishAccessAttempt(ctx context.Context, username, operationID, operation string, pending int, message string) error {
	desired, _, partial := accessLifecycleStates(operation)
	s.state = managedUserLifecycle{Username: username, DesiredState: desired, EffectiveState: desired, Operation: operation, PendingCount: pending}
	if pending > 0 {
		s.state.EffectiveState = partial
	}
	return s.lifecycleTestStore.FinishAccessAttempt(ctx, username, operationID, operation, pending, message)
}

func TestAuditUA_A07_AccessMustNotOverwritePartialDeletion(t *testing.T) {
	for _, state := range []string{lifecycleStateDeleting, lifecycleStateDeletePartial} {
		for _, enable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enable_%v", state, enable), func(t *testing.T) {
				store := &auditAccessStateStore{lifecycleTestStore: &lifecycleTestStore{}, state: managedUserLifecycle{
					Username: "alice", DesiredState: lifecycleStateDeleted, EffectiveState: state, Operation: "delete", PendingCount: 1,
				}}
				application := &app{adminStore: store}
				request := httptest.NewRequest(http.MethodPost, "/api/custom/users/alice/access", strings.NewReader(fmt.Sprintf(`{"enabled":%v}`, enable)))
				request.Header.Set("MM-Authorization", "admin-session")
				response := httptest.NewRecorder()
				application.userManagementHandler(response, request)
				if response.Code != http.StatusConflict || store.state.EffectiveState != state || len(store.savedPlans) != 0 || !strings.Contains(response.Body.String(), "删除") {
					t.Fatalf("UA-A07: a stale tab/API access request did not preserve deletion: HTTP=%d state=%s body=%s", response.Code, store.state.EffectiveState, response.Body.String())
				}
			})
		}
	}
}

func TestAuditUA_A08_RuntimeRepushCannotLeaveDisabledStateWithLiveOriginal(t *testing.T) {
	auditUserAccessRepro(t, "UA-A08")
	original := map[string]any{"email": "alice-in", "id": "11111111-1111-4111-8111-111111111111"}
	base := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "tag", "vless", original)}}
	store := &auditAccessStateStore{lifecycleTestStore: base}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("tag", "vless", original))})
	defer server.Close()
	application := lifecycleTestApp(t, base, server)
	application.adminStore = store
	if result := auditAccessRun(t, application, base, false); result.PendingCount != 0 {
		t.Fatal("setup disable failed")
	}
	// Model an external writer restoring the official credential; this is not a
	// claim about which closed-source official action performs that write.
	fixture.configs[5] = lifecycleConfig(lifecycleInbound("tag", "vless", original))
	request := httptest.NewRequest(http.MethodGet, "/api/custom/users/lifecycle", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	response := httptest.NewRecorder()
	application.userLifecycleIndexHandler(response, request)
	var body struct {
		Users map[string]managedUserLifecycle `json:"users"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "tag"))
	if err != nil {
		t.Fatal(err)
	}
	state := body.Users["alice"]
	if state.EffectiveState == lifecycleStateDisabled && len(entries) == 1 && credentialsMatch(entries[0], original, "vless") {
		t.Fatal("UA-A08: lifecycle refresh still reports disabled while the original credential is present in the current inbound")
	}
}
