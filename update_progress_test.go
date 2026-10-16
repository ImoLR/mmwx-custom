package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHelperUpdateProgressIsAuthenticatedAndSurvivesHeartbeat(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	customID := state.recordLegacyReporter("12", "helper-token", "v0.5.1")
	app := &app{helperState: state, helperTokens: map[string]string{}}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/update-progress", bytes.NewBufferString(`{"server_id":"`+customID+`","helper_version":"v0.5.1","component":"helper","phase":"verifying","target_version":"v0.5.2"}`))
	request.Header.Set("Authorization", "Bearer helper-token")
	response := httptest.NewRecorder()
	app.helperUpdateProgressHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("progress POST returned %d: %s", response.Code, response.Body.String())
	}
	state.mu.Lock()
	progress := state.data.AgentStatuses["12"].Update
	state.mu.Unlock()
	if progress == nil || progress.Component != "helper" || progress.Phase != "verifying" || progress.TargetVersion != "v0.5.2" {
		t.Fatalf("progress=%#v", progress)
	}
	if _, err := state.acceptManagementReport("12", "helper-token", &managementReport{Status: agentStatus{Helper: componentStatus{Version: "v0.5.1"}}}); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	preserved := state.data.AgentStatuses["12"].Update
	state.mu.Unlock()
	if preserved == nil || preserved.Phase != "verifying" {
		t.Fatalf("heartbeat erased progress: %#v", preserved)
	}
}

func TestHelperUpdateProgressRejectsInvalidPhase(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	customID := state.recordLegacyReporter("12", "helper-token", "v0.5.1")
	app := &app{helperState: state, helperTokens: map[string]string{}}
	request := httptest.NewRequest(http.MethodPost, "/api/custom/agent/update-progress", bytes.NewBufferString(`{"server_id":"`+customID+`","helper_version":"v0.5.1","component":"helper","phase":"executing_shell"}`))
	request.Header.Set("Authorization", "Bearer helper-token")
	response := httptest.NewRecorder()
	app.helperUpdateProgressHandler(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid phase returned %d", response.Code)
	}
}

func TestObservedTargetVersionCompletesLegacyScheduledUpdate(t *testing.T) {
	progress := &componentUpdateProgress{Component: "helper", Phase: "dispatching", TargetVersion: "v0.5.1", UpdatedAt: time.Now().Add(-time.Minute)}
	completed := completeObservedUpdate(progress, agentStatus{Helper: componentStatus{Version: "v0.5.1"}}, time.Now())
	if completed == nil || completed.Phase != "success" {
		t.Fatalf("completed progress=%#v", completed)
	}
	if progress.Phase != "dispatching" {
		t.Fatal("source progress was mutated")
	}
}

func TestUpdateProgressPayloadBoundsAndLog(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id := state.recordLegacyReporter("5", "private-token", "v0.7.1")
	app := &app{helperState: state}
	helperVersion := "v0.7.1"
	post := func(extra string, token string) int {
		body := `{"server_id":"` + id + `","helper_version":"` + helperVersion + `","component":"helper","phase":"downloading","target_version":"v0.7.2"` + extra + `}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		app.helperUpdateProgressHandler(res, req)
		return res.Code
	}
	for _, extra := range []string{`,"percent":-1`, `,"percent":101`, `,"percent":1.5`, `,"bytes_done":-1`, `,"bytes_total":-1`, `,"bytes_done":11,"bytes_total":10`, `,"bytes_total":9007199254740992`, `,"unknown":true`, `,"log_line":"` + strings.Repeat("a", 4096) + `"`} {
		if code := post(extra, "private-token"); code != 400 {
			t.Fatalf("bounds %s: %d", extra, code)
		}
	}
	if code := post("", "wrong"); code != 401 {
		t.Fatalf("unauthorized=%d", code)
	}
	for _, version := range []string{"v0.6.9", "v0.7.1"} {
		helperVersion = version
		if code := post("", "private-token"); code != 200 {
			t.Fatalf("legacy=%d", code)
		}
	}
	if code := post(`,"percent":42,"bytes_done":420,"bytes_total":1000,"log_line":"SHA-256 verified private-token https://secret.example/a?token=x password=secret"`, "private-token"); code != 200 {
		t.Fatalf("new=%d", code)
	}
	progress := state.data.AgentStatuses["5"].Update
	if progress.Percent == nil || *progress.Percent != 42 || progress.BytesDone != 420 || progress.BytesTotal != 1000 || progress.StartedAt.IsZero() {
		t.Fatalf("progress=%+v", progress)
	}
	text := progress.Log[len(progress.Log)-1].Text
	for _, secret := range []string{"private-token", "secret.example", "token=x", "password=secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("leaked %s: %s", secret, text)
		}
	}
	if len(progress.Log) != 2 || progress.Log[0].Text != "正在下载" {
		t.Fatalf("legacy log=%+v", progress.Log)
	}
}

func TestUpdateProgressPersistenceAndTerminalStates(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	initial := dispatchedUpdate("helper", "v0.7.2", now)
	state.data.AgentStatuses["5"] = agentStatus{Update: initial}
	percent := 5
	update := componentUpdateProgress{Component: "helper", Phase: "downloading", TargetVersion: "v0.7.2", Percent: &percent}
	record := func(at time.Time, line string) {
		t.Helper()
		if err := state.recordUpdateProgressAt("5", update, line, at); err != nil {
			t.Fatal(err)
		}
	}
	record(now, "")
	disk, err := os.ReadFile(state.path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 5; i++ {
		percent = i * 10
		record(now.Add(time.Duration(i)*time.Second), "")
	}
	unchanged, _ := os.ReadFile(state.path)
	if !bytes.Equal(disk, unchanged) {
		t.Fatal("percent-only posts rewrote state within 5s")
	}
	if len(state.data.AgentStatuses["5"].Update.Log) != 2 {
		t.Fatal("percent-only posts appended log")
	}
	record(now.Add(5*time.Second), "")
	changed, _ := os.ReadFile(state.path)
	if bytes.Equal(disk, changed) {
		t.Fatal("progress not persisted after 5s")
	}
	update.Phase = "installing"
	update.Percent = nil
	record(now.Add(6*time.Second), "替换文件")
	var saved helperStateData
	changed, _ = os.ReadFile(state.path)
	if err := json.Unmarshal(changed, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.AgentStatuses["5"].Update.Phase != "downloading" {
		t.Fatal("phase change bypassed five-second throttle")
	}
	for i := 0; i < 50; i++ {
		record(now.Add(7*time.Second), strings.Repeat("测", 220)+" https://hidden.example")
	}
	progress := state.data.AgentStatuses["5"].Update
	if len(progress.Log) != 40 || !progress.StartedAt.Equal(now) {
		t.Fatalf("bounded history=%+v", progress)
	}
	for _, line := range progress.Log {
		if utf8.RuneCountInString(line.Text) > 200 || !utf8.ValidString(line.Text) {
			t.Fatal("invalid log bound or UTF-8")
		}
	}
	for i, phase := range []string{"success", "rolled_back", "failed"} {
		update.Phase = phase
		at := now.Add(time.Duration(10+i*5) * time.Second)
		record(at, "terminal")
		changed, _ = os.ReadFile(state.path)
		_ = json.Unmarshal(changed, &saved)
		if saved.AgentStatuses["5"].Update.Phase != phase {
			t.Fatalf("terminal %s not persisted", phase)
		}
		update.Phase = "reconnecting"
		record(at.Add(time.Second), "late post")
		if state.data.AgentStatuses["5"].Update.Phase != phase {
			t.Fatal("late post reopened terminal state")
		}
	}
	failed := state.data.AgentStatuses["5"].Update
	if completeObservedUpdate(failed, agentStatus{Helper: componentStatus{Version: "v0.7.2"}}, now).Phase != "failed" {
		t.Fatal("heartbeat erased failure")
	}
	reset := dispatchedUpdate("helper", "v0.7.3", now.Add(time.Minute))
	if len(reset.Log) != 1 || reset.Log[0].Text != "已下发升级任务 → v0.7.3" || !reset.StartedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("dispatch did not reset: %+v", reset)
	}
	completed := completeObservedUpdate(reset, agentStatus{Helper: componentStatus{Version: "v0.7.3"}}, now.Add(2*time.Minute))
	if len(completed.Log) != 2 || completed.Log[1].Text != "已确认新版本 v0.7.3" || len(reset.Log) != 1 {
		t.Fatal("observation log absent or mutated original")
	}
	if completeObservedUpdate(completed, agentStatus{}, now.Add(3*time.Minute)) != completed {
		t.Fatal("terminal heartbeat changed completion time")
	}
}

func TestRejectedLegacyUpdateFinishesProgress(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state.recordLegacyReporter("5", "helper-token", "v0.6.9")
	payload, _ := json.Marshal(managementArtifact{URL: "https://github.com/ImoLR/mmwx-custom/releases/download/v1.4.51/mmwxc-helper-linux-amd64", SHA256: strings.Repeat("a", 64), Version: "v0.7.2"})
	command, err := state.enqueueManagementCommand("5", "helper.update", payload)
	if err != nil {
		t.Fatal(err)
	}
	result := managementResult{CommandID: command.ID, Action: command.Action, Success: false, Message: "management command expired", CompletedAt: time.Now()}
	result.Signature, err = signManagementResult(result, hashSecret("helper-token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.acceptManagementReport("5", "helper-token", &managementReport{Status: agentStatus{Helper: componentStatus{Version: "v0.6.9"}}, Result: &result}); err != nil {
		t.Fatal(err)
	}
	progress := state.data.AgentStatuses["5"].Update
	if progress.Phase != "failed" || !strings.Contains(progress.Log[len(progress.Log)-1].Text, "management command expired") {
		t.Fatalf("rejection left running: %+v", progress)
	}
}

func TestProgressHTTPAuthenticationDoesNotBypassPersistenceThrottle(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		id := state.recordLegacyReporter("5", "helper-token", "v0.7.1")
		app := &app{helperState: state, helperTokens: map[string]string{"5": "helper-token"}}
		if legacy {
			id = "5"
		}
		post := func(phase string, percent int) {
			t.Helper()
			body, _ := json.Marshal(map[string]any{"server_id": id, "helper_version": "v0.7.2", "component": "helper", "phase": phase, "target_version": "v0.7.2", "percent": percent})
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer helper-token")
			res := httptest.NewRecorder()
			app.helperUpdateProgressHandler(res, req)
			if res.Code != http.StatusOK {
				t.Fatalf("HTTP %d", res.Code)
			}
		}
		post("downloading", 0)
		saved, err := os.Stat(state.path)
		if err != nil {
			t.Fatal(err)
		}
		for _, percent := range []int{5, 10, 15, 20} {
			post("downloading", percent)
		}
		current, err := os.Stat(state.path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(saved, current) {
			t.Fatalf("legacy=%t: authentication rewrote state for percent-only updates", legacy)
		}
		if *state.data.AgentStatuses["5"].Update.Percent != 20 {
			t.Fatal("in-memory progress not refreshed")
		}
		post("verifying", 100)
		terminal, err := os.Stat(state.path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(current, terminal) {
			t.Fatal("phase change bypassed HTTP persistence throttle")
		}
	}
}

func TestConcurrentServerProgressBurstAndFinalFlush(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, phase := range []string{"downloading", "verifying", "installing", "restarting", "reconnecting", "success"} {
		for _, id := range []string{"5", "12"} {
			if err := state.recordUpdateProgressAt(id, componentUpdateProgress{Component: "helper", Phase: phase, TargetVersion: "v0.7.4"}, "", now.Add(time.Duration(i)*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, _ := os.Stat(state.path)
	if err := state.flushUpdateProgressLocked(now.Add(4 * time.Second)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(state.path)
	if !os.SameFile(before, after) {
		t.Fatal("early flush wrote state")
	}
	if err := state.flushUpdateProgressLocked(now.Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openHelperState(state.path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"5", "12"} {
		if reloaded.data.AgentStatuses[id].Update.Phase != "success" {
			t.Fatal("final state lost")
		}
	}
	before, _ = os.Stat(state.path)
	if err := state.flushUpdateProgressLocked(now.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	after, _ = os.Stat(state.path)
	if !os.SameFile(before, after) {
		t.Fatal("clean flush wrote state")
	}
}
