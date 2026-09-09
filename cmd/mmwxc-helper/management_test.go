package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestControlOfficialXrayUsesPersistentSystemdActions(t *testing.T) {
	var calls [][]string
	active := true
	run := func(_ context.Context, args ...string) error {
		calls = append(calls, append([]string(nil), args...))
		if len(args) > 0 && args[0] == "disable" {
			active = false
		}
		if len(args) > 0 && args[0] == "enable" {
			active = true
		}
		return nil
	}
	probe := func(_ context.Context, service string) bool {
		if service != "xray.service" {
			t.Fatalf("unexpected service probe: %s", service)
		}
		return active
	}
	if err := controlOfficialXray(context.Background(), "stop", run, probe); err != nil {
		t.Fatal(err)
	}
	if err := controlOfficialXray(context.Background(), "start", run, probe); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"disable", "--now", "xray.service"}, {"enable", "--now", "xray.service"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("systemctl calls = %#v, want %#v", calls, want)
	}
}

func signedTestCommand(t *testing.T, token, action string, payload json.RawMessage) managementCommand {
	t.Helper()
	now := time.Now().UTC()
	command := managementCommand{ID: "command-1", Action: action, Payload: payload, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	signature, err := managementMAC(helperTokenHash(token), commandSigningBytes(command))
	if err != nil {
		t.Fatal(err)
	}
	command.Signature = signature
	return command
}

func TestVerifyManagementCommand(t *testing.T) {
	command := signedTestCommand(t, "token", "core.status", nil)
	if err := verifyManagementCommand(command, "token", time.Now()); err != nil {
		t.Fatal(err)
	}
	tampered := command
	tampered.Action = "core.restart"
	if err := verifyManagementCommand(tampered, "token", time.Now()); err == nil {
		t.Fatal("tampered action was accepted")
	}
	if err := verifyManagementCommand(command, "wrong-token", time.Now()); err == nil {
		t.Fatal("wrong token was accepted")
	}
	expired := signedTestCommand(t, "token", "core.status", nil)
	expired.CreatedAt = time.Now().Add(-2 * time.Hour)
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	expired.Signature, _ = managementMAC(helperTokenHash("token"), commandSigningBytes(expired))
	if err := verifyManagementCommand(expired, "token", time.Now()); err == nil {
		t.Fatal("expired command was accepted")
	}
}

func TestCompletedCommandReplayWindow(t *testing.T) {
	state := localState{}
	for index := 0; index < completedCommandMax+5; index++ {
		rememberCompletedCommand(&state, string(rune('a'+index)))
	}
	if len(state.CompletedCommandIDs) != completedCommandMax {
		t.Fatalf("unexpected replay window size: %d", len(state.CompletedCommandIDs))
	}
	last := state.CompletedCommandIDs[len(state.CompletedCommandIDs)-1]
	rememberCompletedCommand(&state, last)
	if len(state.CompletedCommandIDs) != completedCommandMax {
		t.Fatal("duplicate command id changed the replay window")
	}
}

func TestManagementResultIsSanitizedBeforeSigning(t *testing.T) {
	result := managementResult{CommandID: "command-1", Action: "core.config.apply", Message: strings.Repeat("x", 600)}
	finalizeManagementResult(&result, "token")
	if len(result.Message) != 512 {
		t.Fatalf("message length = %d, want 512", len(result.Message))
	}
	expected, err := managementMAC(helperTokenHash("token"), resultSigningBytes(result))
	if err != nil {
		t.Fatal(err)
	}
	if result.Signature != expected {
		t.Fatal("sanitized management result signature does not verify")
	}
}

func TestArtifactValidationAndRollbackPruning(t *testing.T) {
	valid := managementArtifact{URL: "https://github.com/ImoLR/mmwx-custom/releases/download/v1.2.0/core", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := validateArtifact(valid); err != nil {
		t.Fatal(err)
	}
	valid.URL = "http://github.com/ImoLR/mmwx-custom/core"
	if err := validateArtifact(valid); err == nil {
		t.Fatal("non-HTTPS artifact was accepted")
	}
	valid.URL = "https://github.com/another/project/releases/download/v1/core"
	if err := validateArtifact(valid); err == nil {
		t.Fatal("artifact from another repository was accepted")
	}
	manager := newLifecycleManager(nil)
	request, _ := http.NewRequest(http.MethodGet, "https://example.com/redirected-core", nil)
	if err := manager.httpClient.CheckRedirect(request, nil); err == nil {
		t.Fatal("redirect to an unapproved host was accepted")
	}
	directory := t.TempDir()
	for _, name := range []string{"01", "02", "03"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneRollback(directory, 2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "02" || entries[1].Name() != "03" {
		t.Fatalf("unexpected rollback files: %#v", entries)
	}
}

func TestCurrentTestBinaryMatchesELFArchitecture(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateELFArchitecture(executable); err != nil {
		t.Fatal(err)
	}
}

func TestCopyRequiredCoreAssets(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte("fixture-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := []byte(`{"routing":{"rules":[{"ip":["geoip:cn"]},{"domain":["geosite:cn"]}]}}`)
	if err := copyRequiredCoreAssets(config, destination, []string{source}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		data, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(data) != "fixture-"+name {
			t.Fatalf("asset %s was not copied exactly: %q err=%v", name, data, err)
		}
	}
}

func TestCopyRequiredCoreAssetsFailsClosed(t *testing.T) {
	config := []byte(`{"routing":{"rules":[{"ip":["geoip:cn"]}]}}`)
	if err := copyRequiredCoreAssets(config, t.TempDir(), []string{t.TempDir()}); err == nil {
		t.Fatal("missing required asset was accepted")
	}
}
