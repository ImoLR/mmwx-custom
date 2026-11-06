package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// reality-pick-v1 (FLVX plan 227) on the Custom side:
//
//   - reality_pin: GET /shares advertises it for Helpers that pin the
//     fastest Reality dest IP; GET /shares/{id}/status carries reality_pins.
//   - reality_probe: POST /shares/{id}/reality-probe queues a probe of FLVX's
//     candidate targets on the share's server; GET
//     /shares/{id}/reality-probe/{probe_id} polls it. Custom keeps the job in
//     memory, hands it (signed) to the Helper with its next upload answer and
//     takes the result from the uploads after it finished.
//   - reality-pool: GET /reality-pool lists official mmwx's 「Reality 共享域名池」
//     domains (official admin API; the pool is merged into official's Reality
//     domain list only while official's 共享 setting is on).

const (
	flvxProbePickupLimit = 2 * time.Minute // queued, never started by the Helper
	flvxProbeRunLimit    = 3 * time.Minute
	flvxProbeKeep        = 30 * time.Minute // finished jobs stay pollable
	flvxProbeEnvelopeTTL = 5 * time.Minute

	flvxPoolTTL     = 12 * time.Hour
	flvxPoolBackoff = 30 * time.Minute
	flvxPoolTimeout = 60 * time.Second
)

func flvxRealityPinCapability(helper flvxHelper) map[string]any {
	if !helper.Ready || !helper.RealityPin {
		return nil
	}
	return map[string]any{"versions": []int{flvx.RealityPinVersion}}
}

func flvxRealityProbeCapability(helper flvxHelper) map[string]any {
	if !helper.Ready || !helper.RealityProbe {
		return nil
	}
	return map[string]any{"versions": []int{flvx.RealityProbeVersion}, "max_targets": flvx.MaxProbeTargets, "concurrency": flvx.ProbeConcurrency}
}

type flvxProbeJob struct {
	job        flvx.ProbeJob
	state      string // queued, running, done, error
	createdAt  time.Time
	startedAt  time.Time
	finishedAt time.Time
	results    []flvx.ProbeResult
	err        string
}

func (j *flvxProbeJob) view() map[string]any {
	ms := func(t time.Time) int64 {
		if t.IsZero() {
			return 0
		}
		return t.UnixMilli()
	}
	results := j.results
	if results == nil {
		results = []flvx.ProbeResult{}
	}
	out := map[string]any{"probe_id": j.job.ID, "state": j.state, "targets": j.job.Targets, "timeout_ms": j.job.TimeoutMS, "created_at": ms(j.createdAt), "started_at": ms(j.startedAt), "finished_at": ms(j.finishedAt), "results": results}
	if j.err != "" {
		out["error"] = j.err
	}
	return out
}

// expireLocked ends a job the Helper never started or never finished.
func (j *flvxProbeJob) expireLocked(now time.Time) {
	switch {
	case j.state == "queued" && now.Sub(j.createdAt) > flvxProbePickupLimit:
		j.state, j.err, j.finishedAt = "error", "Helper did not start the probe", now
	case j.state == "running" && now.Sub(j.createdAt) > flvxProbeRunLimit:
		j.state, j.err, j.finishedAt = "error", "probe timed out", now
	}
}

func (a *app) flvxProbeLocked(id int64, now time.Time) *flvxProbeJob {
	j := a.flvxProbes[id]
	if j == nil {
		return nil
	}
	j.expireLocked(now)
	if (j.state == "done" || j.state == "error") && now.Sub(j.finishedAt) > flvxProbeKeep {
		delete(a.flvxProbes, id)
		return nil
	}
	return j
}

// startFLVXProbe is POST /shares/{id}/reality-probe.
func (a *app) startFLVXProbe(ctx context.Context, id int64, req flvx.ProbeRequest) (map[string]any, error) {
	req, problems := flvx.ValidateProbe(req)
	if len(problems) > 0 {
		return nil, flvxValidationErrors(problems)
	}
	helper := a.flvxHelpers()[id]
	s, err := readFLVXShare(ctx, a.flvxDB(), id, false)
	if err != nil {
		return nil, err
	}
	if s.State == "revoked" {
		return nil, flvxAPIError{409, "share_revoked", "cancelled shares cannot be probed"}
	}
	if !helper.Ready || !helper.RealityProbe {
		return nil, flvxAPIError{409, "helper_outdated", "the share's Helper cannot probe Reality targets yet"}
	}
	probeID, err := randomUUID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	a.flvxProbesMu.Lock()
	defer a.flvxProbesMu.Unlock()
	if a.flvxProbes == nil {
		a.flvxProbes = map[int64]*flvxProbeJob{}
	}
	if j := a.flvxProbeLocked(id, now); j != nil && (j.state == "queued" || j.state == "running") {
		return nil, flvxAPIError{409, "probe_busy", "a probe is already running on this server: " + j.job.ID}
	}
	j := &flvxProbeJob{job: flvx.ProbeJob{ServerID: "", ID: probeID, Targets: req.Targets, TimeoutMS: req.TimeoutMS}, state: "queued", createdAt: now}
	a.flvxProbes[id] = j
	return j.view(), nil
}

// flvxProbeStatus is GET /shares/{id}/reality-probe/{probe_id}.
func (a *app) flvxProbeStatus(id int64, probeID string) (map[string]any, error) {
	a.flvxProbesMu.Lock()
	defer a.flvxProbesMu.Unlock()
	j := a.flvxProbeLocked(id, time.Now())
	if j == nil || j.job.ID != probeID {
		return nil, flvxAPIError{404, "not_found", "probe not found or expired"}
	}
	return j.view(), nil
}

// flvxProbeForHelper takes the Helper's probe report and returns the job to
// hand over (signed for this Helper), nil when there is none.
func (a *app) flvxProbeForHelper(id int64, serverID, token string, capable bool, report *flvx.ProbeReport) *flvx.ProbeEnvelope {
	now := time.Now()
	a.flvxProbesMu.Lock()
	defer a.flvxProbesMu.Unlock()
	j := a.flvxProbeLocked(id, now)
	if j == nil || !capable {
		return nil
	}
	if r := flvx.SanitizeProbeReport(report); r != nil && r.ID == j.job.ID && (j.state == "queued" || j.state == "running") {
		if j.startedAt.IsZero() {
			j.startedAt = now
		}
		switch r.State {
		case "running":
			j.state = "running"
		case "done", "error":
			j.state, j.err, j.finishedAt = r.State, r.Error, now
			if r.State == "done" && len(r.Results) != len(j.job.Targets) {
				j.state, j.err = "error", "incomplete probe report"
			}
			j.results = r.Results
		}
	}
	if j.state != "queued" && j.state != "running" {
		return nil
	}
	job := j.job
	job.ServerID = serverID
	e := &flvx.ProbeEnvelope{Job: job, ExpiresAt: now.Add(flvxProbeEnvelopeTTL).UnixMilli()}
	e.Sign(token)
	return e
}

// flvxRealityPool caches official's shared pool.
type flvxRealityPool struct {
	mu        sync.Mutex
	fetchedAt time.Time
	attempted time.Time
	domains   []map[string]any
	poolSize  int
	err       string
	done      chan struct{} // the refresh in flight, nil when idle
}

// flvxPoolSource reads the pool domains from official; tests replace it.
var flvxPoolSource = func(a *app, ctx context.Context) ([]map[string]any, int, error) {
	return a.fetchOfficialRealityPool(ctx)
}

// flvxPoolWait is how long a request waits for a refresh when nothing is
// cached yet.
var flvxPoolWait = 10 * time.Second

// realityPool is GET /reality-pool. It never fails: without official or
// its license the list is empty and error says why; a failed refresh keeps
// serving the last good list. A due refresh runs detached from the request
// (at most one at a time, at most every 30 min after a failure).
func (a *app) realityPool(ctx context.Context) map[string]any {
	p := &a.flvxPool
	p.mu.Lock()
	now := time.Now()
	if p.done == nil && now.Sub(p.fetchedAt) >= flvxPoolTTL && now.Sub(p.attempted) >= flvxPoolBackoff {
		p.attempted = now
		done := make(chan struct{})
		p.done = done
		go func() {
			child, cancel := context.WithTimeout(context.Background(), flvxPoolTimeout)
			domains, size, err := flvxPoolSource(a, child)
			cancel()
			p.mu.Lock()
			if err != nil {
				p.err = err.Error()
				if size > 0 {
					p.poolSize = size
				}
			} else {
				p.domains, p.poolSize, p.fetchedAt, p.err = domains, size, time.Now(), ""
			}
			p.done = nil
			p.mu.Unlock()
			close(done)
		}()
	}
	if done := p.done; done != nil && p.fetchedAt.IsZero() {
		p.mu.Unlock()
		select {
		case <-done:
		case <-time.After(flvxPoolWait):
		case <-ctx.Done():
		}
		p.mu.Lock()
	}
	defer p.mu.Unlock()
	domains := p.domains
	if domains == nil {
		domains = []map[string]any{}
	}
	out := map[string]any{"domains": domains, "pool_size": p.poolSize, "fetched_at": int64(0), "source": "official", "refreshing": p.done != nil}
	if !p.fetchedAt.IsZero() {
		out["fetched_at"] = p.fetchedAt.UnixMilli()
	}
	if p.err != "" {
		out["error"] = p.err
	}
	return out
}

// fetchOfficialRealityPool reads official v0.5.5's pool through its admin
// API: the share status (licensed / enabled / pool_size) and the Reality
// domain list, whose domain_sources mark pool domains "shared_pool". The
// list asks the probe server's official agent to test every candidate once
// (official's own picker does the same); Custom asks at most every 12 h.
func (a *app) fetchOfficialRealityPool(ctx context.Context) ([]map[string]any, int, error) {
	token, err := a.coreModeAdminSession(ctx, "")
	if err != nil {
		return nil, 0, errors.New("no official administrator session")
	}
	var share struct {
		Success  bool `json:"success"`
		Enabled  bool `json:"enabled"`
		Licensed bool `json:"licensed"`
		PoolSize int  `json:"pool_size"`
	}
	if err = a.officialLifecycleJSON(ctx, token, http.MethodGet, "/api/admin/remote/reality-domains/share", nil, &share); err != nil {
		return nil, 0, errors.New("official share status unavailable")
	}
	switch {
	case !share.Licensed:
		return nil, 0, errors.New("official license has no reality_pool")
	case !share.Enabled:
		return nil, share.PoolSize, errors.New("official 「Reality 共享域名池」 is off")
	}
	server := a.flvxPoolProbeServer()
	if server == "" {
		return nil, share.PoolSize, errors.New("no online server for official's domain list")
	}
	var list officialRealityDomains
	if err = a.officialLifecycleJSON(ctx, token, http.MethodGet, "/api/admin/remote/reality-domains?server_id="+url.QueryEscape(server)+"&timeout_ms=2000", nil, &list); err != nil {
		return nil, share.PoolSize, errors.New("official Reality domain list unavailable")
	}
	return list.pool(server), share.PoolSize, nil
}

// officialRealityDomains is official's GET /api/admin/remote/reality-domains.
type officialRealityDomains struct {
	Success bool                       `json:"success"`
	Sources map[string]json.RawMessage `json:"domain_sources"`
	Domains []struct {
		Domain        string `json:"domain"`
		Success       bool   `json:"success"`
		LatencyMS     int64  `json:"latency_ms"`
		RealityUsable *bool  `json:"reality_usable"`
	} `json:"domains"`
}

// pool keeps the "shared_pool" domains, normalised and sorted, with
// official's own check from the probe server.
func (list officialRealityDomains) pool(server string) []map[string]any {
	probed := map[string]map[string]any{}
	for _, d := range list.Domains {
		item := map[string]any{"official_ok": d.Success}
		if d.Success {
			item["official_latency_ms"] = d.LatencyMS
		}
		if d.RealityUsable != nil {
			item["official_reality_usable"] = *d.RealityUsable
		}
		probed[strings.ToLower(d.Domain)] = item
	}
	serverID, _ := strconv.ParseInt(server, 10, 64)
	seen := map[string]bool{}
	out := []map[string]any{}
	for raw, source := range list.Sources {
		if !flvxPoolSourced(source) {
			continue
		}
		target, ok := flvx.ProbeTarget(raw)
		if !ok {
			continue
		}
		domain, _, _ := net.SplitHostPort(target)
		if seen[domain] {
			continue
		}
		seen[domain] = true
		item := map[string]any{"domain": domain}
		if p := probed[strings.ToLower(raw)]; p != nil {
			for k, v := range p {
				item[k] = v
			}
			item["official_probe_server_id"] = serverID
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["domain"].(string) < out[j]["domain"].(string) })
	return out
}

// flvxPoolSourced: official marks a pool domain "shared_pool" (a string, or
// one entry of a list).
func flvxPoolSourced(raw json.RawMessage) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == "shared_pool"
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, s := range many {
			if s == "shared_pool" {
				return true
			}
		}
	}
	return false
}

// flvxPoolProbeServer is the server official's domain list is probed from:
// MMWXC_REALITY_POOL_SERVER_ID, else the lowest server ID whose Helper
// reported in the last 2 minutes.
func (a *app) flvxPoolProbeServer() string {
	if id := strings.TrimSpace(os.Getenv("MMWXC_REALITY_POOL_SERVER_ID")); id != "" {
		return id
	}
	if a.helperState == nil {
		return ""
	}
	s := a.helperState
	s.mu.Lock()
	defer s.mu.Unlock()
	best := int64(0)
	for id, status := range s.data.AgentStatuses {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 || status.ReportedAt.IsZero() || time.Since(status.ReportedAt) > 2*time.Minute {
			continue
		}
		if best == 0 || n < best {
			best = n
		}
	}
	if best == 0 {
		return ""
	}
	return strconv.FormatInt(best, 10)
}
