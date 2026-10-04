package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	lifecycleStateEnabled       = "enabled"
	lifecycleStateDeleting      = "deleting"
	lifecycleStateDeletePartial = "delete_partial"
	lifecycleStateDeleted       = "deleted"

	lifecycleActionRemoveUser    = "REMOVE_USER_ONLY"
	lifecycleActionDeleteWhole   = "DELETE_WHOLE_INBOUND"
	lifecycleActionConflict      = "CONFLICT"
	lifecycleActionDeletePackage = "DELETE_PACKAGE"
	lifecycleActionKeepPackage   = "KEEP_PACKAGE"

	lifecycleItemKindInbound = "inbound"
	lifecycleItemKindPackage = "package"

	lifecycleItemPending   = "pending"
	lifecycleItemCompleted = "completed"
	lifecycleItemFailed    = "failed"

	maxOfficialLifecycleResponse = 16 << 20
)

const userLifecycleSchema = `
CREATE TABLE IF NOT EXISTS mmwxc_user_lifecycle (
    username TEXT PRIMARY KEY,
    desired_state TEXT NOT NULL DEFAULT 'enabled',
    effective_state TEXT NOT NULL DEFAULT 'enabled',
    operation TEXT NOT NULL DEFAULT '',
    pending_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE,
    CHECK (desired_state IN ('enabled','disabled','deleted')),
    CHECK (effective_state IN ('enabled','disabled','disabling','enabling','partially_disabled','partially_enabled','deleting','delete_partial','error','conflict','deleted'))
);
ALTER TABLE mmwxc_user_lifecycle DROP CONSTRAINT IF EXISTS mmwxc_user_lifecycle_effective_state_check;
ALTER TABLE mmwxc_user_lifecycle ADD CONSTRAINT mmwxc_user_lifecycle_effective_state_check
    CHECK (effective_state IN ('enabled','disabled','disabling','enabling','partially_disabled','partially_enabled','deleting','delete_partial','error','conflict','deleted'));
CREATE TABLE IF NOT EXISTS mmwxc_user_lifecycle_operations (
    operation_id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    operation TEXT NOT NULL,
    state TEXT NOT NULL,
    pending_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_mmwxc_lifecycle_operations_user
    ON mmwxc_user_lifecycle_operations(username, updated_at DESC);
CREATE TABLE IF NOT EXISTS mmwxc_user_lifecycle_items (
    operation_id TEXT NOT NULL,
    server_id BIGINT NOT NULL,
    server_name TEXT NOT NULL DEFAULT '',
    inbound_tag TEXT NOT NULL,
    protocol TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    remaining_users INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    last_checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (operation_id, server_id, inbound_tag),
    FOREIGN KEY (operation_id) REFERENCES mmwxc_user_lifecycle_operations(operation_id) ON DELETE CASCADE
);
ALTER TABLE mmwxc_user_lifecycle_items ADD COLUMN IF NOT EXISTS item_kind TEXT NOT NULL DEFAULT 'inbound';
ALTER TABLE mmwxc_user_lifecycle_items ADD COLUMN IF NOT EXISTS package_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE mmwxc_user_lifecycle_items ADD COLUMN IF NOT EXISTS package_name TEXT NOT NULL DEFAULT '';
ALTER TABLE mmwxc_user_lifecycle_items ADD COLUMN IF NOT EXISTS node_ids JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE mmwxc_user_lifecycle_items ADD COLUMN IF NOT EXISTS default_credentials INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mmwxc_user_lifecycle_items ADD COLUMN IF NOT EXISTS unknown_credentials INTEGER NOT NULL DEFAULT 0;
ALTER TABLE mmwxc_user_lifecycle_items ADD COLUMN IF NOT EXISTS decision_note TEXT NOT NULL DEFAULT '';
ALTER TABLE mmwxc_user_lifecycle_items ADD COLUMN IF NOT EXISTS deletion_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;
CREATE TABLE IF NOT EXISTS mmwxc_user_disabled_credentials (
    username TEXT NOT NULL,
    server_id BIGINT NOT NULL,
    server_name TEXT NOT NULL DEFAULT '',
    inbound_tag TEXT NOT NULL,
    protocol TEXT NOT NULL,
    credential_key TEXT NOT NULL,
    original_credential JSONB NOT NULL,
    disabled_credential JSONB NOT NULL,
    original_hash TEXT NOT NULL,
    disabled_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (username, server_id, inbound_tag, credential_key),
    FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE
);
`

type managedUserLifecycle struct {
	Username       string    `json:"username"`
	DesiredState   string    `json:"desired_state"`
	EffectiveState string    `json:"effective_state"`
	Operation      string    `json:"operation,omitempty"`
	PendingCount   int       `json:"pending_count"`
	LastError      string    `json:"last_error,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type lifecycleCredentialRef struct {
	Username      string
	ServerID      int64
	ServerName    string
	InboundTag    string
	Protocol      string
	CredentialRaw string
	Identity      string
	Source        string
}

type lifecyclePlanItem struct {
	ItemKind           string               `json:"item_kind"`
	ServerID           int64                `json:"server_id"`
	ServerName         string               `json:"server_name"`
	InboundTag         string               `json:"inbound_tag"`
	Protocol           string               `json:"protocol"`
	Action             string               `json:"action"`
	Status             string               `json:"status"`
	RemainingUsers     int                  `json:"remaining_users"`
	DefaultCredentials int                  `json:"default_credentials"`
	UnknownCredentials int                  `json:"unknown_credentials"`
	DecisionNote       string               `json:"decision_note,omitempty"`
	PackageID          int64                `json:"package_id,omitempty"`
	PackageName        string               `json:"package_name,omitempty"`
	NodeIDs            []int64              `json:"node_ids,omitempty"`
	DeletedNodeIDs     []int64              `json:"deleted_node_ids,omitempty"`
	OwnNodes           []lifecycleNodeLabel `json:"own_nodes,omitempty"`
	OtherUserNodes     []lifecycleNodeLabel `json:"other_user_nodes,omitempty"`
	NeutralNodes       []lifecycleNodeLabel `json:"neutral_nodes,omitempty"`
	UnknownNodes       []lifecycleNodeLabel `json:"unknown_nodes,omitempty"`
	Attempts           int                  `json:"attempts"`
	LastError          string               `json:"last_error,omitempty"`
	LastCheckedAt      *time.Time           `json:"last_checked_at,omitempty"`

	deleteRefs              []lifecycleCredentialRef
	targetCredentials       []map[string]any
	nonTargetHashes         []string
	defaultCredentialHashes []string
	sourceInboundHash       string
	replacementInbound      map[string]any
	accessCredentials       []lifecycleCredentialBackup
}

type lifecyclePackageBinding struct {
	ID              int64
	Name            string
	NodeIDs         []int64
	Bound           bool
	BindingConflict bool
}

type lifecycleCredentialBackup struct {
	Username           string
	ServerID           int64
	ServerName         string
	InboundTag         string
	Protocol           string
	CredentialKey      string
	OriginalCredential map[string]any
	DisabledCredential map[string]any
	OriginalHash       string
	DisabledHash       string
}

type lifecycleDeleteResult struct {
	Username     string              `json:"username"`
	OperationID  string              `json:"operation_id"`
	State        string              `json:"state"`
	PendingCount int                 `json:"pending_count"`
	UserDeleted  bool                `json:"user_deleted"`
	LastError    string              `json:"last_error,omitempty"`
	Items        []lifecyclePlanItem `json:"items"`
}

type lifecycleStore interface {
	LifecycleStates(context.Context) (map[string]managedUserLifecycle, error)
	LifecycleCredentialRefs(context.Context, string) ([]lifecycleCredentialRef, error)
	LifecycleInboundBusinessRefs(context.Context, int64, string, string, string) ([]lifecycleCredentialRef, error)
	LifecycleDefaultAdminCredentials(context.Context, int64, string) ([]map[string]any, error)
	LifecyclePackageBindings(context.Context, string) ([]lifecyclePackageBinding, error)
	DeleteExclusivePackage(context.Context, int64, string, lifecyclePackageRecheck) error
	LifecycleDeletionData(context.Context, string) (lifecycleDeletionData, error)
	LifecycleDisabledCredentials(context.Context, string) ([]lifecycleCredentialBackup, error)
	LatestAccessOperation(context.Context, string, string) (string, error)
	SaveAccessPlan(context.Context, string, string, string, []lifecyclePlanItem) error
	FinishAccessAttempt(context.Context, string, string, string, int, string) error
	LatestDeleteOperation(context.Context, string) (string, error)
	SaveDeletePlan(context.Context, string, string, []lifecyclePlanItem) error
	MarkLifecycleItem(context.Context, string, lifecyclePlanItem) error
	FinishDeleteAttempt(context.Context, string, string, int, string) error
	FinalizeManagementUserDeletion(context.Context, string, string) error
}

func (a *app) lockUserLifecycle(username string) func() {
	a.userLifecycleMu.Lock()
	if a.userLifecycleLocks == nil {
		a.userLifecycleLocks = make(map[string]*sync.Mutex)
	}
	lock := a.userLifecycleLocks[username]
	if lock == nil {
		lock = &sync.Mutex{}
		a.userLifecycleLocks[username] = lock
	}
	a.userLifecycleMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *postgresAdminSessionStore) EnsureUserLifecycleSchema(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, userLifecycleSchema); err != nil {
		return fmt.Errorf("migrate user lifecycle: %w", err)
	}
	return nil
}

func (s *postgresAdminSessionStore) LifecycleStates(ctx context.Context) (map[string]managedUserLifecycle, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT username, desired_state, effective_state, operation, pending_count, last_error, updated_at
		FROM mmwxc_user_lifecycle`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]managedUserLifecycle)
	for rows.Next() {
		var item managedUserLifecycle
		if err := rows.Scan(&item.Username, &item.DesiredState, &item.EffectiveState, &item.Operation, &item.PendingCount, &item.LastError, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result[item.Username] = item
	}
	return result, rows.Err()
}

type lifecycleRelationSpec struct {
	table      string
	query      string
	serverName bool
}

var lifecycleRelationTables = []string{
	"user_inbound_configs",
	"package_assignment_inbound_configs",
	"user_subaccounts",
	"package_assignment_subaccounts",
	"user_package_assignments",
	"user_outbounds",
	"mmwxc_connection_assignments",
}

func (s *postgresAdminSessionStore) LifecycleCredentialRefs(ctx context.Context, username string) ([]lifecycleCredentialRef, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	columns, err := s.schemaColumns(ctx)
	if err != nil {
		return nil, err
	}
	var result []lifecycleCredentialRef
	queries := lifecycleCredentialQueries(columns)
	for _, spec := range queries {
		rows, err := s.db.QueryContext(ctx, spec.query, username)
		if err != nil {
			return nil, fmt.Errorf("read %s lifecycle relations: %w", spec.table, err)
		}
		for rows.Next() {
			var ref lifecycleCredentialRef
			if err := rows.Scan(&ref.ServerID, &ref.ServerName, &ref.InboundTag, &ref.Protocol, &ref.CredentialRaw, &ref.Identity, &ref.Source); err != nil {
				rows.Close()
				return nil, err
			}
			ref.Username = username
			ref.InboundTag = strings.TrimSpace(ref.InboundTag)
			ref.Identity = strings.TrimSpace(ref.Identity)
			if ref.ServerID > 0 && ref.InboundTag != "" {
				result = append(result, ref)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	packageRefs, err := s.lifecyclePackageNodeRefs(ctx, columns, username)
	if err != nil {
		return nil, err
	}
	result = append(result, packageRefs...)
	return dedupeLifecycleRefs(result), nil
}

func lifecycleCredentialQueries(columns map[string]map[string]bool) []lifecycleRelationSpec {
	var result []lifecycleRelationSpec
	for _, table := range []string{"user_inbound_configs", "package_assignment_inbound_configs"} {
		if !hasLifecycleColumns(columns, table, "username", "server_id", "inbound_tag") {
			continue
		}
		protocol := "''"
		credential := "''"
		if columns[table]["protocol"] {
			protocol = "COALESCE(c.protocol,'')"
		}
		if columns[table]["credential_json"] {
			credential = "COALESCE(c.credential_json,'')"
		}
		result = append(result, lifecycleRelationSpec{table: table, query: fmt.Sprintf(
			`SELECT c.server_id, COALESCE(s.name,''), c.inbound_tag, %s, %s, '', '%s' FROM %s c LEFT JOIN remote_servers s ON s.id=c.server_id WHERE c.username=$1`,
			protocol, credential, table, table)})
	}
	for _, table := range []string{"user_subaccounts", "package_assignment_subaccounts"} {
		if !hasLifecycleColumns(columns, table, "username", "routed_node_id") ||
			!hasLifecycleColumns(columns, "nodes", "id", "inbound_tag", "original_server") {
			continue
		}
		protocol := "''"
		credential := "''"
		identity := "''"
		if columns["nodes"]["protocol"] {
			protocol = "COALESCE(n.protocol,'')"
		}
		if columns[table]["credential_json"] {
			credential = "COALESCE(a.credential_json,'')"
		}
		if columns[table]["email"] {
			identity = "COALESCE(a.email,'')"
		}
		result = append(result, lifecycleRelationSpec{table: table, query: fmt.Sprintf(
			`SELECT s.id, COALESCE(s.name,''), n.inbound_tag, %s, %s, %s, '%s' FROM %s a JOIN nodes n ON n.id=a.routed_node_id JOIN remote_servers s ON s.name=n.original_server WHERE a.username=$1 AND COALESCE(n.inbound_tag,'')<>''`,
			protocol, credential, identity, table, table)})
	}
	if hasLifecycleColumns(columns, "user_outbounds", "username", "server_id", "inbound_tag") {
		result = append(result, lifecycleRelationSpec{table: "user_outbounds", query: `SELECT o.server_id, COALESCE(s.name,''), o.inbound_tag, '', '', '', 'user_outbounds' FROM user_outbounds o LEFT JOIN remote_servers s ON s.id=o.server_id WHERE o.username=$1`})
	}
	if hasLifecycleColumns(columns, "mmwxc_connection_assignments", "management_username", "server_id", "inbound_tag", "protocol_identity") {
		result = append(result, lifecycleRelationSpec{table: "mmwxc_connection_assignments", query: `SELECT a.server_id, COALESCE(s.name,''), a.inbound_tag, '', '', a.protocol_identity, 'mmwxc_connection_assignments' FROM mmwxc_connection_assignments a LEFT JOIN remote_servers s ON s.id=a.server_id WHERE a.management_username=$1`})
	}
	return result
}

func (s *postgresAdminSessionStore) lifecyclePackageNodeRefs(ctx context.Context, columns map[string]map[string]bool, username string) ([]lifecycleCredentialRef, error) {
	if !hasLifecycleColumns(columns, "packages", "id", "nodes") ||
		!hasLifecycleColumns(columns, "nodes", "id", "original_server", "inbound_tag") ||
		!hasLifecycleColumns(columns, "remote_servers", "id", "name") {
		return nil, nil
	}
	packages, err := s.LifecyclePackageBindings(ctx, username)
	if err != nil {
		return nil, err
	}
	var identity string
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(email,'') FROM users WHERE username=$1`, username).Scan(&identity)
	var result []lifecycleCredentialRef
	for _, pkg := range packages {
		for _, nodeID := range pkg.NodeIDs {
			protocol := "''"
			if columns["nodes"]["protocol"] {
				protocol = "COALESCE(n.protocol,'')"
			}
			var ref lifecycleCredentialRef
			err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT s.id,COALESCE(s.name,''),n.inbound_tag,%s FROM nodes n JOIN remote_servers s ON s.name=n.original_server WHERE n.id=$1 AND COALESCE(n.inbound_tag,'')<>''`, protocol), nodeID).
				Scan(&ref.ServerID, &ref.ServerName, &ref.InboundTag, &ref.Protocol)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			ref.Username = username
			ref.Identity = strings.TrimSpace(identity)
			ref.Source = "user_package_assignments"
			result = append(result, ref)
		}
	}
	return result, nil
}

func hasLifecycleColumns(columns map[string]map[string]bool, table string, required ...string) bool {
	available := columns[table]
	if available == nil {
		return false
	}
	for _, column := range required {
		if !available[column] {
			return false
		}
	}
	return true
}

func (s *postgresAdminSessionStore) LifecycleInboundBusinessRefs(ctx context.Context, serverID int64, serverName, inboundTag, excludeUsername string) ([]lifecycleCredentialRef, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	columns, err := s.schemaColumns(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(serverName) == "" {
		_ = s.db.QueryRowContext(ctx, `SELECT name FROM remote_servers WHERE id=$1`, serverID).Scan(&serverName)
	}
	var result []lifecycleCredentialRef
	for _, table := range []string{"user_inbound_configs", "package_assignment_inbound_configs"} {
		if !hasLifecycleColumns(columns, table, "username", "server_id", "inbound_tag") {
			continue
		}
		protocol, credential, identity := "''", "''", "''"
		if columns[table]["protocol"] {
			protocol = "COALESCE(protocol,'')"
		}
		if columns[table]["credential_json"] {
			credential = "COALESCE(credential_json,'')"
		}
		if columns[table]["email"] {
			identity = "COALESCE(email,'')"
		}
		rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT username,%s,%s,%s FROM %s WHERE server_id=$1 AND inbound_tag=$2 AND username<>$3`, protocol, credential, identity, table), serverID, inboundTag, excludeUsername)
		if err != nil {
			return nil, fmt.Errorf("read %s lifecycle consumers: %w", table, err)
		}
		for rows.Next() {
			var ref lifecycleCredentialRef
			if err := rows.Scan(&ref.Username, &ref.Protocol, &ref.CredentialRaw, &ref.Identity); err != nil {
				rows.Close()
				return nil, err
			}
			ref.ServerID, ref.ServerName, ref.InboundTag, ref.Source = serverID, serverName, inboundTag, table
			result = append(result, ref)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	for _, table := range []string{"user_subaccounts", "package_assignment_subaccounts"} {
		if hasLifecycleColumns(columns, table, "username", "routed_node_id") && hasLifecycleColumns(columns, "nodes", "id", "original_server", "inbound_tag") {
			credential, identity := "''", "''"
			if columns[table]["credential_json"] {
				credential = "COALESCE(a.credential_json,'')"
			}
			if columns[table]["email"] {
				identity = "COALESCE(a.email,'')"
			}
			rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT a.username,%s,%s FROM %s a JOIN nodes n ON n.id=a.routed_node_id WHERE n.original_server=$1 AND n.inbound_tag=$2 AND a.username<>$3`, credential, identity, table), serverName, inboundTag, excludeUsername)
			if err != nil {
				return nil, fmt.Errorf("read %s lifecycle consumers: %w", table, err)
			}
			for rows.Next() {
				var ref lifecycleCredentialRef
				if err := rows.Scan(&ref.Username, &ref.CredentialRaw, &ref.Identity); err != nil {
					rows.Close()
					return nil, err
				}
				ref.ServerID, ref.ServerName, ref.InboundTag, ref.Source = serverID, serverName, inboundTag, table
				result = append(result, ref)
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
	}
	for _, table := range []string{"user_outbounds"} {
		if hasLifecycleColumns(columns, table, "username", "server_id", "inbound_tag") {
			rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT username FROM %s WHERE server_id=$1 AND inbound_tag=$2 AND username<>$3`, table), serverID, inboundTag, excludeUsername)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var ref lifecycleCredentialRef
				if err := rows.Scan(&ref.Username); err != nil {
					rows.Close()
					return nil, err
				}
				ref.ServerID, ref.ServerName, ref.InboundTag, ref.Source = serverID, serverName, inboundTag, table
				result = append(result, ref)
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
		}
	}
	if hasLifecycleColumns(columns, "mmwxc_connection_assignments", "management_username", "server_id", "inbound_tag") {
		identity := "''"
		if columns["mmwxc_connection_assignments"]["protocol_identity"] {
			identity = "COALESCE(protocol_identity,'')"
		}
		rows, err := s.db.QueryContext(ctx, `SELECT management_username,`+identity+` FROM mmwxc_connection_assignments WHERE server_id=$1 AND inbound_tag=$2 AND management_username<>$3`, serverID, inboundTag, excludeUsername)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ref lifecycleCredentialRef
			if err := rows.Scan(&ref.Username, &ref.Identity); err != nil {
				rows.Close()
				return nil, err
			}
			ref.ServerID, ref.ServerName, ref.InboundTag, ref.Source = serverID, serverName, inboundTag, "mmwxc_connection_assignments"
			result = append(result, ref)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	packageRefs, err := s.lifecyclePackageBusinessRefs(ctx, columns, serverID, serverName, inboundTag, excludeUsername)
	if err != nil {
		return nil, err
	}
	result = append(result, packageRefs...)
	return dedupeLifecycleRefs(result), nil
}

func (s *postgresAdminSessionStore) schemaColumns(ctx context.Context) (map[string]map[string]bool, error) {
	return lifecycleSchemaColumns(ctx, s.db)
}

func lifecycleSchemaColumns(ctx context.Context, db lifecycleQueryer) (map[string]map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT table_name,column_name FROM information_schema.columns WHERE table_schema=current_schema()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]map[string]bool)
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return nil, err
		}
		if result[table] == nil {
			result[table] = make(map[string]bool)
		}
		result[table][column] = true
	}
	return result, rows.Err()
}

func (s *postgresAdminSessionStore) LifecyclePackageBindings(ctx context.Context, username string) ([]lifecyclePackageBinding, error) {
	packages, err := lifecyclePackageBindings(ctx, s.db, username)
	if err != nil {
		return nil, err
	}
	var result []lifecyclePackageBinding
	for _, pkg := range packages {
		if pkg.Bound {
			result = append(result, pkg)
		}
	}
	return result, nil
}

func (s *postgresAdminSessionStore) lifecyclePackageBusinessRefs(ctx context.Context, columns map[string]map[string]bool, serverID int64, serverName, inboundTag, excludeUsername string) ([]lifecycleCredentialRef, error) {
	if !hasLifecycleColumns(columns, "packages", "id", "nodes") ||
		!hasLifecycleColumns(columns, "nodes", "id", "original_server", "inbound_tag") ||
		!hasLifecycleColumns(columns, "user_package_assignments", "username", "package_id") {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT package_users.username,COALESCE(u.email,'')
		FROM (
			SELECT a.username,a.package_id FROM user_package_assignments a
			WHERE a.username<>$3 AND COALESCE(a.status,'active')='active'
			UNION
			SELECT direct_user.username,direct_user.package_id FROM users direct_user
			WHERE direct_user.username<>$3 AND direct_user.package_id IS NOT NULL
		) package_users
		JOIN packages p ON p.id=package_users.package_id
		JOIN LATERAL jsonb_array_elements_text(
			CASE WHEN COALESCE(p.nodes,'') ~ '^\s*\[' THEN p.nodes::jsonb ELSE '[]'::jsonb END
		) package_node(node_id) ON true
		JOIN nodes n ON n.id=package_node.node_id::bigint
		LEFT JOIN users u ON u.username=package_users.username
		WHERE n.original_server=$1 AND n.inbound_tag=$2`, serverName, inboundTag, excludeUsername)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []lifecycleCredentialRef
	for rows.Next() {
		var ref lifecycleCredentialRef
		if err := rows.Scan(&ref.Username, &ref.Identity); err != nil {
			return nil, err
		}
		ref.ServerID, ref.ServerName, ref.InboundTag, ref.Source = serverID, serverName, inboundTag, "user_package_assignments"
		result = append(result, ref)
	}
	return result, rows.Err()
}

func (s *postgresAdminSessionStore) LifecycleDefaultAdminCredentials(ctx context.Context, serverID int64, inboundTag string) ([]map[string]any, error) {
	return lifecycleDefaultAdminCredentials(ctx, s.db, serverID, inboundTag)
}

func lifecycleDefaultAdminCredentials(ctx context.Context, db lifecycleQueryer, serverID int64, inboundTag string) ([]map[string]any, error) {
	columns, err := lifecycleSchemaColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	if !hasLifecycleColumns(columns, "server_xray_config_snapshots", "id", "server_id", "config_json", "source", "created_at") {
		return nil, nil
	}
	admins := make(map[string]bool)
	rows, err := db.QueryContext(ctx, `SELECT username,COALESCE(email,'') FROM users WHERE role='admin'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var username, email string
		if err := rows.Scan(&username, &email); err != nil {
			rows.Close()
			return nil, err
		}
		admins[strings.ToLower(strings.TrimSpace(username))] = true
		if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
			admins[email] = true
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(admins) == 0 {
		return nil, nil
	}
	rows, err = db.QueryContext(ctx, `
		SELECT config_json::text,COALESCE(source,'')
		FROM server_xray_config_snapshots
		WHERE server_id=$1 AND config_json::text LIKE $2
		ORDER BY created_at,id
		LIMIT 200`, serverID, "%"+inboundTag+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw, source string
		if err := rows.Scan(&raw, &source); err != nil {
			return nil, err
		}
		var config map[string]any
		if json.Unmarshal([]byte(raw), &config) != nil {
			continue
		}
		inbound := findConfigInbound(config, inboundTag)
		if inbound == nil {
			continue
		}
		if source != "master_write" {
			return nil, nil
		}
		entries, _, err := accessInboundCredentialEntries(inbound)
		if err != nil {
			return nil, nil
		}
		var result []map[string]any
		for _, entry := range entries {
			if credentialMatchesAdmin(entry, admins) {
				result = append(result, entry)
			}
		}
		return result, nil
	}
	return nil, rows.Err()
}

func credentialMatchesAdmin(credential map[string]any, admins map[string]bool) bool {
	for _, key := range []string{"email", "username", "user", "name"} {
		identity := strings.ToLower(strings.TrimSpace(fmt.Sprint(credential[key])))
		if identity == "" {
			continue
		}
		if admins[identity] {
			return true
		}
		for admin := range admins {
			if !strings.Contains(admin, "@") && (strings.HasPrefix(identity, admin+"__") || identity == "mmw@"+admin+".me") {
				return true
			}
		}
	}
	return false
}

func (s *postgresAdminSessionStore) DeleteExclusivePackage(ctx context.Context, packageID int64, username string, recheck lifecyclePackageRecheck) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rawNodes string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(nodes,'[]') FROM packages WHERE id=$1 FOR UPDATE`, packageID).Scan(&rawNodes); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var nodes []int64
	if json.Unmarshal([]byte(rawNodes), &nodes) != nil {
		return errors.New("套餐节点列表无效，未删除套餐")
	}
	var shared bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_package_assignments WHERE package_id=$1 AND username<>$2) OR EXISTS(SELECT 1 FROM users WHERE package_id=$1 AND username<>$2)`, packageID, username).Scan(&shared); err != nil {
		return err
	}
	if shared {
		return errors.New("套餐同时绑定其他用户，存在冲突，未删除套餐")
	}
	if recheck == nil {
		return errors.New("缺少节点归属复核，未删除套餐")
	}
	if err := recheck(ctx, tx, nodes); err != nil {
		return err
	}
	columns, err := lifecycleSchemaColumns(ctx, tx)
	if err != nil {
		return err
	}
	if hasLifecycleColumns(columns, "forward_chain_nodes", "billing_assignment_id") {
		if _, err := tx.ExecContext(ctx, `DELETE FROM forward_chain_nodes WHERE billing_assignment_id IN (SELECT id FROM user_package_assignments WHERE package_id=$1)`, packageID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM packages WHERE id=$1`, packageID); err != nil {
		return err
	}
	return tx.Commit()
}

func dedupeLifecycleRefs(refs []lifecycleCredentialRef) []lifecycleCredentialRef {
	seen := make(map[string]bool)
	result := make([]lifecycleCredentialRef, 0, len(refs))
	for _, ref := range refs {
		key := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", ref.ServerID, ref.InboundTag, ref.Username, ref.CredentialRaw, ref.Identity)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, ref)
	}
	return result
}

func (s *postgresAdminSessionStore) LatestDeleteOperation(ctx context.Context, username string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var operationID string
	err := s.db.QueryRowContext(ctx, `SELECT operation_id FROM mmwxc_user_lifecycle_operations WHERE username=$1 AND operation='delete' AND state IN ('deleting','delete_partial') ORDER BY updated_at DESC LIMIT 1`, username).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return operationID, err
}

func (s *postgresAdminSessionStore) SaveDeletePlan(ctx context.Context, username, operationID string, items []lifecyclePlanItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO mmwxc_user_lifecycle(username,desired_state,effective_state,operation,pending_count,last_error)
		VALUES($1,'deleted','deleting','delete',0,'')
		ON CONFLICT(username) DO UPDATE SET desired_state='deleted',effective_state='deleting',operation='delete',last_error='',updated_at=CURRENT_TIMESTAMP`, username)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO mmwxc_user_lifecycle_operations(operation_id,username,operation,state,pending_count,last_error)
		VALUES($1,$2,'delete','deleting',0,'')
		ON CONFLICT(operation_id) DO UPDATE SET state='deleting',last_error='',updated_at=CURRENT_TIMESTAMP`, operationID, username)
	if err != nil {
		return err
	}
	pending := 0
	for _, item := range items {
		if item.Status != lifecycleItemCompleted {
			pending++
		}
		snapshot, marshalErr := json.Marshal(lifecycleDeletionSnapshot{Item: item, Refs: item.deleteRefs, Targets: item.targetCredentials, NonTargetHashes: item.nonTargetHashes, DefaultHashes: item.defaultCredentialHashes})
		if marshalErr != nil {
			return marshalErr
		}
		nodeIDs, marshalErr := json.Marshal(item.NodeIDs)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO mmwxc_user_lifecycle_items(operation_id,server_id,server_name,inbound_tag,protocol,action,status,remaining_users,last_error,last_checked_at,item_kind,package_id,package_name,node_ids,default_credentials,unknown_credentials,decision_note,deletion_snapshot)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT(operation_id,server_id,inbound_tag) DO UPDATE SET
			server_name=EXCLUDED.server_name,protocol=EXCLUDED.protocol,action=EXCLUDED.action,status=EXCLUDED.status,
			remaining_users=EXCLUDED.remaining_users,last_error=EXCLUDED.last_error,
			last_checked_at=COALESCE(EXCLUDED.last_checked_at,mmwxc_user_lifecycle_items.last_checked_at),item_kind=EXCLUDED.item_kind,
			package_id=EXCLUDED.package_id,package_name=EXCLUDED.package_name,node_ids=EXCLUDED.node_ids,
			default_credentials=EXCLUDED.default_credentials,unknown_credentials=EXCLUDED.unknown_credentials,
			decision_note=EXCLUDED.decision_note,deletion_snapshot=EXCLUDED.deletion_snapshot,updated_at=CURRENT_TIMESTAMP`,
			operationID, item.ServerID, item.ServerName, item.InboundTag, item.Protocol, item.Action, item.Status, item.RemainingUsers, item.LastError, item.LastCheckedAt,
			item.ItemKind, item.PackageID, item.PackageName, string(nodeIDs), item.DefaultCredentials, item.UnknownCredentials, item.DecisionNote, string(snapshot))
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET pending_count=$2,updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, operationID, pending)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle SET pending_count=$2,updated_at=CURRENT_TIMESTAMP WHERE username=$1`, username, pending)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresAdminSessionStore) MarkLifecycleItem(ctx context.Context, operationID string, item lifecyclePlanItem) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_items SET action=$4,status=$5,protocol=$6,remaining_users=$7,attempts=attempts+1,last_error=$8,last_checked_at=CURRENT_TIMESTAMP,default_credentials=$9,unknown_credentials=$10,decision_note=$11,updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1 AND server_id=$2 AND inbound_tag=$3`,
		operationID, item.ServerID, item.InboundTag, item.Action, item.Status, item.Protocol, item.RemainingUsers, item.LastError, item.DefaultCredentials, item.UnknownCredentials, item.DecisionNote)
	return err
}

func (s *postgresAdminSessionStore) FinishDeleteAttempt(ctx context.Context, username, operationID string, pending int, lastError string) error {
	state := lifecycleStateDeleting
	if pending > 0 {
		state = lifecycleStateDeletePartial
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET state=$2,pending_count=$3,last_error=$4,updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, operationID, state, pending, lastError); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle SET effective_state=$2,pending_count=$3,last_error=$4,updated_at=CURRENT_TIMESTAMP WHERE username=$1`, username, state, pending, lastError); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *postgresAdminSessionStore) FinalizeManagementUserDeletion(ctx context.Context, username, operationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(role,'') FROM users WHERE username=$1 FOR UPDATE`, username).Scan(&role); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if role == "admin" {
		return errors.New("administrator accounts cannot be deleted")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_operations SET state='deleted',pending_count=0,last_error='',updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1 AND username=$2`, operationID, username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mmwxc_user_lifecycle_items SET deletion_snapshot='{}'::jsonb WHERE operation_id=$1`, operationID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM users WHERE username=$1`, username)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("user deletion was not committed")
	}
	return tx.Commit()
}

func (a *app) userLifecycleIndexHandler(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	store, ok := a.adminStore.(lifecycleStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "lifecycle store unavailable"})
		return
	}
	states, err := store.LifecycleStates(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to read lifecycle state"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "users": states})
}

func (a *app) buildDeletionPlan(ctx context.Context, token, username string) ([]lifecyclePlanItem, error) {
	store, ok := a.adminStore.(lifecycleStore)
	if !ok {
		return nil, errors.New("lifecycle store unavailable")
	}
	refs, err := store.LifecycleCredentialRefs(ctx, username)
	if err != nil {
		return nil, err
	}
	var previous []lifecyclePlanItem
	if saved, ok := store.(interface {
		LifecycleDeleteItems(context.Context, string) ([]lifecyclePlanItem, error)
	}); ok {
		previous, err = saved.LifecycleDeleteItems(ctx, username)
		if err != nil {
			return nil, err
		}
		for _, item := range previous {
			refs = append(refs, item.deleteRefs...)
			for _, credential := range item.targetCredentials {
				raw, _ := json.Marshal(credential)
				refs = append(refs, lifecycleCredentialRef{Username: username, ServerID: item.ServerID, ServerName: item.ServerName, InboundTag: item.InboundTag, Protocol: item.Protocol, CredentialRaw: string(raw), Source: "delete_snapshot"})
			}
		}
	}
	backups, err := store.LifecycleDisabledCredentials(ctx, username)
	if err != nil {
		return nil, err
	}
	for _, backup := range backups {
		for _, credential := range []map[string]any{backup.OriginalCredential, backup.DisabledCredential} {
			raw, marshalErr := json.Marshal(credential)
			if marshalErr != nil {
				return nil, marshalErr
			}
			refs = append(refs, lifecycleCredentialRef{
				ServerID: backup.ServerID, ServerName: backup.ServerName, InboundTag: backup.InboundTag,
				Protocol: backup.Protocol, CredentialRaw: string(raw), Source: "mmwxc_user_disabled_credentials",
			})
		}
	}
	data, err := store.LifecycleDeletionData(ctx, username)
	if err != nil {
		return nil, err
	}
	grouped := make(map[string][]lifecycleCredentialRef)
	for _, ref := range refs {
		key := strconv.FormatInt(ref.ServerID, 10) + "\x00" + ref.InboundTag
		grouped[key] = append(grouped[key], ref)
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	configs := make(map[int64]map[string]any)
	items := make([]lifecyclePlanItem, 0, len(keys))
	for _, key := range keys {
		refs := grouped[key]
		first := refs[0]
		config := configs[first.ServerID]
		if config == nil {
			config, err = a.fetchOfficialXrayConfig(ctx, token, first.ServerID)
			if err != nil {
				log.Printf("[mmwx-custom] lifecycle config read failed server_id=%d: %v", first.ServerID, err)
				failed := failedPlanItem(first, "读取真实 Xray 配置失败")
				failed.deleteRefs = refs
				items = append(items, failed)
				continue
			}
			configs[first.ServerID] = config
		}
		businessRefs, businessErr := store.LifecycleInboundBusinessRefs(ctx, first.ServerID, first.ServerName, first.InboundTag, username)
		if businessErr != nil {
			failed := failedPlanItem(first, "读取业务用户关系失败")
			failed.deleteRefs = refs
			items = append(items, failed)
			continue
		}
		defaultCredentials, defaultErr := store.LifecycleDefaultAdminCredentials(ctx, first.ServerID, first.InboundTag)
		if defaultErr != nil {
			failed := failedPlanItem(first, "核对创建时管理员 credential 失败")
			failed.deleteRefs = refs
			items = append(items, failed)
			continue
		}
		businessRefs = deletionBusinessRefs(findConfigInbound(config, first.InboundTag), username, data, businessRefs)
		item := analyzeLifecycleInbound(config, refs, businessRefs, defaultCredentials)
		item.deleteRefs = refs
		items = append(items, item)
	}
	deleted := make(map[int64]lifecycleNodeLabel)
	for index := range items {
		item := &items[index]
		for _, old := range previous {
			if old.ItemKind == lifecycleItemKindInbound && old.ServerID == item.ServerID && old.InboundTag == item.InboundTag && old.Action == lifecycleActionDeleteWhole {
				item.NodeIDs = append(item.NodeIDs, old.NodeIDs...)
				if item.Action != lifecycleActionConflict && len(item.targetCredentials) == 0 && len(old.targetCredentials) > 0 {
					item.Action = old.Action
					item.Status = lifecycleItemPending
					item.targetCredentials = old.targetCredentials
					item.nonTargetHashes = old.nonTargetHashes
					item.defaultCredentialHashes = old.defaultCredentialHashes
				}
			}
		}
		if item.Action != lifecycleActionDeleteWhole {
			continue
		}
		for _, node := range data.Nodes {
			if node.ServerID == item.ServerID && node.InboundTag == item.InboundTag && !containsLifecycleNode(item.NodeIDs, node.ID) {
				item.NodeIDs = append(item.NodeIDs, node.ID)
			}
		}
		for _, id := range item.NodeIDs {
			label := lifecycleNodeLabel{ID: id}
			if node, ok := data.Nodes[id]; ok {
				label = node.lifecycleNodeLabel
			}
			deleted[id] = label
		}
		if len(item.NodeIDs) > 0 {
			item.Status = lifecycleItemPending
		}
	}
	for _, pkg := range data.Packages {
		affected := pkg.Bound
		for _, id := range pkg.NodeIDs {
			if _, ok := deleted[id]; ok {
				affected = true
			}
		}
		// The official remove operation may already have pruned packages.nodes;
		// retain our per-node cleanup targets across retries as well.
		var previousDeleted []int64
		for _, old := range previous {
			if old.ItemKind == lifecycleItemKindPackage && old.PackageID == pkg.ID {
				affected = true
				previousDeleted = old.DeletedNodeIDs
			}
		}
		if !affected {
			continue
		}
		item := a.classifyDeletionPackage(ctx, token, username, pkg, data, deleted, configs)
		for _, id := range previousDeleted {
			if !containsLifecycleNode(item.DeletedNodeIDs, id) {
				item.DeletedNodeIDs = append(item.DeletedNodeIDs, id)
				item.OwnNodes = append(item.OwnNodes, lifecycleNodeLabel{ID: id})
				if item.Action == lifecycleActionKeepPackage {
					item.Status = lifecycleItemPending
				}
			}
		}
		if item.Action == lifecycleActionKeepPackage {
			item.DecisionNote = fmt.Sprintf("保留套餐，移除 %d 个该用户节点", len(item.DeletedNodeIDs))
		}
		items = append(items, item)
	}
	return items, nil
}

func failedPlanItem(ref lifecycleCredentialRef, message string) lifecyclePlanItem {
	return lifecyclePlanItem{deleteRefs: []lifecycleCredentialRef{ref}, ItemKind: lifecycleItemKindInbound, ServerID: ref.ServerID, ServerName: ref.ServerName, InboundTag: ref.InboundTag, Protocol: ref.Protocol, Action: lifecycleActionConflict, Status: lifecycleItemFailed, LastError: message, DecisionNote: message}
}

func analyzeLifecycleInbound(config map[string]any, refs, businessRefs []lifecycleCredentialRef, defaultCredentials []map[string]any) lifecyclePlanItem {
	first := refs[0]
	item := lifecyclePlanItem{ItemKind: lifecycleItemKindInbound, ServerID: first.ServerID, ServerName: first.ServerName, InboundTag: first.InboundTag, Protocol: first.Protocol, Status: lifecycleItemPending}
	businessUsers := make(map[string]bool)
	for _, ref := range businessRefs {
		if username := strings.TrimSpace(ref.Username); username != "" {
			businessUsers[username] = true
		}
	}
	item.RemainingUsers = len(businessUsers)
	inbound := findConfigInbound(config, first.InboundTag)
	if inbound == nil {
		item.Action = lifecycleActionDeleteWhole
		item.Status = lifecycleItemCompleted
		item.DecisionNote = "Inbound 已不存在"
		return item
	}
	item.Protocol = strings.ToLower(strings.TrimSpace(fmt.Sprint(inbound["protocol"])))
	entries, _, err := accessInboundCredentialEntries(inbound)
	if err != nil {
		item.Action = lifecycleActionConflict
		item.Status = lifecycleItemFailed
		item.LastError = err.Error()
		return item
	}
	var targets []map[string]any
	var nonTargets []map[string]any
	for _, entry := range entries {
		if lifecycleEntryMatchesRefs(entry, inbound, item.Protocol, refs) {
			targets = append(targets, entry)
		} else {
			nonTargets = append(nonTargets, entry)
			item.nonTargetHashes = append(item.nonTargetHashes, hashJSON(entry))
		}
	}
	item.targetCredentials = targets
	for _, target := range targets {
		for _, ref := range businessRefs {
			if (ref.CredentialRaw == "" && ref.Identity == "") || lifecycleEntryMatchesRefs(target, inbound, item.Protocol, []lifecycleCredentialRef{ref}) {
				item.Action, item.Status = lifecycleActionConflict, lifecycleItemFailed
				item.LastError = "该用户凭据同时被其他用户使用，不能安全删除"
				item.DecisionNote = item.LastError
				return item
			}
		}
	}
	wholePortBusiness := false
	wholePortBusinessUsers := make(map[string]bool)
	for _, ref := range businessRefs {
		if strings.TrimSpace(ref.CredentialRaw) == "" && strings.TrimSpace(ref.Identity) == "" {
			wholePortBusiness = true
			wholePortBusinessUsers[ref.Username] = true
		}
	}
	matchedBusinessUsers := make(map[string]bool)
	for _, entry := range nonTargets {
		matchedBusiness := false
		for _, ref := range businessRefs {
			if lifecycleEntryMatchesRefs(entry, inbound, item.Protocol, []lifecycleCredentialRef{ref}) {
				matchedBusiness = true
				matchedBusinessUsers[ref.Username] = true
			}
		}
		if matchedBusiness || wholePortBusiness {
			for username := range wholePortBusinessUsers {
				matchedBusinessUsers[username] = true
			}
			continue
		}
		isDefault := false
		for _, credential := range defaultCredentials {
			if credentialsMatch(entry, credential, item.Protocol) {
				isDefault = true
				break
			}
		}
		if isDefault {
			item.DefaultCredentials++
			item.defaultCredentialHashes = append(item.defaultCredentialHashes, hashJSON(entry))
		} else {
			item.UnknownCredentials++
		}
	}
	if item.UnknownCredentials > 0 {
		item.Action = lifecycleActionConflict
		item.Status = lifecycleItemFailed
		item.LastError = fmt.Sprintf("发现 %d 个无法确认来源的 credential，需要人工检查", item.UnknownCredentials)
		item.DecisionNote = item.LastError
		return item
	}
	if len(matchedBusinessUsers) < item.RemainingUsers {
		item.Action = lifecycleActionConflict
		item.Status = lifecycleItemFailed
		item.LastError = "业务关系存在但 runtime credential 缺失，需要人工检查"
		item.DecisionNote = item.LastError
		return item
	}
	if item.RemainingUsers > 0 {
		item.Action = lifecycleActionRemoveUser
		item.DecisionNote = fmt.Sprintf("还有 %d 个业务用户使用，保留 Inbound", item.RemainingUsers)
		if len(targets) == 0 {
			item.Status = lifecycleItemCompleted
		}
		return item
	}
	if len(targets) == 0 {
		item.Action, item.Status = lifecycleActionRemoveUser, lifecycleItemCompleted
		item.DecisionNote = "没有该用户的运行凭据，保留节点"
		return item
	}
	item.Action = lifecycleActionDeleteWhole
	if item.DefaultCredentials > 0 {
		item.DecisionNote = "仅存在创建时管理员 credential，不视为业务共享"
	} else {
		item.DecisionNote = "没有其他业务用户，删除整个 Inbound"
	}
	return item
}

func findConfigInbound(config map[string]any, tag string) map[string]any {
	inbounds, _ := config["inbounds"].([]any)
	for _, raw := range inbounds {
		inbound, _ := raw.(map[string]any)
		if strings.TrimSpace(fmt.Sprint(inbound["tag"])) == tag {
			return inbound
		}
	}
	return nil
}

func inboundCredentialEntries(inbound map[string]any) ([]map[string]any, string, error) {
	settings, _ := inbound["settings"].(map[string]any)
	if settings == nil {
		return nil, "", errors.New("Inbound settings 不存在")
	}
	protocol := strings.ToLower(strings.TrimSpace(fmt.Sprint(inbound["protocol"])))
	key := "clients"
	switch protocol {
	case "snell", "mieru", "anytls":
		key = "users"
	case "socks", "http":
		key = "accounts"
	}
	rawEntries, ok := settings[key].([]any)
	if !ok {
		return nil, key, fmt.Errorf("Inbound %s 缺少 %s 数组", strings.TrimSpace(fmt.Sprint(inbound["tag"])), key)
	}
	entries := make([]map[string]any, 0, len(rawEntries))
	for _, raw := range rawEntries {
		entry, _ := raw.(map[string]any)
		if entry != nil {
			entries = append(entries, entry)
		}
	}
	return entries, key, nil
}

func lifecycleEntryMatchesRefs(entry, inbound map[string]any, protocol string, refs []lifecycleCredentialRef) bool {
	for _, ref := range refs {
		if ref.Identity != "" && credentialContainsIdentity(entry, ref.Identity) {
			return true
		}
		if strings.TrimSpace(ref.CredentialRaw) == "" {
			continue
		}
		var credential map[string]any
		if json.Unmarshal([]byte(ref.CredentialRaw), &credential) == nil && credentialsMatch(entry, credential, protocol) {
			return true
		}
		var rawValues []string
		if json.Unmarshal([]byte(ref.CredentialRaw), &rawValues) == nil && lifecycleNodeValuesMatch(entry, inbound, rawValues) {
			return true
		}
	}
	return false
}

func credentialsMatch(actual, expected map[string]any, protocol string) bool {
	primary := map[string]string{
		"vless": "id", "vmess": "id", "trojan": "password", "shadowsocks": "password", "ss": "password",
		"anytls": "password", "snell": "psk", "mieru": "username", "hysteria": "auth", "hysteria2": "auth",
		"hy2": "auth", "socks": "user", "http": "user",
	}[strings.ToLower(strings.TrimSpace(protocol))]
	if primary != "" && nonEmptyCredentialValue(actual, primary) && nonEmptyCredentialValue(expected, primary) {
		return strings.TrimSpace(fmt.Sprint(actual[primary])) == strings.TrimSpace(fmt.Sprint(expected[primary]))
	}
	return nonEmptyCredentialValue(actual, "email") && nonEmptyCredentialValue(expected, "email") &&
		strings.TrimSpace(fmt.Sprint(actual["email"])) == strings.TrimSpace(fmt.Sprint(expected["email"]))
}

func nonEmptyCredentialValue(value map[string]any, key string) bool {
	text := strings.TrimSpace(fmt.Sprint(value[key]))
	return text != "" && text != "<nil>"
}

func lifecycleNodeValuesMatch(entry, inbound map[string]any, rawValues []string) bool {
	return len(matchNodeProtocolIdentities(rawValues, []protocolCredential{{Identity: "match", Secrets: credentialSecrets(entry), SS2022ServerKey: lifecycleInboundSS2022ServerKey(inbound)}})) > 0
}

func lifecycleInboundSS2022ServerKey(inbound map[string]any) string {
	protocol, _ := inbound["protocol"].(string)
	settings, _ := inbound["settings"].(map[string]any)
	method, _ := settings["method"].(string)
	if protocol == "shadowsocks" || protocol == "ss" {
		switch method {
		case "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
			serverKey, _ := settings["password"].(string)
			return serverKey
		}
	}
	return ""
}

func credentialContainsIdentity(credential map[string]any, identity string) bool {
	for _, key := range []string{"email", "username", "user", "id", "password", "pass", "psk", "auth", "token"} {
		if strings.TrimSpace(fmt.Sprint(credential[key])) == identity {
			return true
		}
	}
	return false
}

func hashJSON(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a *app) fetchOfficialXrayConfig(ctx context.Context, token string, serverID int64) (map[string]any, error) {
	var response struct {
		Success bool   `json:"success"`
		Config  string `json:"config"`
	}
	path := "/api/admin/remote/xray/config?server_id=" + url.QueryEscape(strconv.FormatInt(serverID, 10))
	if err := a.officialLifecycleJSON(ctx, token, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	if !response.Success || strings.TrimSpace(response.Config) == "" {
		return nil, errors.New("official API returned an empty Xray config")
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(response.Config), &config); err != nil {
		return nil, errors.New("official API returned invalid Xray config")
	}
	return config, nil
}

func (a *app) officialLifecycleJSON(ctx context.Context, token, method, path string, body any, result any) error {
	if a.officialInternalTarget == nil {
		return errors.New("official API target unavailable")
	}
	endpoint := *a.officialInternalTarget
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + strings.SplitN(path, "?", 2)[0]
	if parts := strings.SplitN(path, "?", 2); len(parts) == 2 {
		endpoint.RawQuery = parts[1]
	} else {
		endpoint.RawQuery = ""
	}
	client := &http.Client{
		Transport: http.DefaultTransport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	channelRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, a.officialInternalTarget.String(), nil)
	if err != nil {
		return err
	}
	channel, err := openOfficialSecureChannel(channelRequest, client, a.officialInternalTarget)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(base64.StdEncoding.EncodeToString(channel.encrypt(raw)))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return err
	}
	request.Host = a.officialInternalTarget.Host
	request.Header.Set("MM-Authorization", token)
	request.Header.Set("X-Secure-Channel", officialSecureChannelVersion)
	request.Header.Set("X-Session-Id", channel.sessionID)
	if body != nil {
		request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxOfficialLifecycleResponse+1))
	if err != nil {
		return err
	}
	if len(raw) > maxOfficialLifecycleResponse {
		return errors.New("official API response too large")
	}
	if response.Header.Get("X-Secure-Channel") != officialSecureChannelVersion {
		return errors.New("official API returned an unencrypted response")
	}
	secureEnvelope, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return errors.New("official API returned an invalid secure-channel response")
	}
	raw, err = channel.decrypt(secureEnvelope)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("official API returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Success        *bool  `json:"success"`
		RuntimeWarning string `json:"runtime_warning"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if envelope.Success != nil && !*envelope.Success {
		return errors.New("official API rejected lifecycle operation")
	}
	if strings.TrimSpace(envelope.RuntimeWarning) != "" {
		return errors.New("Agent runtime 未确认，远程配置已写入但运行态应用失败")
	}
	if result != nil && json.Unmarshal(raw, result) != nil {
		return errors.New("official API returned invalid JSON")
	}
	return nil
}

func (a *app) executeDeletePlan(ctx context.Context, token, username, operationID string, items []lifecyclePlanItem) lifecycleDeleteResult {
	store := a.adminStore.(lifecycleStore)
	result := lifecycleDeleteResult{Username: username, OperationID: operationID, State: lifecycleStateDeleting, Items: items}
	lastError := ""
	sort.SliceStable(result.Items, func(i, j int) bool {
		return result.Items[i].ItemKind != lifecycleItemKindPackage && result.Items[j].ItemKind == lifecycleItemKindPackage
	})
	for index := range result.Items {
		item := &result.Items[index]
		if item.Status == lifecycleItemCompleted {
			continue
		}
		if item.ItemKind == lifecycleItemKindPackage {
			blocked := false
			for _, inbound := range result.Items {
				if inbound.ItemKind != lifecycleItemKindPackage && inbound.Status != lifecycleItemCompleted {
					blocked = true
					break
				}
			}
			if blocked {
				item.LastError = "等待该用户节点清理完成，再处理套餐"
				continue
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := a.executeLifecycleDeleteItem(attemptCtx, token, username, item)
		cancel()
		if err != nil {
			item.Status = lifecycleItemFailed
			item.LastError = lifecycleSafeError(err)
			lastError = item.LastError
		} else {
			item.Status = lifecycleItemCompleted
			item.LastError = ""
		}
		item.Attempts++
		now := time.Now().UTC()
		item.LastCheckedAt = &now
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
		persistErr := store.MarkLifecycleItem(persistCtx, operationID, *item)
		persistCancel()
		if persistErr != nil {
			item.Status = lifecycleItemFailed
			item.LastError = "远程状态已复核，但删除任务进度保存失败"
			lastError = item.LastError
		}
	}
	allCompleted := true
	for _, item := range result.Items {
		if item.Status != lifecycleItemCompleted {
			allCompleted = false
		}
	}
	if allCompleted {
		// Package updates can cause official configuration writes. Verify the
		// removed runtime credentials again before deleting their source rows.
		configs := make(map[int64]map[string]any)
		for index := range result.Items {
			item := &result.Items[index]
			if item.ItemKind != lifecycleItemKindInbound || (len(item.targetCredentials) == 0 && item.Action != lifecycleActionDeleteWhole) {
				continue
			}
			config := configs[item.ServerID]
			var verifyErr error
			if config == nil {
				config, verifyErr = a.fetchOfficialXrayConfig(ctx, token, item.ServerID)
				configs[item.ServerID] = config
			}
			if verifyErr == nil {
				verifyErr = verifyLifecycleDeletedCredential(config, *item)
			}
			if verifyErr != nil {
				item.Status, item.LastError = lifecycleItemFailed, lifecycleSafeError(verifyErr)
				lastError = item.LastError
				persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = store.MarkLifecycleItem(persistCtx, operationID, *item)
				cancel()
			}
		}
	}
	for _, item := range result.Items {
		if item.Status != lifecycleItemCompleted {
			result.PendingCount++
		}
	}
	result.LastError = lastError
	if result.PendingCount > 0 {
		result.State = lifecycleStateDeletePartial
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
		finishErr := store.FinishDeleteAttempt(persistCtx, username, operationID, result.PendingCount, lastError)
		persistCancel()
		if finishErr != nil {
			result.LastError = "删除任务进度保存失败"
		}
		return result
	}
	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := store.FinalizeManagementUserDeletion(finalizeCtx, username, operationID)
	finalizeCancel()
	if err != nil {
		result.State = lifecycleStateDeletePartial
		result.PendingCount = 1
		result.LastError = "远程访问已清理，但用户数据库事务提交失败"
		persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = store.FinishDeleteAttempt(persistCtx, username, operationID, 1, result.LastError)
		persistCancel()
		return result
	}
	result.State = lifecycleStateDeleted
	result.UserDeleted = true
	return result
}

func (a *app) executeLifecycleDeleteItem(ctx context.Context, token, username string, item *lifecyclePlanItem) error {
	if item.Action == lifecycleActionConflict {
		if item.LastError != "" {
			return errors.New(item.LastError)
		}
		return errors.New("删除计划存在冲突，未执行远程修改")
	}
	if item.ItemKind == lifecycleItemKindPackage {
		return a.executeDeletionPackage(ctx, token, username, item)
	}
	config, err := a.fetchOfficialXrayConfig(ctx, token, item.ServerID)
	if err != nil {
		return err
	}
	if findConfigInbound(config, item.InboundTag) == nil {
		return a.finishLifecycleDeletedNodes(ctx, token, item)
	}
	if len(item.targetCredentials) == 0 {
		if item.Status == lifecycleItemCompleted {
			return nil
		}
		if item.Action != lifecycleActionDeleteWhole {
			return errors.New("无法重新定位目标用户 credential")
		}
	}
	inbound := findConfigInbound(config, item.InboundTag)
	currentProtocol := strings.ToLower(strings.TrimSpace(fmt.Sprint(inbound["protocol"])))
	if item.Protocol != "" && currentProtocol != strings.ToLower(strings.TrimSpace(item.Protocol)) {
		return errors.New("Inbound 协议已发生变化，未执行删除")
	}
	entries, _, err := accessInboundCredentialEntries(inbound)
	if err != nil {
		return err
	}
	expectedNonTargets := lifecycleHashCounts(item.nonTargetHashes)
	currentTargets := make([]map[string]any, 0, len(item.targetCredentials))
	currentNonTargets := 0
	for _, entry := range entries {
		matched := false
		for _, target := range item.targetCredentials {
			if credentialsMatch(entry, target, currentProtocol) {
				matched = true
				break
			}
		}
		if matched {
			currentTargets = append(currentTargets, entry)
			continue
		}
		hash := hashJSON(entry)
		if expectedNonTargets[hash] == 0 {
			return errors.New("Inbound credential 已发生变化，未执行删除")
		}
		expectedNonTargets[hash]--
		currentNonTargets++
	}
	for _, remaining := range expectedNonTargets {
		if remaining != 0 {
			return errors.New("共享 Inbound 的其他 credential 已发生变化")
		}
	}
	if len(currentTargets) == 0 {
		if item.Action != lifecycleActionDeleteWhole {
			return nil
		}
	}
	if item.Action == lifecycleActionDeleteWhole {
		allowedDefaults := lifecycleHashCounts(item.defaultCredentialHashes)
		for _, entry := range entries {
			matchedTarget := false
			for _, target := range item.targetCredentials {
				if credentialsMatch(entry, target, currentProtocol) {
					matchedTarget = true
					break
				}
			}
			if matchedTarget {
				continue
			}
			hash := hashJSON(entry)
			if allowedDefaults[hash] == 0 {
				return errors.New("Inbound 出现无法确认来源的 credential，未执行整项删除")
			}
			allowedDefaults[hash]--
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost,
			"/api/admin/remote/inbounds?server_id="+url.QueryEscape(strconv.FormatInt(item.ServerID, 10)),
			map[string]any{"action": "remove", "tag": item.InboundTag}, nil); err != nil {
			return err
		}
		verified, err := a.fetchOfficialXrayConfig(ctx, token, item.ServerID)
		if err != nil {
			return err
		}
		if findConfigInbound(verified, item.InboundTag) != nil {
			return errors.New("HTTP 成功但真实 Xray 配置仍存在该 Inbound")
		}
		return a.finishLifecycleDeletedNodes(ctx, token, item)
	}
	if item.Action != lifecycleActionRemoveUser {
		return errors.New("删除计划存在冲突，未执行远程修改")
	}
	if currentNonTargets == 0 {
		return errors.New("共享 Inbound 已没有可保留的其他 credential，未执行移除")
	}
	for _, credential := range currentTargets {
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost,
			"/api/admin/remote/inbounds?server_id="+url.QueryEscape(strconv.FormatInt(item.ServerID, 10)),
			map[string]any{"action": "remove-client", "tag": item.InboundTag, "client": credential}, nil); err != nil {
			return err
		}
	}
	verified, err := a.fetchOfficialXrayConfig(ctx, token, item.ServerID)
	if err != nil {
		return err
	}
	inbound = findConfigInbound(verified, item.InboundTag)
	if inbound == nil {
		return errors.New("共享 Inbound 被意外删除")
	}
	entries, _, err = accessInboundCredentialEntries(inbound)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		for _, target := range item.targetCredentials {
			if credentialsMatch(entry, target, item.Protocol) {
				return errors.New("HTTP 成功但目标 credential 仍存在")
			}
		}
	}
	actualHashes := make(map[string]int)
	for _, entry := range entries {
		actualHashes[hashJSON(entry)]++
	}
	for _, hash := range item.nonTargetHashes {
		if actualHashes[hash] == 0 {
			return errors.New("共享 Inbound 的其他 credential 发生变化")
		}
		actualHashes[hash]--
	}
	return nil
}

func lifecycleHashCounts(hashes []string) map[string]int {
	result := make(map[string]int, len(hashes))
	for _, hash := range hashes {
		result[hash]++
	}
	return result
}

func lifecycleSafeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func verifyLifecycleDeletedCredential(config map[string]any, item lifecyclePlanItem) error {
	inbound := findConfigInbound(config, item.InboundTag)
	if inbound == nil {
		return nil
	}
	if item.Action == lifecycleActionDeleteWhole {
		return errors.New("套餐处理后入站仍存在，用户删除未完成")
	}
	entries, _, err := accessInboundCredentialEntries(inbound)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		for _, target := range item.targetCredentials {
			if credentialsMatch(entry, target, item.Protocol) {
				return errors.New("套餐处理后旧用户凭据仍存在，用户删除未完成")
			}
		}
	}
	return nil
}
