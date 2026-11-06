package flvx

import (
	"strings"
	"testing"
	"time"
)

func TestProbeTargetAndValidate(t *testing.T) {
	for in, want := range map[string]string{
		"WWW.Lovelive-Anime.jp":      "www.lovelive-anime.jp:443",
		"www.lovelive-anime.jp.:443": "www.lovelive-anime.jp:443",
		"dl.google.com:8443":         "dl.google.com:8443",
		"1.2.3.4:443":                "",
		"localhost:443":              "",
		"nodot:443":                  "",
		"a.example:0":                "",
		"bad_host.example:443":       "",
		"[::1]:443":                  "",
	} {
		got, ok := ProbeTarget(in)
		if ok != (want != "") || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	r, errs := ValidateProbe(ProbeRequest{Targets: []string{"a.example", "A.example:443", "b.example"}})
	if len(errs) != 0 || strings.Join(r.Targets, ",") != "a.example:443,b.example:443" || r.TimeoutMS != 3000 {
		t.Fatalf("%+v %v", r, errs)
	}
	many := make([]string, MaxProbeTargets+1)
	for i := range many {
		many[i] = "a.example"
	}
	for _, bad := range []ProbeRequest{{}, {Targets: many}, {Targets: []string{"a.example"}, TimeoutMS: 100}, {Targets: []string{"a.example"}, TimeoutMS: 6000}, {Targets: []string{"10.0.0.1"}}} {
		if _, errs := ValidateProbe(bad); len(errs) == 0 {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestProbeEnvelopeVerify(t *testing.T) {
	now := time.Now()
	e := ProbeEnvelope{Job: ProbeJob{ServerID: "7", ID: "j", Targets: []string{"a.example:443"}, TimeoutMS: 3000}, ExpiresAt: now.Add(5 * time.Minute).UnixMilli()}
	e.Sign("token")
	if !e.Verify("token", "7", now) || e.Verify("other", "7", now) || e.Verify("token", "8", now) || e.Verify("token", "7", now.Add(6*time.Minute)) {
		t.Fatal("signature / server / expiry")
	}
	tampered := e
	tampered.Job.Targets = []string{"b.example:443"}
	if tampered.Verify("token", "7", now) {
		t.Fatal("tampered job accepted")
	}
	raw := e
	raw.Job.Targets = []string{"A.example"}
	raw.Sign("token")
	if raw.Verify("token", "7", now) {
		t.Fatal("non-normalised target accepted")
	}
}

func TestSanitizeRealityPinsAndProbeReport(t *testing.T) {
	pins := SanitizeRealityPins([]RealityPinStatus{
		{Host: "a.example:443", PinnedIP: "203.0.113.1", PinnedMS: 4.2, BestIP: "203.0.113.2", BestMS: 3.1, CheckedAt: 1, Nodes: []string{"1", ""}},
		{Host: "a.example:443", PinnedIP: "10.0.0.1", PinnedMS: 1},
		{Host: "a.example:443", PinnedIP: "203.0.113.1", PinnedMS: -2},
		{Host: "a.example", PinnedIP: "203.0.113.1"},
	})
	if len(pins) != 1 || len(pins[0].Nodes) != 1 {
		t.Fatalf("%+v", pins)
	}
	if SanitizeRealityPins(nil) != nil {
		t.Fatal("empty report")
	}
	ok := &ProbeReport{ID: "j", State: "done", Results: []ProbeResult{{Target: "a.example:443", Feasible: true, BestIP: "203.0.113.1", LatencyMS: 9, IPs: []ProbeIP{{IP: "203.0.113.1", TCPMS: 3}, {IP: "x", TCPMS: 1}}, ServerNames: []string{"a.example", "*.a.example", "1.2.3.4"}, Reason: strings.Repeat("é", 300)}}}
	r := SanitizeProbeReport(ok)
	if r == nil || len(r.Results[0].IPs) != 1 || len(r.Results[0].ServerNames) != 1 || len([]rune(r.Results[0].Reason)) != 200 {
		t.Fatalf("%+v", r)
	}
	for _, bad := range []*ProbeReport{nil, {ID: "", State: "done"}, {ID: "j", State: "weird"}, {ID: "j", State: "done", Results: []ProbeResult{{Target: "A.example"}}}, {ID: "j", State: "done", Results: []ProbeResult{{Target: "a.example:443", Feasible: true, BestIP: "127.0.0.1"}}}} {
		if SanitizeProbeReport(bad) != nil {
			t.Errorf("kept %+v", bad)
		}
	}
}
