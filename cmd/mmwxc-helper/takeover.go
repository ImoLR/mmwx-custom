package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const takeoverEndpoint = "/api/custom/agent/takeover"

type takeoverRuntime struct {
	XrayMode      string     `json:"xray_mode"`
	Status        string     `json:"status"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
}

type takeoverResponse struct {
	Success bool            `json:"success"`
	Runtime takeoverRuntime `json:"runtime"`
}

func requestTakeoverRuntime(ctx context.Context, client *http.Client, cfg config, mode string) (takeoverRuntime, error) {
	body, err := json.Marshal(map[string]string{"server_id": cfg.ServerID, "helper_version": helperVersion, "xray_mode": mode})
	if err != nil {
		return takeoverRuntime{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.CustomAPIURL+takeoverEndpoint, bytes.NewReader(body))
	if err != nil {
		return takeoverRuntime{}, err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return takeoverRuntime{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return takeoverRuntime{}, fmt.Errorf("controller takeover HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded takeoverResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&decoded); err != nil || !decoded.Success {
		return takeoverRuntime{}, errors.New("controller takeover response invalid")
	}
	return decoded.Runtime, nil
}

func performExternalTakeover(ctx context.Context, client *http.Client, cfg config, lifecycle *lifecycleManager, state *localState) error {
	return ensureExternalDesiredState(ctx, client, cfg, lifecycle, state)
}

// ensureExternalDesiredState is the only automatic embedded -> external repair
// path. It validates and arms ownership before changing either controller or
// Agent mode, performs one bounded handoff, and rolls back to embedded on any
// failed joint health check.
func ensureExternalDesiredState(ctx context.Context, client *http.Client, cfg config, lifecycle *lifecycleManager, state *localState) error {
	runtime, err := requestTakeoverRuntime(ctx, client, cfg, "")
	if err != nil {
		return err
	}
	localMode, err := readAgentXrayMode(officialAgentConfigPath)
	if err != nil {
		return err
	}
	if runtime.XrayMode == "external" && localMode == "external" && state.ExternalOwnership.Enabled {
		if err := lifecycle.reconcileExternalOwnership(ctx, &state.ExternalOwnership); err != nil {
			return err
		}
		status := lifecycle.externalOwnershipStatus(ctx, &state.ExternalOwnership)
		if status.RuntimeOwned && status.SingleCore && status.CoreReady && serviceActive(ctx, "mmw-agent.service") {
			return nil
		}
	}
	if runtime.Status != "connected" || runtime.LastHeartbeat == nil || time.Since(*runtime.LastHeartbeat) > 90*time.Second {
		return errors.New("official Agent is not freshly connected")
	}
	if !serviceActive(ctx, "mmw-agent.service") {
		return errors.New("mmw-agent.service is not active")
	}
	if state.ExternalOwnership.Enabled {
		if state.ExternalOwnership.BackupDir == "" {
			return errors.New("external ownership has no rollback snapshot")
		}
		if err := validateOwnedCoreAndConfig(ctx); err != nil {
			return fmt.Errorf("validate existing ownership: %w", err)
		}
		if err := lifecycle.reconcileExternalOwnership(ctx, &state.ExternalOwnership); err != nil {
			return fmt.Errorf("reconcile existing ownership: %w", err)
		}
		if _, _, err := ensureOwnershipFiles(); err != nil {
			return fmt.Errorf("restore ownership files: %w", err)
		}
		if err := systemctl(ctx, "daemon-reload"); err != nil {
			return err
		}
		if err := armExternalOwnershipServices(ctx, systemctl, serviceActive); err != nil {
			return fmt.Errorf("re-arm ownership: %w", err)
		}
		state.ExternalOwnership.Armed = true
		state.ExternalOwnership.LastRepairAt = time.Now().UTC()
		state.ExternalOwnership.LastRepairReason = "desired external drift repair armed"
	} else {
		if err := lifecycle.prepareExternalOwnership(ctx, &state.ExternalOwnership); err != nil {
			if state.ExternalOwnership.BackupDir != "" {
				return rollbackTakeover(ctx, client, cfg, lifecycle, state, fmt.Errorf("prepare ownership: %w", err))
			}
			return err
		}
		if err := lifecycle.armExternalOwnership(ctx, &state.ExternalOwnership); err != nil {
			return rollbackTakeover(ctx, client, cfg, lifecycle, state, fmt.Errorf("arm ownership: %w", err))
		}
	}
	if _, err := requestTakeoverRuntime(ctx, client, cfg, "external"); err != nil {
		return rollbackTakeover(ctx, client, cfg, lifecycle, state, fmt.Errorf("set controller mode external: %w", err))
	}
	if err := writeAgentXrayMode(officialAgentConfigPath, "external"); err != nil {
		return rollbackTakeover(ctx, client, cfg, lifecycle, state, fmt.Errorf("set Agent mode external: %w", err))
	}
	if err := systemctl(ctx, "restart", "mmw-agent.service"); err != nil {
		return rollbackTakeover(ctx, client, cfg, lifecycle, state, fmt.Errorf("restart official Agent: %w", err))
	}
	// The official switch endpoint starts xray.service as part of the mode
	// transition. A local Agent config restart reconnects in external mode but
	// intentionally leaves an already-stopped service inactive, so bootstrap it
	// exactly once during the armed handoff. Subsequent lifecycle operations are
	// still performed by the official Agent; reconciliation never force-starts
	// a deliberately stopped healthy service.
	if err := bootstrapExternalOwnershipService(ctx, systemctl); err != nil {
		return rollbackTakeover(ctx, client, cfg, lifecycle, state, fmt.Errorf("bootstrap external xray.service: %w", err))
	}
	if err := waitExternalTakeoverHealthy(ctx, client, cfg, lifecycle, state); err != nil {
		return rollbackTakeover(ctx, client, cfg, lifecycle, state, err)
	}
	state.ExternalOwnership.Armed = false
	state.ExternalOwnership.LastRepairAt = time.Now().UTC()
	state.ExternalOwnership.LastRepairReason = "transactional official Agent external handoff completed"
	return nil
}

func ensureEmbeddedDesiredState(ctx context.Context, client *http.Client, cfg config, lifecycle *lifecycleManager, state *localState) error {
	runtime, err := requestTakeoverRuntime(ctx, client, cfg, "")
	if err != nil {
		return err
	}
	localMode, err := readAgentXrayMode(officialAgentConfigPath)
	if err != nil {
		return err
	}
	if runtime.XrayMode == "embedded" && localMode == "embedded" && !state.ExternalOwnership.Enabled {
		return waitEmbeddedTakeoverHealthy(ctx, client, cfg)
	}
	if state.ExternalOwnership.Enabled || state.ExternalOwnership.BackupDir != "" {
		if err := restoreEmbeddedTakeover(ctx, client, cfg, lifecycle, state); err != nil {
			return err
		}
		state.Takeover = takeoverState{Mode: "desired-state", Status: "completed", Message: "official embedded desired state is healthy", CompletedAt: time.Now().UTC()}
		return nil
	}
	previousMode := localMode
	if _, err := requestTakeoverRuntime(ctx, client, cfg, "embedded"); err != nil {
		return err
	}
	rollback := func(cause error) error {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_, _ = requestTakeoverRuntime(rollbackCtx, client, cfg, previousMode)
		_ = writeAgentXrayMode(officialAgentConfigPath, previousMode)
		_ = systemctl(rollbackCtx, "restart", "mmw-agent.service")
		if previousMode == "external" {
			_ = bootstrapExternalOwnershipService(rollbackCtx, systemctl)
		}
		return fmt.Errorf("%v; previous %s mode restoration attempted", cause, previousMode)
	}
	if err := writeAgentXrayMode(officialAgentConfigPath, "embedded"); err != nil {
		return rollback(err)
	}
	_ = systemctl(ctx, "stop", "xray.service")
	if err := systemctl(ctx, "restart", "mmw-agent.service"); err != nil {
		return rollback(err)
	}
	if err := waitEmbeddedTakeoverHealthy(ctx, client, cfg); err != nil {
		return rollback(err)
	}
	state.Takeover = takeoverState{Mode: "desired-state", Status: "completed", Message: "official embedded desired state is healthy", CompletedAt: time.Now().UTC()}
	return nil
}

func bootstrapExternalOwnershipService(ctx context.Context, run systemctlRunner) error {
	if err := run(ctx, "unmask", "xray.service"); err != nil {
		return err
	}
	if err := run(ctx, "enable", "xray.service"); err != nil {
		return err
	}
	return run(ctx, "start", "xray.service")
}

func rollbackTakeover(_ context.Context, client *http.Client, cfg config, lifecycle *lifecycleManager, state *localState, cause error) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := restoreEmbeddedTakeover(rollbackCtx, client, cfg, lifecycle, state); err != nil {
		return fmt.Errorf("%v; rollback incomplete: %w", cause, err)
	}
	return fmt.Errorf("%v; embedded state restored", cause)
}

func restoreEmbeddedTakeover(ctx context.Context, client *http.Client, cfg config, lifecycle *lifecycleManager, state *localState) error {
	var failures []string
	if _, err := requestTakeoverRuntime(ctx, client, cfg, "embedded"); err != nil {
		failures = append(failures, "controller mode rollback: "+err.Error())
	}
	if state.ExternalOwnership.BackupDir != "" {
		if err := lifecycle.rollbackExternalOwnership(ctx, &state.ExternalOwnership); err != nil {
			failures = append(failures, "ownership rollback: "+err.Error())
		}
	}
	if err := systemctl(ctx, "restart", "mmw-agent.service"); err != nil {
		failures = append(failures, "Agent restart rollback: "+err.Error())
	}
	if err := waitEmbeddedTakeoverHealthy(ctx, client, cfg); err != nil {
		failures = append(failures, "embedded health rollback: "+err.Error())
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func waitExternalTakeoverHealthy(ctx context.Context, client *http.Client, cfg config, lifecycle *lifecycleManager, state *localState) error {
	deadline := time.Now().Add(75 * time.Second)
	var lastRuntime takeoverRuntime
	var lastRuntimeErr error
	var lastStatus externalOwnershipStatus
	for time.Now().Before(deadline) {
		runtime, runtimeErr := requestTakeoverRuntime(ctx, client, cfg, "")
		status := lifecycle.externalOwnershipStatus(ctx, &state.ExternalOwnership)
		lastRuntime, lastRuntimeErr, lastStatus = runtime, runtimeErr, status
		if runtimeErr == nil && runtime.XrayMode == "external" && runtime.Status == "connected" && runtime.LastHeartbeat != nil && time.Since(*runtime.LastHeartbeat) < 45*time.Second && serviceActive(ctx, "mmw-agent.service") && status.ServiceActive && status.RuntimeOwned && status.SingleCore && status.OfficialConfig && status.CoreReady {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("external health timeout: controller_mode=%s controller_status=%s controller_error=%v agent_active=%t service_active=%t service_owned=%t runtime_owned=%t single_core=%t official_config=%t core_ready=%t main_pid=%d service_state=%s", lastRuntime.XrayMode, lastRuntime.Status, lastRuntimeErr, serviceActive(ctx, "mmw-agent.service"), lastStatus.ServiceActive, lastStatus.ServiceOwned, lastStatus.RuntimeOwned, lastStatus.SingleCore, lastStatus.OfficialConfig, lastStatus.CoreReady, lastStatus.MainPID, serviceStateSummary(ctx, "xray.service"))
}

func serviceStateSummary(ctx context.Context, service string) string {
	command := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveState,SubState,Result,ExecMainStatus", "--value", service)
	output, err := command.CombinedOutput()
	if err != nil {
		return "unavailable"
	}
	return strings.Join(strings.Fields(string(output)), "/")
}

func waitEmbeddedTakeoverHealthy(ctx context.Context, client *http.Client, cfg config) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		runtime, err := requestTakeoverRuntime(ctx, client, cfg, "")
		ports, _ := officialInboundPorts()
		owners, _ := listeningPIDsForPorts(ports)
		allListening := len(ports) > 0
		for _, port := range ports {
			if len(owners[port]) == 0 {
				allListening = false
			}
		}
		if err == nil && runtime.XrayMode == "embedded" && runtime.Status == "connected" && serviceActive(ctx, "mmw-agent.service") && !serviceActive(ctx, "xray.service") && !serviceActive(ctx, coreServiceName) && allListening {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("embedded rollback health check timed out")
}

func readAgentXrayMode(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read official Agent config: %w", err)
	}
	mode, _, err := replaceAgentXrayMode(data, "")
	return mode, err
}

func writeAgentXrayMode(path, mode string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, updated, err := replaceAgentXrayMode(data, mode)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temporary := path + ".mmwxc-new"
	if err := os.WriteFile(temporary, updated, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func replaceAgentXrayMode(data []byte, replacement string) (string, []byte, error) {
	lines := strings.Split(string(data), "\n")
	found := -1
	current := ""
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "xray_mode:") {
			if found >= 0 {
				return "", nil, errors.New("official Agent config has duplicate xray_mode")
			}
			found = index
			current = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "xray_mode:")), `"'`)
		}
	}
	if found < 0 || (current != "embedded" && current != "external") {
		return "", nil, errors.New("official Agent config has invalid xray_mode")
	}
	if replacement == "" {
		return current, data, nil
	}
	if replacement != "embedded" && replacement != "external" {
		return "", nil, errors.New("invalid replacement xray_mode")
	}
	prefix := lines[found][:len(lines[found])-len(strings.TrimLeft(lines[found], " \t"))]
	lines[found] = prefix + "xray_mode: " + replacement
	return current, []byte(strings.Join(lines, "\n")), nil
}
