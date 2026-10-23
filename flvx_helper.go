package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// egress / protocols / guard report whether this Helper advertises
// flvx.egress.v1 / flvx.protocols.v1 / flvx.reality_guard.v1.
func (a *app) flvxHelperDesired(ctx context.Context, id int64, serverID, token, arch string, egress, protocols, guard bool, report *flvx.Report) (*flvx.Envelope, error) {
	if a.flvxDB() == nil {
		return nil, nil
	}
	var envelope *flvx.Envelope
	accepted := false
	err := flvxTransaction(ctx, a.flvxDB(), func(tx *sql.Tx) error {
		s, err := readFLVXShare(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		previous, _ := json.Marshal(s)
		if report != nil {
			if err = mergeFLVXReport(ctx, tx, id, &s, *report, time.Now()); err != nil {
				return err
			}
			accepted = report.Generation == s.Generation && report.Sequence == s.Sequence && report.CollectedAt == s.Status.CollectedAt
		}
		flvxReset(&s, time.Now())
		if s.Core == nil && a.releaseCache != nil {
			info, _ := a.releaseCache.snapshot()
			s.Core = flvxReleaseCore(info)
		}
		current, _ := json.Marshal(s)
		if string(previous) != string(current) {
			if err = writeFLVXShare(ctx, tx, id, s); err != nil {
				return err
			}
		}
		server, err := flvxServerSnapshot(ctx, tx, id)
		if err != nil {
			return err
		}
		state := s.State
		// No current official inventory means no FLVX admission. Local Helper
		// independently rereads the official config immediately before applying.
		if !server.PortsKnown && state == "active" {
			state = "paused"
		}
		artifact := flvx.Artifact{}
		if s.Core != nil {
			asset := s.Core.Artifacts[arch]
			artifact = flvx.Artifact{URL: asset.URL, SHA256: asset.SHA256, Version: s.Core.Version}
		}
		if a.helperState != nil {
			artifact.GitHubAccelerator = a.helperState.githubAccelerator()
		}
		envelope = &flvx.Envelope{Payload: flvx.Payload{ServerID: serverID, Version: s.Version, Status: state, Config: s.Config, Desired: flvxHelperGuard(flvxHelperProtocols(flvxHelperEgress(s, egress), protocols), guard), OfficialPorts: server.OfficialPorts, Artifact: artifact, Period: s.Period, TrafficUsedBytes: s.Status.TrafficUsedBytes, AckGeneration: s.Generation, AckSequence: s.Sequence}, ExpiresAt: time.Now().Add(10 * time.Minute).UnixMilli()}
		envelope.Sign(token)
		return nil
	})
	if err == nil && accepted {
		a.cacheFLVXOnlineIPs(id, *report)
	}
	return envelope, err
}

// flvxHelperEgress gates the stored plan per Helper. With the share policy off
// the nodes leave through the default direct outbound, as the share owner
// chose. A Helper without egress support (e.g. downgraded after a plan was
// accepted) would fail the signature on an unknown field and must not route
// covered nodes around the plan, so those nodes are withheld (fail closed).
func flvxHelperEgress(s flvxShare, supported bool) flvx.Desired {
	d := s.Desired
	if d.Egress == nil || supported && !s.EgressDenied {
		return d
	}
	covered := d.Egress.CoveredTags()
	d.Egress = nil
	if s.EgressDenied || len(covered) == 0 {
		return d
	}
	disabled := false
	d.Nodes = append([]flvx.Node(nil), d.Nodes...)
	for i := range d.Nodes {
		if covered[d.Nodes[i].Tag()] {
			d.Nodes[i].Enabled = &disabled
		}
	}
	return d
}

// flvxHelperProtocols withholds landing-protocols-v1 nodes from a Helper that
// cannot render them (fail closed): it would reject the whole signed payload
// on the unknown fields. FLVX keeps the nodes and resends them once the
// share advertises the protocol again.
func flvxHelperProtocols(d flvx.Desired, supported bool) flvx.Desired {
	if supported {
		return d
	}
	nodes := make([]flvx.Node, 0, len(d.Nodes))
	for _, n := range d.Nodes {
		if !flvx.IsExtraProtocol(n.Protocol) && n.AnyTLS == nil && n.Snell == nil && n.Mieru == nil {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == len(d.Nodes) {
		return d
	}
	d.Nodes = nodes
	return d
}

// flvxHelperGuard strips the Reality guard switch for a Helper without
// landing-reality-guard-v1: it would reject the signed payload on the unknown
// field. Such a Helper keeps every node on its real dest (fail-open) and
// reports no guard state, so FLVX sees the guard is not on.
func flvxHelperGuard(d flvx.Desired, supported bool) flvx.Desired {
	if !supported {
		d.RealityGuard = nil
	}
	return d
}
