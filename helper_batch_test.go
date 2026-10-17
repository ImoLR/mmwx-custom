package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func batchFixture(t *testing.T, count int) (*helperState, []helperBatchServer, cachedReleaseInfo, time.Time) {
	t.Helper()
	s, err := openHelperState(filepath.Join(t.TempDir(), "state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	servers := []helperBatchServer{}
	for i := 1; i <= count; i++ {
		id := fmt.Sprint(i)
		servers = append(servers, helperBatchServer{ID: id, Name: "Fixture " + id, Status: "connected"})
		s.data.Servers[id] = helperServerIdentity{HelperTokenHash: hashSecret("fixture")}
		s.data.AgentStatuses[id] = agentStatus{Helper: componentStatus{Installed: true, Version: "v0.7.1"}, Architecture: "amd64", ReportedAt: now, Capabilities: []string{"helper.update"}}
	}
	release := cachedReleaseInfo{HelperVersion: "v0.7.4", HelperArtifacts: map[string]releaseArtifact{"amd64": {URL: "https://github.com/ImoLR/mmwx-custom/releases/download/v1.4.57/mmwxc-helper-linux-amd64", SHA256: strings.Repeat("a", 64)}}}
	return s, servers, release, now
}

func startBatchFixture(t *testing.T, s *helperState, servers []helperBatchServer, release cachedReleaseInfo, now time.Time) *helperBatchJob {
	t.Helper()
	ids := []string{}
	for _, server := range servers {
		ids = append(ids, server.ID)
	}
	job, err := s.startHelperBatch(servers, release, ids, now)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestHelperBatchSelection(t *testing.T) {
	s, servers, release, now := batchFixture(t, 8)
	servers[1].Status = "offline"
	status := s.data.AgentStatuses["3"]
	status.Helper.Installed = false
	s.data.AgentStatuses["3"] = status
	status = s.data.AgentStatuses["4"]
	status.Helper.Version = release.HelperVersion
	s.data.AgentStatuses["4"] = status
	status = s.data.AgentStatuses["5"]
	status.Architecture = "mips"
	s.data.AgentStatuses["5"] = status
	status = s.data.AgentStatuses["6"]
	status.Update = dispatchedUpdate("core", "abc", now)
	s.data.AgentStatuses["6"] = status
	status = s.data.AgentStatuses["7"]
	status.Helper.Version = "v0.1.0"
	status.Capabilities = nil
	s.data.AgentStatuses["7"] = status
	status = s.data.AgentStatuses["8"]
	status.Helper.Version = "v0.1.0"
	s.data.AgentStatuses["8"] = status
	rows := s.helperBatchPreviewLocked(servers, release, now)
	for i, row := range rows {
		eligible := i == 0 || i == 7
		if (row.Phase == "queued") != eligible {
			t.Fatalf("row %d: %#v", i, row)
		}
		if !eligible && row.Reason == "" {
			t.Fatal("missing skip reason")
		}
	}
	if !rows[6].FirstUpgrade || !rows[7].FirstUpgrade {
		t.Fatal("legacy upgrade not identified")
	}
	job, err := s.startHelperBatch(servers, release, []string{"1"}, now)
	if err != nil || job.Rows[7].Phase != "skipped" {
		t.Fatalf("confirmation widened: %#v %v", job, err)
	}
}

func TestUpgradeDuplicateQueuedRunningAndBatch(t *testing.T) {
	s, servers, release, now := batchFixture(t, 2)
	artifact := managementArtifact{URL: release.HelperArtifacts["amd64"].URL, SHA256: strings.Repeat("a", 64), Version: release.HelperVersion}
	payload, _ := json.Marshal(artifact)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.enqueueManagementCommand("1", "helper.update", payload)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	accepted, conflict := 0, 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if errors.Is(err, errUpgradeInProgress) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || conflict != 1 {
		t.Fatalf("accepted=%d conflict=%d", accepted, conflict)
	}
	s.data.ManagementCommands["1"] = nil
	status := s.data.AgentStatuses["1"]
	status.Update.Phase = "reconnecting"
	s.data.AgentStatuses["1"] = status
	if _, err := s.enqueueManagementCommand("1", "core.update", payload); !errors.Is(err, errUpgradeInProgress) {
		t.Fatal(err)
	}
	app := &app{helperState: s, apiToken: "test"}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/servers/1/agent", strings.NewReader(`{"action":"helper.update","payload":`+string(payload)+`}`))
	request.Header.Set("Authorization", "Bearer test")
	// An operator token still requires an existing official server.
	app.adminStore = &fakeAdminSessionStore{server: true}
	recorder := httptest.NewRecorder()
	app.agentManagementHandler(recorder, request, "1")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("HTTP=%d %s", recorder.Code, recorder.Body.String())
	}
	startBatchFixture(t, s, servers, release, now)
	if _, err := s.enqueueManagementCommand("2", "helper.update", payload); !errors.Is(err, errUpgradeInProgress) {
		t.Fatal(err)
	}
}

func TestHelperBatchConcurrencyFailureContinuesRestartAndCancel(t *testing.T) {
	s, servers, release, now := batchFixture(t, 5)
	job := startBatchFixture(t, s, servers, release, now)
	if _, err := s.startHelperBatch(servers, release, []string{"1"}, now); !errors.Is(err, errUpgradeInProgress) {
		t.Fatal("parallel batch accepted")
	}
	if err := s.advanceHelperBatch(servers, now); err != nil {
		t.Fatal(err)
	}
	if len(s.data.ManagementCommands["1"]) != 1 || len(s.data.ManagementCommands["2"]) != 1 || len(s.data.ManagementCommands["3"]) != 0 {
		t.Fatal("concurrency cap")
	}
	status := s.data.AgentStatuses["1"]
	p := *status.Update
	p.Phase = "failed"
	p.Message = "forced failure"
	p.UpdatedAt = now.Add(5 * time.Second)
	status.Update = &p
	s.data.AgentStatuses["1"] = status
	if err := s.advanceHelperBatch(servers, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.data.HelperBatch.Rows[0].Phase != "failed" || len(s.data.ManagementCommands["3"]) != 1 {
		t.Fatal("failure stopped queue")
	}
	reloaded, err := openHelperState(s.path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.advanceHelperBatch(servers, now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if reloaded.data.HelperBatch.ID != job.ID || len(reloaded.data.ManagementCommands["2"]) != 1 || len(reloaded.data.ManagementCommands["3"]) != 1 {
		t.Fatal("restart duplicated command")
	}
	if err := reloaded.stopHelperBatch(job.ID, now.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{3, 4} {
		if reloaded.data.HelperBatch.Rows[i].Phase != "cancelled" {
			t.Fatal("queued row not cancelled")
		}
	}
	for _, id := range []string{"2", "3"} {
		status := reloaded.data.AgentStatuses[id]
		p := *status.Update
		p.Phase = "success"
		p.UpdatedAt = now.Add(15 * time.Second)
		status.Update = &p
		reloaded.data.AgentStatuses[id] = status
	}
	if err := reloaded.advanceHelperBatch(servers, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	if reloaded.data.HelperBatch.Phase != "failed" || reloaded.data.HelperBatch.FinishedAt.IsZero() {
		t.Fatal("batch did not finish")
	}
}

func TestHelperBatchTimeoutAndNoTickWrites(t *testing.T) {
	s, servers, release, now := batchFixture(t, 3)
	startBatchFixture(t, s, servers, release, now)
	if err := s.advanceHelperBatch(servers, now); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 5; i++ {
		if err := s.advanceHelperBatch(servers, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.Stat(s.path)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged ticks rewrote state")
	}
	for id, status := range s.data.AgentStatuses {
		status.ReportedAt = now.Add(helperBatchTimeout)
		s.data.AgentStatuses[id] = status
	}
	if err := s.advanceHelperBatch(servers, now.Add(helperBatchTimeout)); err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1} {
		if s.data.HelperBatch.Rows[i].Phase != "failed" || len(s.data.ManagementCommands[fmt.Sprint(i+1)]) != 0 {
			t.Fatal("timeout retained executable command")
		}
	}
	if s.data.HelperBatch.Rows[2].Phase != "dispatching" {
		t.Fatal("timeout stopped queue")
	}
}

func TestHelperBatchPersistenceFailureDoesNotDispatch(t *testing.T) {
	s, servers, release, now := batchFixture(t, 2)
	startBatchFixture(t, s, servers, release, now)
	s.path = filepath.Join(s.path, "impossible")
	if err := s.advanceHelperBatch(servers, now); err == nil {
		t.Fatal("write should fail")
	}
	if len(s.data.ManagementCommands["1"]) != 0 || s.data.HelperBatch.Rows[0].Phase != "queued" {
		t.Fatal("uncommitted dispatch escaped")
	}
}

// tickWrites runs one scheduler step and reports whether it replaced the state file.
func tickWrites(t *testing.T, a *app, now time.Time) bool {
	t.Helper()
	before, err := os.Stat(a.helperState.path)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	a.helperBatchTick(context.Background(), now)
	log.SetOutput(previous)
	if logs.Len() != 0 {
		t.Fatalf("scheduler step logged: %q", logs.String())
	}
	after, err := os.Stat(a.helperState.path)
	if err != nil {
		t.Fatal(err)
	}
	return !os.SameFile(before, after)
}

func staleFixture(t *testing.T, count int) (*app, *helperState, []byte, time.Time) {
	t.Helper()
	s, _, release, _ := batchFixture(t, count)
	payload, _ := json.Marshal(managementArtifact{URL: release.HelperArtifacts["amd64"].URL, SHA256: strings.Repeat("a", 64), Version: release.HelperVersion})
	return &app{helperState: s, apiToken: "test", adminStore: &fakeAdminSessionStore{server: true}}, s, payload, time.Now().UTC()
}

func dispatchHelperUpdate(t *testing.T, a *app, serverID string, payload []byte) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/custom/servers/"+serverID+"/agent", strings.NewReader(`{"action":"helper.update","payload":`+string(payload)+`}`))
	request.Header.Set("Authorization", "Bearer test")
	recorder := httptest.NewRecorder()
	a.agentManagementHandler(recorder, request, serverID)
	return recorder.Code
}

// pendingUpdates counts the update commands the UI sees as pending.
func pendingUpdates(t *testing.T, a *app, serverID string) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/custom/servers/"+serverID+"/agent", nil)
	request.Header.Set("Authorization", "Bearer test")
	recorder := httptest.NewRecorder()
	a.agentManagementHandler(recorder, request, serverID)
	var body struct {
		Pending []managementCommand `json:"pending"`
	}
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &body) != nil {
		t.Fatalf("GET agent: HTTP %d %s", recorder.Code, recorder.Body.String())
	}
	count := 0
	for _, command := range body.Pending {
		if updateAction(command.Action) {
			count++
		}
	}
	return count
}

func TestStalledSingleServerUpgradeStopsBlocking(t *testing.T) {
	a, s, payload, now := staleFixture(t, 2)
	// 1 took its command, then went silent mid-download.
	status := s.data.AgentStatuses["1"]
	status.Update = dispatchedUpdate("helper", "v0.7.4", now.Add(-20*time.Minute))
	status.Update.Phase, status.Update.UpdatedAt = "downloading", now.Add(-managementCommandTTL)
	s.data.AgentStatuses["1"] = status
	// 2 never picked its command up; the command expired while dispatching.
	if _, err := s.enqueueManagementCommandLocked("2", "helper.update", payload, now.Add(-managementCommandTTL-time.Second), false); err != nil {
		t.Fatal(err)
	}
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		if s.upgradeInProgressLocked(id, now) {
			t.Fatalf("server %s: silent upgrade still reserved before the sweep", id)
		}
	}
	if !tickWrites(t, a, now) {
		t.Fatal("stale transition was not persisted")
	}
	reloaded, err := openHelperState(s.path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2"} {
		progress := reloaded.data.AgentStatuses[id].Update
		if progress.Phase != "failed" || !progress.UpdatedAt.Equal(now) || !strings.HasPrefix(progress.Message, "超过 15 分钟没有升级进度") || progress.Log[len(progress.Log)-1].Text != progress.Message {
			t.Fatalf("server %s: %#v", id, progress)
		}
		if pendingUpdates(t, a, id) != 0 {
			t.Fatalf("server %s: expired command still listed as pending", id)
		}
	}
	if tickWrites(t, a, now.Add(5*time.Second)) {
		t.Fatal("a resolved upgrade rewrote state")
	}
	for _, id := range []string{"1", "2"} {
		if code := dispatchHelperUpdate(t, a, id, payload); code != http.StatusAccepted {
			t.Fatalf("server %s: retry HTTP %d", id, code)
		}
	}
}

func TestLiveOrFinishedUpgradeIsNotSwept(t *testing.T) {
	a, s, payload, now := staleFixture(t, 6)
	// 1 posted progress 14 minutes ago.
	status := s.data.AgentStatuses["1"]
	status.Update = dispatchedUpdate("helper", "v0.7.4", now.Add(-20*time.Minute))
	status.Update.Phase, status.Update.UpdatedAt = "downloading", now.Add(-managementCommandTTL+time.Minute)
	s.data.AgentStatuses["1"] = status
	// 2 has old progress, but an unexpired update command can still start it.
	status = s.data.AgentStatuses["2"]
	status.Update = dispatchedUpdate("core", "8f5b614", now.Add(-time.Hour))
	s.data.AgentStatuses["2"] = status
	s.data.ManagementCommands["2"] = []managementCommand{{ID: "live", Action: "core.update", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}}
	// 3-5 finished long ago; 6 never upgraded.
	for i, phase := range []string{"success", "failed", "rolled_back"} {
		id := fmt.Sprint(i + 3)
		status = s.data.AgentStatuses[id]
		status.Update = &componentUpdateProgress{Component: "helper", Phase: phase, StartedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Hour), Message: "kept"}
		s.data.AgentStatuses[id] = status
	}
	if err := s.saveLocked(); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(s.data)
	for i := 0; i < 3; i++ {
		if tickWrites(t, a, now.Add(time.Duration(i)*5*time.Second)) {
			t.Fatal("unchanged sweep wrote state")
		}
	}
	if after, _ := json.Marshal(s.data); !bytes.Equal(before, after) {
		t.Fatal("sweep changed live or finished upgrades")
	}
	for _, id := range []string{"1", "2"} {
		if code := dispatchHelperUpdate(t, a, id, payload); code != http.StatusConflict {
			t.Fatalf("server %s: HTTP %d, want 409", id, code)
		}
	}
}

func TestStaleSweepLeavesBatchRowsToTheirTimeout(t *testing.T) {
	s, servers, release, now := batchFixture(t, 1)
	startBatchFixture(t, s, servers, release, now)
	if err := s.advanceHelperBatch(servers, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(managementCommandTTL)
	s.expireStaleUpdatesLocked(later)
	if s.data.AgentStatuses["1"].Update.Phase != "dispatching" || s.updateProgressDirty {
		t.Fatal("stale sweep took over a running batch row")
	}
	if err := s.advanceHelperBatch(servers, later); err != nil {
		t.Fatal(err)
	}
	if progress := s.data.AgentStatuses["1"].Update; progress.Phase != "failed" || !strings.HasPrefix(progress.Message, "升级超过 5 分钟") {
		t.Fatalf("batch timeout replaced: %#v", progress)
	}
}
