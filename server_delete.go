package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const serverDeletionSchema = `
CREATE TABLE IF NOT EXISTS mmwxc_server_deletions (
 operation_id TEXT PRIMARY KEY,
 server_id BIGINT NOT NULL,
 server_name TEXT NOT NULL,
 state TEXT NOT NULL,
 plan JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS mmwxc_server_deletion_pending ON mmwxc_server_deletions(server_id) WHERE state <> 'deleted';
`

type serverDeletionNode struct {
	SourceServerID int64  `json:"server_id,omitempty"`
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	ServerName     string `json:"server_name"`
	InboundTag     string `json:"inbound_tag,omitempty"`
	Reason         string `json:"reason"`
	Direct         bool   `json:"direct"`
	Hash           string `json:"hash"`
}
type serverDeletionPackage struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Users          []string `json:"users"`
	NodeIDs        []int64  `json:"node_ids"`
	DeletedNodeIDs []int64  `json:"deleted_node_ids"`
	Delete         bool     `json:"delete"`
}
type serverDeletionGroup struct {
	ID        int64   `json:"id"`
	PackageID int64   `json:"package_id"`
	Name      string  `json:"name"`
	NodeIDs   []int64 `json:"node_ids"`
	Limit     int64   `json:"limit_bytes"`
	Delete    bool    `json:"delete"`
}
type serverDeletionMachine struct {
	Helper        string     `json:"helper"`
	OfficialAgent string     `json:"official_agent"`
	CommandID     string     `json:"command_id,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	Outstanding   []string   `json:"outstanding"`
	ManualCommand string     `json:"manual_command"`
}
type serverDeletionStep struct {
	Kind      string             `json:"kind"`
	Name      string             `json:"name"`
	ID        int64              `json:"id,omitempty"`
	ServerID  int64              `json:"server_id,omitempty"`
	Tag       string             `json:"tag,omitempty"`
	Hash      string             `json:"hash,omitempty"`
	Payload   json.RawMessage    `json:"payload,omitempty"`
	Item      *lifecyclePlanItem `json:"item,omitempty"`
	Status    string             `json:"status"`
	Attempts  int                `json:"attempts"`
	LastError string             `json:"last_error,omitempty"`
}
type serverDeletionPlan struct {
	Inbounds          []serverDeletionInbound   `json:"retired_inbounds,omitempty"`
	Identities        []serverDeletionIdentity  `json:"retired_identities,omitempty"`
	SubscriptionNodes []deletedSubscriptionNode `json:"subscription_nodes,omitempty"`
	OperationID       string                    `json:"operation_id,omitempty"`
	ServerID          int64                     `json:"server_id"`
	ServerName        string                    `json:"server_name"`
	ServerHash        string                    `json:"server_hash"`
	Fingerprint       string                    `json:"fingerprint"`
	State             string                    `json:"state"`
	UninstallAgent    bool                      `json:"uninstall_agent"`
	Nodes             []serverDeletionNode      `json:"nodes"`
	Packages          []serverDeletionPackage   `json:"packages"`
	Groups            []serverDeletionGroup     `json:"groups"`
	Connections       map[string]int64          `json:"connections"`
	Attention         []string                  `json:"attention"`
	Machine           serverDeletionMachine     `json:"machine"`
	Steps             []serverDeletionStep      `json:"steps"`
	UpdatedAt         time.Time                 `json:"updated_at"`
}

type serverDeletionInventory map[string][]map[string]any

func deletionID(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case json.Number:
		i, _ := x.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(x, 10, 64)
		return i
	}
	return 0
}
func deletionText(v any) string { s, _ := v.(string); return s }
func deletionIDs(v any) []int64 {
	if s, ok := v.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(s), &decoded) != nil {
			return nil
		}
		v = decoded
	}
	result := []int64{}
	if values, ok := v.([]any); ok {
		for _, x := range values {
			if id := deletionID(x); id > 0 {
				result = append(result, id)
			}
		}
	}
	return result
}
func deletionObject(v any) map[string]any {
	if s, ok := v.(string); ok {
		var result map[string]any
		_ = json.Unmarshal([]byte(s), &result)
		return result
	}
	result, _ := v.(map[string]any)
	return result
}
func deletionHost(v any) string {
	s := strings.TrimSpace(deletionText(v))
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	return strings.ToLower(strings.Trim(s, "[]"))
}
func deletionNodeHost(n map[string]any) string {
	c := deletionObject(n["clash_config"])
	if c == nil {
		c = deletionObject(n["parsed_config"])
	}
	return deletionHost(c["server"])
}
func deletionServerHash(s map[string]any) string {
	return hashJSON([]any{s["id"], s["name"], s["created_at"], s["token"]})
}
func deletionNodeHash(n map[string]any) string {
	copy := make(map[string]any)
	for k, v := range n {
		if k != "updated_at" {
			copy[k] = v
		}
	}
	return hashJSON(copy)
}

func (s *postgresAdminSessionStore) serverDeletionInventory(ctx context.Context) (serverDeletionInventory, error) {
	columns, err := s.schemaColumns(ctx)
	if err != nil {
		return nil, err
	}
	result := serverDeletionInventory{}
	for _, table := range []string{"remote_servers", "nodes", "packages", "users", "user_package_assignments", "forward_groups", "forward_group_members", "forward_chains", "forward_chain_hops", "forward_chain_branches", "forward_chain_nodes", "user_outbounds", "mmwxc_package_traffic_groups", "mmwxc_connection_assignments", "user_inbound_configs", "package_assignment_inbound_configs", "user_subaccounts", "package_assignment_subaccounts", "subscribe_files", "server_xray_config_snapshots", "certificates"} {
		if columns[table] == nil {
			continue
		}
		query := `SELECT to_jsonb(t) FROM ` + table + ` t ORDER BY to_jsonb(t)::text`
		if table == "server_xray_config_snapshots" {
			query = `SELECT to_jsonb(t) FROM (SELECT DISTINCT ON(server_id) * FROM server_xray_config_snapshots ORDER BY server_id,id DESC)t`
		}
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				break
			}
			var row map[string]any
			if err = json.Unmarshal(raw, &row); err != nil {
				break
			}
			result[table] = append(result[table], row)
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if rowErr != nil {
			return nil, rowErr
		}
	}
	return result, nil
}

func planServerDeletion(data serverDeletionInventory, serverID int64) (serverDeletionPlan, error) {
	p := serverDeletionPlan{ServerID: serverID, State: "preview", UninstallAgent: true, Nodes: []serverDeletionNode{}, Packages: []serverDeletionPackage{}, Groups: []serverDeletionGroup{}, Connections: map[string]int64{}, Attention: []string{}, Steps: []serverDeletionStep{}}
	var server map[string]any
	servers := map[int64]map[string]any{}
	for _, s := range data["remote_servers"] {
		id := deletionID(s["id"])
		servers[id] = s
		if id == serverID {
			server = s
		}
	}
	if server == nil {
		return p, errOperatorServerNotFound
	}
	p.ServerName = deletionText(server["name"])
	p.ServerHash = deletionServerHash(server)
	for id, s := range servers {
		if id != serverID && deletionText(s["name"]) == p.ServerName {
			return p, errors.New("服务器名称重复，无法安全判定节点归属")
		}
	}
	hosts := deletionServerHosts(server)
	sharedHosts := map[string][]string{}
	for id, other := range servers {
		if id == serverID {
			continue
		}
		for host := range deletionServerHosts(other) {
			if hosts[host] {
				sharedHosts[host] = appendUniqueString(sharedHosts[host], deletionText(other["name"]))
			}
		}
	}
	for host, names := range sharedHosts {
		delete(hosts, host)
		sort.Strings(names)
		p.Attention = append(p.Attention, "地址 "+host+" 同时属于服务器「"+strings.Join(names, "」、「")+"」，无法按地址判定依赖；指向该地址的中转/出站/隧道未自动删除，请人工核对")
	}
	nodes := map[int64]map[string]any{}
	reasons := map[int64]string{}
	direct := map[int64]bool{}
	for _, n := range data["nodes"] {
		id := deletionID(n["id"])
		nodes[id] = n
		if deletionText(n["original_server"]) == p.ServerName {
			reasons[id] = "服务器直属节点"
			direct[id] = true
		} else if hosts[deletionNodeHost(n)] {
			reasons[id] = "中转或导入节点的连接地址是此服务器"
		}
	}
	groupMembers := map[int64][]map[string]any{}
	affectedGroups := map[int64]bool{}
	emptyGroups := map[int64]bool{}
	for _, m := range data["forward_group_members"] {
		g := deletionID(m["group_id"])
		if deletionID(m["server_id"]) == serverID {
			affectedGroups[g] = true
		} else if servers[deletionID(m["server_id"])] != nil {
			groupMembers[g] = append(groupMembers[g], m)
		}
	}
	for g := range affectedGroups {
		emptyGroups[g] = len(groupMembers[g]) == 0
	}
	chainGroups := map[int64][]int64{}
	brokenChains := map[int64]bool{}
	degradedChains := map[int64]bool{}
	for _, h := range data["forward_chain_hops"] {
		g := deletionID(h["group_id"])
		c := deletionID(h["chain_id"])
		chainGroups[c] = append(chainGroups[c], g)
		if emptyGroups[g] {
			brokenChains[c] = true
		} else if affectedGroups[g] {
			degradedChains[c] = true
		}
	}
	for c := range degradedChains {
		if !deletionChainHasPath(chainGroups[c], groupMembers) {
			brokenChains[c] = true
		}
	}
	for _, f := range data["forward_chain_nodes"] {
		if brokenChains[deletionID(f["chain_id"])] || deletionID(f["pinned_exit_server_id"]) == serverID {
			reasons[deletionID(f["node_id"])] = "转发链失去必要的一跳或固定出口"
		}
	}
	// A whole-inbound outbound makes every node on that inbound depend on A.
	for _, out := range data["user_outbounds"] {
		if deletionID(out["server_id"]) == serverID {
			continue
		}
		if !deletionJSONUsesHost(deletionObject(out["outbound_json"]), hosts) {
			continue
		}
		source := servers[deletionID(out["server_id"])]
		for id, n := range nodes {
			if n["original_server"] == source["name"] && n["inbound_tag"] == out["inbound_tag"] {
				reasons[id] = "此入站的出站连接依赖将删除的服务器"
			}
		}
	}
	planDeletionTunnels(data, &p, servers, nodes, reasons, hosts)
	for changed := true; changed; {
		changed = false
		for id, n := range nodes {
			if reasons[id] != "" {
				continue
			}
			for _, key := range []string{"parent_node_id", "chain_proxy_node_id", "routed_target_node_id"} {
				if reasons[deletionID(n[key])] != "" {
					reasons[id] = "依赖已删除节点（" + key + "）"
					changed = true
					break
				}
			}
			refs := deletionIDs(n["relay_group_node_ids"])
			removed, remaining := 0, 0
			for _, ref := range refs {
				if reasons[ref] != "" {
					removed++
				} else if nodes[ref] != nil {
					remaining++
				}
			}
			if removed > 0 && remaining == 0 && reasons[id] == "" {
				reasons[id] = "中转组已没有可用成员"
				changed = true
			}
		}
	}
	ids := []int64{}
	for id, reason := range reasons {
		n := nodes[id]
		if n == nil {
			continue
		}
		ids = append(ids, id)
		fingerprint := deletedSubscriptionNode{Name: deletionText(n["node_name"])}
		if direct[id] || hosts[deletionNodeHost(n)] {
			fingerprint.Host = deletionNodeHost(n)
			fingerprint.Port = deletionID(deletionObject(n["clash_config"])["port"])
			if fingerprint.Port == 0 {
				fingerprint.Port = deletionID(deletionObject(n["parsed_config"])["port"])
			}
		}
		p.SubscriptionNodes = append(p.SubscriptionNodes, fingerprint)
		sourceID := int64(0)
		for sid, source := range servers {
			if deletionText(source["name"]) == deletionText(n["original_server"]) {
				if sourceID != 0 {
					return p, errors.New("依赖节点的服务器名称重复，无法安全清理")
				}
				sourceID = sid
			}
		}
		p.Nodes = append(p.Nodes, serverDeletionNode{SourceServerID: sourceID, ID: id, Name: deletionText(n["node_name"]), ServerName: deletionText(n["original_server"]), InboundTag: deletionText(n["inbound_tag"]), Reason: reason, Direct: direct[id], Hash: deletionNodeHash(n)})
	}
	sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].ID < p.Nodes[j].ID })
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, n := range data["nodes"] {
		if reasons[deletionID(n["id"])] != "" {
			continue
		}
		old := deletionIDs(n["relay_group_node_ids"])
		kept := []int64{}
		for _, id := range old {
			if reasons[id] == "" {
				kept = append(kept, id)
			}
		}
		if len(old) != len(kept) {
			p.Attention = append(p.Attention, deletionText(n["node_name"])+"：中转组仍有其他成员，保留节点并移除失效成员")
			payload := map[string]any{}
			for k, v := range n {
				payload[k] = v
			}
			delete(payload, "created_at")
			delete(payload, "updated_at")
			payload["relay_group_node_ids"] = kept
			payload["enabled"] = deletionID(n["enabled"]) != 0
			var tags []string
			_ = json.Unmarshal([]byte(deletionText(n["tags"])), &tags)
			if tags == nil {
				tags = []string{}
			}
			payload["tags"] = tags
			raw, _ := json.Marshal(payload)
			p.Steps = append(p.Steps, serverDeletionStep{Kind: "trim-node", ID: deletionID(n["id"]), Name: deletionText(n["node_name"]), Hash: deletionNodeHash(n), Payload: raw})
		}
	}
	for _, pkg := range data["packages"] {
		old := deletionIDs(pkg["nodes"])
		removed := []int64{}
		for _, id := range old {
			if reasons[id] != "" {
				removed = append(removed, id)
			}
		}
		if len(removed) == 0 {
			continue
		}
		id := deletionID(pkg["id"])
		users := map[string]bool{}
		for _, u := range data["users"] {
			if deletionID(u["package_id"]) == id {
				users[deletionText(u["username"])] = true
			}
		}
		for _, u := range data["user_package_assignments"] {
			if deletionID(u["package_id"]) == id {
				users[deletionText(u["username"])] = true
			}
		}
		names := []string{}
		for name := range users {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, deletedID := range removed {
			if alias := deletionText(deletionObject(pkg["node_name_overrides"])[strconv.FormatInt(deletedID, 10)]); alias != "" {
				p.SubscriptionNodes = append(p.SubscriptionNodes, deletedSubscriptionNode{Name: alias})
			}
		}
		p.Packages = append(p.Packages, serverDeletionPackage{ID: id, Name: deletionText(pkg["name"]), Users: names, NodeIDs: old, DeletedNodeIDs: removed, Delete: lifecyclePackageEmptied(old, ids)})
	}
	sort.Slice(p.Packages, func(i, j int) bool { return p.Packages[i].ID < p.Packages[j].ID })
	for _, g := range data["mmwxc_package_traffic_groups"] {
		old := deletionIDs(g["node_ids"])
		kept := []int64{}
		for _, id := range old {
			if reasons[id] == "" {
				kept = append(kept, id)
			}
		}
		if len(old) != len(kept) {
			p.Groups = append(p.Groups, serverDeletionGroup{ID: deletionID(g["id"]), PackageID: deletionID(g["package_id"]), Name: deletionText(g["name"]), NodeIDs: kept, Limit: deletionID(g["limit_bytes"]), Delete: len(kept) == 0})
		}
	}
	sort.Slice(p.Groups, func(i, j int) bool { return p.Groups[i].ID < p.Groups[j].ID })
	for _, table := range []string{"mmwxc_connection_assignments", "user_inbound_configs", "package_assignment_inbound_configs", "user_subaccounts", "package_assignment_subaccounts", "user_outbounds"} {
		for _, r := range data[table] {
			if deletionID(r["server_id"]) == serverID || reasons[deletionID(r["routed_node_id"])] != "" {
				p.Connections[table]++
			}
		}
	}
	for _, c := range data["forward_chains"] {
		id := deletionID(c["id"])
		if brokenChains[id] {
			p.Steps = append(p.Steps, serverDeletionStep{Kind: "chain", ID: id, Name: deletionText(c["name"])})
		} else if degradedChains[id] {
			p.Attention = append(p.Attention, deletionText(c["name"])+"：其余服务器仍构成有效路径，保留转发链并移除失效成员")
		}
	}
	for _, g := range data["forward_groups"] {
		id := deletionID(g["id"])
		if !affectedGroups[id] {
			continue
		}
		kind := "group"
		var raw []byte
		if !emptyGroups[id] {
			kind = "trim-group"
			payload := map[string]any{}
			for k, v := range g {
				payload[k] = v
			}
			members := []map[string]any{}
			for _, m := range groupMembers[id] {
				members = append(members, map[string]any{"server_id": m["server_id"], "weight": m["weight"]})
			}
			delete(payload, "created_at")
			delete(payload, "updated_at")
			payload["members"] = members
			payload["failover_enabled"] = deletionID(g["failover_enabled"]) != 0
			raw, _ = json.Marshal(payload)
		}
		p.Steps = append(p.Steps, serverDeletionStep{Kind: kind, ID: id, Name: deletionText(g["name"]), Payload: raw})
	}
	for _, file := range data["subscribe_files"] {
		selected := deletionIDs(file["selected_node_ids"])
		kept := []int64{}
		for _, id := range selected {
			if reasons[id] == "" {
				kept = append(kept, id)
			}
		}
		stats := deletionText(file["stats_server_ids"])
		statsIDs := deletionIDs(stats)
		if !strings.HasPrefix(stats, "[") {
			for _, part := range strings.Split(stats, ",") {
				if id := deletionID(strings.TrimSpace(part)); id > 0 {
					statsIDs = append(statsIDs, id)
				}
			}
		}
		keptStats := []string{}
		statsChanged := false
		for _, id := range statsIDs {
			if id == serverID {
				statsChanged = true
			} else {
				keptStats = append(keptStats, strconv.FormatInt(id, 10))
			}
		}
		if len(selected) == len(kept) && !statsChanged {
			continue
		}
		kind := "subscription"
		if len(selected) > 0 && len(kept) == 0 {
			kind = "empty-subscription"
		}
		payload := map[string]any{"selected_node_ids": kept, "stats_server_ids": strings.Join(keptStats, ",")}
		raw, _ := json.Marshal(payload)
		p.Steps = append(p.Steps, serverDeletionStep{Kind: kind, ID: deletionID(file["id"]), Name: "更新订阅：" + deletionText(file["name"]), Hash: hashJSON([]any{file["selected_node_ids"], file["stats_server_ids"]}), Payload: raw})
		if kind == "empty-subscription" {
			p.Attention = append(p.Attention, "订阅 "+deletionText(file["name"])+" 只含待删除节点，将删除该文件和绑定，避免空选择变成全部节点")
		} else {
			p.Attention = append(p.Attention, "订阅 "+deletionText(file["name"])+" 将移除失效引用；旧缓存若仍含删除节点会拒绝输出")
		}
	}
	sort.SliceStable(p.Steps, func(i, j int) bool {
		a, b := p.Steps[i], p.Steps[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.ServerID != b.ServerID {
			return a.ServerID < b.ServerID
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Tag < b.Tag
	})
	for _, certificate := range data["certificates"] {
		if deletionID(certificate["remote_server_id"]) == serverID {
			p.Steps = append(p.Steps, serverDeletionStep{Kind: "certificate", ID: deletionID(certificate["id"]), Name: "停用证书自动任务：" + deletionText(certificate["domain"])})
			p.Attention = append(p.Attention, "证书 "+deletionText(certificate["domain"])+" 保留证书材料，停用依赖此服务器的自动续期与部署")
		}
	}
	planDeletionIdentities(data, &p, nodes, reasons, servers)
	// Save package scope before any upstream node deletion can turn [id] into [].
	packageSteps := []serverDeletionStep{}
	for _, pkg := range p.Packages {
		action := lifecycleActionKeepPackage
		if pkg.Delete {
			action = lifecycleActionDeleteEmptyPackage
		}
		item := &lifecyclePlanItem{ItemKind: lifecycleItemKindPackage, PackageID: pkg.ID, PackageName: pkg.Name, NodeIDs: pkg.NodeIDs, DeletedNodeIDs: pkg.DeletedNodeIDs, Action: action, Status: lifecycleItemPending}
		packageSteps = append(packageSteps, serverDeletionStep{Kind: "package", ID: pkg.ID, Name: pkg.Name, Item: item})
	}
	p.Steps = append(packageSteps, p.Steps...)
	// Dependants first. The official server operation is always called with delete_nodes=true.
	for _, directOnly := range []bool{false, true} {
		for _, n := range p.Nodes {
			if n.Direct == directOnly {
				p.Steps = append(p.Steps, serverDeletionStep{Kind: "node", ID: n.ID, Name: n.Name, Hash: n.Hash})
			}
		}
	}
	p.Steps = append(p.Steps, serverDeletionStep{Kind: "helper", Name: "停止自有 Core 并卸载 Helper"}, serverDeletionStep{Kind: "server", ID: serverID, Name: "删除官方服务器"}, serverDeletionStep{Kind: "sweep", Name: "清理关联记录和 Custom 状态"}, serverDeletionStep{Kind: "verify", Name: "重新读取官方并检查残留"})
	for i := range p.Steps {
		p.Steps[i].Status = lifecycleItemPending
	}
	sort.Strings(p.Attention)
	p.Fingerprint = hashJSON([]any{p.ServerHash, p.Nodes, p.Packages, p.Groups, p.Steps})
	return p, nil
}

func deletionJSONUsesHost(v any, hosts map[string]bool) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, item := range x {
			if (k == "server" || k == "address" || k == "host") && hosts[deletionHost(item)] {
				return true
			}
			if deletionJSONUsesHost(item, hosts) {
				return true
			}
		}
	case []any:
		for _, item := range x {
			if deletionJSONUsesHost(item, hosts) {
				return true
			}
		}
	}
	return false
}

func (a *app) previewServerDeletion(ctx context.Context, serverID int64) (serverDeletionPlan, error) {
	store, ok := a.adminStore.(*postgresAdminSessionStore)
	if !ok {
		return serverDeletionPlan{}, errors.New("删除服务器需要 PostgreSQL")
	}
	data, err := store.serverDeletionInventory(ctx)
	if err != nil {
		return serverDeletionPlan{}, err
	}
	warning := a.serverDeletionTunnels(ctx, data)
	p, err := planServerDeletion(data, serverID)
	if err != nil {
		return p, err
	}
	p.Machine = a.serverDeletionMachine(serverID)
	if warning != "" {
		p.Machine.Outstanding = append(p.Machine.Outstanding, warning)
	}
	return p, nil
}

func (s *postgresAdminSessionStore) saveServerDeletion(ctx context.Context, p *serverDeletionPlan) error {
	p.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mmwxc_server_deletions(operation_id,server_id,server_name,state,plan) VALUES($1,$2,$3,$4,$5::jsonb)
 ON CONFLICT(operation_id) DO UPDATE SET state=EXCLUDED.state,plan=EXCLUDED.plan,updated_at=CURRENT_TIMESTAMP`, p.OperationID, p.ServerID, p.ServerName, p.State, string(raw))
	return err
}
func (s *postgresAdminSessionStore) loadServerDeletion(ctx context.Context, serverID int64, id string) (serverDeletionPlan, error) {
	var p serverDeletionPlan
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT plan FROM mmwxc_server_deletions WHERE server_id=$1 AND (operation_id=$2 OR $2='') ORDER BY created_at DESC LIMIT 1`, serverID, id).Scan(&raw)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(raw, &p)
	return p, err
}
func (a *app) serverDeletionHandler(w http.ResponseWriter, r *http.Request, serverID int64, preview bool) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	store, ok := a.adminStore.(*postgresAdminSessionStore)
	if !ok {
		writeJSON(w, 503, map[string]any{"message": "删除服务器需要 PostgreSQL"})
		return
	}
	unlock := a.lockUserLifecycle("server-delete:" + strconv.FormatInt(serverID, 10))
	defer unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		var p serverDeletionPlan
		var err error
		if preview {
			p, err = a.previewServerDeletion(ctx, serverID)
		} else {
			p, err = store.loadServerDeletion(ctx, serverID, r.URL.Query().Get("operation_id"))
		}
		if err != nil {
			writeJSON(w, 400, map[string]any{"message": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "plan": publicServerDeletion(p)})
		return
	}
	if preview || r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var input struct {
		ConfirmName    string `json:"confirm_name"`
		Fingerprint    string `json:"fingerprint"`
		OperationID    string `json:"operation_id"`
		UninstallAgent *bool  `json:"uninstall_agent"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		writeJSON(w, 400, map[string]any{"message": "删除请求无效"})
		return
	}
	var p serverDeletionPlan
	var err error
	if input.OperationID != "" {
		p, err = store.loadServerDeletion(ctx, serverID, input.OperationID)
	} else {
		if old, e := store.loadServerDeletion(ctx, serverID, ""); e == nil && old.State != "deleted" {
			writeJSON(w, 409, map[string]any{"message": "此服务器有未完成的删除任务，请继续原操作", "plan": publicServerDeletion(old)})
			return
		} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
			err = e
		}
		if err == nil {
			p, err = a.previewServerDeletion(ctx, serverID)
		}
		if err == nil && (input.ConfirmName != p.ServerName || input.Fingerprint != p.Fingerprint) {
			err = errors.New("名称或预览范围已变化，请重新预览并确认")
		}
		if err == nil {
			p.OperationID, err = newManagedUserStatusTaskID()
			p.State = "deleting"
			if input.UninstallAgent != nil {
				p.UninstallAgent = *input.UninstallAgent
			}
		}
		if err == nil {
			err = store.saveServerDeletion(ctx, &p)
		}
	}
	if err != nil {
		writeJSON(w, 409, map[string]any{"message": err.Error()})
		return
	}
	if input.ConfirmName != p.ServerName {
		writeJSON(w, 400, map[string]any{"message": "请输入完整服务器名称确认"})
		return
	}
	token := strings.TrimSpace(r.Header.Get("MM-Authorization"))
	if token == "" {
		token, err = store.CoreModeAdminSession(ctx)
	}
	if err == nil {
		err = a.executeServerDeletionStep(ctx, token, &p)
	}
	if err != nil {
		writeJSON(w, 500, map[string]any{"message": lifecycleSafeError(err), "plan": publicServerDeletion(p)})
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "plan": publicServerDeletion(p)})
}

func parseServerDeletionPath(path string) (int64, bool, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/custom/servers/"), "/")
	if len(parts) != 2 {
		return 0, false, false
	}
	if parts[1] != "deletion-preview" && parts[1] != "delete" {
		return 0, false, false
	}
	id, e := strconv.ParseInt(parts[0], 10, 64)
	return id, parts[1] == "deletion-preview", e == nil && id > 0
}

// Execution payloads can contain node credentials and never belong in a preview.
func publicServerDeletion(p serverDeletionPlan) serverDeletionPlan {
	p.SubscriptionNodes = nil
	p.Identities = nil
	p.Steps = append([]serverDeletionStep(nil), p.Steps...)
	for i := range p.Steps {
		p.Steps[i].Payload = nil
		p.Steps[i].Item = nil
	}
	return p
}
