package flvx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// LocalAddrs is the kernel-selected source address per family; forced
// outbounds bind to it. The zero Addr means the family is unavailable.
type LocalAddrs struct{ V4, V6 netip.Addr }

const ipBindAddressNoPort = "24" // IP_BIND_ADDRESS_NO_PORT at level SOL_IP (0)

const (
	WARPEndpointV4 = "162.159.192.1:2408"
	WARPEndpointV6 = "[2606:4700:d0::a29f:c001]:2408"
)

var freedomStrategy = map[string]string{"system": "UseIP", "prefer_ipv4": "UseIPv4v6", "prefer_ipv6": "UseIPv6v4", "force_ipv4": "ForceIPv4", "force_ipv6": "ForceIPv6"}
var dialStrategy = map[string]string{"prefer_ipv4": "UseIPv4v6", "prefer_ipv6": "UseIPv6v4", "force_ipv4": "ForceIPv4", "force_ipv6": "ForceIPv6"}
var wireguardStrategy = map[string]string{"system": "ForceIP", "prefer_ipv4": "ForceIPv4v6", "prefer_ipv6": "ForceIPv6v4", "force_ipv4": "ForceIPv4", "force_ipv6": "ForceIPv6"}

// SafetyRule is always the first routing rule; no plan can override it.
func SafetyRule() map[string]any {
	return map[string]any{"type": "field", "ruleTag": "flvx-safety", "ip": BlockCIDRs, "outboundTag": "block"}
}

func Blackhole(tag string) map[string]any { return map[string]any{"tag": tag, "protocol": "blackhole"} }

// CanonicalJSON marshals with sorted map keys (encoding/json's map order).
func CanonicalJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// CoreTag is the plan tag plus a content hash, so any change of the rendered
// outbound (including a new local address) yields a new tag (§5.4/§5.6).
func CoreTag(tag string, rendered map[string]any) string {
	sum := sha256.Sum256([]byte(CanonicalJSON(rendered)))
	return tag + "-" + hex.EncodeToString(sum[:4])
}

func forceBind(ipVersion string, local LocalAddrs) (string, bool) {
	switch ipVersion {
	case "force_ipv4":
		return local.V4.String(), local.V4.IsValid()
	case "force_ipv6":
		return local.V6.String(), local.V6.IsValid()
	}
	return "", true
}

// RenderOutbound converts one validated plan outbound into Core JSON (§5.4),
// byte-compatible with FLVX's native landing renderer. Forced modes bind to a
// local address of that family: the Go dialer then refuses other-family IP
// literals and UDP sockets become single-family. A forced outbound on a server
// without that family becomes a blackhole, so nothing leaves over the other
// family. For WireGuard the IP version is the family inside the tunnel, fixed
// by its interface addresses.
func RenderOutbound(o EgressOutbound, local LocalAddrs) (map[string]any, string) {
	bind, ok := "", true
	if o.Type != "wireguard" {
		bind, ok = forceBind(o.IPVersion, local)
	}
	if !ok {
		family := "IPv4"
		if o.IPVersion == "force_ipv6" {
			family = "IPv6"
		}
		return Blackhole(o.Tag), fmt.Sprintf("%s: 服务器没有可用的 %s 地址，强制 %s 的连接会失败", o.Tag, family, family)
	}
	out := map[string]any{"tag": o.Tag}
	sockopt := map[string]any{}
	if bind != "" {
		out["sendThrough"] = bind
		sockopt["customSockopt"] = []any{map[string]any{"system": "linux", "network": "tcp", "level": "0", "opt": ipBindAddressNoPort, "value": "1", "type": "int"}}
	}
	if s, ok := dialStrategy[o.IPVersion]; ok && o.Type != "direct" && o.Type != "wireguard" {
		sockopt["domainStrategy"] = s
	}
	stream := map[string]any{}
	switch o.Type {
	case "direct":
		out["protocol"] = "freedom"
		out["settings"] = map[string]any{"domainStrategy": freedomStrategy[o.IPVersion]}
	case "socks", "http":
		out["protocol"] = o.Type
		server := map[string]any{"address": o.Server, "port": o.Port}
		if o.Username != "" || o.Password != "" {
			server["users"] = []any{map[string]any{"user": o.Username, "pass": o.Password}}
		}
		out["settings"] = map[string]any{"servers": []any{server}}
	case "shadowsocks":
		out["protocol"] = "shadowsocks"
		out["settings"] = map[string]any{"servers": []any{map[string]any{"address": o.Server, "port": o.Port, "method": o.Method, "password": o.Password}}}
	case "vless":
		out["protocol"] = "vless"
		user := map[string]any{"id": o.UUID, "encryption": "none"}
		if o.Flow != "" {
			user["flow"] = o.Flow
		}
		out["settings"] = map[string]any{"vnext": []any{map[string]any{"address": o.Server, "port": o.Port, "users": []any{user}}}}
		fp := o.Reality.Fingerprint
		if fp == "" {
			fp = "chrome"
		}
		stream["network"] = "tcp"
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{"serverName": o.Reality.ServerName, "publicKey": o.Reality.PublicKey, "shortId": o.Reality.ShortID, "fingerprint": fp}
	case "wireguard":
		w := o.WireGuard
		var addresses []string
		if w.AddressV4 != "" && o.IPVersion != "force_ipv6" {
			p, _ := parsePrefix(w.AddressV4)
			addresses = append(addresses, netip.PrefixFrom(p.Addr(), 32).String())
		}
		if w.AddressV6 != "" && o.IPVersion != "force_ipv4" {
			p, _ := parsePrefix(w.AddressV6)
			addresses = append(addresses, netip.PrefixFrom(p.Addr(), 128).String())
		}
		endpoint := w.Endpoint
		if endpoint == "" {
			v4, v6 := w.EndpointV4, w.EndpointV6
			if v4 == "" {
				v4 = WARPEndpointV4
			}
			if v6 == "" {
				v6 = WARPEndpointV6
			}
			switch {
			case local.V4.IsValid():
				endpoint = v4
			case local.V6.IsValid():
				endpoint = v6
			default:
				return Blackhole(o.Tag), fmt.Sprintf("%s: 服务器没有可用的公网地址连接 WARP", o.Tag)
			}
		}
		mtu := w.MTU
		if mtu == 0 {
			mtu = 1280
		}
		// No keepalive: an idle WARP outbound sends nothing.
		settings := map[string]any{"secretKey": w.PrivateKey, "address": addresses, "peers": []any{map[string]any{"publicKey": w.PeerPublicKey, "endpoint": endpoint, "allowedIPs": []string{"0.0.0.0/0", "::/0"}}}, "mtu": mtu, "domainStrategy": wireguardStrategy[o.IPVersion], "noKernelTun": true}
		if len(w.Reserved) == 3 {
			settings["reserved"] = w.Reserved
		}
		out["protocol"] = "wireguard"
		out["settings"] = settings
	}
	if len(sockopt) > 0 {
		stream["sockopt"] = sockopt
	}
	if len(stream) > 0 {
		out["streamSettings"] = stream
	}
	return out, ""
}

// GeoIPReader expands one geoip category; tests replace it.
type GeoIPReader func(code string) ([]netip.Prefix, error)

func sourceSet(entries []string, read GeoIPReader) (dualSet, error) {
	var direct []netip.Prefix
	out := dualSet{}
	sets := []dualSet{}
	for _, v := range entries {
		if code, ok := strings.CutPrefix(v, "geoip:"); ok {
			reverse := strings.HasPrefix(code, "!")
			prefixes, err := read(strings.TrimPrefix(code, "!"))
			if err != nil {
				return out, err
			}
			s := newDualSet(prefixes)
			if reverse {
				s = s.complement()
			}
			sets = append(sets, s)
			continue
		}
		p, err := parsePrefix(v)
		if err != nil {
			return out, err
		}
		direct = append(direct, p)
	}
	out = newDualSet(direct)
	for _, s := range sets {
		out = out.union(s)
	}
	return out, nil
}

// SourceMemo keeps expanded source lists between renders: geoip:cn minus the
// exemption is ~10k CIDRs and would otherwise re-read the 16 MiB geoip.dat on
// every periodic re-render. The pinned geo data never changes inside one
// process, so (entries, exemption) fully determine the result. Only entries
// the latest render used are kept.
type SourceMemo struct{ prev, next map[string]sourceResult }

type sourceResult struct {
	list []string
	drop bool
}

func (s *SourceMemo) get(key string) (sourceResult, bool) {
	if s == nil {
		return sourceResult{}, false
	}
	if r, ok := s.next[key]; ok {
		return r, true
	}
	r, ok := s.prev[key]
	return r, ok
}

func (s *SourceMemo) put(key string, r sourceResult) {
	if s == nil {
		return
	}
	if s.next == nil {
		s.next = map[string]sourceResult{}
	}
	s.next[key] = r
}

// Rotate keeps only what the finished render used; Discard drops a failed one.
func (s *SourceMemo) Rotate() {
	if s != nil {
		s.prev, s.next = s.next, nil
	}
}
func (s *SourceMemo) Discard() {
	if s != nil {
		s.next = nil
	}
}

// RenderRules returns the routing rules in order (§5.2): the immutable safety
// rule first, then the plan rules with outbound references already mapped by
// the caller. A source list that overlaps the exempt sources is rewritten to
// the exact CIDR cover of the remainder (§5.3); a rule whose whole source set
// is exempt is dropped, because an empty sourceIP would match every source.
func RenderRules(e *Egress, read GeoIPReader, memo *SourceMemo) ([]any, error) {
	rules := []any{SafetyRule()}
	if e == nil {
		return rules, nil
	}
	exemptPrefixes := make([]netip.Prefix, 0, len(e.ExemptSources))
	for _, s := range e.ExemptSources {
		p, err := parsePrefix(s)
		if err != nil {
			return nil, err
		}
		exemptPrefixes = append(exemptPrefixes, p)
	}
	exempt := newDualSet(exemptPrefixes)
	exemptKey := strings.Join(exempt.cidrs(), ",")
	for i, r := range e.Rules {
		rule := map[string]any{"type": "field", "ruleTag": "flvxr-" + strconv.Itoa(i+1), "outboundTag": r.Outbound, "inboundTag": sortedUnique(r.InboundTags)}
		if len(r.Domain) > 0 {
			rule["domain"] = r.Domain
		}
		if len(r.IP) > 0 {
			rule["ip"] = r.IP
		}
		if len(r.SourceIP) > 0 {
			list := r.SourceIP
			if !exempt.empty() {
				key := strings.Join(r.SourceIP, "\n") + "\x00" + exemptKey
				res, ok := memo.get(key)
				if !ok {
					set, err := sourceSet(r.SourceIP, read)
					if err != nil {
						return nil, fmt.Errorf("rule %d: %w", i+1, err)
					}
					res = sourceResult{list: r.SourceIP}
					if set.intersects(exempt) {
						set = set.subtract(exempt)
						if set.empty() {
							res = sourceResult{drop: true}
						} else {
							res = sourceResult{list: set.cidrs()}
						}
					}
				}
				memo.put(key, res)
				if res.drop {
					continue
				}
				list = res.list
			}
			rule["sourceIP"] = list
		}
		if r.Port != "" {
			rule["port"] = r.Port
		}
		if r.SourcePort != "" {
			rule["sourcePort"] = r.SourcePort
		}
		if r.Network != "" {
			rule["network"] = r.Network
		}
		if len(r.Protocol) > 0 {
			rule["protocol"] = r.Protocol
		}
		rules = append(rules, rule)
	}
	return rules, nil
}
