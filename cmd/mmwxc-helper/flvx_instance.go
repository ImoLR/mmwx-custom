package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

const (
	flvxBinaryPath  = "/opt/mmwxc/flvx-core/mmwxc-flvx-core"
	flvxConfigPath  = "/etc/mmwxc/flvx-core/config.json"
	flvxStateDir    = "/var/lib/mmwxc-helper/flvx"
	flvxSocketPath  = "/run/mmwxc-flvx/core-control.sock"
	flvxServiceName = "mmwxc-flvx-core.service"
	flvxUnitPath    = "/etc/systemd/system/" + flvxServiceName
)

type flvxLocalState struct {
	Payload      flvx.Payload             `json:"payload"`
	APIPort      int                      `json:"api_port"`
	Counters     map[string]*flvx.Counter `json:"counters"`
	Applied      map[string]string        `json:"applied"`
	AppliedEpoch string                   `json:"applied_epoch"`
	Report       flvx.Report              `json:"report"`
}

type flvxManager struct {
	config                          config
	dir, binary, configPath, socket string
	state                           flvxLocalState
	core                            *coreClient
	lifecycle                       *lifecycleManager
	incoming                        chan flvx.Envelope
	mu                              sync.Mutex
	report                          *flvx.Report
	logs                            *periodicLogLimiter
	verifiedHash                    string
	verifiedMTime                   time.Time
	removed                         bool
	// The real transport is the fixed Core CLI's gRPC client; tests inject a fake.
	apiCall      func(context.Context, string, ...string) ([]byte, error)
	readOfficial func() ([]int, error)
	setup        func(context.Context) error
	stop         func(context.Context) error
	cleanup      func(context.Context) error
}

func newFLVXManager(cfg config) (*flvxManager, error) {
	m := &flvxManager{config: cfg, dir: flvxStateDir, binary: flvxBinaryPath, configPath: flvxConfigPath, socket: flvxSocketPath, incoming: make(chan flvx.Envelope, 1), logs: newPeriodicLogLimiter()}
	m.core = newCoreClient(m.socket)
	m.lifecycle = newLifecycleManager(m.core)
	m.apiCall = m.api
	m.setup = m.ensureService
	m.stop = func(ctx context.Context) error {
		if _, err := os.Stat(flvxUnitPath); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return systemctl(ctx, "stop", flvxServiceName)
	}
	m.cleanup = m.removeInstance
	m.readOfficial = func() ([]int, error) {
		raw, err := os.ReadFile(officialConfigPath)
		if err != nil {
			return nil, err
		}
		return flvx.OfficialPorts(raw)
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *flvxManager) load() error {
	raw, err := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &m.state); err != nil {
			return errors.New("invalid FLVX instance state; refusing to reset counters")
		}
	}
	if m.state.Counters == nil {
		m.state.Counters = map[string]*flvx.Counter{}
	}
	if m.state.Applied == nil {
		m.state.Applied = map[string]string{}
	}
	if m.state.Report.Generation == "" {
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return err
		}
		m.state.Report.Generation = hex.EncodeToString(id[:])
	}
	if m.state.Payload.Version > 0 {
		m.publish()
	}
	return nil
}

func writeFLVXFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer os.Remove(path + ".tmp")
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(path+".tmp", path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (m *flvxManager) persist() error {
	raw, err := json.Marshal(m.state)
	if err != nil {
		return err
	}
	return writeFLVXFile(filepath.Join(m.dir, "state.json"), raw, 0600)
}

func (m *flvxManager) publish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.state.Report
	r.Nodes = append([]flvx.NodeStatus{}, r.Nodes...)
	m.report = &r
}

func (m *flvxManager) snapshotReport() *flvx.Report {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.report == nil {
		return nil
	}
	r := *m.report
	r.Nodes = append([]flvx.NodeStatus{}, r.Nodes...)
	return &r
}

func (m *flvxManager) submit(e *flvx.Envelope) {
	if m == nil || e == nil {
		return
	}
	select {
	case m.incoming <- *e:
	default:
		select {
		case <-m.incoming:
		default:
		}
		m.incoming <- *e
	}
}

func (m *flvxManager) run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var nextAttempt time.Time
	retryDelay := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-m.incoming:
			changed := e.Payload.Version != m.state.Payload.Version
			if err := m.accept(e); err != nil {
				m.logs.report("FLVX desired rejected", err)
				continue
			}
			m.logs.report("FLVX desired rejected", nil)
			if changed {
				nextAttempt = time.Time{}
				retryDelay = 5 * time.Second
			}
		case <-ticker.C:
		}
		if m.state.Payload.Version == 0 {
			continue
		}
		if time.Now().Before(nextAttempt) {
			continue
		}
		child, cancel := context.WithTimeout(ctx, 16*time.Minute)
		err := m.reconcile(child)
		cancel()
		m.logs.report("FLVX instance reconcile", err)
		if err != nil {
			stopContext, stopCancel := context.WithTimeout(ctx, 5*time.Second)
			m.logs.report("FLVX failed instance stop", m.stop(stopContext))
			stopCancel()
			m.state.Report.Core.State = "error"
			for i := range m.state.Report.Nodes {
				if m.state.Report.Nodes[i].State != "removed" {
					m.state.Report.Nodes[i].State = "error"
					m.state.Report.Nodes[i].Error = "FLVX Core reconciliation failed"
				}
			}
			nextAttempt = time.Now().Add(retryDelay)
			if retryDelay < 5*time.Minute {
				retryDelay *= 2
				if retryDelay > 5*time.Minute {
					retryDelay = 5 * time.Minute
				}
			}
		} else {
			retryDelay = 5 * time.Second
			nextAttempt = time.Now().Add(5 * time.Second)
		}
		m.publish()
	}
}

func (m *flvxManager) accept(e flvx.Envelope) error {
	if !e.Verify(m.config.Token, m.config.ServerID, time.Now()) {
		return errors.New("invalid FLVX desired signature or expiry")
	}
	p := e.Payload
	if p.Version < m.state.Payload.Version {
		return errors.New("stale FLVX desired version")
	}
	if p.Status != "active" && p.Status != "paused" && p.Status != "revoked" {
		return errors.New("invalid FLVX share status")
	}
	if err := flvx.ValidateConfig(p.Config); err != nil {
		return err
	}
	if p.Desired.Revision > 0 {
		if len(flvx.ValidateDesired(p.Desired, p.Config, p.OfficialPorts)) > 0 && p.Status == "active" {
			return errors.New("invalid FLVX structured desired state")
		}
	}
	if p.Desired.Revision < m.state.Payload.Desired.Revision {
		return errors.New("stale FLVX node revision")
	}
	if m.removed && p.Status == "revoked" && p.Version == m.state.Payload.Version {
		return nil
	}
	m.state.Payload = p
	period := flvx.Period(time.Now(), p.Config.ResetDay)
	if m.state.Report.Period != period {
		m.state.Report.Period = period
		m.state.Report.TrafficUsedBytes = 0
	}
	if p.Period == period && p.TrafficUsedBytes > m.state.Report.TrafficUsedBytes {
		m.state.Report.TrafficUsedBytes = p.TrafficUsedBytes
	}
	return m.persist()
}

func flvxServiceUnit(c flvx.Config) string {
	return fmt.Sprintf(`[Unit]
Description=MMWXC FLVX shared Core
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=mmwxc-flvx
Group=mmwxc-flvx
Environment=MMWXC_CORE_CONTROL_SOCKET=%s
ExecStart=%s run -config %s
RuntimeDirectory=mmwxc-flvx
RuntimeDirectoryMode=0700
UMask=0077
MemoryMax=%dM
CPUQuota=%d%%
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=/run/mmwxc-flvx
CapabilityBoundingSet=
RestrictSUIDSGID=yes
LimitNOFILE=65535
LogRateLimitIntervalSec=60s
LogRateLimitBurst=10
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, flvxSocketPath, flvxBinaryPath, flvxConfigPath, c.MemoryMB, c.CPUPercent)
}

func flvxFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxArtifactBytes+1))
	if err != nil || n > maxArtifactBytes {
		return "", errors.New("invalid FLVX binary size")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m *flvxManager) ensureBinary(ctx context.Context) (bool, error) {
	a := m.state.Payload.Artifact
	artifact := managementArtifact{URL: a.URL, SHA256: a.SHA256, Version: a.Version, GitHubAccelerator: a.GitHubAccelerator}
	if err := validateArtifact(artifact); err != nil {
		return false, err
	}
	if info, err := os.Stat(m.binary); err == nil {
		if m.verifiedHash == a.SHA256 && m.verifiedMTime.Equal(info.ModTime()) {
			return false, nil
		}
		if hash, err := flvxFileHash(m.binary); err == nil && hash == a.SHA256 {
			m.verifiedHash, m.verifiedMTime = hash, info.ModTime()
			return false, nil
		}
	}
	m.state.Report.Core.State = "installing"
	m.publish()
	staged := ""
	// Read the already installed Custom binary when it matches the release hash;
	// never install, restart or otherwise mutate the official-serving instance.
	if hash, err := flvxFileHash(coreBinaryPath); err == nil && hash == a.SHA256 {
		staged = coreBinaryPath
	} else {
		var err error
		staged, err = m.lifecycle.downloadArtifact(ctx, artifact, "mmwxc-flvx-core")
		if err != nil {
			return false, errors.New("FLVX Core download or checksum failed")
		}
		defer os.Remove(staged)
	}
	if err := validateELFArchitecture(staged); err != nil {
		return false, err
	}
	version, err := binaryVersion(ctx, staged, "version")
	if err != nil || !strings.Contains(strings.ToLower(version), "xray") {
		return false, errors.New("invalid Custom Core binary")
	}
	if err = os.MkdirAll(filepath.Dir(m.binary), 0755); err != nil {
		return false, err
	}
	if err = copyFile(staged, m.binary+".new", 0755); err != nil {
		return false, err
	}
	if hash, err := flvxFileHash(m.binary + ".new"); err != nil || hash != a.SHA256 {
		os.Remove(m.binary + ".new")
		return false, errors.New("FLVX staged binary checksum mismatch")
	}
	if err = os.Rename(m.binary+".new", m.binary); err != nil {
		return false, err
	}
	m.verifiedHash = ""
	return true, nil
}

func (m *flvxManager) ensureService(ctx context.Context) error {
	changed, err := m.ensureBinary(ctx)
	if err != nil {
		return err
	}
	account, err := user.Lookup("mmwxc-flvx")
	if err != nil {
		if err = exec.CommandContext(ctx, "useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "mmwxc-flvx").Run(); err != nil {
			return errors.New("create FLVX service account failed")
		}
		account, err = user.Lookup("mmwxc-flvx")
		if err != nil {
			return err
		}
	}
	if account.Uid == "0" {
		return errors.New("FLVX Core must be unprivileged")
	}
	if m.state.APIPort == 0 {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return err
		}
		m.state.APIPort = l.Addr().(*net.TCPAddr).Port
		l.Close()
		if err = m.persist(); err != nil {
			return err
		}
	}
	data, _ := json.Marshal(flvx.BaseConfig(m.state.APIPort))
	previousConfig, _ := os.ReadFile(m.configPath)
	if string(previousConfig) != string(data) {
		if err = writeFLVXFile(m.configPath, data, 0640); err != nil {
			return err
		}
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	if err = os.Chown(filepath.Dir(m.configPath), 0, gid); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Dir(m.configPath), 0750); err != nil {
		return err
	}
	if err = os.Chown(m.configPath, 0, gid); err != nil {
		return err
	}
	unit := flvxServiceUnit(m.state.Payload.Config)
	previous, _ := os.ReadFile(flvxUnitPath)
	if string(previous) != unit {
		if err = writeFLVXFile(flvxUnitPath, []byte(unit), 0644); err != nil {
			return err
		}
		if err = systemctl(ctx, "daemon-reload"); err != nil {
			return err
		}
		if err = systemctl(ctx, "set-property", "--runtime", flvxServiceName, fmt.Sprintf("MemoryMax=%dM", m.state.Payload.Config.MemoryMB), fmt.Sprintf("CPUQuota=%d%%", m.state.Payload.Config.CPUPercent)); err != nil {
			return err
		}
	}
	if changed {
		if err = m.stop(ctx); err != nil {
			return err
		}
	}
	if _, err = m.core.snapshot(ctx); err != nil {
		if err = systemctl(ctx, "enable", "--now", flvxServiceName); err != nil {
			return err
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, err = m.core.snapshot(ctx); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if err != nil {
			return errors.New("FLVX Core readiness timed out")
		}
	}
	return nil
}

func (m *flvxManager) removeInstance(ctx context.Context) error {
	if _, err := os.Stat(flvxUnitPath); err == nil {
		if err := systemctl(ctx, "disable", "--now", flvxServiceName); err != nil {
			return err
		}
	}
	for _, path := range []string{flvxUnitPath, flvxUnitPath + ".d", "/run/systemd/system.control/" + flvxServiceName + ".d", filepath.Dir(flvxConfigPath), filepath.Dir(flvxBinaryPath), filepath.Dir(flvxSocketPath)} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return os.RemoveAll(m.dir)
}
