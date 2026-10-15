package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func countingSystemctl(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	active := filepath.Join(directory, "active")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$PROBE_CALLS"
case "$1" in
list-units)
  if test -f "$PROBE_ACTIVE"; then
    for unit; do :; done
    printf '%s loaded active running Test service\n' "$unit"
  fi
  ;;
is-active) test -f "$PROBE_ACTIVE" ;;
show) printf '123\n' ;;
start|restart) touch "$PROBE_ACTIVE" ;;
stop) rm -f "$PROBE_ACTIVE" ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "systemctl"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+":"+os.Getenv("PATH"))
	t.Setenv("PROBE_CALLS", calls)
	t.Setenv("PROBE_ACTIVE", active)
	invalidateServiceCaches()
	t.Cleanup(invalidateServiceCaches)
	return calls, active
}

func TestEmbeddedTicksDoNotLoadInactiveServices(t *testing.T) {
	calls, _ := countingSystemctl(t)
	ctx := context.Background()
	now := time.Now()
	units := []string{"xray.service", coreServiceName, "mmw-agent.service", helperServiceName}
	for tick := 0; tick < 36; tick++ {
		for _, unit := range units {
			if probeServiceActive(ctx, unit, now.Add(time.Duration(tick)*5*time.Second), false) {
				t.Fatalf("inactive %s reported active", unit)
			}
			// Ownership status used to load the same stopped unit again here.
			if pid, err := serviceMainPID(ctx, unit); err != nil || pid != 0 {
				t.Fatalf("inactive %s MainPID=%d err=%v", unit, pid, err)
			}
		}
	}
	data, _ := os.ReadFile(calls)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3*len(units) {
		t.Fatalf("expected one call per unit per minute, got %s", data)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "list-units ") {
			t.Fatalf("inactive probe could load a unit: %s", line)
		}
	}
}

func TestServiceCgroupAppearanceAndLifecycleInvalidateCache(t *testing.T) {
	_, active := countingSystemctl(t)
	ctx := context.Background()
	for _, unit := range []string{"xray.service", coreServiceName, "mmw-agent.service", helperServiceName} {
		now := time.Now()
		if probeServiceActive(ctx, unit, now, false) {
			t.Fatal("initially active")
		}
		if err := os.WriteFile(active, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if !probeServiceActive(ctx, unit, now.Add(5*time.Second), true) {
			t.Fatalf("%s cgroup appearance did not bypass negative cache on first tick", unit)
		}
		if pid, err := serviceMainPID(ctx, unit); err != nil || pid != 123 {
			t.Fatalf("active MainPID=%d err=%v", pid, err)
		}
		for _, action := range []string{"stop", "start", "stop", "restart", "stop"} {
			if err := systemctl(ctx, action, unit); err != nil {
				t.Fatal(err)
			}
			if got := probeServiceActive(ctx, unit, now, false); got != (action != "stop") {
				t.Fatalf("%s after %s: active=%t", unit, action, got)
			}
		}
	}
}
