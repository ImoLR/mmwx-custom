package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func int64Value(value int64) *int64 { return &value }

func TestMachineActiveTCPDefinition(t *testing.T) {
	counts := tcpStateCounts{Established: 10, SynSent: 1, SynRecv: 2, FinWait1: 3, FinWait2: 4, TimeWait: 100, CloseWait: 5, LastAck: 6, Closing: 7, Close: 8, Listen: 11, Unknown: 9}
	if got := machineActiveTCP(counts); got != 38 {
		t.Fatalf("machine active=%d, want 38", got)
	}
}

func TestMachineProtectionDisabledRemovesOnlyOwnedTable(t *testing.T) {
	removed := false
	manager := &machineProtectionManager{supported: true, now: time.Now, run: func(_ context.Context, script []byte, remove bool) error {
		removed = remove
		if len(script) != 0 {
			t.Fatalf("disabled protection supplied rules: %s", script)
		}
		return nil
	}}
	status := manager.reconcile(context.Background(), nil, tcpStateCounts{Total: 25, Established: 4}, []uint32{10017})
	if !removed || !status.Effective || status.Blocking || status.Configured.Enabled {
		t.Fatalf("unexpected disabled status: %#v", status)
	}
}

func TestMachineProtectionThresholdsAndControlPlaneExemptions(t *testing.T) {
	var script string
	manager := &machineProtectionManager{supported: true, now: time.Now, run: func(_ context.Context, data []byte, remove bool) error {
		if remove {
			t.Fatal("enabled protection unexpectedly removed its table")
		}
		script = string(data)
		return nil
	}}
	settings := machineProtectionSettings{Enabled: true, MaxActive: int64Value(10), MaxTotal: int64Value(200)}
	status := manager.reconcile(context.Background(), &settings, tcpStateCounts{Total: 150, Established: 10, TimeWait: 140}, []uint32{10017, 10016, 10017})
	if !status.Effective || status.Blocking || status.ThresholdReason != "" {
		t.Fatalf("unexpected threshold status: %#v", status)
	}
	for _, expected := range []string{
		"table inet " + machineProtectionTableName,
		"iifname \"lo\" return",
		"ct state established,related return",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("machine protection script missing %q:\n%s", expected, script)
		}
	}
	for _, forbidden := range []string{"tcp dport 22", "flush ruleset", "ip saddr", "ip daddr"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("machine protection script contains unsafe control-plane matcher %q:\n%s", forbidden, script)
		}
	}
}

func TestMachineProtectionLegacyActiveIsCompatibilityFallbackOnly(t *testing.T) {
	manager := &machineProtectionManager{supported: true, now: time.Now, run: func(_ context.Context, data []byte, remove bool) error {
		if remove || !strings.Contains(string(data), "ct state new counter drop") {
			t.Fatalf("legacy active fallback did not install the blocking rule: remove=%v script=%s", remove, data)
		}
		return nil
	}}
	settings := machineProtectionSettings{Enabled: true, MaxActive: int64Value(10)}
	status := manager.reconcile(context.Background(), &settings, tcpStateCounts{Total: 150, Established: 10, TimeWait: 140}, []uint32{10017})
	if !status.Effective || !status.Blocking || status.ThresholdReason != "active_compat" {
		t.Fatalf("unexpected legacy threshold status: %#v", status)
	}
}

func TestMachineProtectionBelowThresholdInstallsNonBlockingOwnedTable(t *testing.T) {
	var script string
	manager := &machineProtectionManager{supported: true, now: time.Now, run: func(_ context.Context, data []byte, remove bool) error {
		script = string(data)
		return nil
	}}
	settings := machineProtectionSettings{Enabled: true, MaxActive: int64Value(1000), MaxTotal: int64Value(2000)}
	status := manager.reconcile(context.Background(), &settings, tcpStateCounts{Total: 150, Established: 10, TimeWait: 140}, []uint32{10017})
	if !status.Effective || status.Blocking || status.LastError != "" {
		t.Fatalf("unexpected non-blocking status: %#v", status)
	}
	if strings.Contains(script, "ct state new counter drop") {
		t.Fatalf("below-threshold rules unexpectedly drop new traffic:\n%s", script)
	}
}

func TestMachineProtectionTotalThreshold(t *testing.T) {
	manager := &machineProtectionManager{supported: true, now: time.Now, run: func(_ context.Context, data []byte, remove bool) error {
		if remove || !strings.Contains(string(data), "ct state new counter drop") {
			t.Fatalf("total threshold did not install the blocking rule: remove=%v script=%s", remove, data)
		}
		return nil
	}}
	settings := machineProtectionSettings{Enabled: true, MaxActive: int64Value(1000), MaxTotal: int64Value(150)}
	status := manager.reconcile(context.Background(), &settings, tcpStateCounts{Total: 150, Established: 10, TimeWait: 140}, []uint32{10017})
	if !status.Effective || !status.Blocking || status.ThresholdReason != "total" {
		t.Fatalf("unexpected total threshold status: %#v", status)
	}
}

func TestMachineProtectionMalformedConfigFailsOpen(t *testing.T) {
	removed := false
	manager := &machineProtectionManager{supported: true, now: time.Now, run: func(_ context.Context, _ []byte, remove bool) error {
		removed = remove
		return nil
	}}
	settings := machineProtectionSettings{Enabled: true, MaxActive: int64Value(0)}
	status := manager.reconcile(context.Background(), &settings, tcpStateCounts{Total: 100}, []uint32{10017})
	if !removed || status.Effective || status.Blocking || status.LastError == "" {
		t.Fatalf("malformed config did not fail open: %#v", status)
	}
}

func TestRenderMachineProtectionNftSyntax(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft is not installed")
	}
	script := strings.ReplaceAll(renderMachineProtectionNft([]uint32{10016, 10017}, true), machineProtectionTableName, machineProtectionTableName+"_test")
	command := exec.CommandContext(context.Background(), "nft", "-c", "-f", "-")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("nft rejected generated machine protection rules: %v: %s", err, output)
	}
}
