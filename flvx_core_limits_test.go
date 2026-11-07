package main

import (
	"context"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// 共享资源上限 「不限制」: memory_mb / cpu_percent 0 (or omitted) is stored as
// 0, goes as 0 to a Helper with flvx.core_unlimited.v1 and as 4096 / 400 to an
// older one (which rejects 0), signed either way; numbers are kept as typed.
func TestFLVXCoreUnlimitedPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	stored := func() flvx.Config {
		t.Helper()
		s, err := readFLVXShare(ctx, a.flvxDB(), 7, false)
		if err != nil {
			t.Fatal(err)
		}
		return s.Config
	}
	put := func(body map[string]any) int {
		t.Helper()
		body["status"] = "active"
		return flvxTestRequest(a, "PUT", "/api/custom/flvx/admin/shares/7", "operator-token", body).Code
	}
	helperConfig := func(unlimited bool) flvx.Config {
		t.Helper()
		e, err := a.flvxHelperDesired(ctx, 7, "7", "helper", "amd64", true, true, true, true, true, true, unlimited, false, nil)
		if err != nil || e == nil || !e.Verify("helper", "7", time.Now()) {
			t.Fatalf("helper payload: %v %+v", err, e)
		}
		return e.Payload.Config
	}

	// A new share without the fields (the API default) is 不限制.
	if code := put(map[string]any{"port_min": 30000, "port_max": 30100, "reset_day": 1}); code != 200 {
		t.Fatalf("new share: %d", code)
	}
	if c := stored(); c.MemoryMB != 0 || c.CPUPercent != 0 {
		t.Fatalf("omitted limits stored as %d / %d", c.MemoryMB, c.CPUPercent)
	}
	if c := helperConfig(true); c.MemoryMB != 0 || c.CPUPercent != 0 {
		t.Fatalf("capable Helper got %d / %d", c.MemoryMB, c.CPUPercent)
	}
	// The old Helper's own check (64–4096 MB, 1–400 %) passes: never 0, never small.
	if c := helperConfig(false); c.MemoryMB != flvx.MaxCoreMemoryMB || c.CPUPercent != flvx.MaxCoreCPUPercent {
		t.Fatalf("old Helper got %d / %d", c.MemoryMB, c.CPUPercent)
	}

	// Explicit 0 likewise; a typed number is kept for every Helper.
	if code := put(map[string]any{"port_min": 30000, "port_max": 30100, "reset_day": 1, "memory_mb": 0, "cpu_percent": 0}); code != 200 {
		t.Fatalf("explicit 0: %d", code)
	}
	if c := stored(); c.MemoryMB != 0 || c.CPUPercent != 0 {
		t.Fatalf("explicit 0 stored as %d / %d", c.MemoryMB, c.CPUPercent)
	}
	if code := put(map[string]any{"port_min": 30000, "port_max": 30100, "reset_day": 1, "memory_mb": 512, "cpu_percent": 0}); code != 200 {
		t.Fatalf("memory only: %d", code)
	}
	for _, unlimited := range []bool{true, false} {
		want := 0
		if !unlimited {
			want = flvx.MaxCoreCPUPercent
		}
		if c := helperConfig(unlimited); c.MemoryMB != 512 || c.CPUPercent != want {
			t.Fatalf("unlimited=%v: %d / %d", unlimited, c.MemoryMB, c.CPUPercent)
		}
	}
	for _, bad := range []map[string]any{{"memory_mb": 63}, {"memory_mb": 4097}, {"memory_mb": -1}, {"cpu_percent": -1}, {"cpu_percent": 401}} {
		body := map[string]any{"port_min": 30000, "port_max": 30100, "reset_day": 1}
		for k, v := range bad {
			body[k] = v
		}
		if code := put(body); code != 422 {
			t.Fatalf("%v accepted: %d", bad, code)
		}
	}
	if c := stored(); c.MemoryMB != 512 || c.CPUPercent != 0 {
		t.Fatalf("rejected update stored: %+v", c)
	}
	// A status-only change keeps the stored limits.
	if code := flvxTestRequest(a, "PUT", "/api/custom/flvx/admin/shares/7", "operator-token", map[string]any{"status": "paused"}).Code; code != 200 {
		t.Fatalf("pause: %d", code)
	}
	if c := stored(); c.MemoryMB != 512 || c.CPUPercent != 0 {
		t.Fatalf("pause changed limits: %+v", c)
	}

	// The capability reaches the share dialog's Helper object.
	if a.flvxHelpers()[7].CoreUnlimited {
		t.Fatal("v0.7.5 Helper marked core_unlimited")
	}
	key := flvxID(7)
	a.helperState.mu.Lock()
	status := a.helperState.data.AgentStatuses[key]
	status.Capabilities = append(append([]string(nil), status.Capabilities...), flvx.CoreUnlimitedCapability)
	a.helperState.data.AgentStatuses[key] = status
	a.helperState.mu.Unlock()
	if !a.flvxHelpers()[7].CoreUnlimited {
		t.Fatal("core_unlimited capability not seen")
	}
}
