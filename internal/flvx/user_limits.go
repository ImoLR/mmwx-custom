package flvx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"
)

// Per-user landing limits for the FLVX Core instance (contract
// landing-user-limits-v1, FLVX plans 158 / 159). FLVX's quota coordinator
// splits a user's 总连接数 / 总 IP 数 into one budget per place; the budgets of
// a share travel outside the signed desired state (a budget change never
// bumps the revision) and are held in memory only: Custom keeps the latest per
// share, the Helper the latest it was handed with an upload answer. mmwx's own
// Core, its users and settings are never involved.
const (
	UserLimitsVersion     = 1
	UserLimitsRateVersion = 2
	// Helper capabilities: per-user groups + 入站 node limits + usage, and the
	// per-user 速率 (version 2) on top.
	UserLimitsCapability = "flvx.user_limits.v1"
	UserRateCapability   = "flvx.user_rate.v1"

	ConnModeTotal   = "total"
	ConnModeInbound = "inbound"

	MaxUserLimitUsers   = 10000
	MaxUserLimitIPs     = 1024
	MaxRelaySources     = 4096
	MaxRelayIPReserve   = 4096
	MaxUserRateMbps     = 100000
	maxUserLimitCount   = 10000000
	maxUserLimitIPCount = 1000000

	// RateBytesPerMbps is FLVX's unit for every 速率: Mbps / 8 in MiB.
	RateBytesPerMbps = 131072

	// UserLimitsTTL: budgets expire this long after the last successful FLVX
	// exchange (FLVX gone ⇒ fail open to node / port limits only).
	UserLimitsTTL = 10 * time.Minute

	// UserGroupPrefix names the FLVX Core management group of one owner.
	UserGroupPrefix = "flvx-user-"
)

// InboundLimit reports whether the node's max_connections counts inbound
// (client) connections only (conn_mode "inbound"); otherwise 入站+出站.
func (n Node) InboundLimit() bool { return n.ConnMode == ConnModeInbound }

// UserGroup is the Core management group of a node owner.
func UserGroup(owner OwnerID) string { return UserGroupPrefix + string(owner) }

// UserLimitBudget is one owner's budget on one share (UserID = node owner.id).
// Pointers are null = unlimited; 0 = full. MaxInboundConnections excludes
// relays (the Helper adds its live relay count), ServerMaxInboundConnections
// includes them, MaxInboundOnlineIPs excludes relay IPs; RelayIPReserve are
// FLVX nodes that dial this user's nodes here (each keeps an IP slot).
type UserLimitBudget struct {
	UserID                      string `json:"user_id"`
	MaxInboundConnections       *int64 `json:"max_inbound_connections"`
	ServerMaxInboundConnections *int64 `json:"server_max_inbound_connections"`
	MaxInboundOnlineIPs         *int   `json:"max_inbound_online_ips"`
	RelayIPReserve              int    `json:"relay_ip_reserve"`
	// Version 2 only: the owner's 速率 on this share, each direction.
	MaxRateMbps *int `json:"max_rate_mbps,omitempty"`
}

// UserLimitsRequest is the body of PUT /shares/{server_id}/user-limits.
type UserLimitsRequest struct {
	Version  int               `json:"version"`
	Sequence int64             `json:"sequence"`
	Users    []UserLimitBudget `json:"users"`
}

// UserLimitUsage is one owner group's live usage: InboundConnections and
// OnlineIPs exclude relay sources, RelayConnections counts them.
type UserLimitUsage struct {
	UserID             string   `json:"user_id"`
	InboundConnections int64    `json:"inbound_connections"`
	RelayConnections   int64    `json:"relay_connections"`
	OnlineIPs          []string `json:"online_ips"`
	// Optional (contract §7.4): the owner's 速率 is in force. FLVX ignores it.
	RateApplied bool `json:"rate_applied,omitempty"`
}

// UserLimitsReport is the answer to FLVX.
type UserLimitsReport struct {
	AppliedSequence int64            `json:"applied_sequence"`
	CollectedAt     int64            `json:"collected_at"`
	Users           []UserLimitUsage `json:"users"`
}

// UserLimitsUsage is the Helper's part of its 5 s upload (Helper → Custom
// only): the newest budgets it holds and applied, and the usage of the last
// Core snapshot. It rides next to the FLVX report, outside its sequence, so
// it is fresh on every upload and never stored.
type UserLimitsUsage struct {
	HeldSequence    int64            `json:"held_sequence"`
	AppliedSequence int64            `json:"applied_sequence"`
	CollectedAt     int64            `json:"collected_at"`
	Users           []UserLimitUsage `json:"users"`
}

// UserLimitsPayload is what Custom hands the Helper: the share's latest
// budgets and the relay sources (IP literals of flvx_node_hosts).
type UserLimitsPayload struct {
	ServerID     string            `json:"server_id"`
	Sequence     int64             `json:"sequence"`
	Users        []UserLimitBudget `json:"users"`
	RelaySources []string          `json:"relay_sources"`
}

// UserLimitsEnvelope is signed like the desired envelope with its own domain.
// ExpiresAt = last successful FLVX exchange + UserLimitsTTL.
type UserLimitsEnvelope struct {
	Payload   UserLimitsPayload `json:"payload"`
	ExpiresAt int64             `json:"expires_at"`
	Signature string            `json:"signature"`
}

func (e UserLimitsEnvelope) signature(token string) string {
	key := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, key[:])
	b, _ := json.Marshal(struct {
		Payload   UserLimitsPayload
		ExpiresAt int64
	}{e.Payload, e.ExpiresAt})
	mac.Write([]byte("mmwxc-flvx-user-limits-v1\n"))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

func (e *UserLimitsEnvelope) Sign(token string) { e.Signature = e.signature(token) }

// Verify checks the signature, the server and that the budgets have not
// expired (expiry at most 11 minutes ahead, as for the desired envelope).
func (e UserLimitsEnvelope) Verify(token, serverID string, now time.Time) bool {
	return e.Payload.ServerID == serverID && e.Payload.Sequence > 0 && e.ExpiresAt > now.UnixMilli() && e.ExpiresAt <= now.Add(11*time.Minute).UnixMilli() && hmac.Equal([]byte(e.Signature), []byte(e.signature(token)))
}

// ValidateUserLimits checks a request per contract §4 / §7.2. rate = the
// share's Helper applies 速率, so version 2 is allowed.
func ValidateUserLimits(r UserLimitsRequest, rate bool) []ValidationError {
	errors := []ValidationError{}
	add := func(field, message string) { errors = append(errors, ValidationError{Field: field, Message: message}) }
	switch {
	case r.Version == UserLimitsVersion:
	case r.Version == UserLimitsRateVersion && rate:
	default:
		add("version", "unsupported user limits version for this share")
	}
	if r.Sequence <= 0 {
		add("sequence", "sequence must be a positive integer")
	}
	if r.Users == nil || len(r.Users) > MaxUserLimitUsers {
		add("users", "users must be an array of at most 10000 entries")
		return errors
	}
	seen := map[string]bool{}
	for i, u := range r.Users {
		at := fmt.Sprintf("users[%d]: ", i)
		if !identifier.MatchString(u.UserID) || seen[u.UserID] {
			add("users", at+"user_id must be a unique node owner id")
		}
		seen[u.UserID] = true
		if v := u.MaxInboundConnections; v != nil && (*v < 0 || *v > maxUserLimitCount) {
			add("users", at+"invalid max_inbound_connections")
		}
		if v := u.ServerMaxInboundConnections; v != nil && (*v < 0 || *v > maxUserLimitCount) {
			add("users", at+"invalid server_max_inbound_connections")
		}
		if v := u.MaxInboundOnlineIPs; v != nil && (*v < 0 || *v > maxUserLimitIPCount) {
			add("users", at+"invalid max_inbound_online_ips")
		}
		if u.RelayIPReserve < 0 || u.RelayIPReserve > MaxRelayIPReserve {
			add("users", at+"relay_ip_reserve must be 0–4096")
		}
		if v := u.MaxRateMbps; v != nil && (r.Version != UserLimitsRateVersion || *v < 1 || *v > MaxUserRateMbps) {
			add("users", at+"max_rate_mbps must be 1–100000 and needs version 2")
		}
	}
	return errors
}

// ValidUserLimitsPayload bounds a payload the Helper received (it was signed
// by Custom after ValidateUserLimits; this only guards the Helper's maps).
func ValidUserLimitsPayload(p UserLimitsPayload) bool {
	if len(p.Users) > MaxUserLimitUsers || len(p.RelaySources) > MaxRelaySources+2 {
		return false
	}
	r := UserLimitsRequest{Version: UserLimitsRateVersion, Sequence: p.Sequence, Users: p.Users}
	if r.Users == nil {
		r.Users = []UserLimitBudget{}
	}
	if len(ValidateUserLimits(r, true)) > 0 {
		return false
	}
	for _, ip := range p.RelaySources {
		if _, err := netip.ParseAddr(ip); err != nil {
			return false
		}
	}
	return true
}

// CanonicalIP is the canonical text of an address (IPv4-mapped → IPv4,
// brackets trimmed); ok is false for anything that is not an IP literal.
func CanonicalIP(raw string) (string, bool) {
	addr, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(raw), "[]"))
	if err != nil {
		return "", false
	}
	return addr.Unmap().WithZone("").String(), true
}

// RelaySources are the canonical IP literals among FLVX's flvx_node_hosts
// (at most MaxRelaySources) plus the loopback addresses, sorted; host names
// are skipped (FLVX sends their resolved addresses next to them).
func RelaySources(hosts []string) []string {
	loopback := map[string]bool{"127.0.0.1": true, "::1": true}
	set := map[string]bool{}
	for _, h := range hosts {
		if ip, ok := CanonicalIP(h); ok && !loopback[ip] {
			set[ip] = true
		}
	}
	out := make([]string, 0, len(set)+2)
	for ip := range set {
		out = append(out, ip)
	}
	sort.Strings(out)
	if len(out) > MaxRelaySources {
		out = out[:MaxRelaySources]
	}
	out = append(out, "127.0.0.1", "::1")
	sort.Strings(out)
	return out
}
