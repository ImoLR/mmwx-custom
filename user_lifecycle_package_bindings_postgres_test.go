package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestLifecyclePackageBindingsIsolatedPostgresSingleConnection(t *testing.T) {
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
	schema := fmt.Sprintf("mmwxc_lifecycle_bindings_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE users(username text PRIMARY KEY,package_id bigint)`,
		`CREATE TABLE packages(id bigint PRIMARY KEY,name text,nodes text)`,
		`CREATE TABLE user_package_assignments(id bigint PRIMARY KEY,username text,package_id bigint,status text)`,
		`INSERT INTO packages VALUES(1,'assigned','[65,76]'),(2,'legacy','[]'),(3,'unbound','[99]')`,
		`INSERT INTO users VALUES('alice',2),('bob',1),('erin',1),('grace',2)`,
		`INSERT INTO user_package_assignments VALUES(1,'alice',1,'active'),(2,'bob',1,'active'),(3,'bob',1,'active'),(4,'carol',1,NULL),(5,'dave',1,'inactive'),(6,'frank',2,'active'),(7,'alice',1,'active')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture: %v\n%s", err, statement)
		}
	}
	store := &postgresAdminSessionStore{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bindings, err := store.LifecyclePackageBindings(ctx, "alice")
	if err != nil {
		t.Fatalf("package bindings with one connection: %v", err)
	}
	want := []lifecyclePackageBinding{
		{ID: 1, Name: "assigned", NodeIDs: []int64{65, 76}, Bound: true, BindingConflict: true},
		{ID: 2, Name: "legacy", NodeIDs: []int64{}, Bound: true, BindingConflict: true},
	}
	if !reflect.DeepEqual(bindings, want) {
		t.Fatalf("package bindings = %+v, want %+v", bindings, want)
	}
}
