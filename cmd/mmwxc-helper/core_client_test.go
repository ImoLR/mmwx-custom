package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func serveUnixHTTP(t *testing.T, handler http.Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "core.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = os.Remove(path)
	})
	return path
}

func TestCoreClientMalformedResponse(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"wrong"}`))
	}))
	if _, err := newCoreClient(path).snapshot(context.Background()); err == nil {
		t.Fatal("malformed Core response was accepted")
	}
}

func TestCoreClientAcceptsCurrentRejectionCounters(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"started_at":"2026-09-08T00:00:00Z","proxy_users":[{"identity":{"inbound_tag":"in-a","user":"user-a"},"attributed":true,"inbound_active":1,"inbound_total":2,"outbound_active":1,"outbound_pending":0,"outbound_new_total":3,"outbound_new_rate":1,"outbound_rejected_total":2,"rejected_active_limit":1,"rejected_new_rate_limit":1,"max_outbound_tcp_active":null,"max_outbound_tcp_new_per_second":null,"close_wait_timeout_seconds":null}]}`))
	}))
	snapshot, err := newCoreClient(path).snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Users) != 1 || snapshot.Users[0].RejectedActiveLimit != 1 || snapshot.Users[0].RejectedNewRateLimit != 1 {
		t.Fatalf("unexpected rejection counters: %#v", snapshot.Users)
	}
}

func TestCoreClientAcceptsV2TupleStatesAndGlobalLimit(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":2,"started_at":"2026-09-13T00:00:00Z","global":{"current_total":4,"max_total":20,"rejected_global_total_limit":1},"proxy_users":[{"identity":{"inbound_tag":"in-a","user":"user-a"},"attributed":true,"current_total":4,"inbound_active":2,"inbound_tcp":{"tcp_total":3,"established":2,"time_wait":1},"inbound_online_ips":[{"ip":"198.51.100.1","connections":2}],"outbound_active":2,"outbound_pending":0,"outbound_tcp":{"tcp_total":3,"established":2,"time_wait":1},"rejected_user_total_limit":1,"rejected_online_ip_limit":2,"rejected_global_total_limit":1}]}`))
	}))
	snapshot, err := newCoreClient(path).snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Global.CurrentTotal != 4 || snapshot.Global.MaxTotal == nil || *snapshot.Global.MaxTotal != 20 || len(snapshot.Users) != 1 || snapshot.Users[0].InboundTCP.TimeWait != 1 || snapshot.Users[0].OutboundTCP.TimeWait != 1 {
		t.Fatalf("v2 snapshot was not decoded: %#v", snapshot)
	}
}

func TestCoreClientUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sock")
	if _, err := newCoreClient(path).snapshot(context.Background()); err == nil {
		t.Fatal("missing Core socket was accepted")
	}
}

func TestCoreClientAppliesNullUnlimited(t *testing.T) {
	var received coreConfig
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/config" || r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	settings := defaultConnectionSettings()
	settings.Users = []userConnectionSettings{{Identity: coreIdentity{InboundTag: "in-a", User: "user-a"}}}
	if err := newCoreClient(path).apply(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if len(received.Limits) != 1 || received.Limits[0].MaxOutboundTCPActive != nil {
		t.Fatalf("null unlimited changed: %#v", received)
	}
}

func TestCoreClientReconnectsAfterSocketAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sock")
	client := newCoreClient(path)
	if _, err := client.snapshot(context.Background()); err == nil {
		t.Fatal("missing socket was accepted")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/snapshot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":1,"started_at":"2026-09-08T00:00:00Z","proxy_users":[]}`))
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	if _, err := client.snapshot(context.Background()); err != nil {
		t.Fatalf("client did not reconnect: %v", err)
	}
}

func TestCoreClientRejectsMalformedApplyResponse(t *testing.T) {
	path := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":"yes"}`))
	}))
	if err := newCoreClient(path).apply(context.Background(), defaultConnectionSettings()); err == nil {
		t.Fatal("malformed Core apply response was accepted")
	}
}
