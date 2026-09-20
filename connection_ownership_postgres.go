package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const connectionOwnershipSchema = `
CREATE TABLE IF NOT EXISTS mmwxc_connection_assignments (
    server_id BIGINT NOT NULL,
    inbound_tag TEXT NOT NULL,
    management_username TEXT NOT NULL,
    protocol_identity TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'manual',
    assignment_type TEXT NOT NULL DEFAULT 'manual',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (server_id, inbound_tag, management_username, protocol_identity),
    CHECK (source = 'manual'),
    CHECK (assignment_type IN ('manual', 'identity', 'port')),
    FOREIGN KEY (server_id) REFERENCES remote_servers(id) ON DELETE CASCADE,
    FOREIGN KEY (management_username) REFERENCES users(username) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mmwxc_connection_assignments_server_tag
    ON mmwxc_connection_assignments(server_id, inbound_tag);
DROP INDEX IF EXISTS idx_mmwxc_connection_assignments_identity_owner;
CREATE UNIQUE INDEX idx_mmwxc_connection_assignments_identity_owner
    ON mmwxc_connection_assignments(server_id, inbound_tag, protocol_identity)
    WHERE protocol_identity <> '';
`

func (s *postgresAdminSessionStore) EnsureConnectionOwnershipSchema(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, connectionOwnershipSchema)
	if err != nil {
		return fmt.Errorf("migrate connection ownership: %w", err)
	}
	return nil
}

func (s *postgresAdminSessionStore) ConnectionOwnership(ctx context.Context, serverID string) (connectionOwnershipData, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(serverID), 10, 64)
	if err != nil || parsed <= 0 {
		return connectionOwnershipData{}, errOperatorServerNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	result := connectionOwnershipData{ManagementUsers: []string{}, Relations: []connectionOwnershipRelation{}}
	var serverName string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM remote_servers WHERE id = $1`, parsed).Scan(&serverName); errors.Is(err, sql.ErrNoRows) {
		return connectionOwnershipData{}, errOperatorServerNotFound
	} else if err != nil {
		return connectionOwnershipData{}, err
	}

	users, err := s.db.QueryContext(ctx, `SELECT username FROM users WHERE is_active = 1 ORDER BY username`)
	if err != nil {
		return connectionOwnershipData{}, err
	}
	for users.Next() {
		var username string
		if err := users.Scan(&username); err != nil {
			users.Close()
			return connectionOwnershipData{}, err
		}
		result.ManagementUsers = append(result.ManagementUsers, username)
	}
	if err := users.Close(); err != nil {
		return connectionOwnershipData{}, err
	}

	bindings, err := s.db.QueryContext(ctx, `
		SELECT c.username, c.inbound_tag, c.credential_json
		FROM user_inbound_configs c
		JOIN users u ON u.username = c.username AND u.is_active = 1
		WHERE c.server_id = $1`, parsed)
	if err != nil {
		return connectionOwnershipData{}, err
	}
	for bindings.Next() {
		var username, tag, rawCredential string
		if err := bindings.Scan(&username, &tag, &rawCredential); err != nil {
			bindings.Close()
			return connectionOwnershipData{}, err
		}
		result.Relations = append(result.Relations, connectionOwnershipRelation{
			InboundTag: tag, ManagementUsername: username, ProtocolIdentity: extractProtocolIdentity(rawCredential),
			Source: connectionSourceBinding, AssignmentType: "user_inbound_config",
		})
	}
	if err := bindings.Close(); err != nil {
		return connectionOwnershipData{}, err
	}

	subaccounts, err := s.db.QueryContext(ctx, `
		SELECT sa.username, n.inbound_tag, sa.email
		FROM user_subaccounts sa
		JOIN users u ON u.username = sa.username AND u.is_active = 1
		JOIN nodes n ON n.id = sa.routed_node_id
		WHERE n.original_server = $1 AND COALESCE(n.inbound_tag, '') <> '' AND sa.is_active = 1`, serverName)
	if err != nil {
		return connectionOwnershipData{}, err
	}
	for subaccounts.Next() {
		var username, tag, identity string
		if err := subaccounts.Scan(&username, &tag, &identity); err != nil {
			subaccounts.Close()
			return connectionOwnershipData{}, err
		}
		result.Relations = append(result.Relations, connectionOwnershipRelation{
			InboundTag: tag, ManagementUsername: username, ProtocolIdentity: strings.TrimSpace(identity),
			Source: connectionSourceBinding, AssignmentType: "user_subaccount",
		})
	}
	if err := subaccounts.Close(); err != nil {
		return connectionOwnershipData{}, err
	}

	coreCredentials := map[string][]protocolCredential{}
	var currentConfig string
	if err := s.db.QueryRowContext(ctx, `
		SELECT config_json FROM server_xray_config_snapshots
		WHERE server_id = $1 AND status = 'current'
		ORDER BY created_at DESC LIMIT 1`, parsed).Scan(&currentConfig); err == nil {
		coreCredentials = extractCoreInboundCredentials(currentConfig)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return connectionOwnershipData{}, err
	}

	owners, err := s.db.QueryContext(ctx, `
		SELECT n.username, n.inbound_tag, COALESCE(n.raw_url, ''), COALESCE(n.parsed_config, ''), COALESCE(n.clash_config, '')
		FROM nodes n
		JOIN users u ON u.username = n.username AND u.is_active = 1
		WHERE n.original_server = $1 AND COALESCE(n.inbound_tag, '') <> '' AND COALESCE(n.node_type, 'physical') <> 'routed'`, serverName)
	if err != nil {
		return connectionOwnershipData{}, err
	}
	ownerKeys := make(map[string]struct{})
	for owners.Next() {
		var username, tag, rawURL, parsedConfig, clashConfig string
		if err := owners.Scan(&username, &tag, &rawURL, &parsedConfig, &clashConfig); err != nil {
			owners.Close()
			return connectionOwnershipData{}, err
		}
		identities := matchNodeProtocolIdentities([]string{rawURL, parsedConfig, clashConfig}, coreCredentials[tag])
		if len(identities) == 0 {
			identities = []string{""}
		}
		for _, identity := range identities {
			key := username + "\x00" + tag + "\x00" + identity
			if _, exists := ownerKeys[key]; exists {
				continue
			}
			ownerKeys[key] = struct{}{}
			result.Relations = append(result.Relations, connectionOwnershipRelation{
				InboundTag: tag, ManagementUsername: username, ProtocolIdentity: identity,
				Source: connectionSourceOwner, AssignmentType: "node_owner",
			})
		}
	}
	if err := owners.Close(); err != nil {
		return connectionOwnershipData{}, err
	}

	manual, err := s.db.QueryContext(ctx, `
		SELECT a.inbound_tag, a.management_username, a.protocol_identity, a.source, a.assignment_type
		FROM mmwxc_connection_assignments a
		JOIN users u ON u.username = a.management_username AND u.is_active = 1
		WHERE a.server_id = $1`, parsed)
	if err != nil {
		return connectionOwnershipData{}, err
	}
	for manual.Next() {
		var relation connectionOwnershipRelation
		if err := manual.Scan(&relation.InboundTag, &relation.ManagementUsername, &relation.ProtocolIdentity, &relation.Source, &relation.AssignmentType); err != nil {
			manual.Close()
			return connectionOwnershipData{}, err
		}
		result.Relations = append(result.Relations, relation)
	}
	if err := manual.Close(); err != nil {
		return connectionOwnershipData{}, err
	}

	sort.Slice(result.Relations, func(i, j int) bool {
		if result.Relations[i].InboundTag != result.Relations[j].InboundTag {
			return result.Relations[i].InboundTag < result.Relations[j].InboundTag
		}
		if sourcePriority(result.Relations[i].Source) != sourcePriority(result.Relations[j].Source) {
			return sourcePriority(result.Relations[i].Source) > sourcePriority(result.Relations[j].Source)
		}
		if result.Relations[i].ManagementUsername != result.Relations[j].ManagementUsername {
			return result.Relations[i].ManagementUsername < result.Relations[j].ManagementUsername
		}
		return result.Relations[i].ProtocolIdentity < result.Relations[j].ProtocolIdentity
	})
	return result, nil
}

func (s *postgresAdminSessionStore) SaveManualConnectionAssignment(ctx context.Context, serverID string, relation connectionOwnershipRelation) error {
	parsed, err := strconv.ParseInt(strings.TrimSpace(serverID), 10, 64)
	if err != nil || parsed <= 0 || relation.Source != connectionSourceManual {
		return errors.New("invalid manual assignment")
	}
	assignmentType := "port"
	if relation.ProtocolIdentity != "" {
		assignmentType = "identity"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO mmwxc_connection_assignments
			(server_id, inbound_tag, management_username, protocol_identity, source, assignment_type)
		SELECT $1, $2, $3, $4, 'manual', $5
		WHERE EXISTS (SELECT 1 FROM remote_servers WHERE id = $1)
		  AND EXISTS (SELECT 1 FROM users WHERE username = $3 AND is_active = 1)
		ON CONFLICT (server_id, inbound_tag, management_username, protocol_identity)
		DO UPDATE SET assignment_type = EXCLUDED.assignment_type, updated_at = CURRENT_TIMESTAMP`,
		parsed, relation.InboundTag, relation.ManagementUsername, relation.ProtocolIdentity, assignmentType)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("server or management user does not exist")
	}
	return nil
}

func (s *postgresAdminSessionStore) DeleteManualConnectionAssignment(ctx context.Context, serverID string, relation connectionOwnershipRelation) error {
	parsed, err := strconv.ParseInt(strings.TrimSpace(serverID), 10, 64)
	if err != nil || parsed <= 0 {
		return errors.New("invalid manual assignment")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = s.db.ExecContext(ctx, `
		DELETE FROM mmwxc_connection_assignments
		WHERE server_id = $1 AND inbound_tag = $2 AND management_username = $3 AND protocol_identity = $4 AND source = 'manual'`,
		parsed, relation.InboundTag, relation.ManagementUsername, relation.ProtocolIdentity)
	return err
}

func extractProtocolIdentity(raw string) string {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return ""
	}
	var find func(any) string
	find = func(current any) string {
		switch typed := current.(type) {
		case map[string]any:
			for key, value := range typed {
				if strings.EqualFold(key, "email") {
					if identity, ok := value.(string); ok {
						return strings.TrimSpace(identity)
					}
				}
			}
			for _, value := range typed {
				if identity := find(value); identity != "" {
					return identity
				}
			}
		case []any:
			for _, value := range typed {
				if identity := find(value); identity != "" {
					return identity
				}
			}
		}
		return ""
	}
	return find(value)
}

type protocolCredential struct {
	Identity string
	Secrets  map[string]struct{}
}

func extractCoreInboundCredentials(raw string) map[string][]protocolCredential {
	result := make(map[string][]protocolCredential)
	var document map[string]any
	if json.Unmarshal([]byte(raw), &document) != nil {
		return result
	}
	inbounds, _ := document["inbounds"].([]any)
	for _, item := range inbounds {
		inbound, _ := item.(map[string]any)
		tag, _ := inbound["tag"].(string)
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		var walk func(any)
		walk = func(current any) {
			switch typed := current.(type) {
			case map[string]any:
				identity := ""
				for key, value := range typed {
					if strings.EqualFold(key, "email") {
						identity, _ = value.(string)
						identity = strings.TrimSpace(identity)
					}
				}
				if identity != "" {
					result[tag] = append(result[tag], protocolCredential{Identity: identity, Secrets: credentialSecrets(typed)})
				}
				for _, value := range typed {
					walk(value)
				}
			case []any:
				for _, value := range typed {
					walk(value)
				}
			}
		}
		walk(inbound["settings"])
	}
	return result
}

func matchNodeProtocolIdentities(rawValues []string, candidates []protocolCredential) []string {
	nodeSecrets := make(map[string]struct{})
	for _, raw := range rawValues {
		var value any
		if json.Unmarshal([]byte(raw), &value) == nil {
			for secret := range credentialSecrets(value) {
				nodeSecrets[secret] = struct{}{}
			}
		}
		if parsed, err := url.Parse(strings.TrimSpace(raw)); err == nil && parsed.Scheme != "" {
			if parsed.User != nil {
				if value := strings.TrimSpace(parsed.User.Username()); len(value) >= 4 {
					nodeSecrets[value] = struct{}{}
				}
				if value, ok := parsed.User.Password(); ok && len(strings.TrimSpace(value)) >= 4 {
					nodeSecrets[strings.TrimSpace(value)] = struct{}{}
				}
			}
			for key, values := range parsed.Query() {
				if !credentialKey(key) {
					continue
				}
				for _, value := range values {
					if value = strings.TrimSpace(value); len(value) >= 4 {
						nodeSecrets[value] = struct{}{}
					}
				}
			}
		}
	}
	identities := []string{}
	for _, candidate := range candidates {
		matched := false
		for secret := range candidate.Secrets {
			if _, exists := nodeSecrets[secret]; exists {
				matched = true
				break
			}
		}
		if matched {
			identities = appendUniqueString(identities, candidate.Identity)
		}
	}
	sort.Strings(identities)
	return identities
}

func credentialSecrets(value any) map[string]struct{} {
	result := make(map[string]struct{})
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, value := range typed {
				if credentialKey(key) {
					if text, ok := value.(string); ok {
						text = strings.TrimSpace(text)
						if len(text) >= 4 {
							result[text] = struct{}{}
						}
					}
				}
				walk(value)
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		}
	}
	walk(value)
	return result
}

func credentialKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "id", "uuid", "password", "pass", "secret", "psk", "token", "key", "private_key", "public_key":
		return true
	default:
		return false
	}
}
