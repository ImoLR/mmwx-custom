package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestProgressRetryOldControllerOnce(t *testing.T) {
	for _, status := range []int{400, 401, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var fields map[string]any
				if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
					t.Error(err)
				}
				call := calls.Add(1)
				if call == 1 {
					if fields["percent"] != float64(42) || fields["bytes_total"] != float64(100) || fields["log_line"] == nil {
						t.Error("missing new fields")
					}
					w.WriteHeader(status)
					return
				}
				if len(fields) != 6 || fields["phase"] != "downloading" {
					t.Errorf("legacy retry: %+v", fields)
				}
				w.WriteHeader(400) // Even a rejecting old controller receives only one retry.
			}))
			defer server.Close()
			n := 42
			cfg := config{CustomAPIURL: server.URL, ServerID: "5", Token: "secret-token"}
			err := postUpdateProgress(context.Background(), server.Client(), cfg, "helper", "downloading", "v0.7.2", "", updateProgressDetails{Percent: &n, BytesDone: 42, BytesTotal: 100, LogLine: "download"})
			if err == nil {
				t.Fatal("expected rejection")
			}
			want := int32(1)
			if status == 400 {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d", calls.Load())
			}
		})
	}
}

func TestProgressCompatibilityAndSanitizing(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if calls.Add(1) == 1 {
			w.WriteHeader(400)
			return
		}
		for _, secret := range []string{"private-token", "private.example", "secret=foo"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("payload leaked %s", secret)
			}
		}
		if strings.Contains(string(raw), "log_line") {
			t.Error("legacy unknown field")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	cfg := config{CustomAPIURL: server.URL, ServerID: "5", Token: "private-token"}
	if err := postUpdateProgress(context.Background(), server.Client(), cfg, "helper", "success", "v0.7.2", "", updateProgressDetails{LogLine: "新进程首次上报 private-token https://private.example/a?secret=foo"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("missing compatibility retry")
	}
}

type progressTestTransport func(*http.Request) (*http.Response, error)

func (transport progressTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestSlowProgressDoesNotBlockUpdate(t *testing.T) {
	entered := make(chan struct{})
	client := &http.Client{Transport: progressTestTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Error("missing 5s timeout")
		}
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	reporter := newUpdateProgressReporter(client, config{CustomAPIURL: "http://127.0.0.1", ServerID: "5"})
	reporter.report("core", "downloading", "abcdef0", "")
	<-entered
	start := time.Now()
	for i := 0; i < 1000; i++ {
		reporter.report("core", "installing", "abcdef0", "")
	}
	if time.Since(start) > time.Second {
		t.Error("progress blocked update")
	}
	if len(reporter.queue) > 64 {
		t.Error("unbounded queue")
	}
	reporter.cancel()
	reporter.close()
}

func TestDownloadProgressRateAndUnknownLength(t *testing.T) {
	now := time.Now()
	var samples []updateProgressDetails
	writer := &downloadProgressWriter{total: 1000, lastAt: now, now: func() time.Time { return now }, report: func(d updateProgressDetails) { samples = append(samples, d) }}
	_, _ = writer.Write(make([]byte, 100))
	if len(samples) != 0 {
		t.Fatal("more than 1 post/s")
	}
	now = now.Add(time.Second)
	_, _ = writer.Write(make([]byte, 320))
	if len(samples) != 1 || *samples[0].Percent != 42 || samples[0].BytesDone != 420 {
		t.Fatalf("samples=%+v", samples)
	}
	now = now.Add(time.Second)
	_, _ = writer.Write(make([]byte, 40))
	if len(samples) != 1 {
		t.Fatal("posted less than 5% change")
	}
	_, _ = writer.Write(make([]byte, 10))
	if len(samples) != 2 || *samples[1].Percent != 47 {
		t.Fatal("missing 5% sample")
	}
	now = now.Add(time.Second)
	_, _ = writer.Write(make([]byte, 600))
	if *samples[2].Percent != 100 || samples[2].BytesDone != 1000 {
		t.Fatal("progress not bounded")
	}
	writer.total = -1
	now = now.Add(time.Second)
	_, _ = writer.Write(make([]byte, 100))
	if len(samples) != 3 {
		t.Fatal("unknown length invented percent")
	}
}
