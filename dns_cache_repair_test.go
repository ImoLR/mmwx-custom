package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 「修复 DNS 缓存」: the dns.cache.ensure command through the existing Custom → Helper channel. Admin only
// (operator endpoint), one at a time per server, one click a minute, short-lived, never re-sent.

func dnsCacheRepairStatus(id string, round int, repair *dnsCacheEnsure) agentStatus {
	status := dnsCacheHeartbeat(id, round, time.Now().UTC(), true)
	status.Helper.Version = "v0.7.26"
	status.Capabilities = append(append([]string(nil), status.Capabilities...), dnsCacheRepairAction)
	status.DNSCache.Repair = repair
	return status
}

func postDNSCacheRepair(t *testing.T, a *app, id, body string) (int, string, managementCommand) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/custom/servers/"+id+"/agent", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer operator-token")
	recorder := httptest.NewRecorder()
	a.agentManagementHandler(recorder, request, id)
	var response struct {
		Message string            `json:"message"`
		Command managementCommand `json:"command"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	return recorder.Code, response.Message, response.Command
}

const dnsCacheRepairBody = `{"action":"dns.cache.ensure"}`

func TestDNSCacheRepairCommandFlow(t *testing.T) {
	a, state := heartbeatFixture(t, 1)
	a.apiToken = "operator-token"
	// Helper v0.7.25 reports the cache but has no repair command: 「需要先升级 Helper」.
	postHeartbeat(t, a, "1", dnsCacheHeartbeat("1", 0, time.Now().UTC(), false), nil)
	if code, message, _ := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody); code != http.StatusBadRequest || !strings.Contains(message, "需要先升级 Helper") {
		t.Fatalf("old Helper: HTTP %d %q", code, message)
	}
	postHeartbeat(t, a, "1", dnsCacheRepairStatus("1", 1, nil), nil)
	if code, message, _ := postDNSCacheRepair(t, a, "1", `{"action":"dns.cache.ensure","payload":{"zones":["evil.example"]}}`); code != http.StatusBadRequest || !strings.Contains(message, "payload") {
		t.Fatalf("payload accepted: HTTP %d %q", code, message)
	}
	code, message, command := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody)
	if code != http.StatusAccepted || command.ID == "" || command.Action != "dns.cache.ensure" {
		t.Fatalf("first click: HTTP %d %q %+v", code, message, command)
	}
	if ttl := command.ExpiresAt.Sub(command.CreatedAt); ttl != dnsCacheRepairTTL {
		t.Fatalf("command lifetime = %s, want %s (a Helper that is away never runs it late)", ttl, dnsCacheRepairTTL)
	}
	// Queued, not yet picked up: one at a time.
	if code, message, _ := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody); code != http.StatusConflict || !strings.Contains(message, "正在修复") {
		t.Fatalf("second click while queued: HTTP %d %q", code, message)
	}
	// The Helper takes it on its next report.
	delivered := postHeartbeat(t, a, "1", dnsCacheRepairStatus("1", 2, nil), nil)
	if delivered == nil || delivered.ID != command.ID {
		t.Fatalf("command not delivered: %+v", delivered)
	}
	// Its result (claimed, running) arrives with the next report: the command leaves the queue for good.
	started := time.Now().UTC()
	result := managementResult{CommandID: command.ID, Action: dnsCacheRepairAction, Success: true, Data: json.RawMessage(`{"repair":{"command_id":"` + command.ID + `"}}`), CompletedAt: started}
	result.Signature, _ = signManagementResult(result, hashSecret("token-1"))
	running := &dnsCacheEnsure{CommandID: command.ID, HelperVersion: "v0.7.26", StartedAt: started}
	if next := postHeartbeat(t, a, "1", dnsCacheRepairStatus("1", 3, running), &result); next != nil {
		t.Fatalf("command sent again after its result: %+v", next)
	}
	if code, message, _ := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody); code != http.StatusConflict || !strings.Contains(message, "正在修复") {
		t.Fatalf("click while the Helper runs it: HTTP %d %q", code, message)
	}
	// Finished: the record and the fresh state are stored at once (not a heartbeat-only change).
	writes := stateWrites(state)
	finished := *running
	finished.FinishedAt, finished.Summary = started.Add(300*time.Millisecond), "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 1.1.1.1 8.8.8.8）无需改动"
	postHeartbeat(t, a, "1", dnsCacheRepairStatus("1", 4, &finished), nil)
	if stateWrites(state) != writes+1 {
		t.Fatal("finished repair record was not saved at once")
	}
	if disk := readStateFile(t, state.path).AgentStatuses["1"].DNSCache; disk == nil || disk.Repair == nil || disk.Repair.CommandID != command.ID || !strings.Contains(disk.Repair.Summary, "无需改动") {
		t.Fatalf("stored repair = %+v", disk)
	}
	if summary := state.coreModeSummaries()["1"].DNSCache; summary == nil || summary.Repair == nil || summary.Repair.CommandID != command.ID {
		t.Fatalf("card summary repair = %+v", summary)
	}
	// One click a minute.
	if code, message, _ := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody); code != http.StatusConflict || !strings.Contains(message, "刚刚执行过，请") || !strings.Contains(message, "秒后再试") {
		t.Fatalf("click inside the minute: HTTP %d %q", code, message)
	}
	state.mu.Lock()
	state.dnsCacheRepairAt["1"] = time.Now().UTC().Add(-dnsCacheRepairCooldown - time.Second)
	state.mu.Unlock()
	if code, message, _ := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody); code != http.StatusAccepted {
		t.Fatalf("click after the minute: HTTP %d %q", code, message)
	}
}

func TestDNSCacheRepairBlockedWhileOfflineOrUpgrading(t *testing.T) {
	a, state := heartbeatFixture(t, 1)
	a.apiToken = "operator-token"
	postHeartbeat(t, a, "1", dnsCacheRepairStatus("1", 0, nil), nil)
	// An upgrade is queued: the repair would land in the old or the new Helper.
	state.mu.Lock()
	state.data.ManagementCommands["1"] = []managementCommand{{ID: "upgrade", Action: "helper.update", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute)}}
	state.mu.Unlock()
	if code, message, _ := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody); code != http.StatusConflict || message != errUpgradeInProgress.Error() {
		t.Fatalf("during an upgrade: HTTP %d %q", code, message)
	}
	state.mu.Lock()
	state.data.ManagementCommands["1"] = nil
	// A run the Helper still calls running after 5 minutes no longer blocks (it bounds a run at 260 s).
	status := state.data.AgentStatuses["1"]
	status.DNSCache.Repair = &dnsCacheEnsure{CommandID: "old", StartedAt: time.Now().UTC().Add(-dnsCacheRepairRunMax - time.Second)}
	state.data.AgentStatuses["1"] = status
	state.mu.Unlock()
	if code, message, _ := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody); code != http.StatusAccepted {
		t.Fatalf("stale running record still blocks: HTTP %d %q", code, message)
	}
	// Helper silent past the stale timeout: refused, nothing queued.
	state.mu.Lock()
	state.data.ManagementCommands["1"] = nil
	state.dnsCacheRepairAt = nil
	status = state.data.AgentStatuses["1"]
	status.ReportedAt = time.Now().UTC().Add(-helperStaleTimeout - time.Second)
	state.data.AgentStatuses["1"] = status
	state.mu.Unlock()
	if code, message, _ := postDNSCacheRepair(t, a, "1", dnsCacheRepairBody); code != http.StatusBadRequest || !strings.Contains(message, "Helper 离线") {
		t.Fatalf("offline Helper: HTTP %d %q", code, message)
	}
	if queued := len(state.data.ManagementCommands["1"]); queued != 0 {
		t.Fatalf("refused click queued %d commands", queued)
	}
	// Admin only: a request without the operator credential is rejected before anything else.
	request := httptest.NewRequest(http.MethodPost, "/api/custom/servers/1/agent", strings.NewReader(dnsCacheRepairBody))
	recorder := httptest.NewRecorder()
	a.agentManagementHandler(recorder, request, "1")
	if recorder.Code != http.StatusUnauthorized && recorder.Code != http.StatusForbidden {
		t.Fatalf("anonymous click: HTTP %d", recorder.Code)
	}
}
