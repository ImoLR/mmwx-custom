package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func deletionFixture(t *testing.T) serverDeletionInventory {
	t.Helper()
	var data serverDeletionInventory
	if err := json.Unmarshal([]byte(`{
 "remote_servers":[{"id":1,"name":"A","ip_address":"192.0.2.1","token":"secret"},{"id":2,"name":"B","ip_address":"192.0.2.2"}],
 "nodes":[
 {"id":10,"node_name":"A node","original_server":"A","clash_config":"{\"server\":\"192.0.2.1\",\"port\":443,\"uuid\":\"private-credential\"}"},
 {"id":20,"node_name":"B node","original_server":"B"},
 {"id":21,"node_name":"relay","original_server":"B","clash_config":"{\"server\":\"192.0.2.1\",\"port\":443}"},
 {"id":22,"node_name":"routed","original_server":"B","routed_target_node_id":10},
 {"id":23,"node_name":"chain","original_server":"B","chain_proxy_node_id":22},
 {"id":24,"node_name":"alternatives","original_server":"B","relay_group_node_ids":"[10,20]"},
 {"id":25,"node_name":"empty relay","original_server":"B","relay_group_node_ids":"[10,22]"}],
 "packages":[{"id":101,"name":"empty","nodes":"[10,21]"},{"id":102,"name":"mixed","nodes":"[10,20]"},{"id":103,"name":"keep","nodes":"[20]"},{"id":104,"name":"all","nodes":"[]"}],
 "users":[{"username":"alice","package_id":101}],"user_package_assignments":[{"username":"bob","package_id":102}],
 "mmwxc_package_traffic_groups":[{"id":1,"package_id":101,"node_ids":[10],"limit_bytes":99},{"id":2,"package_id":102,"node_ids":[10,20],"limit_bytes":88}],
 "forward_groups":[{"id":1,"name":"only A"},{"id":2,"name":"A or B"}],
 "forward_group_members":[{"group_id":1,"server_id":1},{"group_id":2,"server_id":1},{"group_id":2,"server_id":2}],
 "forward_chains":[{"id":1,"name":"broken"},{"id":2,"name":"usable"}],
 "forward_chain_hops":[{"chain_id":1,"group_id":1},{"chain_id":2,"group_id":2}],
 "forward_chain_nodes":[{"chain_id":1,"node_id":21},{"chain_id":2,"node_id":24}]
 }`), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestServerDeletionCascadeAndAlternatives(t *testing.T) {
	p, err := planServerDeletion(deletionFixture(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	ids := deletionPlanIDs(&p)
	if hashJSON(ids) != hashJSON([]int64{10, 21, 22, 23, 25}) {
		t.Fatalf("deleted scope=%v", ids)
	}
	if len(p.Packages) != 2 || !p.Packages[0].Delete || p.Packages[1].Delete {
		t.Fatalf("packages=%+v", p.Packages)
	}
	if len(p.Groups) != 2 || !p.Groups[0].Delete || p.Groups[1].Delete || p.Groups[1].Limit != 88 || hashJSON(p.Groups[1].NodeIDs) != hashJSON([]int64{20}) {
		t.Fatalf("groups=%+v", p.Groups)
	}
	if len(p.Packages[0].Users) != 1 || p.Packages[0].Users[0] != "alice" || p.Packages[1].Users[0] != "bob" {
		t.Fatalf("users=%+v", p.Packages)
	}
	for _, step := range p.Steps {
		if step.Kind == "chain" && step.ID == 2 {
			t.Fatal("removed viable alternative chain")
		}
	}
	if len(p.Attention) != 2 {
		t.Fatalf("attention=%v", p.Attention)
	}
}

func TestServerDeletionSharedHosts(t *testing.T) {
	for _, field := range []string{"ip_address", "domain"} {
		t.Run(field, func(t *testing.T) {
			data := deletionFixture(t)
			shared, unique := "192.0.2.1", "a.example"
			if field == "domain" {
				shared, unique = "shared.example", "192.0.2.1"
			} else {
				data["remote_servers"][0]["domain"] = unique
			}
			data["remote_servers"][0][field] = shared
			data["remote_servers"][1][field] = " " + strings.ToUpper(shared) + ":443 "
			config := func(host string, port int64) map[string]any {
				return map[string]any{"server": host, "port": port}
			}
			data["nodes"][0]["clash_config"] = config(shared, 1443)
			data["nodes"][1]["clash_config"] = config(shared, 3443)
			data["nodes"][2]["clash_config"] = config(unique, 2443)
			data["forward_chain_nodes"][0]["node_id"] = int64(30)
			data["nodes"] = append(data["nodes"], []map[string]any{
				{"id": int64(11), "node_name": "A parsed", "original_server": "A", "parsed_config": config(unique, 2443)},
				{"id": int64(26), "node_name": "B parsed", "original_server": "B", "parsed_config": config(shared, 4443)},
				{"id": int64(27), "node_name": "shared outbound", "original_server": "B", "inbound_tag": "out-shared"},
				{"id": int64(28), "node_name": "unique outbound", "original_server": "B", "inbound_tag": "out-unique"},
				{"id": int64(29), "node_name": "shared import", "clash_config": config(shared, 5443)},
				{"id": int64(30), "node_name": "required chain", "original_server": "B", "clash_config": config(shared, 6443)},
				{"id": int64(31), "node_name": "parent copy", "original_server": "B", "parent_node_id": int64(10)},
				{"id": int64(32), "node_name": "shared route", "original_server": "B", "inbound_tag": "route-shared", "routed_outbound_tag": "routed-shared"},
				{"id": int64(33), "node_name": "unique route", "original_server": "B", "inbound_tag": "route-unique", "routed_outbound_tag": "routed-unique"},
				{"id": int64(35), "node_name": "shared tunnel", "original_server": "B", "clash_config": config(shared, 5345)},
				{"id": int64(36), "node_name": "unique tunnel", "original_server": "B", "clash_config": config(shared, 6345)},
				{"id": int64(37), "node_name": "required tunnel hop", "original_server": "B", "clash_config": config(shared, 7345)},
			}...)
			data["user_outbounds"] = []map[string]any{
				{"server_id": int64(2), "inbound_tag": "out-shared", "outbound_json": map[string]any{"settings": map[string]any{"servers": []any{config(shared, 443)}}}},
				{"server_id": int64(2), "inbound_tag": "out-unique", "outbound_json": map[string]any{"settings": map[string]any{"servers": []any{config(unique, 443)}}}},
			}
			data["tunnels"] = []map[string]any{
				{"kind": "routed", "server_id": int64(2), "tag": "routed-shared", "inbound_tag": "route-shared", "target_address": shared, "target_port": int64(443)},
				{"kind": "routed", "server_id": int64(2), "tag": "routed-unique", "inbound_tag": "route-unique", "target_address": unique, "target_port": int64(443)},
				{"server_id": int64(2), "tag": "inbound-shared", "listen_port": int64(5345), "target_address": shared, "target_port": int64(443)},
				{"server_id": int64(2), "tag": "inbound-unique", "listen_port": int64(6345), "target_address": unique, "target_port": int64(443)},
			}
			data["tunnel_chains"] = []map[string]any{
				{"label": "shared", "hops": []any{map[string]any{"server_id": int64(2), "tag": "tunnel-shared-h0", "target_address": shared, "target_port": int64(443)}}},
				{"label": "B-A", "hops": []any{map[string]any{"server_id": int64(2), "tag": "tunnel-b-a-h0", "listen_port": int64(7345), "target_address": shared, "target_port": int64(8345)}, map[string]any{"server_id": int64(1), "tag": "tunnel-b-a-h1", "listen_port": int64(8345)}}},
			}
			p, err := planServerDeletion(data, 1)
			if err != nil {
				t.Fatal(err)
			}
			if ids := deletionPlanIDs(&p); hashJSON(ids) != hashJSON([]int64{10, 11, 21, 22, 23, 25, 28, 30, 31, 33, 36, 37}) {
				t.Fatalf("deleted scope=%v", ids)
			}
			if len(p.Packages) != 2 || !p.Packages[0].Delete || p.Packages[1].Delete || hashJSON(p.Packages[1].DeletedNodeIDs) != hashJSON([]int64{10}) {
				t.Fatalf("packages=%+v", p.Packages)
			}
			for _, inbound := range p.Inbounds {
				if inbound.Tag == "out-shared" || inbound.Tag == "route-shared" {
					t.Fatalf("shared inbound/outbound retired: %+v", inbound)
				}
			}
			tunnels := []string{}
			for _, step := range p.Steps {
				if step.Kind == "tunnel" {
					tunnels = append(tunnels, step.Tag)
				}
			}
			if hashJSON(tunnels) != hashJSON([]string{"inbound-unique", "routed-unique", "tunnel-b-a-h0"}) {
				t.Fatalf("tunnel cleanup=%v", tunnels)
			}
			warnings := 0
			for _, note := range p.Attention {
				if strings.HasPrefix(note, "地址 "+shared+" ") && strings.Contains(note, "「B」") {
					warnings++
				}
			}
			if warnings != 1 {
				t.Fatalf("shared host attention=%v", p.Attention)
			}
			for _, fingerprint := range p.SubscriptionNodes {
				if fingerprint.Host == shared && fingerprint.Port != 1443 {
					t.Fatalf("ambiguous fingerprint=%+v", fingerprint)
				}
			}
			for _, node := range []map[string]any{config(shared, 1443), config(unique, 2443)} {
				raw, _ := json.Marshal(node)
				if !subscriptionContainsDeletedNode(raw, p.SubscriptionNodes, 0) {
					t.Fatalf("direct node cache survives: %s", raw)
				}
			}
			kept, _ := json.Marshal(map[string]any{"proxies": []any{config(shared, 3443), config(shared, 4443)}})
			for _, content := range []string{string(kept), base64.StdEncoding.EncodeToString(kept)} {
				if subscriptionContainsDeletedNode([]byte(content), p.SubscriptionNodes, 0) {
					t.Fatal("other server's cache rejected")
				}
			}
		})
	}
}

func TestServerDeletionSharedHostFields(t *testing.T) {
	fields := []string{"ip_address", "ip_address_v6", "domain", "domain_v6", "observed_ip", "observed_ip_v6", "pull_address", "pull_address_v6"}
	for _, targetField := range fields {
		for _, otherField := range fields {
			t.Run(targetField+"/"+otherField, func(t *testing.T) {
				data := serverDeletionInventory{
					"remote_servers": {
						{"id": int64(1), "name": "A", targetField: " [2001:DB8::1]:443 "},
						{"id": int64(2), "name": "B", otherField: "2001:db8::1"},
						{"id": int64(3), "name": "C", "ip_address_v6": "2001:db8::1", "observed_ip_v6": "[2001:db8::1]"},
					},
					"nodes": {
						{"id": int64(10), "original_server": "A", "clash_config": `{"server":"2001:db8::1","port":443}`},
						{"id": int64(20), "original_server": "B", "clash_config": `{"server":"2001:db8::1","port":8443}`},
					},
				}
				p, err := planServerDeletion(data, 1)
				if err != nil {
					t.Fatal(err)
				}
				if len(p.Nodes) != 1 || p.Nodes[0].ID != 10 || !p.Nodes[0].Direct {
					t.Fatalf("deleted scope=%v", p.Nodes)
				}
				if len(p.Attention) != 1 || !strings.Contains(p.Attention[0], "地址 2001:db8::1 同时属于服务器「B」、「C」") {
					t.Fatalf("shared host attention=%v", p.Attention)
				}
			})
		}
	}
}

func TestServerDeletionPublicPlanAndStableFingerprint(t *testing.T) {
	data := deletionFixture(t)
	p, err := planServerDeletion(data, 1)
	if err != nil {
		t.Fatal(err)
	}
	p.Steps = append(p.Steps, serverDeletionStep{Payload: json.RawMessage(`{"password":"private-credential"}`)})
	raw, _ := json.Marshal(publicServerDeletion(p))
	if strings.Contains(string(raw), "private-credential") || strings.Contains(string(raw), "subscription_nodes") || strings.Contains(string(raw), "payload") {
		t.Fatal("private plan exposed")
	}
	for n := 0; n < 20; n++ {
		other, err := planServerDeletion(data, 1)
		if err != nil || p.Fingerprint != other.Fingerprint {
			t.Fatal("non-deterministic scope")
		}
	}
	data["nodes"][0]["node_name"] = "changed"
	changed, _ := planServerDeletion(data, 1)
	if changed.Fingerprint == p.Fingerprint {
		t.Fatal("scope change was not detected")
	}
	data["remote_servers"][1]["name"] = "A"
	if _, err := planServerDeletion(data, 1); err == nil {
		t.Fatal("ambiguous server name accepted")
	}
}

func TestServerDeletionTunnelDependencies(t *testing.T) {
	data := deletionFixture(t)
	data["nodes"] = append(data["nodes"], map[string]any{"id": float64(30), "node_name": "tunnel entry", "original_server": "B", "clash_config": `{"server":"192.0.2.2","port":2345}`})
	data["tunnel_chains"] = []map[string]any{{"label": "B-A", "hops": []any{map[string]any{"server_id": float64(2), "tag": "tunnel-b-a-h0", "listen_port": float64(2345), "target_address": "192.0.2.1", "target_port": float64(2346)}, map[string]any{"server_id": float64(1), "tag": "tunnel-b-a-h1", "listen_port": float64(2346)}}}}
	p, err := planServerDeletion(data, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !containsLifecycleNode(deletionPlanIDs(&p), 30) {
		t.Fatal("tunnel dependent survives")
	}
	found := false
	for _, step := range p.Steps {
		if step.Kind == "tunnel" && step.ServerID == 2 && step.Tag == "tunnel-b-a-h0" {
			found = true
		}
	}
	if !found {
		t.Fatal("remote tunnel cleanup missing")
	}
}

func TestServerDeletionSubscriptionCaches(t *testing.T) {
	nodes := []deletedSubscriptionNode{{Host: "192.0.2.1", Port: 443, Name: "retired node"}}
	for _, content := range []string{`{"proxies":[{"server":"192.0.2.1","port":443}]}`, "proxies:\n - name: retired node\n   server: 192.0.2.1\n   port: 443", "vless://fixture@192.0.2.1:443#retired%20node", "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(`{"add":"192.0.2.1","port":"443"}`))} {
		for _, encoded := range []string{content, base64.StdEncoding.EncodeToString([]byte(content))} {
			if !subscriptionContainsDeletedNode([]byte(encoded), nodes, 0) {
				t.Fatal("stale subscription served")
			}
		}
	}
	if subscriptionContainsDeletedNode([]byte(`{"server":"192.0.2.2","port":443,"name":"kept node"}`), nodes, 0) {
		t.Fatal("unrelated subscription rejected")
	}
	for _, content := range []string{`proxies: [{name: 「2倍」retired relay, server: 192.0.2.2, port: 443}]`, `{"tag":"retired relay / suffix"}`, "vless://fixture@192.0.2.2:443#prefix-retired%20relay"} {
		if !subscriptionContainsDeletedNode([]byte(content), []deletedSubscriptionNode{{Name: "retired relay"}}, 0) {
			t.Fatal("renamed relay cache served")
		}
	}
}

func TestDeletedServerRejectsRegisteredAndLegacyTokens(t *testing.T) {
	state := newCoreModeTestState(t)
	a := &app{adminStore: &fakeAdminSessionStore{server: false}, helperState: state, helperTokens: map[string]string{"12": "legacy"}}
	r := httptest.NewRequest("POST", "/api/custom/agent/connections", nil)
	r.Header.Set("Authorization", "Bearer helper-token")
	if _, _, ok := a.authorizedHelper(r, state.data.Servers["12"].CustomServerUUID, "v0.7.3"); ok {
		t.Fatal("registered token survives official deletion")
	}
	r.Header.Set("Authorization", "Bearer legacy")
	if _, _, ok := a.authorizedHelper(r, "12", "v0.7.3"); ok {
		t.Fatal("legacy token resurrected server")
	}
}

func TestServerDeletionRejectsForwardLoopAsAlternative(t *testing.T) {
	members := map[int64][]map[string]any{1: {{"server_id": int64(2)}}, 2: {{"server_id": int64(2)}}}
	if deletionChainHasPath([]int64{1, 2}, members) {
		t.Fatal("same-server loop accepted as usable path")
	}
	members[2] = append(members[2], map[string]any{"server_id": int64(3)})
	if !deletionChainHasPath([]int64{1, 2}, members) {
		t.Fatal("distinct usable path rejected")
	}
}

func TestServerDeletionRetiresOnlyTargetHelperState(t *testing.T) {
	state := newCoreModeTestState(t)
	state.data.Servers["2"] = helperServerIdentity{OfficialServerID: "2"}
	state.data.InstallTokens["retired"] = helperInstallToken{OfficialServerID: "12"}
	state.data.InstallTokens["kept"] = helperInstallToken{OfficialServerID: "2"}
	state.data.RebindTokens["retired"] = helperRebindToken{OfficialServerID: "12"}
	state.data.ConnectionSettings["12"] = serverConnectionSettings{}
	blocked := []serverConnectionIdentity{{InboundTag: "retired", User: "old"}, {InboundTag: "kept", User: "current"}}
	state.data.ConnectionSettings["2"] = serverConnectionSettings{BlockedIdentities: &blocked, Ports: []serverPortConnectionSettings{{InboundTag: "retired"}, {InboundTag: "kept"}}}
	a := &app{helperState: state,
		disabledUserBlocks: map[string]map[string][]serverConnectionIdentity{"alice": {"12": {{InboundTag: "target"}}, "2": {{InboundTag: "retired", User: "old"}, {InboundTag: "kept", User: "current"}, {InboundTag: "shared", User: "retired-user"}, {InboundTag: "shared", User: "kept-user"}}}},
		disabledUserAccess: map[string][]userAccessNodeStatus{"alice": {{ServerID: 12}, {ServerID: 2, InboundTag: "retired"}, {ServerID: 2, InboundTag: "kept", NodeID: 20}}},
	}
	p := serverDeletionPlan{ServerID: 12, Inbounds: []serverDeletionInbound{{ServerID: 2, Tag: "retired"}}}
	p.Identities = []serverDeletionIdentity{{ServerID: 2, Tag: "shared", Identity: "retired-user"}}
	if err := a.retireDeletedServerState(&p); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.data.Servers["12"]; ok {
		t.Fatal("deleted Helper still authorizes reports")
	}
	if _, ok := state.data.InstallTokens["retired"]; ok {
		t.Fatal("deleted install token retained")
	}
	if _, ok := state.data.RebindTokens["retired"]; ok {
		t.Fatal("deleted rebind token retained")
	}
	kept := state.data.ConnectionSettings["2"]
	if len(state.data.Servers) != 1 || len(state.data.InstallTokens) != 1 || len(kept.Ports) != 1 || kept.Ports[0].InboundTag != "kept" || len(*kept.BlockedIdentities) != 1 {
		t.Fatal("unrelated Helper settings changed or retired settings remain")
	}
	remaining := a.disabledUserBlocks["alice"]["2"]
	if len(a.disabledUserBlocks["alice"]["12"]) != 0 || len(remaining) != 2 || remaining[0].User != "current" || remaining[1].User != "kept-user" || len(a.disabledUserAccess["alice"]) != 1 || a.disabledUserAccess["alice"][0].NodeID != 20 {
		t.Fatal("retired dependency cache retained or unrelated disabled identity lost")
	}
}

func TestServerDeletionRoutedTunnelKeepsPartiallyUsableInbound(t *testing.T) {
	for _, conditional := range []bool{false, true} {
		data := serverDeletionInventory{"tunnels": {{"kind": "routed", "server_id": int64(2), "tag": "tunnel-route", "inbound_tag": "shared", "target_address": "192.0.2.1", "target_port": int64(443), "conditional": conditional}}}
		p := serverDeletionPlan{ServerID: 1}
		reasons := map[int64]string{}
		planDeletionTunnels(data, &p, map[int64]map[string]any{2: {"name": "B"}}, map[int64]map[string]any{10: {"original_server": "B", "inbound_tag": "shared"}}, reasons, map[string]bool{"192.0.2.1": true})
		if (reasons[10] == "") != conditional || len(p.Steps) != 1 || (len(p.Attention) > 0) != conditional {
			t.Fatalf("conditional=%t reasons=%v steps=%v notes=%v", conditional, reasons, p.Steps, p.Attention)
		}
	}
}

func TestServerDeletionRoutedTunnelResumesWithoutTouchingOtherRules(t *testing.T) {
	config := map[string]any{"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}, map[string]any{"tag": "broken", "protocol": "freedom", "settings": map[string]any{"redirect": "192.0.2.1:443"}}}, "routing": map[string]any{"rules": []any{map[string]any{"outboundTag": "direct"}, map[string]any{"outboundTag": "broken"}, map[string]any{"outboundTag": "broken"}}}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{2: config})
	defer server.Close()
	failed := false
	fixture.extraHandler = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPost {
			return false
		}
		var payload map[string]any
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid remote operation")
			return false
		}
		switch r.URL.Path {
		case "/api/admin/remote/routing":
			routing := deletionObject(config["routing"])
			rules := routing["rules"].([]any)
			i := int(deletionID(payload["index"]))
			routing["rules"] = append(rules[:i], rules[i+1:]...)
		case "/api/admin/remote/outbounds":
			if !failed {
				failed = true
				writeJSON(w, 502, map[string]any{"error": "injected"})
				return true
			}
			config["outbounds"] = config["outbounds"].([]any)[:1]
		default:
			return false
		}
		writeJSON(w, 200, map[string]any{"success": true})
		return true
	}
	target, _ := url.Parse(server.URL)
	a := &app{officialInternalTarget: target}
	step := serverDeletionStep{ServerID: 2, Tag: "broken"}
	expected := map[string]any{"target_address": "192.0.2.1", "target_port": int64(443)}
	if err := a.removeServerDeletionRoutedTunnel(context.Background(), "session", &step, expected, config); err == nil {
		t.Fatal("injected outage was not retained")
	}
	for retry := 0; retry < 2; retry++ {
		if err := a.removeServerDeletionRoutedTunnel(context.Background(), "session", &step, expected, config); err != nil {
			t.Fatal(err)
		}
	}
	rules := deletionObject(config["routing"])["rules"].([]any)
	if len(rules) != 1 || deletionObject(rules[0])["outboundTag"] != "direct" || len(config["outbounds"].([]any)) != 1 {
		t.Fatal("unrelated routing changed")
	}
}
