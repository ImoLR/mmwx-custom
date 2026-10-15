package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHelperUninstallRejectsUnsignedCommand(t *testing.T) {
	executor := &commandExecutor{state: &localState{}}
	result := executor.execute(context.Background(), managementCommand{
		ID: "delete-fixture", Action: "helper.uninstall", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute),
	}, "fixture-token")
	if result.Success || !strings.Contains(result.Message, "signature") || executor.state.UninstallPending {
		t.Fatalf("unsigned uninstall accepted: %+v", result)
	}
}

func TestHelperUninstallCompletesAfterLostAcknowledgement(t *testing.T) {
	if !uninstallAcknowledged(nil) || !uninstallAcknowledged(errors.New("HTTP 401: unauthorized")) {
		t.Fatal("completed deletion cannot revoke token safely")
	}
	for _, message := range []string{"HTTP 500: unavailable", "timeout", "controller rejected detailed metrics"} {
		if uninstallAcknowledged(errors.New(message)) {
			t.Fatal("unconfirmed outage treated as acknowledgement")
		}
	}
}
