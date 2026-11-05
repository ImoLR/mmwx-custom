package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// landing-node-total-v1 §4: without max_total_connections the FLVX Core gets
// a /v1/config body byte-identical to Helper v0.7.19's. The golden file was
// written by this same test on b2dbc84 (v1.4.85 / v0.7.19) with
// MMWXC_WRITE_GOLDEN=1.
func TestFLVXNodeTotalAbsentConfigUnchanged(t *testing.T) {
	m, f := fakeFLVXManager(t)
	inbound := helperFLVXNode("two", 30002)
	inbound.ConnMode = flvx.ConnModeInbound
	total := helperFLVXNode("three", 30004)
	total.ConnMode, total.MaxOnlineIPs = flvx.ConnModeTotal, 0
	open := helperFLVXNode("four", 30005)
	open.MaxConnections = 0
	m.state.Payload.Desired.Nodes = append(m.state.Payload.Desired.Nodes, inbound, total, open)
	f.snap.Users = []coreUserSnapshot{{Identity: coreIdentity{InboundTag: "flvxl-one", User: "flvxl-one"}, CurrentTotal: 3, InboundTCP: tcpStateCounts{Total: 1, Established: 1}, OutboundTCP: tcpStateCounts{Total: 2, TimeWait: 2}}}
	bodies := []string{}
	for i := 0; i < 3; i++ {
		if err := m.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(f.raw))
	}
	golden := filepath.Join("testdata", "flvx-node-total-v0.7.19-config.json")
	if os.Getenv("MMWXC_WRITE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(bodies[0]+"\n"+bodies[2]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got := bodies[0] + "\n" + bodies[2] + "\n"; got != string(want) {
		t.Fatalf("/v1/config changed without max_total_connections:\n got %s\nwant %s", got, want)
	}
}
