package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const nftTableName = "mmwxc_connection_control"

type inboundIPPolicy struct {
	Port  uint32
	Limit int
}

func deriveInboundIPPolicies(core coreSnapshotResponse, settings connectionSettings) ([]inboundIPPolicy, []string) {
	settingsByIdentity := make(map[coreIdentity]userConnectionSettings, len(settings.Users))
	for _, item := range settings.Users {
		settingsByIdentity[item.Identity] = item
	}
	usersByPort := make(map[uint32][]coreUserSnapshot)
	for _, user := range core.Users {
		if user.Attributed && user.InboundPort > 0 {
			usersByPort[user.InboundPort] = append(usersByPort[user.InboundPort], user)
		}
	}
	var policies []inboundIPPolicy
	var warnings []string
	for port, users := range usersByPort {
		if len(users) != 1 {
			for _, user := range users {
				if settingsByIdentity[user.Identity].MaxInboundOnlineIPs != nil {
					warnings = append(warnings, fmt.Sprintf("inbound port %d has multiple authenticated users; IP limit not enforced", port))
					break
				}
			}
			continue
		}
		limit := settingsByIdentity[users[0].Identity].MaxInboundOnlineIPs
		if limit != nil && *limit > 0 {
			policies = append(policies, inboundIPPolicy{Port: port, Limit: *limit})
		}
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Port < policies[j].Port })
	return policies, warnings
}

func renderNftables(policies []inboundIPPolicy, grace time.Duration) string {
	if grace <= 0 {
		grace = 30 * time.Second
	}
	seconds := int64(grace.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	var output strings.Builder
	fmt.Fprintf(&output, "table inet %s {\n", nftTableName)
	for _, policy := range policies {
		fmt.Fprintf(&output, "  set p%d_v4 { type ipv4_addr; flags dynamic,timeout; size %d; timeout %ds; }\n", policy.Port, policy.Limit, seconds)
		fmt.Fprintf(&output, "  set p%d_v6 { type ipv6_addr; flags dynamic,timeout; size %d; timeout %ds; }\n", policy.Port, policy.Limit, seconds)
	}
	output.WriteString("  chain input {\n    type filter hook input priority -5; policy accept;\n")
	for _, policy := range policies {
		fmt.Fprintf(&output, "    tcp dport %d ct state new update @p%d_v4 { ip saddr timeout %ds } accept\n", policy.Port, policy.Port, seconds)
		fmt.Fprintf(&output, "    tcp dport %d ct state new meta nfproto ipv6 update @p%d_v6 { ip6 saddr & ffff:ffff:ffff:ffff:: timeout %ds } accept\n", policy.Port, policy.Port, seconds)
		fmt.Fprintf(&output, "    tcp dport %d ct state new drop\n", policy.Port)
	}
	output.WriteString("  }\n}\n")
	return output.String()
}

type nftablesManager struct {
	enabled bool
	run     func(context.Context, []byte, bool) error
}

func newNftablesManager(enabled bool) *nftablesManager {
	return &nftablesManager{enabled: enabled, run: runNft}
}

func (manager *nftablesManager) apply(ctx context.Context, policies []inboundIPPolicy, grace time.Duration) error {
	if !manager.enabled {
		return nil
	}
	if len(policies) == 0 {
		_ = manager.run(ctx, nil, true)
		return nil
	}
	script := []byte(renderNftables(policies, grace))
	if err := manager.run(ctx, script, false); err != nil {
		return err
	}
	return nil
}

func runNft(ctx context.Context, script []byte, removeOnly bool) error {
	if removeOnly {
		deleteCommand := exec.CommandContext(ctx, "nft", "delete", "table", "inet", nftTableName)
		_ = deleteCommand.Run()
		return nil
	}
	validationScript := bytes.ReplaceAll(script, []byte(nftTableName), []byte(nftTableName+"_validate"))
	check := exec.CommandContext(ctx, "nft", "-c", "-f", "-")
	check.Stdin = bytes.NewReader(validationScript)
	if output, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("validate nftables rules: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deleteCommand := exec.CommandContext(ctx, "nft", "delete", "table", "inet", nftTableName)
	_ = deleteCommand.Run()
	apply := exec.CommandContext(ctx, "nft", "-f", "-")
	apply.Stdin = bytes.NewReader(script)
	if output, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("apply nftables rules: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
