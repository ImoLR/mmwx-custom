package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/releaseurl"
)

const helperBatchTimeout = 5 * time.Minute

type helperBatchServer struct {
	ID     string
	Name   string
	Status string
}

type helperBatchServerStore interface {
	HelperBatchServers(context.Context) ([]helperBatchServer, error)
}

func (s *postgresAdminSessionStore) HelperBatchServers(ctx context.Context) ([]helperBatchServer, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, name, COALESCE(status, '') FROM remote_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	servers := []helperBatchServer{}
	for rows.Next() {
		var server helperBatchServer
		if err := rows.Scan(&server.ID, &server.Name, &server.Status); err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, rows.Err()
}

type helperBatchRow struct {
	ServerID     string             `json:"server_id"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	Phase        string             `json:"phase"`
	Reason       string             `json:"reason,omitempty"`
	FirstUpgrade bool               `json:"first_upgrade,omitempty"`
	CommandID    string             `json:"command_id,omitempty"`
	StartedAt    time.Time          `json:"started_at,omitempty"`
	FinishedAt   time.Time          `json:"finished_at,omitempty"`
	Artifact     managementArtifact `json:"artifact"`
}

type helperBatchJob struct {
	ID         string           `json:"id"`
	Phase      string           `json:"phase"`
	StartedAt  time.Time        `json:"started_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
	FinishedAt time.Time        `json:"finished_at,omitempty"`
	Stopped    bool             `json:"stopped"`
	Rows       []helperBatchRow `json:"rows"`
}

func batchRowRunning(phase string) bool {
	return phase != "queued" && phase != "skipped" && phase != "cancelled" && !terminalUpdatePhase(phase)
}

func (s *helperState) batchServerBusyLocked(serverID string) bool {
	job := s.data.HelperBatch
	if job != nil && job.Phase == "running" {
		for _, row := range job.Rows {
			if row.ServerID == serverID && (row.Phase == "queued" || batchRowRunning(row.Phase)) {
				return true
			}
		}
	}
	return false
}

func (s *helperState) helperBatchPreviewLocked(servers []helperBatchServer, release cachedReleaseInfo, now time.Time) []helperBatchRow {
	rows := make([]helperBatchRow, 0, len(servers))
	for _, server := range servers {
		status := s.data.AgentStatuses[server.ID]
		row := helperBatchRow{ServerID: server.ID, Name: server.Name, Version: status.Helper.Version, Phase: "queued"}
		version := strings.TrimPrefix(status.Helper.Version, "v")
		row.FirstUpgrade = strings.HasPrefix(version, "0.1.") || version == "0.1" || version == ""
		artifact, available := release.HelperArtifacts[status.Architecture]
		row.Artifact = managementArtifact{URL: artifact.URL, SHA256: artifact.SHA256, Version: release.HelperVersion}
		switch {
		case s.upgradeInProgressLocked(server.ID, now) || s.batchServerBusyLocked(server.ID):
			row.Reason = "升级已在进行"
		case server.Status != "connected" || status.ReportedAt.IsZero() || now.Sub(status.ReportedAt) > helperStaleTimeout:
			row.Reason = "离线或 Helper 未上报"
		case !status.Helper.Installed || s.data.Servers[server.ID].HelperTokenHash == "":
			row.Reason = "未安装 Helper"
		case release.HelperVersion == "":
			row.Reason = "最新版本暂不可用"
		case status.Helper.Version == release.HelperVersion:
			row.Reason = "已是最新"
		case row.FirstUpgrade && !hasCapability(status, "helper.update"):
			row.Reason = "需要首次升级，请先在单台 Helper 面板完成"
		case !available:
			row.Reason = "当前架构无升级产物"
		default:
			payload, _ := json.Marshal(row.Artifact)
			if validateManagementPayload("helper.update", payload) != nil {
				row.Reason = "升级产物校验信息不完整"
			}
		}
		if row.Reason != "" {
			row.Phase = "skipped"
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *helperState) startHelperBatch(servers []helperBatchServer, release cachedReleaseInfo, selected []string, now time.Time) (*helperBatchJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.HelperBatch != nil && s.data.HelperBatch.Phase == "running" {
		return nil, errUpgradeInProgress
	}
	rows := s.helperBatchPreviewLocked(servers, release, now)
	allowed := map[string]bool{}
	for _, id := range selected {
		allowed[id] = true
	}
	count := 0
	for i := range rows {
		if rows[i].Phase == "queued" {
			if !allowed[rows[i].ServerID] {
				rows[i].Phase, rows[i].Reason = "skipped", "未在本次确认列表中"
			} else {
				count++
			}
		}
	}
	if count == 0 {
		return nil, errors.New("没有可升级的 Helper，请刷新列表")
	}
	id, err := randomUUID()
	if err != nil {
		return nil, err
	}
	previous := s.data.HelperBatch
	job := &helperBatchJob{ID: id, Phase: "running", StartedAt: now, UpdatedAt: now, Rows: rows}
	s.data.HelperBatch = job
	if err := s.saveLocked(); err != nil {
		s.data.HelperBatch = previous
		return nil, err
	}
	return cloneHelperBatch(job), nil
}

func cloneHelperBatch(job *helperBatchJob) *helperBatchJob {
	if job == nil {
		return nil
	}
	copy := *job
	copy.Rows = append([]helperBatchRow(nil), job.Rows...)
	return &copy
}

func (s *helperState) stopHelperBatch(id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data.HelperBatch
	if previous == nil || previous.ID != id || previous.Phase != "running" {
		return errors.New("批量任务已结束或已变化，请刷新")
	}
	job := cloneHelperBatch(previous)
	job.Stopped, job.UpdatedAt = true, now
	for i := range job.Rows {
		if job.Rows[i].Phase == "queued" {
			job.Rows[i].Phase, job.Rows[i].Reason, job.Rows[i].FinishedAt = "cancelled", "已停止剩余任务", now
		}
	}
	finishHelperBatch(job, now)
	s.data.HelperBatch = job
	if err := s.saveLocked(); err != nil {
		s.data.HelperBatch = previous
		return err
	}
	return nil
}

func finishHelperBatch(job *helperBatchJob, now time.Time) {
	failed := false
	for _, row := range job.Rows {
		if row.Phase == "queued" || batchRowRunning(row.Phase) {
			return
		}
		failed = failed || row.Phase == "failed" || row.Phase == "rolled_back"
	}
	job.Phase, job.FinishedAt = "success", now
	if failed {
		job.Phase = "failed"
	}
}

// Called every five seconds. Unchanged jobs cause no state writes or log lines.
// Commands and row transitions commit in one atomic state-file replacement.
func (s *helperState) advanceHelperBatch(servers []helperBatchServer, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data.HelperBatch
	if previous == nil || previous.Phase != "running" {
		return s.flushUpdateProgressLocked(now)
	}
	job := cloneHelperBatch(previous)
	oldCommands, oldStatuses := s.data.ManagementCommands, s.data.AgentStatuses
	s.data.ManagementCommands, s.data.AgentStatuses = maps.Clone(oldCommands), maps.Clone(oldStatuses)
	active := 0
	for i := range job.Rows {
		row := &job.Rows[i]
		if !batchRowRunning(row.Phase) {
			continue
		}
		progress := s.data.AgentStatuses[row.ServerID].Update
		if progress != nil && progress.Component == "helper" && progress.StartedAt.Equal(row.StartedAt) {
			row.Phase = progress.Phase
			if terminalUpdatePhase(row.Phase) {
				row.Reason, row.FinishedAt = progress.Message, progress.UpdatedAt
			}
		}
		if batchRowRunning(row.Phase) && now.Sub(row.StartedAt) >= helperBatchTimeout {
			row.Phase, row.Reason, row.FinishedAt = "failed", "升级超过 5 分钟，停止等待；请核对 Helper 实际版本", now
			status := s.data.AgentStatuses[row.ServerID]
			if progress != nil && progress.StartedAt.Equal(row.StartedAt) {
				copy := *progress
				copy.Phase, copy.Message, copy.UpdatedAt = "failed", row.Reason, now
				copy.appendLine(now, row.Reason)
				status.Update = &copy
				s.data.AgentStatuses[row.ServerID] = status
			}
		}
		if terminalUpdatePhase(row.Phase) {
			kept := []managementCommand{}
			for _, command := range s.data.ManagementCommands[row.ServerID] {
				if command.ID != row.CommandID {
					kept = append(kept, command)
				}
			}
			s.data.ManagementCommands[row.ServerID] = kept
		} else {
			active++
		}
	}
	byID := map[string]helperBatchServer{}
	for _, server := range servers {
		byID[server.ID] = server
	}
	for i := range job.Rows {
		row := &job.Rows[i]
		if row.Phase != "queued" || active >= 2 {
			continue
		}
		status := s.data.AgentStatuses[row.ServerID]
		server, exists := byID[row.ServerID]
		if !exists || server.Status != "connected" || status.ReportedAt.IsZero() || now.Sub(status.ReportedAt) > helperStaleTimeout {
			row.Phase, row.Reason, row.FinishedAt = "failed", "轮到此服务器时已离线或已移除", now
			continue
		}
		if status.Helper.Version == row.Artifact.Version {
			row.Phase, row.Reason, row.FinishedAt = "success", "已确认目标版本", now
			continue
		}
		artifact := row.Artifact
		artifact.GitHubAccelerator = releaseurl.DefaultAccelerator
		if s.data.GitHubAccelerator != nil {
			artifact.GitHubAccelerator = *s.data.GitHubAccelerator
		}
		payload, _ := json.Marshal(artifact)
		command, err := s.enqueueManagementCommandLocked(row.ServerID, "helper.update", payload, now, true)
		if err != nil {
			row.Phase, row.Reason, row.FinishedAt = "failed", sanitizeUpdateText(err.Error()), now
			continue
		}
		row.Phase, row.StartedAt, row.CommandID = "dispatching", now, command.ID
		active++
	}
	finishHelperBatch(job, now)
	if reflect.DeepEqual(previous, job) {
		s.data.ManagementCommands, s.data.AgentStatuses = oldCommands, oldStatuses
		return s.flushUpdateProgressLocked(now)
	}
	job.UpdatedAt = now
	s.data.HelperBatch = job
	if err := s.saveLocked(); err != nil {
		s.data.HelperBatch, s.data.ManagementCommands, s.data.AgentStatuses = previous, oldCommands, oldStatuses
		return err
	}
	s.updateProgressSavedAt, s.updateProgressDirty = now, false
	return nil
}

func (a *app) runHelperBatch(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			a.helperState.mu.Lock()
			active := a.helperState.data.HelperBatch != nil && a.helperState.data.HelperBatch.Phase == "running"
			a.helperState.mu.Unlock()
			if !active {
				a.helperState.mu.Lock()
				_ = a.helperState.flushUpdateProgressLocked(now.UTC())
				a.helperState.mu.Unlock()
				continue
			}
			store, ok := a.adminStore.(helperBatchServerStore)
			if !ok {
				continue
			}
			servers, err := store.HelperBatchServers(ctx)
			if err == nil {
				_ = a.helperState.advanceHelperBatch(servers, now.UTC())
			}
		}
	}
}

func (a *app) helperBatchHandler(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false})
		return
	}
	store, ok := a.adminStore.(helperBatchServerStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "服务器列表暂不可用"})
		return
	}
	servers, err := store.HelperBatchServers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "服务器列表暂不可用"})
		return
	}
	release, _ := a.releaseCache.snapshot()
	if r.Method == http.MethodPost {
		var request struct {
			Action    string   `json:"action"`
			JobID     string   `json:"job_id"`
			Version   string   `json:"version"`
			ServerIDs []string `json:"server_ids"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
			return
		}
		switch request.Action {
		case "start":
			if request.Version != release.HelperVersion {
				err = errors.New("最新版本已变化，请重新确认")
			} else {
				_, err = a.helperState.startHelperBatch(servers, release, request.ServerIDs, time.Now().UTC())
			}
		case "stop":
			err = a.helperState.stopHelperBatch(request.JobID, time.Now().UTC())
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid action"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": err.Error()})
			return
		}
	}
	a.helperState.mu.Lock()
	job := cloneHelperBatch(a.helperState.data.HelperBatch)
	preview := a.helperState.helperBatchPreviewLocked(servers, release, time.Now().UTC())
	a.helperState.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "version": release.HelperVersion, "preview": preview, "job": job})
}
