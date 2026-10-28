package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// TestFLVXUserLimitsLabServer serves this branch's Custom for the local
// end-to-end run with a real FLVX panel (cmd/mmwxc-helper
// TestFLVXUserLimitsPanelE2E starts it): the FLVX tenant API and the Helper
// upload endpoint on MMWXC_UL_LAB_LISTEN, backed by an isolated PostgreSQL
// database with one shared server (7, active). It writes the FLVX
// integration token to MMWXC_UL_LAB_INFO and stops when MMWXC_UL_LAB_STOP
// exists. GET /lab/counts tells how often FLVX called user-limits.
func TestFLVXUserLimitsLabServer(t *testing.T) {
	listen, info, stop := os.Getenv("MMWXC_UL_LAB_LISTEN"), os.Getenv("MMWXC_UL_LAB_INFO"), os.Getenv("MMWXC_UL_LAB_STOP")
	if listen == "" || info == "" || stop == "" {
		t.Skip("lab server for the FLVX panel E2E only")
	}
	a := flvxTestApp(t)
	ctx := context.Background()
	a.helperRate = map[string]time.Time{}
	a.connectionMetrics = map[string]connectionMetrics{}
	a.detailedConnections = map[string]serverDetailedConnectionRecord{}
	helperToken := os.Getenv("MMWXC_UL_LAB_HELPER_TOKEN")
	a.helperState.mu.Lock()
	a.helperState.data.Servers["7"] = helperServerIdentity{OfficialServerID: "7", CustomServerUUID: "uuid-7", HelperTokenHash: hashSecret(helperToken), MachineID: "machine-7"}
	a.helperState.mu.Unlock()
	if err := a.saveFLVXShare(ctx, 7, flvx.Config{PortMin: 31000, PortMax: 31999, ResetDay: 1, MemoryMB: 256, CPUPercent: 100}, "active", nil); err != nil {
		t.Fatal(err)
	}
	// The shared server's address is the landing namespace's.
	auditDeleteExec(t, a.flvxDB(), `UPDATE remote_servers SET ip_address='203.0.113.1' WHERE id=7`)
	auditDeleteExec(t, a.flvxDB(), `UPDATE remote_servers SET ip_address='203.0.113.8' WHERE id=8`)
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/token", "operator-token", nil)
	var token struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &token)
	raw, _ := json.Marshal(map[string]string{"flvx_token": token.Token})
	if err := os.WriteFile(info, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var calls, refused atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/custom/flvx/v1/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/user-limits") {
			calls.Add(1)
			rec := &statusRecorder{ResponseWriter: w, status: 200}
			a.flvxV1Handler(rec, r)
			if rec.status != 200 {
				refused.Add(1)
			}
			return
		}
		a.flvxV1Handler(w, r)
	})
	mux.HandleFunc("/api/custom/agent/connections", func(w http.ResponseWriter, r *http.Request) {
		// The lab Helper uploads every 2 s; skip the 1 s report limiter.
		a.connectionMu.Lock()
		delete(a.helperRate, "7")
		a.connectionMu.Unlock()
		a.helperDetailedConnectionsHandler(w, r)
	})
	mux.HandleFunc("/lab/counts", func(w http.ResponseWriter, r *http.Request) {
		s, _ := readFLVXShare(r.Context(), a.flvxDB(), 7, false)
		conn := 0
		for _, n := range s.Desired.Nodes {
			if n.ConnMode != "" {
				conn++
			}
		}
		var usage *flvx.UserLimitsUsage
		var budgets []flvx.UserLimitBudget
		a.flvxLimitsMu.Lock()
		if e := a.flvxLimits[7]; e != nil {
			usage, budgets = e.usage, e.request.Users
		}
		a.flvxLimitsMu.Unlock()
		writeJSON(w, 200, map[string]any{"user_limits_calls": calls.Load(), "user_limits_refused": refused.Load(), "desired_revision": s.Desired.Revision, "desired_nodes": len(s.Desired.Nodes), "conn_mode_nodes": conn, "summary": a.flvxUserLimitsSummary(7, a.flvxHelpers()[7]), "helper_usage": usage, "budgets": budgets})
	})
	l, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: mux}
	go server.Serve(l)
	defer server.Close()
	for {
		if _, err := os.Stat(stop); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
