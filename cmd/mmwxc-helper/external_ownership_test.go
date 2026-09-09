package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnershipDropInPinsForkAndOfficialConfig(t *testing.T) {
	for _, required := range []string{
		"ExecStart=\n",
		"ExecStart=/opt/mmwxc/core/xray run -config /usr/local/etc/xray/config.json",
		"MMWXC_CORE_CONTROL_SOCKET=/run/mmwxc/core-control.sock",
		"XRAY_LOCATION_ASSET=/opt/mmwxc/core",
	} {
		if !strings.Contains(ownershipDropIn, required) {
			t.Fatalf("ownership drop-in is missing %q", required)
		}
	}
	if strings.Contains(ownershipDropIn, coreConfigPath) {
		t.Fatalf("ownership drop-in references the legacy Custom config %s", coreConfigPath)
	}
}

func TestEnsureOwnershipFilesReplacesMaskAndPreservesBaseUnit(t *testing.T) {
	directory := t.TempDir()
	base := filepath.Join(directory, "xray.service")
	dropIn := filepath.Join(directory, "xray.service.d", "90-mmwxc-owner.conf")
	if err := os.Symlink("/dev/null", base); err != nil {
		t.Fatal(err)
	}
	baseChanged, dropInChanged, err := ensureOwnershipFilesAt(base, dropIn)
	if err != nil {
		t.Fatal(err)
	}
	if !baseChanged || !dropInChanged || !exactFileContents(base, ownershipBaseUnit) || !exactFileContents(dropIn, ownershipDropIn) {
		t.Fatal("masked service was not replaced with the owned service and drop-in")
	}

	officialUnit := "[Service]\nExecStart=/usr/local/bin/xray run -config /usr/local/etc/xray/config.json\n"
	if err := os.WriteFile(base, []byte(officialUnit), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dropIn, []byte("drifted"), 0644); err != nil {
		t.Fatal(err)
	}
	baseChanged, dropInChanged, err = ensureOwnershipFilesAt(base, dropIn)
	if err != nil {
		t.Fatal(err)
	}
	if baseChanged || !dropInChanged {
		t.Fatalf("unexpected repair flags: base=%t drop-in=%t", baseChanged, dropInChanged)
	}
	data, err := os.ReadFile(base)
	if err != nil || string(data) != officialUnit {
		t.Fatalf("official base unit was overwritten: %q err=%v", data, err)
	}
	if !exactFileContents(dropIn, ownershipDropIn) {
		t.Fatal("drifted ownership drop-in was not restored")
	}
}

func TestHashValidJSONFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"inbounds":[],"outbounds":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := hashValidJSONFile(path)
	if err != nil || len(first) != 64 {
		t.Fatalf("valid config hash = %q, err=%v", first, err)
	}
	if err := os.WriteFile(path, []byte(`{"inbounds":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := hashValidJSONFile(path); err == nil {
		t.Fatal("invalid official config was accepted")
	}
}

func TestInboundPortsFromOfficialConfig(t *testing.T) {
	ports, err := inboundPortsFromConfig([]byte(`{"inbounds":[{"port":10022},{"port":"12968"},{"port":10022},{"port":"invalid"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 2 || ports[0] != 10022 || ports[1] != 12968 {
		t.Fatalf("unexpected inbound ports: %#v", ports)
	}
}

func TestExternalOwnershipStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := localState{ExternalOwnership: externalOwnershipState{
		Prepared: true, Armed: true, Enabled: true, BackupDir: "/rollback/snapshot",
		ExpectedCoreSHA: strings.Repeat("a", 64), LastGoodConfigSHA: strings.Repeat("b", 64),
	}}
	if err := saveLocalState(path, state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadLocalState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.ExternalOwnership.Armed || !reloaded.ExternalOwnership.Enabled || reloaded.ExternalOwnership.BackupDir != state.ExternalOwnership.BackupDir || reloaded.ExternalOwnership.ExpectedCoreSHA != state.ExternalOwnership.ExpectedCoreSHA {
		t.Fatalf("ownership state was not preserved: %#v", reloaded.ExternalOwnership)
	}
}

func TestArmExternalOwnershipStopsBothServicesWithoutStartingXray(t *testing.T) {
	var calls []string
	active := map[string]bool{"xray.service": true, coreServiceName: true}
	run := func(_ context.Context, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		if len(args) >= 2 && args[0] == "stop" && args[1] == "xray.service" {
			active["xray.service"] = false
		}
		if len(args) >= 3 && args[0] == "disable" && args[1] == "--now" && args[2] == coreServiceName {
			active[coreServiceName] = false
		}
		return nil
	}
	probe := func(_ context.Context, service string) bool { return active[service] }
	if err := armExternalOwnershipServices(context.Background(), run, probe); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "|")
	if joined != "stop xray.service|disable --now mmwxc-core.service" {
		t.Fatalf("unexpected handoff sequence: %s", joined)
	}
	if strings.Contains(joined, "start") || strings.Contains(joined, "restart") {
		t.Fatalf("armed handoff must not start xray: %s", joined)
	}
}
