package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults approved by the user (2026-10-20): every machine with a Helper is
// protected at 1000 whole-machine connections, TIME_WAIT 500, CLOSE_WAIT 10 s.
const (
	defaultMachineMaxTotal         = int64(1000)
	defaultMachineTWBuckets        = int64(500)
	defaultMachineCloseWaitSeconds = int64(10)
)

func machineProtectionV2(status *serverMachineProtectionStatus) bool {
	return status != nil && status.Version >= 2
}

// MMWXC_MACHINE_PROTECTION_DEFAULTS=off stops new servers from receiving the
// defaults (rollback switch); servers that already have them keep them.
func machineProtectionDefaultsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MMWXC_MACHINE_PROTECTION_DEFAULTS"))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

func defaultMachineProtectionSettings() *serverMachineProtectionSettings {
	maxTotal, tw, closeWait := defaultMachineMaxTotal, defaultMachineTWBuckets, defaultMachineCloseWaitSeconds
	return &serverMachineProtectionSettings{Enabled: true, MaxTotal: &maxTotal, TWBuckets: &tw, CloseWaitSeconds: &closeWait}
}

// unconfiguredMachineProtection: nothing was ever chosen. The settings form
// always sent an empty, disabled block, so that counts as unconfigured too;
// a deliberate "off" keeps the cap value it had.
func unconfiguredMachineProtection(settings *serverMachineProtectionSettings) bool {
	return settings == nil || !settings.Enabled && settings.MaxActive == nil && settings.MaxTotal == nil && settings.TWBuckets == nil && settings.CloseWaitSeconds == nil
}

// applyMachineProtectionDefaults is the one-time default-on migration. It runs
// for a server the first time its Helper reports machine protection v2, so
// defaults arrive Helper by Helper (canary order), and never again once
// recorded, so an admin's later choice always stands.
func (s *helperState) applyMachineProtectionDefaults(officialServerID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, done := s.data.MachineProtectionDefaults[officialServerID]; done {
		return false, nil
	}
	if s.data.MachineProtectionDefaults == nil {
		s.data.MachineProtectionDefaults = make(map[string]time.Time)
	}
	settings, ok := s.data.ConnectionSettings[officialServerID]
	if !ok {
		settings = defaultServerConnectionSettings()
	}
	applied := unconfiguredMachineProtection(settings.MachineProtection)
	if applied {
		settings.MachineProtection = defaultMachineProtectionSettings()
		s.data.ConnectionSettings[officialServerID] = cloneServerConnectionSettings(settings)
	}
	s.data.MachineProtectionDefaults[officialServerID] = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		delete(s.data.MachineProtectionDefaults, officialServerID)
		return false, err
	}
	return applied, nil
}

// serverMachineSummary is the server card's machine protection badge source.
type serverMachineSummary struct {
	Version      int                       `json:"version"`
	Enabled      bool                      `json:"enabled"`
	Effective    bool                      `json:"effective"`
	MaxTotal     *int64                    `json:"max_total"`
	ReleaseBelow *int64                    `json:"release_below,omitempty"`
	Blocking     bool                      `json:"blocking"`
	WarmingUp    bool                      `json:"warming_up,omitempty"`
	DroppedTotal int64                     `json:"dropped_total"`
	Connections  *serverMachineConnections `json:"connections,omitempty"`
	TWBuckets    *int64                    `json:"tw_buckets"`
	CloseWait    *int64                    `json:"close_wait_seconds"`
	TCPPorts     int                       `json:"tcp_ports"`
	UDPPorts     int                       `json:"udp_ports"`
	Sources      *serverMachinePortSources `json:"port_sources,omitempty"`
	IdleReason   string                    `json:"idle_reason,omitempty"`
	LastError    string                    `json:"last_error,omitempty"`
}

// machineSummary returns nil for Helpers without machine protection v2; the
// card then keeps today's plain number and says the Helper is too old.
func machineSummary(record serverDetailedConnectionRecord) *serverMachineSummary {
	status := record.Snapshot.MachineProtection
	if !machineProtectionV2(status) {
		return nil
	}
	summary := &serverMachineSummary{
		Version: status.Version, Enabled: status.Configured.Enabled, Effective: status.Effective, MaxTotal: status.MaxTotal,
		ReleaseBelow: status.ReleaseBelow, Blocking: status.Blocking, WarmingUp: status.WarmingUp, DroppedTotal: status.DroppedTotal,
		Connections: status.Connections, CloseWait: status.CloseWaitSeconds, TCPPorts: len(status.TCPPorts), UDPPorts: len(status.UDPPorts),
		Sources: status.Sources, IdleReason: status.IdleReason, LastError: status.LastError,
	}
	if status.TWBuckets != nil {
		summary.TWBuckets = status.TWBuckets.Target
	}
	return summary
}

// flvxMachineStatus is the server-level `machine` object of the FLVX share
// status (contract machine-protect-v1 §5, FLVX landingcustom.Machine). It is
// omitted for older Helpers and stale reports, which FLVX shows as unknown.
func flvxMachineStatus(record serverDetailedConnectionRecord, exists bool, now time.Time) map[string]any {
	status := record.Snapshot.MachineProtection
	if !exists || now.Sub(record.UpdatedAt) > helperStaleTimeout || !machineProtectionV2(status) || status.Connections == nil {
		return nil
	}
	conns := *status.Connections
	var tw *int64
	if status.TWBuckets != nil {
		tw = status.TWBuckets.Target
	}
	owner := status.Owner
	if owner == "" {
		owner = "mmwx"
	}
	updated := record.Snapshot.SampledAt
	if updated.IsZero() {
		updated = record.UpdatedAt
	}
	return map[string]any{
		"conns": map[string]int64{
			"total": conns.Total, "tcp": conns.TCP, "udp": conns.UDP, "established": conns.Established,
			"time_wait": conns.TimeWait, "close_wait": conns.CloseWait, "other_tcp": conns.OtherTCP,
		},
		"protect": map[string]any{
			"owner": owner, "enabled": status.Configured.Enabled, "max_total": status.MaxTotal, "tw_buckets": tw,
			"close_wait_seconds": status.CloseWaitSeconds, "blocking": status.Blocking, "dropped_total": status.DroppedTotal,
		},
		"updated_at": updated.UTC().Format(time.RFC3339),
	}
}

func (a *app) flvxShareMachine(serverID int64) map[string]any {
	a.connectionMu.Lock()
	record, exists := a.detailedConnections[strconv.FormatInt(serverID, 10)]
	a.connectionMu.Unlock()
	return flvxMachineStatus(record, exists, time.Now())
}
