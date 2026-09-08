package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDeriveInboundIPPoliciesAndSharedPortBoundary(t *testing.T) {
	limit := 3
	identityA := coreIdentity{InboundTag: "a", User: "a"}
	identityB := coreIdentity{InboundTag: "b", User: "b"}
	core := coreSnapshotResponse{Users: []coreUserSnapshot{
		{Identity: identityA, Attributed: true, InboundPort: 10001},
		{Identity: identityB, Attributed: true, InboundPort: 10002},
		{Identity: coreIdentity{InboundTag: "c", User: "c"}, Attributed: true, InboundPort: 10002},
	}}
	settings := connectionSettings{Users: []userConnectionSettings{
		{Identity: identityA, MaxInboundOnlineIPs: &limit},
		{Identity: identityB, MaxInboundOnlineIPs: &limit},
	}}
	policies, warnings := deriveInboundIPPolicies(core, settings)
	if len(policies) != 1 || policies[0].Port != 10001 || policies[0].Limit != 3 {
		t.Fatalf("unexpected policies: %#v", policies)
	}
	if len(warnings) != 1 {
		t.Fatalf("shared-port warning missing: %#v", warnings)
	}
}

func TestRenderNftablesUsesSlotSizeGraceAndIPv6Prefix(t *testing.T) {
	script := renderNftables([]inboundIPPolicy{{Port: 12968, Limit: 3}}, 30*time.Second)
	for _, expected := range []string{"size 3", "timeout 30s", "tcp dport 12968", "ffff:ffff:ffff:ffff::"} {
		if !strings.Contains(script, expected) {
			t.Fatalf("script missing %q:\n%s", expected, script)
		}
	}
	if _, err := exec.LookPath("nft"); err == nil {
		command := exec.CommandContext(context.Background(), "nft", "-c", "-f", "-")
		command.Stdin = strings.NewReader(strings.ReplaceAll(script, nftTableName, nftTableName+"_test"))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("nft rejected generated rules: %v: %s", err, output)
		}
	}
}
