package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPeriodicLogLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	var lines []string
	limiter := newPeriodicLogLimiter()
	limiter.now = func() time.Time { return now }
	limiter.print = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	err := errors.New("read tcp 127.0.0.1:12345->127.0.0.2:443: reset at 2026-10-15T03:15:00+08:00")
	limiter.report("upload failed", err)
	if len(lines) != 1 || !strings.Contains(lines[0], err.Error()) {
		t.Fatalf("first occurrence: %v", lines)
	}
	limiter.report("upload failed", errors.New("read tcp 127.0.0.1:54321->127.0.0.2:443: reset at 2026-10-15T03:15:05+08:00"))
	if len(lines) != 1 {
		t.Fatalf("normalized repeat was logged: %v", lines)
	}
	limiter.report("upload failed", errors.New("HTTP 429 rate limited"))
	limiter.report("upload failed", err)
	if len(lines) != 2 {
		t.Fatalf("different/alternating errors: %v", lines)
	}
	now = now.Add(periodicLogInterval)
	limiter.report("upload failed", err)
	if len(lines) != 3 || !strings.Contains(lines[2], "repeated 3 times in the last 10m") {
		t.Fatalf("summary: %v", lines)
	}
	limiter.report("upload failed", err)
	if len(lines) != 3 {
		t.Fatalf("more than one summary in interval: %v", lines)
	}
	limiter.report("other site", err)
	if len(lines) != 4 {
		t.Fatalf("sites share suppression: %v", lines)
	}
	limiter.report("upload failed", nil)
	limiter.report("upload failed", nil)
	if len(lines) != 5 || !strings.Contains(lines[4], "recovered") {
		t.Fatalf("recovery must log exactly once: %v", lines)
	}
	limiter.report("upload failed", err)
	if len(lines) != 6 {
		t.Fatalf("new incident was suppressed: %v", lines)
	}
}

func TestPeriodicLogLimiterBoundsMemory(t *testing.T) {
	limiter := newPeriodicLogLimiter()
	limiter.print = func(string, ...any) {}
	for i := 0; i < 1000; i++ {
		limiter.report("upload", fmt.Errorf("different error %d", i))
	}
	if len(limiter.sites["upload"].seen) > 64 {
		t.Fatal("unbounded error history")
	}
}

func TestPeriodicLogNormalizesIPv6AndNamedPorts(t *testing.T) {
	limiter := newPeriodicLogLimiter()
	lines := 0
	limiter.print = func(string, ...any) { lines++ }
	for _, port := range []int{12345, 54321} {
		limiter.report("upload", fmt.Errorf("read tcp [2001:db8::1]:%d: timeout", port))
		limiter.report("IP limiter", fmt.Errorf("inbound port %d has multiple authenticated users", port))
	}
	if lines != 2 {
		t.Fatalf("port changes produced %d lines, want 2", lines)
	}
}
