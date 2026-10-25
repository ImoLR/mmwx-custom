package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// The 「Boil HKT 99u」(2) listeners on 2026-10-25 as a v0.7.14 Helper reports them.
const listeningSnapshot = `{"system":{"tcp_total":10},"core":{"available":false},"listening_ports":{"scanned_at":"2026-10-25T02:30:00Z","ports":[
{"port":10016,"protocol":"tcp","pid":231078,"process":"xray","owner":"core","inbound_tag":"自己用 ss-10016"},
{"port":10017,"protocol":"tcp","pid":212883,"process":"lkbk-core"},
{"port":10017,"protocol":"udp","pid":212883,"process":"lkbk-core"},
{"port":0,"protocol":"tcp","pid":1,"process":"bad-port"},
{"port":80,"protocol":"sctp","pid":1,"process":"bad-protocol"},
{"port":81,"protocol":"tcp","pid":7,"process":"odd\u0007name","owner":"root"}]}}`

func getListeningPorts(t *testing.T, a *app, path string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer operator-token")
	w := httptest.NewRecorder()
	a.serverConnectionsHandler(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

func TestListeningPortsReachTheOperatorReadOnly(t *testing.T) {
	a, _ := machineTestApp(t)
	reportMachine(t, a, "v0.7.14", listeningSnapshot)
	code, body := getListeningPorts(t, a, "/api/custom/servers/7/listening-ports")
	if code != http.StatusOK || body["available"] != true || body["supported"] != true || body["helper_version"] != "v0.7.14" {
		t.Fatalf("listening ports: %d %#v", code, body)
	}
	listening := body["listening"].(map[string]any)
	ports := listening["ports"].([]any)
	if len(ports) != 4 {
		t.Fatalf("malformed entries must be dropped, kept %#v", ports)
	}
	held := ports[1].(map[string]any)
	if held["port"] != float64(10017) || held["process"] != "lkbk-core" || held["pid"] != float64(212883) || held["owner"] != nil {
		t.Fatalf("10017 owner = %#v", held)
	}
	if core := ports[0].(map[string]any); core["owner"] != "core" || core["inbound_tag"] != "自己用 ss-10016" {
		t.Fatalf("Core entry = %#v", core)
	}
	if odd := ports[3].(map[string]any); odd["process"] != "oddname" || odd["owner"] != nil {
		t.Fatalf("control characters and unknown owners must be cleaned: %#v", odd)
	}

	// Only GET, only for an authorised operator.
	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete} {
		r := httptest.NewRequest(method, "/api/custom/servers/7/listening-ports", nil)
		r.Header.Set("Authorization", "Bearer operator-token")
		w := httptest.NewRecorder()
		a.serverConnectionsHandler(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s must be refused: %d", method, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/custom/servers/7/listening-ports", nil)
	w := httptest.NewRecorder()
	a.adminStore = &fakePersistentConnectionStore{fakeAdminSessionStore: fakeAdminSessionStore{authorized: false, server: true}}
	a.serverConnectionsHandler(w, r)
	if w.Code == http.StatusOK {
		t.Fatalf("an unauthenticated request must not read listening ports: %d", w.Code)
	}
	a.adminStore = &fakePersistentConnectionStore{fakeAdminSessionStore: fakeAdminSessionStore{authorized: true, server: true}}

	// The connections view keeps its old payload.
	code, connections := getListeningPorts(t, a, "/api/custom/servers/7/connections")
	if code != http.StatusOK || strings.Contains(fmt.Sprint(connections["record"]), "lkbk-core") {
		t.Fatalf("connections payload must not carry listening ports: %d %#v", code, connections["record"])
	}
}

func TestListeningPortsUnsupportedOrStaleHelper(t *testing.T) {
	a, _ := machineTestApp(t)
	if code, body := getListeningPorts(t, a, "/api/custom/servers/7/listening-ports"); code != http.StatusOK || body["available"] != false || body["supported"] != false {
		t.Fatalf("no report yet: %d %#v", code, body)
	}
	reportMachine(t, a, "v0.7.13", machineV1Snapshot)
	if _, body := getListeningPorts(t, a, "/api/custom/servers/7/listening-ports"); body["available"] != true || body["supported"] != false || body["listening"] != nil {
		t.Fatalf("an old Helper reports nothing: %#v", body)
	}
	reportMachine(t, a, "v0.7.14", listeningSnapshot)
	a.connectionMu.Lock()
	record := a.detailedConnections["7"]
	record.UpdatedAt = time.Now().Add(-time.Minute)
	a.detailedConnections["7"] = record
	a.connectionMu.Unlock()
	if _, body := getListeningPorts(t, a, "/api/custom/servers/7/listening-ports"); body["available"] != false || body["supported"] != false {
		t.Fatalf("a stale report must not be trusted: %#v", body)
	}
}

func TestSanitizeListeningPortsIsBounded(t *testing.T) {
	report := &serverListeningPorts{Error: strings.Repeat("端", 300)}
	for port := 1; port <= flvx.MaxBusyPorts+5; port++ {
		report.Ports = append(report.Ports, serverListeningPort{Port: port, Protocol: "tcp", Process: strings.Repeat("p", 100)})
	}
	clean := sanitizeListeningPorts(report)
	if len(clean.Ports) != flvx.MaxBusyPorts || !clean.Truncated || len(clean.Ports[0].Process) != 64 || len(clean.Error) > 512 || !strings.HasPrefix(clean.Error, "端") {
		t.Fatalf("clean = %d ports truncated=%t process=%d error=%d", len(clean.Ports), clean.Truncated, len(clean.Ports[0].Process), len(clean.Error))
	}
	if sanitizeListeningPorts(nil) != nil {
		t.Fatal("a missing report stays missing")
	}
	for path, want := range map[string]string{"/api/custom/servers/7/listening-ports": "7", "/api/custom/servers/0/listening-ports": "", "/api/custom/servers/a/listening-ports": "", "/api/custom/servers/7/x/listening-ports": ""} {
		if id, ok := parseListeningPortsPath(path); id != want && ok {
			t.Fatalf("%s parsed as %q", path, id)
		}
	}
}
