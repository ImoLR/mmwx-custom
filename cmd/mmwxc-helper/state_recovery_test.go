package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryTestPaths(t *testing.T) stateRecoveryPaths {
	t.Helper()
	root := t.TempDir()
	return stateRecoveryPaths{
		dropIn: filepath.Join(root, "owner.conf"), image: filepath.Join(root, "protected-xray"),
		core: filepath.Join(root, "core-xray"), lastGoodConfig: filepath.Join(root, "config.last-good.json"),
		backupRoot: filepath.Join(root, "rollback"), machineID: filepath.Join(root, "machine-id"),
	}
}

func writeRecoveryFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func completeRecoveryEvidence(t *testing.T, paths stateRecoveryPaths) string {
	t.Helper()
	writeRecoveryFixture(t, paths.dropIn, []byte(ownershipDropIn))
	writeRecoveryFixture(t, paths.image, []byte("same-core"))
	writeRecoveryFixture(t, paths.core, []byte("same-core"))
	writeRecoveryFixture(t, paths.lastGoodConfig, []byte(`{"inbounds":[],"outbounds":[]}`))
	writeRecoveryFixture(t, paths.machineID, []byte("existing-machine-id\n"))
	oldBackup := filepath.Join(paths.backupRoot, "old")
	newBackup := filepath.Join(paths.backupRoot, "new")
	for _, dir := range []string{oldBackup, newBackup} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldBackup, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	return newBackup
}

func TestLocalStateCorruptPrimaryLoadsBackup(t *testing.T) {
	for name, bad := range map[string][]byte{
		"NUL": bytes.Repeat([]byte{0}, 4568), "truncated": []byte(`{"settings":`),
		"empty": {}, "null": []byte("null"), "missing": nil,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if bad != nil {
				writeRecoveryFixture(t, path, bad)
			}
			backup := []byte(`{"settings":{"online_ip_grace_period_seconds":45},"machine_id":"backup-machine"}`)
			writeRecoveryFixture(t, path+".bak", backup)
			state, recovery, err := loadLocalStateWithRecovery(path, recoveryTestPaths(t))
			if err != nil || state.MachineID != "backup-machine" || state.Settings.OnlineIPGracePeriodSeconds != 45 || !strings.Contains(recovery, "last-good backup") {
				t.Fatalf("backup recovery: state=%+v recovery=%q err=%v", state, recovery, err)
			}
			data, err := os.ReadFile(path + ".bak")
			if err != nil || !bytes.Equal(data, backup) {
				t.Fatalf("good backup was overwritten: data=%q err=%v", data, err)
			}
			if _, err := readLocalState(path); err != nil {
				t.Fatalf("primary was not restored: %v", err)
			}
			if err := saveLocalState(path, state); err != nil {
				t.Fatal(err)
			}
			if _, err := readLocalState(path + ".bak"); err != nil {
				t.Fatalf("next save corrupted backup: %v", err)
			}
			quarantined, err := filepath.Glob(path + ".corrupt-*")
			want := 1
			if bad == nil {
				want = 0
			}
			if err != nil || len(quarantined) != want {
				t.Fatalf("quarantine files=%v err=%v; want %d", quarantined, err, want)
			}
		})
	}
}

func TestLocalStateBothBadReconstructsOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	paths := recoveryTestPaths(t)
	newestBackup := completeRecoveryEvidence(t, paths)
	writeRecoveryFixture(t, path, bytes.Repeat([]byte{0}, 4568))
	writeRecoveryFixture(t, path+".bak", []byte(`{"truncated"`))
	state, recovery, err := loadLocalStateWithRecovery(path, paths)
	if err != nil || !strings.Contains(recovery, "reconstructed external ownership") {
		t.Fatalf("recovery=%q err=%v", recovery, err)
	}
	coreHash, _ := sha256File(paths.core)
	configHash, _ := hashValidJSONFile(paths.lastGoodConfig)
	if !state.ExternalOwnership.Prepared || !state.ExternalOwnership.Enabled || state.ExternalOwnership.Armed ||
		state.ExternalOwnership.ExpectedCoreSHA != coreHash || state.ExternalOwnership.LastGoodConfigSHA != configHash ||
		state.ExternalOwnership.BackupDir != newestBackup || state.Takeover.Status != "completed" ||
		state.MachineID != "existing-machine-id" || state.Settings.OnlineIPGracePeriodSeconds != 30 {
		t.Fatalf("unexpected recovered state: %+v", state)
	}
	if _, err := readLocalState(path); err != nil {
		t.Fatalf("recovered state not persisted: %v", err)
	}
	quarantined, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(quarantined) != 1 || !strings.HasSuffix(quarantined[0], "Z") {
		t.Fatalf("expected UTC quarantine: files=%v err=%v", quarantined, err)
	}
}

func TestLocalStateBothBadLeavesInconsistentOwnershipDisabled(t *testing.T) {
	for _, name := range []string{"missing drop-in", "changed drop-in", "missing core", "mismatched core", "missing last-good config", "invalid last-good config", "missing rollback"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			paths := recoveryTestPaths(t)
			completeRecoveryEvidence(t, paths)
			switch name {
			case "missing drop-in":
				_ = os.Remove(paths.dropIn)
			case "changed drop-in":
				writeRecoveryFixture(t, paths.dropIn, []byte("unrelated service"))
			case "missing core":
				_ = os.Remove(paths.core)
			case "mismatched core":
				writeRecoveryFixture(t, paths.core, []byte("different-core"))
			case "missing last-good config":
				_ = os.Remove(paths.lastGoodConfig)
			case "invalid last-good config":
				writeRecoveryFixture(t, paths.lastGoodConfig, []byte(`{"inbounds":`))
			case "missing rollback":
				_ = os.RemoveAll(paths.backupRoot)
			}
			writeRecoveryFixture(t, path, []byte{})
			writeRecoveryFixture(t, path+".bak", []byte{0})
			state, recovery, err := loadLocalStateWithRecovery(path, paths)
			if err != nil || state.ExternalOwnership.Enabled || state.ExternalOwnership.Prepared || !strings.Contains(recovery, "ownership disabled") {
				t.Fatalf("inconsistent evidence enabled ownership: state=%+v recovery=%q err=%v", state, recovery, err)
			}
		})
	}
}

func TestLocalStateQuarantineKeepsTwo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	for _, timestamp := range []string{"20000101T000001.000000000Z", "20000101T000002.000000000Z", "20000101T000003.000000000Z"} {
		writeRecoveryFixture(t, path+".corrupt-"+timestamp, []byte{0})
	}
	writeRecoveryFixture(t, path, []byte{0})
	if _, _, err := loadLocalStateWithRecovery(path, recoveryTestPaths(t)); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(files) != 2 {
		t.Fatalf("quarantine files=%v err=%v", files, err)
	}
	if _, err := os.Stat(path + ".corrupt-20000101T000003.000000000Z"); err != nil {
		t.Fatalf("newest existing quarantine removed: %v", err)
	}
}

func TestLocalStateRecoveryResumesAfterQuarantine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	paths := recoveryTestPaths(t)
	completeRecoveryEvidence(t, paths)
	writeRecoveryFixture(t, path+".corrupt-20000101T000003.000000000Z", []byte{0})
	state, recovery, err := loadLocalStateWithRecovery(path, paths)
	if err != nil || !state.ExternalOwnership.Enabled || !strings.Contains(recovery, "reconstructed external ownership") {
		t.Fatalf("interrupted recovery lost ownership: state=%+v recovery=%q err=%v", state, recovery, err)
	}
}

func TestLocalStateUnreadablePrimaryLoadsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	writeRecoveryFixture(t, path+".bak", []byte(`{"machine_id":"backup-machine"}`))
	state, _, err := loadLocalStateWithRecovery(path, recoveryTestPaths(t))
	if err != nil || state.MachineID != "backup-machine" {
		t.Fatalf("unreadable primary: state=%+v err=%v", state, err)
	}
}

func TestRecoveredOwnershipReconcileDoesNotRestartCore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	paths := recoveryTestPaths(t)
	completeRecoveryEvidence(t, paths)
	writeRecoveryFixture(t, path, []byte{0})
	writeRecoveryFixture(t, path+".bak", []byte{})
	state, _, err := loadLocalStateWithRecovery(path, paths)
	if err != nil || !state.ExternalOwnership.Enabled {
		t.Fatalf("recovery: state=%+v err=%v", state, err)
	}
	root := filepath.Dir(paths.core)
	base := filepath.Join(root, "xray.service")
	officialBinary := filepath.Join(root, "official-xray")
	config := filepath.Join(root, "config.json")
	writeRecoveryFixture(t, base, []byte(ownershipBaseUnit))
	writeRecoveryFixture(t, config, []byte(`{"inbounds":[],"outbounds":[]}`))
	if err := os.Symlink(paths.core, officialBinary); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(paths.core)
	if err != nil {
		t.Fatal(err)
	}
	dependencies := ownershipReconcileDependencies{
		core: paths.core, image: paths.image, config: config, configBackup: paths.lastGoodConfig,
		baseUnit: base, dropIn: paths.dropIn, officialBinary: officialBinary,
		run: func(context.Context, ...string) error {
			t.Error("matching recovered ownership must not call systemctl")
			return errors.New("unexpected systemctl")
		},
		active: func(context.Context, string) bool { return false },
		status: func(context.Context, *externalOwnershipState) externalOwnershipStatus {
			return externalOwnershipStatus{ServiceActive: true, RuntimeOwned: true, SingleCore: true, CoreReady: true}
		},
		validateConfig: func(context.Context) (string, error) {
			t.Error("matching config must not run Core validation")
			return "", errors.New("unexpected validation")
		},
		waitReady: func(context.Context, time.Duration, ...func()) error {
			t.Error("matching recovered ownership must not wait for a restart")
			return errors.New("unexpected wait")
		},
	}
	if err := reconcileExternalOwnershipWith(context.Background(), &state.ExternalOwnership, dependencies); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(paths.core)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) ||
		!state.ExternalOwnership.LastRepairAt.IsZero() || state.ExternalOwnership.LastRepairReason != "" {
		t.Fatalf("matching recovered ownership was repaired: state=%+v err=%v", state.ExternalOwnership, err)
	}
}
