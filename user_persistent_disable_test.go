package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func persistentTestRecord() serverDetailedConnectionRecord {
	return serverDetailedConnectionRecord{HelperVersion: "v0.6.8", UpdatedAt: time.Now(), Snapshot: serverDetailedConnectionSnapshot{Core: serverCoreConnectionStatus{Available: true, Version: 7, TrafficBlockSupported: true}}}
}

func TestPersistentDisableBlockSourcesMerge(t *testing.T) {
	identity := serverConnectionIdentity{InboundTag: "shared", User: "alice__shared"}
	a := &app{trafficGroupsReady: true, disabledUsersReady: true, trafficGroupBlocks: map[string][]serverConnectionIdentity{"1": {identity}}, disabledUserBlocks: map[string]map[string][]serverConnectionIdentity{"alice": {"1": {identity}}}}
	check := func(want int) {
		t.Helper()
		settings := a.trafficBlocksForHelper(defaultServerConnectionSettings(), "1", "v0.6.8")
		if settings.BlockedIdentities == nil || len(*settings.BlockedIdentities) != want {
			t.Fatalf("merged blocks: %+v", settings.BlockedIdentities)
		}
	}
	check(1)
	delete(a.disabledUserBlocks, "alice") // Enable removes only this user's source.
	check(1)
	a.disabledUserBlocks["alice"] = map[string][]serverConnectionIdentity{"1": {identity}}
	a.trafficGroupBlocks = map[string][]serverConnectionIdentity{} // Group cycle reset.
	check(1)
	delete(a.disabledUserBlocks, "alice")
	check(0)
	a.trafficGroupsReady = false
	if got := a.trafficBlocksForHelper(defaultServerConnectionSettings(), "1", "v0.6.8"); got.BlockedIdentities != nil {
		t.Fatal("failed evaluation must preserve Helper's previous list")
	}
}

func TestPersistentDisableReconcilePostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "persistent")
	for _, statement := range []string{
		`ALTER TABLE remote_servers ADD COLUMN xray_mode text DEFAULT 'external'`,
		`CREATE TABLE nodes(id bigint PRIMARY KEY,node_name text,username text,original_server text,inbound_tag text,protocol text)`,
		`CREATE TABLE user_inbound_configs(username text,server_id bigint,inbound_tag text,protocol text,credential_json text)`,
		`ALTER TABLE package_assignment_inbound_configs ADD COLUMN email text`,
		`CREATE TABLE user_subaccounts(username text,routed_node_id bigint,email text,credential_json text,is_active bigint)`,
		`CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamptz DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO users(username,role,email) VALUES('alice','user',''),('bob','user',''),('admin','admin','')`,
		`INSERT INTO remote_servers VALUES(1,'supported','external'),(2,'embedded','embedded')`,
		`INSERT INTO nodes VALUES(1,'原节点','admin','supported','old','vless'),(2,'尽力节点','admin','embedded','old','vless')`,
		`INSERT INTO user_inbound_configs VALUES('alice',1,'old','vless','{"email":"alice__old","id":"alice-original"}'),('admin',1,'old','vless','{"email":"admin__old","id":"admin-original"}'),('alice',2,'old','vless','{"email":"alice__old","id":"alice-original"}'),('admin',2,'old','vless','{"email":"admin__old","id":"admin-original"}')`,
		`INSERT INTO packages(id,name,nodes) VALUES(1,'Alice','[1,2]')`,
		`UPDATE users SET package_id=1 WHERE username='alice'`,
		`INSERT INTO mmwxc_user_lifecycle(username,desired_state,effective_state,operation) VALUES('alice','disabled','disabled','disable')`,
	} {
		auditDeleteExec(t, db, statement)
	}
	ctx := context.Background()
	store := &postgresAdminSessionStore{db: db}
	application := &app{adminStore: store, trafficGroupsReady: true, detailedConnections: map[string]serverDetailedConnectionRecord{"1": persistentTestRecord()}}
	config := lifecycleConfig(lifecycleInbound("old", "vless", map[string]any{"email": "alice__old", "id": "alice-original"}, map[string]any{"email": "admin__old", "id": "admin-original"}))
	raw, _ := json.Marshal(config)
	auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots(id,server_id,config_json,status) VALUES(1,1,$1,'current'),(2,2,$1,'current')`, string(raw))
	check := func(want int) {
		t.Helper()
		if err := application.refreshDisabledUsers(ctx); err != nil {
			t.Fatal(err)
		}
		got := application.trafficBlocksForHelper(defaultServerConnectionSettings(), "1", "v0.6.8")
		if got.BlockedIdentities == nil || len(*got.BlockedIdentities) != want {
			t.Fatalf("blocks=%+v statuses=%+v", got.BlockedIdentities, application.disabledUserAccess)
		}
	}
	check(1)
	// Official inactive removes runtime clients but keeps credential records.
	auditDeleteExec(t, db, `UPDATE server_xray_config_snapshots SET config_json='{"inbounds":[]}' WHERE server_id=1`)
	check(1)
	for _, event := range []string{"renew", "rebind", "renew_while_official_inactive"} {
		t.Run(event, func(t *testing.T) {
			auditDeleteExec(t, db, `UPDATE server_xray_config_snapshots SET config_json=$1 WHERE server_id=1`, string(raw))
			check(1)
		})
	}
	for index, source := range []string{"user_inbound_configs", "package_assignment_inbound_configs", "user_subaccounts"} {
		t.Run(source, func(t *testing.T) {
			id := int64(index + 3)
			tag := source
			credential := map[string]any{"email": "alice__" + source, "id": "new-" + source}
			encoded, _ := json.Marshal(credential)
			auditDeleteExec(t, db, `INSERT INTO nodes VALUES($1,$2,'admin','supported',$2,'vless')`, id, tag)
			adminCredential, _ := json.Marshal(map[string]any{"email": "admin__" + source, "id": "admin-" + source})
			auditDeleteExec(t, db, `INSERT INTO user_inbound_configs VALUES('admin',1,$1,'vless',$2)`, tag, string(adminCredential))
			switch source {
			case "user_inbound_configs":
				auditDeleteExec(t, db, `INSERT INTO user_inbound_configs VALUES('alice',1,$1,'vless',$2)`, tag, string(encoded))
			case "package_assignment_inbound_configs":
				auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(7,'alice',1,'active')`)
				auditDeleteExec(t, db, `INSERT INTO package_assignment_inbound_configs(assignment_id,username,server_id,inbound_tag,protocol,email,credential_json) VALUES(7,'alice',1,$1,'vless',$2,$3)`, tag, credential["email"], string(encoded))
			case "user_subaccounts":
				auditDeleteExec(t, db, `INSERT INTO user_subaccounts VALUES('alice',$1,$2,$3,1)`, id, credential["email"], string(encoded))
			}
			// Prove the next periodic evaluation alone includes a new assignment.
			check(index + 2)
		})
	}
	states, err := store.LifecycleStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	application.attachUserAccess(states)
	warning := false
	for _, item := range states["alice"].Access {
		warning = warning || item.ServerID == 2 && item.Status == "best_effort" && item.Reason == persistentDisableWarning
	}
	if !warning || states["alice"].EffectiveState != lifecycleStateDisabled {
		t.Fatalf("disabled UI state/warning lost: %+v", states)
	}
	// A restart and stale capability report must not clear persisted blocks.
	application.disabledUserBlocks = nil
	application.detailedConnections["1"] = serverDetailedConnectionRecord{}
	check(4)
	// Sharing discovered after disable removes only the unsafe identity.
	auditDeleteExec(t, db, `INSERT INTO user_inbound_configs VALUES('bob',1,'old','vless','{"email":"alice__old","id":"alice-original"}')`)
	check(3)
	conflict := false
	for _, item := range application.disabledUserAccess["alice"] {
		conflict = conflict || item.InboundTag == "old" && item.ServerID == 1 && item.Status == "conflict"
	}
	if !conflict {
		t.Fatal("shared identity not shown as conflict")
	}
	// Enable cannot be reversed by reconciliation and does not clear group blocks.
	identity := serverConnectionIdentity{InboundTag: "old", User: "alice__old"}
	application.trafficGroupBlocks = map[string][]serverConnectionIdentity{"1": {identity}}
	if err := store.SaveAccessPlan(ctx, "alice", lifecycleOperationEnable, "enable", nil); err != nil {
		t.Fatal(err)
	}
	check(1)
}
