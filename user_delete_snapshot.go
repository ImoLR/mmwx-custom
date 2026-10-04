package main

import (
	"context"
	"encoding/json"
	"errors"
)

// Credentials stay in the database snapshot, never in the preview response.
type lifecycleDeletionSnapshot struct {
	Item            lifecyclePlanItem
	Refs            []lifecycleCredentialRef
	Targets         []map[string]any
	NonTargetHashes []string
	DefaultHashes   []string
}

func (s *postgresAdminSessionStore) LifecycleDeleteItems(ctx context.Context, username string) ([]lifecyclePlanItem, error) {
	operationID, err := s.LatestDeleteOperation(ctx, username)
	if err != nil || operationID == "" {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT deletion_snapshot,status FROM mmwxc_user_lifecycle_items WHERE operation_id=$1`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []lifecyclePlanItem
	for rows.Next() {
		var raw []byte
		var status string
		if err := rows.Scan(&raw, &status); err != nil {
			return nil, err
		}
		var snapshot lifecycleDeletionSnapshot
		if json.Unmarshal(raw, &snapshot) != nil {
			return nil, errors.New("删除目标快照损坏，停止删除")
		}
		item := snapshot.Item
		if item.InboundTag == "" {
			return nil, errors.New("旧删除任务缺少目标快照，请核对远程节点后重试")
		}
		item.Status = status
		item.deleteRefs = snapshot.Refs
		item.targetCredentials = snapshot.Targets
		item.nonTargetHashes = snapshot.NonTargetHashes
		item.defaultCredentialHashes = snapshot.DefaultHashes
		items = append(items, item)
	}
	return items, rows.Err()
}
