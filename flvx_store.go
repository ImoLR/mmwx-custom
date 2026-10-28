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
	"unicode"

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
	// Share policy 「允许自定义出站与路由」, default on. Custom-only: it is
	// enforced by rejecting/withholding plans and never reaches the Helper.
	EgressDenied bool `json:"egress_denied,omitempty"`
	// The FLVX Core build this share's Helper installs; see flvxPinnedCore.
	Core *flvxPinnedCore `json:"core_artifact,omitempty"`
}

// flvxPinnedCore is pinned at the share's first Helper payload and moves to a
// newer release only on the admin's 「升级 FLVX Core」. Publishing a release
// therefore never replaces (and restarts) the FLVX Core of an existing share.
type flvxPinnedCore struct {
	Version   string                     `json:"version"`
	Artifacts map[string]releaseArtifact `json:"artifacts"`
}

func flvxReleaseCore(info cachedReleaseInfo) *flvxPinnedCore {
	if info.CoreVersion == "" || len(info.CoreArtifacts) == 0 {
		return nil
	}
	artifacts := map[string]releaseArtifact{}
	for arch, artifact := range info.CoreArtifacts {
		artifacts[arch] = artifact
	}
	return &flvxPinnedCore{Version: info.CoreVersion, Artifacts: artifacts}
}

// flvxShareProtocols is what the share may carry of landing-protocols-v1: the
// last Helper report's capability, while that Helper renders the protocols.
func flvxShareProtocols(s flvxShare, helper flvxHelper) map[string]flvx.ProtocolCap {
	if !helper.Ready || !helper.Protocols {
		return nil
	}
	return flvx.SanitizeProtocols(s.Status.Protocols)
}

// flvxOwnPorts are the ports of the share's current nodes; they are FLVX's
// own and never busy.
func flvxOwnPorts(s flvxShare) map[int]bool {
	own := map[int]bool{}
	for _, n := range s.Desired.Nodes {
		own[n.Port] = true
	}
	return own
}

// flvxBusyListening is the Helper-reported listening set without the share's
// own node ports, for validating a new desired state; nil when unknown.
func flvxBusyListening(s flvxShare) *flvx.ListeningPorts {
	l := s.Status.Listening
	if l == nil {
		return nil
	}
	own := flvxOwnPorts(s)
	return &flvx.ListeningPorts{TCP: flvx.BusyPorts(1, 65535, own, l.TCP), UDP: flvx.BusyPorts(1, 65535, own, l.UDP)}
}

// flvxBusyPorts is the share list's busy_ports: official inbound ports and
// whatever else listens on the machine, inside the share range.
func flvxBusyPorts(s flvxShare, server flvxServer) []int {
	lists := [][]int{}
	if server.PortsKnown {
		lists = append(lists, server.OfficialPorts)
	}
	if l := s.Status.Listening; l != nil {
		lists = append(lists, l.TCP, l.UDP)
	}
	if len(lists) == 0 {
		return nil
	}
	return flvx.BusyPorts(s.Config.PortMin, s.Config.PortMax, flvxOwnPorts(s), lists...)
}

type flvxLedger struct {
	Counter flvx.Counter    `json:"counter"`
	Status  flvx.NodeStatus `json:"status"`
}

// Online IPs are transient observations, never part of the durable traffic ledger.
type flvxOnlineSnapshot struct {
	Generation  string
	Sequence    int64
	CollectedAt int64
	Nodes       map[string]flvx.NodeStatus
}

// Called after the report transaction commits.
func (a *app) cacheFLVXOnlineIPs(id int64, r flvx.Report) {
	a.flvxOnlineMu.Lock()
	defer a.flvxOnlineMu.Unlock()
	now := time.Now().UnixMilli()
	for serverID, snapshot := range a.flvxOnline {
		if now-snapshot.CollectedAt > 60000 {
			delete(a.flvxOnline, serverID)
		}
	}
	delete(a.flvxOnline, id)
	if r.Core.State != "running" || r.CollectedAt <= 0 || now-r.CollectedAt > 60000 {
		return
	}
	snapshot := flvxOnlineSnapshot{Generation: r.Generation, Sequence: r.Sequence, CollectedAt: r.CollectedAt, Nodes: map[string]flvx.NodeStatus{}}
	for _, n := range r.Nodes {
		if n.State != "active" || n.OnlineIPCount == nil || *n.OnlineIPCount < 0 {
			continue
		}
		count := *n.OnlineIPCount
		ips := []flvx.OnlineIP{}
		if n.OnlineIPs != nil {
			ips = append(ips, (*n.OnlineIPs)...)
		}
		sort.Slice(ips, func(i, j int) bool {
			if ips[i].Connections != ips[j].Connections {
				return ips[i].Connections > ips[j].Connections
			}
			return ips[i].IP < ips[j].IP
		})
		if len(ips) > 64 {
			ips = append([]flvx.OnlineIP(nil), ips[:64]...)
		}
		snapshot.Nodes[n.ID] = flvx.NodeStatus{OnlineIPCount: &count, OnlineIPs: &ips}
	}
	if len(snapshot.Nodes) > 0 {
		if a.flvxOnline == nil {
			a.flvxOnline = map[int64]flvxOnlineSnapshot{}
		}
		a.flvxOnline[id] = snapshot
	}
}

func (a *app) flvxStatusWithOnlineIPs(id int64, s flvxShare) flvx.Status {
	a.flvxOnlineMu.Lock()
	defer a.flvxOnlineMu.Unlock()
	status := s.Status
	status.Nodes = append([]flvx.NodeStatus{}, status.Nodes...)
	snapshot, ok := a.flvxOnline[id]
	fresh := ok && snapshot.Generation == s.Generation && snapshot.Sequence == s.Sequence &&
		snapshot.CollectedAt == status.CollectedAt && time.Now().UnixMilli()-snapshot.CollectedAt <= 60000 &&
		status.Core.State == "running"
	for i := range status.Nodes {
		n := &status.Nodes[i]
		n.OnlineIPCount, n.OnlineIPs = nil, nil
		if online, exists := snapshot.Nodes[n.ID]; fresh && exists && n.State == "active" {
			count := *online.OnlineIPCount
			ips := append([]flvx.OnlineIP{}, (*online.OnlineIPs)...)
			n.OnlineIPCount, n.OnlineIPs = &count, &ips
		}
	}
	return status
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
	Helper         flvxHelper `json:"helper"`
	// Official remote_servers.xray_mode when embedded / external (information only).
	Mode string `json:"server_mode,omitempty"`
}

// flvxHelper is the server's last Helper report as FLVX sharing sees it. Only
// Helpers advertising "flvx.share" (v0.7.5+) run the FLVX Core; older ones
// ignore the share, so its desired nodes would never apply.
type flvxHelper struct {
	Installed    bool   `json:"installed"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Ready        bool   `json:"flvx_ready"`
	Upgrading    bool   `json:"upgrading"`
	// Helper renders landing-protocols-v1 nodes ("flvx.protocols.v1").
	Protocols bool `json:"flvx_protocols"`
	// Helper renders the Reality guard, landing-reality-guard-v1
	// ("flvx.reality_guard.v1").
	RealityGuard bool `json:"flvx_reality_guard"`
	// Helper groups the FLVX Core per user (landing-user-limits-v1,
	// "flvx.user_limits.v1") and polices per-user 速率 ("flvx.user_rate.v1").
	UserLimits bool `json:"flvx_user_limits"`
	UserRate   bool `json:"flvx_user_rate"`
}

func (a *app) flvxHelpers() map[int64]flvxHelper {
	result := map[int64]flvxHelper{}
	if a.helperState == nil {
		return result
	}
	s, now := a.helperState, time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, status := range s.data.AgentStatuses {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			continue
		}
		installed := status.Helper.Installed && s.data.Servers[id].HelperTokenHash != ""
		result[n] = flvxHelper{Installed: installed, Version: status.Helper.Version, Architecture: status.Architecture, Ready: installed && hasCapability(status, "flvx.share"), Upgrading: s.upgradeInProgressLocked(id, now) || s.batchServerBusyLocked(id), Protocols: installed && hasCapability(status, "flvx.protocols.v1"), RealityGuard: installed && hasCapability(status, "flvx.reality_guard.v1"), UserLimits: installed && hasCapability(status, flvx.UserLimitsCapability), UserRate: installed && hasCapability(status, flvx.UserRateCapability)}
	}
	return result
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
		s := flvxServer{ID: deletionID(data["id"]), Name: deletionText(data["name"]), Hosts: flvxLinkHosts(data)}
		if mode := deletionText(data["xray_mode"]); mode == "embedded" || mode == "external" {
			s.Mode = mode
		}
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

// flvxLinkHosts gives FLVX the address official node links use: 服务器域名
// (domain), else 服务器地址 (pull_address: the DDNS name or a NAT host's entry
// IP), else the reported IP. FLVX prefers Domain, so it stays empty when the
// link address is an IP literal; ip_address is then only the IPv4 fallback.
func flvxLinkHosts(data map[string]any) flvx.Hosts {
	h := flvx.Hosts{}
	for i, key := range []string{"domain", "pull_address", "ip_address", "domain_v6", "pull_address_v6", "ip_address_v6"} {
		v := strings.TrimSpace(deletionText(data[key]))
		ip := net.ParseIP(v)
		switch {
		case v == "":
			continue
		case ip == nil:
			if h.Domain == "" && h.IPv4 == "" && i < 3 {
				h.Domain = v
			}
		case ip.To4() != nil:
			if h.IPv4 == "" && i < 3 {
				h.IPv4 = ip.String()
			}
		case h.IPv6 == "":
			h.IPv6 = ip.String()
		}
	}
	return h
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

// flvxSanitizeCoreError bounds the Helper's reason it cannot start the shared
// Core (one line, at most 300 characters); it is shown to the operator only.
func flvxSanitizeCoreError(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, ""))
	if runes := []rune(strings.TrimSpace(text)); len(runes) > 300 {
		return string(runes[:300])
	}
	return strings.TrimSpace(text)
}

func mergeFLVXReport(ctx context.Context, tx *sql.Tx, id int64, s *flvxShare, r flvx.Report, now time.Time) error {
	if r.Generation == "" || len(r.Generation) > 64 || r.Sequence < 1 || r.AppliedRevision > s.Desired.Revision || !r.Egress.Valid() || r.Egress != nil && r.Egress.AppliedRevision > s.Desired.Revision || !r.Listening.Valid() || !r.RealityGuard.Valid() {
		return errors.New("invalid FLVX report")
	}
	r.Protocols = flvx.SanitizeProtocols(r.Protocols)
	r.Core.Error = flvxSanitizeCoreError(r.Core.Error)
	if r.RealityGuard != nil {
		guard := *r.RealityGuard
		guard.Error = flvxSanitizeCoreError(guard.Error)
		r.RealityGuard = &guard
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
	if len(s.Retired) > 32 {
		s.Retired = s.Retired[len(s.Retired)-32:]
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
		n.OnlineIPCount, n.OnlineIPs = nil, nil
		if n.InboundConnections != nil && *n.InboundConnections < 0 {
			n.InboundConnections = nil
		}
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
			if l.Status.InboundConnections != nil {
				zero := int64(0)
				l.Status.InboundConnections = &zero
			}
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
