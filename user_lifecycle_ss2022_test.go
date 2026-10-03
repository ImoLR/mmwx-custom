package main

import (
	"context"
	"encoding/json"
	"testing"
)

func ss2022LifecycleNodeRef(t *testing.T, username, protocol, password string) lifecycleCredentialRef {
	t.Helper()
	node, err := json.Marshal(map[string]any{"type": "ss", "password": password})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]string{string(node)})
	if err != nil {
		t.Fatal(err)
	}
	return lifecycleCredentialRef{Username: username, ServerID: 5, ServerName: "server-5", InboundTag: "tag", Protocol: protocol, CredentialRaw: string(raw), Source: "user_package_assignments"}
}

func ss2022LifecycleInbound(protocol, method string, credentials ...map[string]any) map[string]any {
	inbound := lifecycleInbound("tag", protocol, credentials...)
	settings := inbound["settings"].(map[string]any)
	settings["method"] = method
	settings["password"] = "server-key"
	return inbound
}

func TestLifecycleSS2022NodeRefsDelete(t *testing.T) {
	for _, method := range []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305"} {
		for _, protocol := range []string{"shadowsocks", "ss"} {
			t.Run(protocol+"/"+method, func(t *testing.T) {
				alice := map[string]any{"email": "alice__tag", "password": "alice-key"}
				bob := map[string]any{"email": "bob__tag", "password": "bob-key"}
				store := &lifecycleTestStore{
					refs:         []lifecycleCredentialRef{ss2022LifecycleNodeRef(t, "alice", protocol, "server-key:alice-key")},
					businessRefs: map[string][]lifecycleCredentialRef{"5/tag": {ss2022LifecycleNodeRef(t, "bob", protocol, "server-key:bob-key")}},
				}
				fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(ss2022LifecycleInbound(protocol, method, alice, bob))})
				defer server.Close()
				application := lifecycleTestApp(t, store, server)
				plan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
				if err != nil || len(plan) != 1 || plan[0].Action != lifecycleActionRemoveUser || plan[0].Status != lifecycleItemPending || len(plan[0].targetCredentials) != 1 {
					t.Fatalf("node-sourced shared SS2022 delete plan failed: %#v err=%v", plan, err)
				}
				result := application.executeDeletePlan(context.Background(), "session", "alice", "ss2022-delete", plan)
				entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "tag"))
				if !result.UserDeleted || err != nil || len(entries) != 1 || hashJSON(entries[0]) != hashJSON(bob) {
					t.Fatalf("node-sourced shared SS2022 deletion changed the wrong credential: result=%#v entries=%#v err=%v", result, entries, err)
				}
				solo := analyzeLifecycleInbound(lifecycleConfig(ss2022LifecycleInbound(protocol, method, alice)), store.refs, nil, nil)
				if solo.Action != lifecycleActionDeleteWhole || solo.Status != lifecycleItemPending || len(solo.targetCredentials) != 1 {
					t.Fatalf("node-sourced exclusive SS2022 delete plan failed: %#v", solo)
				}
			})
		}
	}
}

func TestLifecycleSS2022NodeRefsDisableEnable(t *testing.T) {
	alice := map[string]any{"email": "alice__tag", "password": "alice-key", "level": float64(1)}
	bob := map[string]any{"email": "bob__tag", "password": "bob-key"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{ss2022LifecycleNodeRef(t, "alice", "shadowsocks", "server-key:alice-key")}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(ss2022LifecycleInbound("shadowsocks", "2022-blake3-aes-128-gcm", alice, bob))})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	plan, err := application.buildAccessPlan(context.Background(), "session", "alice", false)
	if err != nil || len(plan) != 1 || plan[0].Status != lifecycleItemPending || len(plan[0].accessCredentials) != 1 {
		t.Fatalf("node-sourced SS2022 disable plan failed: %#v err=%v", plan, err)
	}
	if err := store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationDisable, "ss2022-disable", plan); err != nil {
		t.Fatal(err)
	}
	result := application.executeAccessPlan(context.Background(), "session", "alice", "ss2022-disable", lifecycleOperationDisable, plan)
	entries, _, err := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "tag"))
	if result.PendingCount != 0 || err != nil || len(entries) != 2 || entries[0]["password"] == alice["password"] || entries[0]["email"] != alice["email"] || entries[0]["level"] != alice["level"] || hashJSON(entries[1]) != hashJSON(bob) {
		t.Fatalf("node-sourced SS2022 disable failed: result=%#v entries=%#v err=%v", result, entries, err)
	}
	if hashJSON(store.backups[0].OriginalCredential) != hashJSON(alice) || store.backups[0].DisabledHash != hashJSON(entries[0]) {
		t.Fatal("disable did not back up the complete current credential")
	}
	plan, err = application.buildAccessPlan(context.Background(), "session", "alice", true)
	if err != nil || len(plan) != 1 || plan[0].Status != lifecycleItemPending {
		t.Fatalf("node-sourced SS2022 enable plan failed: %#v err=%v", plan, err)
	}
	result = application.executeAccessPlan(context.Background(), "session", "alice", "ss2022-enable", lifecycleOperationEnable, plan)
	if result.PendingCount != 0 || hashJSON(findCredential(t, fixture.configs[5], "tag", 0)) != hashJSON(alice) || hashJSON(findCredential(t, fixture.configs[5], "tag", 1)) != hashJSON(bob) {
		t.Fatalf("node-sourced SS2022 enable did not restore exactly: %#v", result)
	}
}

func TestLifecycleSS2022NodeRefsRequireServerKey(t *testing.T) {
	alice := map[string]any{"email": "alice__tag", "password": "alice-key"}
	for _, method := range []string{"2022-blake3-aes-128-gcm", "aes-128-gcm"} {
		for _, nodePassword := range []string{"other-server:alice-key", "server-key:alice-key"} {
			if method == "2022-blake3-aes-128-gcm" && nodePassword == "server-key:alice-key" {
				continue
			}
			t.Run(method+"/"+nodePassword, func(t *testing.T) {
				config := lifecycleConfig(ss2022LifecycleInbound("shadowsocks", method, alice))
				refs := []lifecycleCredentialRef{ss2022LifecycleNodeRef(t, "alice", "shadowsocks", nodePassword)}
				deletion := analyzeLifecycleInbound(config, refs, nil, nil)
				if deletion.Action != lifecycleActionConflict || len(deletion.targetCredentials) != 0 {
					t.Fatalf("nonmatching SS2022 server key or ordinary SS matched for deletion: %#v", deletion)
				}
				disable := analyzeAccessInbound("alice", false, config, refs, nil)
				if disable.Status != lifecycleItemFailed || disable.replacementInbound != nil {
					t.Fatalf("nonmatching SS2022 server key or ordinary SS matched for disable: %#v", disable)
				}
			})
		}
	}
}

func TestLifecycleSS2022NodeRefsRejectAmbiguousDisable(t *testing.T) {
	config := lifecycleConfig(ss2022LifecycleInbound("shadowsocks", "2022-blake3-aes-128-gcm",
		map[string]any{"email": "alice__tag", "password": "shared-key"},
		map[string]any{"email": "bob__tag", "password": "shared-key"},
	))
	plan := analyzeAccessInbound("alice", false, config, []lifecycleCredentialRef{ss2022LifecycleNodeRef(t, "alice", "shadowsocks", "server-key:shared-key")}, nil)
	if plan.Status != lifecycleItemFailed || plan.replacementInbound != nil {
		t.Fatalf("ambiguous node credential was disabled: %#v", plan)
	}
}

func TestLifecycleNodeRefsOtherProtocolsUnchanged(t *testing.T) {
	for _, test := range []struct {
		protocol string
		primary  string
	}{
		{"vless", "id"}, {"vmess", "id"}, {"trojan", "password"}, {"shadowsocks", "password"}, {"snell", "psk"}, {"anytls", "password"},
	} {
		t.Run(test.protocol, func(t *testing.T) {
			alice := map[string]any{"email": "alice__tag", test.primary: "alice-secret"}
			bob := map[string]any{"email": "bob__tag", test.primary: "bob-secret"}
			raw, _ := json.Marshal(alice)
			nodeRaw, _ := json.Marshal([]string{string(raw)})
			ref := lifecycleRef(5, "tag", test.protocol, alice)
			ref.CredentialRaw = string(nodeRaw)
			bobRef := lifecycleRef(5, "tag", test.protocol, bob)
			bobRef.Username = "bob"
			config := lifecycleConfig(lifecycleInbound("tag", test.protocol, alice, bob))
			deletion := analyzeLifecycleInbound(config, []lifecycleCredentialRef{ref}, []lifecycleCredentialRef{bobRef}, nil)
			if deletion.Action != lifecycleActionRemoveUser || deletion.Status != lifecycleItemPending || len(deletion.targetCredentials) != 1 {
				t.Fatalf("existing raw credential match changed: %#v", deletion)
			}
			disable := analyzeAccessInbound("alice", false, config, []lifecycleCredentialRef{lifecycleRef(5, "tag", test.protocol, alice)}, nil)
			if disable.Status != lifecycleItemPending || len(disable.accessCredentials) != 1 {
				t.Fatalf("existing structured credential disable changed: %#v", disable)
			}
			rawDisable := analyzeAccessInbound("alice", false, config, []lifecycleCredentialRef{ref}, nil)
			if rawDisable.Status != lifecycleItemFailed || rawDisable.replacementInbound != nil {
				t.Fatalf("raw credential disable support expanded beyond SS2022: %#v", rawDisable)
			}
		})
	}
}
