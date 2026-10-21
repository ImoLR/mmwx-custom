package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// closeWaitMergeFixture is shared with cmd/mmwxc-helper/close_wait_merge_test.go,
// which feeds these exact payloads to the real Helper code and checks the
// CLOSE_WAIT each Core ends up with. MMWXC_UPDATE_FIXTURES=1 rewrites it.
const closeWaitMergeFixture = "testdata/close-wait-merge.json"

// closeWaitMergeCase: Before is what v1.4.69 sent to the Helper for the stored
// settings, After what is sent once the CLOSE_WAIT inputs are merged. Want*
// are the Core default CLOSE_WAIT seconds (null = off) a v2 Helper applies.
type closeWaitMergeCase struct {
	Name           string          `json:"name"`
	HelperVersion  string          `json:"helper_version"`
	MachineV2      bool            `json:"machine_v2"`
	Before         json.RawMessage `json:"before"`
	After          json.RawMessage `json:"after"`
	WantCoreBefore *int64          `json:"want_core_before"`
	WantCoreAfter  *int64          `json:"want_core_after"`
	WantFLVXAfter  *int64          `json:"want_flvx_after"`
	Note           string          `json:"note,omitempty"`
}

func closeWaitMergeCases(t *testing.T) []closeWaitMergeCase {
	t.Helper()
	on := func(closeWait *int64) *serverMachineProtectionSettings {
		return &serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1000), TWBuckets: int64Pointer(500), CloseWaitSeconds: closeWait}
	}
	off := &serverMachineProtectionSettings{Enabled: false, MaxTotal: int64Pointer(1000), TWBuckets: int64Pointer(500), CloseWaitSeconds: int64Pointer(10)}
	user := serverUserConnectionSettings{Identity: serverConnectionIdentity{InboundTag: "in-a", User: "alice"}, CloseWaitTimeoutSeconds: int64Pointer(5)}
	items := []struct {
		name, version string
		v2            bool
		legacy        *int64
		machine       *serverMachineProtectionSettings
		users         []serverUserConnectionSettings
		before, after *int64
		flvx          *int64
		noteContains  string
	}{
		{name: "production today: protection on, 10 s, no old default", version: "v0.7.9", v2: true, machine: on(int64Pointer(10)), before: int64Pointer(10), after: int64Pointer(10), flvx: int64Pointer(10)},
		{name: "per-user override is kept", version: "v0.7.9", v2: true, machine: on(int64Pointer(10)), users: []serverUserConnectionSettings{user}, before: int64Pointer(10), after: int64Pointer(10), flvx: int64Pointer(10)},
		{name: "old default 30 was winning, stays 30", version: "v0.7.9", v2: true, legacy: int64Pointer(30), machine: on(int64Pointer(10)), before: int64Pointer(30), after: int64Pointer(30), flvx: int64Pointer(30), noteContains: "machine protection had 10 s"},
		{name: "old default 30 over the implicit 10", version: "v0.7.9", v2: true, legacy: int64Pointer(30), machine: on(nil), before: int64Pointer(30), after: int64Pointer(30), flvx: int64Pointer(30), noteContains: "had 10 s"},
		{name: "old default 30, machine CLOSE_WAIT off", version: "v0.7.9", v2: true, legacy: int64Pointer(30), machine: on(int64Pointer(0)), before: int64Pointer(30), after: int64Pointer(30), flvx: int64Pointer(30), noteContains: "had none"},
		{name: "old default equal to machine value", version: "v0.7.9", v2: true, legacy: int64Pointer(10), machine: on(int64Pointer(10)), before: int64Pointer(10), after: int64Pointer(10), flvx: int64Pointer(10)},
		{name: "old default 0 never took effect", version: "v0.7.9", v2: true, legacy: int64Pointer(0), machine: on(int64Pointer(10)), before: int64Pointer(10), after: int64Pointer(10), flvx: int64Pointer(10)},
		{name: "protection off, no old default", version: "v0.7.9", v2: true, machine: off},
		// Edge (no production server is in it): protection off kept the old
		// default on the mmwx Core; merged, it waits for protection to be on.
		{name: "protection off with old default 30", version: "v0.7.9", v2: true, legacy: int64Pointer(30), machine: off, before: int64Pointer(30), noteContains: "applies once protection is on"},
		// v1 Helpers never get close_wait_seconds and now get a null old default,
		// so an old default stops there until the Helper is upgraded (edge, no
		// production server: all run v2 Helpers).
		{name: "v1 Helper, no old default", version: "v0.7.8", machine: on(int64Pointer(10))},
		{name: "v1 Helper, old default 30", version: "v0.7.8", legacy: int64Pointer(30), machine: on(int64Pointer(10)), noteContains: "had 10 s"},
	}
	cases := make([]closeWaitMergeCase, 0, len(items))
	for _, item := range items {
		stored := defaultServerConnectionSettings()
		stored.DefaultCloseWaitTimeoutSeconds = item.legacy
		stored.MachineProtection = item.machine
		if item.users != nil {
			stored.Users = item.users
		}
		merged := cloneServerConnectionSettings(stored)
		note, _ := mergeLegacyCloseWait(&merged)
		if item.noteContains == "" && note != "" || !strings.Contains(note, item.noteContains) {
			t.Fatalf("%s: note %q, want %q", item.name, note, item.noteContains)
		}
		if merged.DefaultCloseWaitTimeoutSeconds != nil {
			t.Fatalf("%s: old default survived the merge", item.name)
		}
		if err := validateServerConnectionSettings(merged); err != nil {
			t.Fatalf("%s: merged settings invalid: %v", item.name, err)
		}
		// settingsForHelperMachine is unchanged from v1.4.69, so the unmerged
		// settings give exactly what v1.4.69 sent.
		before := mustJSON(t, sortedConnectionSettings(settingsForHelperMachine(stored, item.version, item.v2)))
		after := mustJSON(t, sortedConnectionSettings(settingsForHelperMachine(merged, item.version, item.v2)))
		cases = append(cases, closeWaitMergeCase{Name: item.name, HelperVersion: item.version, MachineV2: item.v2, Before: before, After: after,
			WantCoreBefore: item.before, WantCoreAfter: item.after, WantFLVXAfter: item.flvx, Note: note})
	}
	return cases
}

func TestCloseWaitMergeHelperPayloads(t *testing.T) {
	cases := closeWaitMergeCases(t)
	for _, item := range cases {
		var after map[string]any
		if err := json.Unmarshal(item.After, &after); err != nil {
			t.Fatal(err)
		}
		// Every Helper still gets the field it knows, always null now.
		if value, ok := after["default_close_wait_timeout_seconds"]; !ok || value != nil {
			t.Fatalf("%s: default_close_wait_timeout_seconds = %v, %v", item.Name, value, ok)
		}
		if !item.MachineV2 && bytes.Contains(item.After, []byte(`"close_wait_seconds"`)) {
			t.Fatalf("%s: v1 Helper got close_wait_seconds: %s", item.Name, item.After)
		}
	}
	want, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("MMWXC_UPDATE_FIXTURES") == "1" {
		if err := os.WriteFile(closeWaitMergeFixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(closeWaitMergeFixture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date; rerun with MMWXC_UPDATE_FIXTURES=1", closeWaitMergeFixture)
	}
}

func TestCloseWaitMergeOnLoad(t *testing.T) {
	a, path := machineTestApp(t)
	enabled := &serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1000), TWBuckets: int64Pointer(500), CloseWaitSeconds: int64Pointer(10)}
	state := a.helperState
	prod := defaultServerConnectionSettings()
	prod.MachineProtection = enabled
	prod.Users = []serverUserConnectionSettings{{Identity: serverConnectionIdentity{InboundTag: "in-a", User: "alice"}, CloseWaitTimeoutSeconds: int64Pointer(5)}}
	winning := cloneServerConnectionSettings(prod)
	winning.DefaultCloseWaitTimeoutSeconds = int64Pointer(30)
	// Server 7 never had a v2 Helper: no machine block, defaults still due.
	pending := defaultServerConnectionSettings()
	pending.DefaultCloseWaitTimeoutSeconds = int64Pointer(45)
	state.data.ConnectionSettings = map[string]serverConnectionSettings{"1": prod, "3": winning, "7": pending}
	state.mu.Lock()
	if err := state.saveLocked(); err != nil {
		t.Fatal(err)
	}
	state.mu.Unlock()

	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	reloaded, err := openHelperState(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.connectionSettings("1"), cloneServerConnectionSettings(prod)) {
		t.Fatalf("untouched server changed: %#v", reloaded.connectionSettings("1"))
	}
	three := reloaded.connectionSettings("3")
	if three.DefaultCloseWaitTimeoutSeconds != nil || three.MachineProtection.CloseWaitSeconds == nil || *three.MachineProtection.CloseWaitSeconds != 30 ||
		!three.MachineProtection.Enabled || *three.MachineProtection.MaxTotal != 1000 || *three.Users[0].CloseWaitTimeoutSeconds != 5 {
		t.Fatalf("server 3 merge: %#v / %#v", three, three.MachineProtection)
	}
	seven := reloaded.connectionSettings("7")
	if seven.MachineProtection == nil || seven.MachineProtection.Enabled || *seven.MachineProtection.CloseWaitSeconds != 45 || !unconfiguredMachineProtection(seven.MachineProtection) {
		t.Fatalf("server 7 merge: %#v", seven.MachineProtection)
	}
	text := logs.String()
	if !strings.Contains(text, "CLOSE_WAIT merged server=3: old server default 30 s kept") || !strings.Contains(text, "CLOSE_WAIT merged server=7") || strings.Contains(text, "server=1:") {
		t.Fatalf("merge log: %s", text)
	}
	// Persisted at once: no old default is left on disk, so it runs only once.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var disk struct {
		ConnectionSettings map[string]map[string]json.RawMessage `json:"connection_settings"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	for id, settings := range disk.ConnectionSettings {
		if value := string(settings["default_close_wait_timeout_seconds"]); value != "null" {
			t.Fatalf("server %s still stores the old default: %s", id, value)
		}
	}
	logs.Reset()
	if _, err := openHelperState(path, 0); err != nil || logs.Len() != 0 {
		t.Fatalf("second start merged again: %v %s", err, logs.String())
	}

	// The first v2 report still turns protection on, keeping the merged value.
	a.helperState = reloaded
	got := reportMachine(t, a, "v0.7.9", machineV2Snapshot)
	want := defaultMachineProtectionSettings()
	want.CloseWaitSeconds = int64Pointer(45)
	if !reflect.DeepEqual(got.MachineProtection, want) || got.DefaultCloseWaitTimeoutSeconds != nil {
		t.Fatalf("defaults after merge: %#v", got.MachineProtection)
	}
}

func TestCloseWaitMergeOnOldFormSave(t *testing.T) {
	a, _ := machineTestApp(t)
	reportMachine(t, a, "v0.7.9", machineV2Snapshot)
	// The v1.4.69 page sends both inputs; the old one was the one in effect.
	body := `{"machine_protection":{"enabled":true,"max_active":null,"max_total":1000,"tw_buckets":500,"close_wait_seconds":10},
"default_close_wait_timeout_seconds":20,"online_ip_grace_period_seconds":30,"global_total_limit_enabled":false,
"max_global_total_connections":null,"max_global_inbound_connections":null,"users":[],"ports":[],"management_users":[]}`
	r := httptest.NewRequest(http.MethodPut, "/api/custom/servers/7/connections", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer operator-token")
	w := httptest.NewRecorder()
	a.serverConnectionsHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("old form save: %d %s", w.Code, w.Body.String())
	}
	var reply struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if value := string(reply.Settings["default_close_wait_timeout_seconds"]); value != "null" {
		t.Fatalf("response still carries the old default: %s", value)
	}
	stored := a.helperState.connectionSettings("7")
	if stored.DefaultCloseWaitTimeoutSeconds != nil || *stored.MachineProtection.CloseWaitSeconds != 20 {
		t.Fatalf("old form save stored %#v", stored.MachineProtection)
	}
	if got := reportMachine(t, a, "v0.7.9", machineV2Snapshot); *got.MachineProtection.CloseWaitSeconds != 20 || got.DefaultCloseWaitTimeoutSeconds != nil {
		t.Fatalf("Helper receives %#v", got.MachineProtection)
	}
}
