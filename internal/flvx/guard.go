package flvx

import (
	"net"
	"sort"
	"strconv"
	"strings"
)

// Reality 防盗 (contract landing-reality-guard-v1, FLVX plan 127) for the
// shared FLVX Core. REALITY relays every connection whose ClientHello it does
// not authenticate to the node's dest, so anyone could use a shared server as
// a free relay to whatever else the dest's address serves by sending another
// SNI. With the guard, the Helper points dest at a loopback dokodemo inbound
// of the same Core whose routing lets only the node's own serverNames through
// to the real (pinned) dest; everything else (other SNI, no SNI, not TLS, no
// ClientHello within the Core's sniffing window) is closed.
//
// FLVX only switches it per share (Desired.RealityGuard); the guard inbound,
// its loopback port and the re-pointed dest are Helper-internal and never part
// of the validated desired state, so Custom keeps rejecting any non-public
// dest FLVX sends. Rendering matches FLVX go-gost/x/landing/guard.go.
const RealityGuardVersion = 1

// GuardTagPrefix marks the guard inbounds of the shared Core.
const GuardTagPrefix = "flvxg-"

// guardDeferAccept (TCP_DEFER_ACCEPT, seconds) hands a guarded Reality
// connection to the Core only once its first bytes (the ClientHello) have
// arrived. REALITY dials dest — the guard — at accept, and the guard has the
// Core's fixed 200 ms sniffing window to see the ClientHello; deferring the
// accept keeps a late ClientHello (lost first segment, slow client) inside
// that window. Clients that never send are accepted after the timeout as
// before and then closed by REALITY's own handshake timeout.
const guardDeferAccept = "5"

// RealityGuardStatus is the Helper's report (contract §5). Error is shown to
// the share operator only and never sent to FLVX.
type RealityGuardStatus struct {
	State string `json:"state"`
	Nodes int    `json:"nodes"`
	Error string `json:"error,omitempty"`
}

// Valid bounds a Helper-reported status before Custom stores it.
func (s *RealityGuardStatus) Valid() bool {
	if s == nil {
		return true
	}
	switch s.State {
	case "on", "restart_required", "error":
	default:
		return false
	}
	return s.Nodes >= 0 && s.Nodes <= MaxNodes && len([]rune(s.Error)) <= 200
}

// GuardOn reports whether FLVX asked for the guard on this share. Absent (old
// FLVX panels) means off, exactly as before the contract.
func (d Desired) GuardOn() bool { return d.RealityGuard != nil && *d.RealityGuard }

// Guard is one guard group: the nodes sharing the pinned dest and the same
// serverNames share one loopback inbound.
type Guard struct {
	Dest  string   `json:"dest"`  // pinned public ip:port
	Names []string `json:"names"` // sorted, lower-case
}

func GuardTag(port int) string { return GuardTagPrefix + strconv.Itoa(port) }

// GuardNames is the canonical serverName list of a group: trimmed,
// lower-case (the Core lower-cases the sniffed name before matching), sorted
// and without duplicates.
func GuardNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (g Guard) Key() string { return g.Dest + "|" + strings.Join(g.Names, ",") }

// Inbound is the loopback guard: TLS sniffing for routing only, so the
// connection always goes to the pinned dest, never to the sniffed name.
func (g Guard) Inbound(port int) map[string]any {
	host, destPort, _ := net.SplitHostPort(g.Dest)
	p, _ := strconv.Atoi(destPort)
	return map[string]any{
		"tag": GuardTag(port), "listen": "127.0.0.1", "port": port, "protocol": "dokodemo-door",
		"settings": map[string]any{"address": host, "port": p, "network": "tcp"},
		"sniffing": map[string]any{"enabled": true, "destOverride": []string{"tls"}, "routeOnly": true},
	}
}

// GuardOutboundTag is the direct outbound only guard rules use. REALITY
// closes its dest connection right after each handshake, but the outbound
// never half-closes towards the dest, so the guard inbound and its dest
// socket stay open until the outbound's "one direction ended" timeout: 5 s
// under BaseConfig's level 0, counted against the share's connection limit.
// guardUserLevel is a level BaseConfig never defines, so this outbound gets
// the Core's built-in 1 s timeouts (measured: dest socket closed after 1.0 s
// instead of 5.0 s). The level of the dokodemo inbound does not matter: the
// outbound runs the copy with its own level.
const GuardOutboundTag = GuardTagPrefix + "direct"

const guardUserLevel = 1

// GuardOutbound is a plain direct outbound apart from its policy level; the
// guard target is already the pinned dest address.
func GuardOutbound() map[string]any {
	return map[string]any{"tag": GuardOutboundTag, "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIP", "userLevel": guardUserLevel}}
}

// Rules match only the guard inbound and go before the safety rule: the
// destination is the pinned public dest REALITY dialed directly before (now
// through GuardOutbound instead of REALITY's own dial), and a
// domain-only match needs no DNS lookup of the sniffed name. The allow rule
// also requires the sniffed protocol "tls" (FLVX plan 128 fix): without it a
// no-SNI or non-TLS probe could match the dokodemo target itself.
func (g Guard) Rules(port int) []any {
	tag := GuardTag(port)
	domains := make([]string, 0, len(g.Names))
	for _, n := range g.Names {
		domains = append(domains, "full:"+n)
	}
	return []any{
		map[string]any{"type": "field", "ruleTag": tag + "-allow", "inboundTag": []string{tag}, "protocol": []string{"tls"}, "domain": domains, "outboundTag": GuardOutboundTag},
		map[string]any{"type": "field", "ruleTag": tag + "-block", "inboundTag": []string{tag}, "outboundTag": "block"},
	}
}

// GuardRules renders the rules of the given groups in port order.
func GuardRules(guards map[int]Guard) []any {
	ports := make([]int, 0, len(guards))
	for p := range guards {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	var out []any
	for _, p := range ports {
		out = append(out, guards[p].Rules(p)...)
	}
	return out
}

// WithGuardDest points a rendered VLESS Reality inbound at its guard on
// 127.0.0.1:port and defers its accept until the ClientHello arrived. Keys,
// serverNames, shortIds and the listening port stay as rendered.
func WithGuardDest(cfg map[string]any, port int) map[string]any {
	stream, _ := cfg["streamSettings"].(map[string]any)
	reality, _ := stream["realitySettings"].(map[string]any)
	if reality == nil {
		return cfg
	}
	copyReality := map[string]any{}
	for k, v := range reality {
		copyReality[k] = v
	}
	copyReality["dest"] = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	copyStream := map[string]any{}
	for k, v := range stream {
		copyStream[k] = v
	}
	copyStream["realitySettings"] = copyReality
	copyStream["sockopt"] = map[string]any{"customSockopt": []any{map[string]any{"system": "linux", "network": "tcp", "level": "6", "opt": "9", "value": guardDeferAccept, "type": "int"}}}
	out := map[string]any{}
	for k, v := range cfg {
		out[k] = v
	}
	out["streamSettings"] = copyStream
	return out
}

// GuardPortOf returns the guard port a rendered inbound config points at, or
// 0. Only the Helper renders a loopback dest: every dest FLVX sends is
// validated public, and the Helper pins it to a public address.
func GuardPortOf(config string) int {
	const marker = `"dest":"127.0.0.1:`
	i := strings.Index(config, marker)
	if i < 0 {
		return 0
	}
	rest := config[i+len(marker):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return 0
	}
	port, err := strconv.Atoi(rest[:end])
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}
