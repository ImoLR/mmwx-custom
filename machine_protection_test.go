package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func machineTestApp(t *testing.T) (*app, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper-state.json")
	state, err := openHelperState(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return &app{apiToken: "operator-token", helperState: state, helperTokens: map[string]string{"7": "helper-token"},
		adminStore: &fakePersistentConnectionStore{fakeAdminSessionStore: fakeAdminSessionStore{authorized: true, server: true}},
		helperRate: make(map[string]time.Time), connectionMetrics: make(map[string]connectionMetrics), detailedConnections: make(map[string]serverDetailedConnectionRecord)}, path
}

const machineV2Snapshot = `{"system":{"tcp_total":700,"established":214,"time_wait":400,"close_wait":41},"core":{"available":false},
"machine_protection":{"version":2,"owner":"mmwx","supported":true,"configured":{"enabled":true,"max_active":null,"max_total":1000,"tw_buckets":500,"close_wait_seconds":10},
"effective":true,"blocking":true,"active":300,"total":846,"controlled_ports":[443,20001],"tcp_ports":[443,20001],"udp_ports":[20001],
"connections":{"total":846,"tcp":790,"udp":56,"established":214,"time_wait":500,"close_wait":41,"other_tcp":35},
"max_total":1000,"release_below":950,"port_sources":{"core":1,"flvx_shared_core":0,"flvx_file":"fresh","flvx_file_tcp":1,"flvx_file_udp":1},
"dropped_total":12,"tw_buckets":{"target":500,"current":500,"original":262144},"close_wait_seconds":10}}`

const machineV1Snapshot = `{"system":{"tcp_total":10},"core":{"available":false},"machine_protection":{"supported":true,"configured":{"enabled":false,"max_active":null,"max_total":null},"effective":true,"blocking":false,"active":1,"total":10,"controlled_ports":[]}}`

func reportMachine(t *testing.T, a *app, version, snapshot string) serverConnectionSettings {
	t.Helper()
	a.helperRate = make(map[string]time.Time)
	body := `{"server_id":"7","helper_version":"` + version + `","tcp_count":790,"udp_count":56,"connection_count":846,"snapshot":` + snapshot + `}`
	r := httptest.NewRequest(http.MethodPost, "/api/custom/agent/connections", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer helper-token")
	w := httptest.NewRecorder()
	a.helperDetailedConnectionsHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("Helper report: %d %s", w.Code, w.Body.String())
	}
	var reply struct {
		Settings json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	var settings serverConnectionSettings
	if err := json.Unmarshal(reply.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestMachineProtectionDefaultsReachV2HelpersOnce(t *testing.T) {
	a, path := machineTestApp(t)
	// An old Helper never receives the defaults (no canary bypass on release).
	if got := reportMachine(t, a, "v0.7.8", machineV1Snapshot); got.MachineProtection != nil {
		t.Fatalf("old Helper got machine protection: %#v", got.MachineProtection)
	}
	if got := a.helperState.connectionSettings("7").MachineProtection; got != nil {
		t.Fatalf("defaults stored before a v2 Helper reported: %#v", got)
	}
	got := reportMachine(t, a, "v0.7.9", machineV2Snapshot)
	want := defaultMachineProtectionSettings()
	if !reflect.DeepEqual(got.MachineProtection, want) {
		t.Fatalf("first v2 report: %#v, want %#v", got.MachineProtection, want)
	}
	// Persisted, and an admin's later choice is never overwritten.
	reloaded, err := openHelperState(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if stored := reloaded.connectionSettings("7").MachineProtection; !reflect.DeepEqual(stored, want) {
		t.Fatalf("defaults not persisted: %#v", stored)
	}
	off := a.helperState.connectionSettings("7")
	off.MachineProtection = &serverMachineProtectionSettings{Enabled: false, MaxTotal: int64Pointer(1000), TWBuckets: int64Pointer(500), CloseWaitSeconds: int64Pointer(10)}
	if err := a.helperState.setConnectionSettings("7", off); err != nil {
		t.Fatal(err)
	}
	if got = reportMachine(t, a, "v0.7.9", machineV2Snapshot); got.MachineProtection == nil || got.MachineProtection.Enabled {
		t.Fatalf("admin disable was overwritten: %#v", got.MachineProtection)
	}
	// Even a fully cleared block stays cleared once the migration ran.
	off.MachineProtection = nil
	_ = a.helperState.setConnectionSettings("7", off)
	if got = reportMachine(t, a, "v0.7.9", machineV2Snapshot); got.MachineProtection != nil {
		t.Fatalf("migration ran twice: %#v", got.MachineProtection)
	}
}

func TestMachineProtectionDefaultsRespectExistingChoiceAndKillSwitch(t *testing.T) {
	a, _ := machineTestApp(t)
	custom := defaultServerConnectionSettings()
	custom.MachineProtection = &serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1500)}
	_ = a.helperState.setConnectionSettings("7", custom)
	if got := reportMachine(t, a, "v0.7.9", machineV2Snapshot); got.MachineProtection.MaxTotal == nil || *got.MachineProtection.MaxTotal != 1500 || got.MachineProtection.TWBuckets != nil {
		t.Fatalf("existing choice replaced: %#v", got.MachineProtection)
	}

	b, _ := machineTestApp(t)
	// The old settings form saved an empty disabled block: still unconfigured.
	empty := defaultServerConnectionSettings()
	empty.MachineProtection = &serverMachineProtectionSettings{}
	_ = b.helperState.setConnectionSettings("7", empty)
	t.Setenv("MMWXC_MACHINE_PROTECTION_DEFAULTS", "off")
	if got := reportMachine(t, b, "v0.7.9", machineV2Snapshot); got.MachineProtection.Enabled {
		t.Fatalf("kill switch ignored: %#v", got.MachineProtection)
	}
	t.Setenv("MMWXC_MACHINE_PROTECTION_DEFAULTS", "")
	if got := reportMachine(t, b, "v0.7.9", machineV2Snapshot); !reflect.DeepEqual(got.MachineProtection, defaultMachineProtectionSettings()) {
		t.Fatalf("empty form block must take the defaults: %#v", got.MachineProtection)
	}
}

func TestMachineProtectionNewFieldsOnlyReachV2Helpers(t *testing.T) {
	settings := defaultServerConnectionSettings()
	settings.MachineProtection = defaultMachineProtectionSettings()
	old := settingsForHelperMachine(settings, "v0.7.8", false)
	if old.MachineProtection == nil || !old.MachineProtection.Enabled || old.MachineProtection.TWBuckets != nil || old.MachineProtection.CloseWaitSeconds != nil {
		t.Fatalf("v1 Helper settings: %#v", old.MachineProtection)
	}
	raw, _ := json.Marshal(old)
	if bytes.Contains(raw, []byte("tw_buckets")) || bytes.Contains(raw, []byte("close_wait_seconds")) {
		t.Fatalf("unsupported fields sent to an old Helper: %s", raw)
	}
	if v2 := settingsForHelperMachine(settings, "v0.7.9", true); !reflect.DeepEqual(v2.MachineProtection, settings.MachineProtection) {
		t.Fatalf("v2 Helper settings: %#v", v2.MachineProtection)
	}
	if ancient := settingsForHelperMachine(settings, "v0.6.5", true); ancient.MachineProtection != nil {
		t.Fatal("pre-0.6.6 Helper got machine protection")
	}
	// The stored settings themselves keep every field.
	if settings.MachineProtection.TWBuckets == nil {
		t.Fatal("stripping mutated the stored settings")
	}
}

func TestValidateMachineProtectionRanges(t *testing.T) {
	for _, item := range []struct {
		settings serverMachineProtectionSettings
		ok       bool
	}{
		{*defaultMachineProtectionSettings(), true},
		{serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1000), TWBuckets: int64Pointer(0), CloseWaitSeconds: int64Pointer(0)}, true},
		{serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1000), TWBuckets: int64Pointer(99)}, false},
		{serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1000), TWBuckets: int64Pointer(2000001)}, false},
		{serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1000), CloseWaitSeconds: int64Pointer(-1)}, false},
		{serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1000), CloseWaitSeconds: int64Pointer(3601)}, false},
		{serverMachineProtectionSettings{Enabled: true}, false},
	} {
		settings := defaultServerConnectionSettings()
		machine := item.settings
		settings.MachineProtection = &machine
		if err := validateServerConnectionSettings(settings); (err == nil) != item.ok {
			t.Fatalf("%#v: err=%v", item.settings, err)
		}
	}
}

func TestMachineSummaryReachesTheServerCard(t *testing.T) {
	a, _ := machineTestApp(t)
	reportMachine(t, a, "v0.7.9", machineV2Snapshot)
	r := httptest.NewRequest(http.MethodGet, "/api/custom/connections", nil)
	r.Header.Set("Authorization", "Bearer operator-token")
	w := httptest.NewRecorder()
	a.listConnectionMetrics(w, r)
	var response struct {
		Metrics map[string]connectionMetricsView `json:"metrics"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	view := response.Metrics["7"]
	machine := view.Machine
	if view.ConnectionCount != 846 || machine == nil || machine.MaxTotal == nil || *machine.MaxTotal != 1000 || !machine.Blocking || machine.DroppedTotal != 12 ||
		machine.Connections == nil || machine.Connections.TimeWait != 500 || machine.Connections.UDP != 56 || machine.TCPPorts != 2 || machine.UDPPorts != 1 ||
		machine.Sources == nil || machine.Sources.FLVXFile != "fresh" || machine.TWBuckets == nil || *machine.TWBuckets != 500 || machine.CloseWait == nil || *machine.CloseWait != 10 {
		t.Fatalf("card summary: %#v / %#v", view, machine)
	}
	reportMachine(t, a, "v0.7.8", machineV1Snapshot)
	w = httptest.NewRecorder()
	a.listConnectionMetrics(w, r)
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if response.Metrics["7"].Machine != nil {
		t.Fatal("old Helper must not get a machine summary")
	}
}

// Verbatim shapes of FLVX go-backend/internal/landingcustom/client.go
// (branch feature/3.0.27-conn-protect) that read Custom's `machine` object.
type flvxMachineConns struct {
	Total       int64 `json:"total"`
	TCP         int64 `json:"tcp"`
	UDP         int64 `json:"udp"`
	Established int64 `json:"established"`
	TimeWait    int64 `json:"time_wait"`
	CloseWait   int64 `json:"close_wait"`
	OtherTCP    int64 `json:"other_tcp"`
}

type flvxMachineProtect struct {
	Owner            string `json:"owner"`
	Enabled          bool   `json:"enabled"`
	MaxTotal         *int64 `json:"max_total"`
	TWBuckets        *int64 `json:"tw_buckets"`
	CloseWaitSeconds *int64 `json:"close_wait_seconds"`
	Blocking         bool   `json:"blocking"`
	DroppedTotal     int64  `json:"dropped_total"`
}

type flvxMachine struct {
	Conns     *flvxMachineConns   `json:"conns,omitempty"`
	Protect   *flvxMachineProtect `json:"protect,omitempty"`
	UpdatedAt string              `json:"updated_at,omitempty"`
}

func TestFLVXShareStatusMachineMatchesFLVXContract(t *testing.T) {
	a, _ := machineTestApp(t)
	if a.flvxShareMachine(7) != nil {
		t.Fatal("machine object without any Helper report")
	}
	reportMachine(t, a, "v0.7.9", machineV2Snapshot)
	object := a.flvxShareMachine(7)
	raw, err := json.Marshal(map[string]any{"applied_revision": 1, "machine": object})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Machine *flvxMachine `json:"machine"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&decoded); err != nil || decoded.Machine == nil {
		t.Fatalf("%v %s", err, raw)
	}
	machine := decoded.Machine
	wantConns := flvxMachineConns{Total: 846, TCP: 790, UDP: 56, Established: 214, TimeWait: 500, CloseWait: 41, OtherTCP: 35}
	if machine.Conns == nil || *machine.Conns != wantConns {
		t.Fatalf("conns = %#v", machine.Conns)
	}
	protect := machine.Protect
	if protect == nil || protect.Owner != "mmwx" || !protect.Enabled || protect.MaxTotal == nil || *protect.MaxTotal != 1000 || protect.TWBuckets == nil || *protect.TWBuckets != 500 ||
		protect.CloseWaitSeconds == nil || *protect.CloseWaitSeconds != 10 || !protect.Blocking || protect.DroppedTotal != 12 {
		t.Fatalf("protect = %#v", protect)
	}
	if _, err := time.Parse(time.RFC3339, machine.UpdatedAt); err != nil {
		t.Fatalf("updated_at %q: %v", machine.UpdatedAt, err)
	}
	// Exactly the FLVX keys: nothing renamed, nothing extra.
	var keys map[string]map[string]json.RawMessage
	_ = json.Unmarshal(mustJSON(t, object), &keys)
	for name, want := range map[string]int{"conns": 7, "protect": 7} {
		if len(keys[name]) != want {
			t.Fatalf("%s keys = %v", name, keys[name])
		}
	}

	record := a.detailedConnections["7"]
	record.UpdatedAt = time.Now().Add(-helperStaleTimeout - time.Second)
	if flvxMachineStatus(record, true, time.Now()) != nil {
		t.Fatal("stale report must be omitted (FLVX shows unknown)")
	}
	reportMachine(t, a, "v0.7.8", machineV1Snapshot)
	if a.flvxShareMachine(7) != nil {
		t.Fatal("old Helper must not produce a machine object")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMachineProtectionV2WithoutNftCanStillBeEnabled(t *testing.T) {
	a, _ := machineTestApp(t)
	reportMachine(t, a, "v0.7.9", strings.Replace(machineV2Snapshot, `"supported":true`, `"supported":false`, 1))
	settings := defaultServerConnectionSettings()
	settings.MachineProtection = &serverMachineProtectionSettings{Enabled: true, MaxTotal: int64Pointer(1000), TWBuckets: int64Pointer(500)}
	body, _ := json.Marshal(settings)
	r := httptest.NewRequest(http.MethodPut, "/api/custom/servers/7/connections", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer operator-token")
	w := httptest.NewRecorder()
	a.serverConnectionsHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("v2 Helper without nftables: %d %s", w.Code, w.Body.String())
	}
	reportMachine(t, a, "v0.7.8", strings.Replace(machineV1Snapshot, `"supported":true`, `"supported":false`, 1))
	r = httptest.NewRequest(http.MethodPut, "/api/custom/servers/7/connections", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer operator-token")
	w = httptest.NewRecorder()
	a.serverConnectionsHandler(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("old unsupported Helper must still be refused: %d", w.Code)
	}
}
