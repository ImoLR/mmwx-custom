package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestTrafficGroupAliasEntitlementsIsolatedPostgres(t *testing.T) {
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
	schema := fmt.Sprintf("mmwxc_group_aliases_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error("drop test schema:", err)
		}
	}()
	exec := func(t *testing.T, statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("fixture: %v\n%s", err, statement)
			}
		}
	}
	exec(t,
		`SET search_path TO `+schema,
		`CREATE TABLE users(username text PRIMARY KEY,is_active bigint,email text,package_id bigint)`,
		`CREATE TABLE packages(id bigint PRIMARY KEY,traffic_limit_bytes bigint,nodes text,node_traffic_limits text,traffic_mode text)`,
		`CREATE TABLE user_package_assignments(id bigint PRIMARY KEY,username text,package_id bigint,package_start_date timestamp,package_end_date timestamp,last_reset_at timestamp,is_reset bigint,reset_day bigint,traffic_limit_override bigint,status text,created_at timestamp,legacy_source bigint)`,
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
		`INSERT INTO users VALUES('alice',1,'alice@example.test',1),('bob',1,'bob@example.test',NULL)`,
		`INSERT INTO packages VALUES(1,10000,'[65]','{}','twoway'),(2,10000,'[76]','{}','twoway')`,
		`INSERT INTO user_package_assignments VALUES(1,'alice',1,'2026-10-01',NULL,NULL,0,1,NULL,'active','2026-10-01',1)`,
		`INSERT INTO remote_servers VALUES(5,'managed','external')`,
		`INSERT INTO nodes VALUES(65,'A65','managed','ss2022','bob','physical','shadowsocks','','{}','{"cipher":"2022-blake3-aes-128-gcm","password":"server-key:bob-key"}'),(76,'alias76','managed','ss2022','bob','physical','shadowsocks','','{}','{"cipher":"2022-blake3-aes-128-gcm","password":"server-key:bob-key"}')`,
		`INSERT INTO user_inbound_configs VALUES('alice',5,'ss2022','{"email":"alice-a","password":"alice-key"}'),('bob',5,'ss2022','{"email":"bob-a","password":"bob-key"}')`,
		`INSERT INTO server_xray_config_snapshots VALUES(1,5,'{"inbounds":[{"tag":"ss2022","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"server-key","clients":[{"email":"alice-a","password":"alice-key"},{"email":"bob-a","password":"bob-key"},{"email":"alice-routed","password":"alice-routed-key"}]}}]}','current',CURRENT_TIMESTAMP)`,
		`INSERT INTO traffic_daily_user_nodes VALUES(5,65,'alice','2026-10-01',200,400)`,
	)
	store := &postgresAdminSessionStore{db: db}
	ctx := context.Background()
	for _, ensure := range []func(context.Context) error{store.EnsurePackageTrafficGroupsSchema, store.EnsureConnectionOwnershipSchema, store.EnsureUserLifecycleSchema} {
		if err := ensure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.replaceTrafficGroups(ctx, 1, []packageTrafficGroup{{Name: "A65", Limit: 500, NodeIDs: []int64{65}}}); err != nil {
		t.Fatal(err)
	}
	reset := func(t *testing.T) {
		exec(t,
			`DELETE FROM user_package_assignments WHERE id<>1`,
			`UPDATE users SET package_id=1 WHERE username='alice'`,
			`UPDATE packages SET nodes=CASE id WHEN 1 THEN '[65]' ELSE '[76]' END`,
			`UPDATE nodes SET username='bob',node_type='physical',clash_config='{"cipher":"2022-blake3-aes-128-gcm","password":"server-key:bob-key"}' WHERE id=76`,
			`DELETE FROM user_subaccounts`,
			`DELETE FROM package_assignment_subaccounts`,
			`DELETE FROM user_subscriptions`,
			`DELETE FROM subscribe_files`,
			`DELETE FROM user_outbounds`,
			`DELETE FROM forward_chain_nodes`,
		)
	}
	secondAssignment := `INSERT INTO user_package_assignments VALUES(2,'alice',2,'2026-10-01',NULL,NULL,0,1,NULL,'active','2026-10-01',0)`
	for _, test := range []struct {
		name     string
		setup    []string
		enforced bool
	}{
		{"prod legacy binding with another user's unbound alias", nil, true},
		{"active non-group package assignment", []string{secondAssignment}, false},
		{"active legacy non-group package assignment", []string{secondAssignment, `UPDATE user_package_assignments SET legacy_source=1 WHERE id=2`}, false},
		{"legacy users package binding", []string{`UPDATE users SET package_id=2 WHERE username='alice'`}, false},
		{"empty assignment package grants all nodes", []string{secondAssignment, `UPDATE packages SET nodes='[]' WHERE id=2`}, false},
		{"empty legacy package grants all nodes", []string{`UPDATE users SET package_id=2 WHERE username='alice'`, `UPDATE packages SET nodes='[]' WHERE id=2`}, false},
		{"owned alias", []string{`UPDATE nodes SET username='alice',clash_config='{"cipher":"2022-blake3-aes-128-gcm","password":"server-key:alice-key"}' WHERE id=76`}, false},
		{"legacy routed subaccount shares group identity", []string{`UPDATE nodes SET node_type='routed' WHERE id=76`, `INSERT INTO user_subaccounts VALUES('alice',76,'alice-a','{"email":"alice-a","password":"alice-key"}',1)`}, false},
		{"assignment routed subaccount shares group identity", []string{`UPDATE nodes SET node_type='routed' WHERE id=76`, `INSERT INTO package_assignment_subaccounts VALUES(1,'alice',76,'alice-a','{"email":"alice-a","password":"alice-key"}',1)`}, false},
		{"legacy routed subaccount has distinct identity", []string{`UPDATE nodes SET node_type='routed' WHERE id=76`, `INSERT INTO user_subaccounts VALUES('alice',76,'alice-routed','{"email":"alice-routed","password":"alice-routed-key"}',1)`}, true},
		{"assignment routed subaccount has distinct identity", []string{`UPDATE nodes SET node_type='routed' WHERE id=76`, `INSERT INTO package_assignment_subaccounts VALUES(1,'alice',76,'alice-routed','{"email":"alice-routed","password":"alice-routed-key"}',1)`}, true},
		{"inactive assignment does not grant alias", []string{secondAssignment, `UPDATE user_package_assignments SET status='inactive' WHERE id=2`}, true},
		{"legacy subscription has unknown node scope", []string{`INSERT INTO user_subscriptions VALUES('alice',1)`}, false},
		{"another user's legacy subscription does not grant alias", []string{`INSERT INTO user_subscriptions VALUES('bob',1)`}, true},
		{"authored subscription has unknown node scope", []string{`INSERT INTO subscribe_files VALUES('alice')`}, false},
		{"another user's authored subscription does not grant alias", []string{`INSERT INTO subscribe_files VALUES('bob')`}, true},
		{"user outbound grants the same inbound", []string{`INSERT INTO user_outbounds VALUES('alice',5,'ss2022')`}, false},
		{"user outbound on another inbound does not grant alias", []string{`INSERT INTO user_outbounds VALUES('alice',5,'other')`}, true},
		{"forward chain node owner grants alias", []string{`INSERT INTO forward_chain_nodes VALUES(76,'alice',NULL)`}, false},
		{"forward chain billing assignment grants alias outside its package", []string{secondAssignment, `UPDATE packages SET nodes='[65]' WHERE id=2`, `INSERT INTO forward_chain_nodes VALUES(76,'bob',2)`}, false},
		{"another user's forward chain node does not grant alias", []string{`INSERT INTO forward_chain_nodes VALUES(76,'bob',NULL)`}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reset(t)
			exec(t, test.setup...)
			a := &app{adminStore: store, detailedConnections: map[string]serverDetailedConnectionRecord{
				"5": {HelperVersion: "v0.6.8", UpdatedAt: time.Now(), Snapshot: serverDetailedConnectionSnapshot{Core: serverCoreConnectionStatus{Version: 7, Available: true, TrafficBlockSupported: true}}},
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.refreshTrafficGroupsLocked(ctx, store); err != nil {
				t.Fatal("refresh:", err)
			}
			usage := a.trafficGroupUsage[1].Usage
			if len(usage) != 1 || usage[0].AssignmentID != 1 || usage[0].Used != 600 || len(usage[0].Nodes) != 1 || usage[0].Nodes[0].NodeID != 65 {
				t.Fatalf("unexpected group usage: %+v", usage)
			}
			wantStatus := "no_identity"
			if test.enforced {
				wantStatus = "enforced"
			}
			if usage[0].Blocked != test.enforced || usage[0].Nodes[0].Status != wantStatus {
				t.Fatalf("want blocked=%v status=%s, got %+v", test.enforced, wantStatus, usage[0])
			}
			settings := a.trafficBlocksForHelper(defaultServerConnectionSettings(), "5", "v0.6.8")
			if settings.BlockedIdentities == nil {
				t.Fatal("successful refresh must send an explicit Helper block list")
			}
			blocked := *settings.BlockedIdentities
			if test.enforced {
				if len(blocked) != 1 || blocked[0] != (serverConnectionIdentity{InboundTag: "ss2022", User: "alice-a"}) {
					t.Fatalf("must block only alice-a, got %+v", blocked)
				}
			} else if len(blocked) != 0 {
				t.Fatalf("unsafe alias entitlement must refuse blocking: %+v", blocked)
			}
		})
	}
	reset(t)
	for _, test := range []struct {
		name  string
		setup []string
	}{
		{"missing legacy package column", []string{`ALTER TABLE users DROP COLUMN package_id`}},
		{"missing legacy subaccounts table", []string{`DROP TABLE user_subaccounts`}},
		{"missing assignment subaccounts table", []string{`DROP TABLE package_assignment_subaccounts`}},
		{"missing legacy routed node column", []string{`ALTER TABLE user_subaccounts DROP COLUMN routed_node_id`}},
		{"missing assignment routed node column", []string{`ALTER TABLE package_assignment_subaccounts DROP COLUMN routed_node_id`}},
		{"missing legacy subscriptions table", []string{`DROP TABLE user_subscriptions`}},
		{"missing legacy subscription user column", []string{`ALTER TABLE user_subscriptions DROP COLUMN username`}},
		{"missing subscription files table", []string{`DROP TABLE subscribe_files`}},
		{"missing subscription author column", []string{`ALTER TABLE subscribe_files DROP COLUMN created_by`}},
		{"missing user outbounds table", []string{`DROP TABLE user_outbounds`}},
		{"missing user outbound tag column", []string{`ALTER TABLE user_outbounds DROP COLUMN inbound_tag`}},
		{"missing forward chain nodes table", []string{`DROP TABLE forward_chain_nodes`}},
		{"missing forward chain billing assignment column", []string{`ALTER TABLE forward_chain_nodes DROP COLUMN billing_assignment_id`}},
		{"unparsable assignment package nodes", []string{secondAssignment, `UPDATE packages SET nodes='[' WHERE id=2`}},
		{"unparsable legacy package nodes", []string{`UPDATE users SET package_id=2 WHERE username='alice'`, `UPDATE packages SET nodes='[' WHERE id=2`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec(t, `BEGIN`)
			t.Cleanup(func() { exec(t, `ROLLBACK`) })
			exec(t, test.setup...)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := store.trafficGroupIdentityData(ctx, 5); err == nil {
				t.Fatal("unknown alias entitlement must fail closed")
			}
		})
	}
}
