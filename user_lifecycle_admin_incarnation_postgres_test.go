package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestLifecycleDefaultAdminCredentialsCurrentIncarnationPostgres(t *testing.T) {
	oldAdmin := map[string]any{"id": "old-admin", "email": "admin__coowned"}
	newAdmin := map[string]any{"id": "new-admin", "email": "admin__coowned"}
	latestAdmin := map[string]any{"id": "latest-admin", "email": "admin__coowned"}
	other := map[string]any{"id": "old-other-user", "email": "user-mtl9oyrp"}
	snapshot := func(credentials ...map[string]any) string {
		raw, err := json.Marshal(lifecycleConfig(lifecycleInbound("coowned", "vless", credentials...)))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	type historyEntry struct{ config, source string }
	missing := historyEntry{`{"inbounds":[]}`, "master_write"}
	master := func(credentials ...map[string]any) historyEntry {
		return historyEntry{snapshot(credentials...), "master_write"}
	}
	batched := []historyEntry{master(oldAdmin), missing, master(newAdmin)}
	neverAbsent := []historyEntry{master(oldAdmin)}
	for i := 0; i < 260; i++ {
		batched = append(batched, master(latestAdmin))
		neverAbsent = append(neverAbsent, master(latestAdmin))
	}
	for _, test := range []struct {
		name    string
		history []historyEntry
		want    []map[string]any
	}{
		{"reused tag with other old clients", []historyEntry{master(other), missing, master(newAdmin)}, []map[string]any{newAdmin}},
		{"recreated with regenerated admin", []historyEntry{master(oldAdmin), missing, master(newAdmin), missing, master(latestAdmin)}, []map[string]any{latestAdmin}},
		{"never absent keeps earliest", []historyEntry{master(oldAdmin), master(newAdmin), master(latestAdmin)}, []map[string]any{oldAdmin}},
		{"first current snapshot not master write", []historyEntry{master(oldAdmin), missing, {snapshot(newAdmin), "agent_sync"}, master(newAdmin)}, nil},
		{"no fallback to later admin email", []historyEntry{master(oldAdmin), missing, master(other), master(newAdmin)}, nil},
		{"exact tag gap", []historyEntry{master(oldAdmin), {`{"inbounds":[{"tag":"coowned-extra"}],"remark":"coowned"}`, "master_write"}, master(newAdmin)}, []map[string]any{newAdmin}},
		{"tag whitespace matches inbound lookup", []historyEntry{{`{"inbounds":[{"tag":"\u00a0 coowned \u3000","protocol":"vless","settings":{"clients":[{"id":"old-admin","email":"admin__coowned"}]}}]}`, "master_write"}, master(newAdmin)}, []map[string]any{oldAdmin}},
		{"latest snapshot has no tag", []historyEntry{master(oldAdmin), missing}, nil},
		{"invalid snapshot refuses history", []historyEntry{master(oldAdmin), {`invalid json`, "master_write"}, master(newAdmin)}, nil},
		{"creation beyond first batch", batched, []map[string]any{newAdmin}},
		{"never absent beyond old 200 limit", neverAbsent, []map[string]any{oldAdmin}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := auditUserDeleteDB(t, "admin-incarnation")
			seedDeletionNodes(t, db)
			auditDeleteExec(t, db, `DELETE FROM server_xray_config_snapshots`)
			for i, entry := range test.history {
				// Equal timestamps exercise the id tie-breaker across keyset batches.
				auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots(id,server_id,config_json,source,created_at) VALUES($1,5,$2,$3,'2026-09-23T08:49:13Z')`, i+1, entry.config, entry.source)
			}
			auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots(id,server_id,config_json,source,created_at) VALUES(9999,6,'{}','master_write','2026-09-24T00:00:00Z')`)
			defaults, err := lifecycleDefaultAdminCredentials(context.Background(), db, 5, "coowned")
			if err != nil || !reflect.DeepEqual(defaults, test.want) {
				t.Fatalf("current incarnation defaults=%+v want=%+v err=%v", defaults, test.want, err)
			}
			if test.name == "first current snapshot not master write" {
				alice := map[string]any{"id": "alice-coowned", "email": "alice@example.test"}
				config := lifecycleConfig(lifecycleInbound("coowned", "vless", alice, newAdmin))
				item := analyzeLifecycleInbound(config, []lifecycleCredentialRef{lifecycleRef(5, "coowned", "vless", alice)}, nil, defaults)
				if item.Action != lifecycleActionConflict || item.UnknownCredentials != 1 {
					t.Fatalf("non-master creation trusted admin: %+v", item)
				}
			}
			if test.name == "invalid snapshot refuses history" {
				raw, _ := json.Marshal(newAdmin)
				auditDeleteExec(t, db, `INSERT INTO user_inbound_configs VALUES('admin',5,'coowned','vless',$1)`, string(raw))
				defaults, err = lifecycleDefaultAdminCredentials(context.Background(), db, 5, "coowned")
				if err != nil || !reflect.DeepEqual(defaults, []map[string]any{newAdmin}) {
					t.Fatalf("invalid history lost current admin binding: defaults=%+v err=%v", defaults, err)
				}
			}
		})
	}
}

func TestLifecycleRecreatedInboundDeletionPreviewPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "recreated-inbound-preview")
	configs := seedDeletionNodes(t, db)
	const tag = "vless-tcp-xtls-vision-reality-10017"
	auditDeleteExec(t, db, `DELETE FROM server_xray_config_snapshots`)
	auditDeleteExec(t, db, `INSERT INTO users(username,role,email) VALUES('imolr','admin',''),('khalilgao','user','')`)
	auditDeleteExec(t, db, `INSERT INTO nodes VALUES(73,'Khalilgao VLESS','imolr','server-5',$1,'vless'),(74,'Khalilgao VLESS alias','imolr','server-5',$1,'vless')`, tag)
	auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes) VALUES(11,'Khalilgao package','[73,74]')`)
	auditDeleteExec(t, db, `UPDATE users SET package_id=11 WHERE username='khalilgao'`)
	auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(11,'khalilgao',11,'active')`)
	user := map[string]any{"id": "khalilgao-synthetic-secret", "email": "khalilgao__" + tag}
	admin := map[string]any{"id": "imolr-regenerated-synthetic-secret", "email": "imolr__" + tag}
	credential, _ := json.Marshal(user)
	auditDeleteExec(t, db, `INSERT INTO user_inbound_configs VALUES('khalilgao',5,$1,'vless',$2)`, tag, string(credential))
	oldConfig := lifecycleConfig(lifecycleInbound(tag, "vless", map[string]any{"id": "old-user-1", "email": "user-mtl9oyrp"}, map[string]any{"id": "old-user-2", "email": "odingAI"}))
	for i, config := range []map[string]any{oldConfig, lifecycleConfig(), lifecycleConfig(lifecycleInbound(tag, "vless", map[string]any{"id": "previous-admin", "email": "imolr__" + tag})), lifecycleConfig()} {
		raw, _ := json.Marshal(config)
		auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots(id,server_id,config_json,source,created_at) VALUES($1,5,$2,'master_write','2026-09-23T08:49:12Z')`, i+366, string(raw))
	}
	current := lifecycleInbound(tag, "vless", user, admin)
	configs[5]["inbounds"] = append(configs[5]["inbounds"].([]any), current)
	raw, _ := json.Marshal(configs[5])
	auditDeleteExec(t, db, `INSERT INTO server_xray_config_snapshots(id,server_id,config_json,source,created_at) VALUES(380,5,$1,'master_write','2026-09-23T08:49:13Z')`, string(raw))
	application, fixture := auditDeletionApplication(t, db, configs)
	plan, err := application.buildDeletionPlan(context.Background(), "session", "khalilgao")
	if err != nil {
		t.Fatal(err)
	}
	var foundInbound, foundPackage bool
	for _, item := range plan {
		if item.InboundTag == tag {
			foundInbound = true
			if item.ServerID != 5 || item.Action != lifecycleActionDeleteWhole || item.DefaultCredentials != 1 || item.UnknownCredentials != 0 || len(item.NodeIDs) != 2 || !containsLifecycleNode(item.NodeIDs, 73) || !containsLifecycleNode(item.NodeIDs, 74) {
				t.Fatalf("recreated inbound preview=%+v", item)
			}
		}
		if item.PackageID == 11 {
			foundPackage = true
			var ownIDs []int64
			for _, node := range item.OwnNodes {
				ownIDs = append(ownIDs, node.ID)
			}
			if item.Action != lifecycleActionDeletePackage || !reflect.DeepEqual(ownIDs, []int64{73, 74}) || len(item.UnknownNodes) != 0 || len(item.OtherUserNodes) != 0 {
				t.Fatalf("package 11 preview=%+v", item)
			}
		}
	}
	if !foundInbound || !foundPackage || len(fixture.actions) != 0 {
		t.Fatalf("preview missing inbound/package or made changes: inbound=%t package=%t actions=%v", foundInbound, foundPackage, fixture.actions)
	}
}
