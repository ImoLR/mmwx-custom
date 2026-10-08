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
	Nodes    []ownerNode     `json:"nodes"`
	Refs     []ownerRef      `json:"refs"`
	Admins   map[string]bool `json:"admins"`
	Packages []ownerPackage  `json:"packages"`
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
			users := refs[lifecycleInboundKey(node.ServerID, node.InboundTag)]
			if len(users) == 0 && strings.HasSuffix(node.InboundTag, "-relay") {
				users = refs[lifecycleInboundKey(node.ServerID, strings.TrimSuffix(node.InboundTag, "-relay"))]
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
				// A credential-less inbound's aliases share the same fallback.
				if owner.InboundBacked {
					for _, alias := range data.Nodes {
						if alias.ServerName == node.ServerName && strings.TrimSuffix(alias.InboundTag, "-relay") == strings.TrimSuffix(node.InboundTag, "-relay") {
							for user := range packages[alias.ID] {
								bound[user] = true
							}
						}
					}
				}
				if len(bound) == 1 {
					for user := range bound {
						owner.Users = append(owner.Users, user)
					}
					owner.Source = "package"
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
