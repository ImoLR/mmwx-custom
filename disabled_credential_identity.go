package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

func (s *postgresAdminSessionStore) serverDisabledCredentials(ctx context.Context, serverID int64) ([]lifecycleCredentialBackup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT username,server_id,inbound_tag,protocol,
		original_credential,disabled_credential,disabled_hash
		FROM mmwxc_user_disabled_credentials WHERE server_id=$1`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []lifecycleCredentialBackup
	for rows.Next() {
		var backup lifecycleCredentialBackup
		var original, disabled []byte
		if err := rows.Scan(&backup.Username, &backup.ServerID, &backup.InboundTag, &backup.Protocol,
			&original, &disabled, &backup.DisabledHash); err != nil {
			return nil, err
		}
		if json.Unmarshal(original, &backup.OriginalCredential) != nil || json.Unmarshal(disabled, &backup.DisabledCredential) != nil {
			return nil, errors.New("stored lifecycle credential is invalid")
		}
		result = append(result, backup)
	}
	return result, rows.Err()
}

func disabledIdentityOriginals(serverID int64, username, tag string, candidate trafficGroupConfiguredIdentity, backups []lifecycleCredentialBackup) []map[string]any {
	var result []map[string]any
	for _, backup := range backups {
		if username == "" || backup.Username != username || backup.ServerID != serverID || backup.InboundTag != tag || backup.Protocol != candidate.Protocol {
			continue
		}
		// Lifecycle replacement keeps email. Neither email alone nor a backup
		// for another user's credential establishes ownership of a current entry.
		disabledEmail, _ := backup.DisabledCredential["email"].(string)
		originalEmail, _ := backup.OriginalCredential["email"].(string)
		if candidate.Identity == "" || candidate.Identity != strings.TrimSpace(disabledEmail) || candidate.Identity != strings.TrimSpace(originalEmail) {
			continue
		}
		key := trafficGroupAuthenticationKey(candidate.Protocol)
		if key == "" || !nonEmptyCredentialValue(candidate.Credential, key) || !nonEmptyCredentialValue(backup.DisabledCredential, key) || !nonEmptyCredentialValue(backup.OriginalCredential, key) {
			continue
		}
		if hashJSON(candidate.Credential) == backup.DisabledHash || credentialsMatch(candidate.Credential, backup.DisabledCredential, candidate.Protocol) {
			result = append(result, backup.OriginalCredential)
		}
	}
	return result
}

func matchDisabledNodeProtocolIdentities(serverID int64, username, tag string, rawValues []string, configured []trafficGroupConfiguredIdentity, backups []lifecycleCredentialBackup) []string {
	var candidates []protocolCredential
	for _, candidate := range configured {
		for _, original := range disabledIdentityOriginals(serverID, username, tag, candidate, backups) {
			candidates = append(candidates, protocolCredential{Identity: candidate.Identity, Secrets: credentialSecrets(original), SS2022ServerKey: candidate.SS2022ServerKey})
		}
	}
	return matchNodeProtocolIdentities(rawValues, candidates)
}
