package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMuxSubscriptionProxy(t *testing.T) {
	const userAgent = "clash-verge/v2.0"
	for _, tc := range []struct {
		name        string
		method      string
		path        string
		requestBody string
		status      int
		contentType string
		body        string
	}{
		{"subscription", http.MethodGet, "/x/abc", "", http.StatusOK, "text/yaml", "proxies: []\n"},
		{"not found", http.MethodGet, "/x/unknown", "", http.StatusNotFound, "text/plain", "not found\n"},
		{"method", http.MethodPost, "/x/abc", "request body", http.StatusAccepted, "text/plain", "accepted\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{
				"Content-Type":          tc.contentType,
				"Subscription-Userinfo": "upload=1; download=2; total=100; expire=2000000000",
				"Profile-Title":         "base64:dGVzdA==",
				"Content-Disposition":   `attachment; filename="subscription.yaml"`,
				"Cache-Control":         "private, max-age=60",
				"ETag":                  `"subscription-v1"`,
				"Expires":               "Wed, 07 Oct 2026 00:00:00 GMT",
				"Vary":                  "User-Agent",
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.Path != tc.path || r.URL.RawQuery != "t=auto" || r.UserAgent() != userAgent {
					t.Errorf("upstream request = %s %s UA=%q", r.Method, r.URL, r.UserAgent())
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != tc.requestBody {
					t.Errorf("request body = %q, err = %v", body, err)
				}
				for key, value := range headers {
					w.Header().Set(key, value)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer upstream.Close()
			target, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			frontendDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(frontendDir, "index.html"), []byte("<html>Custom UI</html>"), 0600); err != nil {
				t.Fatal(err)
			}
			api := &app{allowedOrigins: parseOrigins("https://custom.example")}
			request := httptest.NewRequest(tc.method, tc.path+"?t=auto", strings.NewReader(tc.requestBody))
			request.Header.Set("User-Agent", userAgent)
			request.Header.Set("Origin", "https://custom.example")
			response := httptest.NewRecorder()
			newMux(api, target, frontendDir).ServeHTTP(response, request)
			if response.Code != tc.status || response.Body.String() != tc.body {
				t.Fatalf("response = %d %q, want %d %q", response.Code, response.Body.String(), tc.status, tc.body)
			}
			for key, value := range headers {
				if got := response.Header().Get(key); got != value {
					t.Errorf("%s = %q, want %q", key, got, value)
				}
			}
			if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("unexpected CORS header = %q", got)
			}
		})
	}
}

func TestMuxSPA(t *testing.T) {
	const index = "<html>Custom UI</html>"
	frontendDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(frontendDir, "index.html"), []byte(index), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("SPA request reached upstream: %s", r.URL)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	newMux(&app{}, target, frontendDir).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users", nil))
	if response.Code != http.StatusOK || response.Body.String() != index {
		t.Fatalf("response = %d %q, want 200 %q", response.Code, response.Body.String(), index)
	}
	if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content-type = %q, want text/html", got)
	}
}

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
