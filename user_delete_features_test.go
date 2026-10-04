package main

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestLifecyclePackageEmptied(t *testing.T) {
	for _, test := range []struct {
		name    string
		nodes   []int64
		deleted []int64
		want    bool
	}{
		{"all removed", []int64{10, 11}, []int64{10, 11}, true},
		{"admin remains", []int64{10, 12}, []int64{10, 11}, false},
		{"external remains", []int64{10, 13}, []int64{10, 11}, false},
		{"intentional all nodes", []int64{}, []int64{10, 11}, false},
		{"unaffected", []int64{14}, []int64{10, 11}, false},
		{"nothing deleted", []int64{10}, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := lifecyclePackageEmptied(test.nodes, test.deleted); got != test.want {
				t.Fatalf("emptied=%t want=%t", got, test.want)
			}
		})
	}
	application := &app{}
	item := application.classifyDeletionPackage(context.Background(), "", "alice", lifecyclePackageBinding{ID: 2, NodeIDs: []int64{10}}, lifecycleDeletionData{}, map[int64]lifecycleNodeLabel{10: {ID: 10}}, nil)
	if item.Action != lifecycleActionDeleteEmptyPackage || item.DecisionNote != "删除（移除节点后为空）" {
		t.Fatalf("preview=%+v", item)
	}
}

func TestDeletePrunesAndDeletesEmptiedOtherPackagesPostgres(t *testing.T) {
	for _, operation := range []string{"user deletion", "import clear cleanup"} {
		t.Run(operation, func(t *testing.T) {
			db := auditUserDeleteDB(t, "empty-package-followup")
			configs := seedDeletionNodes(t, db)
			auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes) VALUES(2,'emptied','[10,11]'),(3,'admin remains','[10,12]'),(4,'external remains','[10,13]'),(5,'all nodes','[]'),(6,'unaffected','[14]')`)
			auditDeleteExec(t, db, `UPDATE users SET package_id=2 WHERE username='bob'`)
			auditDeleteExec(t, db, `INSERT INTO user_package_assignments VALUES(2,'bob',2,'active')`)
			auditDeleteExec(t, db, `INSERT INTO mmwxc_package_traffic_groups VALUES(2,2,'[10,11]'),(3,3,'[10,12]')`)
			auditDeleteExec(t, db, `INSERT INTO forward_chain_nodes VALUES(13,'bob',2)`)
			application, _ := auditDeletionApplication(t, db, configs)
			if operation == "user deletion" {
				plan, result := runDeletionRegression(t, application, "alice")
				if !result.UserDeleted {
					t.Fatalf("delete=%+v", result)
				}
				for _, item := range plan {
					if item.PackageID == 2 && item.Action != lifecycleActionDeleteEmptyPackage {
						t.Fatalf("empty preview=%+v", item)
					}
					if item.PackageID == 5 || item.PackageID == 6 {
						t.Fatalf("unaffected package included=%+v", item)
					}
				}
			} else {
				before, err := lifecyclePackageBindings(context.Background(), db, "")
				if err != nil {
					t.Fatal(err)
				}
				auditDeleteExec(t, db, `DELETE FROM nodes WHERE id IN(10,11)`)
				// Some official node-removal paths already prune their package list.
				auditDeleteExec(t, db, `UPDATE packages SET nodes='[]' WHERE id=2`)
				if err := application.cleanupLifecycleDeletedNodePackages(context.Background(), "session", before, []int64{10, 11}); err != nil {
					t.Fatal(err)
				}
			}
			for _, query := range []string{
				`SELECT COUNT(*) FROM packages WHERE id=2`,
				`SELECT COUNT(*) FROM user_package_assignments WHERE package_id=2`,
				`SELECT COUNT(*) FROM mmwxc_package_traffic_groups WHERE package_id=2`,
				`SELECT COUNT(*) FROM forward_chain_nodes WHERE billing_assignment_id=2`,
			} {
				var count int
				if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
					t.Errorf("query %s count=%d err=%v", query, count, err)
				}
			}
			for id, want := range map[int64][]int64{3: {12}, 4: {13}, 5: {}, 6: {14}} {
				var raw string
				if err := db.QueryRow(`SELECT nodes FROM packages WHERE id=$1`, id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var got []int64
				if json.Unmarshal([]byte(raw), &got) != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("package=%d nodes=%v want=%v", id, got, want)
				}
			}
			var bobPackage *int64
			if err := db.QueryRow(`SELECT package_id FROM users WHERE username='bob'`).Scan(&bobPackage); err != nil || bobPackage != nil {
				t.Errorf("other user must remain, without deleted package: package=%v err=%v", bobPackage, err)
			}
		})
	}
}

func TestDeleteEmptiedPackageRechecksRemainingNodesPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "empty-package-race")
	configs := seedDeletionNodes(t, db)
	auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes) VALUES(2,'changed','[10]')`)
	application, _ := auditDeletionApplication(t, db, configs)
	item := lifecyclePlanItem{ItemKind: lifecycleItemKindPackage, PackageID: 2, NodeIDs: []int64{10}, DeletedNodeIDs: []int64{10}, Action: lifecycleActionDeleteEmptyPackage}
	auditDeleteExec(t, db, `UPDATE packages SET nodes='[10,13]' WHERE id=2`)
	if err := application.executeDeletionPackage(context.Background(), "session", "alice", &item); err != nil {
		t.Fatal(err)
	}
	var nodes string
	if err := db.QueryRow(`SELECT nodes FROM packages WHERE id=2`).Scan(&nodes); err != nil || nodes != "[13]" || item.Action != lifecycleActionKeepPackage {
		t.Fatalf("concurrently added node lost: nodes=%s action=%s err=%v", nodes, item.Action, err)
	}
}

func TestImportCleanupStillDeletesEmptiedPackagesAfterAnotherFailurePostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "empty-package-partial-cleanup")
	configs := seedDeletionNodes(t, db)
	auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes) VALUES(1,'kept','[10,13]'),(2,'emptied','[10]')`)
	application, fixture := auditDeletionApplication(t, db, configs)
	before, err := lifecyclePackageBindings(context.Background(), db, "")
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.extraHandler
	fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v3" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false})
			return true
		}
		return original(w, r)
	}
	if err := application.cleanupLifecycleDeletedNodePackages(context.Background(), "session", before, []int64{10}); err == nil {
		t.Fatal("partial cleanup must report the package failure")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM packages WHERE id=2`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("later empty package left behind: count=%d err=%v", count, err)
	}
}

func TestDeleteRetryRecognizesOfficialEmptiedPackagePostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "empty-package-retry")
	configs := seedDeletionNodes(t, db)
	auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes) VALUES(2,'emptied','[10,11]')`)
	application, _ := auditDeletionApplication(t, db, configs)
	plan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	store := application.adminStore.(lifecycleStore)
	if err := store.SaveDeletePlan(context.Background(), "alice", "regression-delete", plan); err != nil {
		t.Fatal(err)
	}
	auditDeleteExec(t, db, `UPDATE packages SET nodes='[]' WHERE id=2`)
	plan, result := runDeletionRegression(t, application, "alice")
	if !result.UserDeleted {
		t.Fatalf("retry=%+v", result)
	}
	for _, item := range plan {
		if item.PackageID == 2 && (item.Action != lifecycleActionDeleteEmptyPackage || len(item.DeletedNodeIDs) != 2) {
			t.Fatalf("retry lost empty-package decision: %+v", item)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM packages WHERE id=2`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("empty package remains=%d err=%v", count, err)
	}
}

func TestLifecycleRecognizesCurrentAdminCredentialsPostgres(t *testing.T) {
	for _, source := range []string{"user_inbound_configs", "package_assignment_inbound_configs", "user_subaccounts", "package_assignment_subaccounts", "without snapshots"} {
		t.Run(source, func(t *testing.T) {
			db := auditUserDeleteDB(t, "current-admin-credential")
			configs := seedDeletionNodes(t, db)
			credential := map[string]any{"id": "admin-replaced", "email": "admin@example.test"}
			raw, _ := json.Marshal(credential)
			switch source {
			case "package_assignment_inbound_configs":
				auditDeleteExec(t, db, `INSERT INTO package_assignment_inbound_configs(username,server_id,inbound_tag,protocol,credential_json) VALUES('admin',5,'coowned','vless',$1)`, string(raw))
			case "user_subaccounts", "package_assignment_subaccounts":
				auditDeleteExec(t, db, `CREATE TABLE `+source+`(username text,routed_node_id bigint,credential_json text)`)
				auditDeleteExec(t, db, `INSERT INTO remote_servers VALUES(6,'other-server')`)
				auditDeleteExec(t, db, `INSERT INTO nodes VALUES(16,'Other server','admin','other-server','coowned','vless')`)
				auditDeleteExec(t, db, `INSERT INTO `+source+` VALUES('admin',11,$1)`, string(raw))
			default:
				auditDeleteExec(t, db, `INSERT INTO user_inbound_configs VALUES('admin',5,'coowned','vless',$1)`, string(raw))
			}
			if source == "without snapshots" {
				auditDeleteExec(t, db, `DROP TABLE server_xray_config_snapshots`)
			}
			inbound := findConfigInbound(configs[5], "coowned")
			inbound["settings"].(map[string]any)["clients"].([]any)[1] = credential
			application, _ := auditDeletionApplication(t, db, configs)
			defaults, err := application.adminStore.(lifecycleStore).LifecycleDefaultAdminCredentials(context.Background(), 5, "coowned")
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if source == "without snapshots" {
				want = 1
			}
			if len(defaults) != want {
				t.Fatalf("admin default count=%d want=%d", len(defaults), want)
			}
			plan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range plan {
				if item.InboundTag == "coowned" && (item.Action != lifecycleActionDeleteWhole || item.DefaultCredentials != 1 || item.UnknownCredentials != 0) {
					t.Fatalf("replaced admin credential not recognized: %+v", item)
				}
			}
			if source == "user_subaccounts" || source == "package_assignment_subaccounts" {
				auditDeleteExec(t, db, `INSERT INTO `+source+` VALUES('admin',12,'{"id":"wrong-tag"}'),('admin',16,'{"id":"wrong-server"}'),('bob',11,'{"id":"non-admin","email":"admin@example.test"}')`)
				defaults, err = application.adminStore.(lifecycleStore).LifecycleDefaultAdminCredentials(context.Background(), 5, "coowned")
				if err != nil || len(defaults) != want {
					t.Fatalf("routed admin scope: count=%d want=%d err=%v", len(defaults), want, err)
				}
			}
			inbound["settings"].(map[string]any)["clients"].([]any)[1] = map[string]any{"id": "unrecorded-admin-secret", "email": "admin@example.test"}
			refs := []lifecycleCredentialRef{lifecycleRef(5, "coowned", "vless", map[string]any{"id": "alice-coowned", "email": "alice@example.test"})}
			item := analyzeLifecycleInbound(configs[5], refs, nil, defaults)
			if item.Action != lifecycleActionConflict || item.UnknownCredentials != 1 {
				t.Fatalf("unrecorded admin-like credential trusted: %+v", item)
			}
			wrongScope, err := application.adminStore.(lifecycleStore).LifecycleDefaultAdminCredentials(context.Background(), 7, "coowned")
			if err != nil || len(wrongScope) != 0 {
				t.Fatalf("admin credential escaped server scope: count=%d err=%v", len(wrongScope), err)
			}
		})
	}
}
