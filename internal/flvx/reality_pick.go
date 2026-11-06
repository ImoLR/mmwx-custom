package flvx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

// Reality 伪装域名测速 + 最快 IP 钉选 (contract reality-pick-v1, FLVX plan 227).
//
//   - reality_pin: the Helper pins every Reality dest of the shared FLVX Core
//     to the public IP with the lowest TCP connect time instead of the first
//     DNS answer, re-checks it, and reports the pins per dest
//     (Status.RealityPins). Client links never change.
//   - reality_probe: FLVX asks the share's Helper to test candidate
//     "host:443" targets from the server itself (3x-ui REALITY target scan
//     criteria). Custom relays the request with the Helper's next upload
//     answer and the result with the upload after it finished.
const (
	RealityPinVersion    = 1
	RealityPinCapability = "flvx.reality_pin.v1"

	RealityProbeVersion    = 1
	RealityProbeCapability = "flvx.reality_probe.v1"

	// MaxProbeTargets bounds one request; a Helper probes at most
	// ProbeConcurrency targets at once.
	MaxProbeTargets      = 64
	ProbeConcurrency     = 4
	ProbeMaxIPs          = 8
	ProbeDefaultTimeout  = 3 * time.Second
	ProbeMaxTimeout      = 5 * time.Second
	ProbeMinTimeout      = 500 * time.Millisecond
	MaxProbeServerNames  = 32
	MaxRealityPinReports = 64
)

// RealityPinStatus is one pinned (dest, IP) of the share: the nodes whose
// Reality dest is Host and that the Helper pins to PinnedIP. The times are
// TCP connect times of the last full check, -1 = the IP did not answer.
type RealityPinStatus struct {
	Host      string   `json:"host"`
	PinnedIP  string   `json:"pinned_ip"`
	PinnedMS  float64  `json:"pinned_ms"`
	BestIP    string   `json:"best_ip,omitempty"`
	BestMS    float64  `json:"best_ms"`
	CheckedAt int64    `json:"checked_at"`
	Nodes     []string `json:"nodes"`
	// Fallback: no candidate answered, the pin is the first DNS answer as
	// before the contract.
	Fallback bool `json:"fallback,omitempty"`
}

func validMS(v float64) bool { return v == -1 || v >= 0 && v <= 60000 && !math.IsNaN(v) }

// SanitizeRealityPins keeps the well-formed entries of a Helper report
// (bounded, never fatal: a malformed pin report must not stop billing).
func SanitizeRealityPins(in []RealityPinStatus) []RealityPinStatus {
	if len(in) == 0 {
		return nil
	}
	out := make([]RealityPinStatus, 0, len(in))
	for _, p := range in {
		if len(out) >= MaxRealityPinReports {
			break
		}
		host, port, err := net.SplitHostPort(p.Host)
		n, _ := strconv.Atoi(port)
		if err != nil || !ValidHost(host) || n < 1 || n > 65535 || net.ParseIP(p.PinnedIP) == nil || PrivateHost(p.PinnedIP) || !validMS(p.PinnedMS) || !validMS(p.BestMS) || p.CheckedAt < 0 {
			continue
		}
		if p.BestIP != "" && (net.ParseIP(p.BestIP) == nil || PrivateHost(p.BestIP)) {
			continue
		}
		nodes := []string{}
		for _, id := range p.Nodes {
			if ValidText(id, 64) && len(nodes) < MaxNodes {
				nodes = append(nodes, id)
			}
		}
		p.Nodes = nodes
		out = append(out, p)
	}
	return out
}

// ProbeRequest is what FLVX POSTs (contract §1).
type ProbeRequest struct {
	Targets   []string `json:"targets"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

// ProbeTarget normalises one "host[:port]" candidate: a lower-case DNS name
// (no IP literal: a Reality target is a name the client sends as SNI) and a
// port, 443 by default.
func ProbeTarget(target string) (string, bool) {
	target = strings.TrimSpace(target)
	host, port := target, "443"
	if h, p, err := net.SplitHostPort(target); err == nil {
		host, port = h, p
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host == "" || !ValidHost(host) || net.ParseIP(host) != nil || !strings.Contains(host, ".") || PrivateHost(host) {
		return "", false
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), true
}

// ValidateProbe normalises a request: targets deduplicated, 1–64, timeout
// clamped to 0.5–5 s (default 3 s).
func ValidateProbe(r ProbeRequest) (ProbeRequest, []ValidationError) {
	errs := []ValidationError{}
	if len(r.Targets) == 0 || len(r.Targets) > MaxProbeTargets {
		errs = append(errs, ValidationError{Field: "targets", Message: "1–64 targets required"})
		return r, errs
	}
	out := ProbeRequest{Targets: []string{}}
	seen := map[string]bool{}
	for i, t := range r.Targets {
		norm, ok := ProbeTarget(t)
		if !ok {
			errs = append(errs, ValidationError{Field: fmt.Sprintf("targets[%d]", i), Message: "target must be a public domain host:port"})
			continue
		}
		if !seen[norm] {
			seen[norm] = true
			out.Targets = append(out.Targets, norm)
		}
	}
	timeout := time.Duration(r.TimeoutMS) * time.Millisecond
	switch {
	case r.TimeoutMS == 0:
		timeout = ProbeDefaultTimeout
	case timeout < ProbeMinTimeout || timeout > ProbeMaxTimeout:
		errs = append(errs, ValidationError{Field: "timeout_ms", Message: "timeout_ms must be 500–5000"})
	}
	out.TimeoutMS = int(timeout / time.Millisecond)
	return out, errs
}

// ProbeJob is the signed request Custom hands the Helper.
type ProbeJob struct {
	ServerID  string   `json:"server_id"`
	ID        string   `json:"id"`
	Targets   []string `json:"targets"`
	TimeoutMS int      `json:"timeout_ms"`
}

type ProbeEnvelope struct {
	Job       ProbeJob `json:"job"`
	ExpiresAt int64    `json:"expires_at"`
	Signature string   `json:"signature"`
}

func (e ProbeEnvelope) signature(token string) string {
	key := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, key[:])
	b, _ := json.Marshal(struct {
		Job       ProbeJob
		ExpiresAt int64
	}{e.Job, e.ExpiresAt})
	mac.Write([]byte("mmwxc-flvx-reality-probe-v1\n"))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

func (e *ProbeEnvelope) Sign(token string) { e.Signature = e.signature(token) }

// Verify checks the signature, the server, the shape and that the job has not
// expired (at most 11 minutes ahead, as for the desired envelope).
func (e ProbeEnvelope) Verify(token, serverID string, now time.Time) bool {
	j := e.Job
	if j.ServerID != serverID || !ValidText(j.ID, 64) || len(j.Targets) == 0 || len(j.Targets) > MaxProbeTargets || j.TimeoutMS < int(ProbeMinTimeout/time.Millisecond) || j.TimeoutMS > int(ProbeMaxTimeout/time.Millisecond) {
		return false
	}
	for _, t := range j.Targets {
		if norm, ok := ProbeTarget(t); !ok || norm != t {
			return false
		}
	}
	return e.ExpiresAt > now.UnixMilli() && e.ExpiresAt <= now.Add(11*time.Minute).UnixMilli() && hmac.Equal([]byte(e.Signature), []byte(e.signature(token)))
}

// ProbeIP is one resolved address of a candidate with its TCP connect time
// (-1 = no answer within 1 s).
type ProbeIP struct {
	IP    string  `json:"ip"`
	TCPMS float64 `json:"tcp_ms"`
}

// ProbeResult is the contract §1 result of one candidate. Feasible = TLS 1.3
// + X25519 (or X25519MLKEM768) + h2 + a certificate valid for the SNI + only
// public addresses, as 3x-ui's REALITY target scan. LatencyMS is TCP connect
// plus TLS handshake to BestIP (3x-ui's latencyMs).
type ProbeResult struct {
	Target         string    `json:"target"`
	Feasible       bool      `json:"feasible"`
	Reason         string    `json:"reason,omitempty"`
	LatencyMS      float64   `json:"latency_ms"`
	BestIP         string    `json:"best_ip,omitempty"`
	IPs            []ProbeIP `json:"ips"`
	TLS13          bool      `json:"tls13"`
	TLSVersion     string    `json:"tls_version,omitempty"`
	X25519         bool      `json:"x25519"`
	CurveID        string    `json:"curve_id,omitempty"`
	H2             bool      `json:"h2"`
	CertValid      bool      `json:"cert_valid"`
	ServerNames    []string  `json:"server_names"`
	CertChainBytes int       `json:"cert_chain_bytes"`
}

// ProbeReport is what the Helper uploads: running while it probes, done with
// every result. Results are in request order.
type ProbeReport struct {
	ID         string        `json:"id"`
	State      string        `json:"state"`
	StartedAt  int64         `json:"started_at"`
	FinishedAt int64         `json:"finished_at,omitempty"`
	Results    []ProbeResult `json:"results,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// SanitizeProbeReport bounds a Helper report before Custom keeps it; nil for
// anything malformed.
func SanitizeProbeReport(r *ProbeReport) *ProbeReport {
	if r == nil || !ValidText(r.ID, 64) || r.State != "running" && r.State != "done" && r.State != "error" || len(r.Results) > MaxProbeTargets {
		return nil
	}
	out := *r
	out.Error = clip(out.Error, 200)
	out.Results = make([]ProbeResult, 0, len(r.Results))
	for _, p := range r.Results {
		norm, ok := ProbeTarget(p.Target)
		if !ok || norm != p.Target || !validMS(p.LatencyMS) || p.CertChainBytes < 0 || p.CertChainBytes > 1<<20 {
			return nil
		}
		if p.BestIP != "" && (net.ParseIP(p.BestIP) == nil || PrivateHost(p.BestIP) && p.Feasible) {
			return nil
		}
		p.Reason = clip(p.Reason, 200)
		p.TLSVersion, p.CurveID = clip(p.TLSVersion, 16), clip(p.CurveID, 32)
		ips := make([]ProbeIP, 0, len(p.IPs))
		for _, ip := range p.IPs {
			if net.ParseIP(ip.IP) != nil && validMS(ip.TCPMS) && len(ips) < ProbeMaxIPs {
				ips = append(ips, ip)
			}
		}
		p.IPs = ips
		names := []string{}
		for _, n := range p.ServerNames {
			if ValidHost(n) && net.ParseIP(n) == nil && len(names) < MaxProbeServerNames {
				names = append(names, n)
			}
		}
		p.ServerNames = names
		out.Results = append(out.Results, p)
	}
	return &out
}

func clip(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}
