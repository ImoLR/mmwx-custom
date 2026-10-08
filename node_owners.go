package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type nodeOwner struct {
	Users         []string `json:"users"`
	AdminOnly     bool     `json:"admin_only"`
	Shared        bool     `json:"shared"`
	Source        string   `json:"source"`
	InboundBacked bool     `json:"inbound_backed"`
	ParentNodeID  int64    `json:"parent_node_id,omitempty"`
}

type ownerNode struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	InboundTag string `json:"inbound_tag"`
	ParentID   int64  `json:"parent_id"`
	ChainID    int64  `json:"chain_id"`
	RelayHost  string `json:"relay_host"`
	Config     string `json:"config"`
}

type ownerRef struct {
	Username string `json:"username"`
	ServerID int64  `json:"server_id"`
	Tag      string `json:"tag"`
}

type ownerPackage struct {
	Username string `json:"username"`
	Nodes    string `json:"nodes"`
}

type nodeOwnerData struct {
	Nodes       []ownerNode       `json:"nodes"`
	Refs        []ownerRef        `json:"refs"`
	Admins      map[string]bool   `json:"admins"`
	AdminEmails map[string]string `json:"admin_emails"`
	Snapshots   map[int64]string  `json:"snapshots"`
	Packages    []ownerPackage    `json:"packages"`
}

type nodeOwnerStore interface {
	NodeOwners(context.Context) (map[int64]nodeOwner, error)
}

func (a *app) nodeOwnersHandler(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	store, ok := a.adminStore.(nodeOwnerStore)
	if !ok {
		writeOperatorAuthorizationError(w, errOperatorAuthorizationUnavailable)
		return
	}
	owners, err := store.NodeOwners(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "读取节点归属失败"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"owners": owners})
}

func (s *postgresAdminSessionStore) NodeOwners(ctx context.Context) (map[int64]nodeOwner, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	columns, err := s.schemaColumns(ctx)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, nodeOwnerDataSQL(columns)).Scan(&raw); err != nil {
		return nil, err
	}
	var data nodeOwnerData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return resolveNodeOwners(data)
}

// These are the credential/access relations used by LifecycleInboundBusinessRefs.
// Only relation existence matters here, including credential-less whole-port access.
// One statement gives a consistent DB snapshot; no Agent/config requests or writes.
func nodeOwnerDataSQL(columns map[string]map[string]bool) string {
	refs := []string{`SELECT ''::text username,0::bigint server_id,''::text tag WHERE false`}
	for _, table := range []string{"user_inbound_configs", "package_assignment_inbound_configs", "user_outbounds"} {
		if hasLifecycleColumns(columns, table, "username", "server_id", "inbound_tag") {
			refs = append(refs, fmt.Sprintf(`SELECT username,server_id,inbound_tag FROM %s`, table))
		}
	}
	for _, table := range []string{"user_subaccounts", "package_assignment_subaccounts"} {
		if hasLifecycleColumns(columns, table, "username", "routed_node_id") {
			refs = append(refs, fmt.Sprintf(`SELECT c.username,s.id,n.inbound_tag FROM %s c JOIN nodes n ON n.id=c.routed_node_id JOIN remote_servers s ON s.name=n.original_server`, table))
		}
	}
	if hasLifecycleColumns(columns, "mmwxc_connection_assignments", "management_username", "server_id", "inbound_tag") {
		refs = append(refs, `SELECT management_username,server_id,inbound_tag FROM mmwxc_connection_assignments`)
	}
	bindings := `SELECT username,package_id FROM users WHERE package_id IS NOT NULL`
	if hasLifecycleColumns(columns, "user_package_assignments", "username", "package_id") {
		bindings += ` UNION SELECT username,package_id FROM user_package_assignments a WHERE COALESCE(to_jsonb(a)->>'status','active')='active'`
	}
	snapshots := `'{}'::jsonb`
	if hasLifecycleColumns(columns, "server_xray_config_snapshots", "id", "server_id", "config_json", "created_at") {
		// Only the latest snapshot is evidence of current access. Unlike deletion,
		// ownership does not need the historical master_write creation snapshot.
		// Read each needed server once, even when it has multiple unassigned inbounds.
		snapshots = `COALESCE((SELECT jsonb_object_agg(needed.id,snapshot.config_json::text)
			FROM (SELECT DISTINCT s.id FROM nodes n JOIN remote_servers s ON s.name=n.original_server
				WHERE COALESCE(n.inbound_tag,'')<>'' AND NOT EXISTS (
					SELECT 1 FROM refs r JOIN users u ON u.username=r.username
					WHERE u.role<>'admin' AND r.server_id=s.id AND
					(r.tag=n.inbound_tag OR r.tag=regexp_replace(n.inbound_tag,'-relay$','')))) needed
			JOIN LATERAL (SELECT config_json FROM server_xray_config_snapshots
				WHERE server_id=needed.id ORDER BY created_at DESC,id DESC LIMIT 1) snapshot ON true),'{}'::jsonb)`
	}
	return `WITH refs AS (` + strings.Join(refs, " UNION ") + `), bindings AS (` + bindings + `)
	SELECT jsonb_build_object(
		'nodes', COALESCE((SELECT jsonb_agg(jsonb_build_object(
			'id',n.id,'name',COALESCE(to_jsonb(n)->>'node_name',''),
			'server_id',COALESCE(s.id,0),'server_name',COALESCE(n.original_server,''),'inbound_tag',COALESCE(n.inbound_tag,''),
			'parent_id',COALESCE((to_jsonb(n)->>'parent_node_id')::bigint,0),'chain_id',COALESCE((to_jsonb(n)->>'chain_proxy_node_id')::bigint,0),
			'relay_host',COALESCE(to_jsonb(n)->>'relay_orig_server',''),
			'config',COALESCE(NULLIF(to_jsonb(n)->>'clash_config',''),to_jsonb(n)->>'parsed_config','')) ORDER BY n.id)
			FROM nodes n LEFT JOIN remote_servers s ON s.name=n.original_server),'[]'::jsonb),
		'admins', COALESCE((SELECT jsonb_object_agg(username,role='admin') FROM users),'{}'::jsonb),
		'admin_emails', COALESCE((SELECT jsonb_object_agg(username,COALESCE(to_jsonb(u)->>'email','')) FROM users u WHERE role='admin'),'{}'::jsonb),
		'snapshots', ` + snapshots + `,
		'refs', COALESCE((SELECT jsonb_agg(to_jsonb(r)) FROM refs r JOIN users u ON u.username=r.username),'[]'::jsonb),
		'packages', COALESCE((SELECT jsonb_agg(jsonb_build_object('username',b.username,'nodes',COALESCE(p.nodes,'[]')))
			FROM bindings b JOIN users u ON u.username=b.username JOIN packages p ON p.id=b.package_id WHERE u.role<>'admin'),'[]'::jsonb))`
}

func ownerConfigKey(raw string) string {
	var config map[string]any
	if json.Unmarshal([]byte(raw), &config) != nil || config["server"] == nil || config["port"] == nil {
		return ""
	}
	delete(config, "name")
	encoded, _ := json.Marshal(config)
	return string(encoded)
}

func resolveNodeOwners(data nodeOwnerData) (map[int64]nodeOwner, error) {
	nodes := make(map[int64]ownerNode)
	refs := make(map[string]map[string]bool)
	packages := make(map[int64]map[string]bool)
	for _, node := range data.Nodes {
		nodes[node.ID] = node
	}
	for _, ref := range data.Refs {
		if _, exists := data.Admins[ref.Username]; !exists || ref.Tag == "" || ref.ServerID == 0 {
			continue
		}
		key := lifecycleInboundKey(ref.ServerID, ref.Tag)
		if refs[key] == nil {
			refs[key] = make(map[string]bool)
		}
		refs[key][ref.Username] = true
	}
	for _, pkg := range data.Packages {
		if admin, exists := data.Admins[pkg.Username]; !exists || admin {
			continue
		}
		var ids []int64
		if err := json.Unmarshal([]byte(pkg.Nodes), &ids); err != nil {
			return nil, fmt.Errorf("invalid package node membership: %w", err)
		}
		if len(ids) == 0 { // Official empty membership means all nodes.
			for id := range nodes {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			if packages[id] == nil {
				packages[id] = make(map[string]bool)
			}
			packages[id][pkg.Username] = true
		}
	}
	parents := make(map[int64]int64)
	for _, node := range data.Nodes {
		if _, exists := nodes[node.ParentID]; exists && node.ParentID != node.ID {
			parents[node.ID] = node.ParentID
			continue
		}
		// Relay copies may carry a -relay suffix; chain_proxy_node_id is the
		// EXIT, not the source owner. Match its unchanged source config instead.
		var candidates []int64
		key := ownerConfigKey(node.Config)
		for _, source := range data.Nodes {
			if source.ID == node.ID || source.ParentID != 0 || source.ChainID != 0 || source.RelayHost != "" || strings.HasSuffix(source.InboundTag, "-relay") {
				continue
			}
			sameInbound := node.ServerName != "" && node.ServerName == source.ServerName && source.InboundTag != "" &&
				(node.InboundTag == source.InboundTag || strings.TrimSuffix(node.InboundTag, "-relay") == source.InboundTag)
			if ((node.RelayHost != "" || strings.HasSuffix(node.InboundTag, "-relay")) && sameInbound) ||
				(node.ChainID != 0 && key != "" && key == ownerConfigKey(source.Config)) {
				candidates = append(candidates, source.ID)
			}
		}
		if len(candidates) == 1 {
			parents[node.ID] = candidates[0]
		}
	}
	result := make(map[int64]nodeOwner)
	snapshots := make(map[int64]map[string]any)
	adminIdentities := make(map[string]map[string]bool)
	for username, admin := range data.Admins {
		if admin {
			identities := map[string]bool{strings.ToLower(strings.TrimSpace(username)): true}
			if email := strings.ToLower(strings.TrimSpace(data.AdminEmails[username])); email != "" {
				identities[email] = true
			}
			adminIdentities[username] = identities
		}
	}
	snapshotRefs := make(map[string]map[string]bool)
	snapshotInbounds := make(map[string]bool)
	var resolve func(int64, map[int64]bool) nodeOwner
	resolve = func(id int64, visiting map[int64]bool) nodeOwner {
		if owner, ok := result[id]; ok {
			return owner
		}
		node := nodes[id]
		owner := nodeOwner{Users: []string{}, Source: "none", InboundBacked: node.ServerName != "" && node.InboundTag != ""}
		if visiting[id] {
			return owner
		}
		visiting[id] = true
		defer delete(visiting, id)
		if parent := parents[id]; parent != 0 {
			owner = resolve(parent, visiting)
			owner.ParentNodeID = parent
		} else {
			tag := node.InboundTag
			users := refs[lifecycleInboundKey(node.ServerID, tag)]
			if len(users) == 0 && strings.HasSuffix(node.InboundTag, "-relay") {
				tag = strings.TrimSuffix(tag, "-relay")
				users = refs[lifecycleInboundKey(node.ServerID, tag)]
			}
			key := lifecycleInboundKey(node.ServerID, tag)
			if owner.InboundBacked && len(users) == 0 {
				if _, checked := snapshotRefs[key]; !checked {
					if _, loaded := snapshots[node.ServerID]; !loaded {
						var config map[string]any
						_ = json.Unmarshal([]byte(data.Snapshots[node.ServerID]), &config)
						snapshots[node.ServerID] = config
					}
					snapshotRefs[key] = make(map[string]bool)
					if inbound := findConfigInbound(snapshots[node.ServerID], tag); inbound != nil {
						snapshotInbounds[key] = true
						entries, _, err := accessInboundCredentialEntries(inbound)
						if err == nil {
							for _, entry := range entries {
								for username, identities := range adminIdentities {
									if credentialMatchesAdmin(entry, identities) {
										snapshotRefs[key][username] = true
									}
								}
							}
						}
					}
				}
				users = snapshotRefs[key]
			}
			if owner.InboundBacked && len(users) > 0 {
				for user := range users {
					if !data.Admins[user] {
						owner.Users = append(owner.Users, user)
					}
				}
				if len(owner.Users) == 0 {
					owner.AdminOnly = true
					for user := range users {
						owner.Users = append(owner.Users, user)
					}
				}
				owner.Source = "credential"
				owner.Shared = !owner.AdminOnly && len(owner.Users) > 1
			} else {
				bound := make(map[string]bool)
				for user := range packages[id] {
					bound[user] = true
				}
				// Inbound aliases share the same package/default-admin fallback.
				if owner.InboundBacked {
					for _, alias := range data.Nodes {
						ancestor := alias.ID
						seen := make(map[int64]bool)
						for parents[ancestor] != 0 && ancestor != id && !seen[ancestor] {
							seen[ancestor] = true
							ancestor = parents[ancestor]
						}
						if ancestor == id || (alias.ServerName == node.ServerName && strings.TrimSuffix(alias.InboundTag, "-relay") == strings.TrimSuffix(node.InboundTag, "-relay")) {
							for user := range packages[alias.ID] {
								bound[user] = true
							}
						}
					}
				}
				if len(bound) == 1 || (owner.InboundBacked && len(bound) > 1) {
					for user := range bound {
						owner.Users = append(owner.Users, user)
					}
					owner.Source = "package"
					owner.Shared = len(owner.Users) > 1
				} else if owner.InboundBacked && snapshotInbounds[key] {
					for username := range adminIdentities {
						owner.Users = append(owner.Users, username)
					}
					owner.AdminOnly = true
					owner.Source = "default-admin"
				}
			}
		}
		sort.Strings(owner.Users)
		result[id] = owner
		return owner
	}
	for _, node := range data.Nodes {
		resolve(node.ID, make(map[int64]bool))
	}
	return result, nil
}
