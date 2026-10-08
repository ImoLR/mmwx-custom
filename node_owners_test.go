package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
)

func TestResolveNodeOwners(t *testing.T) {
	base := ownerNode{ID: 1, ServerID: 12, ServerName: "Boil Hinet 158", InboundTag: "ss2022-10015"}
	for _, test := range []struct {
		name string
		refs []ownerRef
		want nodeOwner
	}{
		{"non-admin overrides default admin", []ownerRef{{"admin", 12, base.InboundTag}, {"wings", 12, base.InboundTag}}, nodeOwner{Users: []string{"wings"}, Source: "credential", InboundBacked: true}},
		{"admin alone", []ownerRef{{"admin", 12, base.InboundTag}}, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "credential", InboundBacked: true}},
		{"shared deduped", []ownerRef{{"usb", 12, base.InboundTag}, {"admin", 12, base.InboundTag}, {"wings", 12, base.InboundTag}, {"usb", 12, base.InboundTag}}, nodeOwner{Users: []string{"usb", "wings"}, Shared: true, Source: "credential", InboundBacked: true}},
		{"no data ignores node author", nil, nodeOwner{Users: []string{}, Source: "none", InboundBacked: true}},
		{"credential-less SS2022 whole-port business ref", []ownerRef{{"wings", 12, base.InboundTag}}, nodeOwner{Users: []string{"wings"}, Source: "credential", InboundBacked: true}},
		{"different server and deleted user ignored", []ownerRef{{"wings", 5, base.InboundTag}, {"deleted", 12, base.InboundTag}}, nodeOwner{Users: []string{}, Source: "none", InboundBacked: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := nodeOwnerData{Nodes: []ownerNode{base}, Refs: test.refs, Admins: map[string]bool{"admin": true, "wings": false, "usb": false}}
			got, err := resolveNodeOwners(data)
			if err != nil || !reflect.DeepEqual(got[1], test.want) {
				t.Fatalf("owners=%+v err=%v want=%+v", got, err, test.want)
			}
		})
	}
}

func TestNodeOwnerInheritanceAndPackages(t *testing.T) {
	config := `{"server":"example.test","port":10015,"password":"fixture","name":"original"}`
	data := nodeOwnerData{
		Admins: map[string]bool{"admin": true, "wings": false, "usb": false},
		Nodes: []ownerNode{
			{ID: 1, ServerID: 12, ServerName: "A", InboundTag: "ss", Config: config},
			{ID: 2, ServerID: 12, ServerName: "A", InboundTag: "ss-relay", RelayHost: "example.test"},
			{ID: 3, ParentID: 2},
			{ID: 4, ChainID: 10, Config: `{"name":"chain","port":10015,"password":"fixture","server":"example.test"}`},
			{ID: 5}, {ID: 6}, {ID: 7},
			{ID: 8, ServerID: 12, ServerName: "A", InboundTag: "empty"},
			{ID: 9, ServerID: 12, ServerName: "A", InboundTag: "empty-relay", RelayHost: "example.test"},
			{ID: 10, ServerID: 5, ServerName: "B", InboundTag: "exit"},
		},
		Refs:     []ownerRef{{"wings", 12, "ss"}, {"admin", 12, "ss"}, {"usb", 5, "exit"}},
		Packages: []ownerPackage{{"wings", "[5,6,8]"}, {"usb", "[6]"}},
	}
	got, err := resolveNodeOwners(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2, 3, 4} {
		if !reflect.DeepEqual(got[id].Users, []string{"wings"}) || got[id].Source != "credential" || !got[id].InboundBacked {
			t.Fatalf("source inheritance %d: %+v", id, got[id])
		}
	}
	for _, id := range []int64{5, 8, 9} {
		if !reflect.DeepEqual(got[id].Users, []string{"wings"}) || got[id].Source != "package" {
			t.Fatalf("package %d: %+v", id, got[id])
		}
	}
	for _, id := range []int64{6, 7} {
		if len(got[id].Users) != 0 || got[id].InboundBacked || got[id].Source != "none" {
			t.Fatalf("external %d: %+v", id, got[id])
		}
	}
	data.Packages = []ownerPackage{{"wings", "[]"}}
	got, err = resolveNodeOwners(data)
	if err != nil || got[7].Source != "package" || got[10].Users[0] != "usb" {
		t.Fatalf("all-node package: %+v, %v", got, err)
	}
	data.Packages[0].Nodes = "invalid"
	if _, err := resolveNodeOwners(data); err == nil {
		t.Fatal("invalid membership silently accepted")
	}
}

func TestNodeOwnerSnapshotCredentials(t *testing.T) {
	adminSnapshot := `{"inbounds":[{"tag":"self","protocol":"vless","settings":{"clients":[{"id":"admin-default","email":"MMW@ADMIN.ME"}]}}]}`
	unknownSnapshot := `{"inbounds":[{"tag":"self","protocol":"vless","settings":{"clients":[{"id":"unknown","email":"unrecognized"}]}}]}`
	for _, test := range []struct {
		name     string
		snapshot string
		refs     []ownerRef
		packages []ownerPackage
		want     nodeOwner
	}{
		{"admin default", adminSnapshot, nil, nil, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "credential", InboundBacked: true}},
		{"non-admin ref wins", adminSnapshot, []ownerRef{{"wings", 12, "self"}}, nil, nodeOwner{Users: []string{"wings"}, Source: "credential", InboundBacked: true}},
		{"snapshot wins over package", adminSnapshot, nil, []ownerPackage{{"wings", "[1]"}}, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "credential", InboundBacked: true}},
		{"SS2022 single default password", `{"inbounds":[{"tag":"self","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"admin-key","clients":[],"email":"admin__default"}}]}`, nil, nil, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "credential", InboundBacked: true}},
		{"admin email", `{"inbounds":[{"tag":"self","protocol":"trojan","settings":{"clients":[{"password":"admin-key","email":" Owner@Example.Test "}]}}]}`, nil, nil, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "credential", InboundBacked: true}},
		{"no snapshot or refs", "", nil, nil, nodeOwner{Users: []string{}, Source: "none", InboundBacked: true}},
		{"SS2022 single password without identity defaults to admin", `{"inbounds":[{"tag":"self","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"legacy-key","clients":[]}}]}`, nil, nil, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "default-admin", InboundBacked: true}},
		{"unknown credential defaults to admin", unknownSnapshot, nil, nil, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "default-admin", InboundBacked: true}},
		{"unknown credential allows package fallback", unknownSnapshot, nil, []ownerPackage{{"wings", "[1]"}}, nodeOwner{Users: []string{"wings"}, Source: "package", InboundBacked: true}},
		{"unknown credential allows shared package fallback", unknownSnapshot, nil, []ownerPackage{{"wings", "[1]"}, {"usb", "[2]"}}, nodeOwner{Users: []string{"usb", "wings"}, Shared: true, Source: "package", InboundBacked: true}},
		{"child package prevents default admin", unknownSnapshot, nil, []ownerPackage{{"wings", "[3]"}}, nodeOwner{Users: []string{"wings"}, Source: "package", InboundBacked: true}},
		{"empty inbound defaults to admin", `{"inbounds":[{"tag":"self","protocol":"vless","settings":{"clients":[]}}]}`, nil, nil, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "default-admin", InboundBacked: true}},
		{"two package users without credentials share", `{"inbounds":[{"tag":"self","protocol":"vless","settings":{"clients":[]}}]}`, nil, []ownerPackage{{"wings", "[1]"}, {"usb", "[3]"}}, nodeOwner{Users: []string{"usb", "wings"}, Shared: true, Source: "package", InboundBacked: true}},
		{"admin business match wins over packages", unknownSnapshot, []ownerRef{{"admin", 12, "self"}}, []ownerPackage{{"wings", "[1]"}, {"usb", "[2]"}}, nodeOwner{Users: []string{"admin"}, AdminOnly: true, Source: "credential", InboundBacked: true}},
		{"missing inbound remains unowned", `{"inbounds":[]}`, nil, nil, nodeOwner{Users: []string{}, Source: "none", InboundBacked: true}},
		{"unreadable snapshot remains unowned", `invalid`, nil, nil, nodeOwner{Users: []string{}, Source: "none", InboundBacked: true}},
		{"no credential allows package", `{"inbounds":[]}`, nil, []ownerPackage{{"wings", "[1]"}}, nodeOwner{Users: []string{"wings"}, Source: "package", InboundBacked: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := nodeOwnerData{
				Nodes: []ownerNode{
					{ID: 1, ServerID: 12, ServerName: "A", InboundTag: "self"},
					{ID: 2, ServerID: 12, ServerName: "A", InboundTag: "self-relay", RelayHost: "origin"},
					{ID: 3, ParentID: 2},
				},
				Admins: map[string]bool{"admin": true, "wings": false, "usb": false}, AdminEmails: map[string]string{"admin": "owner@example.test"},
				Snapshots: map[int64]string{12: test.snapshot}, Refs: test.refs, Packages: test.packages,
			}
			got, err := resolveNodeOwners(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{1, 2, 3} {
				want := test.want
				if id > 1 {
					want.ParentNodeID = id - 1
				}
				if !reflect.DeepEqual(got[id], want) {
					t.Fatalf("node %d: %+v want %+v", id, got[id], want)
				}
			}
		})
	}
}

func TestNodeOwnerDefaultAdmins(t *testing.T) {
	data := nodeOwnerData{
		Admins: map[string]bool{"owner": true, "admin": true, "wings": false},
		Nodes: []ownerNode{
			{ID: 2, ParentID: 1},
			{ID: 1, ServerID: 12, ServerName: "A", InboundTag: "self"},
			{ID: 3},
		},
		Snapshots: map[int64]string{12: `{"inbounds":[{"tag":"self","protocol":"shadowsocks","settings":{"password":"legacy-key"}}]}`},
	}
	got, err := resolveNodeOwners(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		want := nodeOwner{Users: []string{"admin", "owner"}, AdminOnly: true, Source: "default-admin", InboundBacked: true}
		if id == 2 {
			want.ParentNodeID = 1
		}
		if !reflect.DeepEqual(got[id], want) {
			t.Fatalf("default admins %d: %+v want %+v", id, got[id], want)
		}
	}
	if want := (nodeOwner{Users: []string{}, Source: "none"}); !reflect.DeepEqual(got[3], want) {
		t.Fatalf("external without package: %+v want %+v", got[3], want)
	}
}

type fakeNodeOwnerStore struct {
	fakeAdminSessionStore
	reads int
}

func (s *fakeNodeOwnerStore) NodeOwners(context.Context) (map[int64]nodeOwner, error) {
	s.reads++
	return map[int64]nodeOwner{1: {Users: []string{"wings"}, Source: "credential"}}, nil
}

func TestNodeOwnersEndpointAuthorizationAndReadOnly(t *testing.T) {
	store := &fakeNodeOwnerStore{}
	target, _ := url.Parse("http://127.0.0.1:1")
	mux := newMux(&app{adminStore: store}, target, t.TempDir())
	for _, tc := range []struct {
		method, token string
		admin         bool
		status        int
	}{
		{"GET", "", false, 401}, {"GET", "ordinary", false, 401}, {"POST", "admin", true, 405}, {"GET", "admin", true, 200},
	} {
		store.authorized = tc.admin
		r := httptest.NewRequest(tc.method, "/api/custom/nodes/owners", nil)
		r.Header.Set("MM-Authorization", tc.token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.method, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var body struct {
				Owners map[string]nodeOwner `json:"owners"`
			}
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Owners["1"].Users[0] != "wings" {
				t.Fatal(w.Body.String())
			}
		}
	}
	if store.reads != 1 {
		t.Fatalf("unauthorized/mutation request accessed ownership data: %d", store.reads)
	}
}

func TestNodeOwnersPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "node-owners")
	for _, sql := range []string{
		`CREATE TABLE nodes(id bigint PRIMARY KEY,node_name text,original_server text,inbound_tag text,parent_node_id bigint,relay_orig_server text)`,
		`CREATE TABLE user_inbound_configs(username text,server_id bigint,inbound_tag text,credential_json text)`,
		`CREATE TABLE user_outbounds(username text,server_id bigint,inbound_tag text)`,
		`CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,source text,created_at timestamptz)`,
		`INSERT INTO users(username,role) VALUES('admin','admin'),('wings','user'),('usb','user')`,
		`INSERT INTO remote_servers VALUES(12,'Boil Hinet 158'),(13,'No snapshot'),(14,'Non-admin only')`,
		`INSERT INTO nodes VALUES(1,'SS2022','Boil Hinet 158','ss',NULL,NULL),(2,'relay','Boil Hinet 158','ss-relay',NULL,'origin'),(3,'external',NULL,NULL,NULL,NULL),(4,'admin','Boil Hinet 158','self',NULL,NULL)`,
		`INSERT INTO nodes VALUES(5,'snapshot admin','Boil Hinet 158','default',NULL,NULL),(6,'relay admin','Boil Hinet 158','default-relay',NULL,'origin'),(7,'SS2022 default','Boil Hinet 158','single',NULL,NULL),(8,'unknown','No snapshot','empty',NULL,NULL),(9,'user','Non-admin only','user',NULL,NULL)`,
		`INSERT INTO nodes VALUES(10,'legacy single password','Boil Hinet 158','legacy',NULL,NULL),(11,'legacy relay','Boil Hinet 158','legacy-relay',NULL,'origin'),(12,'unknown package','Boil Hinet 158','unknown',NULL,NULL),(13,'shared packages','Boil Hinet 158','shared',NULL,NULL),(14,'external no package',NULL,NULL,NULL,NULL)`,
		`INSERT INTO user_inbound_configs VALUES('admin',12,'ss','{"password":"default"}'),('admin',12,'self','{}')`,
		`INSERT INTO user_outbounds VALUES('wings',12,'ss'),('wings',14,'user')`,
		`INSERT INTO packages VALUES(1,'external','[3,5,7]')`,
		`INSERT INTO user_package_assignments VALUES(1,'usb',1,'active')`,
		`INSERT INTO packages VALUES(2,'unknown/shared','[12,13]'),(3,'second shared','[13]')`,
		`INSERT INTO user_package_assignments VALUES(2,'usb',2,'active'),(3,'wings',3,'active')`,
		`INSERT INTO server_xray_config_snapshots VALUES(1,12,'{"inbounds":[]}','master_write','2026-10-01'),(2,12,'{"inbounds":[]}','master_write','2026-10-02'),(3,12,'{"inbounds":[{"tag":"default","protocol":"vless","settings":{"clients":[{"id":"default-id","email":"mmw@admin.me"}]}},{"tag":"ss","protocol":"vless","settings":{"clients":[{"id":"default-id","email":"admin"}]}},{"tag":"single","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"admin-key","email":"admin__default","clients":[]}}]}','agent_sync','2026-10-02'),(4,14,'must not be read','agent_sync','2026-10-02')`,
	} {
		auditDeleteExec(t, db, sql)
	}
	auditDeleteExec(t, db, `UPDATE server_xray_config_snapshots SET config_json=jsonb_set(config_json::jsonb,'{inbounds}',(config_json::jsonb->'inbounds') || '[{"tag":"legacy","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"legacy-key","clients":[]}},{"tag":"unknown","protocol":"vless","settings":{"clients":[{"id":"unknown","email":"unrecognized"}]}},{"tag":"shared","protocol":"vless","settings":{"clients":[]}}]'::jsonb)::text WHERE id=3`)
	// The actual loader must work with writes forbidden and with a whole-port
	// business relation that has no credential_json/email columns at all.
	auditDeleteExec(t, db, `SET default_transaction_read_only=on`)
	t.Cleanup(func() { auditDeleteExec(t, db, `SET default_transaction_read_only=off`) })
	got, err := (&postgresAdminSessionStore{db: db}).NodeOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[1].Users[0] != "wings" || got[2].Users[0] != "wings" || got[3].Users[0] != "usb" || got[3].Source != "package" || !got[4].AdminOnly {
		t.Fatalf("owners: %+v", got)
	}
	for _, id := range []int64{5, 6, 7} {
		if !got[id].AdminOnly || got[id].Source != "credential" || !reflect.DeepEqual(got[id].Users, []string{"admin"}) {
			t.Fatalf("snapshot admin %d: %+v", id, got[id])
		}
	}
	if got[8].Source != "none" || len(got[8].Users) != 0 || got[9].Users[0] != "wings" {
		t.Fatalf("missing/unneeded snapshots: %+v", got)
	}
	for id, want := range map[int64]nodeOwner{
		10: {Users: []string{"admin"}, AdminOnly: true, Source: "default-admin", InboundBacked: true},
		11: {Users: []string{"admin"}, AdminOnly: true, Source: "default-admin", InboundBacked: true, ParentNodeID: 10},
		12: {Users: []string{"usb"}, Source: "package", InboundBacked: true},
		13: {Users: []string{"usb", "wings"}, Shared: true, Source: "package", InboundBacked: true},
		14: {Users: []string{}, Source: "none"},
	} {
		if !reflect.DeepEqual(got[id], want) {
			t.Fatalf("fallback %d: %+v want %+v", id, got[id], want)
		}
	}
	columns, err := (&postgresAdminSessionStore{db: db}).schemaColumns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow(nodeOwnerDataSQL(columns)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var data nodeOwnerData
	if err := json.Unmarshal(raw, &data); err != nil || len(data.Snapshots) != 1 || data.Snapshots[12] == "" {
		t.Fatalf("only one needed server snapshot should be loaded: count=%d err=%v", len(data.Snapshots), err)
	}
}
