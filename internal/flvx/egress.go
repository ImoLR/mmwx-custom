package flvx

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Landing egress v1 (contract /root/flvx-workers/contracts/landing-egress-v1.md):
// FLVX compiles its outbound templates and routing profiles into one flat
// plan per shared server. Custom validates it again; the Helper renders it into
// the FLVX Core and applies it hot through the Core API.
const EgressVersion = 1

const (
	MaxEgressOutbounds = 8
	MaxEgressRules     = 128
	MaxEgressItems     = 4000
	MaxEgressExempt    = 1024
	MaxRuleGeoBytes    = 5 << 19 // 2.5 MiB
	MaxPlanGeoBytes    = 3 << 20 // the Core API accepts 4 MiB per call
)

type Egress struct {
	Version       int              `json:"version"`
	Outbounds     []EgressOutbound `json:"outbounds"`
	Rules         []EgressRule     `json:"rules"`
	ExemptSources []string         `json:"exempt_sources,omitempty"`
}

type EgressOutbound struct {
	Tag       string           `json:"tag"`
	Type      string           `json:"type"`
	IPVersion string           `json:"ip_version"`
	Server    string           `json:"server,omitempty"`
	Port      int              `json:"port,omitempty"`
	Username  string           `json:"username,omitempty"`
	Password  string           `json:"password,omitempty"`
	Method    string           `json:"method,omitempty"`
	UUID      string           `json:"uuid,omitempty"`
	Flow      string           `json:"flow,omitempty"`
	Reality   *EgressReality   `json:"reality,omitempty"`
	WireGuard *EgressWireGuard `json:"wireguard,omitempty"`
}

type EgressReality struct {
	ServerName  string `json:"server_name"`
	PublicKey   string `json:"public_key"`
	ShortID     string `json:"short_id"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type EgressWireGuard struct {
	PrivateKey    string `json:"private_key"`
	PeerPublicKey string `json:"peer_public_key"`
	Endpoint      string `json:"endpoint,omitempty"`
	EndpointV4    string `json:"endpoint_v4,omitempty"`
	EndpointV6    string `json:"endpoint_v6,omitempty"`
	AddressV4     string `json:"address_v4,omitempty"`
	AddressV6     string `json:"address_v6,omitempty"`
	Reserved      []int  `json:"reserved,omitempty"`
	MTU           int    `json:"mtu,omitempty"`
}

type EgressRule struct {
	Name        string   `json:"name,omitempty"`
	InboundTags []string `json:"inbound_tags"`
	Domain      []string `json:"domain,omitempty"`
	IP          []string `json:"ip,omitempty"`
	Port        string   `json:"port,omitempty"`
	SourceIP    []string `json:"source_ip,omitempty"`
	SourcePort  string   `json:"source_port,omitempty"`
	Network     string   `json:"network,omitempty"`
	Protocol    []string `json:"protocol,omitempty"`
	Outbound    string   `json:"outbound"`
}

// EgressStatus is reported by the Helper: Versions/GeoIP/GeoSite are its
// capability (advertised in GET /shares, §2), the rest is the applied plan
// state (§6).
type EgressStatus struct {
	Versions        []int    `json:"versions"`
	GeoIP           bool     `json:"geoip"`
	GeoSite         bool     `json:"geosite"`
	State           string   `json:"state"`
	Error           string   `json:"error,omitempty"`
	AppliedRevision int64    `json:"applied_revision"`
	Warnings        []string `json:"warnings,omitempty"`
}

// Valid bounds a Helper-reported status before Custom stores it (§6: at most
// 10 warnings of 200 characters).
func (s *EgressStatus) Valid() bool {
	if s == nil {
		return true
	}
	if len(s.Versions) > 8 || len(s.Warnings) > 10 || len([]rune(s.Error)) > 200 || s.AppliedRevision < 0 {
		return false
	}
	switch s.State {
	case "none", "applied", "error":
	default:
		return false
	}
	for _, w := range s.Warnings {
		if len([]rune(w)) > 200 {
			return false
		}
	}
	return true
}

// Supports reports whether the Helper can apply plan version v.
func (s *EgressStatus) Supports(v int) bool {
	if s == nil {
		return false
	}
	for _, have := range s.Versions {
		if have == v {
			return true
		}
	}
	return false
}

// UnmarshalJSON ignores unknown keys of the egress object itself (contract
// §4.1, e.g. FLVX's native-only allow_core_restart) while outbounds and rules
// stay strict: an unknown field there would carry semantics Custom does not
// render.
func (e *Egress) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == nil {
		return errors.New("egress must be an object")
	}
	*e = Egress{}
	strict := func(key string, target any) error {
		v, ok := raw[key]
		if !ok {
			return nil
		}
		d := json.NewDecoder(bytes.NewReader(v))
		d.DisallowUnknownFields()
		return d.Decode(target)
	}
	for key, target := range map[string]any{"version": &e.Version, "outbounds": &e.Outbounds, "rules": &e.Rules, "exempt_sources": &e.ExemptSources} {
		if err := strict(key, target); err != nil {
			return err
		}
	}
	e.normalize()
	return nil
}

// normalize maps empty lists to nil so a JSON round trip (and therefore the
// idempotent-retry comparison and the Helper signature) is stable.
func (e *Egress) normalize() {
	nilEmpty := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return s
	}
	if len(e.Outbounds) == 0 {
		e.Outbounds = nil
	}
	if len(e.Rules) == 0 {
		e.Rules = nil
	}
	e.ExemptSources = nilEmpty(e.ExemptSources)
	for i := range e.Outbounds {
		if w := e.Outbounds[i].WireGuard; w != nil && len(w.Reserved) == 0 {
			w.Reserved = nil
		}
	}
	for i := range e.Rules {
		r := &e.Rules[i]
		r.InboundTags, r.Domain, r.IP, r.SourceIP, r.Protocol = nilEmpty(r.InboundTags), nilEmpty(r.Domain), nilEmpty(r.IP), nilEmpty(r.SourceIP), nilEmpty(r.Protocol)
	}
}

// Active reports whether the plan changes anything versus the built-in
// default (safety rule only, every node leaves through `direct`).
func (e *Egress) Active() bool { return e != nil && (len(e.Outbounds) > 0 || len(e.Rules) > 0) }

// CoveredTags lists inbound tags whose routing depends on the plan.
func (e *Egress) CoveredTags() map[string]bool {
	out := map[string]bool{}
	if !e.Active() {
		return out
	}
	for _, r := range e.Rules {
		for _, t := range r.InboundTags {
			out[t] = true
		}
	}
	return out
}

// GeoFiles lists the pinned geo data files the rules reference.
func (e *Egress) GeoFiles() []string {
	ip, site := false, false
	if e != nil {
		for _, r := range e.Rules {
			for _, d := range r.Domain {
				site = site || strings.HasPrefix(d, "geosite:")
			}
			for _, list := range [][]string{r.IP, r.SourceIP} {
				for _, v := range list {
					ip = ip || strings.HasPrefix(v, "geoip:")
				}
			}
		}
	}
	var out []string
	if ip {
		out = append(out, "geoip.dat")
	}
	if site {
		out = append(out, "geosite.dat")
	}
	return out
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
