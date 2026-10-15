package main

import (
	"time"

	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const externalControlFixture = `{"api":{"tag":"api","services":["HandlerService","LoggerService","StatsService","RoutingService"]},"metrics":{"tag":"Metrics","listen":"127.0.0.1:38889"},"inbounds":[{"tag":"api","listen":"127.0.0.1","port":46736,"protocol":"dokodemo-door"}],"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"}]}}`

func TestExternalConfigRepairOrderingAndRetry(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "lost response"}[lostResponse], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			_ = os.WriteFile(path, []byte(`{"inbounds":[]}`), 0600)
			state := externalOwnershipState{}
			var calls []string
			persist := func() error { calls = append(calls, "persist"); return nil }
			regenerate := func() error {
				if !state.AgentConfigReloadPending || !state.ExternalConfigSyncPending {
					t.Fatal("reload intent not saved before official write")
				}
				calls = append(calls, "regenerate")
				_ = os.WriteFile(path, []byte(externalControlFixture), 0600)
				if lostResponse {
					return errors.New("HTTP response lost")
				}
				return nil
			}
			restart := func() error { calls = append(calls, "restart agent"); return nil }
			confirm := func() error { calls = append(calls, "confirm snapshot"); return nil }
			err := repairExternalControlConfig(context.Background(), path, &state, persist, regenerate, restart, confirm)
			if lostResponse {
				if err == nil || !state.AgentConfigReloadPending {
					t.Fatal("lost response discarded pending reload")
				}
				err = repairExternalControlConfig(context.Background(), path, &state, persist, regenerate, restart, confirm)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"persist", "regenerate", "restart agent", "persist", "confirm snapshot", "persist"}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("ordering=%v", calls)
			}
			calls = nil
			if err := repairExternalControlConfig(context.Background(), path, &state, persist, regenerate, restart, confirm); err != nil || len(calls) > 0 {
				t.Fatalf("healthy tick repeated actions: %v %v", calls, err)
			}
		})
	}
}

func TestExternalConfigConfirmationFailureDoesNotRestartAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(path, []byte(externalControlFixture), 0600)
	state := externalOwnershipState{AgentConfigReloadPending: true, ExternalConfigSyncPending: true}
	restarts := 0
	persist := func() error { return nil }
	regenerate := func() error { t.Fatal("healthy config regenerated"); return nil }
	restart := func() error { restarts++; return nil }
	confirm := func() error { return errors.New("Agent report not received yet") }
	for i := 0; i < 3; i++ {
		if err := repairExternalControlConfig(context.Background(), path, &state, persist, regenerate, restart, confirm); err == nil {
			t.Fatal("confirmation failure hidden")
		}
	}
	if restarts != 1 || !state.ExternalConfigSyncPending {
		t.Fatalf("restarts=%d state=%+v", restarts, state)
	}
}

func TestExternalConfigBackoffDoesNotLogFalseRecovery(t *testing.T) {
	now := time.Now()
	state := localState{ExternalOwnership: externalOwnershipState{
		Enabled: true, NextConfigRepairAt: now.Add(time.Minute),
		configRepairError: errors.New("an active official administrator session is required for Core handoff"),
	}}
	logs := newPeriodicLogLimiter()
	lines := 0
	logs.print = func(string, ...any) { lines++ }
	for tick := 0; tick <= 120; tick++ {
		logs.now = func() time.Time { return now.Add(time.Duration(tick) * 5 * time.Second) }
		err := ensureExternalControlConfig(context.Background(), nil, config{}, &state, false)
		if err == nil {
			t.Fatal("backoff reported a false recovery")
		}
		logs.report("external control config reconcile failed", err)
	}
	if lines != 2 {
		t.Fatalf("expected initial error and one 10m summary; got %d lines", lines)
	}
	state.ExternalOwnership.Enabled = false
	logs.report("external control config reconcile failed", ensureExternalControlConfig(context.Background(), nil, config{}, &state, false))
	if lines != 3 {
		t.Fatal("actual recovery was not reported once")
	}
}
