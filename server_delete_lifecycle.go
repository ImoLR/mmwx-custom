package main

import (
	"context"
	"database/sql"
	"encoding/json"
)

func pruneDeletionSnapshotNodeIDs(value any, deleted []int64) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if key == "NodeIDs" || key == "node_ids" || key == "deleted_node_ids" {
				if values, ok := item.([]any); ok {
					kept := []any{}
					for _, id := range values {
						if !containsLifecycleNode(deleted, deletionID(id)) {
							kept = append(kept, id)
						}
					}
					v[key] = kept
				}
			} else {
				pruneDeletionSnapshotNodeIDs(item, deleted)
			}
		}
	case []any:
		for _, item := range v {
			pruneDeletionSnapshotNodeIDs(item, deleted)
		}
	}
}

func retireServerDeletionLifecycle(ctx context.Context, tx *sql.Tx, p *serverDeletionPlan) error {
	rows, err := tx.QueryContext(ctx, `SELECT to_jsonb(i),o.operation FROM mmwxc_user_lifecycle_items i JOIN mmwxc_user_lifecycle_operations o USING(operation_id) WHERE i.status<>'completed' OR i.server_id=$1 FOR UPDATE OF i`, p.ServerID)
	if err != nil {
		return err
	}
	type change struct {
		operationID, tag, status, operation string
		serverID                            int64
		snapshot                            []byte
		ids                                 []int64
		completed                           bool
	}
	changes := []change{}
	ids := deletionPlanIDs(p)
	for rows.Next() {
		var raw []byte
		var operation string
		if err = rows.Scan(&raw, &operation); err != nil {
			break
		}
		var row map[string]any
		if err = json.Unmarshal(raw, &row); err != nil {
			break
		}
		snapshot := deletionObject(row["deletion_snapshot"])
		old, _ := json.Marshal(snapshot)
		nodeIDs := deletionIDs(row["node_ids"])
		sid, tag := deletionID(row["server_id"]), deletionText(row["inbound_tag"])
		retired := sid == p.ServerID
		for _, inbound := range p.Inbounds {
			retired = retired || (sid == inbound.ServerID && tag == inbound.Tag)
		}
		if row["item_kind"] == lifecycleItemKindNode && lifecyclePackageEmptied(nodeIDs, ids) {
			retired = true
		}
		if operation == "clear_imported_nodes" {
			pruneDeletionSnapshotNodeIDs(snapshot, ids)
			if len(deletionIDs(snapshot["NodeIDs"])) == 0 {
				retired = true
			}
		}
		if retired {
			// Preserve the non-secret Item so another user-delete operation can resume.
			// Clearing the entire snapshot would make LifecycleDeleteItems reject it.
			item := map[string]any{}
			for key, value := range row {
				if key != "deletion_snapshot" {
					item[key] = value
				}
			}
			item["status"] = lifecycleItemCompleted
			snapshot = map[string]any{"Item": item}
		} else {
			pruneDeletionSnapshotNodeIDs(snapshot, ids)
		}
		next, _ := json.Marshal(snapshot)
		if string(old) == string(next) && !retired {
			continue
		}
		kept := []int64{}
		for _, id := range nodeIDs {
			if !containsLifecycleNode(ids, id) {
				kept = append(kept, id)
			}
		}
		status := deletionText(row["status"])
		if retired {
			status = lifecycleItemCompleted
		}
		changes = append(changes, change{deletionText(row["operation_id"]), tag, status, operation, sid, next, kept, retired})
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	for _, change := range changes {
		raw, _ := json.Marshal(change.ids)
		if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_items SET status=$4,deletion_snapshot=$5::jsonb,node_ids=$6::jsonb,last_error=CASE WHEN $7 THEN '' ELSE last_error END,decision_note=CASE WHEN $7 THEN '服务器或节点已删除，此项已退役' ELSE decision_note END,updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1 AND server_id=$2 AND inbound_tag=$3`, change.operationID, change.serverID, change.tag, change.status, string(change.snapshot), string(raw), change.completed); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET pending_count=(SELECT count(*) FROM mmwxc_user_lifecycle_items WHERE operation_id=$1 AND status<>'completed'),updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, change.operationID); err != nil {
			return err
		}
		if change.operation == "clear_imported_nodes" && change.completed {
			if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET state='completed',last_error='' WHERE operation_id=$1 AND pending_count=0`, change.operationID); err != nil {
				return err
			}
		}
	}
	return nil
}
