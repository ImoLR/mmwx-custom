package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestGitHubAcceleratorSettingsAreAdminOnlyAndNormalized(t *testing.T) {
	state, err := openHelperState(filepath.Join(t.TempDir(), "helper-state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	application := &app{apiToken: "operator-token", helperState: state}

	request := httptest.NewRequest(http.MethodGet, "/api/custom/settings/github-accelerator", nil)
	response := httptest.NewRecorder()
	application.githubAcceleratorHandler(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized GET status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPut, "/api/custom/settings/github-accelerator", bytes.NewBufferString(`{"github_accelerator":"https://mirror.example/base"}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response = httptest.NewRecorder()
	application.githubAcceleratorHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authorized PUT status=%d body=%s", response.Code, response.Body.String())
	}
	var body githubAcceleratorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Accelerator != "https://mirror.example/base/" || body.Effective != body.Accelerator {
		t.Fatalf("unexpected response: %#v", body)
	}
	if len(state.data.ManagementCommands) != 0 {
		t.Fatal("saving the accelerator unexpectedly queued a node command")
	}

	request = httptest.NewRequest(http.MethodPut, "/api/custom/settings/github-accelerator", bytes.NewBufferString(`{"github_accelerator":"file:///tmp/core"}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response = httptest.NewRecorder()
	application.githubAcceleratorHandler(response, request)
	if response.Code != http.StatusBadRequest || state.githubAccelerator() != "https://mirror.example/base/" {
		t.Fatalf("unsafe setting status=%d current=%q", response.Code, state.githubAccelerator())
	}
}
