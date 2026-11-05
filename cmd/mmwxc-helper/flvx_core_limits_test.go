package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// 不限制 (memory_mb / cpu_percent 0): the unit has no MemoryMax / CPUQuota
// lines and is otherwise the limited unit; the running Core is set to
// infinity / no quota. Numbers render exactly as before.
func TestFLVXCoreUnlimitedUnit(t *testing.T) {
	render := func(memory, cpu int) string {
		m := &flvxManager{binary: flvxBinaryPath, configPath: flvxConfigPath, socket: flvxSocketPath}
		m.state.Payload.Config = flvx.Config{MemoryMB: memory, CPUPercent: cpu}
		return m.serviceUnit()
	}
	limited := render(128, 50)
	if got, want := render(0, 0), strings.Replace(limited, "MemoryMax=128M\nCPUQuota=50%\n", "", 1); got != want || got == limited {
		t.Fatalf("unlimited unit:\n%s", got)
	}
	if got := render(0, 50); strings.Contains(got, "MemoryMax") || !strings.Contains(got, "\nUMask=0077\nCPUQuota=50%\nNoNewPrivileges=yes\n") {
		t.Fatalf("memory-only unlimited unit:\n%s", got)
	}
	if got := render(256, 0); strings.Contains(got, "CPUQuota") || !strings.Contains(got, "\nUMask=0077\nMemoryMax=256M\nNoNewPrivileges=yes\n") {
		t.Fatalf("cpu-only unlimited unit:\n%s", got)
	}
	for in, want := range map[[2]int][]string{
		{0, 0}:    {"MemoryMax=infinity", "CPUQuota="},
		{128, 50}: {"MemoryMax=128M", "CPUQuota=50%"},
		{0, 400}:  {"MemoryMax=infinity", "CPUQuota=400%"},
		{4096, 0}: {"MemoryMax=4096M", "CPUQuota="},
	} {
		if got := flvxRuntimeLimits(flvx.Config{MemoryMB: in[0], CPUPercent: in[1]}); !reflect.DeepEqual(got, want) {
			t.Errorf("%v: %q", in, got)
		}
	}
}

// The Helper accepts a signed 0 / 0 share and advertises that it does.
func TestFLVXCoreUnlimitedAccept(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	e := flvx.Envelope{Payload: m.state.Payload, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	e.Payload.ServerID = "7"
	e.Payload.Version++
	e.Payload.Config.MemoryMB, e.Payload.Config.CPUPercent = 0, 0
	e.Sign("helper")
	if err := m.accept(e); err != nil {
		t.Fatal(err)
	}
	if c := m.state.Payload.Config; c.MemoryMB != 0 || c.CPUPercent != 0 {
		t.Fatalf("accepted config %+v", c)
	}
	found := false
	for _, c := range helperCapabilities {
		found = found || c == flvx.CoreUnlimitedCapability
	}
	if !found {
		t.Fatal("flvx.core_unlimited.v1 not advertised")
	}
}
