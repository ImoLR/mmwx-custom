package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// scanCore is a fake Core with scan_guard.v1: the hits it reports and the
// tags it blocks, plus the scan_guard / scan_unblock it was last sent.
type scanCore struct {
	hits    []coreScanHit
	blocked map[string]coreScanHit
}

func scanManager(t *testing.T, features []string) (*flvxManager, *fakeFLVXCore, *scanCore) {
	t.Helper()
	m, f := fakeFLVXManager(t)
	m.state.Payload.Artifact = flvx.Artifact{SHA256: strings.Repeat("c", 64), Version: "f6f5844"}
	m.readFeatures = func(context.Context) ([]string, error) { return features, nil }
	core := &scanCore{blocked: map[string]coreScanHit{}}
	f.scan = func(after string) (int, any) {
		report := coreScanReport{Version: 1, Hits: []coreScanHit{}, Blocked: []coreScanHit{}}
		for _, h := range core.hits {
			if after == "" || h.Seq > mustInt(t, after) {
				report.Hits = append(report.Hits, h)
			}
		}
		for _, h := range core.blocked {
			report.Blocked = append(report.Blocked, h)
		}
		return 200, report
	}
	return m, f, core
}

func mustInt(t *testing.T, s string) int64 {
	var v int64
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (c *scanCore) hit(tag string, seq int64) coreScanHit {
	h := coreScanHit{Identity: tag, Type: "horizontal", Count: 100, Port: 22, WindowS: 60, At: time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC), Seq: seq}
	c.hits = append(c.hits, h)
	c.blocked[tag] = h
	return h
}

// sent decodes the scan fields of the last /v1/config body.
func sent(t *testing.T, f *fakeFLVXCore) (map[string]any, []coreScanUnblock) {
	t.Helper()
	var body struct {
		ScanGuard   map[string]any    `json:"scan_guard"`
		ScanUnblock []coreScanUnblock `json:"scan_unblock"`
	}
	if err := json.Unmarshal(f.raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.ScanGuard, body.ScanUnblock
}

func blockedIdentity(f *fakeFLVXCore, tag, user string) bool {
	if f.limits.BlockedIdentities == nil {
		return false
	}
	for _, id := range *f.limits.BlockedIdentities {
		if id == (coreIdentity{InboundTag: tag, User: user}) {
			return true
		}
	}
	return false
}

func setScanGuard(m *flvxManager, g *flvx.ScanGuard, enabled bool) {
	nodes := append([]flvx.Node(nil), m.state.Payload.Desired.Nodes...)
	for i := range nodes {
		on := enabled
		nodes[i].Enabled = &on
	}
	m.state.Payload.Version++
	m.state.Payload.Desired.Revision++
	m.state.Payload.Desired.Nodes = nodes
	m.state.Payload.Desired.ScanGuard = g
}

func pass(t *testing.T, m *flvxManager) {
	t.Helper()
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Guard off (FLVX sends none, or a Core without the feature): the /v1/config
// body is today's (v0.7.17's), byte for byte. A Core with the feature is read
// once per epoch (a sticky block may predate a Helper restart), an old Core
// never.
func TestFLVXScanGuardOffUnchanged(t *testing.T) {
	settings := defaultConnectionSettings()
	v := int64(5)
	settings.Ports = []portConnectionSettings{{InboundTag: "flvxl-1", MaxTotalConnections: &v}}
	settings.BlockedIdentities = []coreIdentity{{InboundTag: "flvxl-2", User: "flvxl-2"}}
	old, _ := json.Marshal(settings.coreConfig())
	wrapped, _ := json.Marshal(flvxCoreConfig{coreConfig: settings.coreConfig()})
	if !bytes.Equal(old, wrapped) {
		t.Fatalf("wrapper changed the body:\n%s\n%s", old, wrapped)
	}
	bodies := map[string][][]byte{}
	for _, name := range []string{"capable core", "old core"} {
		features := map[string][]string{"capable core": {"anytls.downlink_stats", flvx.CoreScanGuardFeature}, "old core": {"anytls.downlink_stats"}}[name]
		t.Run(name, func(t *testing.T) {
			m, f, _ := scanManager(t, features)
			for i := 0; i < 20; i++ {
				pass(t, m)
				if bytes.Contains(f.raw, []byte("scan_")) {
					t.Fatalf("scan fields sent: %s", f.raw)
				}
				// The body is exactly a coreConfig (v0.7.17's type) encoding.
				var limits coreConfig
				if err := json.Unmarshal(f.raw, &limits); err != nil {
					t.Fatal(err)
				}
				if again, _ := json.Marshal(limits); !bytes.Equal(again, f.raw) {
					t.Fatalf("body is not today's:\n%s\n%s", f.raw, again)
				}
				bodies[name] = append(bodies[name], append([]byte(nil), f.raw...))
			}
			if want := map[string]int{"capable core": 1, "old core": 0}[name]; len(f.scans) != want {
				t.Fatalf("/v1/scan read %d times with the guard off, want %d", len(f.scans), want)
			}
			// A new Core epoch is read once again.
			f.epoch = f.epoch.Add(time.Minute)
			for i := 0; i < 5; i++ {
				pass(t, m)
			}
			if want := map[string]int{"capable core": 2, "old core": 0}[name]; len(f.scans) != want {
				t.Fatalf("/v1/scan read %d times after a Core restart, want %d", len(f.scans), want)
			}
			st := m.state.Report.ScanGuard
			if st == nil || st.State != "off" || st.CoreReady != (name == "capable core") || len(st.Hits) != 0 {
				t.Fatalf("status %+v", st)
			}
		})
	}
	if !reflect.DeepEqual(bodies["capable core"], bodies["old core"]) {
		t.Fatal("capable and old Core got different /v1/config bodies with the guard off")
	}
}

// A read that fails with the guard off is retried on the next pass, and only
// until one succeeds.
func TestFLVXScanGuardOffReadRetried(t *testing.T) {
	m, f, _ := scanManager(t, []string{flvx.CoreScanGuardFeature})
	ok := f.scan
	f.scan = func(string) (int, any) { return 500, map[string]string{"error": "boom"} }
	pass(t, m)
	pass(t, m)
	if len(f.scans) != 2 {
		t.Fatalf("reads %d", len(f.scans))
	}
	if st := m.state.Report.ScanGuard; st.State != "off" {
		t.Fatalf("status %+v", st)
	}
	f.scan = ok
	for i := 0; i < 5; i++ {
		pass(t, m)
	}
	if len(f.scans) != 3 {
		t.Fatalf("reads %d", len(f.scans))
	}
}

// The Core's block is sticky (§8): FLVX records a hit and switches the guard
// off (or drops it), the Helper restarts while the Core keeps running, then
// 解除禁用 must still lift the block.
func TestFLVXScanGuardStickyBlockAfterHelperRestart(t *testing.T) {
	for _, name := range []string{"guard off", "guard dropped"} {
		t.Run(name, func(t *testing.T) {
			m, f, core := scanManager(t, []string{flvx.CoreScanGuardFeature})
			g := &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-one"}}
			setScanGuard(m, g, true)
			pass(t, m)
			core.hit("flvxl-one", 77)
			pass(t, m)
			if !reflect.DeepEqual(m.state.Report.ScanGuard.Blocked, []string{"flvxl-one"}) {
				t.Fatalf("status %+v", m.state.Report.ScanGuard)
			}
			// FLVX records the hit, disables the node and switches the guard
			// off; or it drops the guard while the Helper is down (applied, a
			// dropped guard lifts every block at once).
			var next *flvx.ScanGuard
			if name == "guard off" {
				next = &flvx.ScanGuard{Enabled: false, Identities: []string{"flvxl-one"}, Seen: 77}
				setScanGuard(m, next, false)
				pass(t, m)
				if _, unblock := sent(t, f); unblock != nil {
					t.Fatalf("disabled node unblocked: %v", unblock)
				}
			}
			// Helper restart; the Core (and its block) keeps running.
			m.scan = flvxScanPass{}
			setScanGuard(m, next, true) // 解除禁用
			var unblock []coreScanUnblock
			for i := 0; i < 5 && unblock == nil; i++ {
				pass(t, m)
				_, unblock = sent(t, f)
			}
			if !reflect.DeepEqual(unblock, []coreScanUnblock{{Identity: "flvxl-one", Seq: 77}}) {
				t.Fatalf("sticky block not lifted after a Helper restart: unblock %v, reads %d", unblock, len(f.scans))
			}
			if blockedIdentity(f, "flvxl-one", "") {
				t.Fatal("re-enabled node still blocked by the Helper")
			}
			// Lifted: back to no reads with the guard off.
			delete(core.blocked, "flvxl-one")
			pass(t, m)
			reads := len(f.scans)
			for i := 0; i < 5; i++ {
				pass(t, m)
			}
			if _, unblock := sent(t, f); unblock != nil || len(f.scans) != reads {
				t.Fatalf("after lift: unblock %v, reads %d → %d", unblock, reads, len(f.scans))
			}
		})
	}
}

// Guard on: a Helper restart re-learns the Core's blocks on its first read.
func TestFLVXScanGuardOnHelperRestartRelearns(t *testing.T) {
	m, f, core := scanManager(t, []string{flvx.CoreScanGuardFeature})
	g := &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-one"}}
	setScanGuard(m, g, true)
	core.hit("flvxl-one", 5)
	pass(t, m)
	recorded := *g
	recorded.Seen = 5
	setScanGuard(m, &recorded, false)
	pass(t, m)
	m.scan = flvxScanPass{}
	setScanGuard(m, &recorded, true)
	pass(t, m)
	if _, unblock := sent(t, f); !reflect.DeepEqual(unblock, []coreScanUnblock{{Identity: "flvxl-one", Seq: 5}}) {
		t.Fatalf("unblock %v", unblock)
	}
}

// The whole cycle: hit → Core block + Helper hold (survives Helper and Core
// restart) → FLVX records it and disables the node → 解除禁用 lifts it.
func TestFLVXScanGuardCycle(t *testing.T) {
	m, f, core := scanManager(t, []string{flvx.CoreScanGuardFeature})
	g := &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-one"}, Total: 2000, WebPorts: []int{80, 443}}
	setScanGuard(m, g, true)
	pass(t, m)
	guard, unblock := sent(t, f)
	if guard == nil || !reflect.DeepEqual(guard["identities"], []any{"flvxl-one"}) || guard["total"] != float64(2000) || guard["horizontal"] != float64(100) || unblock != nil {
		t.Fatalf("guard %v unblock %v", guard, unblock)
	}
	if st := m.state.Report.ScanGuard; st.State != "on" || !st.CoreReady {
		t.Fatalf("status %+v", st)
	}
	// One read per pass, incremental once a hit was seen.
	hit := core.hit("flvxl-one", 1793709225987084)
	pass(t, m)
	if len(f.scans) != 2 || f.scans[1] != "" {
		t.Fatalf("reads %v", f.scans)
	}
	pass(t, m)
	if len(f.scans) != 3 || f.scans[2] != "1793709225987084" {
		t.Fatalf("reads %v", f.scans)
	}
	st := m.state.Report.ScanGuard
	want := flvx.ScanHit{ID: "one", InboundTag: "flvxl-one", Type: "horizontal", Count: 100, Port: 22, WindowS: 60, At: hit.At, Seq: hit.Seq}
	if len(st.Hits) != 1 || st.Hits[0] != want || !reflect.DeepEqual(st.Blocked, []string{"flvxl-one"}) {
		t.Fatalf("status %+v", st)
	}
	if !blockedIdentity(f, "flvxl-one", "flvxl-one") || !blockedIdentity(f, "flvxl-one", "") {
		t.Fatalf("held tag not blocked: %+v", f.limits.BlockedIdentities)
	}
	if _, unblock = sent(t, f); unblock != nil {
		t.Fatalf("unrecorded hit unblocked: %v", unblock)
	}
	// Helper restart, then Core restart (block and ring gone): the hit is
	// still held from state.json.
	restarted := &flvxManager{dir: m.dir}
	if err := restarted.load(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.state.ScanPending) != 1 || restarted.state.ScanPending[0] != want {
		t.Fatalf("pending not persisted: %+v", restarted.state.ScanPending)
	}
	core.hits, core.blocked = nil, map[string]coreScanHit{}
	f.epoch = f.epoch.Add(time.Minute)
	pass(t, m)
	if !blockedIdentity(f, "flvxl-one", "flvxl-one") || len(m.state.Report.ScanGuard.Hits) != 1 {
		t.Fatalf("Core restart lifted the hold: %+v", f.limits.BlockedIdentities)
	}
	if f.scans[len(f.scans)-1] != "" {
		t.Fatal("new Core read incrementally from the old epoch")
	}
	// FLVX records the hit and disables the node in the same step.
	core.hit("flvxl-one", hit.Seq+1) // the Core blocked it again meanwhile
	pass(t, m)
	recorded := *g
	recorded.Seen = hit.Seq + 1
	setScanGuard(m, &recorded, false)
	pass(t, m)
	if st := m.state.Report.ScanGuard; len(st.Hits) != 0 {
		t.Fatalf("recorded hits still reported: %+v", st.Hits)
	}
	guard, unblock = sent(t, f)
	if guard != nil || unblock != nil {
		t.Fatalf("disabled node: guard %v unblock %v", guard, unblock)
	}
	if !blockedIdentity(f, "flvxl-one", "flvxl-one") {
		t.Fatal("disabled node not blocked")
	}
	// Agent-style re-push keeps it.
	pass(t, m)
	if _, unblock = sent(t, f); unblock != nil {
		t.Fatal("re-push unblocked a disabled node")
	}
	// 解除禁用.
	setScanGuard(m, &recorded, true)
	pass(t, m)
	_, unblock = sent(t, f)
	if !reflect.DeepEqual(unblock, []coreScanUnblock{{Identity: "flvxl-one", Seq: hit.Seq + 1}}) {
		t.Fatalf("unblock %v", unblock)
	}
	if blockedIdentity(f, "flvxl-one", "flvxl-one") {
		t.Fatal("re-enabled node still blocked by the Helper")
	}
	delete(core.blocked, "flvxl-one")
	pass(t, m)
	pass(t, m)
	if guard, unblock = sent(t, f); unblock != nil || guard == nil {
		t.Fatalf("after unblock: guard %v unblock %v", guard, unblock)
	}
	if st := m.state.Report.ScanGuard; st.State != "on" || len(st.Blocked) != 0 || len(st.Hits) != 0 {
		t.Fatalf("status %+v", st)
	}
}

// A newer hit than seen is never lifted by 解除禁用 of an older one; a node
// FLVX removed, or FLVX dropping the guard object, lifts every block.
func TestFLVXScanGuardUnblockRules(t *testing.T) {
	m, f, core := scanManager(t, []string{flvx.CoreScanGuardFeature})
	g := &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-one"}, Seen: 50}
	setScanGuard(m, g, true)
	core.hit("flvxl-one", 60)
	pass(t, m)
	if _, unblock := sent(t, f); unblock != nil {
		t.Fatalf("newer hit lifted: %v", unblock)
	}
	if !blockedIdentity(f, "flvxl-one", "") {
		t.Fatal("newer hit not held")
	}
	// Switched off but the hit unrecorded: still held, nothing counted.
	off := *g
	off.Enabled = false
	setScanGuard(m, &off, true)
	pass(t, m)
	if guard, unblock := sent(t, f); guard != nil || unblock != nil || !blockedIdentity(f, "flvxl-one", "") {
		t.Fatalf("switched off: guard %v unblock %v", guard, unblock)
	}
	if st := m.state.Report.ScanGuard; st.State != "off" || len(st.Hits) != 1 {
		t.Fatalf("status %+v", st)
	}
	// FLVX stops sending the object (older panel): every block is lifted.
	setScanGuard(m, nil, true)
	pass(t, m)
	if _, unblock := sent(t, f); !reflect.DeepEqual(unblock, []coreScanUnblock{{Identity: "flvxl-one", Seq: 60}}) {
		t.Fatalf("unblock %v", unblock)
	}
	if blockedIdentity(f, "flvxl-one", "") || len(m.state.ScanPending) != 0 {
		t.Fatal("hold kept without a guard")
	}
	// Still polled while a block is to be lifted, then never again.
	delete(core.blocked, "flvxl-one")
	reads := len(f.scans)
	for i := 0; i < 5; i++ {
		pass(t, m)
	}
	if len(f.scans) != reads+1 {
		t.Fatalf("reads %d → %d", reads, len(f.scans))
	}
	// A removed node's block is lifted too.
	setScanGuard(m, &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-one"}}, true)
	core.blocked["flvxl-gone"] = coreScanHit{Identity: "flvxl-gone", Type: "total", Seq: 70}
	pass(t, m)
	if _, unblock := sent(t, f); !reflect.DeepEqual(unblock, []coreScanUnblock{{Identity: "flvxl-gone", Seq: 70}}) {
		t.Fatalf("unblock %v", unblock)
	}
}

// A Core without scan_guard.v1 gets nothing new; a Core that refuses the
// fields after all gets today's body and the share says so.
func TestFLVXScanGuardOldCore(t *testing.T) {
	m, f, _ := scanManager(t, []string{"anytls.downlink_stats"})
	setScanGuard(m, &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-one"}}, true)
	pass(t, m)
	if bytes.Contains(f.raw, []byte("scan_")) || len(f.scans) != 0 {
		t.Fatalf("old Core got scan fields or reads: %s %v", f.raw, f.scans)
	}
	if st := m.state.Report.ScanGuard; st.State != "core_update" || st.CoreReady {
		t.Fatalf("status %+v", st)
	}

	m, f, _ = scanManager(t, []string{flvx.CoreScanGuardFeature})
	f.reject = func(raw []byte) string {
		if bytes.Contains(raw, []byte("scan_guard")) {
			return `json: unknown field "scan_guard"`
		}
		return ""
	}
	setScanGuard(m, &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-one"}}, true)
	pass(t, m)
	if bytes.Contains(f.raw, []byte("scan_")) || f.present["flvxl-one"] != true {
		t.Fatalf("fallback body %s, node present %v", f.raw, f.present)
	}
	if st := m.state.Report.ScanGuard; st.State != "core_update" || st.Error == "" || st.CoreReady {
		t.Fatalf("status %+v", st)
	}
	pass(t, m)
	if bytes.Contains(f.raw, []byte("scan_")) {
		t.Fatal("refused fields sent again in the same epoch")
	}
}

// An invalid guard (Custom never signs one) is not applied.
func TestFLVXScanGuardInvalid(t *testing.T) {
	m, f, _ := scanManager(t, []string{flvx.CoreScanGuardFeature})
	setScanGuard(m, &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-other"}}, true)
	pass(t, m)
	if bytes.Contains(f.raw, []byte("scan_guard")) {
		t.Fatalf("invalid guard sent: %s", f.raw)
	}
	if st := m.state.Report.ScanGuard; st.State != "error" {
		t.Fatalf("status %+v", st)
	}
}

// A report the Core cannot give leaves blocks and holds as they were.
func TestFLVXScanGuardReadFailure(t *testing.T) {
	m, f, core := scanManager(t, []string{flvx.CoreScanGuardFeature})
	setScanGuard(m, &flvx.ScanGuard{Enabled: true, Identities: []string{"flvxl-one"}}, true)
	core.hit("flvxl-one", 9)
	pass(t, m)
	f.scan = func(string) (int, any) { return 500, map[string]string{"error": "boom"} }
	pass(t, m)
	st := m.state.Report.ScanGuard
	if st.State != "error" || len(st.Hits) != 1 || !reflect.DeepEqual(st.Blocked, []string{"flvxl-one"}) || !blockedIdentity(f, "flvxl-one", "") {
		t.Fatalf("status %+v", st)
	}
}
