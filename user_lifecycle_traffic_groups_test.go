package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestAccessLifecycleTrafficBlocksDoNotCauseCredentialDrift(t *testing.T) {
	for _, protocol := range []string{"vless", "shadowsocks"} {
		t.Run(protocol, func(t *testing.T) {
			original := map[string]any{"email": "alice-in", lifecycleCredentialPrimaryKey(protocol): "alice-secret"}
			inbound := lifecycleInbound("shared", protocol, original)
			if protocol == "shadowsocks" {
				inbound["settings"].(map[string]any)["method"] = "2022-blake3-aes-128-gcm"
				inbound["settings"].(map[string]any)["password"] = "server-key"
			}
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{1: lifecycleConfig(inbound)})
			defer server.Close()
			store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(1, "shared", protocol, original)}}
			application := lifecycleTestApp(t, store, server)
			identity := serverConnectionIdentity{InboundTag: "shared", User: "alice-in"}
			application.trafficGroupsReady = true
			application.trafficGroupBlocks = map[string][]serverConnectionIdentity{"1": {identity}}
			for _, operation := range []string{lifecycleOperationDisable, lifecycleOperationEnable} {
				plan, err := application.buildAccessPlan(context.Background(), "session", "alice", operation == lifecycleOperationEnable)
				if err != nil || len(plan) != 1 || plan[0].Status != lifecycleItemPending {
					t.Fatalf("%s plan: %+v, %v", operation, plan, err)
				}
				before := hashJSON(fixture.configs[1])
				for _, blocked := range []bool{false, true} {
					application.trafficGroupBlocks["1"] = nil
					if blocked {
						application.trafficGroupBlocks["1"] = []serverConnectionIdentity{identity}
					}
					settings := application.trafficBlocksForHelper(defaultServerConnectionSettings(), "1", "v0.6.8")
					wantCount := 0
					if blocked {
						wantCount = 1
					}
					if settings.BlockedIdentities == nil || len(*settings.BlockedIdentities) != wantCount {
						t.Fatalf("incorrect traffic decision: %+v", settings.BlockedIdentities)
					}
					if hashJSON(fixture.configs[1]) != before {
						t.Fatal("traffic control changed the Xray configuration")
					}
				}
				if err := store.SaveAccessPlan(context.Background(), "alice", operation, operation, plan); err != nil {
					t.Fatal(err)
				}
				result := application.executeAccessPlan(context.Background(), "session", "alice", operation, operation, plan)
				if result.PendingCount != 0 {
					t.Fatalf("traffic decision caused a lifecycle conflict: %+v", result)
				}
				entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[1], "shared"))
				if err != nil || len(entries) != 1 || entries[0]["email"] != identity.User {
					t.Fatalf("%s changed traffic identity: %+v, %v", operation, entries, err)
				}
				if (hashJSON(entries[0]) == hashJSON(original)) != (operation == lifecycleOperationEnable) {
					t.Fatalf("%s did not change/restore the credential", operation)
				}
				settings := application.trafficBlocksForHelper(defaultServerConnectionSettings(), "1", "v0.6.8")
				if settings.BlockedIdentities == nil || len(*settings.BlockedIdentities) != 1 || (*settings.BlockedIdentities)[0] != identity {
					t.Fatalf("%s lost the existing traffic block: %+v", operation, settings.BlockedIdentities)
				}
			}
		})
	}
}

func TestUserLifecycleTrafficGroupsIsolatedPostgres(t *testing.T) {
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
	schema := fmt.Sprintf("mmwxc_lifecycle_groups_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE users(username text PRIMARY KEY,role text,is_active bigint,email text,package_id bigint)`,
		`CREATE TABLE packages(id bigint PRIMARY KEY,traffic_limit_bytes bigint,nodes text,node_traffic_limits text,traffic_mode text)`,
		`CREATE TABLE user_package_assignments(id bigint PRIMARY KEY,username text,package_id bigint,package_start_date timestamp,package_end_date timestamp,last_reset_at timestamp,is_reset bigint,reset_day bigint,traffic_limit_override bigint,status text,created_at timestamp)`,
		`CREATE TABLE remote_servers(id bigint PRIMARY KEY,name text,xray_mode text)`,
		`CREATE TABLE nodes(id bigint PRIMARY KEY,node_name text,original_server text,inbound_tag text,username text,node_type text,protocol text,raw_url text,parsed_config text,clash_config text)`,
		`CREATE TABLE traffic_daily_user_nodes(server_id bigint,node_id bigint,username text,date text,weighted_uplink real,weighted_downlink real)`,
		`CREATE TABLE package_user_node_traffic_baselines(username text,package_id bigint,node_id bigint,baseline real,updated_at timestamp)`,
		`CREATE TABLE user_inbound_configs(username text,server_id bigint,inbound_tag text,credential_json text)`,
		`CREATE TABLE user_subaccounts(username text,routed_node_id bigint,email text,credential_json text,is_active bigint)`,
		`CREATE TABLE package_assignment_inbound_configs(assignment_id bigint,username text,server_id bigint,inbound_tag text,email text,credential_json text)`,
		`CREATE TABLE package_assignment_subaccounts(assignment_id bigint,username text,routed_node_id bigint,email text,credential_json text,is_active bigint)`,
		`CREATE TABLE user_subscriptions(username text,subscription_id bigint)`,
		`CREATE TABLE subscribe_files(created_by text)`,
		`CREATE TABLE user_outbounds(username text,server_id bigint,inbound_tag text)`,
		`CREATE TABLE forward_chain_nodes(node_id bigint,owner_username text,billing_assignment_id bigint)`,
		`CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamp)`,
		`INSERT INTO users(username,role,is_active,email) VALUES('alice','user',1,'alice-in'),('bob','user',1,'bob-in')`,
		`INSERT INTO remote_servers VALUES(1,'managed','external')`,
		`INSERT INTO packages VALUES(1,10000,'[1]','{}','twoway')`,
		`INSERT INTO nodes VALUES(1,'shared','managed','shared','bob','physical','shadowsocks','','{}','{"cipher":"2022-blake3-aes-128-gcm","password":"server-key:bob-key"}')`,
		`INSERT INTO user_package_assignments VALUES(1,'alice',1,CURRENT_DATE,NULL,NULL,0,1,NULL,'active',CURRENT_DATE),(2,'bob',1,CURRENT_DATE,NULL,NULL,0,1,NULL,'active',CURRENT_DATE)`,
		`INSERT INTO traffic_daily_user_nodes VALUES(1,1,'alice',CURRENT_DATE::text,300,300)`,
		`INSERT INTO package_assignment_inbound_configs VALUES(1,'alice',1,'shared','alice-in','{"email":"alice-in","password":"alice-key"}'),(2,'bob',1,'shared','bob-in','{"email":"bob-in","password":"bob-key"}')`,
		`INSERT INTO user_inbound_configs SELECT username,server_id,inbound_tag,credential_json FROM package_assignment_inbound_configs`,
		`INSERT INTO server_xray_config_snapshots VALUES(1,1,'{}','current',CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture: %v\n%s", err, statement)
		}
	}
	store := &postgresAdminSessionStore{db: db}
	ctx := context.Background()
	for _, migrate := range []func(context.Context) error{store.EnsureConnectionOwnershipSchema, store.EnsureUserLifecycleSchema, store.EnsureUserManagementSchema, store.EnsurePackageTrafficGroupsSchema} {
		if err := migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	groups, err := store.replaceTrafficGroups(ctx, 1, []packageTrafficGroup{{Name: "shared", Limit: 500, NodeIDs: []int64{1}}})
	if err != nil {
		t.Fatal(err)
	}
	alice := map[string]any{"email": "alice-in", "password": "alice-key"}
	bob := map[string]any{"email": "bob-in", "password": "bob-key"}
	inbound := lifecycleInbound("shared", "shadowsocks", alice, bob)
	inbound["settings"].(map[string]any)["method"] = "2022-blake3-aes-128-gcm"
	inbound["settings"].(map[string]any)["password"] = "server-key"
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{1: lifecycleConfig(inbound)})
	defer server.Close()
	lifecycleStatusFixture(t, fixture, db, nil)
	application := lifecycleTestApp(t, &lifecycleTestStore{}, server)
	application.adminStore = store
	application.detailedConnections = map[string]serverDetailedConnectionRecord{"1": {HelperVersion: "v0.6.8", UpdatedAt: time.Now(), Snapshot: serverDetailedConnectionSnapshot{Core: serverCoreConnectionStatus{Version: 7, Available: true, TrafficBlockSupported: true}}}}
	identity := serverConnectionIdentity{InboundTag: "shared", User: "alice-in"}
	publishConfig := func() {
		t.Helper()
		raw, err := json.Marshal(fixture.configs[1])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE server_xray_config_snapshots SET config_json=$1`, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	checkBlocks := func(blocked bool) {
		t.Helper()
		if err := application.refreshTrafficGroupsLocked(ctx, store); err != nil {
			t.Fatal(err)
		}
		identities := application.trafficGroupBlocks["1"]
		if blocked {
			if len(identities) != 1 || identities[0] != identity {
				t.Fatalf("group block changed across lifecycle: %+v; usage=%+v", identities, application.trafficGroupUsage[1])
			}
		} else if len(identities) != 0 {
			t.Fatalf("expected empty group block list: %+v", identities)
		}
		var count int
		wantCount := 0
		if blocked {
			wantCount = 1
		}
		if err := db.QueryRow(`SELECT count(*) FROM mmwxc_package_traffic_group_blocks`).Scan(&count); err != nil || count != wantCount {
			t.Fatalf("block rows=%d blocked=%t: %v", count, blocked, err)
		}
	}
	runAccess := func(username, operation, operationID string) {
		t.Helper()
		credential, otherCredential, targetIndex := alice, bob, 0
		if username == "bob" {
			credential, otherCredential, targetIndex = bob, alice, 1
		}
		backups, err := store.LifecycleDisabledCredentials(ctx, username)
		if err != nil {
			t.Fatal(err)
		}
		item := analyzeAccessInbound(username, operation == lifecycleOperationEnable, fixture.configs[1], []lifecycleCredentialRef{lifecycleRef(1, "shared", "shadowsocks", credential)}, backups)
		if item.Status != lifecycleItemPending {
			t.Fatalf("%s plan: %+v", operation, item)
		}
		if err := store.SaveAccessPlan(ctx, username, operation, operationID, []lifecyclePlanItem{item}); err != nil {
			t.Fatal(err)
		}
		result := application.executeAccessPlan(ctx, "session", username, operationID, operation, []lifecyclePlanItem{item})
		if result.PendingCount != 0 {
			t.Fatalf("%s failed: %+v", operation, result)
		}
		var active int
		wantActive := 0
		if operation == lifecycleOperationEnable {
			wantActive = 1
		}
		if err := db.QueryRow(`SELECT is_active FROM users WHERE username=$1`, username).Scan(&active); err != nil || active != wantActive {
			t.Fatalf("%s official users.is_active=%d, want %d: %v", operation, active, wantActive, err)
		}
		entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[1], "shared"))
		if err != nil || len(entries) != 2 || entries[targetIndex]["email"] != credential["email"] || hashJSON(entries[1-targetIndex]) != hashJSON(otherCredential) {
			t.Fatalf("%s changed identity/other user: %+v, %v", operation, entries, err)
		}
		publishConfig()
	}
	publishConfig()
	checkBlocks(true)
	runAccess("bob", lifecycleOperationDisable, "disable-owner")
	checkBlocks(true)
	ownership, err := store.ConnectionOwnership(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	ownerFound := false
	for _, relation := range ownership.Relations {
		if relation.ManagementUsername == "bob" && relation.Source == connectionSourceOwner {
			ownerFound = true
			if relation.ProtocolIdentity != "bob-in" {
				t.Fatalf("disabled owner has an unresolved or other user's identity: %+v", relation)
			}
		}
	}
	if ownerFound {
		t.Fatal("officially inactive user must remain excluded from assignable ownership")
	}
	_, mappings := buildManagementView(ownershipSnapshot("shared", 443, "alice-in", "bob-in"), defaultServerConnectionSettings(), ownership)
	if len(mappings) != 1 || mappings[0].Identity.User != "alice-in" || mappings[0].Group != "alice" {
		t.Fatalf("official disable changed another user's mapping: %+v", mappings)
	}
	runAccess("bob", lifecycleOperationEnable, "enable-owner")
	checkBlocks(true)
	runAccess("alice", lifecycleOperationDisable, "disable-blocked")
	checkBlocks(true)
	runAccess("alice", lifecycleOperationEnable, "enable-blocked")
	checkBlocks(true)
	runAccess("alice", lifecycleOperationDisable, "disable-unblock")
	groups[0].Limit = 1000
	if _, err := store.replaceTrafficGroups(ctx, 1, groups); err != nil {
		t.Fatal(err)
	}
	checkBlocks(false)
	runAccess("alice", lifecycleOperationEnable, "enable-unblocked")
	checkBlocks(false)
	groups[0].Limit = 500
	if _, err := store.replaceTrafficGroups(ctx, 1, groups); err != nil {
		t.Fatal(err)
	}
	checkBlocks(true)
	otherRef := lifecycleRef(1, "shared", "shadowsocks", bob)
	otherRef.Username = "bob"
	item := analyzeLifecycleInbound(fixture.configs[1], []lifecycleCredentialRef{lifecycleRef(1, "shared", "shadowsocks", alice)}, []lifecycleCredentialRef{otherRef}, nil)
	if item.Action != lifecycleActionRemoveUser || item.Status != lifecycleItemPending {
		t.Fatalf("blocked user delete plan: %+v", item)
	}
	if err := store.SaveDeletePlan(ctx, "alice", "delete-blocked", []lifecyclePlanItem{item}); err != nil {
		t.Fatal(err)
	}
	result := application.executeDeletePlan(ctx, "session", "alice", "delete-blocked", []lifecyclePlanItem{item})
	if result.PendingCount != 0 || !result.UserDeleted {
		t.Fatalf("blocked user deletion failed: %+v", result)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM mmwxc_package_traffic_group_blocks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("user deletion did not cascade block rows: %d, %v", count, err)
	}
	if len(application.trafficGroupBlocks["1"]) != 1 {
		t.Fatal("expected previous desired blocks until the next successful evaluation")
	}
	publishConfig()
	checkBlocks(false)
	entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[1], "shared"))
	if err != nil || len(entries) != 1 || hashJSON(entries[0]) != hashJSON(bob) {
		t.Fatalf("deletion did not preserve the other user: %+v, %v", entries, err)
	}
}
