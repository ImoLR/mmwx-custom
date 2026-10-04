package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPersistentAccessIdentitiesUseCoreLabels(t *testing.T) {
	for _, source := range []string{"user_inbound_configs", "package_assignment_inbound_configs", "user_subaccounts", "package_assignment_subaccounts", "mmwxc_connection_assignments"} {
		t.Run(source, func(t *testing.T) {
			credential := map[string]any{"id": "alice-uuid", "email": "alice__pkg2__inbound"}
			ref := lifecycleRef(5, "inbound", "vless", credential)
			ref.Username, ref.Source = "alice", source
			want := []serverConnectionIdentity{{InboundTag: "inbound", User: "alice__pkg2__inbound"}}
			for _, inbound := range []map[string]any{lifecycleInbound("inbound", "vless", credential), lifecycleInbound("inbound", "vless"), nil} {
				got, fallback, reason := persistentAccessIdentities("alice", inbound, []lifecycleCredentialRef{ref}, nil, nil, nil)
				if !reflect.DeepEqual(got, want) || len(fallback) != 0 || reason != "" {
					t.Fatalf("source=%s got=%+v fallback=%d reason=%s", source, got, len(fallback), reason)
				}
			}
		})
	}
}

func TestPersistentAccessIdentitiesResolveOfficialLabelWithoutCurrentClient(t *testing.T) {
	for _, raw := range []string{"", `{"id":"alice-uuid"}`} {
		ref := lifecycleCredentialRef{Username: "alice", ServerID: 5, InboundTag: "inbound", Protocol: "vless", CredentialRaw: raw, Identity: "alice__pkg2__inbound", Source: "package_assignment_inbound_configs"}
		got, fallback, reason := persistentAccessIdentities("alice", nil, []lifecycleCredentialRef{ref}, nil, nil, nil)
		want := []serverConnectionIdentity{{InboundTag: "inbound", User: ref.Identity}}
		if !reflect.DeepEqual(got, want) || len(fallback) != 0 || reason != "" {
			t.Fatalf("official label was lost when inactive removed the client: got=%+v fallback=%d reason=%s", got, len(fallback), reason)
		}
	}
}

func TestPersistentAccessIdentitiesPreferCurrentClientLabel(t *testing.T) {
	current := map[string]any{"id": "alice-uuid", "email": "alice__inbound"}
	ref := lifecycleRef(5, "inbound", "vless", map[string]any{"id": "alice-uuid", "email": "older-client-label"})
	got, fallback, reason := persistentAccessIdentities("alice", lifecycleInbound("inbound", "vless", current), []lifecycleCredentialRef{ref}, nil, nil, nil)
	if len(got) != 1 || got[0].User != "alice__inbound" || len(fallback) != 0 || reason != "" {
		t.Fatalf("current Core label not selected: %+v fallback=%d reason=%s", got, len(fallback), reason)
	}
}

func TestPersistentAccessIdentitiesRefuseEverySharedRelation(t *testing.T) {
	credential := map[string]any{"id": "shared-uuid", "email": "alice__inbound"}
	ref := lifecycleRef(5, "inbound", "vless", credential)
	ref.Username = "alice"
	for _, source := range []string{"user_inbound_configs", "package_assignment_inbound_configs", "user_subaccounts", "package_assignment_subaccounts", "mmwxc_connection_assignments", "user_outbounds", "node_entitlement"} {
		for _, same := range []string{"label", "secret"} {
			t.Run(source+"/"+same, func(t *testing.T) {
				otherCredential := map[string]any{"id": "bob-uuid", "email": "alice__inbound"}
				if same == "secret" {
					otherCredential = map[string]any{"id": "shared-uuid", "email": "bob__inbound"}
				}
				other := lifecycleRef(5, "inbound", "vless", otherCredential)
				other.Username, other.Source = "bob", source
				if source == "user_outbounds" || source == "node_entitlement" {
					other.CredentialRaw = ""
				}
				got, fallback, reason := persistentAccessIdentities("alice", lifecycleInbound("inbound", "vless", credential), []lifecycleCredentialRef{ref}, nil, []lifecycleCredentialRef{other}, nil)
				if len(got) != 0 || len(fallback) != 0 || reason == "" {
					t.Fatalf("shared relation permitted: %+v fallback=%d reason=%s", got, len(fallback), reason)
				}
			})
		}
	}
	for _, admin := range []map[string]any{{"id": "admin-uuid", "email": "alice__inbound"}, {"id": "shared-uuid", "email": "admin__inbound"}} {
		got, _, reason := persistentAccessIdentities("alice", lifecycleInbound("inbound", "vless", credential), []lifecycleCredentialRef{ref}, nil, nil, []map[string]any{admin})
		if len(got) != 0 || !strings.Contains(reason, "管理员默认凭据") {
			t.Fatalf("administrator credential not protected: %+v reason=%s", got, reason)
		}
	}
}

func TestPersistentAccessIdentitiesRefuseDuplicateClients(t *testing.T) {
	credential := map[string]any{"id": "alice-uuid", "email": "alice__inbound"}
	ref := lifecycleRef(5, "inbound", "vless", credential)
	for _, other := range []map[string]any{{"id": "alice-uuid", "email": "bob__inbound"}, {"id": "bob-uuid", "email": "alice__inbound"}} {
		got, _, reason := persistentAccessIdentities("alice", lifecycleInbound("inbound", "vless", credential, other), []lifecycleCredentialRef{ref}, nil, nil, nil)
		if len(got) != 0 || reason == "" {
			t.Fatalf("ambiguous authentication permitted: %+v reason=%s", got, reason)
		}
	}
}

func TestPersistentAccessIdentitiesRefuseUnresolvedOtherUserCredentials(t *testing.T) {
	credential := map[string]any{"id": "alice-uuid", "email": "alice__inbound"}
	ref := lifecycleRef(5, "inbound", "vless", credential)
	for _, raw := range []string{"", "{}", "[]", "invalid-json"} {
		other := lifecycleCredentialRef{Username: "bob", ServerID: 5, InboundTag: "inbound", Protocol: "vless", CredentialRaw: raw, Source: "user_inbound_configs"}
		got, _, reason := persistentAccessIdentities("alice", lifecycleInbound("inbound", "vless", credential), []lifecycleCredentialRef{ref}, nil, []lifecycleCredentialRef{other}, nil)
		if len(got) != 0 || reason == "" {
			t.Fatalf("unresolved other user credential was assumed safe: raw=%q got=%+v reason=%s", raw, got, reason)
		}
	}
}

func TestPersistentAccessIdentitiesKeepDistinctUsersIndependent(t *testing.T) {
	alice := map[string]any{"id": "alice-uuid", "email": "alice__inbound"}
	bob := map[string]any{"id": "bob-uuid", "email": "bob__inbound"}
	ref := lifecycleRef(5, "inbound", "vless", alice)
	other := lifecycleRef(5, "inbound", "vless", bob)
	other.Username = "bob"
	got, fallback, reason := persistentAccessIdentities("alice", lifecycleInbound("inbound", "vless", alice, bob), []lifecycleCredentialRef{ref}, nil, []lifecycleCredentialRef{other}, []map[string]any{{"id": "admin-uuid", "email": "admin__inbound"}})
	if len(got) != 1 || got[0].User != "alice__inbound" || len(fallback) != 0 || reason != "" {
		t.Fatalf("distinct identity wrongly refused: %+v fallback=%d reason=%s", got, len(fallback), reason)
	}
}

func TestPersistentAccessIdentitiesFallbackWithoutPerUserCoreEmail(t *testing.T) {
	for _, protocol := range []string{"socks", "http", "vless"} {
		credential := map[string]any{"user": "alice", "pass": "password", "email": "label-not-used-by-core"}
		inbound := map[string]any{"tag": "inbound", "protocol": protocol, "settings": map[string]any{"auth": "password", "accounts": []any{credential}}}
		if protocol == "vless" {
			credential = map[string]any{"id": "alice-uuid"}
			inbound = lifecycleInbound("inbound", protocol, credential)
		}
		ref := lifecycleRef(5, "inbound", protocol, credential)
		got, fallback, reason := persistentAccessIdentities("alice", inbound, []lifecycleCredentialRef{ref}, nil, nil, nil)
		if len(got) != 0 || len(fallback) != 1 || reason != "" {
			t.Fatalf("%s must use best effort without blocking an inbound: %+v fallback=%d reason=%s", protocol, got, len(fallback), reason)
		}
	}
	credential := map[string]any{"id": "alice-uuid"}
	ref := lifecycleRef(5, "inbound", "vless", credential)
	ref.Identity = "alice__inbound"
	got, fallback, reason := persistentAccessIdentities("alice", lifecycleInbound("inbound", "vless", credential), []lifecycleCredentialRef{ref}, nil, nil, nil)
	if len(got) != 0 || len(fallback) != 1 || reason != "" {
		t.Fatalf("a known client without an email cannot acquire a Core label from database metadata: %+v fallback=%d reason=%s", got, len(fallback), reason)
	}
}

func TestPersistentAccessDataWithoutAccountEmailsPostgres(t *testing.T) {
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
	schema := fmt.Sprintf("mmwxc_disable_identity_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE users(username text PRIMARY KEY,role text,package_id bigint)`,
		`CREATE TABLE remote_servers(id bigint PRIMARY KEY,name text,xray_mode text)`,
		`CREATE TABLE nodes(id bigint PRIMARY KEY,node_name text,original_server text,inbound_tag text,protocol text)`,
		`CREATE TABLE packages(id bigint PRIMARY KEY,nodes text)`,
		`CREATE TABLE user_package_assignments(id bigint PRIMARY KEY,username text,package_id bigint,status text)`,
		`CREATE TABLE user_inbound_configs(username text,server_id bigint,inbound_tag text,protocol text,credential_json text)`,
		`CREATE TABLE package_assignment_inbound_configs(username text,server_id bigint,inbound_tag text,protocol text,email text,credential_json text)`,
		`CREATE TABLE user_subaccounts(username text,routed_node_id bigint,email text,credential_json text)`,
		`CREATE TABLE package_assignment_subaccounts(username text,routed_node_id bigint,email text,credential_json text)`,
		`INSERT INTO users VALUES('alice','user',1),('bob','user',NULL),('admin','admin',NULL)`,
		`INSERT INTO remote_servers VALUES(5,'server-5','external')`,
		`INSERT INTO nodes VALUES(10,'original node','server-5','inbound','vless'),(11,'assignment node','server-5','assignment','vless'),(12,'routed node','server-5','routed','vless')`,
		`INSERT INTO packages VALUES(1,'[10]'),(2,'[11]')`,
		`INSERT INTO user_package_assignments VALUES(1,'alice',2,'active')`,
		`INSERT INTO user_inbound_configs VALUES('alice',5,'inbound','vless','{"id":"alice-uuid","email":"alice__inbound"}'),('admin',5,'inbound','vless','{"id":"admin-uuid","email":"admin__inbound"}')`,
		`INSERT INTO package_assignment_inbound_configs VALUES('alice',5,'assignment','vless','alice__pkg2__assignment','{"id":"assignment-uuid"}')`,
		`INSERT INTO user_subaccounts VALUES('alice',12,'alice__routed','{"id":"routed-uuid"}')`,
		`INSERT INTO package_assignment_subaccounts VALUES('bob',12,'alice__routed','{"id":"routed-uuid"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	store := &postgresAdminSessionStore{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := store.persistentAccessData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !data.Admins["admin"] || !data.NodeUsers["alice"][10] || !data.NodeUsers["alice"][11] || !data.NodeUsers["alice"][12] {
		t.Fatalf("missing official entitlements: %+v", data.NodeUsers)
	}
	var assignment []lifecycleCredentialRef
	for _, ref := range data.Refs["alice"] {
		if ref.InboundTag == "assignment" {
			assignment = append(assignment, ref)
		}
	}
	got, _, reason := persistentAccessIdentities("alice", nil, assignment, nil, data.businessRefs(5, "assignment", "alice"), nil)
	if len(got) != 1 || got[0].User != "alice__pkg2__assignment" || reason != "" {
		t.Fatalf("official credential label did not survive absent account email/current client: %+v reason=%s", got, reason)
	}
	for _, email := range []string{"", "unrelated-account@example.test"} {
		if email == "" {
			if _, err := db.Exec(`ALTER TABLE users ADD COLUMN email text NOT NULL DEFAULT ''`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`UPDATE users SET email=$1`, email); err != nil {
			t.Fatal(err)
		}
		other, err := store.persistentAccessData(ctx)
		if err != nil || !reflect.DeepEqual(data.Refs, other.Refs) {
			t.Fatalf("account email changed credential references: %v", err)
		}
	}
}
