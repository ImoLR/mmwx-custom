package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func (a *app) flvxHelperDesired(ctx context.Context, id int64, serverID, token, arch string, report *flvx.Report) (*flvx.Envelope, error) {
	if a.flvxDB() == nil {
		return nil, nil
	}
	var envelope *flvx.Envelope
	err := flvxTransaction(ctx, a.flvxDB(), func(tx *sql.Tx) error {
		s, err := readFLVXShare(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if report != nil {
			if err = mergeFLVXReport(ctx, tx, id, &s, *report, time.Now()); err != nil {
				return err
			}
		}
		flvxReset(&s, time.Now())
		if err = writeFLVXShare(ctx, tx, id, s); err != nil {
			return err
		}
		servers, err := flvxServers(ctx, tx)
		if err != nil {
			return err
		}
		server, err := flvxServerByID(servers, id)
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
		envelope = &flvx.Envelope{Payload: flvx.Payload{ServerID: serverID, Version: s.Version, Status: state, Config: s.Config, Desired: s.Desired, OfficialPorts: server.OfficialPorts, Artifact: artifact, Period: s.Period, TrafficUsedBytes: s.Status.TrafficUsedBytes, AckGeneration: s.Generation, AckSequence: s.Sequence}, ExpiresAt: time.Now().Add(10 * time.Minute).UnixMilli()}
		envelope.Sign(token)
		return nil
	})
	return envelope, err
}
