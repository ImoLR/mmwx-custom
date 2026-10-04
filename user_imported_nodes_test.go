package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestImportedNodeCleanupRetriesAfterNodesDisappearPostgres(t *testing.T) {
	db := auditUserDeleteDB(t, "import-cleanup-retry")
	configs := seedDeletionNodes(t, db)
	auditDeleteExec(t, db, `INSERT INTO packages(id,name,nodes) VALUES(1,'kept','[10,13]'),(2,'emptied','[10]'),(3,'all nodes','[]')`)
	application, fixture := auditDeletionApplication(t, db, configs)
	original := fixture.extraHandler
	failed, deletes := false, 0
	fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/api/admin/nodes/user-imported" {
			if r.URL.Query().Get("username") != "alice" {
				t.Error("wrong imported-node owner")
			}
			var exists bool
			if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM nodes WHERE id=10)`).Scan(&exists); err != nil {
				t.Error(err)
			}
			nodes := []map[string]any{}
			if exists {
				nodes = append(nodes, map[string]any{"id": 10, "node_name": "Imported"})
			}
			writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
			return true
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/admin/nodes/batch-delete" {
			var body struct {
				NodeIDs []int64 `json:"node_ids"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.NodeIDs) != 1 || body.NodeIDs[0] != 10 {
				t.Error("unexpected deletion targets")
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false})
				return true
			}
			deletes++
			if _, err := db.Exec(`DELETE FROM nodes WHERE id=10`); err != nil {
				t.Error(err)
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})
			return true
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v3" && !failed {
			failed = true
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false})
			return true
		}
		return original(w, r)
	}
	store := application.adminStore.(*postgresAdminSessionStore)
	ctx := context.Background()
	if _, err := application.clearManagedUserImportedNodes(ctx, "session", "alice", store); err == nil {
		t.Fatal("failed package update should keep cleanup pending")
	}
	id, snapshot, err := store.importedNodeCleanupSnapshot(ctx, "alice")
	if err != nil || id == "" || len(snapshot.NodeIDs) != 1 || snapshot.NodeIDs[0] != 10 {
		t.Fatalf("pending snapshot lost: id=%q snapshot=%+v err=%v", id, snapshot, err)
	}
	var emptied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM packages WHERE id=2`).Scan(&emptied); err != nil || emptied != 0 {
		t.Fatalf("empty package must still be removed: count=%d err=%v", emptied, err)
	}
	if deletion, err := store.LatestDeleteOperation(ctx, "alice"); err != nil || deletion != "" {
		t.Fatalf("import cleanup confused with user deletion: %q %v", deletion, err)
	}
	if _, err := application.clearManagedUserImportedNodes(ctx, "session", "alice", store); err != nil {
		t.Fatalf("retry failed after imported list became empty: %v", err)
	}
	if deletes != 1 {
		t.Fatalf("retry repeated node deletion: %d", deletes)
	}
	if pending, _, err := store.importedNodeCleanupSnapshot(ctx, "alice"); err != nil || pending != "" {
		t.Fatalf("cleanup still pending: %q %v", pending, err)
	}
	var nodes, rawSnapshot string
	if err := db.QueryRow(`SELECT nodes FROM packages WHERE id=1`).Scan(&nodes); err != nil || nodes != "[13]" {
		t.Fatalf("kept package nodes=%s err=%v", nodes, err)
	}
	if err := db.QueryRow(`SELECT nodes FROM packages WHERE id=3`).Scan(&nodes); err != nil || nodes != "[]" {
		t.Fatalf("intentional all-nodes package changed: %s %v", nodes, err)
	}
	if err := db.QueryRow(`SELECT deletion_snapshot::text FROM mmwxc_user_lifecycle_items WHERE operation_id=$1`, id).Scan(&rawSnapshot); err != nil || rawSnapshot != "{}" {
		t.Fatalf("successful cleanup retained snapshot: %s %v", rawSnapshot, err)
	}
	if states, err := store.LifecycleStates(ctx); err != nil || len(states) != 0 {
		t.Fatalf("import cleanup changed user lifecycle: %v %v", states, err)
	}
}
