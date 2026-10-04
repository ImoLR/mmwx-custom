package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	lifecycleOperationDisable = "disable"
	lifecycleOperationEnable  = "enable"

	lifecycleStateDisabling         = "disabling"
	lifecycleStateEnabling          = "enabling"
	lifecycleStatePartiallyDisabled = "partially_disabled"
	lifecycleStatePartiallyEnabled  = "partially_enabled"
	lifecycleStateDisabled          = "disabled"

	lifecycleActionReplaceCredential = "REPLACE_CREDENTIAL"
	lifecycleDisabledPrefix          = "mmwxc-disabled-"
)

func (s *postgresAdminSessionStore) LifecycleDisabledCredentials(ctx context.Context, username string) ([]lifecycleCredentialBackup, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT username,server_id,server_name,inbound_tag,protocol,credential_key,
		original_credential,disabled_credential,original_hash,disabled_hash
		FROM mmwxc_user_disabled_credentials WHERE username=$1 ORDER BY server_id,inbound_tag,credential_key`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []lifecycleCredentialBackup
	for rows.Next() {
		var item lifecycleCredentialBackup
		var originalRaw, disabledRaw []byte
		if err := rows.Scan(&item.Username, &item.ServerID, &item.ServerName, &item.InboundTag, &item.Protocol,
			&item.CredentialKey, &originalRaw, &disabledRaw, &item.OriginalHash, &item.DisabledHash); err != nil {
			return nil, err
		}
		if json.Unmarshal(originalRaw, &item.OriginalCredential) != nil || json.Unmarshal(disabledRaw, &item.DisabledCredential) != nil {
			return nil, errors.New("stored lifecycle credential is invalid")
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *postgresAdminSessionStore) LatestAccessOperation(ctx context.Context, username, operation string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, progress, partial := accessLifecycleStates(operation)
	var operationID string
	err := s.db.QueryRowContext(ctx, `SELECT operation_id FROM mmwxc_user_lifecycle_operations
		WHERE username=$1 AND operation=$2 AND state IN ($3,$4) ORDER BY updated_at DESC LIMIT 1`,
		username, operation, progress, partial).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return operationID, err
}

func accessLifecycleStates(operation string) (desired, progress, partial string) {
	if operation == lifecycleOperationEnable {
		return lifecycleStateEnabled, lifecycleStateEnabling, lifecycleStatePartiallyEnabled
	}
	return lifecycleStateDisabled, lifecycleStateDisabling, lifecycleStatePartiallyDisabled
}

func (s *postgresAdminSessionStore) SaveAccessPlan(ctx context.Context, username, operation, operationID string, items []lifecyclePlanItem) error {
	desired, progress, _ := accessLifecycleStates(operation)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO mmwxc_user_lifecycle(username,desired_state,effective_state,operation,pending_count,last_error)
		VALUES($1,$2,$3,$4,0,'') ON CONFLICT(username) DO UPDATE SET desired_state=$2,effective_state=$3,operation=$4,last_error='',updated_at=CURRENT_TIMESTAMP`,
		username, desired, progress, operation); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO mmwxc_user_lifecycle_operations(operation_id,username,operation,state,pending_count,last_error)
		VALUES($1,$2,$3,$4,0,'') ON CONFLICT(operation_id) DO UPDATE SET state=$4,last_error='',updated_at=CURRENT_TIMESTAMP`,
		operationID, username, operation, progress); err != nil {
		return err
	}
	pending := 0
	for _, item := range items {
		if item.Status != lifecycleItemCompleted {
			pending++
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO mmwxc_user_lifecycle_items(operation_id,server_id,server_name,inbound_tag,protocol,action,status,remaining_users,last_error,last_checked_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(operation_id,server_id,inbound_tag) DO UPDATE SET
			server_name=EXCLUDED.server_name,protocol=EXCLUDED.protocol,action=EXCLUDED.action,status=EXCLUDED.status,
			remaining_users=EXCLUDED.remaining_users,last_error=EXCLUDED.last_error,last_checked_at=EXCLUDED.last_checked_at,updated_at=CURRENT_TIMESTAMP`,
			operationID, item.ServerID, item.ServerName, item.InboundTag, item.Protocol, item.Action, item.Status,
			item.RemainingUsers, item.LastError, item.LastCheckedAt); err != nil {
			return err
		}
		for _, backup := range item.accessCredentials {
			originalRaw, marshalErr := json.Marshal(backup.OriginalCredential)
			if marshalErr != nil {
				return marshalErr
			}
			disabledRaw, marshalErr := json.Marshal(backup.DisabledCredential)
			if marshalErr != nil {
				return marshalErr
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO mmwxc_user_disabled_credentials(
				username,server_id,server_name,inbound_tag,protocol,credential_key,original_credential,disabled_credential,original_hash,disabled_hash)
				VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10)
				ON CONFLICT(username,server_id,inbound_tag,credential_key) DO UPDATE SET
				server_name=EXCLUDED.server_name,protocol=EXCLUDED.protocol,original_credential=EXCLUDED.original_credential,
				disabled_credential=EXCLUDED.disabled_credential,original_hash=EXCLUDED.original_hash,
				disabled_hash=EXCLUDED.disabled_hash,updated_at=CURRENT_TIMESTAMP`,
				username, backup.ServerID, backup.ServerName, backup.InboundTag, backup.Protocol, backup.CredentialKey,
				string(originalRaw), string(disabledRaw), backup.OriginalHash, backup.DisabledHash); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET pending_count=$2,updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, operationID, pending); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle SET pending_count=$2,updated_at=CURRENT_TIMESTAMP WHERE username=$1`, username, pending); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresAdminSessionStore) FinishAccessAttempt(ctx context.Context, username, operationID, operation string, pending int, lastError string) error {
	desired, _, partial := accessLifecycleStates(operation)
	state := desired
	if pending > 0 {
		state = partial
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET state=$2,pending_count=$3,last_error=$4,updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, operationID, state, pending, lastError); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle SET desired_state=$2,effective_state=$3,operation=$4,pending_count=$5,last_error=$6,updated_at=CURRENT_TIMESTAMP WHERE username=$1`,
		username, desired, state, operation, pending, lastError); err != nil {
		return err
	}
	if operation == lifecycleOperationEnable && pending == 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM mmwxc_user_disabled_credentials WHERE username=$1`, username); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *app) buildAccessPlan(ctx context.Context, token, username string, enable bool) ([]lifecyclePlanItem, error) {
	store := a.adminStore.(lifecycleStore)
	refs, err := store.LifecycleCredentialRefs(ctx, username)
	if err != nil {
		return nil, err
	}
	backups, err := store.LifecycleDisabledCredentials(ctx, username)
	if err != nil {
		return nil, err
	}
	type accessGroup struct {
		refs    []lifecycleCredentialRef
		backups []lifecycleCredentialBackup
	}
	grouped := make(map[string]*accessGroup)
	for _, ref := range refs {
		key := strconv.FormatInt(ref.ServerID, 10) + "\x00" + ref.InboundTag
		if grouped[key] == nil {
			grouped[key] = &accessGroup{}
		}
		grouped[key].refs = append(grouped[key].refs, ref)
	}
	for _, backup := range backups {
		key := strconv.FormatInt(backup.ServerID, 10) + "\x00" + backup.InboundTag
		if grouped[key] == nil {
			grouped[key] = &accessGroup{}
		}
		grouped[key].backups = append(grouped[key].backups, backup)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	configs := make(map[int64]map[string]any)
	items := make([]lifecyclePlanItem, 0, len(keys))
	for _, key := range keys {
		group := grouped[key]
		var serverID int64
		var serverName, inboundTag, protocol string
		if len(group.refs) > 0 {
			serverID, serverName, inboundTag, protocol = group.refs[0].ServerID, group.refs[0].ServerName, group.refs[0].InboundTag, group.refs[0].Protocol
		} else {
			serverID, serverName, inboundTag, protocol = group.backups[0].ServerID, group.backups[0].ServerName, group.backups[0].InboundTag, group.backups[0].Protocol
		}
		config := configs[serverID]
		if config == nil {
			config, err = a.fetchOfficialXrayConfig(ctx, token, serverID)
			if err != nil {
				items = append(items, lifecyclePlanItem{ServerID: serverID, ServerName: serverName, InboundTag: inboundTag, Protocol: protocol,
					Action: lifecycleActionReplaceCredential, Status: lifecycleItemFailed, LastError: "读取真实 Xray 配置失败"})
				continue
			}
			configs[serverID] = config
		}
		item := analyzeAccessInbound(username, enable, config, group.refs, group.backups)
		if !enable && item.Status != lifecycleItemFailed {
			businessRefs, businessErr := store.LifecycleInboundBusinessRefs(ctx, serverID, serverName, inboundTag, username)
			defaultCredentials, defaultErr := store.LifecycleDefaultAdminCredentials(ctx, serverID, inboundTag)
			message := ""
			switch {
			case businessErr != nil:
				message = "读取业务用户关系失败，拒绝禁用"
			case defaultErr != nil:
				message = "核对管理员默认凭据失败，拒绝禁用"
			default:
				message = accessCredentialSharingReason(findConfigInbound(config, inboundTag), item, businessRefs, defaultCredentials)
			}
			if message != "" {
				item.Action, item.Status, item.LastError = lifecycleActionConflict, lifecycleItemFailed, message
				item.DecisionNote = message
				item.replacementInbound, item.accessCredentials = nil, nil
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func accessCredentialSharingReason(inbound map[string]any, item lifecyclePlanItem, businessRefs []lifecycleCredentialRef, defaultCredentials []map[string]any) string {
	_, key, _ := accessInboundCredentialEntries(inbound)
	for _, ref := range businessRefs {
		if key == "password" || (strings.TrimSpace(ref.CredentialRaw) == "" && strings.TrimSpace(ref.Identity) == "") {
			return "该端口还有其他用户、子账户或套餐绑定使用，无法单独禁用此用户"
		}
		for _, backup := range item.accessCredentials {
			if lifecycleEntryMatchesRefs(backup.OriginalCredential, inbound, item.Protocol, []lifecycleCredentialRef{ref}) ||
				lifecycleEntryMatchesRefs(backup.DisabledCredential, inbound, item.Protocol, []lifecycleCredentialRef{ref}) {
				return "该认证凭据被其他用户、子账户或套餐绑定共用，拒绝替换"
			}
		}
	}
	for _, credential := range defaultCredentials {
		for _, backup := range item.accessCredentials {
			if credentialsMatch(backup.OriginalCredential, credential, item.Protocol) || credentialsMatch(backup.DisabledCredential, credential, item.Protocol) {
				return "该认证凭据与管理员默认凭据共用，拒绝替换"
			}
		}
	}
	return ""
}

func accessInboundCredentialEntries(inbound map[string]any) ([]map[string]any, string, error) {
	settings, _ := inbound["settings"].(map[string]any)
	protocol, _ := inbound["protocol"].(string)
	if (protocol == "shadowsocks" || protocol == "ss") && nonEmptyCredentialValue(settings, "password") {
		clients, exists := settings["clients"]
		entries, array := clients.([]any)
		if !exists || (array && len(entries) == 0) {
			credential := map[string]any{"password": settings["password"]}
			for _, key := range []string{"email", "level"} {
				if value, ok := settings[key]; ok {
					credential[key] = value
				}
			}
			return []map[string]any{credential}, "password", nil
		}
	}
	return inboundCredentialEntries(inbound)
}

func analyzeAccessInbound(username string, enable bool, config map[string]any, refs []lifecycleCredentialRef, backups []lifecycleCredentialBackup) lifecyclePlanItem {
	item := lifecyclePlanItem{Action: lifecycleActionReplaceCredential, Status: lifecycleItemPending}
	if len(refs) > 0 {
		item.ServerID, item.ServerName, item.InboundTag, item.Protocol = refs[0].ServerID, refs[0].ServerName, refs[0].InboundTag, refs[0].Protocol
	} else if len(backups) > 0 {
		item.ServerID, item.ServerName, item.InboundTag, item.Protocol = backups[0].ServerID, backups[0].ServerName, backups[0].InboundTag, backups[0].Protocol
	} else {
		item.Status, item.LastError = lifecycleItemFailed, "没有可管理的 credential"
		return item
	}
	inbound := findConfigInbound(config, item.InboundTag)
	if inbound == nil {
		item.Status, item.LastError = lifecycleItemFailed, "目标 Inbound 不存在"
		return item
	}
	item.Protocol = strings.ToLower(strings.TrimSpace(fmt.Sprint(inbound["protocol"])))
	settings, _ := inbound["settings"].(map[string]any)
	if item.Protocol == "socks" && strings.TrimSpace(fmt.Sprint(settings["auth"])) != "password" {
		item.Status, item.LastError = lifecycleItemFailed, "匿名 SOCKS 端口无法单独禁用或启用用户"
		return item
	}
	entries, key, err := accessInboundCredentialEntries(inbound)
	if err != nil {
		item.Status, item.LastError = lifecycleItemFailed, err.Error()
		return item
	}
	backupByKey := make(map[string]lifecycleCredentialBackup)
	for _, backup := range backups {
		backupByKey[backup.CredentialKey] = backup
	}
	type expectedCredential struct {
		key      string
		original map[string]any
		backup   *lifecycleCredentialBackup
	}
	var expected []expectedCredential
	seen := make(map[string]bool)
	for _, ref := range refs {
		var credential map[string]any
		if json.Unmarshal([]byte(ref.CredentialRaw), &credential) != nil || len(credential) == 0 {
			var rawValues []string
			if (key != "password" && lifecycleInboundSS2022ServerKey(inbound) == "") || json.Unmarshal([]byte(ref.CredentialRaw), &rawValues) != nil {
				continue
			}
			for _, actual := range entries {
				if !lifecycleNodeValuesMatch(actual, inbound, rawValues) {
					continue
				}
				if credential != nil {
					item.Status, item.LastError = lifecycleItemFailed, "节点 credential 对应多个 runtime credential，需要人工检查"
					return item
				}
				credential = actual
			}
			if credential == nil {
				continue
			}
		}
		credentialKey := accessCredentialKey(item.Protocol, credential)
		if credentialKey == "" || seen[credentialKey] {
			continue
		}
		seen[credentialKey] = true
		entry := expectedCredential{key: credentialKey, original: credential}
		if backup, ok := backupByKey[credentialKey]; ok {
			copy := backup
			entry.backup = &copy
			entry.original = copy.OriginalCredential
		}
		expected = append(expected, entry)
	}
	for credentialKey, backup := range backupByKey {
		if seen[credentialKey] {
			continue
		}
		copy := backup
		expected = append(expected, expectedCredential{key: credentialKey, original: copy.OriginalCredential, backup: &copy})
	}
	sort.Slice(expected, func(i, j int) bool { return expected[i].key < expected[j].key })
	if len(expected) == 0 {
		item.Status, item.LastError = lifecycleItemFailed, "缺少可恢复的结构化 credential"
		return item
	}
	replacements := make(map[int]map[string]any)
	for _, wanted := range expected {
		var original, disabled map[string]any
		if wanted.backup != nil {
			original, disabled = wanted.backup.OriginalCredential, wanted.backup.DisabledCredential
		}
		matchedIndex := -1
		matchedState := ""
		for index, actual := range entries {
			if original != nil && credentialsMatch(actual, original, item.Protocol) {
				if hashJSON(actual) != wanted.backup.OriginalHash {
					item.Status, item.LastError = lifecycleItemFailed, "原 credential 配置已发生漂移"
					return item
				}
				matchedIndex, matchedState = index, "original"
				break
			}
			if disabled != nil && credentialsMatch(actual, disabled, item.Protocol) {
				if hashJSON(actual) != wanted.backup.DisabledHash {
					item.Status, item.LastError = lifecycleItemFailed, "禁用 credential 配置已发生漂移"
					return item
				}
				matchedIndex, matchedState = index, "disabled"
				break
			}
			if original == nil && credentialsMatch(actual, wanted.original, item.Protocol) {
				matchedIndex, matchedState = index, "original"
				original = cloneLifecycleMap(actual)
				break
			}
		}
		if matchedIndex < 0 {
			item.Status, item.LastError = lifecycleItemFailed, "真实配置中找不到原 credential 或禁用 credential"
			return item
		}
		if wanted.backup == nil {
			if enable {
				item.Status, item.LastError = lifecycleItemFailed, "缺少禁用 credential 备份，拒绝盲目启用"
				return item
			}
			disabled, err = disabledLifecycleCredential(item.Protocol, original, username, item.ServerID, item.InboundTag, wanted.key, inbound)
			if err != nil {
				item.Status, item.LastError = lifecycleItemFailed, err.Error()
				return item
			}
			backup := lifecycleCredentialBackup{
				Username: username, ServerID: item.ServerID, ServerName: item.ServerName, InboundTag: item.InboundTag,
				Protocol: item.Protocol, CredentialKey: wanted.key, OriginalCredential: original, DisabledCredential: disabled,
				OriginalHash: hashJSON(original), DisabledHash: hashJSON(disabled),
			}
			item.accessCredentials = append(item.accessCredentials, backup)
		} else {
			item.accessCredentials = append(item.accessCredentials, *wanted.backup)
		}
		desiredState := "disabled"
		desiredCredential := disabled
		if enable {
			desiredState, desiredCredential = "original", original
		}
		if matchedState != desiredState {
			replacements[matchedIndex] = desiredCredential
		}
	}
	item.RemainingUsers = len(entries) - len(expected)
	if len(replacements) == 0 {
		item.Status = lifecycleItemCompleted
		return item
	}
	replacement := cloneLifecycleMap(inbound)
	settings, _ = replacement["settings"].(map[string]any)
	rawEntries := make([]any, 0, len(entries))
	for index, entry := range entries {
		if changed := replacements[index]; changed != nil {
			rawEntries = append(rawEntries, changed)
		} else {
			rawEntries = append(rawEntries, entry)
		}
	}
	if len(rawEntries) == 0 {
		item.Status, item.LastError = lifecycleItemFailed, "credential 替换不得产生空 Inbound"
		return item
	}
	if key == "password" {
		settings[key] = rawEntries[0].(map[string]any)[key]
	} else {
		settings[key] = rawEntries
	}
	item.sourceInboundHash = hashJSON(inbound)
	item.replacementInbound = replacement
	return item
}

func accessCredentialKey(protocol string, credential map[string]any) string {
	value := lifecycleCredentialPrimaryValue(credential, protocol)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(protocol) + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func lifecycleCredentialPrimaryKey(protocol string) string {
	return map[string]string{
		"vless": "id", "vmess": "id", "trojan": "password", "shadowsocks": "password", "ss": "password",
		"anytls": "password", "snell": "psk", "mieru": "username", "hysteria": "auth", "hysteria2": "auth",
		"hy2": "auth", "socks": "user", "http": "user",
	}[strings.ToLower(protocol)]
}

func lifecycleCredentialPrimaryValue(credential map[string]any, protocol string) string {
	return strings.TrimSpace(fmt.Sprint(credential[lifecycleCredentialPrimaryKey(protocol)]))
}

func disabledLifecycleCredential(protocol string, original map[string]any, username string, serverID int64, tag, credentialKey string, inbound map[string]any) (map[string]any, error) {
	credential := cloneLifecycleMap(original)
	markerSum := sha256.Sum256([]byte(username + "\x00" + strconv.FormatInt(serverID, 10) + "\x00" + tag + "\x00" + credentialKey))
	marker := lifecycleDisabledPrefix + hex.EncodeToString(markerSum[:12])
	randomText, err := randomLifecycleText(24)
	if err != nil {
		return nil, errors.New("无法生成禁用 credential")
	}
	switch protocol {
	case "vless", "vmess":
		credential["id"], err = randomLifecycleUUID()
	case "shadowsocks", "ss":
		length := shadowsocksCredentialLength(inbound, fmt.Sprint(original["password"]))
		value := make([]byte, length)
		if _, err = rand.Read(value); err == nil {
			credential["password"] = base64.StdEncoding.EncodeToString(value)
		}
	case "mieru":
		credential["username"] = marker
		credential["password"] = randomText
	case "socks", "http":
		credential["user"] = marker
		if _, ok := credential["pass"]; ok {
			credential["pass"] = randomText
		} else {
			credential["password"] = randomText
		}
	default:
		primary := lifecycleCredentialPrimaryKey(protocol)
		if primary == "" {
			return nil, fmt.Errorf("协议 %s 暂不支持安全 credential 替换", protocol)
		}
		credential[primary] = randomText
	}
	if err != nil {
		return nil, errors.New("无法生成禁用 credential")
	}
	return credential, nil
}

func shadowsocksCredentialLength(inbound map[string]any, current string) int {
	if decoded, err := base64.StdEncoding.DecodeString(current); err == nil && len(decoded) > 0 {
		return len(decoded)
	}
	settings, _ := inbound["settings"].(map[string]any)
	method := strings.ToLower(strings.TrimSpace(fmt.Sprint(settings["method"])))
	if strings.Contains(method, "aes-128") {
		return 16
	}
	return 32
}

func randomLifecycleText(length int) (string, error) {
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func randomLifecycleUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	hexValue := hex.EncodeToString(value)
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:], nil
}

func cloneLifecycleMap(value map[string]any) map[string]any {
	raw, _ := json.Marshal(value)
	var clone map[string]any
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func (a *app) executeAccessPlan(ctx context.Context, token, username, operationID, operation string, items []lifecyclePlanItem) lifecycleDeleteResult {
	store := a.adminStore.(lifecycleStore)
	_, progress, partial := accessLifecycleStates(operation)
	result := lifecycleDeleteResult{Username: username, OperationID: operationID, State: progress, Items: items}
	lastError := ""
	for index := range result.Items {
		item := &result.Items[index]
		if item.Status == lifecycleItemCompleted {
			continue
		}
		if item.Status == lifecycleItemFailed && item.replacementInbound == nil {
			lastError = item.LastError
			continue
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := a.executeAccessItem(attemptCtx, token, item)
		cancel()
		if err != nil {
			item.Status, item.LastError = lifecycleItemFailed, lifecycleSafeError(err)
			lastError = item.LastError
		} else {
			item.Status, item.LastError = lifecycleItemCompleted, ""
		}
		item.Attempts++
		now := time.Now().UTC()
		item.LastCheckedAt = &now
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
		persistErr := store.MarkLifecycleItem(persistCtx, operationID, *item)
		persistCancel()
		if persistErr != nil {
			item.Status, item.LastError = lifecycleItemFailed, "远程状态已复核，但生命周期进度保存失败"
			lastError = item.LastError
		}
	}
	for _, item := range result.Items {
		if item.Status != lifecycleItemCompleted {
			result.PendingCount++
		}
	}
	result.LastError = lastError
	if result.PendingCount > 0 {
		result.State = partial
	} else {
		result.State, _, _ = accessLifecycleStates(operation)
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := store.FinishAccessAttempt(persistCtx, username, operationID, operation, result.PendingCount, lastError); err != nil {
		result.PendingCount++
		result.State = partial
		result.LastError = "生命周期最终状态保存失败"
	}
	cancel()
	return result
}

func (a *app) executeAccessItem(ctx context.Context, token string, item *lifecyclePlanItem) error {
	currentConfig, err := a.fetchOfficialXrayConfig(ctx, token, item.ServerID)
	if err != nil {
		return err
	}
	current := findConfigInbound(currentConfig, item.InboundTag)
	if current == nil || hashJSON(current) != item.sourceInboundHash {
		return errors.New("Inbound 在生命周期计划后发生变化")
	}
	body := map[string]any{"action": "replace", "tag": item.InboundTag, "inbound": item.replacementInbound}
	path := "/api/admin/remote/inbounds?server_id=" + strconv.FormatInt(item.ServerID, 10)
	if err := a.officialLifecycleJSON(ctx, token, "POST", path, body, nil); err != nil {
		return err
	}
	verifiedConfig, err := a.fetchOfficialXrayConfig(ctx, token, item.ServerID)
	if err != nil {
		return err
	}
	verified := findConfigInbound(verifiedConfig, item.InboundTag)
	if verified == nil || hashJSON(verified) != hashJSON(item.replacementInbound) {
		return errors.New("HTTP 成功但真实 Agent 配置未变化")
	}
	entries, _, err := accessInboundCredentialEntries(verified)
	if err != nil || len(entries) == 0 {
		return errors.New("credential 替换产生了空 Inbound")
	}
	return nil
}
