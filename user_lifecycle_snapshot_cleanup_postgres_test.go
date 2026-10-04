package main

import (
	"context"
	"testing"
)

func TestLifecycleSchemaClearsOnlyDeletedOperationSnapshotsPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "snapshot-cleanup")
	store := &postgresAdminSessionStore{db: db}
	const snapshot = `{"target_credentials":[{"id":"old-deletion-test-credential"}]}`
	for _, operation := range []struct {
		id    string
		state string
	}{
		{"old-deleted", lifecycleStateDeleted},
		{"new-partial", lifecycleStateDeletePartial},
		{"in-progress", lifecycleStateDeleting},
	} {
		auditDeleteExec(t, db, `INSERT INTO mmwxc_user_lifecycle_operations
			(operation_id,username,operation,state,pending_count,last_error,created_at,updated_at)
			VALUES($1,'alice','delete',$2,1,'history note','2026-09-01T00:00:00Z','2026-09-02T00:00:00Z')`, operation.id, operation.state)
	}
	items := []struct {
		operationID string
		tag         string
		status      string
		snapshot    string
	}{
		{"old-deleted", "completed", lifecycleItemCompleted, snapshot},
		{"old-deleted", "pending", lifecycleItemPending, snapshot},
		{"old-deleted", "already-empty", lifecycleItemCompleted, "{}"},
		{"new-partial", "completed", lifecycleItemCompleted, snapshot},
		{"new-partial", "failed", lifecycleItemFailed, snapshot},
		{"in-progress", "pending", lifecycleItemPending, snapshot},
	}
	for _, item := range items {
		auditDeleteExec(t, db, `INSERT INTO mmwxc_user_lifecycle_items
			(operation_id,server_id,inbound_tag,action,status,attempts,last_error,last_checked_at,created_at,updated_at,deletion_snapshot)
			VALUES($1,5,$2,$3,$4,2,'item history','2026-09-02T00:00:00Z','2026-09-01T00:00:00Z','2026-09-02T00:00:00Z',$5::jsonb)`,
			item.operationID, item.tag, lifecycleActionDeleteWhole, item.status, item.snapshot)
	}

	readHistory := func() (string, string) {
		t.Helper()
		var operations, items string
		if err := db.QueryRow(`SELECT jsonb_agg(to_jsonb(operation) ORDER BY operation_id)::text
			FROM mmwxc_user_lifecycle_operations AS operation`).Scan(&operations); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT jsonb_agg(to_jsonb(item)-'deletion_snapshot' ORDER BY operation_id,server_id,inbound_tag)::text
			FROM mmwxc_user_lifecycle_items AS item`).Scan(&items); err != nil {
			t.Fatal(err)
		}
		return operations, items
	}
	wantOperations, wantItems := readHistory()
	for attempt := 1; attempt <= 2; attempt++ {
		if err := store.EnsureUserLifecycleSchema(context.Background()); err != nil {
			t.Fatalf("ensure attempt %d: %v", attempt, err)
		}
		for _, item := range items {
			wantSnapshot := item.snapshot
			if item.operationID == "old-deleted" {
				wantSnapshot = "{}"
			}
			var matches bool
			if err := db.QueryRow(`SELECT deletion_snapshot=$3::jsonb FROM mmwxc_user_lifecycle_items
				WHERE operation_id=$1 AND server_id=5 AND inbound_tag=$2`, item.operationID, item.tag, wantSnapshot).Scan(&matches); err != nil || !matches {
				t.Fatalf("ensure attempt %d snapshot %s/%s preserved or cleared incorrectly: matches=%t err=%v", attempt, item.operationID, item.tag, matches, err)
			}
		}
		if operations, items := readHistory(); operations != wantOperations || items != wantItems {
			t.Fatalf("ensure attempt %d changed operation/item history beyond snapshots", attempt)
		}
	}
}
