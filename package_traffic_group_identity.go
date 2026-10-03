package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type trafficGroupIdentityRef struct {
	AssignmentID int64
	Username     string
	Tag          string
	NodeID       int64
	Identity     string
	Credential   string
}

type trafficGroupIdentityBinding struct {
	AssignmentID int64
	Username     string
	Email        string
	NodeIDs      []int64
}

type trafficGroupNodeEntitlement struct {
	Username string
	NodeID   int64
	Tag      string
}

type trafficGroupConfiguredIdentity struct {
	Identity        string
	Protocol        string
	Credential      map[string]any
	SS2022ServerKey string
}

type trafficGroupIdentityData struct {
	ServerID            int64
	Configured          map[string][]trafficGroupConfiguredIdentity
	NodeIdentities      map[int64][]string
	Refs                []trafficGroupIdentityRef
	Bindings            []trafficGroupIdentityBinding
	Entitlements        []trafficGroupNodeEntitlement
	Ownership           connectionOwnershipData
	DisabledCredentials []lifecycleCredentialBackup
}

func (s *postgresAdminSessionStore) trafficGroupIdentityData(ctx context.Context, serverID int64) (trafficGroupIdentityData, error) {
	data := trafficGroupIdentityData{ServerID: serverID, NodeIdentities: map[int64][]string{}}
	var err error
	data.Ownership, err = s.ConnectionOwnership(ctx, strconv.FormatInt(serverID, 10))
	if err != nil {
		return data, err
	}
	var raw string
	err = s.db.QueryRowContext(ctx, `SELECT config_json FROM server_xray_config_snapshots WHERE server_id=$1 AND status='current' ORDER BY created_at DESC,id DESC LIMIT 1`, serverID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return data, nil
	}
	if err != nil {
		return data, err
	}
	data.Configured, err = trafficGroupConfiguredIdentities(raw)
	if err != nil {
		return data, err
	}
	data.DisabledCredentials, err = s.serverDisabledCredentials(ctx, serverID)
	if err != nil {
		return data, err
	}
	coreCredentials := extractCoreInboundCredentials(raw)
	rows, err := s.db.QueryContext(ctx, `
		SELECT assignment_id,username,inbound_tag,0::bigint,email,credential_json FROM package_assignment_inbound_configs WHERE server_id=$1
		UNION ALL
		SELECT 0::bigint,username,inbound_tag,0::bigint,'',credential_json FROM user_inbound_configs WHERE server_id=$1
		UNION ALL
		SELECT a.assignment_id,a.username,COALESCE(n.inbound_tag,''),n.id,a.email,a.credential_json FROM package_assignment_subaccounts a JOIN nodes n ON n.id=a.routed_node_id JOIN remote_servers s ON s.name=n.original_server WHERE s.id=$1 AND a.is_active=1
		UNION ALL
		SELECT 0::bigint,a.username,COALESCE(n.inbound_tag,''),n.id,a.email,a.credential_json FROM user_subaccounts a JOIN nodes n ON n.id=a.routed_node_id JOIN remote_servers s ON s.name=n.original_server WHERE s.id=$1 AND a.is_active=1`, serverID)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var ref trafficGroupIdentityRef
		if err := rows.Scan(&ref.AssignmentID, &ref.Username, &ref.Tag, &ref.NodeID, &ref.Identity, &ref.Credential); err != nil {
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
	// Include packages without traffic groups: the same credential may serve
	// a node in another assignment that must remain available.
	rows, err = s.db.QueryContext(ctx, `
		SELECT a.id,a.username,COALESCE(u.email,''),COALESCE(p.nodes,'[]') FROM user_package_assignments a JOIN packages p ON p.id=a.package_id JOIN users u ON u.username=a.username WHERE a.status='active'
		UNION ALL
		SELECT 0::bigint,u.username,COALESCE(u.email,''),COALESCE(p.nodes,'[]') FROM users u JOIN packages p ON p.id=u.package_id`)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var binding trafficGroupIdentityBinding
		var nodeJSON string
		if err := rows.Scan(&binding.AssignmentID, &binding.Username, &binding.Email, &nodeJSON); err != nil {
			rows.Close()
			return data, err
		}
		if err := json.Unmarshal([]byte(nodeJSON), &binding.NodeIDs); err != nil {
			rows.Close()
			return data, err
		}
		data.Bindings = append(data.Bindings, binding)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	// File subscriptions and per-user outbound routes do not expose a proven
	// node allowlist. NodeID zero keeps the old alias refusal for those relations.
	rows, err = s.db.QueryContext(ctx, `
		SELECT username,0::bigint,'' FROM user_subscriptions
		UNION ALL
		SELECT created_by,0::bigint,'' FROM subscribe_files WHERE created_by<>''
		UNION ALL
		SELECT username,0::bigint,inbound_tag FROM user_outbounds WHERE server_id=$1
		UNION ALL
		SELECT owner_username,node_id,'' FROM forward_chain_nodes
		UNION ALL
		SELECT a.username,f.node_id,'' FROM forward_chain_nodes f JOIN user_package_assignments a ON a.id=f.billing_assignment_id WHERE a.status='active'`, serverID)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var entitlement trafficGroupNodeEntitlement
		if err := rows.Scan(&entitlement.Username, &entitlement.NodeID, &entitlement.Tag); err != nil {
			rows.Close()
			return data, err
		}
		data.Entitlements = append(data.Entitlements, entitlement)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT n.id,COALESCE(n.username,''),COALESCE(n.inbound_tag,''),COALESCE(n.raw_url,''),COALESCE(n.parsed_config,''),COALESCE(n.clash_config,'') FROM nodes n JOIN remote_servers s ON s.name=n.original_server WHERE s.id=$1`, serverID)
	if err != nil {
		return data, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var username, tag, rawURL, parsed, clash string
		if err := rows.Scan(&id, &username, &tag, &rawURL, &parsed, &clash); err != nil {
			return data, err
		}
		data.NodeIdentities[id] = matchNodeProtocolIdentities([]string{rawURL, parsed, clash}, coreCredentials[tag])
		for _, identity := range matchDisabledNodeProtocolIdentities(serverID, username, tag, []string{rawURL, parsed, clash}, data.Configured[tag], data.DisabledCredentials) {
			data.NodeIdentities[id] = appendUniqueString(data.NodeIdentities[id], identity)
		}
	}
	return data, rows.Err()
}

func trafficGroupConfiguredIdentities(raw string) (map[string][]trafficGroupConfiguredIdentity, error) {
	var config struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, fmt.Errorf("traffic groups current Core config: %w", err)
	}
	result := map[string][]trafficGroupConfiguredIdentity{}
	for _, inbound := range config.Inbounds {
		tag, _ := inbound["tag"].(string)
		protocol, _ := inbound["protocol"].(string)
		entries, _, err := inboundCredentialEntries(inbound)
		if tag == "" || err != nil {
			continue
		}
		for _, entry := range entries {
			identity, _ := entry["email"].(string)
			key := trafficGroupAuthenticationKey(protocol)
			if identity = strings.TrimSpace(identity); key != "" && nonEmptyCredentialValue(entry, key) {
				result[tag] = append(result[tag], trafficGroupConfiguredIdentity{Identity: identity, Protocol: protocol, Credential: entry, SS2022ServerKey: lifecycleInboundSS2022ServerKey(inbound)})
			}
		}
	}
	return result, nil
}

func trafficGroupAuthenticationKey(protocol string) string {
	return map[string]string{
		"vless": "id", "vmess": "id", "trojan": "password", "shadowsocks": "password", "ss": "password",
		"anytls": "password", "snell": "psk", "mieru": "username", "hysteria": "auth", "hysteria2": "auth",
		"hy2": "auth", "socks": "user", "http": "user",
	}[strings.ToLower(strings.TrimSpace(protocol))]
}

func trafficGroupSameAuthentication(a, b trafficGroupConfiguredIdentity) bool {
	key := trafficGroupAuthenticationKey(a.Protocol)
	if key == "" || !nonEmptyCredentialValue(a.Credential, key) || !nonEmptyCredentialValue(b.Credential, key) {
		return true
	}
	left, right := fmt.Sprint(a.Credential[key]), fmt.Sprint(b.Credential[key])
	if key == "id" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (data trafficGroupIdentityData) trafficGroupRefIdentities(ref trafficGroupIdentityRef, configured []trafficGroupConfiguredIdentity) []string {
	var credential map[string]any
	if ref.Credential != "" && json.Unmarshal([]byte(ref.Credential), &credential) != nil {
		return nil
	}
	identity := strings.TrimSpace(ref.Identity)
	if identity == "" {
		identity = extractProtocolIdentity(ref.Credential)
	}
	var result []string
	for _, candidate := range configured {
		if candidate.Identity == "" {
			continue
		}
		if identity != "" && candidate.Identity != identity {
			continue
		}
		if len(credential) > 0 {
			matched := credentialsMatch(candidate.Credential, credential, candidate.Protocol)
			if !matched {
				for _, original := range disabledIdentityOriginals(data.ServerID, ref.Username, ref.Tag, candidate, data.DisabledCredentials) {
					if credentialsMatch(original, credential, candidate.Protocol) {
						matched = true
					}
				}
			}
			if !matched {
				continue
			}
		} else if identity == "" {
			continue
		}
		result = appendUniqueString(result, candidate.Identity)
	}
	return result
}

func trafficGroupNodeIdentities(data trafficGroupIdentityData, assignmentID int64, username string, node trafficGroupNode) []string {
	var result []string
	hasRef := false
	for _, ref := range data.Refs {
		if ref.Username != username || ref.Tag != node.Tag || (ref.AssignmentID != 0 && ref.AssignmentID != assignmentID) {
			continue
		}
		if (node.Routed && ref.NodeID != node.ID) || (!node.Routed && ref.NodeID != 0) {
			continue
		}
		hasRef = true
		identities := data.trafficGroupRefIdentities(ref, data.Configured[node.Tag])
		if len(identities) != 1 {
			return nil
		}
		result = appendUniqueString(result, identities[0])
	}
	if hasRef || node.Routed {
		return result
	}
	// Exact configured ownership/email matches are usable for older bindings.
	// A whole-port relation never grants ownership of every runtime identity.
	for _, relation := range data.Ownership.Relations {
		if relation.InboundTag == node.Tag && relation.ManagementUsername == username && relation.ProtocolIdentity != "" && relation.AssignmentType != "user_subaccount" {
			result = appendUniqueString(result, relation.ProtocolIdentity)
		}
	}
	for _, binding := range data.Bindings {
		if binding.AssignmentID == assignmentID && binding.Username == username && binding.Email != "" {
			result = appendUniqueString(result, binding.Email)
		}
	}
	if node.Owner == username {
		for _, identity := range data.NodeIdentities[node.ID] {
			result = appendUniqueString(result, identity)
		}
	}
	var current []string
	for _, identity := range result {
		for _, candidate := range data.Configured[node.Tag] {
			if candidate.Identity == identity {
				current = appendUniqueString(current, identity)
			}
		}
	}
	return current
}

func trafficGroupContainsNode(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func trafficGroupUserEntitledToNode(data trafficGroupIdentityData, username string, node trafficGroupNode) bool {
	if node.Owner == username {
		return true
	}
	for _, binding := range data.Bindings {
		if binding.Username == username && (len(binding.NodeIDs) == 0 || trafficGroupContainsNode(binding.NodeIDs, node.ID)) {
			return true
		}
	}
	for _, ref := range data.Refs {
		if ref.Username == username && ref.NodeID == node.ID {
			return true
		}
	}
	for _, entitlement := range data.Entitlements {
		if entitlement.Username == username && (entitlement.Tag == "" || entitlement.Tag == node.Tag) && (entitlement.NodeID == 0 || entitlement.NodeID == node.ID) {
			return true
		}
	}
	return false
}

func resolveTrafficGroupIdentity(data trafficGroupIdentityData, assignment trafficGroupAssignment, node trafficGroupNode, groupNodeIDs []int64, nodes map[int64]trafficGroupNode) (serverConnectionIdentity, string) {
	missing := serverConnectionIdentity{}
	if node.ServerID != data.ServerID || node.Tag == "" || !trafficGroupContainsNode(groupNodeIDs, node.ID) {
		return missing, "无法确定节点对应的独立入站身份"
	}
	bound := false
	for _, binding := range data.Bindings {
		if binding.AssignmentID == assignment.ID && binding.Username == assignment.Username && (len(binding.NodeIDs) == 0 || trafficGroupContainsNode(binding.NodeIDs, node.ID)) {
			bound = true
		}
	}
	if !bound {
		return missing, "无法确认用户对该套餐节点的有效绑定"
	}
	identities := trafficGroupNodeIdentities(data, assignment.ID, assignment.Username, node)
	if len(identities) != 1 {
		return missing, "无法从当前配置唯一确认用户身份（共享单密钥入站不支持）"
	}
	identity := identities[0]
	configured := data.Configured[node.Tag]
	var match *trafficGroupConfiguredIdentity
	for index := range configured {
		if configured[index].Identity == identity {
			if match != nil {
				return missing, "当前入站有重复身份，无法安全拦截"
			}
			match = &configured[index]
		}
	}
	if match == nil {
		return missing, "用户身份不在当前入站配置中"
	}
	for _, other := range configured {
		if other.Identity != identity && trafficGroupSameAuthentication(*match, other) {
			return missing, "多个入站身份共用认证凭据，无法安全拦截"
		}
	}
	for _, ref := range data.Refs {
		if ref.Tag == node.Tag && ref.Username != assignment.Username {
			otherIdentities := data.trafficGroupRefIdentities(ref, configured)
			if len(otherIdentities) != 1 || otherIdentities[0] == identity {
				return missing, "该身份与其他用户共享或归属不明确，无法安全拦截"
			}
		}
	}
	for _, relation := range data.Ownership.Relations {
		if relation.InboundTag == node.Tag && relation.ManagementUsername != assignment.Username && (relation.ProtocolIdentity == identity || relation.ProtocolIdentity == "") {
			return missing, "该入站存在其他用户的共享或不明确身份，无法安全拦截"
		}
	}
	for _, binding := range data.Bindings {
		for _, other := range nodes {
			if other.ServerID != node.ServerID || other.Tag != node.Tag || (len(binding.NodeIDs) > 0 && !trafficGroupContainsNode(binding.NodeIDs, other.ID)) {
				continue
			}
			otherIdentities := trafficGroupNodeIdentities(data, binding.AssignmentID, binding.Username, other)
			if binding.Username != assignment.Username {
				if len(otherIdentities) != 1 || otherIdentities[0] == identity {
					return missing, "该身份被其他套餐用户共用，无法安全拦截"
				}
				continue
			}
			if !trafficGroupContainsNode(groupNodeIDs, other.ID) && (len(otherIdentities) != 1 || otherIdentities[0] == identity) {
				return missing, "该身份可能同时影响共享组以外的节点，已跳过拦截"
			}
		}
	}
	for _, other := range nodes {
		if other.ServerID != node.ServerID || other.Tag != node.Tag || trafficGroupContainsNode(groupNodeIDs, other.ID) {
			continue
		}
		for _, ref := range data.Refs {
			if ref.Username == assignment.Username && ref.Tag == other.Tag && ref.AssignmentID == 0 && ((ref.NodeID == 0 && !other.Routed) || ref.NodeID == other.ID) && containsString(data.trafficGroupRefIdentities(ref, configured), identity) && trafficGroupUserEntitledToNode(data, assignment.Username, other) {
				return missing, "旧用户凭据同时覆盖共享组以外的节点，已跳过拦截"
			}
			if ref.Username == assignment.Username && ref.Tag == other.Tag && ref.NodeID == other.ID {
				otherIdentities := data.trafficGroupRefIdentities(ref, configured)
				if len(otherIdentities) != 1 || otherIdentities[0] == identity {
					return missing, "该身份可能同时影响用户的组外子账号节点，已跳过拦截"
				}
			}
		}
		if other.Owner == assignment.Username {
			otherIdentities := trafficGroupNodeIdentities(data, 0, assignment.Username, other)
			if len(otherIdentities) != 1 || otherIdentities[0] == identity {
				return missing, "该身份可能同时影响用户拥有的组外节点，已跳过拦截"
			}
		}
	}
	return serverConnectionIdentity{InboundTag: node.Tag, User: identity}, ""
}
