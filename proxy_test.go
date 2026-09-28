package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestMMWXAPIProxyTimeoutReturnsSmallJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v3", nil)
	response := httptest.NewRecorder()
	mmwxAPIProxyWithTimeout(target, 5*time.Millisecond).ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if response.Body.Len() > 256 {
		t.Fatalf("proxy error body too large: %d bytes", response.Body.Len())
	}
	var body struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Success || body.Message == "" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestCustomServerWriteTimeoutAllowsOfficialMutations(t *testing.T) {
	if customServerWriteTimeout < 90*time.Second {
		t.Fatalf("write timeout = %s, want at least 90s", customServerWriteTimeout)
	}
	if customServerWriteTimeout <= mmwxProxyResponseHeaderTimeout {
		t.Fatalf("write timeout %s must exceed proxy timeout %s", customServerWriteTimeout, mmwxProxyResponseHeaderTimeout)
	}
}
