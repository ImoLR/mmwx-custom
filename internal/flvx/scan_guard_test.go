package flvx

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func scanDesired(g *ScanGuard) Desired {
	a, b := testNode(), testNode()
	a.ID, b.ID = "21", "22"
	return Desired{Revision: 3, Nodes: []Node{a, b}, ScanGuard: g}
}

// A share without scan_guard keeps today's desired bytes (and so today's
// signed Helper payload); with it, the object is FLVX's native one.
func TestScanGuardWireShape(t *testing.T) {
	plain, _ := json.Marshal(scanDesired(nil))
	if strings.Contains(string(plain), "scan_guard") {
		t.Fatalf("absent guard rendered: %s", plain)
	}
	raw := `{"enabled":true,"identities":["flvxl-21"],"horizontal":100,"horizontal_web":500,"web_ports":[80,443],"window_s":60,"vertical":30,"total":1000,"total_window_s":300,"seen":1793709225987084}`
	var g ScanGuard
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&g); err != nil {
		t.Fatal(err)
	}
	if out, _ := json.Marshal(g); string(out) != raw {
		t.Fatalf("round trip %s", out)
	}
	// [] = no web ports survives storage; absent stays absent (null) and an
	// identical retry stays DeepEqual after the round trip.
	for _, in := range []string{`{"enabled":true,"identities":[],"web_ports":[]}`, `{"enabled":false}`} {
		var a, b ScanGuard
		json.Unmarshal([]byte(in), &a)
		stored, _ := json.Marshal(a)
		json.Unmarshal(stored, &b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: %#v != %#v after storage", in, a, b)
		}
	}
	var none ScanGuard
	json.Unmarshal([]byte(`{"enabled":true,"identities":["flvxl-21"],"web_ports":[]}`), &none)
	if ports := none.CoreConfig([]string{"flvxl-21"})["web_ports"].([]int); len(ports) != 0 {
		t.Fatalf("[] became %v", ports)
	}
}

func TestScanGuardCoreConfigDefaults(t *testing.T) {
	got, _ := json.Marshal(ScanGuard{Enabled: true, Horizontal: 150, Total: 2000}.CoreConfig([]string{"flvxl-1", "flvxl-2"}))
	want := `{"enabled":true,"horizontal":150,"horizontal_web":500,"identities":["flvxl-1","flvxl-2"],"total":2000,"total_window_s":300,"vertical":30,"web_ports":[80,443],"window_s":60}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}

func TestValidateScanGuard(t *testing.T) {
	ok := []*ScanGuard{
		nil,
		{Enabled: true, Identities: []string{"flvxl-21", "flvxl-22"}, WebPorts: []int{80, 443}, Seen: MaxScanSeq},
		{Enabled: true},
		// Disabled: thresholds are not checked (a half-edited rule never
		// blocks a node push), identities still are.
		{Enabled: false, Horizontal: -5, WindowS: 1},
	}
	for _, g := range ok {
		if errs := ValidateScanGuard(scanDesired(g)); len(errs) > 0 {
			t.Fatalf("%+v rejected: %v", g, errs)
		}
	}
	bad := []*ScanGuard{
		{Enabled: true, Identities: []string{"flvxl-99"}},
		{Enabled: false, Identities: []string{"flvxl-21", "flvxl-21"}},
		{Enabled: true, Identities: []string{"21"}},
		{Enabled: true, Identities: []string{"flvxl-21", "flvxl-22", "flvxl-23"}},
		{Enabled: true, Seen: -1},
		{Enabled: true, Seen: MaxScanSeq + 1},
		{Enabled: true, Horizontal: 100001},
		{Enabled: true, Vertical: -1},
		{Enabled: true, WindowS: 9},
		{Enabled: true, TotalWindowS: 3601},
		{Enabled: true, WebPorts: []int{80, 80}},
		{Enabled: true, WebPorts: []int{0}},
		{Enabled: true, WebPorts: make([]int, 17)},
	}
	for _, g := range bad {
		if errs := ValidateScanGuard(scanDesired(g)); len(errs) == 0 {
			t.Fatalf("%+v accepted", g)
		}
	}
}

func TestSanitizeScanGuardStatus(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.FixedZone("x", 3600))
	hit := func(seq int64) ScanHit {
		return ScanHit{ID: "21", InboundTag: "flvxl-21", Type: "horizontal", Count: 100, Port: 22, WindowS: 60, At: at, Seq: seq}
	}
	if SanitizeScanGuardStatus(nil) != nil || SanitizeScanGuardStatus(&ScanGuardStatus{State: "applied"}) != nil {
		t.Fatal("unknown state kept")
	}
	in := &ScanGuardStatus{State: "on", Error: strings.Repeat("错", 300), CoreReady: true, Blocked: []string{"flvxl-22", "flvxl-21", "flvxl-21", "other", "flvxl-a b"}}
	for seq := int64(1000); seq > 0; seq-- {
		in.Hits = append(in.Hits, hit(seq))
	}
	bogus := []ScanHit{hit(0), hit(MaxScanSeq + 1), {ID: "21", InboundTag: "flvxl-22", Type: "total", Seq: 5000}, {ID: "21", InboundTag: "flvxl-21", Type: "sweep", Seq: 5001},
		{ID: "21", InboundTag: "flvxl-21", Type: "vertical", Host: "bad host", Seq: 5002}, {ID: "21", InboundTag: "flvxl-21", Type: "vertical", Port: 70000, Seq: 5003}}
	in.Hits = append(in.Hits, bogus...)
	in.Hits = append(in.Hits, hit(1000))
	out := SanitizeScanGuardStatus(in)
	if out.State != "on" || !out.CoreReady || len([]rune(out.Error)) != 200 {
		t.Fatalf("state %+v", out)
	}
	if len(out.Hits) != MaxScanHits || out.Hits[0].Seq != 1000-MaxScanHits+1 || out.Hits[len(out.Hits)-1].Seq != 1000 || out.Hits[0].At.Location() != time.UTC {
		t.Fatalf("hits %d first %d last %d", len(out.Hits), out.Hits[0].Seq, out.Hits[len(out.Hits)-1].Seq)
	}
	if !reflect.DeepEqual(out.Blocked, []string{"flvxl-21", "flvxl-22"}) {
		t.Fatalf("blocked %v", out.Blocked)
	}
	v := hit(7)
	v.Type, v.Port, v.Host = "vertical", 0, "44.31.0.1"
	if !ValidScanHit(v) {
		t.Fatal("vertical IP host rejected")
	}
	v.Host = "scan.lab.test"
	if !ValidScanHit(v) {
		t.Fatal("vertical domain host rejected")
	}
	v.Host = "2001:db8::1"
	if !ValidScanHit(v) {
		t.Fatal("vertical IPv6 host rejected")
	}
}
