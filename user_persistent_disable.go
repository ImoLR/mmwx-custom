package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	lifecycleActionBlockIdentity   = "BLOCK_IDENTITY"
	lifecycleActionUnblockIdentity = "UNBLOCK_IDENTITY"
	persistentDisableWarning       = "此服务器不支持封禁，只能尽力禁用：官方续期/改套餐/加节点后该用户可能恢复连接"
)

const persistentDisableSchema = `
CREATE TABLE IF NOT EXISTS mmwxc_user_disable_blocks (
    username TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
    server_id BIGINT NOT NULL,
    inbound_tag TEXT NOT NULL,
    identity TEXT NOT NULL,
    PRIMARY KEY(username,server_id,inbound_tag,identity)
);
`

type userAccessNodeStatus struct {
	ServerID   int64  `json:"server_id"`
	ServerName string `json:"server_name"`
	InboundTag string `json:"inbound_tag"`
	NodeID     int64  `json:"node_id,omitempty"`
	NodeName   string `json:"node_name,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

func (s *postgresAdminSessionStore) savePersistentAccessBlocks(ctx context.Context, tx *sql.Tx, username, operation string, items []lifecyclePlanItem) error {
	if operation == lifecycleOperationEnable {
		_, err := tx.ExecContext(ctx, `DELETE FROM mmwxc_user_disable_blocks WHERE username=$1`, username)
		return err
	}
	for _, item := range items {
		for _, identity := range item.persistentIdentities {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mmwxc_user_disable_blocks(username,server_id,inbound_tag,identity) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, username, item.ServerID, identity.InboundTag, identity.User); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *app) accessBlockSupported(ctx context.Context, serverID int64) bool {
	a.connectionMu.Lock()
	record := a.detailedConnections[strconv.FormatInt(serverID, 10)]
	a.connectionMu.Unlock()
	if !helperSupportsTrafficBlocks(record.HelperVersion) || !record.Snapshot.Core.TrafficBlockSupported || !record.Snapshot.Core.Available || record.Snapshot.Core.Version < 7 || time.Since(record.UpdatedAt) > helperStaleTimeout {
		return false
	}
	if store, ok := a.adminStore.(*postgresAdminSessionStore); ok {
		var mode string
		if store.db.QueryRowContext(ctx, `SELECT xray_mode FROM remote_servers WHERE id=$1`, serverID).Scan(&mode) != nil || mode != "external" {
			return false
		}
	}
	return true
}

// Resolve against both current clients and the official credential records.
// The official inactive API can remove clients; its recorded label must still
// be blocked when a later renewal writes that client back.
func persistentAccessIdentities(username string, inbound map[string]any, refs []lifecycleCredentialRef, backups []lifecycleCredentialBackup, business []lifecycleCredentialRef, defaults []map[string]any) ([]serverConnectionIdentity, []lifecycleCredentialRef, string) {
	protocol, _ := inbound["protocol"].(string)
	if protocol == "" && len(refs) > 0 {
		protocol = refs[0].Protocol
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	entries, _, _ := accessInboundCredentialEntries(inbound)
	var targets []map[string]any
	var fallback []lifecycleCredentialRef
	for _, ref := range refs {
		var credential map[string]any
		_ = json.Unmarshal([]byte(ref.CredentialRaw), &credential)
		var matched []map[string]any
		for _, entry := range entries {
			if lifecycleEntryMatchesRefs(entry, inbound, protocol, []lifecycleCredentialRef{ref}) {
				matched = append(matched, entry)
			}
		}
		for _, backup := range backups {
			if len(credential) > 0 && credentialsMatch(credential, backup.OriginalCredential, protocol) {
				for _, entry := range entries {
					if hashJSON(entry) == backup.DisabledHash {
						matched = append(matched, entry)
					}
				}
			}
		}
		if len(matched) > 1 {
			return nil, nil, "同一凭据对应多个客户端，无法安全封禁"
		}
		if len(matched) == 1 {
			credential = matched[0]
		}
		if len(credential) == 0 && ref.Identity != "" {
			credential = map[string]any{"email": ref.Identity}
		}
		if len(matched) == 0 && len(credential) > 0 && !nonEmptyCredentialValue(credential, "email") && ref.Identity != "" {
			credential = cloneLifecycleMap(credential)
			credential["email"] = ref.Identity
		}
		if len(credential) == 0 {
			// An entitlement without its own credential is only redundant when
			// another precise reference on this inbound identifies this user.
			continue
		}
		email, _ := credential["email"].(string)
		if strings.TrimSpace(email) == "" || protocol == "socks" || protocol == "http" {
			fallback = append(fallback, ref)
			continue
		}
		found := false
		for _, target := range targets {
			found = found || hashJSON(target) == hashJSON(credential)
		}
		if !found {
			targets = append(targets, credential)
		}
	}
	for _, backup := range backups {
		email, _ := backup.OriginalCredential["email"].(string)
		if email != "" && protocol != "socks" && protocol != "http" {
			found := false
			for _, target := range targets {
				found = found || target["email"] == email
			}
			if !found {
				targets = append(targets, backup.OriginalCredential)
			}
		}
	}
	if len(targets) == 0 {
		return nil, refs, ""
	}
	for _, target := range targets {
		email := target["email"]
		for _, entry := range entries {
			if entry["email"] == email && !credentialsMatch(entry, target, protocol) || entry["email"] != email && credentialsMatch(entry, target, protocol) {
				return nil, nil, "入站身份或认证凭据被多个客户端共用，无法安全封禁"
			}
		}
		for _, ref := range business {
			if ref.Username == username {
				continue
			}
			var other map[string]any
			_ = json.Unmarshal([]byte(ref.CredentialRaw), &other)
			if ref.Identity == email || other["email"] == email || lifecycleEntryMatchesRefs(target, inbound, protocol, []lifecycleCredentialRef{ref}) || persistentSameAuthentication(target, other, protocol) {
				return nil, nil, "该身份与其他用户、子账户或套餐绑定共用，已跳过封禁"
			}
			var nodeValues []string
			_ = json.Unmarshal([]byte(ref.CredentialRaw), &nodeValues)
			known := ref.Identity != "" || nonEmptyCredentialValue(other, "email") || nonEmptyCredentialValue(other, lifecycleCredentialPrimaryKey(protocol))
			if !known && len(nodeValues) > 0 {
				for _, entry := range entries {
					known = known || lifecycleNodeValuesMatch(entry, inbound, nodeValues)
				}
			}
			if !known {
				return nil, nil, "该入站还有身份不明确的其他用户绑定，无法安全封禁"
			}
		}
		for _, credential := range defaults {
			if credential["email"] == email || credentialsMatch(credential, target, protocol) || persistentSameAuthentication(target, credential, protocol) {
				return nil, nil, "该身份与管理员默认凭据共用，已跳过封禁"
			}
		}
	}
	var result []serverConnectionIdentity
	tag, _ := inbound["tag"].(string)
	if tag == "" && len(refs) > 0 {
		tag = refs[0].InboundTag
	}
	if tag == "" && len(backups) > 0 {
		tag = backups[0].InboundTag
	}
	for _, target := range targets {
		result = append(result, serverConnectionIdentity{InboundTag: tag, User: strings.TrimSpace(fmt.Sprint(target["email"]))})
	}
	return uniqueAccessIdentities(result), fallback, ""
}

func persistentSameAuthentication(left, right map[string]any, protocol string) bool {
	key := lifecycleCredentialPrimaryKey(protocol)
	if key == "" || !nonEmptyCredentialValue(left, key) || !nonEmptyCredentialValue(right, key) {
		return false
	}
	if key == "id" {
		return strings.EqualFold(fmt.Sprint(left[key]), fmt.Sprint(right[key]))
	}
	return fmt.Sprint(left[key]) == fmt.Sprint(right[key])
}

func uniqueAccessIdentities(values []serverConnectionIdentity) []serverConnectionIdentity {
	seen := map[serverConnectionIdentity]bool{}
	result := []serverConnectionIdentity{}
	for _, value := range values {
		if value.InboundTag != "" && value.User != "" && !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].InboundTag != result[j].InboundTag {
			return result[i].InboundTag < result[j].InboundTag
		}
		return result[i].User < result[j].User
	})
	return result
}

func (a *app) planPersistentAccess(ctx context.Context, username string, enable bool, config map[string]any, refs []lifecycleCredentialRef, backups []lifecycleCredentialBackup) (lifecyclePlanItem, bool, error) {
	item := lifecyclePlanItem{Action: lifecycleActionBlockIdentity, Status: lifecycleItemPending}
	if len(refs) > 0 {
		item.ServerID, item.ServerName, item.InboundTag, item.Protocol = refs[0].ServerID, refs[0].ServerName, refs[0].InboundTag, refs[0].Protocol
	} else if len(backups) > 0 {
		item.ServerID, item.ServerName, item.InboundTag, item.Protocol = backups[0].ServerID, backups[0].ServerName, backups[0].InboundTag, backups[0].Protocol
	}
	if !a.accessBlockSupported(ctx, item.ServerID) {
		return item, false, nil
	}
	if enable {
		if len(backups) > 0 {
			item = analyzeAccessInbound(username, true, config, refs, backups)
		}
		item.Action = lifecycleActionUnblockIdentity
		return item, true, nil
	}
	business, err := a.accessBusinessRefs(ctx, item.ServerID, item.ServerName, item.InboundTag, username)
	if err != nil {
		return item, true, err
	}
	defaults, err := a.accessDefaultCredentials(ctx, item.ServerID, item.InboundTag)
	if err != nil {
		return item, true, err
	}
	inbound := findConfigInbound(config, item.InboundTag)
	identities, fallback, reason := persistentAccessIdentities(username, inbound, refs, backups, business, defaults)
	if reason != "" {
		item.Action, item.Status, item.LastError, item.DecisionNote = lifecycleActionConflict, lifecycleItemFailed, reason, reason
		return item, true, nil
	}
	if len(identities) == 0 {
		return item, false, nil
	}
	if len(fallback) > 0 {
		item = analyzeAccessInbound(username, false, config, fallback, backups)
		if reason := accessCredentialSharingReason(inbound, item, business, defaults); reason != "" {
			item.Action, item.Status, item.LastError, item.DecisionNote = lifecycleActionConflict, lifecycleItemFailed, reason, reason
			item.replacementInbound, item.accessCredentials = nil, nil
		}
		if item.Status == lifecycleItemFailed {
			return item, true, nil
		}
	} else if len(backups) > 0 {
		item = analyzeAccessInbound(username, true, config, refs, backups)
		if item.Status == lifecycleItemFailed {
			return item, true, nil
		}
	}
	item.Action, item.Status, item.persistentIdentities = lifecycleActionBlockIdentity, lifecycleItemPending, identities
	item.DecisionNote = "封禁用户身份，官方重推凭据后仍保持禁用"
	if len(fallback) > 0 {
		item.DecisionNote += "；部分凭据没有独立客户端身份。" + persistentDisableWarning
	}
	return item, true, nil
}

func (a *app) executePersistentAccessItem(ctx context.Context, token, username string, enable bool, item *lifecyclePlanItem) error {
	if _, ok := a.adminStore.(*postgresAdminSessionStore); !ok {
		a.trafficGroupsMu.Lock()
		if a.disabledUserBlocks == nil {
			a.disabledUserBlocks = map[string]map[string][]serverConnectionIdentity{}
		}
		if enable {
			delete(a.disabledUserBlocks, username)
		} else {
			if a.disabledUserBlocks[username] == nil {
				a.disabledUserBlocks[username] = map[string][]serverConnectionIdentity{}
			}
			a.disabledUserBlocks[username][strconv.FormatInt(item.ServerID, 10)] = item.persistentIdentities
		}
		a.trafficGroupsMu.Unlock()
	}
	if err := a.refreshDisabledUsers(ctx); err != nil {
		return err
	}
	if item.replacementInbound != nil {
		if !enable && item.accessEnable && !a.accessIdentitiesConfirmed(item.ServerID, item.persistentIdentities) {
			return errors.New("封禁已安排，等待服务器确认后恢复旧凭据；可稍后重试")
		}
		if err := a.executeAccessItem(ctx, token, item); err != nil {
			return err
		}
	}
	if store, ok := a.adminStore.(*postgresAdminSessionStore); ok && item.accessEnable {
		_, err := store.db.ExecContext(ctx, `DELETE FROM mmwxc_user_disabled_credentials WHERE username=$1 AND server_id=$2 AND inbound_tag=$3`, username, item.ServerID, item.InboundTag)
		return err
	}
	return nil
}

func (a *app) previewUserAccess(ctx context.Context, token, username string) ([]userAccessNodeStatus, error) {
	plan, err := a.buildAccessPlan(ctx, token, username, false)
	if err != nil {
		return nil, err
	}
	data := persistentAccessData{}
	if store, ok := a.adminStore.(*postgresAdminSessionStore); ok {
		data, err = store.persistentAccessData(ctx)
		if err != nil {
			return nil, err
		}
	}
	result := []userAccessNodeStatus{}
	for _, item := range plan {
		status, reason := "best_effort", persistentDisableWarning
		if item.Action == lifecycleActionBlockIdentity {
			status, reason = "blocked", item.DecisionNote
		}
		if item.Status == lifecycleItemFailed {
			status, reason = "conflict", item.LastError
		}
		result = append(result, accessNodeStatuses(data, username, item.ServerID, item.ServerName, item.InboundTag, status, reason)...)
		if item.Action == lifecycleActionBlockIdentity && strings.Contains(item.DecisionNote, persistentDisableWarning) {
			result = append(result, accessNodeStatuses(data, username, item.ServerID, item.ServerName, item.InboundTag, "best_effort", "部分凭据没有独立客户端身份。"+persistentDisableWarning)...)
		}
	}
	return result, nil
}

func accessNodeStatuses(data persistentAccessData, username string, serverID int64, serverName, tag, status, reason string) []userAccessNodeStatus {
	result := []userAccessNodeStatus{}
	for _, node := range data.Nodes {
		if node.ServerID == serverID && node.Tag == tag && (data.NodeUsers[username][node.ID] || len(data.NodeUsers[username]) == 0) {
			result = append(result, userAccessNodeStatus{ServerID: serverID, ServerName: serverName, InboundTag: tag, NodeID: node.ID, NodeName: node.Name, Status: status, Reason: reason})
		}
	}
	if len(result) == 0 {
		result = append(result, userAccessNodeStatus{ServerID: serverID, ServerName: serverName, InboundTag: tag, Status: status, Reason: reason})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NodeID < result[j].NodeID })
	return result
}

func (a *app) attachUserAccess(states map[string]managedUserLifecycle) {
	a.trafficGroupsMu.Lock()
	defer a.trafficGroupsMu.Unlock()
	for username, state := range states {
		if state.DesiredState == lifecycleStateDisabled {
			state.Access = append([]userAccessNodeStatus{}, a.disabledUserAccess[username]...)
			states[username] = state
		}
	}
}

func (a *app) refreshDisabledUsers(ctx context.Context) error {
	a.trafficGroupsMu.Lock()
	defer a.trafficGroupsMu.Unlock()
	if store, ok := a.adminStore.(*postgresAdminSessionStore); ok && !a.trafficGroupsReady {
		if err := a.refreshTrafficGroupsLocked(ctx, store); err != nil {
			return err
		}
	}
	return a.refreshDisabledUsersLocked(ctx)
}

func (a *app) refreshDisabledUsersLocked(ctx context.Context) error {
	store, ok := a.adminStore.(*postgresAdminSessionStore)
	if !ok {
		a.disabledUsersReady = true
		return nil
	}
	err := a.evaluateDisabledUsers(ctx, store)
	if err != nil {
		a.disabledUsersReady = false
	}
	return err
}

func (a *app) evaluateDisabledUsers(ctx context.Context, store *postgresAdminSessionStore) error {
	states, err := store.LifecycleStates(ctx)
	if err != nil {
		return err
	}
	data, err := store.persistentAccessData(ctx)
	if err != nil {
		return err
	}
	blocks := map[string]map[string][]serverConnectionIdentity{}
	statuses := map[string][]userAccessNodeStatus{}
	for username, state := range states {
		if state.DesiredState != lifecycleStateDisabled {
			continue
		}
		refs := append([]lifecycleCredentialRef{}, data.Refs[username]...)
		previous := map[string]map[serverConnectionIdentity]bool{}
		rows, err := store.db.QueryContext(ctx, `SELECT server_id,inbound_tag,identity FROM mmwxc_user_disable_blocks WHERE username=$1`, username)
		if err != nil {
			return err
		}
		for rows.Next() {
			ref := lifecycleCredentialRef{Username: username, Source: "persistent_disable"}
			if err := rows.Scan(&ref.ServerID, &ref.InboundTag, &ref.Identity); err != nil {
				rows.Close()
				return err
			}
			key := strconv.FormatInt(ref.ServerID, 10)
			if previous[key] == nil {
				previous[key] = map[serverConnectionIdentity]bool{}
			}
			previous[key][serverConnectionIdentity{InboundTag: ref.InboundTag, User: ref.Identity}] = true
			// Prefer the full current credential when one still records this label.
			found := false
			for _, existing := range refs {
				found = found || existing.ServerID == ref.ServerID && existing.InboundTag == ref.InboundTag && (existing.Identity == ref.Identity || extractProtocolIdentity(existing.CredentialRaw) == ref.Identity)
			}
			if !found {
				refs = append(refs, ref)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		backups, err := store.LifecycleDisabledCredentials(ctx, username)
		if err != nil {
			return err
		}
		grouped := map[string][]lifecycleCredentialRef{}
		for _, ref := range refs {
			key := lifecycleInboundKey(ref.ServerID, ref.InboundTag)
			grouped[key] = append(grouped[key], ref)
		}
		for _, backup := range backups {
			key := lifecycleInboundKey(backup.ServerID, backup.InboundTag)
			if len(grouped[key]) == 0 {
				raw, _ := json.Marshal(backup.OriginalCredential)
				grouped[key] = []lifecycleCredentialRef{{Username: username, ServerID: backup.ServerID, ServerName: backup.ServerName, InboundTag: backup.InboundTag, Protocol: backup.Protocol, CredentialRaw: string(raw)}}
			}
		}
		userBlocks := map[string][]serverConnectionIdentity{}
		for _, group := range grouped {
			first := group[0]
			var raw string
			err := store.db.QueryRowContext(ctx, `SELECT config_json FROM server_xray_config_snapshots WHERE server_id=$1 AND status='current' ORDER BY created_at DESC,id DESC LIMIT 1`, first.ServerID).Scan(&raw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			config := map[string]any{}
			if raw != "" && json.Unmarshal([]byte(raw), &config) != nil {
				return errors.New("服务器配置快照无效")
			}
			var groupBackups []lifecycleCredentialBackup
			for _, backup := range backups {
				if backup.ServerID == first.ServerID && backup.InboundTag == first.InboundTag {
					groupBackups = append(groupBackups, backup)
				}
			}
			defaults, err := a.accessDefaultCredentials(ctx, first.ServerID, first.InboundTag)
			if err != nil {
				return err
			}
			identities, fallback, reason := persistentAccessIdentities(username, findConfigInbound(config, first.InboundTag), group, groupBackups, data.businessRefs(first.ServerID, first.InboundTag, username), defaults)
			status := "best_effort"
			if reason != "" {
				status = "conflict"
			} else if len(identities) > 0 {
				serverID := strconv.FormatInt(first.ServerID, 10)
				supported := a.accessBlockSupported(ctx, first.ServerID)
				for _, identity := range identities {
					if supported || previous[serverID][identity] {
						userBlocks[serverID] = append(userBlocks[serverID], identity)
					}
				}
				status, reason = "best_effort", persistentDisableWarning
				if supported {
					status, reason = "blocked", ""
					if !a.accessIdentitiesConfirmed(first.ServerID, identities) {
						status, reason = "pending", "封禁已安排，等待服务器确认"
					}
				}
				if supported && len(groupBackups) > 0 {
					reason = "封禁已安排；旧凭据替换等待安全恢复"
				}
			} else {
				reason = persistentDisableWarning
			}
			statuses[username] = append(statuses[username], accessNodeStatuses(data, username, first.ServerID, first.ServerName, first.InboundTag, status, reason)...)
			if len(fallback) > 0 && status != "best_effort" && status != "conflict" {
				statuses[username] = append(statuses[username], accessNodeStatuses(data, username, first.ServerID, first.ServerName, first.InboundTag, "best_effort", "部分凭据没有独立客户端身份。"+persistentDisableWarning)...)
			}
		}
		for id := range data.NodeUsers[username] {
			node := data.Nodes[id]
			if node.ServerID == 0 || node.Tag == "" {
				statuses[username] = append(statuses[username], userAccessNodeStatus{ServerID: node.ServerID, ServerName: node.ServerName, NodeID: node.ID, NodeName: node.Name, Status: "best_effort", Reason: persistentDisableWarning})
			}
		}
		// Serialize with SaveAccessPlan: a completed enable must not be undone
		// by an evaluation which started before the administrator enabled it.
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var desired string
		err = tx.QueryRowContext(ctx, `SELECT desired_state FROM mmwxc_user_lifecycle WHERE username=$1 FOR UPDATE`, username).Scan(&desired)
		if errors.Is(err, sql.ErrNoRows) || err == nil && desired != lifecycleStateDisabled {
			tx.Rollback()
			continue
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM mmwxc_user_disable_blocks WHERE username=$1`, username); err != nil {
			tx.Rollback()
			return err
		}
		for serverID, identities := range userBlocks {
			for _, identity := range uniqueAccessIdentities(identities) {
				if _, err = tx.ExecContext(ctx, `INSERT INTO mmwxc_user_disable_blocks VALUES($1,$2,$3,$4)`, username, serverID, identity.InboundTag, identity.User); err != nil {
					tx.Rollback()
					return err
				}
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		blocks[username] = userBlocks
	}
	a.disabledUserBlocks, a.disabledUserAccess, a.disabledUsersReady = blocks, statuses, true
	return nil
}

func (a *app) accessIdentitiesConfirmed(serverID int64, identities []serverConnectionIdentity) bool {
	if len(identities) == 0 {
		return false
	}
	a.connectionMu.Lock()
	defer a.connectionMu.Unlock()
	record := a.detailedConnections[strconv.FormatInt(serverID, 10)]
	if time.Since(record.UpdatedAt) > helperStaleTimeout {
		return false
	}
	for _, identity := range identities {
		found := false
		for _, user := range record.Snapshot.ProxyUsers {
			found = found || user.Identity == identity && user.Blocked
		}
		if !found {
			return false
		}
	}
	return true
}
