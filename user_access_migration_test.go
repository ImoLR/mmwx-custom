package main

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
)

func TestPersistentDisableMigrationWaitsForConfirmedBlock(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(strconv.FormatBool(confirmed), func(t *testing.T) {
			db := auditUserDeleteDB(t, "disable-migration")
			auditDeleteExec(t, db, `ALTER TABLE remote_servers ADD COLUMN xray_mode text DEFAULT 'external'`)
			auditDeleteExec(t, db, `CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamptz DEFAULT CURRENT_TIMESTAMP)`)
			auditDeleteExec(t, db, `INSERT INTO users(username,is_active) VALUES('alice',0)`)
			auditDeleteExec(t, db, `INSERT INTO remote_servers VALUES(1,'supported','external'),(2,'embedded','embedded')`)
			original := map[string]any{"email": "alice__old", "id": "11111111-1111-4111-8111-111111111111"}
			configs := map[int64]map[string]any{}
			var plans []lifecyclePlanItem
			for _, id := range []int64{1, 2} {
				ref := lifecycleRef(id, "old", "vless", original)
				auditDeleteExec(t, db, `INSERT INTO package_assignment_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES('alice',$1,'old','vless',$2)`, id, ref.CredentialRaw)
				item := analyzeAccessInbound("alice", false, lifecycleConfig(lifecycleInbound("old", "vless", original)), []lifecycleCredentialRef{ref}, nil)
				plans = append(plans, item)
				configs[id] = lifecycleConfig(item.replacementInbound)
				raw, _ := json.Marshal(configs[id])
				auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots(id,server_id,config_json,status) VALUES($1,$1,$2,'current')`, id, string(raw))
			}
			store := &postgresAdminSessionStore{db: db}
			ctx := context.Background()
			if err := store.SaveAccessPlan(ctx, "alice", lifecycleOperationDisable, "old-disable", plans); err != nil {
				t.Fatal(err)
			}
			auditDeleteExec(t, db, `UPDATE mmwxc_user_lifecycle SET official_was_active=NULL,effective_state='disabled' WHERE username='alice'`)
			fixture, server := newLifecycleAgentFixture(configs)
			defer server.Close()
			application := lifecycleTestApp(t, &lifecycleTestStore{}, server)
			application.adminStore, application.trafficGroupsReady = store, true
			record := persistentTestRecord()
			record.Snapshot.ProxyUsers = []serverProxyUserConnections{{Identity: serverConnectionIdentity{InboundTag: "old", User: "alice__old"}, Blocked: confirmed}}
			application.detailedConnections = map[string]serverDetailedConnectionRecord{"1": record}
			if err := application.refreshDisabledUsers(ctx); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := application.migratePersistentDisabledUser(ctx, store, "session", "alice"); err != nil {
					t.Fatal(err)
				}
			}
			backups, err := store.LifecycleDisabledCredentials(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			wantActions, wantBackups := 0, 2
			if confirmed {
				wantActions, wantBackups = 1, 1
			}
			if len(fixture.actions) != wantActions || len(backups) != wantBackups {
				t.Fatalf("confirmation=%v actions=%d backups=%d", confirmed, len(fixture.actions), len(backups))
			}
			if credentialsMatch(findCredential(t, configs[1], "old", 0), original, "vless") != confirmed {
				t.Fatal("supported credential restored before confirmation or not restored afterwards")
			}
			if credentialsMatch(findCredential(t, configs[2], "old", 0), original, "vless") {
				t.Fatal("background migration restored an unsupported server's swapped credential")
			}
		})
	}
}

func TestPersistentDisableMigrationMissingClientRequiresExactOriginal(t *testing.T) {
	for _, change := range []string{"missing", "official_metadata_drift", "runtime_label_drift"} {
		t.Run(change, func(t *testing.T) {
			db := auditUserDeleteDB(t, "disable-migration-missing")
			auditDeleteExec(t, db, `ALTER TABLE remote_servers ADD COLUMN xray_mode text DEFAULT 'external'`)
			auditDeleteExec(t, db, `CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamptz DEFAULT CURRENT_TIMESTAMP)`)
			auditDeleteExec(t, db, `INSERT INTO users(username,is_active) VALUES('alice',0)`)
			auditDeleteExec(t, db, `INSERT INTO remote_servers VALUES(1,'supported','external')`)
			original := map[string]any{"email": "alice__old", "id": "11111111-1111-4111-8111-111111111111"}
			ref := lifecycleRef(1, "old", "vless", original)
			item := analyzeAccessInbound("alice", false, lifecycleConfig(lifecycleInbound("old", "vless", original)), []lifecycleCredentialRef{ref}, nil)
			store := &postgresAdminSessionStore{db: db}
			ctx := context.Background()
			if err := store.SaveAccessPlan(ctx, "alice", lifecycleOperationDisable, "old-disable", []lifecyclePlanItem{item}); err != nil {
				t.Fatal(err)
			}
			recorded := cloneLifecycleMap(original)
			if change == "official_metadata_drift" {
				recorded["level"] = float64(7)
			}
			raw, _ := json.Marshal(recorded)
			auditDeleteExec(t, db, `INSERT INTO package_assignment_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES('alice',1,'old','vless',$1)`, string(raw))
			config := lifecycleConfig(lifecycleInbound("old", "vless"))
			if change == "runtime_label_drift" {
				changed := cloneLifecycleMap(original)
				changed["id"] = "22222222-2222-4222-8222-222222222222"
				config = lifecycleConfig(lifecycleInbound("old", "vless", changed))
			}
			raw, _ = json.Marshal(config)
			auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots(id,server_id,config_json,status) VALUES(1,1,$1,'current')`, string(raw))
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{1: config})
			defer server.Close()
			application := lifecycleTestApp(t, &lifecycleTestStore{}, server)
			application.adminStore, application.trafficGroupsReady = store, true
			record := persistentTestRecord()
			record.Snapshot.ProxyUsers = []serverProxyUserConnections{{Identity: serverConnectionIdentity{InboundTag: "old", User: "alice__old"}, Blocked: true}}
			application.detailedConnections = map[string]serverDetailedConnectionRecord{"1": record}
			if err := application.refreshDisabledUsers(ctx); err != nil {
				t.Fatal(err)
			}
			if err := application.migratePersistentDisabledUser(ctx, store, "session", "alice"); err != nil {
				t.Fatal(err)
			}
			backups, err := store.LifecycleDisabledCredentials(ctx, "alice")
			if err != nil {
				t.Fatal(err)
			}
			wantBackups := 1
			if change == "missing" {
				wantBackups = 0
			}
			if len(backups) != wantBackups || len(fixture.actions) != 0 {
				t.Fatalf("%s: backups=%d actions=%d", change, len(backups), len(fixture.actions))
			}
		})
	}
}
