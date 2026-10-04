package main

import (
	"context"
	"testing"
)

func TestAccessConcurrentRebaseKeepsTargetDriftProtection(t *testing.T) {
	for _, change := range []string{"credential", "inbound"} {
		t.Run(change, func(t *testing.T) {
			alice := map[string]any{"email": "alice-in", "id": "11111111-1111-4111-8111-111111111111"}
			config := lifecycleConfig(lifecycleInbound("shared", "vless", alice))
			item := analyzeAccessInbound("alice", false, config, []lifecycleCredentialRef{lifecycleRef(5, "shared", "vless", alice)}, nil)
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: config})
			defer server.Close()
			if change == "credential" {
				alice["level"] = float64(7)
			} else {
				findConfigInbound(config, "shared")["port"] = float64(12345)
			}
			application := lifecycleTestApp(t, &lifecycleTestStore{}, server)
			if err := application.executeAccessItem(context.Background(), "session", &item); err == nil {
				t.Fatal("concurrent target or inbound drift must not be overwritten")
			}
			if len(fixture.actions) != 0 {
				t.Fatal("drift rejection changed the inbound")
			}
		})
	}
}
