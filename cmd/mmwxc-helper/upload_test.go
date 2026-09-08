package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadDetailedMetricsProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != detailedEndpoint {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("missing bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"settings":{"default_close_wait_timeout_seconds":null,"online_ip_grace_period_seconds":30,"users":[]}}`))
	}))
	defer server.Close()

	settings, err := uploadDetailedMetrics(context.Background(), server.Client(), config{
		CustomAPIURL: server.URL,
		ServerID:     "7",
		Token:        "secret",
	}, detailedConnectionSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if settings.OnlineIPGracePeriodSeconds != 30 || settings.Users == nil {
		t.Fatalf("unexpected settings: %#v", settings)
	}
}

func TestUploadDetailedMetricsRejectsMalformedControllerResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"success":"yes"}`))
	}))
	defer server.Close()

	_, err := uploadDetailedMetrics(context.Background(), server.Client(), config{
		CustomAPIURL: server.URL,
		ServerID:     "7",
		Token:        "secret",
	}, detailedConnectionSnapshot{})
	if err == nil {
		t.Fatal("malformed controller response was accepted")
	}
}
