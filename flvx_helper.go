package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// egress reports whether this Helper advertises flvx.egress.v1.
func (a *app) flvxHelperDesired(ctx context.Context, id int64, serverID, token, arch string, egress bool, report *flvx.Report) (*flvx.Envelope, error) {
	if a.flvxDB() == nil {
		return nil, nil
	}
	a.flvxOnlineMu.Lock()
	defer a.flvxOnlineMu.Unlock()
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
		if a.releaseCache != nil {
			info, _ := a.releaseCache.snapshot()
			asset := info.CoreArtifacts[arch]
			artifact = flvx.Artifact{URL: asset.URL, SHA256: asset.SHA256, Version: info.CoreVersion}
		}
		if a.helperState != nil {
			artifact.GitHubAccelerator = a.helperState.githubAccelerator()
		}
		envelope = &flvx.Envelope{Payload: flvx.Payload{ServerID: serverID, Version: s.Version, Status: state, Config: s.Config, Desired: flvxHelperEgress(s, egress), OfficialPorts: server.OfficialPorts, Artifact: artifact, Period: s.Period, TrafficUsedBytes: s.Status.TrafficUsedBytes, AckGeneration: s.Generation, AckSequence: s.Sequence}, ExpiresAt: time.Now().Add(10 * time.Minute).UnixMilli()}
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
