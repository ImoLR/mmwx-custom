package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Regression fixtures contain synthetic credentials only.
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
			if result := auditAccessRun(t, application, store, true); result.PendingCount != 0 || result.State != lifecycleStateEnabled {
				t.Fatalf("UA-A03: enable did not finish: %+v", result)
			}
			if !credentialsMatch(findCredential(t, fixture.configs[5], "old", 0), original, "vless") {
				t.Fatal("UA-A03: original credential was not restored")
			}
			addedIndex := 0
			if sameInbound {
				addedIndex = 1
			}
			if hashJSON(findCredential(t, fixture.configs[5], tag, addedIndex)) != hashJSON(added) {
				t.Fatal("UA-A03: enable changed a newly added credential with no backup")
			}
		})
	}
}

func TestAuditUA_A04_ConcurrentUsersCannotRestoreEachOthersCredential(t *testing.T) {
	alice := map[string]any{"email": "alice-in", "id": "11111111-1111-4111-8111-111111111111"}
	bob := map[string]any{"email": "bob-in", "id": "22222222-2222-4222-8222-222222222222"}
	config := lifecycleConfig(lifecycleInbound("shared", "vless", alice, bob))
	aliceItem := analyzeAccessInbound("alice", false, config, []lifecycleCredentialRef{lifecycleRef(5, "shared", "vless", alice)}, nil)
	bobItem := analyzeAccessInbound("bob", false, config, []lifecycleCredentialRef{lifecycleRef(5, "shared", "vless", bob)}, nil)
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: config})
	defer server.Close()
	application := lifecycleTestApp(t, &lifecycleTestStore{}, server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, item := range []*lifecyclePlanItem{&aliceItem, &bobItem} {
		go func(item *lifecyclePlanItem) {
			<-start
			unlock := application.lockUserLifecycle(item.accessUsername)
			defer unlock()
			done <- application.executeAccessItem(ctx, "session", item)
		}(item)
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("concurrent disable failed: %v", err)
		}
	}
	entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "shared"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if credentialsMatch(entry, alice, "vless") || credentialsMatch(entry, bob, "vless") {
			t.Fatal("UA-A04: a concurrent replacement restored another user's original credential")
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

func TestAccessAnonymousSocksEnableWithoutBackupNeedsNoRestore(t *testing.T) {
	for _, auth := range []string{"noauth", "", "missing"} {
		t.Run(auth, func(t *testing.T) {
			settings := map[string]any{"auth": auth}
			if auth == "missing" {
				delete(settings, "auth")
			}
			inbound := map[string]any{"tag": "socks", "protocol": "socks", "settings": settings}
			ref := lifecycleRef(5, "socks", "socks", map[string]any{"user": "alice", "pass": "synthetic-secret"})
			item := analyzeAccessInbound("alice", true, lifecycleConfig(inbound), []lifecycleCredentialRef{ref}, nil)
			if item.Status != lifecycleItemCompleted || item.DecisionNote != "匿名 SOCKS 端口无需恢复" || item.LastError != "" ||
				item.replacementInbound != nil || len(item.accessCredentials) != 0 {
				t.Fatalf("anonymous SOCKS without backups should need no restoration: %+v", item)
			}
		})
	}
}

func TestAccessAnonymousSocksEnableRestoresLegacyBackup(t *testing.T) {
	account := map[string]any{"user": "alice", "pass": "synthetic-secret"}
	ref := lifecycleRef(5, "socks", "socks", account)
	inbound := map[string]any{"tag": "socks", "protocol": "socks", "settings": map[string]any{"auth": "password", "accounts": []any{account}}}
	disabled := analyzeAccessInbound("alice", false, lifecycleConfig(inbound), []lifecycleCredentialRef{ref}, nil)
	if disabled.Status != lifecycleItemPending || len(disabled.accessCredentials) != 1 {
		t.Fatalf("legacy backup setup failed: %+v", disabled)
	}
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprintf("already_restored_%v", restored), func(t *testing.T) {
			current := cloneLifecycleMap(disabled.replacementInbound)
			if restored {
				current = cloneLifecycleMap(inbound)
			}
			current["settings"].(map[string]any)["auth"] = "noauth"
			store := &lifecycleTestStore{refs: []lifecycleCredentialRef{ref}, backups: disabled.accessCredentials}
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(current)})
			defer server.Close()
			result := auditAccessRun(t, lifecycleTestApp(t, store, server), store, true)
			if result.PendingCount != 0 || result.State != lifecycleStateEnabled || len(store.backups) != 0 {
				t.Fatalf("legacy SOCKS backup was not restored and retired: %+v backups=%d", result, len(store.backups))
			}
			settings := findConfigInbound(fixture.configs[5], "socks")["settings"].(map[string]any)
			if settings["auth"] != "noauth" || hashJSON(settings["accounts"]) != hashJSON([]any{account}) {
				t.Fatal("legacy restoration did not preserve anonymous auth and restore the original account")
			}
			wantActions := 1
			if restored {
				wantActions = 0
			}
			if len(fixture.actions) != wantActions {
				t.Fatalf("legacy restoration dispatched %d actions, want %d", len(fixture.actions), wantActions)
			}
		})
	}
}

func TestAccessAnonymousSocksEnableAfterPartialDisablePostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "UA-A05-enable")
	auditDeleteExec(t, db, `CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamp)`)
	auditDeleteExec(t, db, `INSERT INTO users(username) VALUES('alice')`)
	auditDeleteExec(t, db, `INSERT INTO remote_servers VALUES(5,'server-5')`)
	account := map[string]any{"user": "alice", "pass": "synthetic-secret"}
	credential := map[string]any{"email": "alice-in", "id": "11111111-1111-4111-8111-111111111111"}
	for _, ref := range []lifecycleCredentialRef{lifecycleRef(5, "socks", "socks", account), lifecycleRef(5, "normal", "vless", credential)} {
		auditDeleteExec(t, db, `INSERT INTO package_assignment_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES('alice',$1,$2,$3,$4)`,
			ref.ServerID, ref.InboundTag, ref.Protocol, ref.CredentialRaw)
	}
	socks := map[string]any{"tag": "socks", "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(socks, lifecycleInbound("normal", "vless", credential))})
	defer server.Close()
	lifecycleStatusFixture(t, fixture, db, nil)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := &postgresAdminSessionStore{db: db}
	application := &app{adminStore: store, officialInternalTarget: target, trafficGroupsReady: true}
	ctx := context.Background()
	for _, enable := range []bool{false, true} {
		operation := lifecycleOperationDisable
		wantPending, wantBackups := 1, 1
		wantState, wantSocksStatus := lifecycleStatePartiallyDisabled, lifecycleItemFailed
		if enable {
			operation = lifecycleOperationEnable
			wantPending, wantBackups = 0, 0
			wantState, wantSocksStatus = lifecycleStateEnabled, lifecycleItemCompleted
		}
		plan, err := application.buildAccessPlan(ctx, "session", "alice", enable)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveAccessPlan(ctx, "alice", operation, operation, plan); err != nil {
			t.Fatal(err)
		}
		result := application.executeAccessPlan(ctx, "session", "alice", operation, operation, plan)
		if result.PendingCount != wantPending || result.State != wantState || len(result.Items) != 2 {
			t.Fatalf("access enable=%v: %+v", enable, result)
		}
		for _, item := range result.Items {
			if item.InboundTag == "socks" && (item.Status != wantSocksStatus || (enable && item.DecisionNote != "匿名 SOCKS 端口无需恢复")) {
				t.Fatalf("anonymous SOCKS enable=%v: %+v", enable, item)
			}
		}
		backups, err := store.LifecycleDisabledCredentials(ctx, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if len(backups) != wantBackups {
			t.Fatalf("access enable=%v left %d backups, want %d", enable, len(backups), wantBackups)
		}
		states, err := store.LifecycleStates(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if states["alice"].EffectiveState != wantState || states["alice"].PendingCount != wantPending {
			t.Fatalf("persisted access enable=%v: %+v", enable, states["alice"])
		}
	}
	if len(fixture.actions) != 2 || hashJSON(findConfigInbound(fixture.configs[5], "socks")) != hashJSON(socks) ||
		hashJSON(findConfigInbound(fixture.configs[5], "normal")) != hashJSON(lifecycleInbound("normal", "vless", credential)) {
		t.Fatal("access cycle must restore the normal inbound and leave anonymous SOCKS unchanged")
	}
}

func TestAuditUA_A06_SingleSecretShadowsocksCanBeDisabled(t *testing.T) {
	for _, method := range []string{"aes-128-gcm", "2022-blake3-aes-128-gcm"} {
		for _, clients := range []string{"missing", "null", "empty"} {
			t.Run(method+"/clients_"+clients, func(t *testing.T) {
				credential := map[string]any{"password": "c3ludGhldGljLWtleS0xNg==", "email": "alice-in"}
				originalSettings := map[string]any{
					"method": method, "password": credential["password"], "email": credential["email"],
				}
				if clients == "null" {
					originalSettings["clients"] = nil
				} else if clients == "empty" {
					originalSettings["clients"] = []any{}
				}
				inbound := map[string]any{"tag": "single", "protocol": "shadowsocks", "settings": originalSettings}
				store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "single", "shadowsocks", credential)}}
				fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(inbound)})
				defer server.Close()
				application := lifecycleTestApp(t, store, server)
				for _, enable := range []bool{false, true} {
					result := auditAccessRun(t, application, store, enable)
					if method == "aes-128-gcm" && clients == "empty" {
						if result.PendingCount != 1 || len(fixture.actions) != 0 || len(store.backups) != 0 {
							t.Fatal("classic Shadowsocks with an empty clients array must not treat the ignored top-level password as a user")
						}
						return
					}
					if result.PendingCount != 0 {
						t.Fatalf("UA-A06: valid single-secret Shadowsocks access change failed: %+v", result)
					}
					settings := findConfigInbound(fixture.configs[5], "single")["settings"].(map[string]any)
					if (settings["password"] == credential["password"]) != enable || settings["email"] != credential["email"] || settings["method"] != method {
						t.Fatalf("single password or unchanged settings incorrect after enable=%v", enable)
					}
					if _, exists := settings["clients"]; exists != (clients != "missing") || hashJSON(settings["clients"]) != hashJSON(originalSettings["clients"]) {
						t.Fatal("single-password replacement changed the clients setting")
					}
				}
			})
		}
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
	original := map[string]any{"email": "alice__tag", "id": "11111111-1111-4111-8111-111111111111"}
	base := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "tag", "vless", original)}}
	store := &auditAccessStateStore{lifecycleTestStore: base}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("tag", "vless", original))})
	defer server.Close()
	application := lifecycleTestApp(t, base, server)
	application.adminStore = store
	application.trafficGroupsReady = true
	application.detailedConnections = map[string]serverDetailedConnectionRecord{"5": persistentTestRecord()}
	if result := auditAccessRun(t, application, base, false); result.PendingCount != 0 {
		t.Fatalf("disable: %+v", result)
	}
	if len(base.backups) != 0 || len(fixture.actions) != 0 {
		t.Fatal("supported disable must not swap credentials")
	}
	for _, event := range []string{"renew", "rebind", "add_node", "new_assignment", "renew_while_official_inactive"} {
		t.Run(event, func(t *testing.T) {
			fixture.configs[5] = lifecycleConfig(lifecycleInbound("tag", "vless", original))
			request := httptest.NewRequest(http.MethodGet, "/api/custom/user-lifecycle", nil)
			request.Header.Set("MM-Authorization", "admin-session")
			response := httptest.NewRecorder()
			application.userLifecycleIndexHandler(response, request)
			var body struct {
				Users map[string]managedUserLifecycle `json:"users"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			settings := application.trafficBlocksForHelper(defaultServerConnectionSettings(), "5", "v0.6.8")
			if body.Users["alice"].EffectiveState != lifecycleStateDisabled || settings.BlockedIdentities == nil || len(*settings.BlockedIdentities) != 1 || (*settings.BlockedIdentities)[0].User != "alice__tag" {
				t.Fatalf("repush lost disabled intent/block: %+v %+v", body, settings.BlockedIdentities)
			}
		})
	}
}
