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
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
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
	Payload      flvx.Payload              `json:"payload"`
	APIPort      int                       `json:"api_port"`
	Counters     map[string]*flvx.Counter  `json:"counters"`
	Applied      map[string]string         `json:"applied"`
	AppliedEpoch string                    `json:"applied_epoch"`
	RealityPins  map[string]flvxRealityPin `json:"reality_pins,omitempty"`
	Report       flvx.Report               `json:"report"`
	// Landing egress plan applied in AppliedEpoch ("" = built-in default).
	EgressApplied    string   `json:"egress_applied,omitempty"`
	EgressCovered    []string `json:"egress_covered,omitempty"`
	EgressRevision   int64    `json:"egress_revision,omitempty"`
	EgressGeoBytes   int64    `json:"egress_geo_bytes,omitempty"`
	EgressStale      bool     `json:"egress_stale,omitempty"`
	RoutingRestartAt int64    `json:"routing_restart_at,omitempty"`
	// landing-protocols-v1: features of the installed FLVX Core build.
	CoreFeatures *flvxCoreFeatures `json:"core_features,omitempty"`
	// landing-reality-guard-v1: loopback port -> guard group, kept while a
	// node needs or still points at it; GuardRulePorts are the groups whose
	// rules the rule set applied in AppliedEpoch holds. See flvx_guard.go.
	Guards         map[int]flvx.Guard `json:"reality_guards,omitempty"`
	GuardRulePorts []int              `json:"guard_rule_ports,omitempty"`
}

type flvxRealityPin struct {
	Hash string `json:"hash"`
	Dest string `json:"dest"`
}

type flvxManager struct {
	config                          config
	dir, binary, configPath, socket string
	unitPath, service               string
	state                           flvxLocalState
	core                            *coreClient
	lifecycle                       *lifecycleManager
	incoming                        chan flvx.Envelope
	mu                              sync.Mutex
	report                          *flvx.Report
	machinePorts                    machinePorts
	machineCloseWait                *int64
	logs                            *periodicLogLimiter
	verifiedHash                    string
	verifiedMTime                   time.Time
	persistedHash                   [32]byte
	persistedPayload                [32]byte
	persistedAt                     time.Time
	reportedMaterial                [32]byte
	reconciledPayload               [32]byte
	removed                         bool
	// Landing egress caches; see flvx_egress.go.
	egress                    flvxEgressCache
	egressSources             flvx.SourceMemo
	egressCleanupAt           time.Time
	routingEpoch              string
	routingOK, routingMissing bool
	withheld                  map[string]string
	geoStamps                 map[string]flvxFileStamp
	geoRetry                  map[string]*flvxRetry
	capability                [2]bool
	capabilityAt              time.Time
	pins                      map[string]netip.Addr
	egressValidKey            string
	egressInvalid             string
	detectLocal               func() flvx.LocalAddrs
	lookupIP                  func(context.Context, string) ([]netip.Addr, error)
	geoDownload               func(context.Context, string, string) error
	diskFree                  func(string) (int64, error)
	restartCore               func(context.Context) error
	memory                    func() (current, available int64, availableOK bool)
	// landing-protocols-v1; see flvx_protocols.go.
	featuresEpoch  string
	readFeatures   func(context.Context) ([]string, error)
	readListening  func() (tcp, udp []uint32, err error)
	listeningCache *flvx.ListeningPorts
	listeningAt    time.Time
	listeningKey   string
	// landing-reality-guard-v1; see flvx_guard.go.
	guards guardPass
	// landing-user-limits-v1; see flvx_user_limits.go and flvx_rate.go.
	// budgetMu guards the budgets handed over by the upload loop; the other
	// fields belong to the pass, userLimits is published under mu.
	budgetMu       sync.Mutex
	budgets        *flvx.UserLimitsEnvelope
	budgetsExpired bool
	budgetCache    *flvxBudgetView
	budgetCacheOf  *flvx.UserLimitsEnvelope
	appliedBudget  int64
	userLimits     flvxUserLimitsPublished
	rate           *flvxRateLimiter
	// pickPort returns a free loopback TCP port; tests inject a fake.
	pickPort func() (int, error)
	// The real transport is the fixed Core CLI's gRPC client; tests inject a fake.
	apiCall      func(context.Context, string, ...string) ([]byte, error)
	lookupHost   func(context.Context, string) ([]string, error)
	writeState   func(string, []byte, os.FileMode) error
	clock        func() time.Time
	readOfficial func() ([]int, error)
	setup        func(context.Context) error
	stop         func(context.Context) error
	cleanup      func(context.Context) error
	// Parent-directory access for the FLVX Core account; see flvx_access.go.
	accessFixer *flvxAccessFixer
	preflight   func(context.Context, flvxAccount) error
	accessIssue string
}

func newFLVXManager(cfg config) (*flvxManager, error) {
	m := &flvxManager{config: cfg, dir: flvxStateDir, binary: flvxBinaryPath, configPath: flvxConfigPath, socket: flvxSocketPath, unitPath: flvxUnitPath, service: flvxServiceName, incoming: make(chan flvx.Envelope, 1), logs: newPeriodicLogLimiter()}
	m.core = newCoreClient(m.socket)
	m.lifecycle = newLifecycleManager(m.core)
	m.rate = newFLVXRateLimiter()
	m.apiCall = m.api
	m.setup = m.ensureService
	m.stop = m.stopService
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
		m.persistedHash = sha256.Sum256(raw)
		payload, _ := json.Marshal(m.state.Payload)
		m.persistedPayload = sha256.Sum256(payload)
		m.persistedAt = m.now()
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
		if m.state.Payload.AckGeneration == m.state.Report.Generation && m.state.Report.Sequence < m.state.Payload.AckSequence {
			m.state.Report.Sequence = m.state.Payload.AckSequence
			if err = m.persist(); err != nil {
				return err
			}
		}
		m.publish()
	}
	return nil
}

func writeFLVXFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return durableWriteFile(path, data, mode)
}

func (m *flvxManager) persist() error {
	state := m.state
	state.Report.Nodes = append([]flvx.NodeStatus{}, state.Report.Nodes...)
	for i := range state.Report.Nodes {
		state.Report.Nodes[i].OnlineIPCount, state.Report.Nodes[i].OnlineIPs = nil, nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(raw)
	if hash == m.persistedHash {
		return nil
	}
	write := m.writeState
	if write == nil {
		write = writeFLVXFile
	}
	if err := write(filepath.Join(m.dir, "state.json"), raw, 0600); err != nil {
		return err
	}
	m.persistedHash = hash
	payload, _ := json.Marshal(m.state.Payload)
	m.persistedPayload = sha256.Sum256(payload)
	m.persistedAt = m.now()
	return nil
}

func (m *flvxManager) materialHash() [32]byte {
	s := m.state
	s.Payload.AckGeneration, s.Payload.AckSequence = "", 0
	s.Payload.Period, s.Payload.TrafficUsedBytes = "", 0
	s.Counters = nil
	s.Report.Nodes = append([]flvx.NodeStatus{}, s.Report.Nodes...)
	for i := range s.Report.Nodes {
		s.Report.Nodes[i].UpBytes = 0
		s.Report.Nodes[i].DownBytes = 0
		s.Report.Nodes[i].Connections = 0
		s.Report.Nodes[i].InboundConnections = nil
		s.Report.Nodes[i].OnlineIPCount, s.Report.Nodes[i].OnlineIPs = nil, nil
	}
	s.Report.Sequence, s.Report.CollectedAt = 0, 0
	s.Report.TrafficUsedBytes = 0
	if limit := s.Payload.Config.TrafficLimitBytes; limit > 0 && m.state.Report.TrafficUsedBytes >= limit {
		s.Report.TrafficUsedBytes = 1
	}
	raw, _ := json.Marshal(s)
	return sha256.Sum256(raw)
}

func (m *flvxManager) persistReport(force bool) error {
	for i := range m.state.Report.Nodes {
		n := &m.state.Report.Nodes[i]
		if c := m.state.Counters[n.ID]; c != nil {
			n.UpBytes, n.DownBytes = c.Up, c.Down
		}
	}
	m.state.Report.CollectedAt = m.now().UnixMilli()
	if !force && m.materialHash() == m.reportedMaterial && m.now().Sub(m.persistedAt) < 30*time.Second {
		return nil
	}
	if p := m.state.Payload; p.AckGeneration == m.state.Report.Generation && p.AckSequence > m.state.Report.Sequence {
		m.state.Report.Sequence = p.AckSequence
	}
	m.state.Report.Sequence++
	if err := m.persist(); err != nil {
		return err
	}
	m.publish()
	return nil
}

func (m *flvxManager) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

func (m *flvxManager) publish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.state.Report
	r.Nodes = append([]flvx.NodeStatus{}, r.Nodes...)
	m.report = &r
	m.reportedMaterial = m.materialHash()
	m.machinePorts = m.appliedPorts()
}

// appliedPorts are the shared Core's live inbound ports for machine
// protection: SS2022 and Mieru listen on TCP and UDP, VLESS Reality, AnyTLS
// and Snell on TCP only.
func (m *flvxManager) appliedPorts() machinePorts {
	var ports machinePorts
	for _, n := range m.state.Payload.Desired.Nodes {
		if m.state.Applied[n.Tag()] == "" || n.Port <= 0 || n.Port > 65535 {
			continue
		}
		ports.TCP = append(ports.TCP, uint32(n.Port))
		if flvx.UDPProtocol(n.Protocol) {
			ports.UDP = append(ports.UDP, uint32(n.Port))
		}
	}
	return ports.normalized()
}

func (m *flvxManager) businessPorts() machinePorts {
	if m == nil {
		return machinePorts{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return machinePorts{TCP: append([]uint32(nil), m.machinePorts.TCP...), UDP: append([]uint32(nil), m.machinePorts.UDP...)}
}

// setMachineCloseWait hands the machine owner's CLOSE_WAIT timeout to the
// shared Core; it is applied with the next limits update.
func (m *flvxManager) setMachineCloseWait(settings *machineProtectionSettings) {
	if m == nil {
		return
	}
	var value *int64
	if settings != nil {
		value = settings.closeWaitSeconds()
	}
	m.mu.Lock()
	m.machineCloseWait = value
	m.mu.Unlock()
}

func (m *flvxManager) machineCloseWaitSeconds() *int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.machineCloseWait == nil {
		return nil
	}
	value := *m.machineCloseWait
	return &value
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
		m.reconcileRates(ctx)
		m.reportBudgetExpiry()
		if err != nil {
			m.reportFailure(ctx, err)
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
	}
}

// reportFailure stops the instance after a failed reconcile and reports why;
// the reason reaches the operator's share card, FLVX sees only node errors.
func (m *flvxManager) reportFailure(ctx context.Context, err error) {
	stopContext, stopCancel := context.WithTimeout(ctx, 5*time.Second)
	m.logs.report("FLVX failed instance stop", m.stop(stopContext))
	stopCancel()
	m.state.Report.Core.State = "error"
	m.state.Report.Core.Error = flvxCoreErrorText(err)
	m.recordUserLimits(coreSnapshotResponse{})
	for i := range m.state.Report.Nodes {
		m.state.Report.Nodes[i].OnlineIPCount, m.state.Report.Nodes[i].OnlineIPs = nil, nil
		if m.state.Report.Nodes[i].State != "removed" {
			m.state.Report.Nodes[i].State = "error"
			m.state.Report.Nodes[i].Error = "FLVX Core reconciliation failed"
		}
	}
	m.logs.report("FLVX error state persistence", m.persistReport(true))
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
		// Extra protocols are checked structurally here; whether the running
		// Core may serve them is decided per node when rendering.
		if len(flvx.ValidateDesiredWith(p.Desired, p.Config, p.OfficialPorts, flvx.DesiredOptions{Protocols: flvx.AllProtocols()})) > 0 && p.Status == "active" {
			return errors.New("invalid FLVX structured desired state")
		}
	}
	if p.Desired.Revision < m.state.Payload.Desired.Revision {
		return errors.New("stale FLVX node revision")
	}
	if m.removed && p.Status == "revoked" {
		return nil
	}
	if m.reportAbsentInstance(p) {
		return nil
	}
	previousPeriod := m.state.Report.Period
	m.state.Payload = p
	period := flvx.Period(time.Now(), p.Config.ResetDay)
	if m.state.Report.Period != period {
		m.state.Report.Period = period
		m.state.Report.TrafficUsedBytes = 0
	}
	if p.Period == period && p.TrafficUsedBytes > m.state.Report.TrafficUsedBytes {
		m.state.Report.TrafficUsedBytes = p.TrafficUsedBytes
	}
	current, _ := json.Marshal(p)
	if sha256.Sum256(current) == m.persistedPayload && previousPeriod == period {
		return nil
	}
	return m.persist()
}

func (m *flvxManager) reportAbsentInstance(p flvx.Payload) bool {
	if p.Status != "revoked" || m.state.APIPort != 0 || len(m.state.Counters) > 0 || len(m.state.Applied) > 0 {
		return false
	}
	if _, err := os.Stat(filepath.Join(m.dir, "state.json")); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if _, err := os.Stat(flvxUnitPath); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	// No local lifetime counters exist. Reuse only Custom's durable totals and
	// acknowledgement; an empty stopped tombstone needs no instance files.
	m.state.Payload = p
	m.state.Report.Status = flvx.Status{AppliedRevision: p.Desired.Revision, Core: flvx.Core{State: "stopped"}, Nodes: []flvx.NodeStatus{}, TrafficUsedBytes: p.TrafficUsedBytes, CollectedAt: m.now().UnixMilli()}
	m.state.Report.Period = p.Period
	if p.AckGeneration != "" {
		m.state.Report.Generation = p.AckGeneration
		m.state.Report.Sequence = p.AckSequence
	}
	m.state.Report.Sequence++
	m.removed = true
	m.publish()
	return true
}

// flvxCoreErrorText is the reconcile failure shown on the operator's share
// card (never sent to FLVX).
func flvxCoreErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if runes := []rune(text); len(runes) > 300 {
		text = string(runes[:300])
	}
	return text
}

func (m *flvxManager) stopService(ctx context.Context) error {
	if _, err := os.Stat(m.unitPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return systemctl(ctx, "stop", m.service)
}

func (m *flvxManager) serviceUnit() string {
	return flvxServiceUnit(m.state.Payload.Config, m.binary, m.configPath, m.socket)
}

func flvxServiceUnit(c flvx.Config, binary, configPath, socket string) string {
	runtime := filepath.Dir(socket)
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
RuntimeDirectory=%s
RuntimeDirectoryMode=0700
UMask=0077
MemoryMax=%dM
CPUQuota=%d%%
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ReadWritePaths=%s
CapabilityBoundingSet=
RestrictSUIDSGID=yes
LimitNOFILE=65535
LogRateLimitIntervalSec=60s
LogRateLimitBurst=10
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, socket, binary, configPath, filepath.Base(runtime), c.MemoryMB, c.CPUPercent, runtime)
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
	m.state.Report.Core.State, m.state.Report.Core.Error = "installing", ""
	if err := m.persistReport(true); err != nil {
		return false, err
	}
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
	return m.installBinary(ctx, staged, a.SHA256)
}

func (m *flvxManager) installBinary(ctx context.Context, staged, hash string) (bool, error) {
	if err := validateELFArchitecture(staged); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(m.binary), 0755); err != nil {
		return false, err
	}
	defer os.Remove(m.binary + ".new")
	if err := copyFile(staged, m.binary+".new", 0755); err != nil {
		return false, err
	}
	if copied, err := flvxFileHash(m.binary + ".new"); err != nil || copied != hash {
		return false, errors.New("FLVX staged binary checksum mismatch")
	}
	version, err := binaryVersion(ctx, m.binary+".new", "version")
	if err != nil || !strings.Contains(strings.ToLower(version), "xray") {
		return false, errors.New("invalid Custom Core binary")
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
	identity, err := flvxAccountOf(account)
	if err != nil {
		return err
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
	// Parent directories, owners and modes; a running Core keeps serving and
	// only a start is refused (with the exact path) while access is broken.
	accessErr := m.ensureAccess(identity)
	m.accessIssue = flvxCoreErrorText(accessErr)
	unit := m.serviceUnit()
	previous, _ := os.ReadFile(m.unitPath)
	if string(previous) != unit {
		if err = writeFLVXFile(m.unitPath, []byte(unit), 0644); err != nil {
			return err
		}
		if err = systemctl(ctx, "daemon-reload"); err != nil {
			return err
		}
		if err = systemctl(ctx, "set-property", "--runtime", m.service, fmt.Sprintf("MemoryMax=%dM", m.state.Payload.Config.MemoryMB), fmt.Sprintf("CPUQuota=%d%%", m.state.Payload.Config.CPUPercent)); err != nil {
			return err
		}
	}
	_, err = m.core.snapshot(ctx)
	if changed || err != nil {
		// Check the start before stopping anything: a start that would fail is
		// reported with its path instead of looping in systemd.
		if accessErr != nil {
			return accessErr
		}
		preflight := m.preflight
		if preflight == nil {
			preflight = m.preflightCore
		}
		if err = preflight(ctx, identity); err != nil {
			m.accessIssue = flvxCoreErrorText(err)
			return err
		}
	}
	if changed {
		if err = m.stop(ctx); err != nil {
			return err
		}
	}
	if _, err = m.core.snapshot(ctx); err != nil {
		if err = systemctl(ctx, "enable", "--now", m.service); err != nil {
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
	if _, err := os.Stat(m.unitPath); err == nil {
		if err := systemctl(ctx, "disable", "--now", m.service); err != nil {
			return err
		}
	}
	for _, path := range []string{m.unitPath, m.unitPath + ".bak", m.unitPath + ".d", "/run/systemd/system.control/" + m.service + ".d", filepath.Dir(m.configPath), filepath.Dir(m.binary), filepath.Dir(m.socket)} {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := m.rate.release(ctx); err != nil {
		return err
	}
	return os.RemoveAll(m.dir)
}
