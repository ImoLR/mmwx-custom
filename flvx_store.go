package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

const flvxSchema = `
CREATE TABLE IF NOT EXISTS mmwxc_flvx_integration (
 id INTEGER PRIMARY KEY CHECK(id=1), token_hash TEXT NOT NULL DEFAULT '',
 last_api_at BIGINT NOT NULL DEFAULT 0,
 landing_hosts JSONB NOT NULL DEFAULT '{"native_landing_hosts":[],"flvx_node_hosts":[]}',
 hosts_updated_at BIGINT NOT NULL DEFAULT 0
);
INSERT INTO mmwxc_flvx_integration(id) VALUES(1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS mmwxc_flvx_shares (
 server_id BIGINT PRIMARY KEY, data_json JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS mmwxc_flvx_counters (
 server_id BIGINT NOT NULL REFERENCES mmwxc_flvx_shares(server_id) ON DELETE CASCADE,
 node_id TEXT NOT NULL, data_json JSONB NOT NULL,
 PRIMARY KEY(server_id,node_id)
);`

type flvxShare struct {
	Config     flvx.Config  `json:"config"`
	State      string       `json:"state"`
	Version    int64        `json:"version"`
	Desired    flvx.Desired `json:"desired"`
	Status     flvx.Status  `json:"status"`
	Period     string       `json:"period"`
	Generation string       `json:"generation"`
	Sequence   int64        `json:"sequence"`
	Retired    []string     `json:"retired,omitempty"`
}

type flvxLedger struct {
	Counter flvx.Counter    `json:"counter"`
	Status  flvx.NodeStatus `json:"status"`
}

type flvxServer struct {
	ID             int64      `json:"server_id"`
	Name           string     `json:"name"`
	Hosts          flvx.Hosts `json:"hosts"`
	Addresses      []string   `json:"-"`
	OfficialPorts  []int      `json:"official_ports"`
	PortsKnown     bool       `json:"ports_known"`
	DualInstall    bool       `json:"dual_install"`
	NativeLanding  bool       `json:"native_landing"`
	DetectionError bool       `json:"detection_error"`
}

type flvxQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (a *app) flvxDB() *sql.DB {
	if s, ok := a.adminStore.(*postgresAdminSessionStore); ok {
		return s.db
	}
	return nil
}

func readFLVXShare(ctx context.Context, q flvxQuery, id int64, lock bool) (flvxShare, error) {
	query := `SELECT data_json FROM mmwxc_flvx_shares WHERE server_id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	var raw []byte
	if err := q.QueryRowContext(ctx, query, id).Scan(&raw); err != nil {
		return flvxShare{}, err
	}
	var s flvxShare
	err := json.Unmarshal(raw, &s)
	if s.Status.Nodes == nil {
		s.Status.Nodes = []flvx.NodeStatus{}
	}
	return s, err
}

func writeFLVXShare(ctx context.Context, tx *sql.Tx, id int64, s flvxShare) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mmwxc_flvx_shares(server_id,data_json) VALUES($1,$2) ON CONFLICT(server_id) DO UPDATE SET data_json=EXCLUDED.data_json,updated_at=CURRENT_TIMESTAMP`, id, string(raw))
	return err
}

func flvxTransaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serializes host reports, revisions, admin edits and counter merges, including
	// multiple Custom processes sharing the same official PostgreSQL database.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1718188140)`); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func readFLVXHosts(ctx context.Context, q flvxQuery) (flvx.LandingHosts, error) {
	var raw []byte
	var h flvx.LandingHosts
	err := q.QueryRowContext(ctx, `SELECT landing_hosts FROM mmwxc_flvx_integration WHERE id=1`).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &h)
	}
	return h, err
}

func flvxServers(ctx context.Context, q flvxQuery) ([]flvxServer, error) {
	rows, err := q.QueryContext(ctx, `SELECT to_jsonb(s) FROM remote_servers s ORDER BY id`)
	if err != nil {
		return nil, err
	}
	servers := []flvxServer{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		var data map[string]any
		if err = json.Unmarshal(raw, &data); err != nil {
			rows.Close()
			return nil, err
		}
		s := flvxServer{ID: deletionID(data["id"]), Name: deletionText(data["name"]), Hosts: flvx.Hosts{IPv4: deletionText(data["ip_address"]), IPv6: deletionText(data["ip_address_v6"]), Domain: deletionText(data["domain"])}}
		for h := range deletionServerHosts(data) {
			s.Addresses = append(s.Addresses, h)
		}
		servers = append(servers, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range servers {
		var raw []byte
		err = q.QueryRowContext(ctx, `SELECT config_json FROM server_xray_config_snapshots WHERE server_id=$1 AND status='current' ORDER BY created_at DESC,id DESC LIMIT 1`, servers[i].ID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		servers[i].OfficialPorts, err = flvx.OfficialPorts(raw)
		if err != nil {
			return nil, err
		}
		servers[i].PortsKnown = true
	}
	return servers, nil
}

func flvxServerSnapshot(ctx context.Context, q flvxQuery, id int64) (flvxServer, error) {
	s := flvxServer{ID: id}
	var raw sql.NullString
	err := q.QueryRowContext(ctx, `SELECT (SELECT config_json FROM server_xray_config_snapshots WHERE server_id=s.id AND status='current' ORDER BY created_at DESC,id DESC LIMIT 1) FROM remote_servers s WHERE id=$1`, id).Scan(&raw)
	if err != nil || !raw.Valid {
		return s, err
	}
	s.OfficialPorts, err = flvx.OfficialPorts([]byte(raw.String))
	s.PortsKnown = err == nil
	return s, err
}

type flvxLookup func(context.Context, string) ([]string, error)

func flvxHostSet(ctx context.Context, hosts []string, lookup flvxLookup) (map[string]bool, error) {
	set := map[string]bool{}
	for _, raw := range hosts {
		h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if !flvx.ValidHost(h) {
			return nil, errors.New("invalid host")
		}
		if ip := net.ParseIP(h); ip != nil {
			set[ip.String()] = true
			continue
		}
		set[h] = true
		ips, err := lookup(ctx, h)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("hostname resolution failed")
		}
		for _, ip := range ips {
			if p := net.ParseIP(ip); p != nil {
				set[p.String()] = true
			}
		}
	}
	return set, nil
}

func detectFLVXDual(ctx context.Context, s *flvxServer, h flvx.LandingHosts, lookup flvxLookup) {
	sets := []map[string]bool{}
	for _, hosts := range [][]string{s.Addresses, h.Native, h.Nodes} {
		set, err := flvxHostSet(ctx, hosts, lookup)
		if err != nil {
			s.DetectionError = true
			return
		}
		sets = append(sets, set)
	}
	for host := range sets[0] {
		s.NativeLanding = s.NativeLanding || sets[1][host]
		s.DualInstall = s.DualInstall || sets[2][host]
	}
}

func flvxReset(s *flvxShare, now time.Time) {
	period := flvx.Period(now, s.Config.ResetDay)
	if s.Period != period {
		s.Period = period
		s.Status.TrafficUsedBytes = 0
	}
}

func mergeFLVXReport(ctx context.Context, tx *sql.Tx, id int64, s *flvxShare, r flvx.Report, now time.Time) error {
	if r.Generation == "" || len(r.Generation) > 64 || r.Sequence < 1 || r.AppliedRevision > s.Desired.Revision {
		return errors.New("invalid FLVX report")
	}
	if r.Generation == s.Generation && r.Sequence <= s.Sequence {
		return nil
	}
	for _, retired := range s.Retired {
		if r.Generation == retired {
			return nil
		}
	}
	if r.Generation != s.Generation && s.Generation != "" {
		s.Retired = append(s.Retired, s.Generation)
	}
	flvxReset(s, now)
	seen := map[string]bool{}
	for _, n := range r.Nodes {
		if !flvx.ValidText(n.ID, 64) || seen[n.ID] || n.UpBytes < 0 || n.DownBytes < 0 || n.Connections < 0 || (n.State != "active" && n.State != "error" && n.State != "removed") {
			return errors.New("invalid FLVX node report")
		}
		seen[n.ID] = true
		var raw []byte
		var ledger flvxLedger
		err := tx.QueryRowContext(ctx, `SELECT data_json FROM mmwxc_flvx_counters WHERE server_id=$1 AND node_id=$2`, id, n.ID).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &ledger); err != nil {
				return err
			}
		}
		delta := ledger.Counter.Merge(r.Generation, n.UpBytes, n.DownBytes)
		if r.Period == s.Period {
			s.Status.TrafficUsedBytes = flvx.AddBytes(s.Status.TrafficUsedBytes, delta)
		}
		n.UpBytes, n.DownBytes = ledger.Counter.Up, ledger.Counter.Down
		ledger.Status = n
		raw, _ = json.Marshal(ledger)
		if _, err = tx.ExecContext(ctx, `INSERT INTO mmwxc_flvx_counters(server_id,node_id,data_json) VALUES($1,$2,$3) ON CONFLICT(server_id,node_id) DO UPDATE SET data_json=EXCLUDED.data_json`, id, n.ID, string(raw)); err != nil {
			return err
		}
	}
	used := s.Status.TrafficUsedBytes
	// Helper independently enforces the cycle quota while the controller is down.
	if r.Period == s.Period && r.TrafficUsedBytes > used {
		used = r.TrafficUsedBytes
	}
	s.Status = r.Status
	s.Status.TrafficUsedBytes = used
	s.Status.Nodes = []flvx.NodeStatus{}
	s.Generation, s.Sequence = r.Generation, r.Sequence
	rows, err := tx.QueryContext(ctx, `SELECT data_json FROM mmwxc_flvx_counters WHERE server_id=$1 ORDER BY node_id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var l flvxLedger
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &l); err != nil {
			return err
		}
		if !seen[l.Status.ID] {
			l.Status.State = "removed"
			l.Status.Connections = 0
		}
		s.Status.Nodes = append(s.Status.Nodes, l.Status)
	}
	return rows.Err()
}

func flvxServerByID(servers []flvxServer, id int64) (flvxServer, error) {
	for _, s := range servers {
		if s.ID == id {
			return s, nil
		}
	}
	return flvxServer{}, sql.ErrNoRows
}

func flvxSortedPorts(ports []int) []int { sort.Ints(ports); return ports }
func flvxID(id int64) string            { return strconv.FormatInt(id, 10) }
