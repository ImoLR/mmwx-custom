package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Reality 伪装域名测速 (contract reality-pick-v1 §1): FLVX's candidate
// "host:443" targets tested from this server, with the criteria of 3x-ui's
// REALITY target scan (internal/web/service/reality_scan.go). Custom hands
// the signed job over with an upload answer; it runs in the background (the
// upload loop never waits for it) and its report rides on the following
// uploads until Custom stops sending the job.
//
// Third-party sites: per target one DNS lookup, one TCP connect per public
// IPv4 address (at most 8, one at a time) and one TLS handshake to the
// fastest; at most 2 targets at once, no retries. All sockets share the
// Helper's quiet Reality socket cap (≤ 2 open, RST close, no TIME_WAIT).
// The limit stays below Custom's 3 min run limit (pickup + upload ≈ 10 s).
const flvxProbeJobLimit = 150 * time.Second

type flvxProbeState struct {
	mu      sync.Mutex
	current *flvx.ProbeReport
	running bool
}

// probeTarget is injected by tests; nil = flvxProbeTarget.
type flvxProbeFunc func(ctx context.Context, target string, timeout time.Duration) flvx.ProbeResult

// submitProbe starts a verified job unless it is the one already running or
// reported. A nil job (Custom has none for this share) ends the report.
func (m *flvxManager) submitProbe(e *flvx.ProbeEnvelope) error {
	if m == nil {
		return nil
	}
	m.probe.mu.Lock()
	defer m.probe.mu.Unlock()
	if e == nil {
		if !m.probe.running {
			m.probe.current = nil
		}
		return nil
	}
	if c := m.probe.current; c != nil && c.ID == e.Job.ID {
		return nil
	}
	if !e.Verify(m.config.Token, m.config.ServerID, m.now()) {
		return errors.New("invalid FLVX reality probe signature or expiry")
	}
	if m.probe.running {
		return errors.New("FLVX reality probe already running")
	}
	job := e.Job
	m.probe.current = &flvx.ProbeReport{ID: job.ID, State: "running", StartedAt: m.now().UnixMilli()}
	m.probe.running = true
	go m.runProbe(job)
	return nil
}

func (m *flvxManager) runProbe(job flvx.ProbeJob) {
	ctx, cancel := context.WithTimeout(context.Background(), flvxProbeJobLimit)
	defer cancel()
	probe := m.probeTarget
	if probe == nil {
		probe = flvxProbeTarget
	}
	timeout := time.Duration(job.TimeoutMS) * time.Millisecond
	results := make([]flvx.ProbeResult, len(job.Targets))
	sem := make(chan struct{}, flvx.ProbeConcurrency)
	var wg sync.WaitGroup
	for i, target := range job.Targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, target string) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				results[i] = flvx.ProbeResult{Target: target, Reason: "probe deadline reached", IPs: []flvx.ProbeIP{}, ServerNames: []string{}}
				return
			}
			results[i] = probe(ctx, target, timeout)
			results[i].Target = target
		}(i, target)
	}
	wg.Wait()
	m.probe.mu.Lock()
	defer m.probe.mu.Unlock()
	m.probe.running = false
	if m.probe.current == nil || m.probe.current.ID != job.ID {
		return
	}
	report := *m.probe.current
	report.State, report.FinishedAt, report.Results = "done", m.now().UnixMilli(), results
	m.probe.current = &report
}

// probeReport is the upload's flvx_reality_probe, nil without a job.
func (m *flvxManager) probeReport() *flvx.ProbeReport {
	if m == nil {
		return nil
	}
	m.probe.mu.Lock()
	defer m.probe.mu.Unlock()
	if m.probe.current == nil {
		return nil
	}
	r := *m.probe.current
	return &r
}

func flvxTLSVersion(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS10:
		return "1.0"
	}
	return "unknown"
}

func flvxMS(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*10) / 10 }

// flvxProber is one target test; tests inject the resolver, the dialer and
// the trusted roots (nil = system roots).
type flvxProber struct {
	lookup func(context.Context, string) ([]net.IPAddr, error)
	dial   func(context.Context, string, string) (net.Conn, error)
	roots  *x509.CertPool
}

func flvxProbeTarget(ctx context.Context, target string, timeout time.Duration) flvx.ProbeResult {
	return flvxProber{}.probe(ctx, target, timeout)
}

// probe tests one normalised "host:port".
func (p flvxProber) probe(ctx context.Context, target string, timeout time.Duration) flvx.ProbeResult {
	lookup, dial := p.lookup, p.dial
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	if dial == nil {
		var d net.Dialer
		dial = d.DialContext
	}
	host, port, _ := net.SplitHostPort(target)
	res := flvx.ProbeResult{Target: target, IPs: []flvx.ProbeIP{}, ServerNames: []string{}}
	dnsCtx, cancel := context.WithTimeout(ctx, flvxPinDNS)
	addrs, err := lookup(dnsCtx, host)
	cancel()
	if err != nil {
		res.Reason = "DNS lookup failed"
		return res
	}
	ips := []string{}
	for _, a := range addrs {
		ip := a.IP.String()
		if flvx.PrivateHost(ip) {
			res.Reason = "resolves to a private address"
			return res
		}
		if a.IP.To4() != nil && len(ips) < flvx.ProbeMaxIPs {
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		res.Reason = "no public IPv4 address"
		return res
	}
	// One connect per IP, one at a time, each closed with an RST right away.
	// The TLS handshake goes to the fastest IP: on the last IP's connection
	// when that one is the fastest (always for a single IP), otherwise on a
	// new connection.
	ms := map[string]float64{}
	bestIP := ""
	var conn net.Conn
	var elapsed time.Duration
	var closeConn func()
	for i, ip := range ips {
		c, took, closeC, err := flvxQuietDial(ctx, dial, net.JoinHostPort(ip, port), flvxPinDial)
		if err != nil {
			ms[ip] = -1
			continue
		}
		ms[ip] = flvxMS(took)
		faster := bestIP == "" || took < elapsed
		if faster {
			bestIP, elapsed = ip, took
		}
		if faster && i == len(ips)-1 {
			conn, closeConn = c, closeC
		} else {
			closeC()
		}
	}
	for _, ip := range ips {
		res.IPs = append(res.IPs, flvx.ProbeIP{IP: ip, TCPMS: ms[ip]})
	}
	sort.SliceStable(res.IPs, func(i, j int) bool {
		a, b := res.IPs[i].TCPMS, res.IPs[j].TCPMS
		return a >= 0 && (b < 0 || a < b)
	})
	if bestIP == "" {
		res.Reason = "connection failed"
		return res
	}
	res.BestIP = bestIP
	if conn == nil {
		var err error
		if conn, elapsed, closeConn, err = flvxQuietDial(ctx, dial, net.JoinHostPort(bestIP, port), flvxPinDial); err != nil {
			res.Reason = "connection failed"
			return res
		}
	}
	defer closeConn()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	start := time.Now()
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, // verified below, with and without the name
		NextProtos:         []string{"h2", "http/1.1"},
		// Only X25519 is offered: Go 1.24 does not expose the negotiated
		// group, and a completed handshake then proves X25519 (what REALITY
		// needs; 3x-ui also accepts X25519MLKEM768, which always carries it).
		CurvePreferences: []tls.CurveID{tls.X25519},
		MinVersion:       tls.VersionTLS12,
	})
	hsCtx, cancel := context.WithTimeout(ctx, timeout)
	err = tlsConn.HandshakeContext(hsCtx)
	cancel()
	if err != nil {
		res.Reason = "TLS handshake failed: " + flvxShortError(err)
		return res
	}
	res.LatencyMS = flvxMS(elapsed + time.Since(start))
	st := tlsConn.ConnectionState()
	res.TLS13 = st.Version == tls.VersionTLS13
	res.TLSVersion = flvxTLSVersion(st.Version)
	res.H2 = st.NegotiatedProtocol == "h2"
	res.CurveID, res.X25519 = "X25519", true
	if len(st.PeerCertificates) == 0 {
		res.Reason = "no certificate presented"
		return res
	}
	leaf := st.PeerCertificates[0]
	for _, c := range st.PeerCertificates {
		res.CertChainBytes += len(c.Raw)
	}
	for _, name := range leaf.DNSNames {
		if name = strings.TrimSpace(name); name != "" && !strings.HasPrefix(name, "*.") && len(res.ServerNames) < flvx.MaxProbeServerNames {
			res.ServerNames = append(res.ServerNames, name)
		}
	}
	opts := x509.VerifyOptions{Intermediates: x509.NewCertPool(), DNSName: host, Roots: p.roots}
	for _, c := range st.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	if _, err := leaf.Verify(opts); err == nil {
		res.CertValid = true
	} else {
		res.Reason = "certificate not valid for " + host
	}
	res.Feasible = res.TLS13 && res.H2 && res.X25519 && res.CertValid
	if !res.Feasible && res.Reason == "" {
		switch {
		case !res.TLS13:
			res.Reason = "server does not negotiate TLS 1.3"
		case !res.H2:
			res.Reason = "server does not negotiate HTTP/2 (h2)"
		case !res.X25519:
			res.Reason = "server did not use X25519 key exchange"
		}
	}
	return res
}

func flvxShortError(err error) string {
	text := err.Error()
	if r := []rune(text); len(r) > 120 {
		text = string(r[:120])
	}
	return text
}
