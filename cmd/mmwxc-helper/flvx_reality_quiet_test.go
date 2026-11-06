package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// timeWaitRows counts the TIME_WAIT sockets (state 06) with port on either
// end in /proc/net/tcp{,6}.
func timeWaitRows(t *testing.T, port int) int {
	t.Helper()
	hex := fmt.Sprintf(":%04X", port)
	n := 0
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		s := bufio.NewScanner(f)
		for s.Scan() {
			fields := strings.Fields(s.Text())
			if len(fields) > 3 && fields[3] == "06" && (strings.HasSuffix(fields[1], hex) || strings.HasSuffix(fields[2], hex)) {
				n++
			}
		}
		f.Close()
	}
	return n
}

// resetServer accepts on loopback (TLS when cfg is set), reads every
// connection until it ends and reports whether each ended with an RST.
func resetServer(t *testing.T, cfg *tls.Config) (string, int, chan bool) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	ended := make(chan bool, 64)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var r interface{ Read([]byte) (int, error) } = c
				if cfg != nil {
					tc := tls.Server(c, cfg)
					if tc.Handshake() != nil {
						ended <- errors.Is(errors.Unwrap(tc.Handshake()), syscall.ECONNRESET)
						return
					}
					r = tc
				}
				buf := make([]byte, 512)
				for {
					if _, err := r.Read(buf); err != nil {
						ended <- errors.Is(err, syscall.ECONNRESET)
						return
					}
				}
			}(c)
		}
	}()
	return l.Addr().String(), l.Addr().(*net.TCPAddr).Port, ended
}

func expectResets(t *testing.T, ended chan bool, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case rst := <-ended:
			if !rst {
				t.Fatalf("connection %d ended with a FIN, not an RST", i+1)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of %d connections ended", i, n)
		}
	}
}

// Pin/liveness connects and the 测速 probe close every socket with
// SO_LINGER 0: the far end sees an RST and no TIME_WAIT is left on either
// end (user 2026-11-06).
func TestFLVXRealitySocketsLeaveNoTimeWait(t *testing.T) {
	addr, port, ended := resetServer(t, nil)
	for i := 0; i < 3; i++ {
		if _, err := flvxConnectTime(context.Background(), addr); err != nil {
			t.Fatal(err)
		}
	}
	expectResets(t, ended, 3)
	if n := timeWaitRows(t, port); n != 0 {
		t.Fatalf("pin connects left %d TIME_WAIT sockets", n)
	}

	cert, roots := probeCert(t, "good.example")
	tlsAddr, tlsPort, tlsEnded := resetServer(t, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}})
	// Two IPs, the first one the only answer: one ping per IP plus the TLS
	// connection to the fastest.
	r := prober(roots, []string{"203.0.113.1", "203.0.113.2"}, map[string]string{"203.0.113.1": tlsAddr}, nil).probe(context.Background(), "good.example:443", time.Second)
	if !r.Feasible || r.BestIP != "203.0.113.1" {
		t.Fatalf("probe %+v", r)
	}
	expectResets(t, tlsEnded, 2)
	time.Sleep(50 * time.Millisecond)
	if n := timeWaitRows(t, tlsPort); n != 0 {
		t.Fatalf("probe left %d TIME_WAIT sockets", n)
	}
}

// The single (or fastest last) IP's connection carries the TLS handshake:
// no second connect.
func TestFLVXRealityProbeReusesLastFastest(t *testing.T) {
	cert, roots := probeCert(t, "good.example")
	addr := probeServer(t, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}})
	var dials int32
	r := prober(roots, []string{"203.0.113.1"}, map[string]string{"203.0.113.1": addr}, &dials).probe(context.Background(), "good.example:443", time.Second)
	if !r.Feasible || dials != 1 {
		t.Fatalf("dials=%d %+v", dials, r)
	}
}

// At most flvxRealitySockets (2) Reality sockets are open at once across
// the Helper: 2 probe targets (concurrency 2) with 4 IPs each plus a pin
// check of 8 candidates running at the same time.
func TestFLVXRealitySocketCap(t *testing.T) {
	if flvxRealitySockets != 2 {
		t.Fatalf("cap %d", flvxRealitySockets)
	}
	cert, roots := probeCert(t, "good.example")
	addr := probeServer(t, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}})
	var open, peak, total int32
	track := func() {
		now := atomic.AddInt32(&open, 1)
		atomic.AddInt32(&total, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if now <= old || atomic.CompareAndSwapInt32(&peak, old, now) {
				break
			}
		}
	}
	answers := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4"}
	p := flvxProber{
		roots: roots,
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			out := []net.IPAddr{}
			for _, a := range answers {
				out = append(out, net.IPAddr{IP: net.ParseIP(a)})
			}
			return out, nil
		},
		dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			track()
			var d net.Dialer
			c, err := d.DialContext(ctx, network, addr)
			if err != nil {
				atomic.AddInt32(&open, -1)
				return nil, err
			}
			time.Sleep(5 * time.Millisecond)
			return &countedConn{Conn: c, open: &open}, nil
		},
	}
	m, _ := fakeFLVXManager(t)
	m.connectTime = func(context.Context, string) (time.Duration, error) {
		track()
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&open, -1)
		return time.Millisecond, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := p.probe(context.Background(), "good.example:443", time.Second); !r.Feasible {
				t.Errorf("probe %+v", r)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.raceTCP(context.Background(), []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4", "203.0.113.5", "203.0.113.6", "203.0.113.7", "203.0.113.8"}, "443")
	}()
	wg.Wait()
	if peak > flvxRealitySockets || open != 0 {
		t.Fatalf("peak %d open sockets (cap %d), %d still open", peak, flvxRealitySockets, open)
	}
	// 4 probes × (4 pings + 1 TLS connection unless the last IP was the
	// fastest) + 8 pin connects.
	if total < 4*4+8 || total > 4*5+8 {
		t.Fatalf("%d sockets", total)
	}
}

type countedConn struct {
	net.Conn
	open *int32
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { atomic.AddInt32(c.open, -1) })
	return c.Conn.Close()
}
