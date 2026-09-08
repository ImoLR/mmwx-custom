package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultAdminDatabaseConfigPath = "/etc/mmwx/data/database.json"

var (
	errOperatorAuthorizationMissing     = errors.New("missing operator authorization")
	errOperatorAuthorizationInvalid     = errors.New("operator authorization failed")
	errOperatorAuthorizationUnavailable = errors.New("operator authorization unavailable")
	errOperatorServerNotFound           = errors.New("remote server not found")
)

type adminSessionStore interface {
	AuthorizeAdmin(context.Context, string) (bool, error)
	RemoteServerExists(context.Context, string) (bool, error)
	Close() error
}

type postgresAdminSessionStore struct {
	db *sql.DB
}

type adminDatabaseConfig struct {
	Driver   string `json:"driver"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	SSLMode  string `json:"ssl_mode"`
}

func openPostgresAdminSessionStore(configPath string) (*postgresAdminSessionStore, error) {
	data, err := os.ReadFile(strings.TrimSpace(configPath))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, errors.New("admin database config is too large")
	}
	var config adminDatabaseConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, errors.New("invalid admin database config")
	}
	if config.Driver != "postgres" || config.Database == "" || config.Username == "" || config.Password == "" || config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("incomplete admin database config")
	}
	host := strings.TrimSpace(config.Host)
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return nil, errors.New("admin database must use a loopback host")
	}
	sslMode := strings.TrimSpace(config.SSLMode)
	if sslMode == "" {
		sslMode = "disable"
	}
	dsn := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.Username, config.Password),
		Host:   net.JoinHostPort(host, strconv.Itoa(config.Port)),
		Path:   config.Database,
	}
	query := dsn.Query()
	query.Set("sslmode", sslMode)
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("pgx", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect admin database: %w", err)
	}
	return &postgresAdminSessionStore{db: db}, nil
}

func (s *postgresAdminSessionStore) AuthorizeAdmin(ctx context.Context, token string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 4096 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var authorized bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM sessions AS s
			JOIN users AS u ON u.username = s.username
			WHERE s.token = $1
			  AND s.expires_at > CURRENT_TIMESTAMP
			  AND u.role = 'admin'
			  AND u.is_active = 1
		)`, token).Scan(&authorized)
	return authorized, err
}

func (s *postgresAdminSessionStore) RemoteServerExists(ctx context.Context, serverID string) (bool, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(serverID), 10, 64)
	if err != nil || parsed <= 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var exists bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM remote_servers WHERE id = $1)`, parsed).Scan(&exists)
	return exists, err
}

func (s *postgresAdminSessionStore) Close() error {
	return s.db.Close()
}

func (a *app) authorizeOperatorRequest(r *http.Request) error {
	if a.apiToken != "" && a.authorized(r) {
		return nil
	}
	token := strings.TrimSpace(r.Header.Get("MM-Authorization"))
	if token == "" {
		return errOperatorAuthorizationMissing
	}
	if a.adminStore == nil {
		return errOperatorAuthorizationUnavailable
	}
	authorized, err := a.adminStore.AuthorizeAdmin(r.Context(), token)
	if err != nil {
		return errOperatorAuthorizationUnavailable
	}
	if !authorized {
		return errOperatorAuthorizationInvalid
	}
	return nil
}

func (a *app) authorizeOperatorServerRequest(r *http.Request, serverID string) error {
	if err := a.authorizeOperatorRequest(r); err != nil {
		return err
	}
	if a.apiToken != "" && a.authorized(r) {
		return nil
	}
	exists, err := a.adminStore.RemoteServerExists(r.Context(), serverID)
	if err != nil {
		return errOperatorAuthorizationUnavailable
	}
	if !exists {
		return errOperatorServerNotFound
	}
	return nil
}

func writeOperatorAuthorizationError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	if errors.Is(err, errOperatorAuthorizationUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, errOperatorServerNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]any{"success": false, "message": err.Error()})
}
