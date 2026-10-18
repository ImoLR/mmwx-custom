package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	m.readOfficial = func() ([]int, error) { return []int{30003}, nil }
	m.stop = func(context.Context) error { f.stops++; f.present = map[string]bool{}; return nil }
	m.cleanup = func(context.Context) error { return os.RemoveAll(m.dir) }
	m.apiCall = func(_ context.Context, command string, args ...string) ([]byte, error) {
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
