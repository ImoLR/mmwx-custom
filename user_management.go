package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type managedUserState struct {
	Username string `json:"username"`
	Exists   bool   `json:"exists"`
	IsActive bool   `json:"is_active"`
	Role     string `json:"role,omitempty"`
}

type managedUserDeletionPreview struct {
	Username          string              `json:"username"`
	Exists            bool                `json:"exists"`
	Role              string              `json:"role,omitempty"`
	PackageBindings   int64               `json:"package_bindings"`
	Subscriptions     int64               `json:"subscriptions"`
	TelegramBindings  int64               `json:"telegram_bindings"`
	Subaccounts       int64               `json:"subaccounts"`
	InboundBindings   int64               `json:"inbound_bindings"`
	PrivateNodes      int64               `json:"private_nodes"`
	RoutedRelations   int64               `json:"routed_relations"`
	UserLimits        int64               `json:"user_limits"`
	TrafficRecords    int64               `json:"traffic_records"`
	SessionsAndTokens int64               `json:"sessions_and_tokens"`
	CustomAssignments int64               `json:"custom_assignments"`
	OtherPrivate      int64               `json:"other_private"`
	Details           map[string]int64    `json:"details"`
	SharedPreserved   []string            `json:"shared_preserved"`
	InboundPlan       []lifecyclePlanItem `json:"inbound_plan"`
}

type userManagementStore interface {
	ManagedUserState(context.Context, string) (managedUserState, error)
	ManagedUserDeletionPreview(context.Context, string) (managedUserDeletionPreview, error)
}

const userManagementCascadeSchema = `
CREATE OR REPLACE FUNCTION mmwxc_delete_management_user_relations()
RETURNS trigger
LANGUAGE plpgsql
AS $mmwxc$
DECLARE
    relation_spec text;
    relation_table text;
    relation_column text;
BEGIN
    -- Snapshot rows do not carry username, so remove them while the exact
    -- attributed email rows still exist.
    IF to_regclass(format('%I.%I', TG_TABLE_SCHEMA, 'user_email_traffic_snapshots')) IS NOT NULL
       AND to_regclass(format('%I.%I', TG_TABLE_SCHEMA, 'user_email_traffic')) IS NOT NULL THEN
        EXECUTE format('DELETE FROM %I.user_email_traffic_snapshots snapshots
                 WHERE EXISTS (
                    SELECT 1 FROM %I.user_email_traffic traffic
                    WHERE traffic.server_id = snapshots.server_id
                      AND traffic.email = snapshots.email
                      AND traffic.attributed_username = $1
                 )', TG_TABLE_SCHEMA, TG_TABLE_SCHEMA) USING OLD.username;
    END IF;

    -- Traffic carry rows are keyed by assignment id only, so remove them while
    -- the user's assignment rows still identify them.
    IF to_regclass(format('%I.%I', TG_TABLE_SCHEMA, 'package_assignment_traffic_carry')) IS NOT NULL
       AND to_regclass(format('%I.%I', TG_TABLE_SCHEMA, 'user_package_assignments')) IS NOT NULL THEN
        EXECUTE format('DELETE FROM %I.package_assignment_traffic_carry
                 WHERE assignment_id IN (
                    SELECT id FROM %I.user_package_assignments WHERE username = $1
                 )', TG_TABLE_SCHEMA, TG_TABLE_SCHEMA) USING OLD.username;
    END IF;

    -- A targeted, unused bind invite is private to the deleted account. Codes
    -- merely created by that account are retained as administrative records.
    IF to_regclass(format('%I.%I', TG_TABLE_SCHEMA, 'invite_codes')) IS NOT NULL THEN
        EXECUTE format('DELETE FROM %I.invite_codes WHERE bind_username = $1', TG_TABLE_SCHEMA) USING OLD.username;
    END IF;

    FOREACH relation_spec IN ARRAY ARRAY[
        'user_subscriptions:username',
        'user_merged_subscriptions:username',
        'sessions:username',
        'webauthn_credentials:username',
        'firewall_tokens:username',
        'firewall_whitelist:username',
        'user_api_tokens:username',
        'user_subaccounts:username',
        'user_inbound_configs:username',
        'package_assignment_inbound_configs:username',
        'package_assignment_subaccounts:username',
        'user_package_assignments:username',
        'user_outbounds:username',
        'user_routed_outbound_actions:username',
        'package_user_node_traffic_baselines:username',
        'package_node_traffic_suspensions:username',
        'proxy_provider_configs:username',
        'external_subscriptions:username',
        'routing_rule_presets:username',
        'override_scripts:username',
        'renewal_requests:username',
        'invite_code_uses:username',
        'user_settings:username',
        'user_traffic_records:username',
        'user_tokens:username',
        'user_traffic_cycle_carry:username',
        'user_traffic_snapshots:username',
        'traffic_daily_user_nodes:username',
        'traffic_daily_users:username',
        'traffic_daily_users_archived:username',
        'traffic_daily_user_emails:attributed_username',
        'user_email_traffic:attributed_username',
        'user_traffic:username',
        'user_speed_peaks:username',
        'user_conn_ip_history:username',
        'auto_limit_events:username',
        'mmwxc_connection_assignments:management_username',
		'mmwxc_package_traffic_group_blocks:username',
        'mmwxc_routing_rule_presets:username',
        'mmwxc_ui_preferences:username',
        'wg_leases:username',
        'nodes:username'
    ] LOOP
        relation_table := split_part(relation_spec, ':', 1);
        relation_column := split_part(relation_spec, ':', 2);
        IF to_regclass(format('%I.%I', TG_TABLE_SCHEMA, relation_table)) IS NOT NULL THEN
            EXECUTE format('DELETE FROM %I.%I WHERE %I = $1', TG_TABLE_SCHEMA, relation_table, relation_column)
                USING OLD.username;
        END IF;
    END LOOP;
    RETURN OLD;
END;
$mmwxc$;

DROP TRIGGER IF EXISTS mmwxc_delete_management_user_relations_trigger ON users;
CREATE TRIGGER mmwxc_delete_management_user_relations_trigger
BEFORE DELETE ON users
FOR EACH ROW EXECUTE FUNCTION mmwxc_delete_management_user_relations();
`

func (s *postgresAdminSessionStore) EnsureUserManagementSchema(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, userManagementCascadeSchema); err != nil {
		return errors.New("migrate user-management cascade: " + err.Error())
	}
	return nil
}

func (s *postgresAdminSessionStore) ManagedUserState(ctx context.Context, username string) (managedUserState, error) {
	username = strings.TrimSpace(username)
	state := managedUserState{Username: username}
	if username == "" {
		return state, errors.New("username is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var active bool
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(is_active, 0) <> 0, COALESCE(role, '') FROM users WHERE username = $1`, username).Scan(&active, &state.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.Exists = true
	state.IsActive = active
	return state, nil
}

type previewRelation struct {
	name     string
	table    string
	column   string
	category string
}

var managedUserPreviewRelations = []previewRelation{
	{"user_package_assignments", "user_package_assignments", "username", "package_bindings"},
	{"user_subscriptions", "user_subscriptions", "username", "subscriptions"},
	{"external_subscriptions", "external_subscriptions", "username", "subscriptions"},
	{"user_merged_subscriptions", "user_merged_subscriptions", "username", "subscriptions"},
	{"user_tokens", "user_tokens", "username", "sessions_and_tokens"},
	{"user_api_tokens", "user_api_tokens", "username", "sessions_and_tokens"},
	{"sessions", "sessions", "username", "sessions_and_tokens"},
	{"webauthn_credentials", "webauthn_credentials", "username", "sessions_and_tokens"},
	{"firewall_tokens", "firewall_tokens", "username", "sessions_and_tokens"},
	{"user_subaccounts", "user_subaccounts", "username", "subaccounts"},
	{"package_assignment_subaccounts", "package_assignment_subaccounts", "username", "subaccounts"},
	{"user_inbound_configs", "user_inbound_configs", "username", "inbound_bindings"},
	{"package_assignment_inbound_configs", "package_assignment_inbound_configs", "username", "inbound_bindings"},
	{"nodes", "nodes", "username", "private_nodes"},
	{"user_outbounds", "user_outbounds", "username", "routed_relations"},
	{"user_routed_outbound_actions", "user_routed_outbound_actions", "username", "routed_relations"},
	{"mmwxc_routing_rule_presets", "mmwxc_routing_rule_presets", "username", "routed_relations"},
	{"mmwxc_connection_assignments", "mmwxc_connection_assignments", "management_username", "custom_assignments"},
	{"mmwxc_package_traffic_group_blocks", "mmwxc_package_traffic_group_blocks", "username", "custom_assignments"},
	{"user_traffic_records", "user_traffic_records", "username", "traffic_records"},
	{"user_traffic", "user_traffic", "username", "traffic_records"},
	{"user_traffic_cycle_carry", "user_traffic_cycle_carry", "username", "traffic_records"},
	{"user_traffic_snapshots", "user_traffic_snapshots", "username", "traffic_records"},
	{"traffic_daily_users", "traffic_daily_users", "username", "traffic_records"},
	{"traffic_daily_users_archived", "traffic_daily_users_archived", "username", "traffic_records"},
	{"traffic_daily_user_nodes", "traffic_daily_user_nodes", "username", "traffic_records"},
	{"user_email_traffic", "user_email_traffic", "attributed_username", "traffic_records"},
	{"traffic_daily_user_emails", "traffic_daily_user_emails", "attributed_username", "traffic_records"},
	{"user_speed_peaks", "user_speed_peaks", "username", "traffic_records"},
	{"user_conn_ip_history", "user_conn_ip_history", "username", "traffic_records"},
	{"auto_limit_events", "auto_limit_events", "username", "traffic_records"},
	{"user_settings", "user_settings", "username", "other_private"},
	{"routing_rule_presets", "routing_rule_presets", "username", "other_private"},
	{"override_scripts", "override_scripts", "username", "other_private"},
	{"renewal_requests", "renewal_requests", "username", "other_private"},
	{"targeted_invite_codes", "invite_codes", "bind_username", "other_private"},
	{"invite_code_uses", "invite_code_uses", "username", "other_private"},
	{"package_user_node_traffic_baselines", "package_user_node_traffic_baselines", "username", "other_private"},
	{"package_node_traffic_suspensions", "package_node_traffic_suspensions", "username", "other_private"},
	{"proxy_provider_configs", "proxy_provider_configs", "username", "other_private"},
	{"mmwxc_ui_preferences", "mmwxc_ui_preferences", "username", "other_private"},
	{"wg_leases", "wg_leases", "username", "other_private"},
	{"firewall_whitelist", "firewall_whitelist", "username", "other_private"},
}

func (s *postgresAdminSessionStore) ManagedUserDeletionPreview(ctx context.Context, username string) (managedUserDeletionPreview, error) {
	username = strings.TrimSpace(username)
	preview := managedUserDeletionPreview{
		Username:        username,
		Details:         make(map[string]int64),
		SharedPreserved: []string{"remote_servers", "packages with other assignments", "shared nodes", "shared inbounds"},
	}
	if username == "" {
		return preview, errors.New("username is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var packageBound, telegramBound, limitsSet bool
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(role, ''), package_id IS NOT NULL,
		       COALESCE(telegram_id, 0) <> 0,
		       speed_limit_override IS NOT NULL OR device_limit_override IS NOT NULL OR
		       traffic_limit_override IS NOT NULL OR
		       COALESCE(node_speed_limit_overrides, '{}') NOT IN ('', '{}') OR
		       COALESCE(node_device_limit_overrides, '{}') NOT IN ('', '{}')
		FROM users WHERE username = $1`, username).Scan(&preview.Role, &packageBound, &telegramBound, &limitsSet)
	if errors.Is(err, sql.ErrNoRows) {
		return preview, nil
	}
	if err != nil {
		return preview, err
	}
	preview.Exists = true
	if packageBound {
		preview.PackageBindings = 1
	}
	if telegramBound {
		preview.TelegramBindings = 1
	}
	if limitsSet {
		preview.UserLimits = 1
	}

	rows, err := s.db.QueryContext(ctx, `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema()`)
	if err != nil {
		return preview, err
	}
	columns := make(map[string]struct{})
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			rows.Close()
			return preview, err
		}
		columns[table+"\x00"+column] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return preview, err
	}

	for _, relation := range managedUserPreviewRelations {
		if _, ok := columns[relation.table+"\x00"+relation.column]; !ok {
			continue
		}
		// table and column are compiled-in identifiers, never request data.
		var count int64
		query := `SELECT COUNT(*) FROM ` + relation.table + ` WHERE ` + relation.column + ` = $1`
		if err := s.db.QueryRowContext(ctx, query, username).Scan(&count); err != nil {
			return preview, err
		}
		preview.Details[relation.name] = count
		switch relation.category {
		case "package_bindings":
			preview.PackageBindings += count
		case "subscriptions":
			preview.Subscriptions += count
		case "sessions_and_tokens":
			preview.SessionsAndTokens += count
		case "subaccounts":
			preview.Subaccounts += count
		case "inbound_bindings":
			preview.InboundBindings += count
		case "private_nodes":
			preview.PrivateNodes += count
		case "routed_relations":
			preview.RoutedRelations += count
		case "custom_assignments":
			preview.CustomAssignments += count
		case "traffic_records":
			preview.TrafficRecords += count
		case "other_private":
			preview.OtherPrivate += count
		}
	}
	if _, snapshotsOK := columns["user_email_traffic_snapshots\x00email"]; snapshotsOK {
		if _, trafficOK := columns["user_email_traffic\x00attributed_username"]; trafficOK {
			var count int64
			err := s.db.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM user_email_traffic_snapshots snapshots
				WHERE EXISTS (
					SELECT 1 FROM user_email_traffic traffic
					WHERE traffic.server_id = snapshots.server_id
					  AND traffic.email = snapshots.email
					  AND traffic.attributed_username = $1
				)`, username).Scan(&count)
			if err != nil {
				return preview, err
			}
			preview.Details["user_email_traffic_snapshots"] = count
			preview.TrafficRecords += count
		}
	}
	return preview, nil
}

func (a *app) userManagementHandler(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	store, ok := a.adminStore.(userManagementStore)
	if !ok || store == nil {
		writeOperatorAuthorizationError(w, errOperatorAuthorizationUnavailable)
		return
	}
	const prefix = "/api/custom/users/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(path, "/")
	if len(parts) != 2 || parts[0] == "" || (parts[1] != "state" && parts[1] != "deletion-preview" && parts[1] != "delete" && parts[1] != "access") {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "not found"})
		return
	}
	username, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(username) == "" || len(username) > 256 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid username"})
		return
	}
	writeAction := parts[1] == "delete" || parts[1] == "access"
	if (writeAction && r.Method != http.MethodPost) || (!writeAction && r.Method != http.MethodGet) {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if parts[1] == "state" {
		state, err := store.ManagedUserState(r.Context(), username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to read user state"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "user": state})
		return
	}
	lifecycle, lifecycleOK := a.adminStore.(lifecycleStore)
	if !lifecycleOK || lifecycle == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "user lifecycle service unavailable"})
		return
	}
	unlock := func() {}
	if writeAction {
		unlock = a.lockUserLifecycle(username)
		defer unlock()
	}
	if parts[1] == "access" {
		state, err := store.ManagedUserState(r.Context(), username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to read user state"})
			return
		}
		if !state.Exists {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "user not found"})
			return
		}
		if state.Role == "admin" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "administrator access cannot be managed"})
			return
		}
		states, err := lifecycle.LifecycleStates(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取用户删除状态失败"})
			return
		}
		current := states[username]
		if current.DesiredState == lifecycleStateDeleted || current.EffectiveState == lifecycleStateDeleting || current.EffectiveState == lifecycleStateDeletePartial {
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": "用户正在删除或删除未完成，请先完成删除，不能更改访问状态"})
			return
		}
		var request struct {
			Enabled *bool `json:"enabled"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || request.Enabled == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid access request"})
			return
		}
		token := strings.TrimSpace(r.Header.Get("MM-Authorization"))
		if token == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "official operator session is required"})
			return
		}
		operation := lifecycleOperationDisable
		if *request.Enabled {
			operation = lifecycleOperationEnable
		}
		plan, err := a.buildAccessPlan(r.Context(), token, username, *request.Enabled)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to build user access plan"})
			return
		}
		operationID, err := lifecycle.LatestAccessOperation(r.Context(), username, operation)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to resume user access operation"})
			return
		}
		if operationID == "" {
			operationID, err = newManagedUserStatusTaskID()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to create user access operation"})
				return
			}
		}
		if err := lifecycle.SaveAccessPlan(r.Context(), username, operation, operationID, plan); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to persist user access plan"})
			return
		}
		result := a.executeAccessPlan(r.Context(), token, username, operationID, operation, plan)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": result})
		return
	}
	preview, err := store.ManagedUserDeletionPreview(r.Context(), username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to inspect user relationships"})
		return
	}
	if !preview.Exists {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "user not found"})
		return
	}
	if preview.Role == "admin" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "administrator accounts cannot be deleted"})
		return
	}
	token := strings.TrimSpace(r.Header.Get("MM-Authorization"))
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "official operator session is required"})
		return
	}
	plan, err := a.buildDeletionPlan(r.Context(), token, username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to build user deletion plan"})
		return
	}
	if parts[1] == "deletion-preview" {
		preview.InboundPlan = plan
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "preview": preview})
		return
	}
	operationID, err := lifecycle.LatestDeleteOperation(r.Context(), username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to resume user deletion"})
		return
	}
	if operationID == "" {
		operationID, err = newManagedUserStatusTaskID()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to create user deletion task"})
			return
		}
	}
	if err := lifecycle.SaveDeletePlan(r.Context(), username, operationID, plan); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to persist user deletion plan"})
		return
	}
	result := a.executeDeletePlan(r.Context(), token, username, operationID, plan)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": result})
	return
}
