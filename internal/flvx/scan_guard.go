package flvx

import (
	"net"
	"sort"
	"strings"
	"time"
)

// 禁止端口扫描 on shared servers (contract landing-scan-guard-v1 §9). The
// FLVX Core build with the Core feature scan_guard.v1 (§8) counts every
// outbound TCP dial of the listed inbound tags and blocks a tag by itself the
// moment it crosses a threshold. FLVX sends the same scan_guard object it
// sends its own landing agent, inside the share's desired state; the Helper
// passes it to the shared Core, reads the Core's hits and keeps a hit's node
// blocked until FLVX has recorded it (seen), then the node's enabled flag
// decides (disabled = stays blocked, enabled again = 解除禁用). mmwx's own
// Core, users and settings are never involved.
const (
	ScanGuardVersion = 1
	// ScanGuardCapability: the Helper applies Desired.ScanGuard and reports
	// Status.ScanGuard.
	ScanGuardCapability = "flvx.scan_guard.v1"
	// CoreScanGuardFeature is the Core's /v1/features entry.
	CoreScanGuardFeature = "scan_guard.v1"

	// MaxScanHits bounds the hits kept for FLVX (= the Core's ring).
	MaxScanHits = 256
	// MaxScanSeq keeps every seq exact in a JSON number (float64).
	MaxScanSeq = 1<<53 - 1

	maxScanThreshold = 100000
	maxScanWebPorts  = 16
)

// Core defaults (§3); FLVX always sends its own values, zero fields of an
// enabled guard fall back to these, as on FLVX's landing agent.
const (
	ScanDefaultHorizontal    = 100
	ScanDefaultHorizontalWeb = 500
	ScanDefaultWindowS       = 60
	ScanDefaultVertical      = 30
	ScanDefaultTotal         = 1000
	ScanDefaultTotalWindowS  = 300
)

var ScanDefaultWebPorts = []int{80, 443}

// ScanGuard is FLVX's 禁止端口扫描 setting for one share: the native
// Desired.scan_guard object. Identities are node tags ("flvxl-" + node id) of
// this desired state. WebPorts and Identities carry no omitempty so [] (no
// web ports) survives storage and an identical retry stays identical.
type ScanGuard struct {
	Enabled       bool     `json:"enabled"`
	Identities    []string `json:"identities"`
	Horizontal    int      `json:"horizontal,omitempty"`
	HorizontalWeb int      `json:"horizontal_web,omitempty"`
	WebPorts      []int    `json:"web_ports"`
	WindowS       int      `json:"window_s,omitempty"`
	Vertical      int      `json:"vertical,omitempty"`
	Total         int      `json:"total,omitempty"`
	TotalWindowS  int      `json:"total_window_s,omitempty"`
	// Seen is the highest hit seq FLVX has recorded for this share (it
	// disabled the node in the same step). A hit above it is the Helper's to
	// hold; a Core block at or below it follows the node's enabled flag.
	Seen int64 `json:"seen,omitempty"`
}

// ScanHit is one Core hit as reported to FLVX (ID = the FLVX node id).
type ScanHit struct {
	ID         string    `json:"id"`
	InboundTag string    `json:"inbound_tag"`
	Type       string    `json:"type"` // horizontal | vertical | total
	Count      int       `json:"count"`
	Port       int       `json:"port,omitempty"`
	Host       string    `json:"host,omitempty"`
	WindowS    int       `json:"window_s"`
	At         time.Time `json:"at"`
	Seq        int64     `json:"seq"`
}

// ScanGuardStatus is the Helper's report. State: off | on | core_update
// (the running FLVX Core build lacks scan_guard.v1: 「升级 FLVX Core」) |
// error. Hits are those FLVX has not recorded (seq > seen), oldest first;
// Blocked are the tags the Core blocks right now. CoreReady (Helper → Custom
// only) says the installed FLVX Core build lists scan_guard.v1; Custom
// advertises the share capability only then.
type ScanGuardStatus struct {
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	Hits      []ScanHit `json:"hits"`
	Blocked   []string  `json:"blocked,omitempty"`
	CoreReady bool      `json:"core_ready"`
}

// ScanGuardOn reports whether FLVX asked for the guard (absent = off).
func (d Desired) ScanGuardOn() bool { return d.ScanGuard != nil && d.ScanGuard.Enabled }

// ValidateScanGuard checks Desired.ScanGuard like FLVX's landing agent:
// identities are unique tags of this desired state's nodes; thresholds,
// windows and web ports only while enabled (0 = default), so a disabled rule
// with half-edited numbers never blocks a node push.
func ValidateScanGuard(d Desired) []ValidationError {
	g := d.ScanGuard
	if g == nil {
		return nil
	}
	errors := []ValidationError{}
	add := func(field, message string) {
		errors = append(errors, ValidationError{Field: "scan_guard." + field, Message: message})
	}
	if g.Seen < 0 || g.Seen > MaxScanSeq {
		add("seen", "seen must be 0–9007199254740991")
	}
	tags := map[string]bool{}
	for _, n := range d.Nodes {
		tags[n.Tag()] = true
	}
	if len(g.Identities) > len(d.Nodes) {
		add("identities", "more identities than nodes")
	} else {
		seen := map[string]bool{}
		for _, id := range g.Identities {
			if !tags[id] || seen[id] {
				add("identities", "identities must be unique node tags (flvxl-<node id>) of this desired state")
				break
			}
			seen[id] = true
		}
	}
	if !g.Enabled {
		return errors
	}
	for _, v := range []int{g.Horizontal, g.HorizontalWeb, g.Vertical, g.Total} {
		if v < 0 || v > maxScanThreshold {
			add("threshold", "thresholds must be 1–100000 (0 = default)")
			break
		}
	}
	for _, v := range []int{g.WindowS, g.TotalWindowS} {
		if v != 0 && (v < 10 || v > 3600) {
			add("window", "windows must be 10–3600 s (0 = default)")
			break
		}
	}
	if len(g.WebPorts) > maxScanWebPorts {
		add("web_ports", "at most 16 web ports")
	} else {
		ports := map[int]bool{}
		for _, p := range g.WebPorts {
			if p < 1 || p > 65535 || ports[p] {
				add("web_ports", "web ports must be unique 1–65535")
				break
			}
			ports[p] = true
		}
	}
	return errors
}

// CoreConfig is the Core's scan_guard object (§8.1) for the given tags,
// defaults filled in.
func (g ScanGuard) CoreConfig(identities []string) map[string]any {
	or := func(v, d int) int {
		if v > 0 {
			return v
		}
		return d
	}
	ports := g.WebPorts
	if ports == nil {
		ports = ScanDefaultWebPorts
	}
	return map[string]any{
		"enabled": true, "identities": identities,
		"horizontal": or(g.Horizontal, ScanDefaultHorizontal), "horizontal_web": or(g.HorizontalWeb, ScanDefaultHorizontalWeb),
		"web_ports": ports, "window_s": or(g.WindowS, ScanDefaultWindowS),
		"vertical": or(g.Vertical, ScanDefaultVertical),
		"total":    or(g.Total, ScanDefaultTotal), "total_window_s": or(g.TotalWindowS, ScanDefaultTotalWindowS),
	}
}

// ValidScanHit bounds one hit (Helper → Custom → FLVX).
func ValidScanHit(h ScanHit) bool {
	switch h.Type {
	case "horizontal", "vertical", "total":
	default:
		return false
	}
	host := h.Host == "" || len(h.Host) <= 255 && (net.ParseIP(h.Host) != nil || ValidText(h.Host, 255) && !strings.ContainsAny(h.Host, " /\\\"'"))
	return identifier.MatchString(h.ID) && h.InboundTag == "flvxl-"+h.ID && h.Count >= 0 && h.Count <= maxScanThreshold && h.Port >= 0 && h.Port <= 65535 && host && h.WindowS >= 0 && h.WindowS <= 3600 && h.Seq > 0 && h.Seq <= MaxScanSeq
}

// SanitizeScanGuardStatus bounds a Helper-reported status before Custom
// stores it: unknown state → nil (dropped, never fails the report), invalid
// or duplicate hits dropped, at most MaxScanHits (newest kept, oldest first),
// blocked tags sorted and bounded.
func SanitizeScanGuardStatus(s *ScanGuardStatus) *ScanGuardStatus {
	if s == nil {
		return nil
	}
	switch s.State {
	case "off", "on", "core_update", "error":
	default:
		return nil
	}
	out := &ScanGuardStatus{State: s.State, Hits: []ScanHit{}, CoreReady: s.CoreReady}
	if runes := []rune(strings.ToValidUTF8(s.Error, "")); len(runes) > 200 {
		out.Error = string(runes[:200])
	} else {
		out.Error = string(runes)
	}
	seen := map[int64]bool{}
	for _, h := range s.Hits {
		if ValidScanHit(h) && !seen[h.Seq] {
			seen[h.Seq] = true
			h.At = h.At.UTC()
			out.Hits = append(out.Hits, h)
		}
	}
	sort.Slice(out.Hits, func(i, j int) bool { return out.Hits[i].Seq < out.Hits[j].Seq })
	if len(out.Hits) > MaxScanHits {
		out.Hits = append([]ScanHit{}, out.Hits[len(out.Hits)-MaxScanHits:]...)
	}
	blocked := map[string]bool{}
	for _, tag := range s.Blocked {
		if len(blocked) < MaxNodes && strings.HasPrefix(tag, "flvxl-") && identifier.MatchString(strings.TrimPrefix(tag, "flvxl-")) {
			blocked[tag] = true
		}
	}
	for tag := range blocked {
		out.Blocked = append(out.Blocked, tag)
	}
	sort.Strings(out.Blocked)
	return out
}
