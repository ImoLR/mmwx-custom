package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type importedNodeCleanupSnapshot struct {
	Packages []lifecyclePackageBinding
	NodeIDs  []int64
}

func (s *postgresAdminSessionStore) importedNodeCleanupSnapshot(ctx context.Context, username string) (string, importedNodeCleanupSnapshot, error) {
	var id string
	var raw []byte
	var snapshot importedNodeCleanupSnapshot
	err := s.db.QueryRowContext(ctx, `SELECT o.operation_id,i.deletion_snapshot
		FROM mmwxc_user_lifecycle_operations o
		JOIN mmwxc_user_lifecycle_items i ON i.operation_id=o.operation_id
		WHERE o.username=$1 AND o.operation='clear_imported_nodes' AND o.state='pending'
		ORDER BY o.created_at LIMIT 1`, username).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", snapshot, nil
	}
	if err != nil {
		return "", snapshot, err
	}
	if json.Unmarshal(raw, &snapshot) != nil || len(snapshot.NodeIDs) == 0 {
		return "", snapshot, errors.New("导入节点清理快照无效，请先核对套餐")
	}
	return id, snapshot, nil
}

func (s *postgresAdminSessionStore) saveImportedNodeCleanup(ctx context.Context, username string, snapshot importedNodeCleanupSnapshot) (string, error) {
	id, err := newManagedUserStatusTaskID()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO mmwxc_user_lifecycle_operations(operation_id,username,operation,state,pending_count)
		VALUES($1,$2,'clear_imported_nodes','pending',1)`, id, username); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO mmwxc_user_lifecycle_items(operation_id,server_id,inbound_tag,action,deletion_snapshot)
		VALUES($1,0,'imported-nodes','CLEAR_IMPORTED_NODES',$2::jsonb)`, id, string(raw)); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (s *postgresAdminSessionStore) finishImportedNodeCleanup(ctx context.Context, id string, cleanupErr error) error {
	if cleanupErr != nil {
		_, err := s.db.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET last_error=$2,updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, id, cleanupErr.Error())
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_items SET status='completed',deletion_snapshot='{}'::jsonb,updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET state='completed',pending_count=0,last_error='',updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *app) clearManagedUserImportedNodes(ctx context.Context, token, username string, store *postgresAdminSessionStore) (result map[string]any, resultErr error) {
	var imported struct {
		Nodes []struct {
			ID int64 `json:"id"`
		} `json:"nodes"`
	}
	if err := a.officialLifecycleJSON(ctx, token, http.MethodGet, "/api/admin/nodes/user-imported?username="+url.QueryEscape(username), nil, &imported); err != nil {
		return nil, err
	}
	id, snapshot, err := store.importedNodeCleanupSnapshot(ctx, username)
	if err != nil {
		return nil, err
	}
	current := make(map[int64]bool)
	for _, node := range imported.Nodes {
		current[node.ID] = true
	}
	if id == "" {
		if len(imported.Nodes) == 0 {
			return map[string]any{"success": true, "deleted_count": 0}, nil
		}
		for _, node := range imported.Nodes {
			snapshot.NodeIDs = append(snapshot.NodeIDs, node.ID)
		}
		snapshot.Packages, err = lifecyclePackageBindings(ctx, store.db, "")
		if err != nil {
			return nil, err
		}
		id, err = store.saveImportedNodeCleanup(ctx, username, snapshot)
		if err != nil {
			return nil, fmt.Errorf("保存导入节点清理快照失败：%w", err)
		}
	}
	defer func() {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := store.finishImportedNodeCleanup(finishCtx, id, resultErr); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("保存节点清理结果失败，请重试：%w", err))
		}
	}()
	ids := []int64{}
	for _, nodeID := range snapshot.NodeIDs {
		if current[nodeID] {
			ids = append(ids, nodeID)
		}
	}
	var clearErr error
	result = map[string]any{"success": true}
	if len(ids) > 0 {
		clearErr = a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/admin/nodes/batch-delete", map[string]any{"node_ids": ids}, &result)
	}
	// Upstream may delete some nodes before failing. Preserve the original
	// package memberships until every disappeared node has been cleaned up.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	deleted := []int64{}
	for _, nodeID := range snapshot.NodeIDs {
		var exists bool
		if err := store.db.QueryRowContext(cleanupCtx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1)`, nodeID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("节点清空后核对失败，请重试：%w", err)
		}
		if !exists {
			deleted = append(deleted, nodeID)
		} else if !current[nodeID] {
			clearErr = errors.Join(clearErr, errors.New("导入节点归属已变化，已保留该节点，请核对后重试"))
		}
	}
	if err := a.cleanupLifecycleDeletedNodePackages(cleanupCtx, token, snapshot.Packages, deleted); err != nil {
		return nil, fmt.Errorf("节点已删除，但套餐清理未完成，请重试清理：%w", err)
	}
	if clearErr != nil {
		return nil, clearErr
	}
	if len(deleted) != len(snapshot.NodeIDs) {
		return nil, errors.New("部分导入节点未删除，请刷新后重试")
	}
	result["deleted_count"] = len(deleted)
	return result, nil
}
