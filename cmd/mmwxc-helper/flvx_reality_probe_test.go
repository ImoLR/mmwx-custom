package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// probeCert is a self-signed CA certificate for the given names.
func probeCert(t *testing.T, names ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// probeServer is a TLS listener on loopback that completes handshakes only.
func probeServer(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	l, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { c.(*tls.Conn).Handshake(); time.Sleep(50 * time.Millisecond); c.Close() }()
		}
	}()
	return l.Addr().String()
}

// prober resolves every name to the given public IPs and dials each IP at
// the mapped loopback address (missing = refused).
func prober(roots *x509.CertPool, answers []string, at map[string]string, dials *int32) flvxProber {
	return flvxProber{
		roots: roots,
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			out := []net.IPAddr{}
			for _, a := range answers {
				out = append(out, net.IPAddr{IP: net.ParseIP(a)})
			}
			return out, nil
		},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if dials != nil {
				atomic.AddInt32(dials, 1)
			}
			ip, _, _ := net.SplitHostPort(address)
			real, ok := at[ip]
			if !ok {
				return nil, errors.New("connection refused")
			}
			var d net.Dialer
			return d.DialContext(ctx, network, real)
		},
	}
}

func TestFLVXRealityProbeCriteria(t *testing.T) {
	cert, roots := probeCert(t, "good.example", "*.good.example", "alt.good.example")
	good := probeServer(t, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12})
	noH2 := probeServer(t, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}})
	tls12 := probeServer(t, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}, MaxVersion: tls.VersionTLS12})
	p256 := probeServer(t, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}, CurvePreferences: []tls.CurveID{tls.CurveP256}})
	ctx := context.Background()

	r := prober(roots, []string{"203.0.113.1", "203.0.113.2"}, map[string]string{"203.0.113.1": good}, nil).probe(ctx, "good.example:443", time.Second)
	if !r.Feasible || !r.TLS13 || !r.H2 || !r.X25519 || !r.CertValid || r.BestIP != "203.0.113.1" || r.CertChainBytes == 0 || r.Reason != "" || r.LatencyMS <= 0 || r.TLSVersion != "1.3" || r.CurveID != "X25519" {
		t.Fatalf("feasible target: %+v", r)
	}
	if len(r.IPs) != 2 || r.IPs[0].IP != "203.0.113.1" || r.IPs[0].TCPMS < 0 || r.IPs[1].TCPMS != -1 {
		t.Fatalf("per-IP times: %+v", r.IPs)
	}
	if strings.Join(r.ServerNames, ",") != "good.example,alt.good.example" {
		t.Fatalf("usable SANs: %v", r.ServerNames)
	}
	cases := map[string]struct {
		addr, target, reason string
	}{
		"no h2":     {noH2, "good.example:443", "HTTP/2"},
		"tls 1.2":   {tls12, "good.example:443", "TLS 1.3"},
		"no x25519": {p256, "good.example:443", "TLS handshake failed"},
		"wrong SNI": {good, "other.example:443", "certificate not valid"},
	}
	for name, c := range cases {
		r := prober(roots, []string{"203.0.113.1"}, map[string]string{"203.0.113.1": c.addr}, nil).probe(ctx, c.target, time.Second)
		if r.Feasible || !strings.Contains(r.Reason, c.reason) {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	// Untrusted chain (system roots only).
	if r := prober(nil, []string{"203.0.113.1"}, map[string]string{"203.0.113.1": good}, nil).probe(ctx, "good.example:443", time.Second); r.Feasible || r.CertValid {
		t.Fatalf("self-signed accepted: %+v", r)
	}
}

// A name with any private address is never dialled (SSRF / DNS rebinding);
// nothing answers = connection failed; at most 8 addresses are dialled.
func TestFLVXRealityProbeAddresses(t *testing.T) {
	var dials int32
	ctx := context.Background()
	r := prober(nil, []string{"203.0.113.1", "10.1.2.3"}, map[string]string{}, &dials).probe(ctx, "x.example:443", time.Second)
	if r.Feasible || r.Reason != "resolves to a private address" || dials != 0 {
		t.Fatalf("private: %+v dials=%d", r, dials)
	}
	many := []string{}
	for i := 1; i <= 12; i++ {
		many = append(many, "203.0.113."+string(rune('0'+i%10)))
	}
	dials = 0
	r = prober(nil, append(many, "2001:db8::1"), map[string]string{}, &dials).probe(ctx, "x.example:443", time.Second)
	if r.Reason != "connection failed" || dials != 8 || len(r.IPs) != 8 {
		t.Fatalf("dials=%d %+v", dials, r)
	}
}

// The job runs in the background with at most 4 targets at once; the report
// rides on uploads until Custom stops sending the job.
func TestFLVXRealityProbeJob(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	var running, peak int32
	release := make(chan struct{})
	m.probeTarget = func(_ context.Context, target string, _ time.Duration) flvx.ProbeResult {
		now := atomic.AddInt32(&running, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if now <= old || atomic.CompareAndSwapInt32(&peak, old, now) {
				break
			}
		}
		<-release
		atomic.AddInt32(&running, -1)
		return flvx.ProbeResult{Feasible: target == "a.example:443", LatencyMS: 12, IPs: []flvx.ProbeIP{}, ServerNames: []string{}}
	}
	targets := []string{}
	for _, h := range "abcdefghij" {
		targets = append(targets, string(h)+".example:443")
	}
	e := flvx.ProbeEnvelope{Job: flvx.ProbeJob{ServerID: "7", ID: "job-1", Targets: targets, TimeoutMS: 3000}, ExpiresAt: time.Now().Add(5 * time.Minute).UnixMilli()}
	bad := e
	bad.Sign("other-token")
	if err := m.submitProbe(&bad); err == nil || m.probeReport() != nil {
		t.Fatal("unsigned job accepted")
	}
	e.Sign("helper")
	if err := m.submitProbe(&e); err != nil {
		t.Fatal(err)
	}
	if r := m.probeReport(); r == nil || r.ID != "job-1" || r.State != "running" {
		t.Fatalf("running report %+v", r)
	}
	// The same job again (next upload answer) is not started twice.
	if err := m.submitProbe(&e); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200 && m.probeReport().State == "running"; i++ {
			time.Sleep(5 * time.Millisecond)
			if i == 20 {
				close(release)
			}
		}
	}()
	wg.Wait()
	r := m.probeReport()
	if r.State != "done" || len(r.Results) != 10 || r.Results[0].Target != "a.example:443" || !r.Results[0].Feasible || r.Results[1].Feasible || r.FinishedAt == 0 {
		t.Fatalf("done report %+v", r)
	}
	if peak > flvx.ProbeConcurrency {
		t.Fatalf("%d targets at once", peak)
	}
	if err := m.submitProbe(&e); err != nil || m.probeReport() == nil {
		t.Fatal("report dropped while Custom still sends the job")
	}
	m.submitProbe(nil)
	if m.probeReport() != nil {
		t.Fatal("report kept after Custom dropped the job")
	}
}
