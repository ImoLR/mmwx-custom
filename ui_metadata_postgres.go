package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const uiMetadataSchema = `
CREATE TABLE IF NOT EXISTS mmwxc_ui_preferences (
    username TEXT NOT NULL,
    preference_key TEXT NOT NULL,
    data_json JSONB NOT NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (username, preference_key),
    FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS mmwxc_routing_rule_presets (
    id BIGSERIAL PRIMARY KEY,
    username TEXT NOT NULL,
    name TEXT NOT NULL,
    rule_json JSONB NOT NULL,
    rule_hash CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (username, rule_hash),
    FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_mmwxc_routing_rule_presets_username_updated
    ON mmwxc_routing_rule_presets(username, updated_at DESC, id DESC);
`

func (s *postgresAdminSessionStore) EnsureUIMetadataSchema(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, uiMetadataSchema); err != nil {
		return fmt.Errorf("migrate Custom UI metadata: %w", err)
	}
	return nil
}

func (s *postgresAdminSessionStore) AdminUsername(ctx context.Context, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 4096 {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var username string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.username
		FROM sessions s
		JOIN users u ON u.username = s.username
		WHERE s.token = $1 AND s.expires_at > CURRENT_TIMESTAMP AND u.role = 'admin' AND u.is_active = 1
		LIMIT 1`, token).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return username, err
}

func (s *postgresAdminSessionStore) GetUIPreference(ctx context.Context, username, key string) (uiPreferenceRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var record uiPreferenceRecord
	err := s.db.QueryRowContext(ctx, `SELECT data_json, revision, updated_at FROM mmwxc_ui_preferences WHERE username = $1 AND preference_key = $2`, username, key).
		Scan(&record.Data, &record.Revision, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return uiPreferenceRecord{Data: json.RawMessage(`{}`)}, nil
	}
	if err != nil {
		return uiPreferenceRecord{}, err
	}
	record.Exists = true
	return record, nil
}

func (s *postgresAdminSessionStore) PutUIPreference(ctx context.Context, username, key string, data json.RawMessage, onlyIfEmpty bool) (uiPreferenceRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var record uiPreferenceRecord
	if onlyIfEmpty {
		err := s.db.QueryRowContext(ctx, `
			INSERT INTO mmwxc_ui_preferences (username, preference_key, data_json)
			VALUES ($1, $2, $3::jsonb)
			ON CONFLICT (username, preference_key) DO NOTHING
			RETURNING data_json, revision, updated_at`, username, key, string(data)).Scan(&record.Data, &record.Revision, &record.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return uiPreferenceRecord{}, errUIMetadataConflict
		}
		if err != nil {
			return uiPreferenceRecord{}, err
		}
	} else {
		err := s.db.QueryRowContext(ctx, `
			INSERT INTO mmwxc_ui_preferences (username, preference_key, data_json)
			VALUES ($1, $2, $3::jsonb)
			ON CONFLICT (username, preference_key) DO UPDATE
			SET data_json = EXCLUDED.data_json, revision = mmwxc_ui_preferences.revision + 1, updated_at = CURRENT_TIMESTAMP
			RETURNING data_json, revision, updated_at`, username, key, string(data)).Scan(&record.Data, &record.Revision, &record.UpdatedAt)
		if err != nil {
			return uiPreferenceRecord{}, err
		}
	}
	record.Exists = true
	return record, nil
}

func scanRoutingRulePreset(scanner interface{ Scan(...any) error }) (routingRulePreset, error) {
	var preset routingRulePreset
	var raw []byte
	if err := scanner.Scan(&preset.ID, &preset.Name, &raw, &preset.CreatedAt, &preset.UpdatedAt); err != nil {
		return routingRulePreset{}, err
	}
	if err := json.Unmarshal(raw, &preset.Rule); err != nil {
		return routingRulePreset{}, err
	}
	return preset, nil
}

func (s *postgresAdminSessionStore) ListRoutingRulePresets(ctx context.Context, username string) ([]routingRulePreset, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, rule_json, created_at, updated_at
		FROM mmwxc_routing_rule_presets
		WHERE username = $1 ORDER BY updated_at DESC, id DESC LIMIT $2`, username, maxRoutingRulePresets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	presets := []routingRulePreset{}
	for rows.Next() {
		preset, err := scanRoutingRulePreset(rows)
		if err != nil {
			return nil, err
		}
		presets = append(presets, preset)
	}
	return presets, rows.Err()
}

func (s *postgresAdminSessionStore) UpsertRoutingRulePreset(ctx context.Context, username, name string, rule map[string]any) (routingRulePreset, error) {
	data, hash, err := canonicalRoutingRule(rule)
	if err != nil {
		return routingRulePreset{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return routingRulePreset{}, err
	}
	defer tx.Rollback()
	preset, err := scanRoutingRulePreset(tx.QueryRowContext(ctx, `
		INSERT INTO mmwxc_routing_rule_presets (username, name, rule_json, rule_hash)
		VALUES ($1, $2, $3::jsonb, $4)
		ON CONFLICT (username, rule_hash) DO UPDATE
		SET name = EXCLUDED.name, rule_json = EXCLUDED.rule_json, updated_at = CURRENT_TIMESTAMP
		RETURNING id, name, rule_json, created_at, updated_at`, username, name, string(data), hash))
	if err != nil {
		return routingRulePreset{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM mmwxc_routing_rule_presets
		WHERE username = $1 AND id NOT IN (
			SELECT id FROM mmwxc_routing_rule_presets WHERE username = $1
			ORDER BY updated_at DESC, id DESC LIMIT $2
		)`, username, maxRoutingRulePresets); err != nil {
		return routingRulePreset{}, err
	}
	if err := tx.Commit(); err != nil {
		return routingRulePreset{}, err
	}
	return preset, nil
}

func (s *postgresAdminSessionStore) DeleteRoutingRulePreset(ctx context.Context, username string, id int64) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, `DELETE FROM mmwxc_routing_rule_presets WHERE username = $1 AND id = $2`, username, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return sql.ErrNoRows
	}
	return nil
}
