package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

type flvxAPIError struct {
	status        int
	code, message string
}

func (e flvxAPIError) Error() string { return e.message }

type flvxValidationErrors []flvx.ValidationError

func (e flvxValidationErrors) Error() string { return "invalid desired state" }

func writeFLVXError(w http.ResponseWriter, err error) {
	var validation flvxValidationErrors
	if errors.As(err, &validation) {
		writeJSON(w, 422, map[string]any{"errors": validation})
		return
	}
	var apiErr flvxAPIError
	if errors.Is(err, sql.ErrNoRows) {
		apiErr = flvxAPIError{404, "not_found", "share or server not found"}
	} else if !errors.As(err, &apiErr) {
		apiErr = flvxAPIError{503, "unavailable", "FLVX storage or server inventory unavailable"}
	}
	writeJSON(w, apiErr.status, map[string]any{"error": map[string]string{"code": apiErr.code, "message": apiErr.message}})
}

func decodeFLVX(w http.ResponseWriter, r *http.Request, target any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return flvxAPIError{400, "invalid_json", "invalid JSON or unknown field"}
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return flvxAPIError{400, "invalid_json", "expected one JSON object"}
	}
	return nil
}

func flvxTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *app) authorizeFLVX(r *http.Request) error {
	db := a.flvxDB()
	if db == nil {
		return flvxAPIError{503, "unavailable", "FLVX storage unavailable"}
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 256 {
		return flvxAPIError{401, "unauthorized", "invalid FLVX integration token"}
	}
	token := strings.TrimPrefix(header, "Bearer ")
	var hash string
	if err := db.QueryRowContext(r.Context(), `SELECT token_hash FROM mmwxc_flvx_integration WHERE id=1`).Scan(&hash); err != nil {
		return err
	}
	if !strings.HasPrefix(token, "flvx_") || hash == "" || subtle.ConstantTimeCompare([]byte(hash), []byte(flvxTokenHash(token))) != 1 {
		return flvxAPIError{401, "unauthorized", "invalid FLVX integration token"}
	}
	_, err := db.ExecContext(r.Context(), `UPDATE mmwxc_flvx_integration SET last_api_at=$1 WHERE id=1`, time.Now().UnixMilli())
	return err
}

func flvxResolver() flvxLookup {
	cache := map[string][]string{}
	return func(ctx context.Context, host string) ([]string, error) {
		if ips, ok := cache[host]; ok {
			return ips, nil
		}
		child, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupHost(child, host)
		if err == nil {
			cache[host] = ips
		}
		return ips, err
	}
}

func flvxCheckServer(ctx context.Context, q flvxQuery, id int64) (flvxServer, error) {
	servers, err := flvxServers(ctx, q)
	if err != nil {
		return flvxServer{}, err
	}
	s, err := flvxServerByID(servers, id)
	if err != nil {
		return s, err
	}
	h, err := readFLVXHosts(ctx, q)
	if err != nil {
		return s, err
	}
	detectFLVXDual(ctx, &s, h, flvxResolver())
	if s.NativeLanding {
		return s, flvxAPIError{409, "native_landing", "FLVX native landing already uses this server"}
	}
	if s.DetectionError {
		return s, flvxAPIError{409, "host_resolution", "cannot verify server addresses; retry after DNS recovers"}
	}
	if !s.PortsKnown {
		return s, flvxAPIError{409, "official_ports_unknown", "official inbound configuration is unavailable"}
	}
	return s, nil
}

func (a *app) flvxV1Handler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set("Cache-Control", "no-store")
	if err := a.authorizeFLVX(r); err != nil {
		writeFLVXError(w, err)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/custom/flvx/v1/")
	var result any
	var err error
	switch {
	case path == "shares" && r.Method == http.MethodGet:
		result, err = a.flvxList(r.Context(), false)
	case path == "landing-hosts" && r.Method == http.MethodPut:
		var h flvx.LandingHosts
		err = decodeFLVX(w, r, &h)
		if err == nil {
			err = a.putFLVXHosts(r.Context(), h)
		}
		result = map[string]any{}
	case strings.HasPrefix(path, "shares/"):
		parts := strings.Split(path, "/")
		if len(parts) != 3 {
			err = sql.ErrNoRows
			break
		}
		id, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil || id <= 0 {
			err = sql.ErrNoRows
			break
		}
		if parts[2] == "desired" && r.Method == http.MethodPut {
			var d flvx.Desired
			err = decodeFLVX(w, r, &d)
			if err != nil {
				err = flvxValidationErrors{{Field: "desired", Message: "invalid desired JSON or unknown field"}}
			}
			if err == nil {
				err = a.putFLVXDesired(r.Context(), id, d)
			}
			result = map[string]any{"accepted_revision": d.Revision}
		} else if parts[2] == "status" && r.Method == http.MethodGet {
			var s flvxShare
			s, err = readFLVXShare(r.Context(), a.flvxDB(), id, false)
			flvxReset(&s, time.Now())
			status := flvxPublicStatus(a.flvxStatusWithOnlineIPs(id, s), a.flvxHelpers()[id].Ready)
			if machine := a.flvxShareMachine(id); machine != nil {
				status["machine"] = machine
			}
			result = status
		} else {
			err = flvxAPIError{405, "method_not_allowed", "method not allowed"}
		}
	default:
		err = flvxAPIError{404, "not_found", "endpoint not found"}
	}
	if err != nil {
		writeFLVXError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func flvxPublicStatus(s flvx.Status, helperReady bool) map[string]any {
	state := s.Core.State
	if state == "" {
		state = "stopped"
	}
	if s.CollectedAt > 0 && time.Now().UnixMilli()-s.CollectedAt > 60000 && state == "running" {
		state = "error"
	}
	// An old Helper never reports FLVX, so its stored "stopped" would look idle.
	if !helperReady {
		state = "helper_outdated"
	}
	result := map[string]any{"applied_revision": s.AppliedRevision, "core": map[string]any{"state": state, "epoch": s.Core.Epoch}, "nodes": s.Nodes, "share_traffic_used_bytes": s.TrafficUsedBytes, "collected_at": s.CollectedAt}
	if e := s.Egress; e != nil {
		warnings := e.Warnings
		if warnings == nil {
			warnings = []string{}
		}
		result["egress"] = map[string]any{"state": e.State, "error": e.Error, "applied_revision": e.AppliedRevision, "warnings": warnings}
	}
	// landing-reality-guard-v1 §5. The reason text stays operator-only.
	if g := s.RealityGuard; g != nil {
		result["reality_guard"] = map[string]any{"state": g.State, "nodes": g.Nodes}
	}
	return result
}

// flvxGuardCapability is the landing-reality-guard-v1 advertisement (§1): only
// while the share's Helper renders the guard, so FLVX keeps sending today's
// payload otherwise.
func flvxGuardCapability(helper flvxHelper) map[string]any {
	if !helper.Ready || !helper.RealityGuard {
		return nil
	}
	return map[string]any{"versions": []int{flvx.RealityGuardVersion}}
}

// flvxGuardSummary tells the operator page whether FLVX asked for the guard,
// how many VLESS Reality nodes it covers and why it is not in force.
func flvxGuardSummary(s flvxShare) map[string]any {
	nodes := 0
	for _, n := range s.Desired.Nodes {
		if n.Protocol == "vless_reality_vision" {
			nodes++
		}
	}
	result := map[string]any{"requested": s.Desired.GuardOn(), "reality_nodes": nodes}
	if g := s.Status.RealityGuard; g != nil && g.Error != "" {
		result["error"] = g.Error
	}
	return result
}

// flvxEgressCapability is the landing-egress advertisement (contract §2). It
// is omitted until the share's Helper reports plan version 1 support, so FLVX
// keeps sending today's payload to old Helpers.
func flvxEgressCapability(s flvxShare) map[string]any {
	e := s.Status.Egress
	if !e.Supports(flvx.EgressVersion) {
		return nil
	}
	return map[string]any{"versions": []int{flvx.EgressVersion}, "allowed": !s.EgressDenied, "geoip": e.GeoIP, "geosite": e.GeoSite}
}

// flvxEgressProblems rejects plans the share policy or its Helper cannot
// take, then repeats every contract §4 check (FLVX is an untrusted tenant).
func flvxEgressProblems(s flvxShare, d flvx.Desired) []flvx.ValidationError {
	if d.Egress == nil {
		return nil
	}
	if s.EgressDenied {
		return []flvx.ValidationError{{Field: "egress", Message: "妙妙屋共享方未允许自定义出站与路由"}}
	}
	capability := s.Status.Egress
	if !capability.Supports(flvx.EgressVersion) {
		return []flvx.ValidationError{{Field: "egress", Message: "该共享服务器暂不支持自定义出站（Helper 版本过旧）"}}
	}
	tags := map[string]bool{}
	for _, n := range d.Nodes {
		tags[n.Tag()] = true
	}
	return flvx.ValidateEgress(d.Egress, tags, capability.GeoIP, capability.GeoSite)
}

// flvxEgressSummary describes the stored plan for the operator page without
// any upstream address or credential.
func flvxEgressSummary(s flvxShare) map[string]any {
	e := s.Desired.Egress
	types := []string{}
	seen := map[string]bool{}
	if e.Active() {
		for _, o := range e.Outbounds {
			if !seen[o.Type] {
				seen[o.Type] = true
				types = append(types, o.Type)
			}
		}
	}
	rules := 0
	if e.Active() {
		rules = len(e.Rules)
	}
	return map[string]any{"active": e.Active(), "outbounds": len(types), "outbound_types": types, "rules": rules, "covered_nodes": len(e.CoveredTags())}
}

func (a *app) putFLVXDesired(ctx context.Context, id int64, d flvx.Desired) error {
	helper := a.flvxHelpers()[id]
	return flvxTransaction(ctx, a.flvxDB(), func(tx *sql.Tx) error {
		s, err := readFLVXShare(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if s.State != "active" {
			return flvxAPIError{409, "share_inactive", "paused or revoked shares reject desired state"}
		}
		server, err := flvxCheckServer(ctx, tx, id)
		if err != nil {
			return err
		}
		options := flvx.DesiredOptions{Protocols: flvxShareProtocols(s, helper), Busy: flvxBusyListening(s), Now: time.Now()}
		problems := append(flvx.ValidateDesiredWith(d, s.Config, server.OfficialPorts, options), flvxEgressProblems(s, d)...)
		if len(problems) > 0 {
			return flvxValidationErrors(problems)
		}
		if d.Revision < s.Desired.Revision || d.Revision == s.Desired.Revision && !reflect.DeepEqual(d, s.Desired) {
			return flvxAPIError{409, "revision_conflict", "revision must increase; identical retries are idempotent"}
		}
		if d.Revision == s.Desired.Revision {
			return nil
		}
		s.Desired = d
		s.Version++
		return writeFLVXShare(ctx, tx, id, s)
	})
}

func (a *app) putFLVXHosts(ctx context.Context, h flvx.LandingHosts) error {
	if h.Native == nil || h.Nodes == nil || len(h.Native) > 4096 || len(h.Nodes) > 4096 {
		return flvxAPIError{422, "invalid_hosts", "both host arrays are required (4096 each maximum)"}
	}
	lookup := flvxResolver()
	for _, hosts := range [][]string{h.Native, h.Nodes} {
		if _, err := flvxHostSet(ctx, hosts, lookup); err != nil {
			return flvxAPIError{422, "invalid_hosts", "invalid or unresolved host"}
		}
	}
	return flvxTransaction(ctx, a.flvxDB(), func(tx *sql.Tx) error {
		raw, _ := json.Marshal(h)
		if _, err := tx.ExecContext(ctx, `UPDATE mmwxc_flvx_integration SET landing_hosts=$1,hosts_updated_at=$2 WHERE id=1`, string(raw), time.Now().UnixMilli()); err != nil {
			return err
		}
		servers, err := flvxServers(ctx, tx)
		if err != nil {
			return err
		}
		for _, server := range servers {
			detectFLVXDual(ctx, &server, h, lookup)
			if !server.NativeLanding {
				continue
			}
			s, e := readFLVXShare(ctx, tx, server.ID, true)
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				return e
			}
			if s.State == "active" {
				s.State = "paused"
				s.Version++
				if e = writeFLVXShare(ctx, tx, server.ID, s); e != nil {
					return e
				}
			}
		}
		return nil
	})
}

func (a *app) flvxAdminHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set("Cache-Control", "no-store")
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	if a.flvxDB() == nil {
		writeFLVXError(w, errors.New("storage unavailable"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/custom/flvx/admin")
	var result any
	var err error
	switch {
	case path == "" && r.Method == http.MethodGet:
		result, err = a.flvxList(r.Context(), true)
	case path == "/token" && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		token, hash := "", ""
		if r.Method == http.MethodPost {
			var b [32]byte
			if _, err = rand.Read(b[:]); err == nil {
				token = "flvx_" + base64.RawURLEncoding.EncodeToString(b[:])
				hash = flvxTokenHash(token)
			}
		}
		if err == nil {
			_, err = a.flvxDB().ExecContext(r.Context(), `UPDATE mmwxc_flvx_integration SET token_hash=$1,last_api_at=0 WHERE id=1`, hash)
		}
		result = map[string]string{"token": token}
	case strings.HasPrefix(path, "/shares/") && strings.HasSuffix(path, "/core") && r.Method == http.MethodPost:
		id, e := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, "/shares/"), "/core"), 10, 64)
		if e != nil || id <= 0 {
			err = sql.ErrNoRows
			break
		}
		var version string
		version, err = a.upgradeFLVXCore(r.Context(), id)
		result = map[string]any{"success": err == nil, "version": version}
	case strings.HasPrefix(path, "/shares/") && r.Method == http.MethodPut:
		id, e := strconv.ParseInt(strings.TrimPrefix(path, "/shares/"), 10, 64)
		if e != nil || id <= 0 {
			err = sql.ErrNoRows
			break
		}
		var input struct {
			flvx.Config
			Status      string `json:"status"`
			AllowEgress *bool  `json:"allow_egress"`
		}
		err = decodeFLVX(w, r, &input)
		if err == nil {
			err = a.saveFLVXShare(r.Context(), id, input.Config, input.Status, input.AllowEgress)
		}
		result = map[string]bool{"success": true}
	default:
		err = flvxAPIError{404, "not_found", "endpoint not found"}
	}
	if err != nil {
		writeFLVXError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// allowEgress nil keeps the current 「允许自定义出站与路由」 policy (default on).
func (a *app) saveFLVXShare(ctx context.Context, id int64, c flvx.Config, state string, allowEgress *bool) error {
	if state != "active" && state != "paused" && state != "revoked" {
		return flvxAPIError{422, "invalid_status", "invalid share status"}
	}
	helper := a.flvxHelpers()[id]
	return flvxTransaction(ctx, a.flvxDB(), func(tx *sql.Tx) error {
		s, err := readFLVXShare(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) {
			if state != "active" {
				return sql.ErrNoRows
			}
			if !helper.Ready {
				return flvxAPIError{409, "helper_outdated", "Helper 不支持 FLVX 共享，请先升级 Helper"}
			}
			s = flvxShare{Desired: flvx.Desired{Nodes: []flvx.Node{}}, Status: flvx.Status{Core: flvx.Core{State: "stopped"}, Nodes: []flvx.NodeStatus{}}, Version: 0}
		} else if err != nil {
			return err
		}
		if c.PortMin == 0 && s.Version > 0 {
			c = s.Config
		}
		if c.MemoryMB == 0 {
			c.MemoryMB = 256
		}
		if c.CPUPercent == 0 {
			c.CPUPercent = 50
		}
		if err = flvx.ValidateConfig(c); err != nil {
			return flvxAPIError{422, "invalid_config", err.Error()}
		}
		if s.State == "revoked" {
			if state != "revoked" {
				return flvxAPIError{409, "share_revoked", "cancelled shares cannot be resumed"}
			}
			return nil
		}
		if state == "paused" && s.Desired.Revision > 0 && (c.PortMin != s.Config.PortMin || c.PortMax != s.Config.PortMax) {
			if problems := flvx.ValidateDesiredWith(s.Desired, c, nil, flvx.DesiredOptions{Protocols: flvx.AllProtocols()}); len(problems) > 0 {
				return flvxValidationErrors(problems)
			}
		}
		if state == "active" {
			server, e := flvxCheckServer(ctx, tx, id)
			if e != nil {
				return e
			}
			if s.Desired.Revision > 0 {
				// The share's protocol capability gates FLVX and the Helper payload,
				// not the admin's settings: check ports and structure only.
				if problems := flvx.ValidateDesiredWith(s.Desired, c, server.OfficialPorts, flvx.DesiredOptions{Protocols: flvx.AllProtocols()}); len(problems) > 0 {
					return flvxValidationErrors(problems)
				}
			}
		}
		s.Config = c
		s.State = state
		if allowEgress != nil {
			s.EgressDenied = !*allowEgress
		}
		s.Version++
		flvxReset(&s, time.Now())
		return writeFLVXShare(ctx, tx, id, s)
	})
}

// upgradeFLVXCore moves the share's pinned FLVX Core to the latest release
// (「升级 FLVX Core」). Its Helper installs it and restarts the shared Core.
func (a *app) upgradeFLVXCore(ctx context.Context, id int64) (string, error) {
	var latest *flvxPinnedCore
	if a.releaseCache != nil {
		info, _ := a.releaseCache.snapshot()
		latest = flvxReleaseCore(info)
	}
	if latest == nil {
		return "", flvxAPIError{409, "release_unknown", "最新版本信息还未读取，请稍后重试"}
	}
	return latest.Version, flvxTransaction(ctx, a.flvxDB(), func(tx *sql.Tx) error {
		s, err := readFLVXShare(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if s.State == "revoked" {
			return flvxAPIError{409, "share_revoked", "cancelled shares cannot be changed"}
		}
		if s.Core != nil && reflect.DeepEqual(*s.Core, *latest) {
			return nil
		}
		s.Core = latest
		s.Version++
		return writeFLVXShare(ctx, tx, id, s)
	})
}

// flvxCorePinSummary tells the operator page which FLVX Core the share is
// pinned to and whether a newer release exists.
func flvxCorePinSummary(s flvxShare, latest string) map[string]any {
	pinned := ""
	if s.Core != nil {
		pinned = s.Core.Version
	}
	return map[string]any{"pinned": pinned, "latest": latest, "upgradable": pinned != "" && latest != "" && pinned != latest && s.State != "revoked"}
}

func (a *app) flvxList(ctx context.Context, admin bool) (map[string]any, error) {
	db := a.flvxDB()
	servers, err := flvxServers(ctx, db)
	if err != nil {
		return nil, err
	}
	h, err := readFLVXHosts(ctx, db)
	if err != nil {
		return nil, err
	}
	lookup, helpers := flvxResolver(), a.flvxHelpers()
	latestCore := ""
	if a.releaseCache != nil {
		info, _ := a.releaseCache.snapshot()
		latestCore = info.CoreVersion
	}
	for i := range servers {
		detectFLVXDual(ctx, &servers[i], h, lookup)
		flvxSortedPorts(servers[i].OfficialPorts)
		servers[i].Helper = helpers[servers[i].ID]
	}
	rows, err := db.QueryContext(ctx, `SELECT server_id,data_json FROM mmwxc_flvx_shares ORDER BY server_id`)
	if err != nil {
		return nil, err
	}
	shares := []any{}
	for rows.Next() {
		var id int64
		var raw []byte
		var s flvxShare
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &s); err != nil {
			rows.Close()
			return nil, err
		}
		flvxReset(&s, time.Now())
		server, _ := flvxServerByID(servers, id)
		connections := int64(0)
		for _, n := range s.Status.Nodes {
			connections += n.Connections
		}
		report := flvxPublicStatus(s.Status, server.Helper.Ready)
		coreState := report["core"].(map[string]any)["state"]
		item := map[string]any{"server_id": id, "name": server.Name, "hosts": server.Hosts, "status": s.State, "port_min": s.Config.PortMin, "port_max": s.Config.PortMax, "traffic_limit_bytes": s.Config.TrafficLimitBytes, "traffic_used_bytes": s.Status.TrafficUsedBytes, "reset_day": s.Config.ResetDay, "max_connections": s.Config.MaxConnections, "connections": connections, "share_until": s.Config.ShareUntil, "core": map[string]any{"state": coreState, "version": s.Status.Core.Version}, "protocols": []string{"ss2022", "vless_reality_vision"}, "dual_install": server.DualInstall}
		if capability := flvxEgressCapability(s); capability != nil {
			item["egress"] = capability
		}
		if capability := flvxGuardCapability(server.Helper); capability != nil {
			item["reality_guard"] = capability
		}
		// landing-protocols-v1 §2. protocols above stays the v1 list: older
		// FLVX copies it into a share and validates it against v1 names.
		if protocols := flvxShareProtocols(s, server.Helper); protocols != nil {
			item["protocol_caps"] = flvx.ProtocolCaps{Versions: []int{flvx.ProtocolsVersion}, Protocols: protocols}
		}
		if server.Mode != "" {
			item["server_mode"] = server.Mode
		}
		if busy := flvxBusyPorts(s, server); busy != nil {
			item["busy_ports"] = busy
		}
		if admin {
			nodes := []map[string]any{}
			for _, n := range s.Desired.Nodes {
				// Desired but never reported means the Helper has not applied it
				// yet; only a Helper report can mark a node removed.
				status := flvx.NodeStatus{ID: n.ID, State: "pending"}
				for _, v := range s.Status.Nodes {
					if v.ID == n.ID {
						status = v
						break
					}
				}
				nodes = append(nodes, map[string]any{"id": n.ID, "owner": n.Owner, "name": n.Name, "protocol": n.Protocol, "port": n.Port, "max_connections": n.MaxConnections, "max_online_ips": n.MaxOnlineIPs, "expires_at": n.ExpiresAt, "status": status})
			}
			item["nodes"], item["config"], item["report"] = nodes, s.Config, report
			item["native_landing"], item["detection_error"], item["helper"] = server.NativeLanding, server.DetectionError, server.Helper
			item["allow_egress"], item["egress_plan"] = !s.EgressDenied, flvxEgressSummary(s)
			item["core_pin"] = flvxCorePinSummary(s, latestCore)
			item["reality_guard_plan"] = flvxGuardSummary(s)
			if s.State != "revoked" && s.Status.Core.Error != "" {
				item["core_error"] = s.Status.Core.Error
			}
		}
		shares = append(shares, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := map[string]any{"shares": shares}
	if admin {
		var configured bool
		var last int64
		if err = db.QueryRowContext(ctx, `SELECT token_hash<>'',last_api_at FROM mmwxc_flvx_integration WHERE id=1`).Scan(&configured, &last); err != nil {
			return nil, err
		}
		result["servers"], result["integration"] = servers, map[string]any{"token_configured": configured, "last_api_at": last}
	}
	return result, nil
}
