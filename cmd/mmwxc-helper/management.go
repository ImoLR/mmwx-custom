package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/user"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	managementClockSkew = 30 * time.Second
	completedCommandMax = 64
)

type managementArtifact struct {
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Version  string `json:"version,omitempty"`
	Activate bool   `json:"activate,omitempty"`
}

type managementCommand struct {
	ID        string          `json:"id"`
	Action    string          `json:"action"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	Signature string          `json:"signature"`
}

type managementResult struct {
	CommandID   string          `json:"command_id"`
	Action      string          `json:"action"`
	Success     bool            `json:"success"`
	Message     string          `json:"message,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	CompletedAt time.Time       `json:"completed_at"`
	Signature   string          `json:"signature"`
}

type componentStatus struct {
	Installed  bool   `json:"installed"`
	Prepared   bool   `json:"prepared"`
	Version    string `json:"version,omitempty"`
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Service    string `json:"service,omitempty"`
	Active     bool   `json:"active"`
	Ready      bool   `json:"ready"`
	Error      string `json:"error,omitempty"`
}

type agentStatus struct {
	Helper        componentStatus         `json:"helper"`
	Core          componentStatus         `json:"core"`
	RunUser       string                  `json:"run_user,omitempty"`
	Architecture  string                  `json:"architecture,omitempty"`
	Capabilities  []string                `json:"capabilities"`
	LastOperation *managementResult       `json:"last_operation,omitempty"`
	ReportedAt    time.Time               `json:"reported_at"`
	Ownership     externalOwnershipStatus `json:"external_ownership"`
}

type managementReport struct {
	Status agentStatus       `json:"status"`
	Result *managementResult `json:"result,omitempty"`
}

var helperCapabilities = []string{
	"helper.status", "helper.version", "helper.update",
	"core.status", "core.version", "core.install", "core.update", "core.restart", "core.stop", "core.rollback", "core.config.apply",
	"official.xray.stop", "official.xray.start",
	"official.xray.attach-custom", "official.xray.detach-custom",
	"external.ownership.status", "external.ownership.prepare", "external.ownership.activate", "external.ownership.rollback",
	"connection.status", "connection.settings",
}

func helperTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func commandSigningBytes(command managementCommand) []byte {
	payloadHash := sha256.Sum256(command.Payload)
	return []byte(fmt.Sprintf("%s\n%s\n%s\n%s\n%x", command.ID, command.Action, command.CreatedAt.UTC().Format(time.RFC3339Nano), command.ExpiresAt.UTC().Format(time.RFC3339Nano), payloadHash))
}

func resultSigningBytes(result managementResult) []byte {
	dataHash := sha256.Sum256(result.Data)
	return []byte(fmt.Sprintf("%s\n%s\n%t\n%s\n%s\n%x", result.CommandID, result.Action, result.Success, result.Message, result.CompletedAt.UTC().Format(time.RFC3339Nano), dataHash))
}

func managementMAC(tokenHash string, message []byte) (string, error) {
	key, err := hex.DecodeString(tokenHash)
	if err != nil || len(key) != sha256.Size {
		return "", errors.New("invalid management key")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func verifyManagementCommand(command managementCommand, token string, now time.Time) error {
	if command.ID == "" || command.Action == "" {
		return errors.New("invalid management command")
	}
	if now.Add(managementClockSkew).Before(command.CreatedAt) || now.Add(-managementClockSkew).After(command.ExpiresAt) {
		return errors.New("management command expired")
	}
	expected, err := managementMAC(helperTokenHash(token), commandSigningBytes(command))
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(command.Signature)) != 1 {
		return errors.New("invalid management command signature")
	}
	return nil
}

func signManagementResult(result *managementResult, token string) error {
	signature, err := managementMAC(helperTokenHash(token), resultSigningBytes(*result))
	if err != nil {
		return err
	}
	result.Signature = signature
	return nil
}

type commandExecutor struct {
	lifecycle *lifecycleManager
	core      *coreClient
	state     *localState
}

func (executor *commandExecutor) status(ctx context.Context) agentStatus {
	username := ""
	if current, err := user.Current(); err == nil {
		username = current.Username
	}
	coreStatus := executor.lifecycle.coreStatus(ctx)
	if executor.state.ExternalOwnership.Enabled {
		coreStatus = executor.lifecycle.externalOwnedCoreStatus(ctx)
	}
	status := agentStatus{
		Helper: componentStatus{Installed: true, Prepared: true, Version: helperVersion, BinaryPath: helperBinaryPath, ConfigPath: defaultConfigPath, Service: helperServiceName, Active: serviceActive(ctx, helperServiceName), Ready: true},
		Core:   coreStatus, RunUser: username, Architecture: runtime.GOARCH,
		Capabilities: append([]string(nil), helperCapabilities...), LastOperation: executor.state.LastOperation, ReportedAt: time.Now().UTC(),
		Ownership: executor.lifecycle.externalOwnershipStatus(ctx, &executor.state.ExternalOwnership),
	}
	sort.Strings(status.Capabilities)
	return status
}

func (executor *commandExecutor) execute(ctx context.Context, command managementCommand, token string) managementResult {
	result := managementResult{CommandID: command.ID, Action: command.Action, CompletedAt: time.Now().UTC()}
	if err := verifyManagementCommand(command, token, result.CompletedAt); err != nil {
		result.Message = err.Error()
		finalizeManagementResult(&result, token)
		return result
	}
	var data any
	var err error
	switch command.Action {
	case "helper.status", "helper.version":
		data = executor.status(ctx)
	case "helper.update":
		var artifact managementArtifact
		err = json.Unmarshal(command.Payload, &artifact)
		if err == nil {
			err = executor.lifecycle.scheduleHelperUpdate(ctx, artifact)
		}
	case "core.status", "core.version":
		if executor.state.ExternalOwnership.Enabled {
			data = executor.lifecycle.externalOwnedCoreStatus(ctx)
		} else {
			data = executor.lifecycle.coreStatus(ctx)
		}
	case "core.install", "core.update":
		var artifact managementArtifact
		err = json.Unmarshal(command.Payload, &artifact)
		if err == nil {
			err = executor.lifecycle.installOrUpdateCore(ctx, artifact)
		}
	case "core.restart":
		if executor.state.ExternalOwnership.Enabled {
			err = systemctl(ctx, "restart", "xray.service")
			if err == nil {
				err = executor.lifecycle.waitOwnedCoreReady(ctx, 20*time.Second)
			}
		} else {
			err = executor.lifecycle.restartCore(ctx)
		}
	case "core.stop":
		if executor.state.ExternalOwnership.Enabled {
			err = systemctl(ctx, "stop", "xray.service")
		} else {
			err = executor.lifecycle.stopCore(ctx)
		}
	case "official.xray.stop":
		err = executor.lifecycle.stopOfficialXray(ctx)
	case "official.xray.start":
		err = executor.lifecycle.startOfficialXray(ctx)
	case "official.xray.attach-custom":
		err = executor.lifecycle.attachCustomCoreAsOfficialXray(ctx)
	case "official.xray.detach-custom":
		err = executor.lifecycle.detachCustomCoreAsOfficialXray(ctx)
	case "external.ownership.status":
		data = executor.lifecycle.externalOwnershipStatus(ctx, &executor.state.ExternalOwnership)
	case "external.ownership.prepare":
		err = executor.lifecycle.prepareExternalOwnership(ctx, &executor.state.ExternalOwnership)
	case "external.ownership.activate":
		err = executor.lifecycle.activateExternalOwnership(ctx, &executor.state.ExternalOwnership)
	case "external.ownership.rollback":
		err = executor.lifecycle.rollbackExternalOwnership(ctx, &executor.state.ExternalOwnership)
	case "core.rollback":
		err = executor.lifecycle.rollbackCore(ctx)
	case "core.config.apply":
		var payload struct {
			Config   json.RawMessage `json:"config"`
			Activate bool            `json:"activate,omitempty"`
		}
		err = json.Unmarshal(command.Payload, &payload)
		if err == nil {
			err = executor.lifecycle.applyCoreConfig(ctx, payload.Config, payload.Activate)
		}
	case "connection.status":
		var snapshot coreSnapshotResponse
		snapshot, err = executor.core.snapshot(ctx)
		if err == nil {
			data = struct {
				Core     coreSnapshotResponse `json:"core"`
				Settings connectionSettings   `json:"settings"`
			}{Core: snapshot, Settings: executor.state.Settings}
		}
	case "connection.settings":
		err = executor.core.apply(ctx, executor.state.Settings)
	default:
		err = errors.New("unsupported management action")
	}
	result.Success = err == nil
	if err != nil {
		result.Message = err.Error()
	} else if data != nil {
		result.Data, err = json.Marshal(data)
		if err != nil {
			result.Success = false
			result.Message = err.Error()
		}
	}
	finalizeManagementResult(&result, token)
	return result
}

func finalizeManagementResult(result *managementResult, token string) {
	result.Message = sanitizeManagementMessage(result.Message)
	result.CompletedAt = time.Now().UTC()
	_ = signManagementResult(result, token)
}

func hasCompletedCommand(state localState, id string) bool {
	for _, completed := range state.CompletedCommandIDs {
		if completed == id {
			return true
		}
	}
	return false
}

func rememberCompletedCommand(state *localState, id string) {
	if id == "" || hasCompletedCommand(*state, id) {
		return
	}
	state.CompletedCommandIDs = append(state.CompletedCommandIDs, id)
	if len(state.CompletedCommandIDs) > completedCommandMax {
		state.CompletedCommandIDs = state.CompletedCommandIDs[len(state.CompletedCommandIDs)-completedCommandMax:]
	}
}

func completedCommand(ids []string, id string) bool {
	for _, completed := range ids {
		if completed == id {
			return true
		}
	}
	return false
}

func sanitizeManagementMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
