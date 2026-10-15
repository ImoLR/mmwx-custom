package main

import (
	"context"
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
