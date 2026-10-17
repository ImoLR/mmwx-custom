package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// heartbeatFixture registers count Helpers whose External intents are already
// healthy, as on production, and serves reports through the full handler path.
func heartbeatFixture(t *testing.T, count int) (*app, *helperState) {
	t.Helper()
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= count; i++ {
		id := fmt.Sprint(i)
		state.data.Servers[id] = helperServerIdentity{OfficialServerID: id, CustomServerUUID: "uuid-" + id, HelperTokenHash: hashSecret("token-" + id), MachineID: "machine-" + id}
		state.data.CoreModeIntents[id] = newCoreModeIntent("external", time.Now())
	}
	if err := state.saveLocked(); err != nil {
		t.Fatal(err)
	}
	store := &fakePersistentConnectionStore{fakeAdminSessionStore: fakeAdminSessionStore{server: true, runtime: remoteServerRuntime{XrayMode: "external", Status: "connected"}}}
	return &app{helperState: state, adminStore: store, helperRate: make(map[string]time.Time), connectionMetrics: make(map[string]connectionMetrics), detailedConnections: make(map[string]serverDetailedConnectionRecord)}, state
}

// heartbeatStatus is a steady External report; round only moves the fields
// production reports refresh every time (report and log-sample values).
func heartbeatStatus(id string, round int) agentStatus {
	status := healthyExternalStatus()
	status.MachineID = "machine-" + id
	status.Helper = componentStatus{Installed: true, Version: "v0.7.4", Active: true, Ready: true}
	status.Logs = &logHousekeepingStatus{JournaldCapped: true, AgentLogrotate: true, JournaldDiskUsage: fmt.Sprintf("%dM", 20+round), AgentLogBytes: int64(1000 + round), CheckedAt: time.Now().UTC(), UnitLines: map[string]int{"ssh.service": round}, SampleComplete: round > 0}
	return status
}

func postHeartbeat(t *testing.T, a *app, id string, status agentStatus, result *managementResult) *managementCommand {
	t.Helper()
	// Production reports are five seconds apart; skip the one-second limiter.
	a.connectionMu.Lock()
	delete(a.helperRate, id)
	a.connectionMu.Unlock()
	body, err := json.Marshal(helperDetailedMetricsRequest{ServerID: "uuid-" + id, HelperVersion: "v0.7.4", Management: &managementReport{Status: status, Result: result}})
	if err != nil {
		t.Error(err)
		return nil
	}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/connections", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer token-"+id)
	recorder := httptest.NewRecorder()
	a.helperDetailedConnectionsHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Errorf("server %s report: HTTP %d %s", id, recorder.Code, recorder.Body.String())
		return nil
	}
	var response struct {
		Command *managementCommand `json:"command"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Error(err)
	}
	return response.Command
}

func stateWrites(s *helperState) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

func readStateFile(t *testing.T, path string) helperStateData {
	t.Helper()
	data, err := decodeStateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// decodeStateFile is safe off the test goroutine; rename keeps every read whole.
func decodeStateFile(path string) (helperStateData, error) {
	var data helperStateData
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &data)
	}
	return data, err
}

// repeatHeartbeat re-sends the stored snapshot with fresh log samples, so only
// heartbeat fields change, as in a steady production report.
func repeatHeartbeat(t *testing.T, a *app, id string, round int) {
	t.Helper()
	a.helperState.mu.Lock()
	status := a.helperState.data.AgentStatuses[id]
	a.helperState.mu.Unlock()
	logs := *heartbeatStatus(id, round).Logs
	status.Logs = &logs
	postHeartbeat(t, a, id, status, nil)
}

// requireDiskMatchesMemory proves the last write carried every in-memory change.
func requireDiskMatchesMemory(t *testing.T, s *helperState) {
	t.Helper()
	disk, err := json.Marshal(readStateFile(t, s.path))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	memory, err := json.Marshal(s.data)
	dirty := s.volatileDirty
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if dirty || !bytes.Equal(disk, memory) {
		t.Fatalf("disk differs from memory (volatile dirty=%t)", dirty)
	}
}

// durableView drops only the heartbeat fields the lazy flusher may still owe the disk.
func durableView(data helperStateData) []byte {
	servers := make(map[string]helperServerIdentity, len(data.Servers))
	for id, identity := range data.Servers {
		identity.LastSeenAt, identity.UpdatedAt, identity.LastHelperVersion = time.Time{}, time.Time{}, ""
		servers[id] = identity
	}
	statuses := make(map[string]agentStatus, len(data.AgentStatuses))
	for id, status := range data.AgentStatuses {
		status.ReportedAt = time.Time{}
		if status.Logs != nil {
			logs := *status.Logs
			logs.CheckedAt, logs.AgentLogBytes, logs.JournaldDiskUsage, logs.UnitLines, logs.SampleComplete = time.Time{}, 0, "", nil, false
			status.Logs = &logs
		}
		statuses[id] = status
	}
	intents := make(map[string]coreModeIntent, len(data.CoreModeIntents))
	for id, intent := range data.CoreModeIntents {
		intent.UpdatedAt = time.Time{}
		intents[id] = intent
	}
	data.Servers, data.AgentStatuses, data.CoreModeIntents = servers, statuses, intents
	encoded, _ := json.Marshal(data)
	return encoded
}

// requireNothingDurablePending proves no durable change waits for the flusher.
func requireNothingDurablePending(t *testing.T, s *helperState, step string) {
	t.Helper()
	disk := readStateFile(t, s.path)
	s.mu.Lock()
	memory := durableView(s.data)
	s.mu.Unlock()
	if !bytes.Equal(durableView(disk), memory) {
		t.Fatalf("%s: a durable change is not on disk", step)
	}
}

func TestThirteenServerHeartbeatsCoalesceIntoOneWrite(t *testing.T) {
	a, state := heartbeatFixture(t, 13)
	for i := 1; i <= 13; i++ {
		postHeartbeat(t, a, fmt.Sprint(i), heartbeatStatus(fmt.Sprint(i), 0), nil)
	}
	if err := state.flushPending(); err != nil {
		t.Fatal(err)
	}
	// Restart from the settled file, as a deploy does.
	reloaded, err := openHelperState(state.path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a.helperState = reloaded
	before, err := os.Stat(reloaded.path)
	if err != nil {
		t.Fatal(err)
	}
	start := reloaded.savedAt
	// Six rounds of 13 reports: thirty seconds of production traffic.
	for round := 1; round <= 6; round++ {
		for i := 1; i <= 13; i++ {
			id := fmt.Sprint(i)
			if command := postHeartbeat(t, a, id, heartbeatStatus(id, round), nil); command != nil {
				t.Fatalf("healthy report returned command %s", command.Action)
			}
		}
		reloaded.mu.Lock()
		wrote, err := reloaded.flushVolatileLocked(start.Add(time.Duration(round*5-1) * time.Second))
		reloaded.mu.Unlock()
		if err != nil || wrote {
			t.Fatalf("round %d: early flush wrote=%t err=%v", round, wrote, err)
		}
	}
	after, err := os.Stat(reloaded.path)
	if err != nil {
		t.Fatal(err)
	}
	if writes := stateWrites(reloaded); writes != 0 || !os.SameFile(before, after) {
		t.Fatalf("78 heartbeats wrote %d times", writes)
	}
	// Memory stays current although the disk is behind.
	summaries := reloaded.coreModeSummaries()
	disk := readStateFile(t, reloaded.path)
	reloaded.mu.Lock()
	for i := 1; i <= 13; i++ {
		id := fmt.Sprint(i)
		status := reloaded.data.AgentStatuses[id]
		if !summaries[id].Fresh || time.Since(status.ReportedAt) > time.Second || status.Logs.AgentLogBytes != 1006 {
			reloaded.mu.Unlock()
			t.Fatalf("server %s: in-memory heartbeat is not current: %+v", id, status.Logs)
		}
		if !disk.AgentStatuses[id].ReportedAt.Before(status.ReportedAt) || reloaded.data.CoreModeIntents[id].RepairStatus != "healthy" {
			reloaded.mu.Unlock()
			t.Fatalf("server %s: heartbeat reached disk early or intent changed", id)
		}
	}
	wrote, err := reloaded.flushVolatileLocked(start.Add(helperStateFlushInterval))
	reloaded.mu.Unlock()
	if err != nil || !wrote || stateWrites(reloaded) != 1 {
		t.Fatalf("due flush wrote=%t writes=%d err=%v", wrote, stateWrites(reloaded), err)
	}
	requireDiskMatchesMemory(t, reloaded)
	// The next window again collapses into a single write.
	for i := 1; i <= 13; i++ {
		postHeartbeat(t, a, fmt.Sprint(i), heartbeatStatus(fmt.Sprint(i), 7), nil)
	}
	reloaded.mu.Lock()
	last := reloaded.savedAt
	early, _ := reloaded.flushVolatileLocked(last.Add(helperStateFlushInterval - helperStateFlushSlack - time.Millisecond))
	due, _ := reloaded.flushVolatileLocked(last.Add(helperStateFlushInterval))
	reloaded.mu.Unlock()
	if early || !due || stateWrites(reloaded) != 2 {
		t.Fatalf("second window early=%t due=%t writes=%d", early, due, stateWrites(reloaded))
	}
}

func TestDurableChangesAreWrittenImmediately(t *testing.T) {
	a, state := heartbeatFixture(t, 2)
	for _, id := range []string{"1", "2"} {
		postHeartbeat(t, a, id, heartbeatStatus(id, 0), nil)
	}
	var queued managementCommand
	artifactURL := "https://github.com/ImoLR/mmwx-custom/releases/download/v1.4.57/mmwxc-helper-linux-amd64"
	artifact, _ := json.Marshal(managementArtifact{URL: artifactURL, SHA256: strings.Repeat("a", 64), Version: "v0.7.4"})
	release := cachedReleaseInfo{HelperVersion: "v0.7.5", HelperArtifacts: map[string]releaseArtifact{"amd64": {URL: artifactURL, SHA256: strings.Repeat("a", 64)}}}
	stored := func(id string) agentStatus {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.data.AgentStatuses[id]
	}
	steps := []struct {
		name   string
		run    func() error
		onDisk func(helperStateData) bool
	}{
		{"command enqueued", func() (err error) {
			queued, err = state.enqueueManagementCommand("1", "core.status", nil)
			return err
		}, func(d helperStateData) bool { return len(d.ManagementCommands["1"]) == 1 }},
		{"command acknowledged with result", func() error {
			result := managementResult{CommandID: queued.ID, Action: queued.Action, Success: true, CompletedAt: time.Now().UTC()}
			signature, err := signManagementResult(result, hashSecret("token-1"))
			result.Signature = signature
			postHeartbeat(t, a, "1", stored("1"), &result)
			return err
		}, func(d helperStateData) bool {
			return len(d.ManagementCommands["1"]) == 0 && len(d.ManagementResults["1"]) == 1 && d.ManagementResults["1"][0].CommandID == queued.ID
		}},
		{"expired command dropped", func() error {
			state.mu.Lock()
			state.data.ManagementCommands["2"] = []managementCommand{{ID: "expired", Action: "core.status", CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(-time.Minute)}}
			state.mu.Unlock()
			postHeartbeat(t, a, "2", stored("2"), nil)
			return nil
		}, func(d helperStateData) bool { return len(d.ManagementCommands["2"]) == 0 }},
		{"machine binding changed", func() error {
			status := stored("1")
			status.MachineID = "machine-1-reinstalled"
			postHeartbeat(t, a, "1", status, nil)
			return nil
		}, func(d helperStateData) bool { return d.Servers["1"].MachineID == "machine-1-reinstalled" }},
		{"observed Core state changed", func() error {
			status := stored("2")
			status.Ownership.MainPID = 4242
			postHeartbeat(t, a, "2", status, nil)
			return nil
		}, func(d helperStateData) bool { return d.AgentStatuses["2"].Ownership.MainPID == 4242 }},
		{"mode intent set", func() error {
			_, err := state.setCoreModeIntent("2", "embedded")
			return err
		}, func(d helperStateData) bool {
			return d.CoreModeIntents["2"].DesiredMode == "embedded" && d.CoreModeIntents["2"].PendingChange
		}},
		{"reconcile queued a repair", func() error {
			_, err := state.reconcileCoreMode("2", stored("2"), remoteServerRuntime{XrayMode: "external", Status: "connected"}, nil)
			return err
		}, func(d helperStateData) bool {
			return d.CoreModeIntents["2"].RepairStatus == "repairing" && len(d.ManagementCommands["2"]) == 1
		}},
		{"install token created", func() error {
			_, _, err := state.createInstallTokenForMode("2", "takeover")
			return err
		}, func(d helperStateData) bool { return len(d.InstallTokens) == 1 }},
		{"connection settings", func() error {
			settings := defaultServerConnectionSettings()
			settings.MachineProtection = &serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(500)}
			return state.setConnectionSettings("1", settings)
		}, func(d helperStateData) bool {
			protection := d.ConnectionSettings["1"].MachineProtection
			return protection != nil && *protection.MaxTotal == 500
		}},
		{"accelerator", func() error {
			_, err := state.setGitHubAccelerator("")
			return err
		}, func(d helperStateData) bool { return d.GitHubAccelerator != nil && *d.GitHubAccelerator == "" }},
		{"external config repair marker", func() error { return state.markExternalConfigRepair("1", "restart:abc") },
			func(d helperStateData) bool { return d.ExternalConfigRepairs["1"] == "restart:abc" }},
		{"official mode observed", func() error { return state.observeOfficialModeAfterWrite("2", "external") },
			func(d helperStateData) bool {
				return d.CoreModeIntents["2"].DesiredMode == "external" && !d.CoreModeIntents["2"].PendingChange
			}},
		{"update dispatched", func() error {
			state.mu.Lock()
			state.data.ManagementCommands["2"] = nil
			state.mu.Unlock()
			_, err := state.enqueueManagementCommand("2", "helper.update", artifact)
			return err
		}, func(d helperStateData) bool {
			update := d.AgentStatuses["2"].Update
			return len(d.ManagementCommands["2"]) == 1 && update != nil && update.Phase == "dispatching"
		}},
		{"batch started", func() error {
			state.mu.Lock()
			status := state.data.AgentStatuses["1"]
			status.Architecture, status.Capabilities = "amd64", []string{"core.mode.apply", "helper.update"}
			state.data.AgentStatuses["1"] = status
			state.mu.Unlock()
			_, err := state.startHelperBatch([]helperBatchServer{{ID: "1", Name: "one", Status: "connected"}, {ID: "2", Name: "two", Status: "connected"}}, release, []string{"1"}, time.Now().UTC())
			return err
		}, func(d helperStateData) bool { return d.HelperBatch != nil && d.HelperBatch.Phase == "running" }},
		{"batch stopped", func() error {
			state.mu.Lock()
			id := state.data.HelperBatch.ID
			state.mu.Unlock()
			return state.stopHelperBatch(id, time.Now().UTC())
		}, func(d helperStateData) bool { return d.HelperBatch != nil && d.HelperBatch.Stopped }},
		{"legacy credential bound", func() error {
			if state.recordLegacyReporter("9", "legacy-token", "v0.1.0") == "" {
				return fmt.Errorf("legacy reporter rejected")
			}
			return nil
		}, func(d helperStateData) bool { return d.Servers["9"].HelperTokenHash == hashSecret("legacy-token") }},
	}
	for round, step := range steps {
		// A heartbeat first leaves volatile data pending; the durable write must carry it.
		repeatHeartbeat(t, a, "1", 10+round)
		state.mu.Lock()
		pending, before, heartbeat := state.volatileDirty, state.writes, state.data.AgentStatuses["1"].ReportedAt
		state.mu.Unlock()
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if !pending || stateWrites(state) == before {
			t.Fatalf("%s: pending=%t, no immediate write", step.name, pending)
		}
		disk := readStateFile(t, state.path)
		if !step.onDisk(disk) {
			t.Fatalf("%s: change is not on disk", step.name)
		}
		if disk.AgentStatuses["1"].ReportedAt.Before(heartbeat) {
			t.Fatalf("%s: the durable write did not carry the pending heartbeat", step.name)
		}
		requireNothingDurablePending(t, state, step.name)
	}
}

func TestReconcileWithoutRealChangeDoesNotWrite(t *testing.T) {
	state := newCoreModeTestState(t)
	if _, err := state.setCoreModeIntent("12", "external"); err != nil {
		t.Fatal(err)
	}
	healthy := remoteServerRuntime{XrayMode: "external", Status: "connected"}
	if _, err := state.reconcileCoreMode("12", healthyExternalStatus(), healthy, nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		status  func() agentStatus
		runtime remoteServerRuntime
		want    string
	}{
		{"healthy", healthyExternalStatus, healthy, "healthy"},
		{"waiting for official", healthyExternalStatus, remoteServerRuntime{XrayMode: "external", Status: "offline"}, "waiting_official"},
		{"Helper too old to repair", func() agentStatus {
			status := healthyExternalStatus()
			status.CoreMode, status.Ownership, status.Capabilities = "embedded", externalOwnershipStatus{}, nil
			return status
		}, healthy, "degraded"},
	}
	for _, test := range cases {
		state.mu.Lock()
		intent := state.data.CoreModeIntents["12"]
		intent.PendingChange, intent.DriftDetectedAt = false, time.Now().Add(-time.Minute)
		state.data.CoreModeIntents["12"] = intent
		state.mu.Unlock()
		// The first pass may change the repair state and is saved.
		if _, err := state.reconcileCoreMode("12", test.status(), test.runtime, nil); err != nil {
			t.Fatal(err)
		}
		requireDiskMatchesMemory(t, state)
		before, _ := os.Stat(state.path)
		writes := stateWrites(state)
		first, _ := state.coreModeIntent("12")
		for pass := 0; pass < 20; pass++ {
			if _, err := state.reconcileCoreMode("12", test.status(), test.runtime, nil); err != nil {
				t.Fatal(err)
			}
		}
		after, _ := os.Stat(state.path)
		current, _ := state.coreModeIntent("12")
		if stateWrites(state) != writes || !os.SameFile(before, after) || current.RepairStatus != test.want {
			t.Fatalf("%s: unchanged reconcile wrote %d times (status %s)", test.name, stateWrites(state)-writes, current.RepairStatus)
		}
		// UpdatedAt stays current in memory and is persisted lazily.
		state.mu.Lock()
		dirty := state.volatileDirty
		state.mu.Unlock()
		if current.UpdatedAt.Before(first.UpdatedAt) || !dirty {
			t.Fatalf("%s: in-memory UpdatedAt not kept for the lazy flush", test.name)
		}
	}
}

func TestShutdownFlushesPendingHeartbeats(t *testing.T) {
	a, state := heartbeatFixture(t, 1)
	postHeartbeat(t, a, "1", heartbeatStatus("1", 0), nil)
	if err := state.flushPending(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(a.helperDetailedConnectionsHandler)}
	go func() { _ = server.Serve(listener) }()
	body, _ := json.Marshal(helperDetailedMetricsRequest{ServerID: "uuid-1", HelperVersion: "v0.7.4", Management: &managementReport{Status: heartbeatStatus("1", 1)}})
	a.connectionMu.Lock()
	delete(a.helperRate, "1")
	a.connectionMu.Unlock()
	request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/api/custom/agent/connections", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer token-1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("report HTTP %d", response.StatusCode)
	}
	writes := stateWrites(state)
	state.mu.Lock()
	latest := state.data.AgentStatuses["1"].ReportedAt
	state.mu.Unlock()
	if !readStateFile(t, state.path).AgentStatuses["1"].ReportedAt.Before(latest) {
		t.Fatal("heartbeat was written synchronously")
	}
	a.shutdown(server)
	if stateWrites(state) != writes+1 || !readStateFile(t, state.path).AgentStatuses["1"].ReportedAt.Equal(latest) {
		t.Fatal("shutdown lost the pending heartbeat")
	}
	requireDiskMatchesMemory(t, state)
	// Nothing pending: a second shutdown flush does not write.
	if err := state.flushPending(); err != nil || stateWrites(state) != writes+1 {
		t.Fatal("clean shutdown flush wrote")
	}
}

func TestShutdownFlushesThrottledProgress(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	update := componentUpdateProgress{Component: "helper", Phase: "downloading", TargetVersion: "v0.7.4"}
	for i, phase := range []string{"downloading", "verifying"} {
		update.Phase = phase
		if err := state.recordUpdateProgressAt("5", update, "", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if readStateFile(t, state.path).AgentStatuses["5"].Update.Phase != "downloading" {
		t.Fatal("throttle bypassed")
	}
	if err := state.flushPending(); err != nil || readStateFile(t, state.path).AgentStatuses["5"].Update.Phase != "verifying" {
		t.Fatalf("shutdown lost throttled progress: %v", err)
	}
}

func TestFlusherAndDurableSavesKeepEveryUpdate(t *testing.T) {
	a, state := heartbeatFixture(t, 13)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// The flusher runs as fast as it can with every flush due.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			state.mu.Lock()
			_, err := state.flushVolatileLocked(time.Now().Add(time.Hour))
			state.mu.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
		}
	}()
	var heartbeats sync.WaitGroup
	for i := 1; i <= 13; i++ {
		heartbeats.Add(1)
		go func(id string) {
			defer heartbeats.Done()
			for round := 0; round < 20; round++ {
				postHeartbeat(t, a, id, heartbeatStatus(id, round), nil)
			}
		}(fmt.Sprint(i))
	}
	var durable sync.WaitGroup
	commands := make(chan string, 13*4)
	for i := 1; i <= 13; i++ {
		durable.Add(1)
		go func(id string) {
			defer durable.Done()
			for n := 0; n < 4; n++ {
				command, err := state.enqueueManagementCommand(id, "core.status", nil)
				if err != nil {
					t.Error(err)
					return
				}
				// Durable means on disk when the call returns, whatever the flusher does.
				disk, err := decodeStateFile(state.path)
				found := false
				for _, queued := range disk.ManagementCommands[id] {
					found = found || queued.ID == command.ID
				}
				if err != nil || !found {
					t.Errorf("server %s: command %s not on disk after enqueue returned", id, command.ID)
				}
				commands <- id + "/" + command.ID
			}
		}(fmt.Sprint(i))
	}
	heartbeats.Wait()
	durable.Wait()
	close(stop)
	wg.Wait()
	close(commands)
	if err := state.flushPending(); err != nil {
		t.Fatal(err)
	}
	requireDiskMatchesMemory(t, state)
	reloaded, err := openHelperState(state.path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	queued := map[string]bool{}
	for _, list := range reloaded.data.ManagementCommands {
		for _, command := range list {
			queued[command.ID] = true
		}
	}
	for entry := range commands {
		if !queued[strings.SplitN(entry, "/", 2)[1]] {
			t.Fatalf("command %s lost", entry)
		}
	}
	for i := 1; i <= 13; i++ {
		if logs := reloaded.data.AgentStatuses[fmt.Sprint(i)].Logs; logs == nil || logs.AgentLogBytes != 1019 {
			t.Fatalf("server %d: last heartbeat lost", i)
		}
	}
}

func TestFlushErrorsAreLoggedOncePerDistinctError(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state.recordLegacyReporter("5", "helper-token", "v0.7.4")
	state.path = filepath.Join(state.path, "impossible")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	start := time.Now()
	attempts := 0
	for second := 0; second < 600; second++ {
		state.recordLegacyReporter("5", "helper-token", "v0.7.4")
		state.mu.Lock()
		before := state.writes
		_, err := state.flushVolatileLocked(start.Add(time.Duration(second) * time.Second))
		wrote := state.writes != before
		state.mu.Unlock()
		if err != nil {
			attempts++
		}
		if wrote {
			t.Fatal("impossible path was written")
		}
	}
	if lines := strings.Count(logs.String(), "\n"); lines != 1 || !strings.Contains(logs.String(), "helper state flush failed") {
		t.Fatalf("flush errors logged %d times: %q", lines, logs.String())
	}
	// A failing disk is retried once per interval, not every second.
	if attempts > 21 {
		t.Fatalf("failed flush retried %d times in ten minutes", attempts)
	}
	if err := state.flushPending(); err == nil || strings.Count(logs.String(), "\n") != 1 {
		t.Fatal("shutdown flush error was hidden or logged twice")
	}
}

func TestRunningBatchFlushesHeartbeatsEveryFiveSeconds(t *testing.T) {
	s, servers, release, now := batchFixture(t, 1)
	startBatchFixture(t, s, servers, release, now)
	s.recordLegacyReporter("7", "helper-token", "v0.7.4")
	s.recordLegacyReporter("7", "helper-token", "v0.7.4")
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := s.savedAt
	if wrote, _ := s.flushVolatileLocked(saved.Add(helperStateBatchFlushInterval - helperStateFlushSlack - time.Millisecond)); wrote {
		t.Fatal("batch flush before five seconds")
	}
	if wrote, _ := s.flushVolatileLocked(saved.Add(helperStateBatchFlushInterval)); !wrote {
		t.Fatal("running batch left report times older than its scheduler")
	}
	s.data.HelperBatch.Phase = "success"
	s.markVolatileLocked()
	saved = s.savedAt
	if wrote, _ := s.flushVolatileLocked(saved.Add(helperStateBatchFlushInterval)); wrote {
		t.Fatal("finished batch kept the five-second cadence")
	}
}

func TestHeartbeatOnlyStatusChange(t *testing.T) {
	base := heartbeatStatus("1", 1)
	same := heartbeatStatus("1", 2)
	same.ReportedAt = base.ReportedAt.Add(5 * time.Second)
	if !heartbeatOnlyStatusChange(base, same) {
		t.Fatal("report time and log samples counted as a real change")
	}
	for name, change := range map[string]func(*agentStatus){
		"core mode":     func(s *agentStatus) { s.CoreMode = "embedded" },
		"helper":        func(s *agentStatus) { s.Helper.Version = "v0.7.5" },
		"core PID":      func(s *agentStatus) { s.Ownership.MainPID = 99 },
		"capabilities":  func(s *agentStatus) { s.Capabilities = append(s.Capabilities, "helper.update") },
		"progress":      func(s *agentStatus) { s.Update = dispatchedUpdate("helper", "v0.7.5", time.Now()) },
		"last result":   func(s *agentStatus) { s.LastOperation = &managementResult{CommandID: "x"} },
		"log retention": func(s *agentStatus) { s.Logs.AgentLogrotate = false },
	} {
		changed := heartbeatStatus("1", 2)
		change(&changed)
		if heartbeatOnlyStatusChange(base, changed) {
			t.Fatalf("%s change was treated as heartbeat-only", name)
		}
	}
	if heartbeatOnlyStatusChange(agentStatus{}, base) {
		t.Fatal("first report was treated as heartbeat-only")
	}
}
