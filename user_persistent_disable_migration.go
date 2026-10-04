package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strconv"
)

// Old releases persisted swapped credentials and a disabled desired state.
// Keep those credentials until the Helper confirms the new block. No swap is
// ever repeated on an unsupported server by this background migration.
func (a *app) migratePersistentDisabledUsers(ctx context.Context, store *postgresAdminSessionStore) {
	states, err := store.LifecycleStates(ctx)
	if err != nil {
		return
	}
	var token string
	for username, state := range states {
		if state.DesiredState != lifecycleStateDisabled {
			continue
		}
		if token == "" {
			err = store.db.QueryRowContext(ctx, `SELECT s.token FROM sessions s JOIN users u ON u.username=s.username WHERE s.expires_at>CURRENT_TIMESTAMP AND u.role='admin' AND u.is_active=1 ORDER BY s.expires_at DESC LIMIT 1`).Scan(&token)
			if err != nil {
				return
			}
		}
		unlock := a.lockUserLifecycle(username)
		err = a.migratePersistentDisabledUser(ctx, store, token, username)
		unlock()
		if err != nil {
			log.Printf("[mmwx-custom] disabled user migration user=%s: %v", username, err)
		}
	}
}

func (a *app) migratePersistentDisabledUser(ctx context.Context, store *postgresAdminSessionStore, token, username string) error {
	states, err := store.LifecycleStates(ctx)
	if err != nil {
		return err
	}
	if states[username].DesiredState != lifecycleStateDisabled {
		return nil
	}
	if err := store.rememberAccessOfficialState(ctx, username); err != nil {
		return err
	}
	backups, err := store.LifecycleDisabledCredentials(ctx, username)
	if err != nil {
		return err
	}
	refs, err := a.accessCredentialRefs(ctx, username)
	if err != nil {
		return err
	}
	groups := map[string][]lifecycleCredentialBackup{}
	officialChangeSafe := true
	for _, backup := range backups {
		key := lifecycleInboundKey(backup.ServerID, backup.InboundTag)
		groups[key] = append(groups[key], backup)
	}
	for _, group := range groups {
		first := group[0]
		if !a.accessBlockSupported(ctx, first.ServerID) {
			continue
		}
		a.trafficGroupsMu.Lock()
		var identities []serverConnectionIdentity
		for _, identity := range a.disabledUserBlocks[username][strconv.FormatInt(first.ServerID, 10)] {
			if identity.InboundTag == first.InboundTag {
				identities = append(identities, identity)
			}
		}
		a.trafficGroupsMu.Unlock()
		if !a.accessIdentitiesConfirmed(first.ServerID, identities) {
			continue
		}
		config, err := a.fetchOfficialXrayConfig(ctx, token, first.ServerID)
		if err != nil {
			return err
		}
		var groupRefs []lifecycleCredentialRef
		for _, ref := range refs {
			if ref.ServerID == first.ServerID && ref.InboundTag == first.InboundTag {
				groupRefs = append(groupRefs, ref)
			}
		}
		item, handled, err := a.planPersistentAccess(ctx, username, false, config, groupRefs, group)
		if err != nil {
			return err
		}
		if item.Status == lifecycleItemFailed || item.Action == lifecycleActionConflict {
			officialChangeSafe = false
		}
		if handled && item.persistentRestore != nil {
			restore := item.persistentRestore
			if err := a.executePersistentAccessItem(ctx, token, username, false, restore); err != nil {
				return err
			}
			continue
		}
		if handled && item.Action == lifecycleActionBlockIdentity && item.Status != lifecycleItemFailed && item.accessEnable {
			if err := a.executePersistentAccessItem(ctx, token, username, false, &item); err != nil {
				return err
			}
			continue
		}
		// Inactive official accounts may have no runtime client at all. Retire
		// only backups whose exact original remains in the official records;
		// enabling through the official API will recreate that original.
		current, err := store.ManagedUserState(ctx, username)
		if err != nil {
			return err
		}
		if current.IsActive {
			continue
		}
		entries, _, _ := accessInboundCredentialEntries(findConfigInbound(config, first.InboundTag))
		for _, backup := range group {
			present := false
			for _, entry := range entries {
				present = present || credentialsMatch(entry, backup.OriginalCredential, backup.Protocol) || credentialsMatch(entry, backup.DisabledCredential, backup.Protocol) || nonEmptyCredentialValue(backup.OriginalCredential, "email") && entry["email"] == backup.OriginalCredential["email"]
			}
			if present {
				continue
			}
			originalRecorded := false
			for _, ref := range groupRefs {
				var credential map[string]any
				if json.Unmarshal([]byte(ref.CredentialRaw), &credential) == nil && hashJSON(credential) == backup.OriginalHash {
					originalRecorded = true
				}
			}
			if !originalRecorded {
				continue
			}
			_, err = store.db.ExecContext(ctx, `DELETE FROM mmwxc_user_disabled_credentials WHERE username=$1 AND server_id=$2 AND inbound_tag=$3 AND credential_key=$4`, username, first.ServerID, first.InboundTag, backup.CredentialKey)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
	}
	// The official inactive operation removes clients too. Never use it to
	// bypass a shared-credential refusal made by the Custom planner.
	a.trafficGroupsMu.Lock()
	unsafe := !officialChangeSafe || !a.disabledUsersReady || states[username].PendingCount > 0
	for _, item := range a.disabledUserAccess[username] {
		unsafe = unsafe || item.Status == "conflict"
	}
	a.trafficGroupsMu.Unlock()
	if unsafe {
		return nil
	}
	return a.applyAccessOfficialState(ctx, token, username, false)
}
