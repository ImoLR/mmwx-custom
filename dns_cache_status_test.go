package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var dnsCacheEnsuredAt = time.Date(2026, 11, 8, 23, 59, 0, 0, time.UTC)

func dnsCacheHeartbeat(id string, round int, checkedAt time.Time, ok bool) agentStatus {
	status := heartbeatStatus(id, round)
	status.Helper.Version = "v0.7.25"
	status.DNSCache = &dnsCacheStatus{Mode: "dnsmasq", OK: ok, CheckedAt: checkedAt,
		Ensure: &dnsCacheEnsure{HelperVersion: "v0.7.25", StartedAt: dnsCacheEnsuredAt, FinishedAt: dnsCacheEnsuredAt.Add(2 * time.Second), Summary: "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动"}}
	if !ok {
		status.DNSCache.Reason = "本机缓存 flvx-dnscache 未运行"
	}
	return status
}

// The Helper re-checks every 10 minutes: a new check time alone must not rewrite helper-state.json,
// a real change (ok → broken) must be saved at once, and the card summary carries it.
func TestDNSCacheRecheckIsHeartbeatOnlyAndShownOnCards(t *testing.T) {
	a, state := heartbeatFixture(t, 1)
	t0 := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	postHeartbeat(t, a, "1", dnsCacheHeartbeat("1", 0, t0, true), nil)
	if err := state.flushPending(); err != nil {
		t.Fatal(err)
	}
	writes := stateWrites(state)
	for round := 1; round <= 3; round++ {
		postHeartbeat(t, a, "1", dnsCacheHeartbeat("1", round, t0.Add(time.Duration(round)*10*time.Minute), true), nil)
	}
	if got := stateWrites(state); got != writes {
		t.Fatalf("10-minute re-checks wrote helper-state %d times", got-writes)
	}
	summary := state.coreModeSummaries()["1"]
	if summary.DNSCache == nil || !summary.DNSCache.OK || summary.DNSCache.Mode != "dnsmasq" || !summary.DNSCache.CheckedAt.Equal(t0.Add(30*time.Minute)) ||
		summary.DNSCache.Ensure == nil || !strings.Contains(summary.DNSCache.Ensure.Summary, "无需改动") {
		t.Fatalf("card summary dns_cache = %+v", summary.DNSCache)
	}
	// The summary is a copy: changing it cannot touch the stored status.
	summary.DNSCache.Ensure.Summary = "changed"
	if state.coreModeSummaries()["1"].DNSCache.Ensure.Summary == "changed" {
		t.Fatal("summary shares the stored dns_cache")
	}
	postHeartbeat(t, a, "1", dnsCacheHeartbeat("1", 4, t0.Add(40*time.Minute), false), nil)
	if got := stateWrites(state); got != writes+1 {
		t.Fatalf("broken DNS cache was not saved at once (writes %d → %d)", writes, got)
	}
	requireNothingDurablePending(t, state, "dns cache broken")
	if disk := readStateFile(t, state.path).AgentStatuses["1"].DNSCache; disk == nil || disk.OK || disk.Reason != "本机缓存 flvx-dnscache 未运行" {
		t.Fatalf("disk dns_cache = %+v", disk)
	}
}

func TestHeartbeatOnlyStatusChangeDNSCache(t *testing.T) {
	t0 := time.Now().UTC()
	base := dnsCacheHeartbeat("1", 1, t0, true)
	if !heartbeatOnlyStatusChange(base, dnsCacheHeartbeat("1", 2, t0.Add(10*time.Minute), true)) {
		t.Fatal("a re-check time counted as a real change")
	}
	for name, change := range map[string]func(*agentStatus){
		"ok":      func(s *agentStatus) { s.DNSCache.OK = false },
		"mode":    func(s *agentStatus) { s.DNSCache.Mode = "minimal" },
		"reason":  func(s *agentStatus) { s.DNSCache.Reason = "53 端口已被 unbound 占用" },
		"ensure":  func(s *agentStatus) { s.DNSCache.Ensure.ExitCode = 1 },
		"removed": func(s *agentStatus) { s.DNSCache = nil },
	} {
		changed := dnsCacheHeartbeat("1", 2, t0, true)
		change(&changed)
		if heartbeatOnlyStatusChange(base, changed) {
			t.Fatalf("dns_cache %s change was treated as heartbeat-only", name)
		}
	}
	if heartbeatOnlyStatusChange(heartbeatStatus("1", 1), base) {
		t.Fatal("first dns_cache report (Helper upgraded) was treated as heartbeat-only")
	}
}

func TestAgentStatusEndpointCarriesDNSCache(t *testing.T) {
	a, _ := heartbeatFixture(t, 1)
	a.apiToken = "operator-token"
	postHeartbeat(t, a, "1", dnsCacheHeartbeat("1", 0, time.Now().UTC(), true), nil)
	request := httptest.NewRequest(http.MethodGet, "/api/custom/servers/1/agent", nil)
	request.Header.Set("Authorization", "Bearer operator-token")
	recorder := httptest.NewRecorder()
	a.agentManagementHandler(recorder, request, "1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("agent status HTTP %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Status agentStatus `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status.DNSCache == nil || response.Status.DNSCache.Mode != "dnsmasq" || !response.Status.DNSCache.OK {
		t.Fatalf("agent status dns_cache = %+v (%s)", response.Status.DNSCache, recorder.Body.String())
	}
}
