package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func (m *flvxManager) api(ctx context.Context, command string, args ...string) ([]byte, error) {
	// Rule replacement builds geo matchers inside the CPU-capped Core.
	limit, rpc := 5*time.Second, "3"
	if command == "adrules" {
		limit, rpc = 30*time.Second, "25"
	}
	child, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	argv := []string{"api", command, "--server=127.0.0.1:" + strconv.Itoa(m.state.APIPort), "--timeout=" + rpc}
	argv = append(argv, args...)
	cmd := exec.CommandContext(child, m.binary, argv...)
	// The CLI (not the Core) reads the pinned geo files for rules it builds.
	cmd.Env = append(os.Environ(), "XRAY_LOCATION_ASSET="+m.geoDir())
	raw, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		missing := errors.As(err, &exit) && strings.Contains(string(exit.Stderr)+string(raw), "code = Unimplemented")
		return nil, flvxCLIError{command: command, unimplemented: missing}
	}
	if len(raw) > 4<<20 {
		return nil, errors.New("FLVX gRPC response too large")
	}
	return raw, nil
}

func (m *flvxManager) collect(ctx context.Context, snapshot coreSnapshotResponse) error {
	raw, err := m.apiCall(ctx, "statsquery", "-pattern", "inbound>>>")
	if err != nil {
		return err
	}
	var response struct {
		Stat []struct {
			Name  string      `json:"name"`
			Value json.Number `json:"value"`
		} `json:"stat"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return errors.New("invalid FLVX inbound statistics")
	}
	epoch := snapshot.StartedAt.UTC().Format(time.RFC3339Nano)
	period := flvx.Period(m.now(), m.state.Payload.Config.ResetDay)
	if m.state.Report.Period != period {
		m.state.Report.Period = period
		m.state.Report.TrafficUsedBytes = 0
	}
	type sample struct{ up, down int64 }
	samples := map[string]sample{}
	for _, stat := range response.Stat {
		parts := strings.Split(stat.Name, ">>>")
		if len(parts) != 4 || parts[0] != "inbound" || parts[2] != "traffic" || !strings.HasPrefix(parts[1], "flvxl-") {
			continue
		}
		id := strings.TrimPrefix(parts[1], "flvxl-")
		if stat.Value == "" {
			stat.Value = "0"
		}
		v, err := stat.Value.Int64()
		if err != nil || v < 0 {
			return errors.New("invalid FLVX raw counter")
		}
		s, ok := samples[id]
		if !ok {
			if c := m.state.Counters[id]; c != nil && c.Epoch == epoch {
				s.up, s.down = c.RawUp, c.RawDown
			}
		}
		switch parts[3] {
		case "uplink":
			s.up = v
		case "downlink":
			s.down = v
		default:
			continue
		}
		samples[id] = s
	}
	for id, s := range samples {
		c := m.state.Counters[id]
		if c == nil {
			c = &flvx.Counter{}
			m.state.Counters[id] = c
		}
		m.state.Report.TrafficUsedBytes = flvx.AddBytes(m.state.Report.TrafficUsedBytes, c.Merge(epoch, s.up, s.down))
	}
	m.state.Report.Core = flvx.Core{State: "running", Epoch: epoch, Version: m.state.Payload.Artifact.Version}
	return nil
}

func (m *flvxManager) pinnedReality(ctx context.Context, n flvx.Node) (flvx.Node, error) {
	if n.VLESS == nil {
		return n, nil
	}
	raw, _ := json.Marshal(n)
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	pin := m.state.RealityPins[n.Tag()]
	if pin.Hash == hash && flvxValidRealityPin(pin.Dest) {
		v := *n.VLESS
		v.Reality.Dest = pin.Dest
		n.VLESS = &v
		return n, nil
	}
	host, port, err := net.SplitHostPort(n.VLESS.Reality.Dest)
	if err != nil {
		return n, err
	}
	child, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	lookup := m.lookupHost
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	ips, err := lookup(child, host)
	if err != nil || len(ips) == 0 {
		return n, errors.New("Reality destination DNS unavailable")
	}
	for _, ip := range ips {
		if net.ParseIP(ip) == nil || flvx.PrivateHost(ip) {
			return n, errors.New("Reality destination must resolve exclusively to public addresses")
		}
	}
	// Reality's camouflage dial is outside normal outbound routing. Pin its
	// validated address to prevent DNS rebinding into private destinations.
	sort.Strings(ips)
	v := *n.VLESS
	v.Reality.Dest = net.JoinHostPort(ips[0], port)
	n.VLESS = &v
	if m.state.RealityPins == nil {
		m.state.RealityPins = map[string]flvxRealityPin{}
	}
	m.state.RealityPins[n.Tag()] = flvxRealityPin{Hash: hash, Dest: v.Reality.Dest}
	return n, nil
}

func flvxValidRealityPin(dest string) bool {
	host, _, err := net.SplitHostPort(dest)
	return err == nil && net.ParseIP(host) != nil && !flvx.PrivateHost(host)
}

func (m *flvxManager) wanted(ctx context.Context) (map[string]string, map[string]string) {
	wanted, problems := map[string]string{}, map[string]string{}
	p := m.state.Payload
	ports, err := m.readOfficial()
	if err != nil {
		for _, n := range p.Desired.Nodes {
			problems[n.ID] = "official inbound configuration unavailable"
		}
		return wanted, problems
	}
	ports = append(ports, p.OfficialPorts...)
	validation := flvx.ValidateDesired(p.Desired, p.Config, ports)
	for _, e := range validation {
		problems[e.NodeID] = e.Message
	}
	for _, n := range p.Desired.Nodes {
		if !flvx.Active(p.Status, p.Config, m.state.Report.TrafficUsedBytes, n, m.now()) || problems[n.ID] != "" {
			continue
		}
		if n.Port == m.state.APIPort {
			problems[n.ID] = "port collides with FLVX control API"
			continue
		}
		if reason := m.withheld[n.Tag()]; reason != "" {
			problems[n.ID] = reason
			continue
		}
		node, err := m.pinnedReality(ctx, n)
		if err != nil {
			if applied := m.state.Applied[n.Tag()]; applied != "" && flvxValidRealityPin(m.state.RealityPins[n.Tag()].Dest) {
				wanted[n.Tag()] = applied
				m.logs.report("FLVX Reality destination retained", errors.New("destination unavailable or private; keeping the applied public pin"))
				continue
			}
			problems[n.ID] = "Reality destination is unavailable or private"
			continue
		}
		raw, _ := json.Marshal(flvx.Inbound(node))
		wanted[n.Tag()] = string(raw)
	}
	return wanted, problems
}

func (m *flvxManager) limits(ctx context.Context, snapshot coreSnapshotResponse, wanted map[string]string, changing map[string]bool) error {
	settings := defaultConnectionSettings()
	// The machine owner's CLOSE_WAIT timeout (machine protection) is a plain
	// Core default here; FLVX itself sends none.
	settings.DefaultCloseWaitTimeoutSeconds = m.machineCloseWaitSeconds()
	p := m.state.Payload
	if p.Config.MaxConnections > 0 {
		v := p.Config.MaxConnections
		settings.GlobalTotalLimitEnabled = true
		settings.MaxGlobalTotalConnections = &v
	}
	for _, n := range p.Desired.Nodes {
		port := portConnectionSettings{InboundTag: n.Tag()}
		if n.MaxConnections > 0 {
			v := n.MaxConnections
			port.MaxTotalConnections = &v
		}
		if n.MaxOnlineIPs > 0 {
			v := n.MaxOnlineIPs
			port.MaxInboundOnlineIPs = &v
		}
		settings.Ports = append(settings.Ports, port)
	}
	seen := map[coreIdentity]bool{}
	block := func(tag, user string) {
		if !strings.HasPrefix(tag, "flvxl-") || wanted[tag] != "" && !changing[tag] {
			return
		}
		identity := coreIdentity{InboundTag: tag, User: user}
		if !seen[identity] {
			settings.BlockedIdentities = append(settings.BlockedIdentities, identity)
			seen[identity] = true
		}
	}
	for _, n := range p.Desired.Nodes {
		block(n.Tag(), n.Tag())
		block(n.Tag(), "")
	}
	for tag := range m.state.Applied {
		block(tag, tag)
		block(tag, "")
	}
	for _, u := range snapshot.Users {
		block(u.Identity.InboundTag, u.Identity.User)
	}
	// This instance must support v7 blocks. Never fall back to old interfaces
	// which could leave established sessions alive after pause or quota expiry.
	_, err := m.core.applyConfig(ctx, settings.coreConfig())
	return err
}

func (m *flvxManager) record(snapshot coreSnapshotResponse, wanted map[string]string, problems map[string]string) error {
	connections := map[string]int64{}
	for _, u := range snapshot.Users {
		connections[strings.TrimPrefix(u.Identity.InboundTag, "flvxl-")] += u.CurrentTotal
	}
	online := flvxSnapshotOnlineIPs(snapshot)
	statuses := map[string]flvx.NodeStatus{}
	acknowledged := m.state.Payload.AckGeneration == m.state.Report.Generation && m.state.Payload.AckSequence >= m.state.Report.Sequence
	for _, old := range m.state.Report.Nodes {
		// PostgreSQL keeps the final tombstone and lifetime ledger. Stop sending
		// acknowledged, unchanged removals in the official Helper report channel.
		c := m.state.Counters[old.ID]
		if acknowledged && old.State == "removed" && connections[old.ID] == 0 && (c == nil || c.Up == old.UpBytes && c.Down == old.DownBytes) {
			continue
		}
		old.State = "removed"
		old.Connections = connections[old.ID]
		old.Error = ""
		old.OnlineIPCount, old.OnlineIPs = nil, nil
		statuses[old.ID] = old
	}
	for _, n := range m.state.Payload.Desired.Nodes {
		s := flvx.NodeStatus{ID: n.ID, State: "removed", Connections: connections[n.ID]}
		if wanted[n.Tag()] != "" && m.state.Applied[n.Tag()] == wanted[n.Tag()] {
			s.State = "active"
		}
		if problems[n.ID] != "" {
			s.State = "error"
			s.Error = problems[n.ID]
		}
		if s.State == "active" {
			s.OnlineIPCount, s.OnlineIPs = online[n.Tag()].OnlineIPCount, online[n.Tag()].OnlineIPs
		}
		statuses[n.ID] = s
	}
	ids := []string{}
	for id := range statuses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	m.state.Report.Nodes = []flvx.NodeStatus{}
	for _, id := range ids {
		s := statuses[id]
		if c := m.state.Counters[id]; c != nil {
			s.UpBytes, s.DownBytes = c.Up, c.Down
		}
		m.state.Report.Nodes = append(m.state.Report.Nodes, s)
	}
	return m.persistReport(m.reconciledPayload == [32]byte{})
}

func flvxSnapshotOnlineIPs(snapshot coreSnapshotResponse) map[string]flvx.NodeStatus {
	type sample struct {
		ips     map[string]int
		unknown bool
	}
	samples := map[string]*sample{}
	add := func(s *sample, entries coreOnlineIPs) {
		for _, entry := range entries {
			if entry.IP != "" && entry.Connections >= 0 {
				s.ips[entry.IP] += int(entry.Connections)
			}
		}
	}
	for _, user := range snapshot.Users {
		tag := user.Identity.InboundTag
		if !strings.HasPrefix(tag, "flvxl-") {
			continue
		}
		s := samples[tag]
		if s == nil {
			s = &sample{ips: map[string]int{}}
			samples[tag] = s
		}
		s.unknown = s.unknown || user.InboundOnlineIPs == nil
		add(s, user.InboundOnlineIPs)
	}
	result := map[string]flvx.NodeStatus{}
	for tag, s := range samples {
		if s.unknown {
			continue
		}
		count := len(s.ips)
		ips := make([]flvx.OnlineIP, 0, len(s.ips))
		for ip, connections := range s.ips {
			ips = append(ips, flvx.OnlineIP{IP: ip, Connections: connections})
		}
		sort.Slice(ips, func(i, j int) bool {
			if ips[i].Connections != ips[j].Connections {
				return ips[i].Connections > ips[j].Connections
			}
			return ips[i].IP < ips[j].IP
		})
		if len(ips) > 64 {
			limited := make([]flvx.OnlineIP, 64)
			copy(limited, ips)
			ips = limited
		}
		result[tag] = flvx.NodeStatus{OnlineIPCount: &count, OnlineIPs: &ips}
	}
	return result
}

func (m *flvxManager) reconcile(ctx context.Context) (result error) {
	if m.removed {
		return nil
	}
	p := m.state.Payload
	if m.reportAbsentInstance(p) {
		return nil
	}
	desired := p
	desired.AckGeneration, desired.AckSequence = "", 0
	desired.Period, desired.TrafficUsedBytes = "", 0
	rawDesired, _ := json.Marshal(desired)
	payloadHash := sha256.Sum256(rawDesired)
	defer func() {
		if result == nil {
			m.reconciledPayload = payloadHash
		} else {
			m.reconciledPayload = [32]byte{}
		}
	}()
	if p.Status == "revoked" && m.state.Report.Core.State == "stopped" && p.AckGeneration == m.state.Report.Generation && p.AckSequence >= m.state.Report.Sequence && m.state.Report.Sequence > 0 {
		if err := m.cleanup(ctx); err != nil {
			return err
		}
		// Keep the acknowledged tombstone in memory. No instance state remains.
		m.removed = true
		return nil
	}
	if p.Status == "revoked" {
		snapshot, err := m.core.snapshot(ctx)
		if err == nil {
			if err = m.collect(ctx, snapshot); err != nil {
				return err
			}
			if err = m.limits(ctx, snapshot, map[string]string{}, nil); err != nil {
				return err
			}
			if err = m.collect(ctx, snapshot); err != nil {
				return err
			}
			if err = m.record(snapshot, nil, nil); err != nil {
				return err
			}
		}
		if err = m.stop(ctx); err != nil {
			return err
		}
		m.state.Applied = map[string]string{}
		m.state.Report.Core.State = "stopped"
		m.state.Report.AppliedRevision = p.Desired.Revision
		return m.record(coreSnapshotResponse{}, nil, nil)
	}
	if p.Status == "paused" && m.state.APIPort == 0 {
		m.state.Report.Core.State = "stopped"
		m.state.Report.AppliedRevision = p.Desired.Revision
		return m.record(coreSnapshotResponse{}, nil, nil)
	}
	checkSetup := m.reconciledPayload != payloadHash || m.state.Report.Core.State != "running"
	checkInbounds := checkSetup
	for _, n := range m.state.Report.Nodes {
		checkInbounds = checkInbounds || n.State == "error"
	}
	// Save final counters only when setup may replace/recover this Core.
	if checkSetup {
		if snapshot, err := m.core.snapshot(ctx); err == nil {
			if err = m.collect(ctx, snapshot); err != nil {
				return err
			}
			if err = m.persist(); err != nil {
				return err
			}
		}
	}
	if err := m.setup(ctx); err != nil {
		return err
	}
	snapshot, err := m.core.snapshot(ctx)
	if err != nil {
		return errors.New("FLVX control socket unavailable")
	}
	if snapshot.Version < 7 || snapshot.StartedAt.IsZero() {
		return errors.New("FLVX Core requires control API v7")
	}
	if snapshot, err = m.ensureRouting(ctx, snapshot); err != nil {
		return err
	}
	if err = m.collect(ctx, snapshot); err != nil {
		return err
	}
	epoch := flvxEpoch(snapshot)
	if m.state.AppliedEpoch != epoch {
		checkInbounds = true
		m.state.Applied = map[string]string{}
		m.state.AppliedEpoch = epoch
		// A new Core lost every plan outbound and rule as well.
		m.state.EgressApplied, m.state.EgressCovered, m.state.EgressGeoBytes, m.state.EgressStale = "", nil, 0, false
	}
	// Outbounds and rules first: a node never serves before its plan applies.
	m.applyEgress(ctx, epoch)
	wanted, problems := m.wanted(ctx)
	if !checkInbounds && reflect.DeepEqual(wanted, m.state.Applied) {
		if err = m.limits(ctx, snapshot, wanted, nil); err != nil {
			return err
		}
		m.state.Report.AppliedRevision = p.Desired.Revision
		return m.record(snapshot, wanted, problems)
	}
	raw, err := m.apiCall(ctx, "lsi")
	if err != nil {
		return err
	}
	var list struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	if err = json.Unmarshal(raw, &list); err != nil {
		return errors.New("invalid FLVX inbound list")
	}
	present, changing := map[string]bool{}, map[string]bool{}
	for _, in := range list.Inbounds {
		if !strings.HasPrefix(in.Tag, "flvxl-") {
			return errors.New("unexpected inbound in isolated FLVX Core")
		}
		present[in.Tag] = true
		if wanted[in.Tag] == "" || m.state.Applied[in.Tag] != wanted[in.Tag] {
			changing[in.Tag] = true
		}
	}
	if err = m.limits(ctx, snapshot, wanted, changing); err != nil {
		return err
	}
	if len(changing) > 0 {
		if err = m.collect(ctx, snapshot); err != nil {
			return err
		}
		// Persist final cursors only when handlers/stat counters will be removed.
		if err = m.persist(); err != nil {
			return err
		}
	}
	for tag := range changing {
		if _, err = m.apiCall(ctx, "rmi", tag); err != nil {
			return err
		}
		delete(present, tag)
		delete(m.state.Applied, tag)
		if err = m.persist(); err != nil {
			return err
		}
	}
	// Clear temporary blocks before admitting replacement handlers.
	if err = m.limits(ctx, snapshot, wanted, nil); err != nil {
		return err
	}
	for tag, config := range wanted {
		if present[tag] && m.state.Applied[tag] == config {
			continue
		}
		if err = m.persist(); err != nil {
			return err
		}
		path := filepath.Join(m.dir, "inbound.json")
		if err = writeFLVXFile(path, []byte(`{"inbounds":[`+config+`]}`), 0600); err != nil {
			return err
		}
		_, err = m.apiCall(ctx, "adi", path)
		os.Remove(path)
		if err != nil {
			return err
		}
		m.state.Applied[tag] = config
		if err = m.persist(); err != nil {
			return err
		}
	}
	if snapshot, err = m.core.snapshot(ctx); err != nil {
		return err
	}
	if err = m.collect(ctx, snapshot); err != nil {
		return err
	}
	// Usage can cross the cap while the diff is applying. Remove all admissions
	// immediately, including the existing sockets, within this same pass.
	if p.Config.TrafficLimitBytes > 0 && m.state.Report.TrafficUsedBytes >= p.Config.TrafficLimitBytes && len(wanted) > 0 {
		wanted = map[string]string{}
		if err = m.limits(ctx, snapshot, wanted, nil); err != nil {
			return err
		}
		if err = m.persist(); err != nil {
			return err
		}
		for tag := range m.state.Applied {
			if _, err = m.apiCall(ctx, "rmi", tag); err != nil {
				return err
			}
			delete(m.state.Applied, tag)
		}
	}
	m.state.Report.AppliedRevision = p.Desired.Revision
	return m.record(snapshot, wanted, problems)
}
