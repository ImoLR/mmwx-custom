package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// 禁止端口扫描 for the shared FLVX Core (contract landing-scan-guard-v1 §9),
// the same steps FLVX's landing agent takes for its own Core
// (go-gost/x/landing/scan_guard.go): pass FLVX's scan_guard through, read the
// Core's hits once per pass, keep a hit's node blocked until FLVX has
// recorded it, then let the node's enabled flag decide. Only a Core build
// listing scan_guard.v1 gets scan_guard / scan_unblock (an older one rejects
// the whole /v1/config), and /v1/scan is read once per Core epoch after the
// Helper starts (a block is sticky and survives a Helper restart), then only
// while the guard is on or a block is still to be lifted. Nothing is logged
// per connection or per hit.

// coreScanHit / coreScanReport are the Core's GET /v1/scan wire shape (§8.2).
type coreScanHit struct {
	Identity string    `json:"identity"`
	Type     string    `json:"type"`
	Count    int       `json:"count"`
	Port     int       `json:"port"`
	Host     string    `json:"host"`
	WindowS  int       `json:"window_s"`
	At       time.Time `json:"at"`
	Seq      int64     `json:"seq"`
}

type coreScanReport struct {
	Version int           `json:"version"`
	Hits    []coreScanHit `json:"hits"`
	Blocked []coreScanHit `json:"blocked"`
}

// coreScanUnblock lifts the Core block caused by the hit seq (§8.1).
type coreScanUnblock struct {
	Identity string `json:"identity"`
	Seq      int64  `json:"seq"`
}

// flvxCoreConfig is the shared Core's /v1/config: today's limits plus, for a
// Core with scan_guard.v1 only, the guard. Both are omitted when absent, so
// the body is byte-identical to before for every share without the guard.
type flvxCoreConfig struct {
	coreConfig
	ScanGuard   map[string]any    `json:"scan_guard,omitempty"`
	ScanUnblock []coreScanUnblock `json:"scan_unblock,omitempty"`
}

// flvxScanPass is the guard state of the running Core (memory only: after a
// Core restart its ring and blocks are empty, after a Helper restart one read
// of the bounded ring catches up, whatever the guard state).
type flvxScanPass struct {
	epoch    string
	after    int64            // highest seq read from /v1/scan in epoch
	blocked  map[string]int64 // Core-blocked tag -> seq of its hit
	synced   bool             // a /v1/scan read of epoch succeeded
	err      string
	rejected string // epoch whose Core refused the scan fields
	reads    int
}

// scanReport reads GET /v1/scan?after=.
func (client *coreClient) scanReport(ctx context.Context, after int64) (coreScanReport, error) {
	path := "http://mmwxc-core/v1/scan"
	if after > 0 {
		path += "?after=" + strconv.FormatInt(after, 10)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return coreScanReport{}, err
	}
	response, err := client.client.Do(request)
	if err != nil {
		return coreScanReport{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return coreScanReport{}, fmt.Errorf("core scan report HTTP %d", response.StatusCode)
	}
	var report coreScanReport
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&report); err != nil {
		return coreScanReport{}, fmt.Errorf("decode core scan report: %w", err)
	}
	if report.Version != 1 || len(report.Hits) > flvx.MaxScanHits || len(report.Blocked) > flvx.MaxNodes*4 {
		return coreScanReport{}, fmt.Errorf("unsupported core scan report")
	}
	return report, nil
}

// scanCapable: the installed FLVX Core build lists scan_guard.v1 and has not
// refused the scan fields in this epoch.
func (m *flvxManager) scanCapable() bool {
	return m.coreFeatures()[flvx.CoreScanGuardFeature] && (m.scan.rejected == "" || m.scan.rejected != m.scan.epoch)
}

// scanGuard is FLVX's setting when it is structurally valid (Custom checked
// it already; this only guards the Helper's own maps), else nil.
func (m *flvxManager) scanGuard() (*flvx.ScanGuard, bool) {
	d := m.state.Payload.Desired
	if d.ScanGuard == nil {
		return nil, true
	}
	if len(flvx.ValidateScanGuard(d)) > 0 {
		return nil, false
	}
	return d.ScanGuard, true
}

// scanPass runs once per reconcile of a running Core, before the limits are
// pushed: the Core's new hits and current blocks (one control-socket GET,
// empty lists in the steady state), then the pending hits FLVX has recorded
// are dropped. The first read of an epoch happens even with the guard off:
// a Core block is sticky (§8), so after a Helper restart it would otherwise
// never be learnt, and 解除禁用 or dropping the guard could not lift it.
func (m *flvxManager) scanPass(ctx context.Context, epoch string) {
	if m.scan.epoch != epoch {
		m.scan = flvxScanPass{epoch: epoch, rejected: m.scan.rejected}
	}
	g, _ := m.scanGuard()
	before := scanPendingKey(m.state.ScanPending)
	m.scan.err = ""
	if !m.scanCapable() {
		m.scan.blocked = nil
	} else if g != nil && g.Enabled || len(m.scan.blocked) > 0 || !m.scan.synced {
		m.scan.reads++
		read := m.readScan
		if read == nil {
			read = m.core.scanReport
		}
		report, err := read(ctx, m.scan.after)
		if err != nil {
			m.scan.err = "FLVX Core scan report unavailable"
		} else {
			m.scan.synced = true
			m.scan.blocked = map[string]int64{}
			for _, b := range report.Blocked {
				if strings.HasPrefix(b.Identity, "flvxl-") && b.Seq > 0 && b.Seq <= flvx.MaxScanSeq {
					m.scan.blocked[b.Identity] = b.Seq
				}
			}
			for _, h := range append(report.Hits, report.Blocked...) {
				if h.Seq > m.scan.after && h.Seq <= flvx.MaxScanSeq {
					m.scan.after = h.Seq
				}
				m.addScanHit(g, h)
			}
		}
	}
	m.pruneScanHits(g)
	if scanPendingKey(m.state.ScanPending) != before {
		// A hit FLVX has not recorded survives a Helper restart.
		m.logs.report("FLVX scan hits persistence", m.persist())
	}
}

// addScanHit keeps a hit FLVX has not recorded yet (once per seq).
func (m *flvxManager) addScanHit(g *flvx.ScanGuard, h coreScanHit) {
	if g == nil || h.Seq <= g.Seen || !strings.HasPrefix(h.Identity, "flvxl-") {
		return
	}
	for _, p := range m.state.ScanPending {
		if p.Seq == h.Seq {
			return
		}
	}
	hit := flvx.ScanHit{ID: strings.TrimPrefix(h.Identity, "flvxl-"), InboundTag: h.Identity, Type: h.Type, Count: h.Count, Port: h.Port, Host: h.Host, WindowS: h.WindowS, At: h.At.UTC(), Seq: h.Seq}
	if !flvx.ValidScanHit(hit) {
		// The block stands either way; FLVX must still learn of it.
		hit.Host = ""
	}
	if flvx.ValidScanHit(hit) {
		m.state.ScanPending = append(m.state.ScanPending, hit)
	}
}

// pruneScanHits drops hits FLVX has recorded (seq <= seen) and hits of tags
// no longer in the desired state, and keeps the newest MaxScanHits.
func (m *flvxManager) pruneScanHits(g *flvx.ScanGuard) {
	tags := map[string]bool{}
	for _, n := range m.state.Payload.Desired.Nodes {
		tags[n.Tag()] = true
	}
	kept := []flvx.ScanHit{}
	for _, p := range m.state.ScanPending {
		if g != nil && p.Seq > g.Seen && tags[p.InboundTag] {
			kept = append(kept, p)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Seq < kept[j].Seq })
	if len(kept) > flvx.MaxScanHits {
		kept = kept[len(kept)-flvx.MaxScanHits:]
	}
	if len(kept) == 0 {
		kept = nil
	}
	m.state.ScanPending = kept
}

// scanHeld are the tags with a hit FLVX has not recorded. The Helper blocks
// them itself (blocked_identities), so a Core restart before FLVX saw the hit
// cannot lift the block.
func (m *flvxManager) scanHeld() []string {
	held := map[string]bool{}
	for _, p := range m.state.ScanPending {
		held[p.InboundTag] = true
	}
	tags := make([]string, 0, len(held))
	for tag := range held {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

// scanControl returns the Core's scan_guard (nil = omit) and scan_unblock
// entries for the next /v1/config; nothing for a Core without the feature.
// A Core block is lifted once FLVX recorded its hit and the node is enabled
// again (解除禁用), when the node is gone, or when FLVX sends no guard at all.
func (m *flvxManager) scanControl(wanted map[string]string) (map[string]any, []coreScanUnblock) {
	if !m.scanCapable() {
		return nil, nil
	}
	g, _ := m.scanGuard()
	enabled := map[string]bool{}
	for _, n := range m.state.Payload.Desired.Nodes {
		enabled[n.Tag()] = n.Enabled != nil && *n.Enabled
	}
	var unblock []coreScanUnblock
	tags := make([]string, 0, len(m.scan.blocked))
	for tag := range m.scan.blocked {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		seq := m.scan.blocked[tag]
		_, known := enabled[tag]
		if g == nil || !known || seq <= g.Seen && enabled[tag] {
			unblock = append(unblock, coreScanUnblock{Identity: tag, Seq: seq})
		}
	}
	if g == nil || !g.Enabled {
		return nil, unblock
	}
	ids := []string{}
	for _, id := range g.Identities {
		if wanted[id] != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, unblock
	}
	return g.CoreConfig(ids), unblock
}

// applyFLVXConfig pushes the limits with the guard. A Core that refuses the
// scan fields after all (unknown field) gets today's body and is not sent
// them again in this epoch; the share reports the guard as not in force.
func (m *flvxManager) applyFLVXConfig(ctx context.Context, settings connectionSettings, wanted map[string]string) error {
	config := flvxCoreConfig{coreConfig: settings.coreConfig()}
	config.ScanGuard, config.ScanUnblock = m.scanControl(wanted)
	unknown, err := m.core.applyConfig(ctx, config)
	if err == nil || !unknown || config.ScanGuard == nil && config.ScanUnblock == nil {
		return err
	}
	m.scan.rejected = m.scan.epoch
	m.scan.err = "FLVX Core refused scan_guard"
	_, err = m.core.applyConfig(ctx, flvxCoreConfig{coreConfig: settings.coreConfig()})
	return err
}

// scanStatus is the Status.ScanGuard report.
func (m *flvxManager) scanStatus() *flvx.ScanGuardStatus {
	capable := m.scanCapable()
	st := &flvx.ScanGuardStatus{State: "off", Hits: append([]flvx.ScanHit{}, m.state.ScanPending...), CoreReady: capable}
	g, valid := m.scanGuard()
	switch {
	case !valid:
		st.State, st.Error = "error", "invalid scan_guard"
	case g == nil || !g.Enabled || m.state.Payload.Status != "active":
	case !capable:
		st.State = "core_update"
		if m.scan.err != "" {
			st.Error = m.scan.err
		}
	case m.scan.err != "":
		st.State, st.Error = "error", m.scan.err
	default:
		st.State = "on"
	}
	for tag := range m.scan.blocked {
		st.Blocked = append(st.Blocked, tag)
	}
	sort.Strings(st.Blocked)
	return st
}

func scanPendingKey(hits []flvx.ScanHit) string {
	raw, _ := json.Marshal(hits)
	return string(raw)
}
