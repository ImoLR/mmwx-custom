package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// Like the user lifecycle sweep, these are relation/credential/traffic rows,
// never the official server or node records. Those are deleted by public ops.
var serverDeletionRelations = []struct {
	table, column string
	node          bool
}{
	{"batch_inbounds", "server_id", false}, {"batch_outbounds", "server_id", false},
	{"federated_servers", "server_id", false}, {"firewall_whitelist", "server_id", false},
	{"shared_servers", "server_id", false}, {"server_group_members", "server_id", false},
	{"server_return_routes", "server_id", false}, {"server_unlocks", "server_id", false},
	{"server_system_traffic_snapshots", "server_id", false}, {"server_xray_config_snapshots", "server_id", false},
	{"traffic_threshold_notified", "server_id", false}, {"node_traffic", "server_id", false},
	{"node_traffic_snapshots", "server_id", false}, {"traffic_snapshots", "server_id", false},
	{"traffic_daily_nodes", "server_id", false}, {"traffic_daily_system_servers", "server_id", false},
	{"traffic_daily_users", "server_id", false}, {"traffic_daily_user_emails", "server_id", false},
	{"traffic_daily_user_nodes", "server_id", false}, {"traffic_daily_user_nodes", "node_id", true},
	{"user_email_traffic", "server_id", false}, {"user_email_traffic_snapshots", "server_id", false},
	{"user_traffic", "server_id", false}, {"user_traffic_snapshots", "server_id", false},
	{"user_conn_ip_history", "server_id", false}, {"user_conn_ip_history", "node_id", true},
	{"user_speed_peaks", "server_id", false}, {"user_speed_peaks", "node_id", true},
	{"auto_limit_events", "server_id", false}, {"forward_daily_traffic", "server_id", false},
	{"forward_hop_metrics", "server_id", false}, {"forward_chain_branches", "server_id", false},
	{"forward_group_members", "server_id", false}, {"forward_chain_nodes", "node_id", true},
	{"user_inbound_configs", "server_id", false}, {"package_assignment_inbound_configs", "server_id", false},
	{"user_outbounds", "server_id", false}, {"user_subaccounts", "routed_node_id", true},
	{"package_assignment_subaccounts", "routed_node_id", true}, {"package_user_node_traffic_baselines", "node_id", true},
	{"package_node_traffic_suspensions", "node_id", true}, {"node_reachability", "node_id", true},
	{"node_unlocks", "node_id", true}, {"speed_test_results", "node_id", true},
	{"wg_devices", "server_id", false}, {"mmwxc_connection_assignments", "server_id", false},
	{"mmwxc_user_disable_blocks", "server_id", false}, {"mmwxc_user_disabled_credentials", "server_id", false},
}

func deletionPlanIDs(p *serverDeletionPlan) []int64 {
	ids := []int64{}
	for _, n := range p.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

func (s *postgresAdminSessionStore) sweepServerDeletion(ctx context.Context, p *serverDeletionPlan) error {
	columns, err := s.schemaColumns(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids := deletionPlanIDs(p)
	for _, spec := range serverDeletionRelations {
		if !hasLifecycleColumns(columns, spec.table, spec.column) {
			continue
		}
		targets := []int64{p.ServerID}
		if spec.node {
			targets = ids
		}
		for _, id := range targets {
			if _, err = tx.ExecContext(ctx, `DELETE FROM `+spec.table+` WHERE `+spec.column+`=$1`, id); err != nil {
				return fmt.Errorf("清理 %s: %w", spec.table, err)
			}
		}
	}
	for _, inbound := range p.Inbounds {
		for _, table := range []string{"user_inbound_configs", "package_assignment_inbound_configs", "user_outbounds", "mmwxc_connection_assignments", "mmwxc_user_disable_blocks", "mmwxc_user_disabled_credentials"} {
			if hasLifecycleColumns(columns, table, "server_id", "inbound_tag") {
				if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE server_id=$1 AND inbound_tag=$2`, inbound.ServerID, inbound.Tag); err != nil {
					return err
				}
			}
		}
	}
	for _, ref := range p.Identities {
		for _, spec := range []struct{ table, column, value string }{{"mmwxc_connection_assignments", "protocol_identity", ref.Identity}, {"mmwxc_user_disable_blocks", "identity", ref.Identity}, {"mmwxc_user_disabled_credentials", "credential_key", ref.CredentialKey}} {
			if spec.value != "" && hasLifecycleColumns(columns, spec.table, "server_id", "inbound_tag", spec.column) {
				if _, err = tx.ExecContext(ctx, `DELETE FROM `+spec.table+` WHERE server_id=$1 AND inbound_tag=$2 AND `+spec.column+`=$3`, ref.ServerID, ref.Tag, spec.value); err != nil {
					return err
				}
			}
		}
	}

	if columns["mmwxc_package_traffic_groups"] != nil {
		for _, id := range ids {
			if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_package_traffic_groups SET node_ids=COALESCE((SELECT jsonb_agg(n) FROM jsonb_array_elements(node_ids) n WHERE n<>to_jsonb($1::bigint)),'[]'::jsonb),updated_at=CURRENT_TIMESTAMP WHERE node_ids @> jsonb_build_array($1::bigint)`, id); err != nil {
				return err
			}
		}
		// Delete only groups affected by this plan, including groups already trimmed
		// to [] by the reused user lifecycle package cleanup.
		for _, group := range p.Groups {
			if _, err = tx.ExecContext(ctx, `DELETE FROM mmwxc_package_traffic_groups WHERE id=$1 AND node_ids='[]'::jsonb`, group.ID); err != nil {
				return err
			}
		}
	}
	// Per-node user overrides and ordering must not retain retired identities.
	for _, id := range ids {
		for _, column := range []string{"node_speed_limit_overrides", "node_device_limit_overrides"} {
			if hasLifecycleColumns(columns, "users", column) {
				if _, err = tx.ExecContext(ctx, `UPDATE users SET `+column+`=(COALESCE(NULLIF(`+column+`,''),'{}')::jsonb-$1)::text WHERE COALESCE(NULLIF(`+column+`,''),'{}')::jsonb ? $1`, strconv.FormatInt(id, 10)); err != nil {
					return err
				}
			}
		}
		if hasLifecycleColumns(columns, "user_settings", "node_order") {
			if _, err = tx.ExecContext(ctx, `UPDATE user_settings SET node_order=COALESCE((SELECT jsonb_agg(n) FROM jsonb_array_elements(node_order::jsonb) n WHERE n<>to_jsonb($1::bigint)),'[]'::jsonb)::text WHERE node_order::jsonb @> jsonb_build_array($1::bigint)`, id); err != nil {
				return err
			}
		}
	}

	if columns["mmwxc_ui_preferences"] != nil {
		rows, err := tx.QueryContext(ctx, `SELECT username,data_json FROM mmwxc_ui_preferences WHERE preference_key=$1 FOR UPDATE`, serviceGroupsPreferenceKey)
		if err != nil {
			return err
		}
		type pref struct {
			user string
			doc  serviceGroupsDocument
		}
		updates := []pref{}
		for rows.Next() {
			var user string
			var raw []byte
			if err = rows.Scan(&user, &raw); err != nil {
				break
			}
			var doc serviceGroupsDocument
			if err = json.Unmarshal(raw, &doc); err != nil {
				break
			}
			changed := false
			for i := range doc.Groups {
				kept := []int64{}
				for _, id := range doc.Groups[i].ServerIDs {
					if id == p.ServerID {
						changed = true
					} else {
						kept = append(kept, id)
					}
				}
				doc.Groups[i].ServerIDs = kept
			}
			if changed {
				updates = append(updates, pref{user, doc})
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		for _, u := range updates {
			raw, _ := json.Marshal(u.doc)
			if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_ui_preferences SET data_json=$2::jsonb,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE username=$1 AND preference_key=$3`, u.user, string(raw), serviceGroupsPreferenceKey); err != nil {
				return err
			}
		}
	}
	if columns["mmwxc_user_lifecycle_items"] != nil {
		if err = retireServerDeletionLifecycle(ctx, tx, p); err != nil {
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *postgresAdminSessionStore) verifyServerDeletion(ctx context.Context, p *serverDeletionPlan) error {
	columns, err := s.schemaColumns(ctx)
	if err != nil {
		return err
	}
	ids := deletionPlanIDs(p)
	for _, spec := range serverDeletionRelations {
		if !hasLifecycleColumns(columns, spec.table, spec.column) {
			continue
		}
		targets := []int64{p.ServerID}
		if spec.node {
			targets = ids
		}
		for _, id := range targets {
			var count int
			err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+spec.table+` WHERE `+spec.column+`=$1`, id).Scan(&count)
			if err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("仍有 %s.%s 关联记录", spec.table, spec.column)
			}
		}
	}
	for _, pkg := range p.Packages {
		var raw string
		err = s.db.QueryRowContext(ctx, `SELECT COALESCE(nodes,'[]') FROM packages WHERE id=$1`, pkg.ID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if len(deletionIDs(raw)) == 0 {
			return fmt.Errorf("套餐 %s 被清空但未删除", pkg.Name)
		}
	}
	for _, group := range p.Groups {
		var raw []byte
		err = s.db.QueryRowContext(ctx, `SELECT node_ids FROM mmwxc_package_traffic_groups WHERE id=$1`, group.ID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var got []int64
		if json.Unmarshal(raw, &got) != nil || len(got) == 0 {
			return errors.New("流量组清理未完成")
		}
		for _, id := range got {
			if containsLifecycleNode(ids, id) {
				return errors.New("流量组仍包含节点 " + strconv.FormatInt(id, 10))
			}
		}
	}
	for _, spec := range []struct{ table, column string }{{"nodes", "parent_node_id"}, {"nodes", "chain_proxy_node_id"}, {"nodes", "routed_target_node_id"}} {
		if hasLifecycleColumns(columns, spec.table, spec.column) {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+spec.table+` WHERE `+spec.column+`=ANY($1)`, ids).Scan(&count); err != nil {
				return err
			} else if count > 0 {
				return fmt.Errorf("仍有 %s 依赖已删除节点", spec.column)
			}
		}
	}
	for _, spec := range []struct{ table, column string }{{"nodes", "relay_group_node_ids"}, {"subscribe_files", "selected_node_ids"}, {"user_settings", "node_order"}} {
		if hasLifecycleColumns(columns, spec.table, spec.column) {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+spec.table+` WHERE EXISTS(SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(`+spec.column+`,''),'[]')::jsonb) n WHERE n::bigint=ANY($1))`, ids).Scan(&count); err != nil {
				return err
			} else if count > 0 {
				return fmt.Errorf("仍有 %s.%s 引用已删除节点", spec.table, spec.column)
			}
		}
	}
	if hasLifecycleColumns(columns, "certificates", "remote_server_id", "auto_renew", "auto_deploy") {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM certificates WHERE remote_server_id=$1 AND (auto_renew<>0 OR auto_deploy<>0)`, p.ServerID).Scan(&count); err != nil {
			return err
		} else if count > 0 {
			return errors.New("证书仍有依赖此服务器的自动任务")
		}
	}
	return nil
}
