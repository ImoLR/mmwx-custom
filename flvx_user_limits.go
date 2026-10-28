package main

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Per-user landing limits for the FLVX Core instance (contract
// landing-user-limits-v1 §4 / §7). FLVX PUTs one share's budgets every few
// seconds; Custom keeps the latest per share in memory (never in the signed
// desired state, never on disk), hands them to the share's Helper with its
// next upload answer and answers FLVX with the usage that Helper reported
// last. Budgets expire UserLimitsTTL after the last applied exchange, so a
// gone FLVX fails open to node / port limits.

// flvxUsageMaxAge bounds how old a Helper usage report FLVX still gets
// (FLVX itself keeps a share's last usage for 2 minutes).
const flvxUsageMaxAge = 2 * time.Minute

type flvxUserLimitsShare struct {
	request     flvx.UserLimitsRequest
	exchangedAt time.Time
	usage       *flvx.UserLimitsUsage
	usageAt     time.Time
}

// flvxUserLimitsCapability is the share list's user_limits object (§2,
// §7.1): only while the share's Helper groups the FLVX Core per user, with
// version 2 when it also polices 速率.
func flvxUserLimitsCapability(helper flvxHelper) map[string]any {
	if !helper.Ready || !helper.UserLimits {
		return nil
	}
	versions := []int{flvx.UserLimitsVersion}
	if helper.UserRate {
		versions = append(versions, flvx.UserLimitsRateVersion)
	}
	return map[string]any{"versions": versions}
}

// flvxHelperConnMode strips conn_mode for a Helper without
// flvx.user_limits.v1: its strict payload check would reject the signed
// desired state. Such a Helper keeps 入站+出站 node limits (stricter, never
// unlimited), as FLVX expects while the share does not advertise the
// capability.
func flvxHelperConnMode(d flvx.Desired, supported bool) flvx.Desired {
	if supported {
		return d
	}
	stripped := false
	nodes := make([]flvx.Node, len(d.Nodes))
	for i, n := range d.Nodes {
		if n.ConnMode != "" {
			n.ConnMode, stripped = "", true
		}
		nodes[i] = n
	}
	if stripped {
		d.Nodes = nodes
	}
	return d
}

// userLimitsEntryLocked returns the share's state with expired budgets
// dropped. Caller holds flvxLimitsMu.
func (a *app) userLimitsEntryLocked(id int64, now time.Time) *flvxUserLimitsShare {
	if a.flvxLimits == nil {
		a.flvxLimits = map[int64]*flvxUserLimitsShare{}
	}
	entry := a.flvxLimits[id]
	if entry == nil {
		entry = &flvxUserLimitsShare{}
		a.flvxLimits[id] = entry
	}
	if entry.request.Sequence > 0 && now.Sub(entry.exchangedAt) >= flvx.UserLimitsTTL {
		// FLVX has been gone for the whole TTL: forget the budgets and the
		// sequence, so a restarted FLVX with an earlier clock is accepted.
		entry.request, entry.exchangedAt = flvx.UserLimitsRequest{}, time.Time{}
	}
	return entry
}

// forgetFLVXUserLimits drops a share's budgets (share not active or its
// Helper lost the capability); the Helper's copy expires on its own.
func (a *app) forgetFLVXUserLimits(id int64) {
	a.flvxLimitsMu.Lock()
	defer a.flvxLimitsMu.Unlock()
	delete(a.flvxLimits, id)
}

// putFLVXUserLimits is PUT /shares/{server_id}/user-limits.
func (a *app) putFLVXUserLimits(ctx context.Context, id int64, req flvx.UserLimitsRequest) (flvx.UserLimitsReport, error) {
	helper := a.flvxHelpers()[id]
	s, err := readFLVXShare(ctx, a.flvxDB(), id, false)
	if err != nil {
		return flvx.UserLimitsReport{}, err
	}
	if s.State != "active" {
		a.forgetFLVXUserLimits(id)
		return flvx.UserLimitsReport{}, flvxAPIError{409, "share_inactive", "paused or revoked shares take no user limits"}
	}
	if !helper.Ready || !helper.UserLimits {
		a.forgetFLVXUserLimits(id)
		return flvx.UserLimitsReport{}, flvxAPIError{409, "user_limits_unsupported", "the share's Helper does not apply per-user limits"}
	}
	if problems := flvx.ValidateUserLimits(req, helper.UserRate); len(problems) > 0 {
		return flvx.UserLimitsReport{}, flvxValidationErrors(problems)
	}
	now := time.Now()
	a.flvxLimitsMu.Lock()
	defer a.flvxLimitsMu.Unlock()
	entry := a.userLimitsEntryLocked(id, now)
	// A sequence at or below the stored one is a stale retry: 200 with the
	// stored state, nothing changes (and the expiry does not move).
	if req.Sequence > entry.request.Sequence {
		req.Users = append([]flvx.UserLimitBudget{}, req.Users...)
		entry.request, entry.exchangedAt = req, now
	}
	report := flvx.UserLimitsReport{Users: []flvx.UserLimitUsage{}}
	if u := entry.usage; u != nil && now.Sub(entry.usageAt) <= flvxUsageMaxAge {
		report.AppliedSequence, report.CollectedAt = u.AppliedSequence, u.CollectedAt
		for _, user := range u.Users {
			user.OnlineIPs = append([]string{}, user.OnlineIPs...)
			report.Users = append(report.Users, user)
		}
	}
	return report, nil
}

// sanitizeFLVXUsage bounds a Helper usage report: valid owner ids, no
// negative counts, canonical sorted IPs (≤ 1024 per user).
func sanitizeFLVXUsage(u flvx.UserLimitsUsage) *flvx.UserLimitsUsage {
	out := &flvx.UserLimitsUsage{HeldSequence: max(u.HeldSequence, 0), AppliedSequence: max(u.AppliedSequence, 0), CollectedAt: max(u.CollectedAt, 0), Users: []flvx.UserLimitUsage{}}
	seen := map[string]bool{}
	for _, user := range u.Users {
		if len(out.Users) >= flvx.MaxUserLimitUsers {
			break
		}
		if seen[user.UserID] || !flvx.ValidOwnerID(user.UserID) || user.InboundConnections < 0 || user.RelayConnections < 0 {
			continue
		}
		seen[user.UserID] = true
		ips := []string{}
		unique := map[string]bool{}
		for _, raw := range user.OnlineIPs {
			if ip, ok := flvx.CanonicalIP(raw); ok && !unique[ip] && len(ips) < flvx.MaxUserLimitIPs {
				unique[ip] = true
				ips = append(ips, ip)
			}
		}
		sort.Strings(ips)
		user.OnlineIPs = ips
		out.Users = append(out.Users, user)
	}
	sort.Slice(out.Users, func(i, j int) bool { return out.Users[i].UserID < out.Users[j].UserID })
	return out
}

// flvxUserLimitsForHelper records the Helper's usage and returns the budgets
// to hand it with this upload answer: only while they are in force and the
// Helper does not hold that sequence yet (it reports what it holds), so
// steady state sends nothing extra. capable = the upload's Helper reports
// flvx.user_limits.v1.
func (a *app) flvxUserLimitsForHelper(ctx context.Context, id int64, serverID, token string, capable bool, usage *flvx.UserLimitsUsage) (*flvx.UserLimitsEnvelope, error) {
	if !capable {
		a.forgetFLVXUserLimits(id)
		return nil, nil
	}
	now := time.Now()
	a.flvxLimitsMu.Lock()
	entry := a.userLimitsEntryLocked(id, now)
	if usage != nil {
		entry.usage, entry.usageAt = sanitizeFLVXUsage(*usage), now
	}
	request, exchangedAt := entry.request, entry.exchangedAt
	a.flvxLimitsMu.Unlock()
	if request.Sequence == 0 || usage != nil && usage.HeldSequence == request.Sequence {
		return nil, nil
	}
	db := a.flvxDB()
	if db == nil {
		return nil, errors.New("FLVX storage unavailable")
	}
	hosts, err := readFLVXHosts(ctx, db)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	e := &flvx.UserLimitsEnvelope{Payload: flvx.UserLimitsPayload{ServerID: serverID, Sequence: request.Sequence, Users: append([]flvx.UserLimitBudget{}, request.Users...), RelaySources: flvx.RelaySources(hosts.Nodes)}, ExpiresAt: exchangedAt.Add(flvx.UserLimitsTTL).UnixMilli()}
	e.Sign(token)
	return e, nil
}

// flvxUserLimitsSummary tells the operator page how many FLVX users have
// budgets on the share and when FLVX last sent them (0 = never / expired).
func (a *app) flvxUserLimitsSummary(id int64, helper flvxHelper) map[string]any {
	a.flvxLimitsMu.Lock()
	defer a.flvxLimitsMu.Unlock()
	result := map[string]any{"supported": helper.Ready && helper.UserLimits, "rate_supported": helper.Ready && helper.UserLimits && helper.UserRate, "users": 0, "rated_users": 0, "exchanged_at": int64(0)}
	entry := a.flvxLimits[id]
	if entry == nil || entry.request.Sequence == 0 || time.Since(entry.exchangedAt) >= flvx.UserLimitsTTL {
		return result
	}
	rated := 0
	for _, u := range entry.request.Users {
		if u.MaxRateMbps != nil {
			rated++
		}
	}
	result["users"], result["rated_users"], result["exchanged_at"] = len(entry.request.Users), rated, entry.exchangedAt.UnixMilli()
	return result
}
