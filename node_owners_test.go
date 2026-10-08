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
		r := httptest.NewRequest(tc.method, "/api/admin/nodes/owners", nil)
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
		`INSERT INTO users(username,role) VALUES('admin','admin'),('wings','user'),('usb','user')`,
		`INSERT INTO remote_servers VALUES(12,'Boil Hinet 158')`,
		`INSERT INTO nodes VALUES(1,'SS2022','Boil Hinet 158','ss',NULL,NULL),(2,'relay','Boil Hinet 158','ss-relay',NULL,'origin'),(3,'external',NULL,NULL,NULL,NULL),(4,'admin','Boil Hinet 158','self',NULL,NULL)`,
		`INSERT INTO user_inbound_configs VALUES('admin',12,'ss','{"password":"default"}'),('admin',12,'self','{}')`,
		`INSERT INTO user_outbounds VALUES('wings',12,'ss')`,
		`INSERT INTO packages VALUES(1,'external','[3]')`,
		`INSERT INTO user_package_assignments VALUES(1,'usb',1,'active')`,
	} {
		auditDeleteExec(t, db, sql)
	}
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
}
