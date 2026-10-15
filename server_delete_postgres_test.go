package main

import (
	"context"
	"encoding/json"
	"testing"
)

func TestServerDeletionSweepAndConcurrentUserPlanPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "server-delete")
	ctx := context.Background()
	s := &postgresAdminSessionStore{db: db}
	for _, statement := range []string{
		`ALTER TABLE users ADD COLUMN node_speed_limit_overrides text DEFAULT '{}',ADD COLUMN node_device_limit_overrides text DEFAULT '{}'`,
		`CREATE TABLE user_settings(username text PRIMARY KEY,node_order text)`,
		`CREATE TABLE user_subaccounts(username text,routed_node_id bigint)`,
		`CREATE TABLE traffic_daily_user_nodes(server_id bigint,node_id bigint)`,
		`CREATE TABLE nodes(id bigint PRIMARY KEY,original_server text,inbound_tag text)`,
		packageTrafficGroupsSchema, uiMetadataSchema,
		`INSERT INTO packages VALUES(201,'empty','[101]'),(202,'mixed','[101,102]'),(203,'untouched','[102]')`,
		`INSERT INTO users(username,package_id,node_speed_limit_overrides) VALUES('alice',201,'{"101":10,"102":20}'),('bob',202,'{}')`,
		`INSERT INTO remote_servers VALUES(1,'A'),(2,'B')`,
		`INSERT INTO nodes VALUES(102,'B','in-B')`,
		`INSERT INTO user_subaccounts VALUES('alice',103)`,
		`INSERT INTO traffic_daily_user_nodes VALUES(1,101),(2,103),(2,102)`,
		`INSERT INTO user_settings VALUES('alice','[101,102,103]')`,
		`INSERT INTO mmwxc_package_traffic_groups(id,package_id,name,limit_bytes,node_ids) VALUES(1,201,'deleted',100,'[101]'),(2,202,'trimmed',200,'[101,102]'),(3,202,'empty group',300,'[103]'),(4,203,'keep',400,'[102]')`,
		`INSERT INTO mmwxc_ui_preferences(username,preference_key,data_json) VALUES('alice','service_groups','{"groups":[{"id":"g","name":"test","server_ids":[1,2]}]}')`,
	} {
		auditDeleteExec(t, db, statement)
	}
	items := []lifecyclePlanItem{{ServerID: 1, ServerName: "A", InboundTag: "in-A", Protocol: "vless", Action: lifecycleActionDeleteWhole, Status: lifecycleItemPending, NodeIDs: []int64{101}, targetCredentials: []map[string]any{{"id": "secret-A"}}}, {ServerID: 2, ServerName: "B", InboundTag: "in-B", Protocol: "vless", Action: lifecycleActionRemoveUser, Status: lifecycleItemPending, NodeIDs: []int64{102}, targetCredentials: []map[string]any{{"id": "secret-B"}}}}
	if err := s.SaveDeletePlan(ctx, "alice", "concurrent-user-delete", items); err != nil {
		t.Fatal(err)
	}
	importedID, err := s.saveImportedNodeCleanup(ctx, "bob", importedNodeCleanupSnapshot{NodeIDs: []int64{103}, Packages: []lifecyclePackageBinding{{ID: 202, NodeIDs: []int64{101, 102}}}})
	if err != nil {
		t.Fatal(err)
	}
	p := serverDeletionPlan{ServerID: 1, ServerName: "A", Nodes: []serverDeletionNode{{ID: 101}, {ID: 103}}, Packages: []serverDeletionPackage{{ID: 201, Name: "empty"}, {ID: 202, Name: "mixed"}}, Groups: []serverDeletionGroup{{ID: 1}, {ID: 2}, {ID: 3}}}
	if deleted, err := s.DeleteEmptiedLifecyclePackage(ctx, 201, []int64{101}); err != nil || !deleted {
		t.Fatalf("empty package=%t err=%v", deleted, err)
	}
	// The public package operation has already trimmed the mixed package.
	auditDeleteExec(t, db, `UPDATE packages SET nodes='[102]' WHERE id=202`)
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.sweepServerDeletion(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.verifyServerDeletion(ctx, &p); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{
		`SELECT count(*) FROM users`: 2, `SELECT count(*) FROM packages`: 2, `SELECT count(*) FROM user_subaccounts`: 0,
		`SELECT count(*) FROM traffic_daily_user_nodes`: 1, `SELECT count(*) FROM mmwxc_package_traffic_groups`: 2,
		`SELECT count(*) FROM mmwxc_package_traffic_groups WHERE id=2 AND node_ids='[102]' AND limit_bytes=200`:       1,
		`SELECT count(*) FROM mmwxc_package_traffic_groups WHERE id=4 AND node_ids='[102]' AND limit_bytes=400`:       1,
		`SELECT count(*) FROM users WHERE username='alice' AND node_speed_limit_overrides::jsonb='{"102":20}'::jsonb`: 1,
		`SELECT count(*) FROM user_settings WHERE node_order::jsonb='[102]'::jsonb`:                                   1,
		`SELECT count(*) FROM mmwxc_ui_preferences WHERE data_json->'groups'->0->'server_ids'='[2]'::jsonb`:           1,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got=%d want=%d err=%v", query, got, want, err)
		}
	}
	resumed, err := s.LifecycleDeleteItems(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range resumed {
		if item.ServerID == 1 && (item.Status != lifecycleItemCompleted || len(item.targetCredentials) != 0) {
			t.Fatal("retired item cannot resume safely")
		}
		if item.ServerID == 2 && (item.Status != lifecycleItemPending || len(item.targetCredentials) != 1) {
			t.Fatal("unrelated user target changed")
		}
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM mmwxc_user_lifecycle_operations WHERE operation_id=$1`, importedID).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("imported cleanup state=%s err=%v", state, err)
	}
	raw, _ := json.Marshal(publicServerDeletion(p))
	if len(raw) == 0 {
		t.Fatal("empty public audit")
	}
}
