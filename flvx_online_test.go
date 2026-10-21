package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func onlineTestReport() flvx.Report {
	count := 1
	ips := []flvx.OnlineIP{{IP: "192.0.2.7", Connections: 2}}
	return flvx.Report{Generation: "helper-one", Sequence: 1, Period: flvx.Period(time.Now(), 1), Status: flvx.Status{AppliedRevision: 1, Core: flvx.Core{State: "running"}, CollectedAt: time.Now().UnixMilli(), Nodes: []flvx.NodeStatus{{ID: "11", State: "active", OnlineIPCount: &count, OnlineIPs: &ips}}}}
}

func TestFLVXOnlineIPCacheIsolation(t *testing.T) {
	a := &app{}
	r := onlineTestReport()
	share := flvxShare{Generation: r.Generation, Sequence: r.Sequence, Status: r.Status}
	a.cacheFLVXOnlineIPs(7, r)
	for _, tc := range []struct {
		name   string
		change func(*flvxShare)
		id     int64
		known  bool
	}{
		{"matching", func(*flvxShare) {}, 7, true},
		{"other-server", func(*flvxShare) {}, 8, false},
		{"generation", func(s *flvxShare) { s.Generation = "helper-two" }, 7, false},
		{"sequence", func(s *flvxShare) { s.Sequence++ }, 7, false},
		{"collected-at", func(s *flvxShare) { s.Status.CollectedAt++ }, 7, false},
		{"other-node", func(s *flvxShare) { s.Status.Nodes[0].ID = "12" }, 7, false},
		{"removed", func(s *flvxShare) { s.Status.Nodes[0].State = "removed" }, 7, false},
		{"core-stopped", func(s *flvxShare) { s.Status.Core.State = "stopped" }, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := share
			s.Status.Nodes = append([]flvx.NodeStatus{}, share.Status.Nodes...)
			tc.change(&s)
			raw, _ := json.Marshal(a.flvxStatusWithOnlineIPs(tc.id, s))
			if strings.Contains(string(raw), `"online_ip_count"`) != tc.known || strings.Contains(string(raw), `"online_ips"`) != tc.known {
				t.Fatalf("wrong raw fields: %s", raw)
			}
		})
	}
	// Simulate a delayed cache write after the newer report transaction committed.
	newer := r
	newer.Sequence++
	newer.CollectedAt++
	newer.Nodes = append([]flvx.NodeStatus{}, r.Nodes...)
	ips := []flvx.OnlineIP{{IP: "198.51.100.7", Connections: 3}}
	newer.Nodes[0].OnlineIPs = &ips
	a.cacheFLVXOnlineIPs(7, newer)
	a.cacheFLVXOnlineIPs(7, r)
	share.Generation, share.Sequence, share.Status = newer.Generation, newer.Sequence, newer.Status
	raw, _ := json.Marshal(a.flvxStatusWithOnlineIPs(7, share))
	if strings.Contains(string(raw), "online_ip") {
		t.Fatal("older report leaked into newer status", string(raw))
	}
	a.cacheFLVXOnlineIPs(7, newer)
	got := a.flvxStatusWithOnlineIPs(7, share)
	if (*got.Nodes[0].OnlineIPs)[0].IP != "198.51.100.7" {
		t.Fatal("next report did not restore fields")
	}
	(*got.Nodes[0].OnlineIPs)[0].IP = "mutated"
	if (*a.flvxStatusWithOnlineIPs(7, share).Nodes[0].OnlineIPs)[0].IP != "198.51.100.7" {
		t.Fatal("read aliases cache")
	}
	a.flvxOnlineMu.Lock()
	stale := a.flvxOnline[7]
	stale.CollectedAt = time.Now().Add(-61 * time.Second).UnixMilli()
	a.flvxOnline[7] = stale
	a.flvxOnlineMu.Unlock()
	share.Status.CollectedAt = stale.CollectedAt
	raw, _ = json.Marshal(a.flvxStatusWithOnlineIPs(7, share))
	if strings.Contains(string(raw), "online_ip") {
		t.Fatal("stale cache served", string(raw))
	}
}

func TestFLVXOnlineIPPassthroughPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	if err := a.saveFLVXShare(ctx, 7, flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}, "active", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.putFLVXDesired(ctx, 7, flvxTestDesired()); err != nil {
		t.Fatal(err)
	}
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/token", "operator-token", nil)
	var token struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &token); err != nil || w.Code != 200 || token.Token == "" {
		t.Fatal("token setup failed", err)
	}
	r := onlineTestReport()
	for _, tc := range []struct {
		name        string
		change      func(*flvx.Report)
		count, list string
	}{
		{"accepted", func(*flvx.Report) {}, `"online_ip_count":1`, `"online_ips":[{"ip":"192.0.2.7","connections":2}]`},
		{"old-helper", func(r *flvx.Report) { r.Nodes[0].OnlineIPCount = nil; r.Nodes[0].OnlineIPs = nil }, "", ""},
		{"known-empty", func(r *flvx.Report) {
			zero := 0
			ips := []flvx.OnlineIP{}
			r.Nodes[0].OnlineIPCount = &zero
			r.Nodes[0].OnlineIPs = &ips
		}, `"online_ip_count":0`, `"online_ips":[]`},
		{"expired", func(r *flvx.Report) { r.CollectedAt = time.Now().Add(-61 * time.Second).UnixMilli() }, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.Sequence++
			tc.change(&r)
			if _, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, false, &r); err != nil {
				t.Fatal(err)
			}
			w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", token.Token, nil)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			raw := w.Body.String()
			if tc.count == "" {
				if strings.Contains(raw, `"online_ip_count"`) || strings.Contains(raw, `"online_ips"`) {
					t.Fatal("unknown keys present", raw)
				}
			} else if !strings.Contains(raw, tc.count) || !strings.Contains(raw, tc.list) {
				t.Fatal("online fields missing", raw)
			}
			for _, table := range []string{"mmwxc_flvx_counters", "mmwxc_flvx_shares"} {
				var ledger string
				if err := a.flvxDB().QueryRow(`SELECT data_json::text FROM ` + table + ` WHERE server_id=7`).Scan(&ledger); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(ledger, `"online_ip_count"`) || strings.Contains(ledger, `"online_ips"`) {
					t.Fatal("online fields persisted", table, ledger)
				}
			}
		})
	}
}

func TestFLVXOnlineIPCacheDoesNotWaitForDatabase(t *testing.T) {
	a := flvxTestApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := a.flvxDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before := a.flvxDB().Stats().WaitCount
	reportDone := make(chan error, 1)
	go func() {
		_, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, false, nil)
		reportDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for a.flvxDB().Stats().WaitCount == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if a.flvxDB().Stats().WaitCount == before {
		t.Fatal("report never waited for the occupied database connection")
	}
	cacheDone := make(chan struct{})
	go func() {
		r := onlineTestReport()
		a.cacheFLVXOnlineIPs(8, r)
		a.flvxStatusWithOnlineIPs(8, flvxShare{Generation: r.Generation, Sequence: r.Sequence, Status: r.Status})
		close(cacheDone)
	}()
	select {
	case <-cacheDone:
	case <-time.After(time.Second):
		t.Error("database wait holds the global online cache mutex")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-reportDone; err != nil {
		t.Fatal(err)
	}
}
