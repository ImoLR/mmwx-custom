package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

type fakeFLVXCore struct {
	epoch         time.Time
	present       map[string]bool
	stats         map[string][2]int64
	limits        coreConfig
	adds, removes int
	stops         int
	calls, blocks int
	writes        int
}

func helperFLVXNode(id string, port int) flvx.Node {
	yes := true
	key := base64.StdEncoding.EncodeToString(make([]byte, 16))
	return flvx.Node{ID: id, Owner: flvx.Owner{ID: "1", Name: "Alice"}, Name: "test", Protocol: "ss2022", Port: port, Enabled: &yes, SS2022: &flvx.SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: key, UserKey: key}, MaxConnections: 4, MaxOnlineIPs: 2}
}

func fakeFLVXManager(t *testing.T) (*flvxManager, *fakeFLVXCore) {
	t.Helper()
	dir := t.TempDir()
	socket := filepath.Join(dir, "c.sock")
	f := &fakeFLVXCore{epoch: time.Now().UTC(), present: map[string]bool{}, stats: map[string][2]int64{}}
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config" {
			if err := json.NewDecoder(r.Body).Decode(&f.limits); err != nil {
				t.Error(err)
			}
			if f.limits.BlockedIdentities != nil {
				f.blocks += len(*f.limits.BlockedIdentities)
			}
			json.NewEncoder(w).Encode(map[string]bool{"success": true})
			return
		}
		json.NewEncoder(w).Encode(coreSnapshotResponse{Version: 7, StartedAt: f.epoch})
	})}
	go server.Serve(l)
	t.Cleanup(func() { server.Close() })
	m := &flvxManager{dir: filepath.Join(dir, "state"), core: newCoreClient(socket), config: config{Token: "helper", ServerID: "7"}, logs: newPeriodicLogLimiter()}
	m.state = flvxLocalState{Payload: flvx.Payload{Version: 1, Status: "active", Config: flvx.Config{PortMin: 30000, PortMax: 31000, ResetDay: 1, MemoryMB: 256, CPUPercent: 50, MaxConnections: 6}, Desired: flvx.Desired{Revision: 1, Nodes: []flvx.Node{helperFLVXNode("one", 30001)}}}, APIPort: 49000, Counters: map[string]*flvx.Counter{}, Applied: map[string]string{}, Report: flvx.Report{Generation: "helper-a"}}
	m.setup = func(context.Context) error { return nil }
	m.writeState = func(path string, raw []byte, mode os.FileMode) error {
		if err := writeFLVXFile(path, raw, mode); err != nil {
			return err
		}
		f.writes++
		return nil
	}
	m.readOfficial = func() ([]int, error) { return []int{30003}, nil }
	m.stop = func(context.Context) error { f.stops++; f.present = map[string]bool{}; return nil }
	m.cleanup = func(context.Context) error { return os.RemoveAll(m.dir) }
	m.apiCall = func(_ context.Context, command string, args ...string) ([]byte, error) {
		f.calls++
		switch command {
		case "lsi":
			list := []map[string]string{}
			for tag := range f.present {
				list = append(list, map[string]string{"tag": tag})
			}
			return json.Marshal(map[string]any{"inbounds": list})
		case "adi":
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return nil, err
			}
			var c struct {
				Inbounds []struct {
					Tag string `json:"tag"`
				} `json:"inbounds"`
			}
			json.Unmarshal(raw, &c)
			for _, in := range c.Inbounds {
				f.present[in.Tag] = true
			}
			f.adds++
			return []byte(`{}`), nil
		case "rmi":
			delete(f.present, args[0])
			f.removes++
			return []byte(`{}`), nil
		case "statsquery":
			stats := []map[string]any{}
			for id, v := range f.stats {
				for i, direction := range []string{"uplink", "downlink"} {
					stats = append(stats, map[string]any{"name": "inbound>>>flvxl-" + id + ">>>traffic>>>" + direction, "value": fmt.Sprint(v[i])})
				}
			}
			return json.Marshal(map[string]any{"stat": stats})
		default:
			return nil, fmt.Errorf("unexpected gRPC call %s", command)
		}
	}
	return m, f
}

func TestFLVXSteadyStateRates(t *testing.T) {
	for _, interval := range []time.Duration{time.Second, 5 * time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			m, f := fakeFLVXManager(t)
			now := time.Now()
			m.clock = func() time.Time { return now }
			if err := m.reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.calls, f.writes = 0, 0
			ticks := int(time.Minute / interval)
			for i := 1; i <= ticks; i++ {
				now = now.Add(interval)
				f.stats["one"] = [2]int64{int64(i * 100), int64(i * 20)}
				if err := m.reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("steady minute: ticks=%d writes=%d CLI=%d", ticks, f.writes, f.calls)
			if f.writes > 3 || f.calls > ticks || f.writes < 2 {
				t.Fatal("heartbeat persistence or Core CLI rate regressed")
			}
		})
	}
}

func TestFLVXPublishedCountersSurviveCrash(t *testing.T) {
	for _, restartCore := range []bool{false, true} {
		t.Run(fmt.Sprint(restartCore), func(t *testing.T) {
			m, f := fakeFLVXManager(t)
			now := time.Now()
			m.clock = func() time.Time { return now }
			for i := 0; i <= 7; i++ {
				f.stats["one"] = [2]int64{int64(i * 100), int64(i * 20)}
				if err := m.reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
				r := m.snapshotReport()
				raw, err := os.ReadFile(filepath.Join(m.dir, "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				var disk flvxLocalState
				if err = json.Unmarshal(raw, &disk); err != nil {
					t.Fatal(err)
				}
				if r.Sequence > disk.Report.Sequence || r.Nodes[0].UpBytes > disk.Counters["one"].Up || r.Nodes[0].DownBytes > disk.Counters["one"].Down {
					t.Fatal("published counters or sequence were not persisted")
				}
				now = now.Add(5 * time.Second)
			}
			published := m.snapshotReport()
			if published.Nodes[0].UpBytes != 600 || m.state.Counters["one"].Up != 700 {
				t.Fatal("fixture did not leave an unpersisted tail")
			}
			// Discard memory exactly as a crash would; retain only state.json.
			restarted := &flvxManager{dir: m.dir, core: m.core, config: m.config, logs: newPeriodicLogLimiter(), clock: m.clock, setup: m.setup, stop: m.stop, cleanup: m.cleanup, readOfficial: m.readOfficial, apiCall: m.apiCall}
			if err := restarted.load(); err != nil {
				t.Fatal(err)
			}
			if restartCore {
				f.epoch = f.epoch.Add(time.Second)
				f.present = map[string]bool{}
				f.stats["one"] = [2]int64{1, 1}
			}
			for i := 0; i < 3; i++ {
				r := restarted.snapshotReport()
				if r.Sequence < published.Sequence || r.Nodes[0].UpBytes < published.Nodes[0].UpBytes || r.Nodes[0].DownBytes < published.Nodes[0].DownBytes {
					t.Fatal("restart decreased an already published lifetime/sequence")
				}
				if err := restarted.reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
				now = now.Add(30 * time.Second)
			}
		})
	}
}

func TestFLVXPersistenceFailureAndQuota(t *testing.T) {
	m, f := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.state.Payload.Config.TrafficLimitBytes = 150
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	published, writes := m.snapshotReport(), f.writes
	now = now.Add(5 * time.Second)
	f.stats["one"] = [2]int64{100, 0}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.writes != writes || !reflect.DeepEqual(published, m.snapshotReport()) {
		t.Fatal("counter-only tick wrote or published before 30 seconds")
	}
	// The quota uses memory and must persist/block on the next five-second tick.
	now = now.Add(5 * time.Second)
	f.stats["one"] = [2]int64{160, 0}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.writes == writes || len(f.present) != 0 || f.blocks == 0 || m.snapshotReport().Nodes[0].UpBytes != 160 {
		t.Fatal("quota crossing waited for heartbeat flush")
	}
	published = m.snapshotReport()
	write := m.writeState
	m.writeState = func(string, []byte, os.FileMode) error { return errors.New("disk unavailable") }
	now = now.Add(30 * time.Second)
	f.stats["one"] = [2]int64{170, 0}
	if err := m.reconcile(context.Background()); err == nil {
		t.Fatal("failed persistence was ignored")
	}
	m.state.Report.Core.State = "error"
	if err := m.persistReport(true); err == nil || !reflect.DeepEqual(published, m.snapshotReport()) {
		t.Fatal("failed write published unpersisted counters/error state")
	}
	m.writeState = write
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.snapshotReport().Nodes[0].UpBytes < 170 {
		t.Fatal("recovery lost counters")
	}
}

func TestFLVXRepeatedDesiredAndAcknowledgements(t *testing.T) {
	m, f := fakeFLVXManager(t)
	now := time.Now()
	m.clock = func() time.Time { return now }
	m.state.Payload.ServerID = "7"
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.calls, f.writes = 0, 0
	for i := 1; i <= 12; i++ {
		now = now.Add(5 * time.Second)
		r := m.snapshotReport()
		p := m.state.Payload
		p.AckGeneration, p.AckSequence = r.Generation, r.Sequence
		p.Period, p.TrafficUsedBytes = r.Period, r.TrafficUsedBytes
		e := flvx.Envelope{Payload: p, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
		e.Sign("helper")
		if err := m.accept(e); err != nil {
			t.Fatal(err)
		}
		f.stats["one"] = [2]int64{int64(i * 100), 0}
		if err := m.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.writes > 3 || f.calls != 12 || m.snapshotReport().Sequence > 3 {
		t.Fatalf("ACK/desired caused persistence feedback: writes=%d CLI=%d sequence=%d", f.writes, f.calls, m.snapshotReport().Sequence)
	}
	// Even an older file carrying a newer Custom acknowledgement cannot regress.
	m.state.Payload.AckSequence = m.state.Report.Sequence + 10
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	want := m.state.Payload.AckSequence
	m.state = flvxLocalState{}
	if err := m.load(); err != nil || m.snapshotReport().Sequence < want {
		t.Fatal("load regressed an acknowledged sequence", err)
	}
}

func TestFLVXRealityPinSurvivesDNSAndRestarts(t *testing.T) {
	m, f := fakeFLVXManager(t)
	n := helperFLVXNode("one", 30001)
	n.Protocol, n.SS2022 = "vless_reality_vision", nil
	n.VLESS = &flvx.VLESS{UUID: "12345678-1234-4234-8234-123456789abc", Flow: "xtls-rprx-vision", Reality: flvx.Reality{Dest: "public.example:443", ServerNames: []string{"public.example"}, PrivateKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ShortIDs: []string{""}}}
	m.state.Payload.Desired.Nodes = []flvx.Node{n}
	lookups, tick := 0, 0
	m.lookupHost = func(context.Context, string) ([]string, error) {
		lookups++
		if tick%3 == 1 {
			return nil, errors.New("transient DNS failure")
		}
		if tick%3 == 2 {
			return []string{"203.0.113.1", "127.0.0.1"}, nil
		}
		return []string{fmt.Sprintf("203.0.113.%d", 1+tick%200)}, nil
	}
	apply := func() {
		t.Helper()
		if err := m.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	initial := m.state.Applied[n.Tag()]
	f.adds, f.removes, f.blocks = 0, 0, 0
	for tick = 1; tick <= 60; tick++ {
		apply()
		if m.state.Applied[n.Tag()] != initial {
			t.Fatal("unchanged node was rendered differently")
		}
	}
	if lookups != 1 || f.adds != 0 || f.removes != 0 || f.blocks != 0 {
		t.Fatalf("DNS churn: lookups=%d adds=%d removes=%d blocks=%d", lookups, f.adds, f.removes, f.blocks)
	}
	// A changed destination resolves once and then reuses its new pin.
	tick = 63
	m.state.Payload.Desired.Nodes[0].VLESS.Reality.Dest = "changed.example:443"
	m.state.Payload.Desired.Revision++
	apply()
	changed := m.state.Applied[n.Tag()]
	apply()
	if lookups != 2 || f.adds != 1 || f.removes != 1 || changed == initial {
		t.Fatal("changed destination did not re-pin exactly once")
	}
	// Both restarts reuse the persisted pin, even while DNS is failing.
	m.state = flvxLocalState{}
	if err := m.load(); err != nil {
		t.Fatal(err)
	}
	tick = 64
	apply()
	f.epoch = f.epoch.Add(time.Second)
	f.present = map[string]bool{}
	apply()
	if lookups != 2 || m.state.Applied[n.Tag()] != changed {
		t.Fatal("restart discarded the pin")
	}
	// A failed/private re-pin keeps the entire applied inbound and logs once.
	logs := 0
	m.logs.print = func(string, ...any) { logs++ }
	m.state.Payload.Desired.Nodes[0].VLESS.Reality.Dest = "unavailable.example:443"
	f.adds, f.removes, f.blocks = 0, 0, 0
	for i := 0; i < 30; i++ {
		tick = 64 + i%2
		apply()
	}
	if f.adds != 0 || f.removes != 0 || f.blocks != 0 || logs != 1 || m.state.Applied[n.Tag()] != changed || m.state.Report.Nodes[0].State != "active" {
		t.Fatalf("failed re-pin disrupted node: %+v logs=%d", f, logs)
	}
	// A new node has no public pin to fall back to.
	newNode := m.state.Payload.Desired.Nodes[0]
	newNode.ID, newNode.Port = "new", 30002
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, newNode)
	for _, tick = range []int{64, 65} {
		apply()
		if f.present[newNode.Tag()] || m.state.Report.Nodes[0].State != "error" || m.state.RealityPins[newNode.Tag()].Dest != "" {
			t.Fatal("unresolved/private destination admitted without a pin")
		}
	}
}

func TestFLVXDiffCapsPersistenceAndCancel(t *testing.T) {
	m, f := fakeFLVXManager(t)
	ctx := context.Background()
	apply := func() {
		t.Helper()
		if err := m.reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	apply()
	if f.adds != 1 || f.removes != 0 || f.limits.MaxGlobalTotalConnections == nil || *f.limits.MaxGlobalTotalConnections != 6 || len(f.limits.PortLimits) != 1 || *f.limits.PortLimits[0].MaxTotalConnections != 4 || *f.limits.PortLimits[0].MaxInboundOnlineIPs != 2 {
		t.Fatalf("diff or limits %+v", f)
	}
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, helperFLVXNode("two", 30002))
	m.state.Payload.Desired.Revision++
	apply()
	if f.adds != 2 || f.removes != 0 {
		t.Fatal("add restarted existing handler")
	}
	f.stats["one"] = [2]int64{100, 20}
	apply()
	if m.state.Report.Nodes[0].UpBytes != 100 {
		t.Fatal(m.state.Report)
	}
	if err := m.persistReport(true); err != nil {
		t.Fatal(err)
	}
	saved := m.state
	m.state = flvxLocalState{}
	if err := m.load(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, m.state) {
		t.Fatal("Helper restart did not retain counters/applied state")
	}
	f.epoch = f.epoch.Add(time.Second)
	f.stats["one"] = [2]int64{5, 2}
	f.present = map[string]bool{}
	apply()
	if m.state.Report.Nodes[0].UpBytes != 105 {
		t.Fatal("Core restart decreased counter")
	}
	m.state.Payload.Config.TrafficLimitBytes = 127
	apply()
	if len(f.present) != 0 || len(*f.limits.BlockedIdentities) != 4 {
		t.Fatal("traffic cap did not remove/block both nodes")
	}
	m.state.Payload.Config.TrafficLimitBytes = 1000
	apply()
	if len(f.present) != 2 {
		t.Fatal("raised cap did not restore nodes")
	}
	m.state.Payload.Status = "paused"
	apply()
	if len(f.present) != 0 {
		t.Fatal("paused share listens")
	}
	m.state.Payload.Status = "active"
	apply()
	if len(f.present) != 2 {
		t.Fatal("resume failed")
	}
	m.state.Payload.Desired.Nodes[0].Port = 30003
	apply()
	if f.present["flvxl-one"] || m.state.Report.Nodes[0].State != "error" || !f.present["flvxl-two"] {
		t.Fatal("local official collision not isolated")
	}
	m.state.Payload.Status = "revoked"
	apply()
	if f.stops != 1 || m.state.Report.Core.State != "stopped" {
		t.Fatal("cancel did not stop only FLVX")
	}
	m.state.Payload.AckGeneration = m.state.Report.Generation
	m.state.Payload.AckSequence = m.state.Report.Sequence
	apply()
	apply()
	if _, err := os.Stat(m.dir); !os.IsNotExist(err) {
		t.Fatal("cancel did not remove state after final counters acknowledged")
	}
	for _, n := range m.state.Report.Nodes {
		if n.State != "removed" {
			t.Fatal("FLVX did not see removed")
		}
	}
	e := flvx.Envelope{Payload: m.state.Payload, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	e.Payload.ServerID = "7"
	e.Payload.Version++
	e.Sign("helper")
	if err := m.accept(e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.dir); !os.IsNotExist(err) {
		t.Fatal("repeated cancellation recreated the instance state")
	}
}

func TestFLVXSignatureExpiryAndIsolationUnit(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	e := flvx.Envelope{Payload: m.state.Payload, ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	e.Payload.ServerID = "7"
	e.Sign("helper")
	if err := m.accept(e); err != nil {
		t.Fatal(err)
	}
	e.Payload.Config.CPUPercent = 100
	if err := m.accept(e); err == nil {
		t.Fatal("unsigned mutation accepted")
	}
	unit := flvxServiceUnit(m.state.Payload.Config)
	for _, required := range []string{"User=mmwxc-flvx", "MemoryMax=256M", "CPUQuota=50%", "NoNewPrivileges=yes", "ProtectSystem=strict", "ReadWritePaths=/run/mmwxc-flvx", "LogRateLimitBurst=10", "LimitNOFILE=65535"} {
		if !strings.Contains(unit, required) {
			t.Fatal("unit missing", required)
		}
	}
	if strings.Contains(filepath.Base(flvxBinaryPath), "xray") || strings.Contains(unit, coreBinaryPath) || strings.Contains(unit, defaultCoreSocket) || strings.Contains(unit, "nft") {
		t.Fatal("FLVX unit can affect official ownership")
	}
	m.state.Payload.Config.TrafficLimitBytes = 1
	m.state.Report.TrafficUsedBytes = 1
	m.state.Report.Period = "2000-01-01"
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.state.Report.TrafficUsedBytes != 0 || m.state.Report.Nodes[0].State != "active" {
		t.Fatal("monthly reset failed")
	}
}

func TestFLVXRemovedReportAcknowledgement(t *testing.T) {
	m, f := fakeFLVXManager(t)
	ctx := context.Background()
	f.stats["one"] = [2]int64{100, 20}
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	m.state.Payload.Desired.Nodes = []flvx.Node{}
	m.state.Payload.Desired.Revision++
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.state.Report.Nodes) != 1 || m.state.Report.Nodes[0].State != "removed" || m.state.Report.Nodes[0].UpBytes != 100 {
		t.Fatal("final tombstone missing")
	}
	m.state.Payload.AckGeneration = m.state.Report.Generation
	m.state.Payload.AckSequence = m.state.Report.Sequence
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.state.Report.Nodes) != 0 || m.state.Counters["one"].Up != 100 {
		t.Fatal("acknowledged history bloats report or loses lifetime ledger")
	}
	m.state.Payload.Desired.Nodes = []flvx.Node{helperFLVXNode("one", 30001)}
	m.state.Payload.Desired.Revision++
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.state.Report.Nodes) != 1 || m.state.Report.Nodes[0].UpBytes != 100 {
		t.Fatal("re-added id lost lifetime counter")
	}
}
