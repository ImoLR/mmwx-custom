package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type lifecycleQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type lifecyclePackageRecheck func(context.Context, lifecycleQueryer, []int64) error

type lifecycleNodeLabel struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type lifecycleDeletionNode struct {
	lifecycleNodeLabel
	Username   string
	ServerID   int64
	ServerName string
	InboundTag string
}

type lifecycleDeletionData struct {
	Nodes    map[int64]lifecycleDeletionNode
	Packages []lifecyclePackageBinding
	Refs     []lifecycleCredentialRef
	Admins   map[string]bool
	Defaults map[string][]map[string]any
}

func lifecycleInboundKey(serverID int64, tag string) string {
	return strconv.FormatInt(serverID, 10) + "\x00" + tag
}

func lifecyclePackageBindings(ctx context.Context, db lifecycleQueryer, username string) ([]lifecyclePackageBinding, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.id,COALESCE(p.name,''),COALESCE(p.nodes,'[]'),
		EXISTS(SELECT 1 FROM user_package_assignments a WHERE a.package_id=p.id AND a.username=$1) OR EXISTS(SELECT 1 FROM users u WHERE u.package_id=p.id AND u.username=$1),
		EXISTS(SELECT 1 FROM user_package_assignments a WHERE a.package_id=p.id AND a.username<>$1) OR EXISTS(SELECT 1 FROM users u WHERE u.package_id=p.id AND u.username<>$1)
		FROM packages p ORDER BY p.id`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []lifecyclePackageBinding
	for rows.Next() {
		var pkg lifecyclePackageBinding
		var raw string
		if err := rows.Scan(&pkg.ID, &pkg.Name, &raw, &pkg.Bound, &pkg.BindingConflict); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &pkg.NodeIDs) != nil {
			return nil, fmt.Errorf("套餐 %d 节点列表无效", pkg.ID)
		}
		result = append(result, pkg)
	}
	return result, rows.Err()
}

func (s *postgresAdminSessionStore) LifecycleDeletionData(ctx context.Context, username string) (lifecycleDeletionData, error) {
	return readLifecycleDeletionData(ctx, s.db, username)
}

func readLifecycleDeletionData(ctx context.Context, db lifecycleQueryer, username string) (lifecycleDeletionData, error) {
	data := lifecycleDeletionData{Nodes: make(map[int64]lifecycleDeletionNode), Admins: make(map[string]bool), Defaults: make(map[string][]map[string]any)}
	columns, err := lifecycleSchemaColumns(ctx, db)
	if err != nil {
		return data, err
	}
	if hasLifecycleColumns(columns, "packages", "nodes") && hasLifecycleColumns(columns, "user_package_assignments", "package_id") {
		data.Packages, err = lifecyclePackageBindings(ctx, db, username)
		if err != nil {
			return data, err
		}
	}
	if hasLifecycleColumns(columns, "nodes", "id", "original_server", "inbound_tag") {
		rows, err := db.QueryContext(ctx, `SELECT n.id,COALESCE(to_jsonb(n)->>'node_name',to_jsonb(n)->>'name',''),COALESCE(to_jsonb(n)->>'username',''),COALESCE(s.id,0),COALESCE(n.original_server,''),COALESCE(n.inbound_tag,'') FROM nodes n LEFT JOIN remote_servers s ON s.name=n.original_server ORDER BY n.id`)
		if err != nil {
			return data, err
		}
		for rows.Next() {
			var node lifecycleDeletionNode
			if err := rows.Scan(&node.ID, &node.Name, &node.Username, &node.ServerID, &node.ServerName, &node.InboundTag); err != nil {
				rows.Close()
				return data, err
			}
			data.Nodes[node.ID] = node
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return data, err
		}
		rows.Close()
	}
	rows, err := db.QueryContext(ctx, `SELECT username,COALESCE(to_jsonb(u)->>'role',''),COALESCE(to_jsonb(u)->>'email','') FROM users u`)
	if err != nil {
		return data, err
	}
	identities := make(map[string]string)
	for rows.Next() {
		var user, role, email string
		if err := rows.Scan(&user, &role, &email); err != nil {
			rows.Close()
			return data, err
		}
		identities[user] = email
		data.Admins[user] = role == "admin"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	for user := range identities {
		for _, spec := range lifecycleCredentialQueries(columns) {
			rows, err := db.QueryContext(ctx, spec.query, user)
			if err != nil {
				return data, err
			}
			for rows.Next() {
				ref := lifecycleCredentialRef{Username: user}
				if err := rows.Scan(&ref.ServerID, &ref.ServerName, &ref.InboundTag, &ref.Protocol, &ref.CredentialRaw, &ref.Identity, &ref.Source); err != nil {
					rows.Close()
					return data, err
				}
				data.Refs = append(data.Refs, ref)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return data, err
			}
			rows.Close()
		}
	}
	// Package membership is only a candidate identity, never proof that a port
	// belongs to a user. It must match a live credential before classification.
	for _, pkg := range data.Packages {
		if !pkg.Bound || identities[username] == "" {
			continue
		}
		for _, id := range pkg.NodeIDs {
			node, ok := data.Nodes[id]
			if ok && node.ServerID > 0 && node.InboundTag != "" {
				data.Refs = append(data.Refs, lifecycleCredentialRef{Username: username, ServerID: node.ServerID, ServerName: node.ServerName, InboundTag: node.InboundTag, Identity: identities[username], Source: "user_package_assignments"})
			}
		}
	}
	for _, node := range data.Nodes {
		key := lifecycleInboundKey(node.ServerID, node.InboundTag)
		if _, ok := data.Defaults[key]; ok || node.ServerID == 0 || node.InboundTag == "" {
			continue
		}
		defaults, err := lifecycleDefaultAdminCredentials(ctx, db, node.ServerID, node.InboundTag)
		if err != nil {
			return data, err
		}
		data.Defaults[key] = defaults
	}
	return data, nil
}

func deletionBusinessRefs(inbound map[string]any, username string, data lifecycleDeletionData, refs []lifecycleCredentialRef) []lifecycleCredentialRef {
	var result []lifecycleCredentialRef
	protocol, _ := inbound["protocol"].(string)
	entries, _, _ := accessInboundCredentialEntries(inbound)
	for _, ref := range refs {
		if ref.Username == username || data.Admins[ref.Username] {
			continue
		}
		if ref.Source == "user_package_assignments" {
			matched := false
			for _, entry := range entries {
				if lifecycleEntryMatchesRefs(entry, inbound, protocol, []lifecycleCredentialRef{ref}) {
					matched = true
				}
			}
			if !matched {
				continue
			}
		}
		result = append(result, ref)
	}
	return result
}

func (a *app) classifyDeletionPackage(ctx context.Context, token, username string, pkg lifecyclePackageBinding, data lifecycleDeletionData, deleted map[int64]lifecycleNodeLabel, configs map[int64]map[string]any) lifecyclePlanItem {
	item := lifecyclePlanItem{ItemKind: lifecycleItemKindPackage, ServerName: pkg.Name, InboundTag: "package:" + strconv.FormatInt(pkg.ID, 10), Protocol: "package", PackageID: pkg.ID, PackageName: pkg.Name, NodeIDs: pkg.NodeIDs, Action: lifecycleActionKeepPackage, Status: lifecycleItemPending}
	for _, id := range pkg.NodeIDs {
		if own, ok := deleted[id]; ok {
			item.OwnNodes = append(item.OwnNodes, own)
			item.DeletedNodeIDs = append(item.DeletedNodeIDs, id)
			continue
		}
		node, exists := data.Nodes[id]
		label := node.lifecycleNodeLabel
		if !exists {
			label = lifecycleNodeLabel{ID: id}
			item.NeutralNodes = append(item.NeutralNodes, label)
			continue
		}
		if node.ServerName == "" || node.InboundTag == "" {
			item.NeutralNodes = append(item.NeutralNodes, label)
			continue
		}
		if node.ServerID == 0 {
			item.UnknownNodes = append(item.UnknownNodes, label)
			continue
		}
		config := configs[node.ServerID]
		if config == nil {
			config, _ = a.fetchOfficialXrayConfig(ctx, token, node.ServerID)
			configs[node.ServerID] = config
		}
		if config == nil {
			item.UnknownNodes = append(item.UnknownNodes, label)
			continue
		}
		inbound := findConfigInbound(config, node.InboundTag)
		if inbound == nil {
			item.UnknownNodes = append(item.UnknownNodes, label)
			continue
		}
		entries, _, err := accessInboundCredentialEntries(inbound)
		if err != nil {
			item.UnknownNodes = append(item.UnknownNodes, label)
			continue
		}
		other, unknown := false, false
		for _, entry := range entries {
			known := false
			for _, ref := range data.Refs {
				if ref.ServerID != node.ServerID || ref.InboundTag != node.InboundTag {
					continue
				}
				if lifecycleEntryMatchesRefs(entry, inbound, fmt.Sprint(inbound["protocol"]), []lifecycleCredentialRef{ref}) {
					known = true
					if ref.Username != username && !data.Admins[ref.Username] {
						other = true
					}
				}
			}
			for _, credential := range data.Defaults[lifecycleInboundKey(node.ServerID, node.InboundTag)] {
				if credentialsMatch(entry, credential, fmt.Sprint(inbound["protocol"])) {
					known = true
				}
			}
			if !known {
				unknown = true
			}
		}
		if other {
			item.OtherUserNodes = append(item.OtherUserNodes, label)
		}
		if unknown {
			item.UnknownNodes = append(item.UnknownNodes, label)
		} else if !other {
			item.NeutralNodes = append(item.NeutralNodes, label)
		}
	}
	if pkg.Bound && pkg.BindingConflict {
		item.Action, item.Status, item.DecisionNote = lifecycleActionConflict, lifecycleItemFailed, "套餐同时绑定其他用户，保留套餐，请先核对绑定关系"
	} else if pkg.Bound && len(item.UnknownNodes) > 0 {
		item.Action, item.Status, item.DecisionNote = lifecycleActionConflict, lifecycleItemFailed, "部分节点无法确认归属，保留套餐，请先核对节点凭据"
	} else if pkg.Bound && len(item.OtherUserNodes) == 0 {
		item.Action, item.DecisionNote = lifecycleActionDeletePackage, "没有其他普通用户的节点，随用户删除套餐"
	} else {
		item.DecisionNote = fmt.Sprintf("保留套餐，移除 %d 个该用户节点", len(item.DeletedNodeIDs))
		if len(item.DeletedNodeIDs) == 0 {
			item.Status = lifecycleItemCompleted
		}
	}
	if item.Action == lifecycleActionConflict {
		item.LastError = item.DecisionNote
	}
	return item
}

func (a *app) executeDeletionPackage(ctx context.Context, token, username string, item *lifecyclePlanItem) error {
	store := a.adminStore.(lifecycleStore)
	if item.Action == lifecycleActionDeletePackage {
		return store.DeleteExclusivePackage(ctx, item.PackageID, username, func(ctx context.Context, db lifecycleQueryer, nodes []int64) error {
			data, err := readLifecycleDeletionData(ctx, db, username)
			if err != nil {
				return err
			}
			current := a.classifyDeletionPackage(ctx, token, username, lifecyclePackageBinding{ID: item.PackageID, Name: item.PackageName, NodeIDs: nodes, Bound: true}, data, nil, make(map[int64]map[string]any))
			if current.Action != lifecycleActionDeletePackage {
				return errors.New("套餐节点归属已变化，" + current.DecisionNote)
			}
			return nil
		})
	}
	if len(item.DeletedNodeIDs) == 0 {
		return nil
	}
	var response struct {
		Packages []map[string]any `json:"packages"`
	}
	if err := a.officialLifecycleJSON(ctx, token, http.MethodGet, "/api/admin/packages", nil, &response); err != nil {
		return err
	}
	var payload map[string]any
	for _, pkg := range response.Packages {
		if fmt.Sprint(pkg["id"]) == strconv.FormatInt(item.PackageID, 10) {
			payload = pkg
			break
		}
	}
	if payload == nil {
		if cleanup, ok := store.(interface {
			PruneLifecycleNodeRelations(context.Context, int64, []int64) error
		}); ok {
			return cleanup.PruneLifecycleNodeRelations(ctx, item.PackageID, item.DeletedNodeIDs)
		}
		return errors.New("未找到需要清理的官方套餐")
	}
	deleted := make(map[string]bool)
	for _, id := range item.DeletedNodeIDs {
		deleted[strconv.FormatInt(id, 10)] = true
	}
	if err := pruneLifecyclePackagePayload(payload, deleted); err != nil {
		return err
	}
	if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "f9bed75c75a38c5f", "payload": payload}, nil); err != nil {
		return err
	}
	if cleanup, ok := store.(interface {
		PruneLifecycleNodeRelations(context.Context, int64, []int64) error
	}); ok {
		return cleanup.PruneLifecycleNodeRelations(ctx, item.PackageID, item.DeletedNodeIDs)
	}
	return nil
}

func pruneLifecyclePackagePayload(payload map[string]any, deleted map[string]bool) error {
	nodes, ok := payload["nodes"].([]any)
	if !ok {
		return errors.New("官方套餐节点列表无效，未更新套餐")
	}
	kept := make([]any, 0, len(nodes))
	for _, id := range nodes {
		if !deleted[fmt.Sprint(id)] {
			kept = append(kept, id)
		}
	}
	payload["nodes"] = kept
	for key, value := range payload {
		if !strings.HasPrefix(key, "node_") {
			continue
		}
		if values, ok := value.(map[string]any); ok {
			for id := range deleted {
				delete(values, id)
			}
		}
	}
	return nil
}

func (s *postgresAdminSessionStore) PruneLifecycleNodeRelations(ctx context.Context, packageID int64, nodeIDs []int64) error {
	columns, err := s.schemaColumns(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if packageID > 0 {
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT to_jsonb(p) FROM packages p WHERE id=$1`, packageID).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			var pkg map[string]any
			if json.Unmarshal(raw, &pkg) != nil {
				return errors.New("无法复核套餐节点清理结果")
			}
			for key, value := range pkg {
				if key != "nodes" && !strings.HasPrefix(key, "node_") {
					continue
				}
				if text, ok := value.(string); ok {
					var decoded any
					if json.Unmarshal([]byte(text), &decoded) == nil {
						value = decoded
					}
				}
				for _, id := range nodeIDs {
					if values, ok := value.(map[string]any); ok {
						if _, exists := values[strconv.FormatInt(id, 10)]; exists {
							return errors.New("官方套餐仍有已删除节点的数据，清理未完成")
						}
					}
					if key == "nodes" {
						if values, ok := value.([]any); ok {
							for _, nodeID := range values {
								if fmt.Sprint(nodeID) == strconv.FormatInt(id, 10) {
									return errors.New("官方套餐仍引用已删除节点，清理未完成")
								}
							}
						}
					}
				}
			}
		}
	}
	for _, id := range nodeIDs {
		if hasLifecycleColumns(columns, "forward_chain_nodes", "node_id") {
			if _, err := tx.ExecContext(ctx, `DELETE FROM forward_chain_nodes WHERE node_id=$1`, id); err != nil {
				return err
			}
		}
		if hasLifecycleColumns(columns, "mmwxc_package_traffic_groups", "package_id", "node_ids") {
			if _, err := tx.ExecContext(ctx, `UPDATE mmwxc_package_traffic_groups SET node_ids=COALESCE((SELECT jsonb_agg(n) FROM jsonb_array_elements(node_ids) n WHERE n<>to_jsonb($2::bigint)),'[]'::jsonb) WHERE (package_id=$1 OR $1=0)`, packageID, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func containsLifecycleNode(ids []int64, id int64) bool {
	for _, current := range ids {
		if current == id {
			return true
		}
	}
	return false
}

func (a *app) finishLifecycleDeletedNodes(ctx context.Context, token, username string, item *lifecyclePlanItem) error {
	if (item.Action != lifecycleActionDeleteWhole && item.Action != lifecycleActionDeleteNode) || len(item.NodeIDs) == 0 {
		return nil
	}
	store := a.adminStore.(lifecycleStore)
	data, err := store.LifecycleDeletionData(ctx, "")
	if err != nil {
		return err
	}
	for _, id := range item.NodeIDs {
		if node, exists := data.Nodes[id]; exists {
			if item.ItemKind == lifecycleItemKindNode {
				if node.Username != username || (node.ServerName != "" && node.InboundTag != "") {
					return errors.New("节点归属已变化，未删除节点记录")
				}
			} else if node.ServerID != item.ServerID || node.InboundTag != item.InboundTag {
				return errors.New("节点归属已变化，未删除节点记录")
			}
			if err := a.officialLifecycleJSON(ctx, token, http.MethodDelete, "/api/admin/nodes/"+strconv.FormatInt(id, 10), nil, nil); err != nil {
				return err
			}
		}
	}
	data, err = store.LifecycleDeletionData(ctx, "")
	if err != nil {
		return err
	}
	for _, id := range item.NodeIDs {
		if _, exists := data.Nodes[id]; exists {
			return errors.New("官方节点记录仍存在，节点清理未完成")
		}
	}
	if cleanup, ok := store.(interface {
		PruneLifecycleNodeRelations(context.Context, int64, []int64) error
	}); ok {
		return cleanup.PruneLifecycleNodeRelations(ctx, 0, item.NodeIDs)
	}
	return nil
}
