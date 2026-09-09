package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	officialConfigPath    = "/usr/local/etc/xray/config.json"
	ownershipDropInDir    = "/etc/systemd/system/xray.service.d"
	ownershipDropInPath   = "/etc/systemd/system/xray.service.d/90-mmwxc-owner.conf"
	ownershipImagePath    = "/var/lib/mmwxc/ownership/xray"
	ownershipConfigBackup = "/var/lib/mmwxc/ownership/config.last-good.json"
	ownershipBackupRoot   = "/var/lib/mmwxc/rollback/external-ownership"
	invalidConfigGrace    = 30 * time.Second
)

const ownershipBaseUnit = `[Unit]
Description=Xray Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/opt/mmwxc/core/xray run -config /usr/local/etc/xray/config.json
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

const ownershipDropIn = `[Service]
Type=simple
User=root
Group=root
Environment=MMWXC_CORE_CONTROL_SOCKET=/run/mmwxc/core-control.sock
Environment=XRAY_LOCATION_ASSET=/opt/mmwxc/core
ExecStartPre=
ExecStart=
ExecStart=/opt/mmwxc/core/xray run -config /usr/local/etc/xray/config.json
RuntimeDirectory=mmwxc
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576
`

type externalOwnershipState struct {
	Prepared          bool      `json:"prepared,omitempty"`
	Enabled           bool      `json:"enabled,omitempty"`
	BackupDir         string    `json:"backup_dir,omitempty"`
	ExpectedCoreSHA   string    `json:"expected_core_sha256,omitempty"`
	LastGoodConfigSHA string    `json:"last_good_config_sha256,omitempty"`
	RejectedConfigSHA string    `json:"rejected_config_sha256,omitempty"`
	InvalidConfigAt   time.Time `json:"invalid_config_at,omitempty"`
	LastRepairAt      time.Time `json:"last_repair_at,omitempty"`
	LastRepairReason  string    `json:"last_repair_reason,omitempty"`
}

type externalOwnershipStatus struct {
	Prepared         bool      `json:"prepared"`
	Enabled          bool      `json:"enabled"`
	ServiceOwned     bool      `json:"service_owned"`
	RuntimeOwned     bool      `json:"runtime_owned"`
	SingleCore       bool      `json:"single_core"`
	OfficialConfig   bool      `json:"official_config"`
	ServiceActive    bool      `json:"service_active"`
	CoreReady        bool      `json:"core_ready"`
	MainPID          int       `json:"main_pid,omitempty"`
	RuntimeBinary    string    `json:"runtime_binary,omitempty"`
	LastRepairAt     time.Time `json:"last_repair_at,omitempty"`
	LastRepairReason string    `json:"last_repair_reason,omitempty"`
	Error            string    `json:"error,omitempty"`
}

type ownershipPathBackup struct {
	Exists        bool        `json:"exists"`
	Symlink       bool        `json:"symlink"`
	SymlinkTarget string      `json:"symlink_target,omitempty"`
	Mode          os.FileMode `json:"mode,omitempty"`
	BackupName    string      `json:"backup_name,omitempty"`
}

type ownershipBackupManifest struct {
	CreatedAt       time.Time           `json:"created_at"`
	OfficialUnit    ownershipPathBackup `json:"official_unit"`
	OwnershipDropIn ownershipPathBackup `json:"ownership_drop_in"`
	CustomActive    bool                `json:"custom_active"`
	CustomEnabled   bool                `json:"custom_enabled"`
	OfficialActive  bool                `json:"official_active"`
	OfficialEnabled bool                `json:"official_enabled"`
}

func (manager *lifecycleManager) prepareExternalOwnership(ctx context.Context, state *externalOwnershipState) error {
	if state.Enabled {
		return errors.New("external ownership is already active")
	}
	if err := validateOwnedCoreAndConfig(ctx); err != nil {
		return err
	}
	if !state.Prepared || state.BackupDir == "" {
		backupDir := filepath.Join(ownershipBackupRoot, time.Now().UTC().Format("20060102T150405.000000000Z"))
		if err := createOwnershipBackup(ctx, backupDir); err != nil {
			return err
		}
		state.BackupDir = backupDir
	}
	if err := os.MkdirAll(filepath.Dir(ownershipImagePath), 0700); err != nil {
		return err
	}
	if err := copyFile(coreBinaryPath, ownershipImagePath+".new", 0755); err != nil {
		return err
	}
	if err := os.Rename(ownershipImagePath+".new", ownershipImagePath); err != nil {
		return err
	}
	coreHash, err := sha256File(coreBinaryPath)
	if err != nil {
		return err
	}
	configHash, err := preserveValidOfficialConfig(ctx)
	if err != nil {
		return err
	}
	state.Prepared = true
	state.ExpectedCoreSHA = coreHash
	state.LastGoodConfigSHA = configHash
	state.InvalidConfigAt = time.Time{}
	return nil
}

func (manager *lifecycleManager) activateExternalOwnership(ctx context.Context, state *externalOwnershipState) error {
	if !state.Prepared || state.BackupDir == "" {
		return errors.New("external ownership is not prepared")
	}
	if err := validateOwnedCoreAndConfig(ctx); err != nil {
		return err
	}
	if _, _, err := ensureOwnershipFiles(); err != nil {
		return err
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := systemctl(ctx, "disable", "--now", coreServiceName); err != nil && serviceActive(ctx, coreServiceName) {
		return err
	}
	activationErr := func() error {
		if err := systemctl(ctx, "unmask", "xray.service"); err != nil {
			return err
		}
		if err := systemctl(ctx, "enable", "xray.service"); err != nil {
			return err
		}
		if err := systemctl(ctx, "restart", "xray.service"); err != nil {
			return err
		}
		if err := manager.waitOwnedCoreReady(ctx, 25*time.Second); err != nil {
			return err
		}
		status := manager.externalOwnershipStatus(ctx, state)
		if !status.RuntimeOwned || !status.SingleCore {
			return fmt.Errorf("external ownership verification failed: runtime_owned=%t single_core=%t", status.RuntimeOwned, status.SingleCore)
		}
		return nil
	}()
	if activationErr != nil {
		if rollbackErr := restoreOwnershipBackup(ctx, state.BackupDir); rollbackErr != nil {
			return fmt.Errorf("external ownership activation failed (%v); local rollback failed: %w", activationErr, rollbackErr)
		}
		*state = externalOwnershipState{}
		return fmt.Errorf("external ownership activation failed; previous local service state restored: %w", activationErr)
	}
	state.Enabled = true
	state.LastRepairAt = time.Now().UTC()
	state.LastRepairReason = "activated"
	return nil
}

func (manager *lifecycleManager) rollbackExternalOwnership(ctx context.Context, state *externalOwnershipState) error {
	if state.BackupDir == "" {
		return errors.New("no external ownership rollback snapshot is available")
	}
	if err := restoreOwnershipBackup(ctx, state.BackupDir); err != nil {
		return err
	}
	*state = externalOwnershipState{}
	return nil
}

func (manager *lifecycleManager) reconcileExternalOwnership(ctx context.Context, state *externalOwnershipState) error {
	if !state.Enabled {
		return nil
	}
	reasons := make([]string, 0, 4)
	coreHash, err := sha256File(coreBinaryPath)
	if err != nil || coreHash != state.ExpectedCoreSHA {
		imageHash, imageErr := sha256File(ownershipImagePath)
		if imageErr != nil || imageHash != state.ExpectedCoreSHA {
			return errors.New("owned Core binary drifted and the protected image is unavailable")
		}
		if err := copyFile(ownershipImagePath, coreBinaryPath+".new", 0755); err != nil {
			return fmt.Errorf("restore owned Core binary: %w", err)
		}
		if err := os.Rename(coreBinaryPath+".new", coreBinaryPath); err != nil {
			return fmt.Errorf("replace owned Core binary: %w", err)
		}
		reasons = append(reasons, "core binary restored")
	}
	baseChanged, dropInChanged, err := ensureOwnershipFiles()
	if err != nil {
		return err
	}
	if baseChanged {
		reasons = append(reasons, "xray.service restored")
	}
	if dropInChanged {
		reasons = append(reasons, "service ownership restored")
	}
	if serviceActive(ctx, coreServiceName) {
		if err := systemctl(ctx, "disable", "--now", coreServiceName); err != nil {
			return fmt.Errorf("stop duplicate Custom Core service: %w", err)
		}
		reasons = append(reasons, "duplicate service stopped")
	}
	configHash, configErr := hashValidJSONFile(officialConfigPath)
	if configErr == nil && configHash != state.LastGoodConfigSHA {
		configHash, configErr = validateAndHashOfficialConfig(ctx)
	}
	if configErr == nil {
		state.InvalidConfigAt = time.Time{}
		if configHash != state.LastGoodConfigSHA {
			if err := copyFile(officialConfigPath, ownershipConfigBackup+".new", 0600); err != nil {
				return err
			}
			if err := os.Rename(ownershipConfigBackup+".new", ownershipConfigBackup); err != nil {
				return err
			}
			state.LastGoodConfigSHA = configHash
			state.RejectedConfigSHA = ""
		}
	} else if rejectedHash := sha256FileOrMissing(officialConfigPath); state.RejectedConfigSHA == rejectedHash {
		state.InvalidConfigAt = time.Time{}
	} else if state.InvalidConfigAt.IsZero() {
		state.InvalidConfigAt = time.Now().UTC()
	} else if time.Since(state.InvalidConfigAt) >= invalidConfigGrace {
		if _, err := os.Stat(ownershipConfigBackup); err != nil {
			return fmt.Errorf("official config is invalid and no last-good backup is available: %w", configErr)
		}
		if err := copyFile(ownershipConfigBackup, officialConfigPath+".new", 0600); err != nil {
			return err
		}
		if err := os.Rename(officialConfigPath+".new", officialConfigPath); err != nil {
			return err
		}
		state.InvalidConfigAt = time.Time{}
		state.RejectedConfigSHA = rejectedHash
		reasons = append(reasons, "last-good official config restored")
	}
	status := manager.externalOwnershipStatus(ctx, state)
	if status.ServiceActive && !status.RuntimeOwned {
		reasons = append(reasons, "wrong runtime binary replaced")
	}
	if len(reasons) == 0 {
		return nil
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	// Only restart after ownership drift. Ordinary Agent-driven configuration
	// writes and restarts never enter this branch.
	if status.ServiceActive || baseChanged || dropInChanged {
		if err := systemctl(ctx, "restart", "xray.service"); err != nil {
			return err
		}
		if err := manager.waitOwnedCoreReady(ctx, 25*time.Second); err != nil {
			return err
		}
	}
	state.LastRepairAt = time.Now().UTC()
	state.LastRepairReason = strings.Join(reasons, "; ")
	return nil
}

func (manager *lifecycleManager) externalOwnershipStatus(ctx context.Context, state *externalOwnershipState) externalOwnershipStatus {
	status := externalOwnershipStatus{
		Prepared: state.Prepared, Enabled: state.Enabled, LastRepairAt: state.LastRepairAt,
		LastRepairReason: state.LastRepairReason, OfficialConfig: false,
	}
	status.ServiceOwned = exactFileContents(ownershipDropInPath, ownershipDropIn)
	status.ServiceActive = serviceActive(ctx, "xray.service")
	status.MainPID, _ = serviceMainPID(ctx, "xray.service")
	if status.MainPID > 0 {
		status.RuntimeBinary, _ = os.Readlink(filepath.Join("/proc", strconv.Itoa(status.MainPID), "exe"))
		status.RuntimeOwned = sameFile(filepath.Join("/proc", strconv.Itoa(status.MainPID), "exe"), coreBinaryPath)
	}
	count, _ := countXrayProcesses()
	ports, _ := officialInboundPorts()
	portOwners, _ := listeningPIDsForPorts(ports)
	portsOwned := true
	for _, owners := range portOwners {
		for pid := range owners {
			if pid != status.MainPID {
				portsOwned = false
			}
		}
	}
	status.SingleCore = count == 1 && status.RuntimeOwned && portsOwned && !serviceActive(ctx, coreServiceName)
	if _, err := hashValidJSONFile(officialConfigPath); err == nil {
		status.OfficialConfig = true
	}
	if status.ServiceActive {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := manager.core.snapshot(probeCtx)
		cancel()
		status.CoreReady = err == nil
	}
	if state.Enabled && (!status.ServiceOwned || !status.RuntimeOwned || !status.SingleCore || !status.OfficialConfig || !status.CoreReady) {
		status.Error = "external ownership is not fully healthy"
	}
	return status
}

func validateOwnedCoreAndConfig(ctx context.Context) error {
	if err := validateELFArchitecture(coreBinaryPath); err != nil {
		return fmt.Errorf("Custom Core validation failed: %w", err)
	}
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	version, err := binaryVersion(versionCtx, coreBinaryPath, "version")
	cancel()
	if err != nil || !strings.Contains(strings.ToLower(version), "xray") {
		return errors.New("Custom Core version validation failed")
	}
	if _, err := validateAndHashOfficialConfig(ctx); err != nil {
		return err
	}
	data, err := os.ReadFile(officialConfigPath)
	if err != nil {
		return err
	}
	return ensureCoreAssets(data)
}

func validateAndHashOfficialConfig(ctx context.Context) (string, error) {
	data, err := os.ReadFile(officialConfigPath)
	if err != nil {
		return "", fmt.Errorf("read official Xray config: %w", err)
	}
	if len(data) == 0 || len(data) > 8<<20 || !json.Valid(data) {
		return "", errors.New("official Xray config is not valid JSON")
	}
	testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(testCtx, coreBinaryPath, "run", "-test", "-config", officialConfigPath)
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("official Xray config validation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func preserveValidOfficialConfig(ctx context.Context) (string, error) {
	hash, err := validateAndHashOfficialConfig(ctx)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(ownershipConfigBackup), 0700); err != nil {
		return "", err
	}
	if err := copyFile(officialConfigPath, ownershipConfigBackup+".new", 0600); err != nil {
		return "", err
	}
	if err := os.Rename(ownershipConfigBackup+".new", ownershipConfigBackup); err != nil {
		return "", err
	}
	return hash, nil
}

func ensureOwnershipFiles() (bool, bool, error) {
	return ensureOwnershipFilesAt(officialXrayPath, ownershipDropInPath)
}

func ensureOwnershipFilesAt(basePath, dropInPath string) (bool, bool, error) {
	baseChanged := false
	if info, err := os.Lstat(basePath); errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode()&os.ModeSymlink != 0) {
		if err == nil {
			if removeErr := os.Remove(basePath); removeErr != nil {
				return false, false, removeErr
			}
		}
		if err := writeExactFile(basePath, ownershipBaseUnit, 0644); err != nil {
			return false, false, err
		}
		baseChanged = true
	} else if err != nil {
		return false, false, err
	}
	if err := os.MkdirAll(filepath.Dir(dropInPath), 0755); err != nil {
		return false, false, err
	}
	dropInChanged := !exactFileContents(dropInPath, ownershipDropIn)
	if dropInChanged {
		if err := writeExactFile(dropInPath, ownershipDropIn, 0644); err != nil {
			return false, false, err
		}
	}
	return baseChanged, dropInChanged, nil
}

func writeExactFile(path, contents string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, []byte(contents), mode); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func exactFileContents(path, contents string) bool {
	data, err := os.ReadFile(path)
	return err == nil && string(data) == contents
}

func createOwnershipBackup(ctx context.Context, directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	manifest := ownershipBackupManifest{CreatedAt: time.Now().UTC()}
	var err error
	if manifest.OfficialUnit, err = backupOwnershipPath(officialXrayPath, directory, "xray.service"); err != nil {
		return err
	}
	if manifest.OwnershipDropIn, err = backupOwnershipPath(ownershipDropInPath, directory, "owner.dropin"); err != nil {
		return err
	}
	manifest.CustomActive = serviceActive(ctx, coreServiceName)
	manifest.CustomEnabled = serviceEnabled(ctx, coreServiceName)
	manifest.OfficialActive = serviceActive(ctx, "xray.service")
	manifest.OfficialEnabled = serviceEnabled(ctx, "xray.service")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0600)
}

func backupOwnershipPath(path, directory, name string) (ownershipPathBackup, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ownershipPathBackup{}, nil
	}
	if err != nil {
		return ownershipPathBackup{}, err
	}
	backup := ownershipPathBackup{Exists: true, Mode: info.Mode().Perm()}
	if info.Mode()&os.ModeSymlink != 0 {
		backup.Symlink = true
		backup.SymlinkTarget, err = os.Readlink(path)
		return backup, err
	}
	if !info.Mode().IsRegular() {
		return ownershipPathBackup{}, fmt.Errorf("refusing to back up non-regular path %s", path)
	}
	backup.BackupName = name
	return backup, copyFile(path, filepath.Join(directory, name), info.Mode().Perm())
}

func restoreOwnershipBackup(ctx context.Context, directory string) error {
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest ownershipBackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	_ = systemctl(ctx, "stop", "xray.service")
	if err := restoreOwnershipPath(officialXrayPath, directory, manifest.OfficialUnit); err != nil {
		return err
	}
	if err := restoreOwnershipPath(ownershipDropInPath, directory, manifest.OwnershipDropIn); err != nil {
		return err
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if manifest.OfficialEnabled {
		_ = systemctl(ctx, "enable", "xray.service")
	} else {
		_ = systemctl(ctx, "disable", "xray.service")
	}
	if manifest.OfficialActive {
		_ = systemctl(ctx, "start", "xray.service")
	}
	if manifest.CustomEnabled {
		_ = systemctl(ctx, "enable", coreServiceName)
	} else {
		_ = systemctl(ctx, "disable", coreServiceName)
	}
	if manifest.CustomActive {
		if err := systemctl(ctx, "start", coreServiceName); err != nil {
			return err
		}
	}
	return nil
}

func restoreOwnershipPath(path, directory string, backup ownershipPathBackup) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !backup.Exists {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if backup.Symlink {
		return os.Symlink(backup.SymlinkTarget, path)
	}
	return copyFile(filepath.Join(directory, backup.BackupName), path, backup.Mode)
}

func (manager *lifecycleManager) waitOwnedCoreReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pid, _ := serviceMainPID(ctx, "xray.service")
		if pid > 0 && sameFile(filepath.Join("/proc", strconv.Itoa(pid), "exe"), coreBinaryPath) {
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := manager.core.snapshot(probeCtx)
			cancel()
			if err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("owned Xray Core health check timed out")
}

func serviceMainPID(ctx context.Context, service string) (int, error) {
	command := exec.CommandContext(ctx, "systemctl", "show", "--property", "MainPID", "--value", service)
	output, err := command.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("read %s MainPID: %w: %s", service, err, strings.TrimSpace(string(output)))
	}
	return strconv.Atoi(strings.TrimSpace(string(output)))
}

func serviceEnabled(ctx context.Context, service string) bool {
	return systemctl(ctx, "is-enabled", "--quiet", service) == nil
}

func sameFile(first, second string) bool {
	a, err := os.Stat(first)
	if err != nil {
		return false
	}
	b, err := os.Stat(second)
	return err == nil && os.SameFile(a, b)
}

func countXrayProcesses() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		executable, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil {
			continue
		}
		base := strings.ToLower(filepath.Base(strings.TrimSuffix(executable, " (deleted)")))
		if strings.Contains(base, "xray") && !strings.Contains(base, "agent") && !strings.Contains(base, "helper") {
			count++
		}
	}
	return count, nil
}

func officialInboundPorts() ([]int, error) {
	data, err := os.ReadFile(officialConfigPath)
	if err != nil {
		return nil, err
	}
	return inboundPortsFromConfig(data)
}

func inboundPortsFromConfig(data []byte) ([]int, error) {
	var config struct {
		Inbounds []struct {
			Port json.RawMessage `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	seen := map[int]struct{}{}
	ports := make([]int, 0, len(config.Inbounds))
	for _, inbound := range config.Inbounds {
		raw := strings.Trim(strings.TrimSpace(string(inbound.Port)), `"`)
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	return ports, nil
}

func listeningPIDsForPorts(ports []int) (map[int]map[int]struct{}, error) {
	result := make(map[int]map[int]struct{}, len(ports))
	wanted := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		wanted[port] = struct{}{}
		result[port] = map[int]struct{}{}
	}
	inodePorts := map[string]int{}
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			_, rawPort, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			parsed, err := strconv.ParseInt(rawPort, 16, 32)
			if err != nil {
				continue
			}
			port := int(parsed)
			if _, ok := wanted[port]; ok {
				inodePorts[fields[9]] = port
			}
		}
	}
	if len(inodePorts) == 0 {
		return result, nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		fdRoot := filepath.Join("/proc", entry.Name(), "fd")
		_ = filepath.WalkDir(fdRoot, func(path string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil || item.IsDir() {
				return nil
			}
			target, err := os.Readlink(path)
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				return nil
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if port, ok := inodePorts[inode]; ok {
				result[port][pid] = struct{}{}
			}
			return nil
		})
	}
	return result, nil
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func sha256FileOrMissing(path string) string {
	hash, err := sha256File(path)
	if err != nil {
		return "missing"
	}
	return hash
}

func hashValidJSONFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > 8<<20 || !json.Valid(data) {
		return "", errors.New("invalid JSON file")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (manager *lifecycleManager) externalOwnedCoreStatus(ctx context.Context) componentStatus {
	status := componentStatus{BinaryPath: coreBinaryPath, ConfigPath: officialConfigPath, Service: "xray.service"}
	info, err := os.Stat(coreBinaryPath)
	if err != nil {
		status.Error = "not installed"
		return status
	}
	status.Installed = true
	status.Prepared = exactFileContents(ownershipDropInPath, ownershipDropIn)
	manager.versionMu.Lock()
	version := manager.coreVersion
	if version == "" || !manager.coreMTime.Equal(info.ModTime()) {
		versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		version, err = binaryVersion(versionCtx, coreBinaryPath, "version")
		cancel()
		if err == nil {
			manager.coreVersion = version
			manager.coreMTime = info.ModTime()
		}
	}
	manager.versionMu.Unlock()
	if err == nil {
		status.Version = version
	} else {
		status.Error = err.Error()
	}
	status.Active = serviceActive(ctx, "xray.service")
	if status.Active {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, probeErr := manager.core.snapshot(probeCtx)
		cancel()
		status.Ready = probeErr == nil
		if probeErr != nil {
			status.Error = probeErr.Error()
		}
	}
	return status
}
