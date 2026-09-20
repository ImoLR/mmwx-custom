package main

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/ImoLR/mmwx-custom/internal/releaseurl"
)

const maxGitHubAcceleratorRequestBytes = 4096

type githubAcceleratorResponse struct {
	Success     bool   `json:"success"`
	Accelerator string `json:"github_accelerator"`
	Effective   string `json:"effective_value"`
}

func (s *helperState) githubAccelerator() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.GitHubAccelerator == nil {
		return releaseurl.DefaultAccelerator
	}
	return *s.data.GitHubAccelerator
}

func (s *helperState) setGitHubAccelerator(value string) (string, error) {
	normalized, err := releaseurl.NormalizeAccelerator(value)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.GitHubAccelerator = &normalized
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return normalized, nil
}

func (a *app) githubAcceleratorHandler(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		value := a.helperState.githubAccelerator()
		writeJSON(w, http.StatusOK, githubAcceleratorResponse{Success: true, Accelerator: value, Effective: effectiveAcceleratorLabel(value)})
	case http.MethodPut:
		var request struct {
			Accelerator string `json:"github_accelerator"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxGitHubAcceleratorRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "invalid request"})
			return
		}
		value, err := a.helperState.setGitHubAccelerator(request.Accelerator)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, githubAcceleratorResponse{Success: true, Accelerator: value, Effective: effectiveAcceleratorLabel(value)})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
	}
}

func effectiveAcceleratorLabel(value string) string {
	if value == "" {
		return "GitHub 官方直连"
	}
	return value
}
