package main

import (
	"errors"
	"sort"
	"strings"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Per-user landing limits in the FLVX Core instance (contract
// landing-user-limits-v1 §5, the same formulas as FLVX's own agent in
// go-gost/x/landing/user_limits.go). Every FLVX node of an owner with a
// budget joins the Core management group "flvx-user-<owner.id>"; the group's
// inbound limits come from the latest budgets and the last Core snapshot of
// the pass, and travel with the pass's existing PUT /v1/config. Budgets come
// with the upload answer, live in memory only and expire with their signed
// expiry (10 minutes after FLVX's last exchange ⇒ fail open to node / port
// limits). Nothing here touches mmwx's own Core.

// flvxBudgetView is the held budgets in the form a pass uses.
type flvxBudgetView struct {
	sequence int64
	users    map[flvx.OwnerID]flvx.UserLimitBudget
	relay    map[string]bool
}

// flvxGroupLive is a group's state in the pass's Core snapshot.
type flvxGroupLive struct {
	current    int64 // admitted inbound leases, relays included
	relayConns int64
	relayIPs   int // relay sources among the group's online IPs
	seenIPs    int // every listed IP, the Core's 30 s grace included
	direct     []string
}

// flvxUserLimitsPublished is what the upload loop reads (under m.mu).
type flvxUserLimitsPublished struct {
	applied     int64
	collectedAt int64
	users       []flvx.UserLimitUsage
	rates       map[string]bool
}

// submitUserLimits takes the budgets of an upload answer. It runs on the
// upload loop: no Core call, no disk write, no log (the caller logs).
func (m *flvxManager) submitUserLimits(e *flvx.UserLimitsEnvelope) error {
	if m == nil || e == nil {
		return nil
	}
	now := m.now()
	if !e.Verify(m.config.Token, m.config.ServerID, now) {
		return errors.New("invalid FLVX user limits signature or expiry")
	}
	if !flvx.ValidUserLimitsPayload(e.Payload) {
		return errors.New("invalid FLVX user limits")
	}
	held := *e
	held.Payload.Users = append([]flvx.UserLimitBudget{}, e.Payload.Users...)
	held.Payload.RelaySources = append([]string{}, e.Payload.RelaySources...)
	m.budgetMu.Lock()
	defer m.budgetMu.Unlock()
	// The newest sequence wins; an older one only replaces expired budgets
	// (FLVX restarted with an earlier clock and Custom dropped its state).
	if old := m.budgets; old != nil && held.Payload.Sequence < old.Payload.Sequence && old.ExpiresAt > now.UnixMilli() {
		return nil
	}
	m.budgets = &held
	return nil
}

// reportBudgetExpiry logs once when budgets expired (FLVX stopped the
// exchange) and once when budgets are in force again.
func (m *flvxManager) reportBudgetExpiry() {
	m.budgetMu.Lock()
	expired, held := m.budgetsExpired, m.budgets != nil
	if held {
		m.budgetsExpired = false
	}
	m.budgetMu.Unlock()
	if expired && !held {
		m.logs.report("FLVX user limits", errors.New("budgets expired without a FLVX exchange; node and port limits only"))
	} else if held {
		m.logs.report("FLVX user limits", nil)
	}
}

// heldBudgetSequence is the sequence of the budgets in force (0 = none).
func (m *flvxManager) heldBudgetSequence() int64 {
	m.budgetMu.Lock()
	defer m.budgetMu.Unlock()
	if m.budgets == nil || m.budgets.ExpiresAt <= m.now().UnixMilli() {
		return 0
	}
	return m.budgets.Payload.Sequence
}

// budgetView returns the budgets in force, nil when there are none or they
// expired (expired budgets are dropped: fail open).
func (m *flvxManager) budgetView() *flvxBudgetView {
	m.budgetMu.Lock()
	defer m.budgetMu.Unlock()
	e := m.budgets
	if e != nil && e.ExpiresAt <= m.now().UnixMilli() {
		m.budgets, e = nil, nil
		m.budgetsExpired = true
	}
	if e == nil {
		m.budgetCache = nil
		return nil
	}
	if c := m.budgetCache; c != nil && m.budgetCacheOf == e {
		return c
	}
	view := &flvxBudgetView{sequence: e.Payload.Sequence, users: map[flvx.OwnerID]flvx.UserLimitBudget{}, relay: map[string]bool{"127.0.0.1": true, "::1": true}}
	for _, u := range e.Payload.Users {
		view.users[flvx.OwnerID(u.UserID)] = u
	}
	for _, ip := range e.Payload.RelaySources {
		if canonical, ok := flvx.CanonicalIP(ip); ok {
			view.relay[canonical] = true
		}
	}
	m.budgetCache, m.budgetCacheOf = view, e
	return view
}

// flvxGroupLives derives each owner group's live state from a snapshot.
func flvxGroupLives(snapshot coreSnapshotResponse, relay map[string]bool) map[string]flvxGroupLive {
	lives := map[string]flvxGroupLive{}
	for _, g := range snapshot.ManagementGroups {
		if !strings.HasPrefix(g.Username, flvx.UserGroupPrefix) {
			continue
		}
		l := flvxGroupLive{current: g.InboundCurrent, seenIPs: len(g.InboundOnlineIPs), direct: []string{}}
		for _, ip := range g.InboundOnlineIPs {
			canonical, ok := flvx.CanonicalIP(ip.IP)
			if ok && relay[canonical] {
				l.relayConns += ip.Connections
				l.relayIPs++
				continue
			}
			// Grace-only IPs (no active lease) are not reported as online.
			if ip.Connections > 0 && ok && len(l.direct) < flvx.MaxUserLimitIPs {
				l.direct = append(l.direct, canonical)
			}
		}
		if l.current < 0 {
			l.current = 0
		}
		if l.relayConns > l.current {
			l.relayConns = l.current
		}
		sort.Strings(l.direct)
		lives[g.Username] = l
	}
	return lives
}

// flvxGroupConnLimit maps a budget to the group's max_inbound_connections.
// The Core counts every admitted inbound lease of the group, relays included;
// the user-total budget excludes relays, so the live relay count is added
// back, and the 入站连接数 of this server (relays included) caps it. The Core
// reads 0 as unlimited: a full group with nothing open is blocked instead
// (exact), one with open leases gets 1 (≤ open ⇒ nothing new).
func flvxGroupConnLimit(b flvx.UserLimitBudget, live flvxGroupLive) (*int64, bool) {
	l := int64(-1)
	if b.MaxInboundConnections != nil {
		l = *b.MaxInboundConnections + live.relayConns
	}
	if s := b.ServerMaxInboundConnections; s != nil && (l < 0 || *s < l) {
		l = *s
	}
	if l < 0 {
		return nil, false
	}
	if l == 0 {
		if live.current == 0 {
			return nil, true
		}
		l = 1
	}
	return &l, false
}

// flvxGroupIPLimit maps a budget to max_inbound_online_ips: relay sources
// keep their slots (the ones seen, at least the reserve FLVX announced), so
// a relay is never refused for a new source IP.
func flvxGroupIPLimit(b flvx.UserLimitBudget, live flvxGroupLive) (*int, bool) {
	if b.MaxInboundOnlineIPs == nil {
		return nil, false
	}
	l := *b.MaxInboundOnlineIPs + max(live.relayIPs, b.RelayIPReserve)
	if l == 0 {
		if live.seenIPs == 0 {
			return nil, true
		}
		l = 1
	}
	return &l, false
}

// applyUserLimits adds the owner groups of the admitted (wanted) nodes to
// the pass's Core settings and returns the identities of full idle groups to
// block. Reality guard inbounds are never desired nodes, so never mapped.
func (m *flvxManager) applyUserLimits(settings *connectionSettings, view *flvxBudgetView, snapshot coreSnapshotResponse, wanted map[string]string) []coreIdentity {
	if view == nil {
		return nil
	}
	members := map[string][]string{}
	budgets := map[string]flvx.UserLimitBudget{}
	for _, n := range m.state.Payload.Desired.Nodes {
		b, ok := view.users[n.Owner.ID]
		if !ok || wanted[n.Tag()] == "" {
			continue
		}
		group := flvx.UserGroup(n.Owner.ID)
		members[group] = append(members[group], n.Tag())
		budgets[group] = b
		settings.ManagementMappings = append(settings.ManagementMappings, managementMapping{Identity: coreIdentity{InboundTag: n.Tag(), User: n.Tag()}, Group: group})
	}
	groups := make([]string, 0, len(members))
	for group := range members {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	lives := flvxGroupLives(snapshot, view.relay)
	var blocked []coreIdentity
	for _, group := range groups {
		live := lives[group]
		conns, blockConns := flvxGroupConnLimit(budgets[group], live)
		ips, blockIPs := flvxGroupIPLimit(budgets[group], live)
		if blockConns || blockIPs {
			for _, tag := range members[group] {
				blocked = append(blocked, coreIdentity{InboundTag: tag, User: tag})
			}
		}
		settings.ManagementUsers = append(settings.ManagementUsers, managementUserSettings{Username: group, MaxInboundConnections: conns, MaxInboundOnlineIPs: ips})
	}
	return blocked
}

// recordUserLimits publishes the usage of the pass's snapshot for the next
// uploads (memory only) together with the sequence the pass applied.
func (m *flvxManager) recordUserLimits(snapshot coreSnapshotResponse) {
	relay := map[string]bool{"127.0.0.1": true, "::1": true}
	if view := m.budgetView(); view != nil {
		relay = view.relay
	}
	lives := flvxGroupLives(snapshot, relay)
	users := make([]flvx.UserLimitUsage, 0, len(lives))
	for group, l := range lives {
		users = append(users, flvx.UserLimitUsage{UserID: strings.TrimPrefix(group, flvx.UserGroupPrefix), InboundConnections: l.current - l.relayConns, RelayConnections: l.relayConns, OnlineIPs: l.direct})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UserID < users[j].UserID })
	m.mu.Lock()
	m.userLimits.applied = m.appliedBudget
	m.userLimits.collectedAt = m.now().UnixMilli()
	m.userLimits.users = users
	m.mu.Unlock()
}

// userLimitsUpload is the Helper's user-limits part of an upload; nil while
// this machine holds no FLVX share.
func (m *flvxManager) userLimitsUpload() *flvx.UserLimitsUsage {
	if m == nil {
		return nil
	}
	held := m.heldBudgetSequence()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.report == nil {
		return nil
	}
	u := &flvx.UserLimitsUsage{HeldSequence: held, AppliedSequence: m.userLimits.applied, CollectedAt: m.userLimits.collectedAt, Users: make([]flvx.UserLimitUsage, 0, len(m.userLimits.users))}
	for _, user := range m.userLimits.users {
		user.OnlineIPs = append([]string{}, user.OnlineIPs...)
		user.RateApplied = m.userLimits.rates[user.UserID]
		u.Users = append(u.Users, user)
	}
	return u
}

// flvxInboundByNode is Σ inbound_current per FLVX node id, the number a 入站
// node limit counts.
func flvxInboundByNode(snapshot coreSnapshotResponse) map[string]int64 {
	inbound := map[string]int64{}
	for _, u := range snapshot.Users {
		if strings.HasPrefix(u.Identity.InboundTag, "flvxl-") && u.InboundCurrent > 0 {
			inbound[strings.TrimPrefix(u.Identity.InboundTag, "flvxl-")] += u.InboundCurrent
		}
	}
	return inbound
}
