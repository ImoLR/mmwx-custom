package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	managedUserStatusTaskTimeout = 6 * time.Minute
	managedUserStatusTaskTTL     = 30 * time.Minute
	maxOfficialOperationBody     = 1 << 20
)

type managedUserStatusTask struct {
	ID              string     `json:"id"`
	Username        string     `json:"username"`
	ExpectedActive  bool       `json:"expected_active"`
	Status          string     `json:"status"`
	Message         string     `json:"message,omitempty"`
	UpstreamStatus  int        `json:"upstream_status,omitempty"`
	DurationMS      int64      `json:"duration_ms,omitempty"`
	StartedAt       time.Time  `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ConfirmedActive *bool      `json:"confirmed_active,omitempty"`
}

type managedUserStatusTaskStore struct {
	mu           sync.Mutex
	tasks        map[string]managedUserStatusTask
	activeByUser map[string]string
}

func newManagedUserStatusTaskStore() *managedUserStatusTaskStore {
	return &managedUserStatusTaskStore{
		tasks:        make(map[string]managedUserStatusTask),
		activeByUser: make(map[string]string),
	}
}

func newManagedUserStatusTaskID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (s *managedUserStatusTaskStore) start(username string, expected bool) (managedUserStatusTask, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, task := range s.tasks {
		finished := task.FinishedAt
		if finished != nil && now.Sub(*finished) > managedUserStatusTaskTTL {
			delete(s.tasks, id)
		}
	}
	if id := s.activeByUser[username]; id != "" {
		if task, ok := s.tasks[id]; ok && task.Status == "pending" {
			return managedUserStatusTask{}, errors.New("a status operation is already pending for this user")
		}
		delete(s.activeByUser, username)
	}
	id, err := newManagedUserStatusTaskID()
	if err != nil {
		return managedUserStatusTask{}, err
	}
	task := managedUserStatusTask{ID: id, Username: username, ExpectedActive: expected, Status: "pending", StartedAt: now}
	s.tasks[id] = task
	s.activeByUser[username] = id
	return task, nil
}

func (s *managedUserStatusTaskStore) get(id string) (managedUserStatusTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	return task, ok
}

func (s *managedUserStatusTaskStore) finish(id, status, message string, upstreamStatus int, confirmed *bool) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return
	}
	task.Status = status
	task.Message = message
	task.UpstreamStatus = upstreamStatus
	task.DurationMS = now.Sub(task.StartedAt).Milliseconds()
	task.FinishedAt = &now
	task.ConfirmedActive = confirmed
	s.tasks[id] = task
	if s.activeByUser[task.Username] == id {
		delete(s.activeByUser, task.Username)
	}
}

type officialStatusRequest struct {
	target  *url.URL
	body    []byte
	headers http.Header
}

func (a *app) userStatusTaskHandler(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	if a.userStatusTasks == nil || a.officialInternalTarget == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "status task service unavailable"})
		return
	}
	const prefix = "/api/custom/user-status-tasks"
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if r.Method == http.MethodGet {
		if path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "task id is required"})
			return
		}
		task, ok := a.userStatusTasks.get(path)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "status task not found"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "task": task})
		return
	}
	if r.Method != http.MethodPost || path != "" {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	expected, err := strconv.ParseBool(r.URL.Query().Get("is_active"))
	if username == "" || len(username) > 256 || err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid status task request"})
		return
	}
	if strings.TrimSpace(r.Header.Get("MM-Authorization")) == "" ||
		strings.TrimSpace(r.Header.Get("X-Secure-Channel")) == "" ||
		strings.TrimSpace(r.Header.Get("X-Session-Id")) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "official secure channel is required"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxOfficialOperationBody+1))
	if err != nil || len(body) == 0 || len(body) > maxOfficialOperationBody {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid official operation body"})
		return
	}
	task, err := a.userStatusTasks.start(username, expected)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": err.Error()})
		return
	}
	headers := make(http.Header)
	for _, name := range []string{"MM-Authorization", "X-Secure-Channel", "X-Session-Id", "Content-Type"} {
		if value := r.Header.Get(name); value != "" {
			headers.Set(name, value)
		}
	}
	request := officialStatusRequest{target: a.officialInternalTarget, body: body, headers: headers}
	log.Printf("[mmwx-custom] user status task started id=%s username=%s expected_active=%t target=%s", task.ID, username, expected, request.target.Redacted())
	go a.runUserStatusTask(task, request)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "task": task})
}

func (a *app) runUserStatusTask(task managedUserStatusTask, operation officialStatusRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), managedUserStatusTaskTimeout)
	defer cancel()
	endpoint := *operation.target
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v3"
	endpoint.RawQuery = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(operation.body))
	if err == nil {
		request.Header = operation.headers.Clone()
		request.Host = operation.target.Host
	}
	upstreamStatus := 0
	if err == nil {
		response, requestErr := (&http.Client{Transport: http.DefaultTransport}).Do(request)
		err = requestErr
		if response != nil {
			upstreamStatus = response.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxOfficialOperationBody))
			_ = response.Body.Close()
			if err == nil && (response.StatusCode < 200 || response.StatusCode >= 300) {
				err = fmt.Errorf("official status API returned HTTP %d", response.StatusCode)
			}
		}
	}

	store, ok := a.adminStore.(userManagementStore)
	var state managedUserState
	var stateErr error
	if ok && store != nil {
		verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
		state, stateErr = store.ManagedUserState(verifyCtx, task.Username)
		verifyCancel()
	} else {
		stateErr = errOperatorAuthorizationUnavailable
	}
	if stateErr == nil && state.Exists && state.IsActive == task.ExpectedActive {
		confirmed := state.IsActive
		message := "user status confirmed"
		if err != nil {
			message = "official response failed, but the final database state was confirmed"
		}
		a.userStatusTasks.finish(task.ID, "succeeded", message, upstreamStatus, &confirmed)
		log.Printf("[mmwx-custom] user status task finished id=%s username=%s status=succeeded upstream_status=%d duration=%s", task.ID, task.Username, upstreamStatus, time.Since(task.StartedAt))
		return
	}
	message := "unable to confirm the final user status"
	if err != nil {
		message = err.Error()
	} else if stateErr != nil {
		message = "official status completed, but database verification failed"
	} else if !state.Exists {
		message = "user no longer exists"
	} else {
		message = "official status completed, but database state did not change"
	}
	// Never expose an upstream HTML body or credentials through the task API.
	a.userStatusTasks.finish(task.ID, "failed", message, upstreamStatus, nil)
	log.Printf("[mmwx-custom] user status task finished id=%s username=%s status=failed upstream_status=%d duration=%s error=%v state_error=%v", task.ID, task.Username, upstreamStatus, time.Since(task.StartedAt), err, stateErr)
}
