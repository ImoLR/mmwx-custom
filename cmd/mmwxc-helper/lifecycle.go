package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	helperBinaryPath  = "/usr/local/bin/mmwxc-helper"
	helperServiceName = "mmwxc-helper.service"
	coreBinaryPath    = "/opt/mmwxc/core/xray"
	coreConfigPath    = "/etc/mmwxc/core/config.json"
	coreServicePath   = "/etc/systemd/system/mmwxc-core.service"
	coreServiceName   = "mmwxc-core.service"
	rollbackRoot      = "/var/lib/mmwxc/rollback"
	stagingRoot       = "/var/lib/mmwxc/staging"
	maxRollbackFiles  = 2
	maxArtifactBytes  = 160 << 20
)

const coreServiceUnit = `[Unit]
Description=MMWXC Custom Xray Core
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=MMWXC_CORE_CONTROL_SOCKET=/run/mmwxc/core-control.sock
ExecStart=/opt/mmwxc/core/xray run -config /etc/mmwxc/core/config.json
RuntimeDirectory=mmwxc
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

type lifecycleManager struct {
	httpClient  *http.Client
	core        *coreClient
	versionMu   sync.Mutex
	coreMTime   time.Time
	coreVersion string
}

func newLifecycleManager(core *coreClient) *lifecycleManager {
	return &lifecycleManager{httpClient: &http.Client{
		Timeout: 3 * time.Minute,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if request.URL.Scheme != "https" || !allowedArtifactRedirectHost(request.URL.Hostname()) {
				return errors.New("artifact redirect is not allowed")
			}
			return nil
		},
	}, core: core}
}

func validateArtifact(artifact managementArtifact) error {
	parsed, err := url.Parse(strings.TrimSpace(artifact.URL))
	if err != nil || !allowedArtifactSource(parsed) {
		return errors.New("artifact URL is not allowed")
	}
	if len(artifact.SHA256) != 64 {
		return errors.New("artifact sha256 is required")
	}
	if _, err := hex.DecodeString(artifact.SHA256); err != nil {
		return errors.New("artifact sha256 is invalid")
	}
	return nil
}

func allowedArtifactSource(parsed *url.URL) bool {
	return parsed.Scheme == "https" && strings.EqualFold(parsed.Hostname(), "github.com") && strings.HasPrefix(parsed.EscapedPath(), "/ImoLR/mmwx-custom/releases/")
}

func allowedArtifactRedirectHost(host string) bool {
	host = strings.ToLower(host)
	return host == "github.com" || host == "objects.githubusercontent.com" || host == "release-assets.githubusercontent.com"
}

func (manager *lifecycleManager) downloadArtifact(ctx context.Context, artifact managementArtifact, name string) (string, error) {
	if err := validateArtifact(artifact); err != nil {
		return "", err
	}
	if err := os.MkdirAll(stagingRoot, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(stagingRoot, name+"-*.download")
	if err != nil {
		return "", err
	}
	path := file.Name()
	cleanup := func(returnErr error) (string, error) { file.Close(); os.Remove(path); return "", returnErr }
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return cleanup(err)
	}
	response, err := manager.httpClient.Do(request)
	if err != nil {
		return cleanup(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return cleanup(fmt.Errorf("artifact download HTTP %d", response.StatusCode))
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxArtifactBytes+1))
	if err != nil {
		return cleanup(err)
	}
	if written > maxArtifactBytes {
		return cleanup(errors.New("artifact is too large"))
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256) {
		return cleanup(errors.New("artifact sha256 mismatch"))
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0755); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func validateELFArchitecture(path string) error {
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("artifact is not an ELF binary: %w", err)
	}
	defer file.Close()
	want := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		want = elf.EM_AARCH64
	}
	if file.Machine != want {
		return fmt.Errorf("artifact architecture %s does not match %s", file.Machine, runtime.GOARCH)
	}
	return nil
}

func binaryVersion(ctx context.Context, path string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, path, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("version check failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	line := strings.TrimSpace(string(output))
	if newline := strings.IndexByte(line, '\n'); newline >= 0 {
		line = line[:newline]
	}
	if line == "" {
		return "", errors.New("binary returned an empty version")
	}
	return line, nil
}

func installAtomic(staged, target, rollbackKind string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(rollbackRoot, rollbackKind), 0700); err != nil {
		return err
	}
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
		backup := filepath.Join(rollbackRoot, rollbackKind, filepath.Base(target)+"-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
		if err := copyFile(target, backup, 0755); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := target + ".new"
	if err := copyFile(staged, temporary, 0755); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		os.Remove(temporary)
		return err
	}
	return pruneRollback(filepath.Join(rollbackRoot, rollbackKind), maxRollbackFiles)
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func pruneRollback(directory string, keep int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(files)
	for len(files) > keep {
		if err := os.Remove(files[0]); err != nil {
			return err
		}
		files = files[1:]
	}
	return nil
}

func latestRollback(kind string) (string, error) {
	directory := filepath.Join(rollbackRoot, kind)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, filepath.Join(directory, entry.Name()))
		}
	}
	if len(files) == 0 {
		return "", errors.New("no rollback version is available")
	}
	sort.Strings(files)
	return files[len(files)-1], nil
}

func (manager *lifecycleManager) installOrUpdateCore(ctx context.Context, artifact managementArtifact) error {
	staged, err := manager.downloadArtifact(ctx, artifact, "mmwxc-core")
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if err := validateELFArchitecture(staged); err != nil {
		return err
	}
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	version, err := binaryVersion(versionCtx, staged, "version")
	if err != nil || !strings.Contains(strings.ToLower(version), "xray") {
		return errors.New("artifact is not a supported Xray Core")
	}
	wasActive := serviceActive(ctx, coreServiceName)
	if err := installAtomic(staged, coreBinaryPath, "core"); err != nil {
		return err
	}
	if err := writeCoreService(); err != nil {
		if wasActive {
			return manager.restoreCoreAfterFailure(ctx, err)
		}
		return err
	}
	if !wasActive && !artifact.Activate {
		return nil
	}
	if _, err := os.Stat(coreConfigPath); err != nil {
		if wasActive {
			return manager.restoreCoreAfterFailure(ctx, errors.New("Custom Core config is not ready; refusing to activate"))
		}
		return errors.New("Custom Core config is not ready; refusing to activate")
	}
	if err := systemctl(ctx, "enable", coreServiceName); err != nil {
		return manager.restoreCoreAfterFailure(ctx, err)
	}
	if err := systemctl(ctx, "restart", coreServiceName); err != nil {
		return manager.restoreCoreAfterFailure(ctx, err)
	}
	if err := manager.waitCoreReady(ctx, 20*time.Second); err != nil {
		return manager.restoreCoreAfterFailure(ctx, err)
	}
	return nil
}

func writeCoreService() error {
	if err := os.MkdirAll(filepath.Dir(coreServicePath), 0755); err != nil {
		return err
	}
	temporary := coreServicePath + ".new"
	if err := os.WriteFile(temporary, []byte(coreServiceUnit), 0644); err != nil {
		return err
	}
	if err := os.Rename(temporary, coreServicePath); err != nil {
		os.Remove(temporary)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return systemctl(ctx, "daemon-reload")
}

func (manager *lifecycleManager) applyCoreConfig(ctx context.Context, config json.RawMessage, activate bool) error {
	if !json.Valid(config) || len(config) > 8<<20 {
		return errors.New("invalid Xray config")
	}
	if _, err := os.Stat(coreBinaryPath); err != nil {
		return errors.New("Custom Core is not installed")
	}
	if err := os.MkdirAll(filepath.Dir(coreConfigPath), 0700); err != nil {
		return err
	}
	if err := ensureCoreAssets(config); err != nil {
		return err
	}
	staged, err := os.CreateTemp(filepath.Dir(coreConfigPath), "config-*.json")
	if err != nil {
		return err
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	if err := staged.Chmod(0600); err != nil {
		staged.Close()
		return err
	}
	if _, err := staged.Write(config); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(testCtx, coreBinaryPath, "run", "-test", "-config", stagedPath)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("Xray config validation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	wasActive := serviceActive(ctx, coreServiceName)
	backupPath := ""
	if info, err := os.Stat(coreConfigPath); err == nil && info.Mode().IsRegular() {
		if err := os.MkdirAll(filepath.Join(rollbackRoot, "config"), 0700); err != nil {
			return err
		}
		backup := filepath.Join(rollbackRoot, "config", "config-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json")
		if err := copyFile(coreConfigPath, backup, 0600); err != nil {
			return err
		}
		backupPath = backup
		if err := pruneRollback(filepath.Join(rollbackRoot, "config"), maxRollbackFiles); err != nil {
			return err
		}
	}
	if err := copyFile(stagedPath, coreConfigPath+".new", 0600); err != nil {
		return err
	}
	if err := os.Rename(coreConfigPath+".new", coreConfigPath); err != nil {
		return err
	}
	if !wasActive && !activate {
		return nil
	}
	if err := writeCoreService(); err != nil {
		return manager.restoreConfigAfterFailure(ctx, backupPath, wasActive, err)
	}
	if err := systemctl(ctx, "enable", coreServiceName); err != nil {
		return manager.restoreConfigAfterFailure(ctx, backupPath, wasActive, err)
	}
	if err := systemctl(ctx, "restart", coreServiceName); err != nil {
		return manager.restoreConfigAfterFailure(ctx, backupPath, wasActive, err)
	}
	if err := manager.waitCoreReady(ctx, 20*time.Second); err != nil {
		return manager.restoreConfigAfterFailure(ctx, backupPath, wasActive, err)
	}
	return nil
}

func ensureCoreAssets(config []byte) error {
	return copyRequiredCoreAssets(config, filepath.Dir(coreBinaryPath), coreAssetCandidates())
}

func copyRequiredCoreAssets(config []byte, destination string, candidates []string) error {
	required := make([]string, 0, 2)
	for _, asset := range []string{"geoip.dat", "geosite.dat"} {
		prefix := strings.TrimSuffix(asset, ".dat") + ":"
		if bytes.Contains(config, []byte(prefix)) {
			required = append(required, asset)
		}
	}
	for _, asset := range required {
		target := filepath.Join(destination, asset)
		if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			continue
		}
		source := ""
		for _, directory := range candidates {
			candidate := filepath.Join(directory, asset)
			if filepath.Clean(candidate) == filepath.Clean(target) {
				continue
			}
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				source = candidate
				break
			}
		}
		if source == "" {
			return fmt.Errorf("required Xray asset %s was not found", asset)
		}
		if err := copyFile(source, target+".new", 0644); err != nil {
			return fmt.Errorf("copy Xray asset %s: %w", asset, err)
		}
		if err := os.Rename(target+".new", target); err != nil {
			_ = os.Remove(target + ".new")
			return fmt.Errorf("install Xray asset %s: %w", asset, err)
		}
	}
	return nil
}

func coreAssetCandidates() []string {
	candidates := []string{
		"/usr/local/share/xray", "/usr/share/xray", "/opt/share/xray",
		"/usr/local/etc/xray", "/etc/xray", "/etc/mmwx/xray", "/etc/mmwx/data/xray",
		"/etc/mmwx/data", "/etc/mmwx", "/var/lib/mmwx/xray", "/opt/mmwx/xray", "/opt/mmwx",
	}
	entries, err := os.ReadDir("/proc")
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := strconv.Atoi(entry.Name()); err != nil {
				continue
			}
			processRoot := filepath.Join("/proc", entry.Name())
			cmdline, err := os.ReadFile(filepath.Join(processRoot, "cmdline"))
			lowerCmdline := bytes.ToLower(cmdline)
			if err != nil || (!bytes.Contains(lowerCmdline, []byte("xray")) && !bytes.Contains(lowerCmdline, []byte("mmw-agent"))) {
				continue
			}
			if executable, err := os.Readlink(filepath.Join(processRoot, "exe")); err == nil {
				candidates = append(candidates, filepath.Dir(executable))
			}
			if cwd, err := os.Readlink(filepath.Join(processRoot, "cwd")); err == nil {
				candidates = append(candidates, cwd)
			}
			if environment, err := os.ReadFile(filepath.Join(processRoot, "environ")); err == nil {
				for _, value := range bytes.Split(environment, []byte{0}) {
					for _, prefix := range [][]byte{[]byte("XRAY_LOCATION_ASSET="), []byte("xray.location.asset=")} {
						if bytes.HasPrefix(value, prefix) {
							candidates = append(candidates, string(bytes.TrimPrefix(value, prefix)))
						}
					}
				}
			}
		}
	}
	for _, root := range []string{"/etc/mmwx", "/opt/mmwx", "/var/lib/mmwx", "/usr/local/etc/xray"} {
		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() && strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootDepth > 5 {
				return filepath.SkipDir
			}
			if !entry.IsDir() && (entry.Name() == "geoip.dat" || entry.Name() == "geosite.dat") {
				candidates = append(candidates, filepath.Dir(path))
			}
			return nil
		})
	}
	seen := make(map[string]struct{}, len(candidates))
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(strings.TrimSpace(candidate))
		if candidate == "." || candidate == "/" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

func (manager *lifecycleManager) restoreConfigAfterFailure(ctx context.Context, backup string, wasActive bool, cause error) error {
	if backup == "" {
		if !wasActive {
			_ = systemctl(ctx, "stop", coreServiceName)
			_ = systemctl(ctx, "disable", coreServiceName)
		}
		return fmt.Errorf("Custom Core config activation failed and no previous config is available: %w", cause)
	}
	if err := copyFile(backup, coreConfigPath+".new", 0600); err != nil {
		return fmt.Errorf("Custom Core config activation failed (%v); rollback copy failed: %w", cause, err)
	}
	if err := os.Rename(coreConfigPath+".new", coreConfigPath); err != nil {
		return fmt.Errorf("Custom Core config activation failed (%v); rollback replace failed: %w", cause, err)
	}
	if wasActive {
		if err := systemctl(ctx, "restart", coreServiceName); err != nil {
			return fmt.Errorf("Custom Core config activation failed (%v); rollback restart failed: %w", cause, err)
		}
		if err := manager.waitCoreReady(ctx, 20*time.Second); err != nil {
			return fmt.Errorf("Custom Core config activation failed (%v); rollback health check failed: %w", cause, err)
		}
	}
	return fmt.Errorf("Custom Core config activation failed and the previous config was restored: %w", cause)
}

func (manager *lifecycleManager) restartCore(ctx context.Context) error {
	if _, err := os.Stat(coreBinaryPath); err != nil {
		return errors.New("Custom Core is not installed")
	}
	if _, err := os.Stat(coreConfigPath); err != nil {
		return errors.New("Custom Core config is not ready")
	}
	if err := systemctl(ctx, "restart", coreServiceName); err != nil {
		return err
	}
	return manager.waitCoreReady(ctx, 20*time.Second)
}

func (manager *lifecycleManager) stopCore(ctx context.Context) error {
	if err := systemctl(ctx, "disable", "--now", coreServiceName); err != nil {
		return err
	}
	if serviceActive(ctx, coreServiceName) {
		return errors.New("Custom Core remained active after stop")
	}
	return nil
}

type systemctlRunner func(context.Context, ...string) error
type serviceActiveProbe func(context.Context, string) bool

func controlOfficialXray(ctx context.Context, action string, run systemctlRunner, active serviceActiveProbe) error {
	switch action {
	case "stop":
		if err := run(ctx, "mask", "--now", "xray.service"); err != nil {
			return err
		}
		if active(ctx, "xray.service") {
			return errors.New("official Xray remained active after mask --now")
		}
	case "start":
		if err := run(ctx, "unmask", "xray.service"); err != nil {
			return err
		}
		if err := run(ctx, "daemon-reload"); err != nil {
			return err
		}
		startErr := run(ctx, "start", "xray.service")
		deadline := time.Now().Add(20 * time.Second)
		for {
			if active(ctx, "xray.service") {
				return nil
			}
			if time.Now().After(deadline) {
				if startErr != nil {
					return fmt.Errorf("official Xray remained inactive after unmask: %w", startErr)
				}
				return errors.New("official Xray remained inactive after unmask and start")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	default:
		return errors.New("unsupported official Xray action")
	}
	return nil
}

func (manager *lifecycleManager) stopOfficialXray(ctx context.Context) error {
	return controlOfficialXray(ctx, "stop", systemctl, serviceActive)
}

func (manager *lifecycleManager) startOfficialXray(ctx context.Context) error {
	return controlOfficialXray(ctx, "start", systemctl, serviceActive)
}

func (manager *lifecycleManager) rollbackCore(ctx context.Context) error {
	backup, err := latestRollback("core")
	if err != nil {
		return err
	}
	current := coreBinaryPath + ".rollback-current"
	defer os.Remove(current)
	if _, err := os.Stat(coreBinaryPath); err == nil {
		if err := copyFile(coreBinaryPath, current, 0755); err != nil {
			return err
		}
	}
	if err := copyFile(backup, coreBinaryPath+".new", 0755); err != nil {
		return err
	}
	if err := os.Rename(coreBinaryPath+".new", coreBinaryPath); err != nil {
		return err
	}
	restoreCurrent := func() {
		if _, statErr := os.Stat(current); statErr == nil {
			_ = copyFile(current, coreBinaryPath+".new", 0755)
			_ = os.Rename(coreBinaryPath+".new", coreBinaryPath)
			_ = systemctl(ctx, "restart", coreServiceName)
		}
	}
	if serviceActive(ctx, coreServiceName) {
		if err := systemctl(ctx, "restart", coreServiceName); err != nil {
			restoreCurrent()
			return err
		}
		if err := manager.waitCoreReady(ctx, 20*time.Second); err != nil {
			restoreCurrent()
			return err
		}
	}
	return nil
}

func (manager *lifecycleManager) restoreCoreAfterFailure(ctx context.Context, cause error) error {
	backup, err := latestRollback("core")
	if err != nil {
		return fmt.Errorf("Custom Core update failed and no rollback is available: %w", cause)
	}
	if err := copyFile(backup, coreBinaryPath+".new", 0755); err != nil {
		return fmt.Errorf("Custom Core update failed (%v); rollback copy failed: %w", cause, err)
	}
	if err := os.Rename(coreBinaryPath+".new", coreBinaryPath); err != nil {
		return fmt.Errorf("Custom Core update failed (%v); rollback replace failed: %w", cause, err)
	}
	if err := systemctl(ctx, "restart", coreServiceName); err != nil {
		return fmt.Errorf("Custom Core update failed (%v); rollback restart failed: %w", cause, err)
	}
	if err := manager.waitCoreReady(ctx, 20*time.Second); err != nil {
		return fmt.Errorf("Custom Core update failed (%v); rollback health check failed: %w", cause, err)
	}
	return fmt.Errorf("Custom Core update failed and the previous binary was restored: %w", cause)
}

func (manager *lifecycleManager) waitCoreReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if serviceActive(ctx, coreServiceName) {
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := manager.core.snapshot(probeCtx)
			cancel()
			if err == nil {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("Custom Core health check timed out")
}

func (manager *lifecycleManager) coreStatus(ctx context.Context) componentStatus {
	status := componentStatus{BinaryPath: coreBinaryPath, ConfigPath: coreConfigPath, Service: coreServiceName}
	info, err := os.Stat(coreBinaryPath)
	if err != nil {
		status.Error = "not installed"
		return status
	}
	status.Installed = true
	if _, err := os.Stat(coreServicePath); err == nil {
		status.Prepared = true
	}
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
	status.Active = serviceActive(ctx, coreServiceName)
	if status.Active {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := manager.core.snapshot(probeCtx)
		cancel()
		status.Ready = err == nil
		if err != nil {
			status.Error = err.Error()
		}
	}
	return status
}

func serviceActive(ctx context.Context, service string) bool {
	return systemctl(ctx, "is-active", "--quiet", service) == nil
}

func systemctl(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "systemctl", args...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (manager *lifecycleManager) scheduleHelperUpdate(ctx context.Context, artifact managementArtifact) error {
	if err := validateArtifact(artifact); err != nil {
		return err
	}
	if err := os.MkdirAll(stagingRoot, 0700); err != nil {
		return err
	}
	pruneStaleUpdateWorkers(time.Now())
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	worker := filepath.Join(stagingRoot, fmt.Sprintf("mmwxc-helper-update-worker-%d", time.Now().UnixNano()))
	if err := copyFile(executable, worker+".new", 0755); err != nil {
		return err
	}
	if err := os.Rename(worker+".new", worker); err != nil {
		return err
	}
	unit := fmt.Sprintf("mmwxc-helper-update-%d", time.Now().UnixNano())
	command := exec.CommandContext(ctx, "systemd-run", "--collect", "--unit", unit, worker, "--managed-helper-update", "--update-url", artifact.URL, "--update-sha256", artifact.SHA256, "--update-version", artifact.Version)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("schedule helper update: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func pruneStaleUpdateWorkers(now time.Time) {
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "mmwxc-helper-update-worker-") {
			continue
		}
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(filepath.Join(stagingRoot, entry.Name()))
		}
	}
}

func runManagedHelperUpdate(ctx context.Context, artifact managementArtifact, configPath string) error {
	startedAt := time.Now().UTC()
	manager := newLifecycleManager(newCoreClient(defaultCoreSocket))
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	staged, err := manager.downloadArtifact(ctx, artifact, "mmwxc-helper")
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if err := validateELFArchitecture(staged); err != nil {
		return err
	}
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	versionLine, err := binaryVersion(versionCtx, staged, "--version")
	cancel()
	if err != nil || !strings.HasPrefix(versionLine, "mmwxc-helper v") {
		return errors.New("artifact is not mmwxc-helper")
	}
	newVersion := strings.TrimPrefix(versionLine, "mmwxc-helper ")
	if artifact.Version != "" && artifact.Version != newVersion {
		return errors.New("helper version does not match command")
	}
	if err := installAtomic(staged, helperBinaryPath, "helper"); err != nil {
		return err
	}
	if err := systemctl(ctx, "restart", helperServiceName); err != nil {
		return restoreHelperAfterFailure(ctx, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state, loadErr := loadLocalState(cfg.StatePath)
		if loadErr == nil && state.HelperVersion == newVersion && state.HeartbeatAt.After(startedAt) && state.ControllerConnectedAt.After(startedAt) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return restoreHelperAfterFailure(ctx, errors.New("helper heartbeat health check timed out"))
}

func cleanupManagedUpdateWorker() {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	if filepath.Clean(filepath.Dir(executable)) == stagingRoot && strings.HasPrefix(filepath.Base(executable), "mmwxc-helper-update-worker-") {
		_ = os.Remove(executable)
	}
}

func restoreHelperAfterFailure(ctx context.Context, cause error) error {
	backup, err := latestRollback("helper")
	if err == nil {
		_ = copyFile(backup, helperBinaryPath, 0755)
		_ = systemctl(ctx, "restart", helperServiceName)
	}
	return fmt.Errorf("helper update failed and rollback was attempted: %w", cause)
}
