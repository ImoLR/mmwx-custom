package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Account contact email is deliberately absent: only Core client labels and
// credential relationships can identify a connection.
type persistentAccessData struct {
	Refs      map[string][]lifecycleCredentialRef
	Nodes     map[int64]trafficGroupNode
	NodeUsers map[string]map[int64]bool
	Admins    map[string]bool
}

func (s *postgresAdminSessionStore) persistentAccessData(ctx context.Context) (persistentAccessData, error) {
	data := persistentAccessData{Refs: map[string][]lifecycleCredentialRef{}, Nodes: map[int64]trafficGroupNode{}, NodeUsers: map[string]map[int64]bool{}, Admins: map[string]bool{}}
	columns, err := s.schemaColumns(ctx)
	if err != nil {
		return data, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT username,COALESCE(to_jsonb(u)->>'role','') FROM users u`)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var username, role string
		if err := rows.Scan(&username, &role); err != nil {
			rows.Close()
			return data, err
		}
		data.Refs[username] = nil
		data.NodeUsers[username] = map[int64]bool{}
		data.Admins[username] = role == "admin"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	for username := range data.Refs {
		for _, spec := range persistentAccessCredentialQueries(columns) {
			rows, err := s.db.QueryContext(ctx, spec.query, username)
			if err != nil {
				return data, err
			}
			for rows.Next() {
				ref := lifecycleCredentialRef{Username: username}
				if err := rows.Scan(&ref.ServerID, &ref.ServerName, &ref.InboundTag, &ref.Protocol, &ref.CredentialRaw, &ref.Identity, &ref.Source); err != nil {
					rows.Close()
					return data, err
				}
				ref.InboundTag = strings.TrimSpace(ref.InboundTag)
				if ref.ServerID > 0 && ref.InboundTag != "" {
					data.Refs[username] = append(data.Refs[username], ref)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return data, err
			}
			rows.Close()
		}
	}
	if !hasLifecycleColumns(columns, "nodes", "id", "original_server", "inbound_tag") {
		return data, nil
	}
	rows, err = s.db.QueryContext(ctx, `SELECT n.id,COALESCE(to_jsonb(n)->>'node_name',''),COALESCE(s.id,0),COALESCE(n.original_server,''),COALESCE(to_jsonb(s)->>'xray_mode',''),COALESCE(n.inbound_tag,''),COALESCE(to_jsonb(n)->>'username',''),COALESCE(to_jsonb(n)->>'node_type','')='routed',COALESCE(to_jsonb(n)->>'protocol',''),COALESCE(to_jsonb(n)->>'raw_url',''),COALESCE(to_jsonb(n)->>'parsed_config',''),COALESCE(to_jsonb(n)->>'clash_config','') FROM nodes n LEFT JOIN remote_servers s ON s.name=n.original_server`)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var node trafficGroupNode
		var protocol, raw, parsed, clash string
		if err := rows.Scan(&node.ID, &node.Name, &node.ServerID, &node.ServerName, &node.Mode, &node.Tag, &node.Owner, &node.Routed, &protocol, &raw, &parsed, &clash); err != nil {
			rows.Close()
			return data, err
		}
		data.Nodes[node.ID] = node
		if data.NodeUsers[node.Owner] != nil {
			data.NodeUsers[node.Owner][node.ID] = true
			if node.ServerID > 0 && node.Tag != "" && (raw != "" || parsed != "" || clash != "") {
				values, _ := json.Marshal([]string{raw, parsed, clash})
				data.Refs[node.Owner] = append(data.Refs[node.Owner], lifecycleCredentialRef{Username: node.Owner, ServerID: node.ServerID, ServerName: node.ServerName, InboundTag: node.Tag, Protocol: protocol, CredentialRaw: string(values), Source: "nodes"})
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	if hasLifecycleColumns(columns, "packages", "id", "nodes") {
		bindings := `SELECT username,package_id FROM users WHERE package_id IS NOT NULL`
		if !hasLifecycleColumns(columns, "users", "package_id") {
			bindings = `SELECT username,0::bigint AS package_id FROM users WHERE false`
		}
		if hasLifecycleColumns(columns, "user_package_assignments", "username", "package_id") {
			bindings += ` UNION SELECT username,package_id FROM user_package_assignments a WHERE COALESCE(to_jsonb(a)->>'status','active')='active'`
		}
		rows, err = s.db.QueryContext(ctx, `SELECT b.username,COALESCE(p.nodes,'[]') FROM (`+bindings+`) b JOIN packages p ON p.id=b.package_id`)
		if err != nil {
			return data, err
		}
		for rows.Next() {
			var username, raw string
			if err := rows.Scan(&username, &raw); err != nil {
				rows.Close()
				return data, err
			}
			var ids []int64
			if err := json.Unmarshal([]byte(raw), &ids); err != nil {
				rows.Close()
				return data, fmt.Errorf("套餐节点列表无效: %w", err)
			}
			for id := range data.Nodes {
				if data.NodeUsers[username] != nil && (len(ids) == 0 || trafficGroupContainsNode(ids, id)) {
					data.NodeUsers[username][id] = true
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return data, err
		}
		rows.Close()
	}
	var entitlementQueries []string
	for _, table := range []string{"user_subaccounts", "package_assignment_subaccounts"} {
		if hasLifecycleColumns(columns, table, "username", "routed_node_id") {
			entitlementQueries = append(entitlementQueries, `SELECT username,routed_node_id FROM `+table)
		}
	}
	if hasLifecycleColumns(columns, "forward_chain_nodes", "owner_username", "node_id") {
		entitlementQueries = append(entitlementQueries, `SELECT owner_username,node_id FROM forward_chain_nodes`)
		if hasLifecycleColumns(columns, "forward_chain_nodes", "billing_assignment_id") && hasLifecycleColumns(columns, "user_package_assignments", "id", "username") {
			entitlementQueries = append(entitlementQueries, `SELECT a.username,f.node_id FROM forward_chain_nodes f JOIN user_package_assignments a ON a.id=f.billing_assignment_id`)
		}
	}
	for _, query := range entitlementQueries {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return data, err
		}
		for rows.Next() {
			var username string
			var id int64
			if err := rows.Scan(&username, &id); err != nil {
				rows.Close()
				return data, err
			}
			if data.NodeUsers[username] != nil {
				data.NodeUsers[username][id] = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return data, err
		}
		rows.Close()
	}
	for username, ids := range data.NodeUsers {
		for id := range ids {
			node := data.Nodes[id]
			if node.ServerID == 0 || node.Tag == "" {
				continue
			}
			found := false
			for _, ref := range data.Refs[username] {
				found = found || ref.ServerID == node.ServerID && ref.InboundTag == node.Tag
			}
			if !found {
				data.Refs[username] = append(data.Refs[username], lifecycleCredentialRef{Username: username, ServerID: node.ServerID, ServerName: node.ServerName, InboundTag: node.Tag, Source: "node_entitlement"})
			}
		}
		data.Refs[username] = dedupeLifecycleRefs(data.Refs[username])
	}
	return data, nil
}

func persistentAccessCredentialQueries(columns map[string]map[string]bool) []lifecycleRelationSpec {
	queries := lifecycleCredentialQueries(columns)
	for index, spec := range queries {
		if spec.table == "user_inbound_configs" || spec.table == "package_assignment_inbound_configs" {
			queries[index].query = fmt.Sprintf(`SELECT c.server_id, COALESCE(s.name,''), c.inbound_tag, COALESCE(to_jsonb(c)->>'protocol',''), COALESCE(to_jsonb(c)->>'credential_json',''), COALESCE(to_jsonb(c)->>'email',''), '%s' FROM %s c LEFT JOIN remote_servers s ON s.id=c.server_id WHERE c.username=$1`, spec.table, spec.table)
		}
	}
	return queries
}

func (a *app) accessCredentialRefs(ctx context.Context, username string) ([]lifecycleCredentialRef, error) {
	if store, ok := a.adminStore.(*postgresAdminSessionStore); ok {
		data, err := store.persistentAccessData(ctx)
		return data.Refs[username], err
	}
	return a.adminStore.(lifecycleStore).LifecycleCredentialRefs(ctx, username)
}

func (data persistentAccessData) businessRefs(serverID int64, tag, username string) []lifecycleCredentialRef {
	var result []lifecycleCredentialRef
	for other, refs := range data.Refs {
		if other != username {
			for _, ref := range refs {
				if ref.ServerID == serverID && ref.InboundTag == tag {
					result = append(result, ref)
				}
			}
		}
	}
	return result
}

func (a *app) accessBusinessRefs(ctx context.Context, serverID int64, serverName, tag, username string) ([]lifecycleCredentialRef, error) {
	if store, ok := a.adminStore.(*postgresAdminSessionStore); ok {
		data, err := store.persistentAccessData(ctx)
		return data.businessRefs(serverID, tag, username), err
	}
	return a.adminStore.(lifecycleStore).LifecycleInboundBusinessRefs(ctx, serverID, serverName, tag, username)
}

func (a *app) accessDefaultCredentials(ctx context.Context, serverID int64, tag string) ([]map[string]any, error) {
	store, ok := a.adminStore.(*postgresAdminSessionStore)
	if !ok {
		return a.adminStore.(lifecycleStore).LifecycleDefaultAdminCredentials(ctx, serverID, tag)
	}
	data, err := store.persistentAccessData(ctx)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	admins := map[string]bool{}
	for username, admin := range data.Admins {
		if !admin {
			continue
		}
		admins[strings.ToLower(username)] = true
		for _, ref := range data.Refs[username] {
			if ref.ServerID == serverID && ref.InboundTag == tag {
				var credential map[string]any
				if json.Unmarshal([]byte(ref.CredentialRaw), &credential) == nil && len(credential) > 0 {
					result = append(result, credential)
				}
			}
		}
	}
	columns, err := store.schemaColumns(ctx)
	if err != nil {
		return nil, err
	}
	if hasLifecycleColumns(columns, "server_xray_config_snapshots", "id", "server_id", "config_json", "source", "created_at") {
		inbound, source, err := lifecycleInboundCreationSnapshot(ctx, store.db, serverID, tag)
		if err != nil {
			return nil, err
		}
		if inbound != nil && source == "master_write" {
			entries, _, _ := accessInboundCredentialEntries(inbound)
			for _, entry := range entries {
				if credentialMatchesAdmin(entry, admins) {
					result = append(result, entry)
				}
			}
		}
	}
	return result, nil
}
