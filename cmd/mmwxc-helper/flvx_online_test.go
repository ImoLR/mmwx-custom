package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func TestFLVXOnlineIPMapping(t *testing.T) {
	for _, tc := range []struct {
		name, users string
		count       int
		ips         []flvx.OnlineIP
	}{
		{"present", `[{"identity":{"inbound_tag":"flvxl-one"},"inbound_online_ips":[{"ip":"192.0.2.1","connections":2}]}]`, 1, []flvx.OnlineIP{{IP: "192.0.2.1", Connections: 2}}},
		{"absent", `[{"identity":{"inbound_tag":"flvxl-one"}}]`, -1, nil},
		{"null", `[{"identity":{"inbound_tag":"flvxl-one"},"inbound_online_ips":null}]`, 0, []flvx.OnlineIP{}},
		{"empty", `[{"identity":{"inbound_tag":"flvxl-one"},"inbound_online_ips":[]}]`, 0, []flvx.OnlineIP{}},
		{"ipv6-prefix", `[{"identity":{"inbound_tag":"flvxl-one"},"inbound_online_ips":[{"ip":"2001:db8:1::/64","connections":3}]}]`, 1, []flvx.OnlineIP{{IP: "2001:db8:1::/64", Connections: 3}}},
		{"shared-ip", `[{"identity":{"inbound_tag":"flvxl-one","user":"a"},"inbound_online_ips":[{"ip":"192.0.2.1","connections":2}]},{"identity":{"inbound_tag":"flvxl-one","user":"b"},"inbound_online_ips":[{"ip":"192.0.2.1","connections":4}]}]`, 1, []flvx.OnlineIP{{IP: "192.0.2.1", Connections: 6}}},
		{"partly-unknown", `[{"identity":{"inbound_tag":"flvxl-one","user":"a"},"inbound_online_ips":[]},{"identity":{"inbound_tag":"flvxl-one","user":"b"}}]`, -1, nil},
		{"other-tag", `[{"identity":{"inbound_tag":"flvxl-other"},"inbound_online_ips":[{"ip":"192.0.2.9","connections":9}]},{"identity":{"inbound_tag":"official"},"inbound_online_ips":[{"ip":"192.0.2.8","connections":8}]}]`, -1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var snapshot coreSnapshotResponse
			if err := json.Unmarshal([]byte(`{"proxy_users":`+tc.users+`,"inbounds":[{"inbound_tag":"flvxl-one","online_ip_count":99,"online_ips":[{"ip":"192.0.2.99","connections":99}]}]}`), &snapshot); err != nil {
				t.Fatal(err)
			}
			got := flvxSnapshotOnlineIPs(snapshot)["flvxl-one"]
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if tc.count < 0 {
				if strings.Contains(string(raw), "online_ip") {
					t.Fatalf("unknown fields not omitted: %s", raw)
				}
				return
			}
			if got.OnlineIPCount == nil || *got.OnlineIPCount != tc.count || got.OnlineIPs == nil || !reflect.DeepEqual(*got.OnlineIPs, tc.ips) {
				t.Fatalf("unexpected online fields: %s", raw)
			}
			if tc.count == 0 && !strings.Contains(string(raw), `"online_ips":[]`) {
				t.Fatalf("empty list must be []: %s", raw)
			}
		})
	}
}

func TestFLVXOnlineIPLimitAndTagIsolation(t *testing.T) {
	ips := coreOnlineIPs{}
	for i := 0; i < 80; i++ {
		ips = append(ips, onlineIP{IP: fmt.Sprintf("192.0.2.%03d", i), Connections: int64(i % 3)})
	}
	snapshot := coreSnapshotResponse{Users: []coreUserSnapshot{
		{Identity: coreIdentity{InboundTag: "flvxl-one"}, InboundOnlineIPs: ips},
		{Identity: coreIdentity{InboundTag: "flvxl-other"}, InboundOnlineIPs: coreOnlineIPs{{IP: "198.51.100.1", Connections: 99}}},
	}}
	result := flvxSnapshotOnlineIPs(snapshot)
	got := result["flvxl-one"]
	if *got.OnlineIPCount != 80 || len(*got.OnlineIPs) != 64 || cap(*got.OnlineIPs) != 64 {
		t.Fatalf("count/list/backing array: %+v len=%d cap=%d", got, len(*got.OnlineIPs), cap(*got.OnlineIPs))
	}
	for i, ip := range *got.OnlineIPs {
		if !strings.HasPrefix(ip.IP, "192.0.2.") {
			t.Fatal("another tag leaked", ip)
		}
		if i > 0 {
			prev := (*got.OnlineIPs)[i-1]
			if prev.Connections < ip.Connections || prev.Connections == ip.Connections && prev.IP > ip.IP {
				t.Fatal("incorrect order", prev, ip)
			}
		}
	}
	if other := result["flvxl-other"]; *other.OnlineIPCount != 1 || (*other.OnlineIPs)[0].IP != "198.51.100.1" {
		t.Fatal("other tag changed", other)
	}
}

func TestFLVXOnlineIPActiveAndPersistence(t *testing.T) {
	m, _ := fakeFLVXManager(t)
	m.state.Applied["flvxl-one"] = "applied"
	snapshot := coreSnapshotResponse{Users: []coreUserSnapshot{{Identity: coreIdentity{InboundTag: "flvxl-one"}, InboundOnlineIPs: coreOnlineIPs{{IP: "192.0.2.1", Connections: 3}}}}}
	if err := m.record(snapshot, map[string]string{"flvxl-one": "applied"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.state.Report.Nodes) != 1 || m.state.Report.Nodes[0].OnlineIPs == nil {
		t.Fatal("active online fields missing")
	}
	before, err := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "online_ip_count") || strings.Contains(string(before), `"online_ips"`) {
		t.Fatal("online fields persisted")
	}
	hash := m.materialHash()
	count := 50
	ips := []flvx.OnlineIP{{IP: "2001:db8::/64", Connections: 50}}
	m.state.Report.Nodes[0].OnlineIPCount = &count
	m.state.Report.Nodes[0].OnlineIPs = &ips
	if m.materialHash() != hash {
		t.Fatal("online fields changed material hash")
	}
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("online fields changed persisted JSON")
	}
	for _, tc := range []struct {
		name             string
		wanted, problems map[string]string
		remove           bool
	}{
		{"error", map[string]string{"flvxl-one": "applied"}, map[string]string{"one": "failed"}, false},
		{"removed", nil, nil, false},
		{"deleted", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.remove {
				m.state.Payload.Desired.Nodes = nil
			}
			if err := m.record(snapshot, tc.wanted, tc.problems); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(m.state.Report.Nodes)
			if strings.Contains(string(raw), "online_ip") {
				t.Fatal("inactive online fields retained", string(raw))
			}
		})
	}
}
