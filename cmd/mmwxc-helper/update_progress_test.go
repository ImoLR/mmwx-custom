package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostUpdateProgressUsesHelperAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != updateProgressEndpoint || r.Header.Get("Authorization") != "Bearer helper-token" {
			t.Fatalf("unexpected request path=%s authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["server_id"] != "custom-id" || request["component"] != "core" || request["phase"] != "installing" || request["target_version"] != "abcdef0" {
			t.Fatalf("progress request=%#v", request)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := config{CustomAPIURL: server.URL, ServerID: "custom-id", Token: "helper-token"}
	if err := postUpdateProgress(context.Background(), server.Client(), cfg, "core", "installing", "abcdef0", ""); err != nil {
		t.Fatal(err)
	}
}
