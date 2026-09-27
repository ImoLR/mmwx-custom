package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const machineProtectionTableName = "mmwxc_machine_protection"

// Machine Active deliberately excludes TIME_WAIT, CLOSE and unknown rows. It
// counts TCP states that still represent a live handshake, data path or close
// handshake: ESTABLISHED, SYN_*, FIN_WAIT*, CLOSE_WAIT, LAST_ACK and CLOSING.
func machineActiveTCP(counts tcpStateCounts) int64 {
	return counts.Established + counts.SynSent + counts.SynRecv + counts.FinWait1 + counts.FinWait2 + counts.CloseWait + counts.LastAck + counts.Closing
}

func machineControlledPorts(snapshot detailedConnectionSnapshot) []uint32 {
	ports := make([]uint32, 0, len(snapshot.Inbounds))
	for _, inbound := range snapshot.Inbounds {
		ports = append(ports, inbound.Port)
	}
	return normalizeMachinePorts(ports)
}

func normalizeMachinePorts(values []uint32) []uint32 {
	seen := make(map[uint32]struct{}, len(values))
	for _, port := range values {
		if port > 0 && port <= 65535 {
			seen[port] = struct{}{}
		}
	}
	ports := make([]uint32, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports
}

func validateMachineProtection(settings machineProtectionSettings) error {
	if settings.MaxActive != nil && *settings.MaxActive <= 0 {
		return errors.New("machine max_active must be positive")
	}
	if settings.MaxTotal != nil && *settings.MaxTotal <= 0 {
		return errors.New("machine max_total must be positive")
	}
	if settings.Enabled && settings.MaxActive == nil && settings.MaxTotal == nil {
		return errors.New("enabled machine protection requires max_active or max_total")
	}
	return nil
}

func machineProtectionThreshold(settings machineProtectionSettings, active, total int64) (bool, string) {
	if settings.MaxTotal != nil && total >= *settings.MaxTotal {
		return true, "total"
	}
	if settings.MaxTotal != nil {
		return false, ""
	}
	// max_active is retained only as a compatibility fallback for old saved
	// settings. Once max_total exists, the unified Machine Total is authoritative.
	if settings.MaxActive != nil && active >= *settings.MaxActive {
		return true, "active_compat"
	}
	return false, ""
}

func renderMachineProtectionNft(ports []uint32, blocking bool) string {
	var output strings.Builder
	fmt.Fprintf(&output, "table inet %s {\n", machineProtectionTableName)
	output.WriteString("  chain input {\n")
	output.WriteString("    type filter hook input priority -4; policy accept;\n")
	output.WriteString("    iifname \"lo\" return comment \"mmwxc loopback exemption\"\n")
	output.WriteString("    ct state established,related return comment \"mmwxc established control-plane exemption\"\n")
	if blocking && len(ports) > 0 {
		output.WriteString("    tcp dport { ")
		for index, port := range ports {
			if index > 0 {
				output.WriteString(", ")
			}
			fmt.Fprintf(&output, "%d", port)
		}
		output.WriteString(" } ct state new counter drop comment \"mmwxc controlled business threshold\"\n")
	}
	output.WriteString("  }\n}\n")
	return output.String()
}

type machineNftRunner func(context.Context, []byte, bool) error

type machineProtectionManager struct {
	supported bool
	run       machineNftRunner
	now       func() time.Time
}

func newMachineProtectionManager() *machineProtectionManager {
	_, err := exec.LookPath("nft")
	return &machineProtectionManager{supported: err == nil, run: runMachineProtectionNft, now: time.Now}
}

func (manager *machineProtectionManager) reconcile(ctx context.Context, configured *machineProtectionSettings, system tcpStateCounts, ports []uint32) machineProtectionStatus {
	ports = normalizeMachinePorts(ports)
	settings := machineProtectionSettings{}
	if configured != nil {
		settings = *configured
	}
	status := machineProtectionStatus{
		Supported: manager.supported, Configured: settings, Active: machineActiveTCP(system), Total: system.Total,
		ControlledPorts: append([]uint32(nil), ports...), LastReconciledAt: manager.now().UTC(),
	}
	if err := validateMachineProtection(settings); err != nil {
		status.LastError = err.Error()
		_ = manager.run(ctx, nil, true)
		return status
	}
	if !manager.supported {
		if settings.Enabled {
			status.LastError = "nftables is unavailable"
		}
		return status
	}
	if !settings.Enabled {
		if err := manager.run(ctx, nil, true); err != nil {
			status.LastError = err.Error()
			return status
		}
		status.Effective = true
		return status
	}
	if len(ports) == 0 {
		status.LastError = "no controlled business ports are available"
		_ = manager.run(ctx, nil, true)
		return status
	}
	status.Blocking, status.ThresholdReason = machineProtectionThreshold(settings, status.Active, status.Total)
	if err := manager.run(ctx, []byte(renderMachineProtectionNft(ports, status.Blocking)), false); err != nil {
		status.Blocking = false
		status.ThresholdReason = ""
		status.LastError = err.Error()
		return status
	}
	status.Effective = true
	return status
}

func runMachineProtectionNft(ctx context.Context, script []byte, removeOnly bool) error {
	if removeOnly {
		deleteCommand := exec.CommandContext(ctx, "nft", "delete", "table", "inet", machineProtectionTableName)
		_ = deleteCommand.Run()
		return nil
	}
	validationScript := bytes.ReplaceAll(script, []byte(machineProtectionTableName), []byte(machineProtectionTableName+"_validate"))
	check := exec.CommandContext(ctx, "nft", "-c", "-f", "-")
	check.Stdin = bytes.NewReader(validationScript)
	if output, err := check.CombinedOutput(); err != nil {
		// A stale blocking table is less safe than no guard when the replacement
		// cannot even be validated. Remove only our table and report degraded.
		deleteCommand := exec.CommandContext(ctx, "nft", "delete", "table", "inet", machineProtectionTableName)
		_ = deleteCommand.Run()
		return fmt.Errorf("validate machine protection rules: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deleteCommand := exec.CommandContext(ctx, "nft", "delete", "table", "inet", machineProtectionTableName)
	_ = deleteCommand.Run()
	apply := exec.CommandContext(ctx, "nft", "-f", "-")
	apply.Stdin = bytes.NewReader(script)
	if output, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("apply machine protection rules: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
