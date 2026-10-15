package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/externalconfig"
)

func ensureExternalControlConfig(ctx context.Context, client *http.Client, cfg config, state *localState, force bool) error {
	if !state.ExternalOwnership.Enabled {
		return nil
	}
	ownership := &state.ExternalOwnership
	if !force && time.Now().Before(ownership.NextConfigRepairAt) {
		return ownership.configRepairError
	}
	mode, err := readAgentXrayMode(officialAgentConfigPath)
	if err != nil || mode != "external" {
		return err
	}
	err = repairExternalControlConfig(ctx, officialConfigPath, ownership,
		func() error { return saveLocalState(cfg.StatePath, *state) },
		func() error { _, err := requestTakeover(ctx, client, cfg, "", "prepare"); return err },
		func() error { return systemctl(ctx, "restart", "mmw-agent.service") },
		func() error {
			var lastErr error
			for i := 0; i < 6; i++ {
				_, lastErr = requestTakeover(ctx, client, cfg, "", "confirm")
				if lastErr == nil {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Second):
				}
			}
			return lastErr
		})
	ownership.configRepairError = err
	if err != nil {
		ownership.NextConfigRepairAt = time.Now().Add(time.Minute)
	} else {
		ownership.NextConfigRepairAt = time.Time{}
	}
	return err
}

func repairExternalControlConfig(ctx context.Context, path string, state *externalOwnershipState, persist, regenerate, restartAgent, confirm func() error) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	complete, err := externalconfig.Complete(data)
	if err != nil {
		return err
	}
	if !complete {
		// Persist before the request: an HTTP timeout after the official write
		// must not lose the Agent cache reload on the next Helper tick/start.
		state.AgentConfigReloadPending = true
		state.ExternalConfigSyncPending = true
		if err := persist(); err != nil {
			return err
		}
		if err := regenerate(); err != nil {
			return err
		}
		data, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		complete, err = externalconfig.Complete(data)
		if err != nil || !complete {
			return errors.New("official External control configuration is still incomplete")
		}
	}
	if state.AgentConfigReloadPending {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Official system-config already restarted Core. Reload the Agent's
		// cached metrics config and make it report the repaired configuration.
		if err := restartAgent(); err != nil {
			return err
		}
		state.AgentConfigReloadPending = false
		if err := persist(); err != nil {
			return err
		}
	}
	if state.ExternalConfigSyncPending {
		if err := confirm(); err != nil {
			return err
		}
		state.ExternalConfigSyncPending = false
		return persist()
	}
	return nil
}
