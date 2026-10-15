package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func deletionServerHosts(server map[string]any) map[string]bool {
	result := map[string]bool{}
	for _, key := range []string{"ip_address", "ip_address_v6", "domain", "domain_v6", "observed_ip", "observed_ip_v6", "pull_address", "pull_address_v6"} {
		if h := deletionHost(server[key]); h != "" {
			result[h] = true
		}
	}
	return result
}

func (a *app) serverDeletionTunnels(ctx context.Context, data serverDeletionInventory) string {
	// Official has no tunnel table. Tags and targets live in Core configuration.
	// The latest saved configuration also covers machines currently offline.
	byKey := map[string]map[string]any{}
	for _, snapshot := range data["server_xray_config_snapshots"] {
		id := deletionID(snapshot["server_id"])
		config := deletionObject(snapshot["config_json"])
		inbounds, _ := config["inbounds"].([]any)
		for _, value := range inbounds {
			inbound := deletionObject(value)
			if inbound["protocol"] != "dokodemo-door" && inbound["protocol"] != "tunnel" {
				continue
			}
			settings := deletionObject(inbound["settings"])
			tag := deletionText(inbound["tag"])
			tunnel := map[string]any{"kind": "inbound", "server_id": id, "tag": tag, "listen_port": inbound["port"], "target_address": settings["address"], "target_port": settings["port"]}
			byKey[strconv.FormatInt(id, 10)+":"+tag] = tunnel
		}
		outbounds, _ := config["outbounds"].([]any)
		rules, _ := deletionObject(config["routing"])["rules"].([]any)
		for _, value := range outbounds {
			outbound := deletionObject(value)
			settings := deletionObject(outbound["settings"])
			host, port, err := net.SplitHostPort(deletionText(settings["redirect"]))
			if err != nil {
				continue
			}
			for index, value := range rules {
				rule := deletionObject(value)
				if rule["outboundTag"] != outbound["tag"] {
					continue
				}
				tunnel := map[string]any{"kind": "routed", "server_id": id, "tag": outbound["tag"], "target_address": host, "target_port": deletionID(port), "inbound_tags": rule["inboundTag"], "match_domain": rule["domain"], "match_ip": rule["ip"]}
				for _, field := range []string{"port", "source", "sourcePort", "network", "protocol", "user"} {
					if rule[field] != nil {
						tunnel["conditional"] = true
					}
				}
				byKey[strconv.FormatInt(id, 10)+":"+deletionText(outbound["tag"])+":rule:"+strconv.Itoa(index)] = tunnel
			}
		}
	}
	warning := ""
	store := a.adminStore.(*postgresAdminSessionStore)
	token, err := store.CoreModeAdminSession(ctx)
	if err == nil {
		readCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		var response struct {
			Tunnels []map[string]any `json:"tunnels"`
			Chains  []map[string]any `json:"chains"`
		}
		err = a.officialLifecycleJSON(readCtx, token, http.MethodGet, "/api/admin/tunnels", nil, &response)
		cancel()
		if err == nil {
			data["tunnel_chains"] = response.Chains
			replaced := map[string]bool{}
			for _, t := range response.Tunnels {
				key := strconv.FormatInt(deletionID(t["server_id"]), 10) + ":" + deletionText(t["tag"])
				if !replaced[key] {
					for old := range byKey {
						if old == key || strings.HasPrefix(old, key+":rule:") {
							delete(byKey, old)
						}
					}
					replaced[key] = true
				}
				if t["kind"] == "routed" {
					key += ":live:" + hashJSON(t)
				}
				byKey[key] = t
			}
		}
	}
	if err != nil {
		warning = "部分隧道实时状态无法读取；按最近官方配置快照核对，离线机器仍需人工检查"
	}
	for _, t := range byKey {
		data["tunnels"] = append(data["tunnels"], t)
	}
	return warning
}

func planDeletionTunnels(data serverDeletionInventory, p *serverDeletionPlan, servers map[int64]map[string]any, nodes map[int64]map[string]any, reasons map[int64]string, hosts map[string]bool) {
	broken := map[string]bool{}
	tunnels := map[string]map[string]any{}
	routedSteps := map[string]bool{}
	key := func(t map[string]any) string {
		return strconv.FormatInt(deletionID(t["server_id"]), 10) + ":" + deletionText(t["tag"])
	}
	for _, t := range data["tunnels"] {
		if t["kind"] == "routed" {
			sid := deletionID(t["server_id"])
			if sid != p.ServerID && hosts[deletionHost(t["target_address"])] {
				conditional := t["conditional"] == true
				for _, field := range []string{"match_domain", "match_ip"} {
					values, _ := t[field].([]any)
					conditional = conditional || len(values) > 0
				}
				for id, node := range nodes {
					if node["original_server"] != servers[sid]["name"] {
						continue
					}
					tag := deletionText(node["inbound_tag"])
					matches := deletionText(t["inbound_tag"]) == "" || t["inbound_tag"] == tag
					if tags, ok := t["inbound_tags"].([]any); ok && len(tags) > 0 {
						matches = false
						for _, value := range tags {
							matches = matches || value == tag
						}
					}
					if node["routed_outbound_tag"] == t["tag"] || (matches && !conditional) {
						reasons[id] = "路由隧道的必要出口依赖此服务器"
					}
				}
				if conditional {
					p.Attention = appendUniqueString(p.Attention, "路由隧道 "+deletionText(t["tag"])+" 的部分匹配流量受影响；移除失效规则，保留仍可处理其他流量的节点")
				}
				if !routedSteps[key(t)] {
					// Keep only the endpoint guard, independent of which matching rule was seen first.
					raw, _ := json.Marshal(map[string]any{"kind": "routed", "target_address": t["target_address"], "target_port": t["target_port"]})
					p.Steps = append(p.Steps, serverDeletionStep{Kind: "tunnel", ServerID: sid, Tag: deletionText(t["tag"]), Name: "移除路由隧道：" + deletionText(servers[sid]["name"]) + " / " + deletionText(t["tag"]), Payload: raw})
					routedSteps[key(t)] = true
				}
			}
			continue
		}
		tunnels[key(t)] = t
		if deletionID(t["server_id"]) == p.ServerID || hosts[deletionHost(t["target_address"])] {
			broken[key(t)] = true
		}
	}

	for _, chain := range data["tunnel_chains"] {
		hops, _ := chain["hops"].([]any)
		lastBroken := -1
		for i, h := range hops {
			hop := deletionObject(h)
			tunnels[key(hop)] = hop
			if deletionID(hop["server_id"]) == p.ServerID || hosts[deletionHost(hop["target_address"])] {
				lastBroken = i
			}
		}
		if lastBroken >= 0 {
			for i, h := range hops {
				if i <= lastBroken {
					broken[key(deletionObject(h))] = true
				}
			}
			if lastBroken < len(hops)-1 {
				p.Attention = append(p.Attention, "隧道链 "+deletionText(chain["label"])+" 的原入口或中间跳失效；后续不经过此服务器的路径仍可独立使用，予以保留")
			} else {
				p.Attention = append(p.Attention, "隧道链 "+deletionText(chain["label"])+" 失去必要出口，将移除失效的前置隧道")
			}
		}
	}
	// For offline snapshots, hop order is encoded in tunnel-<label>-hN.
	label := func(tag string) (string, int) {
		if strings.HasPrefix(tag, "tunnel-") {
			if i := strings.LastIndex(tag, "-h"); i > 0 {
				n, err := strconv.Atoi(tag[i+2:])
				if err == nil {
					return tag[:i], n
				}
			}
		}
		return "", -1
	}
	brokenLabels := map[string]int{}
	for k := range broken {
		l, n := label(deletionText(tunnels[k]["tag"]))
		if old, ok := brokenLabels[l]; l != "" && (!ok || n > old) {
			brokenLabels[l] = n
		}
	}
	for k, t := range tunnels {
		l, n := label(deletionText(t["tag"]))
		if last, ok := brokenLabels[l]; ok && n <= last {
			broken[k] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for k, t := range tunnels {
			if broken[k] {
				continue
			}
			for other := range broken {
				hop := tunnels[other]
				if deletionID(t["target_port"]) == deletionID(hop["listen_port"]) && deletionServerHosts(servers[deletionID(hop["server_id"])])[deletionHost(t["target_address"])] {
					broken[k] = true
					changed = true
					break
				}
			}
		}
	}
	for k := range broken {
		t := tunnels[k]
		sid := deletionID(t["server_id"])
		tag := deletionText(t["tag"])
		for id, n := range nodes {
			if deletionServerHosts(servers[sid])[deletionNodeHost(n)] && deletionID(deletionObject(n["clash_config"])["port"]) == deletionID(t["listen_port"]) {
				reasons[id] = "隧道链依赖此服务器，入口将移除"
			}
		}
		if sid != p.ServerID && tag != "" {
			raw, _ := json.Marshal(t)
			p.Steps = append(p.Steps, serverDeletionStep{Kind: "tunnel", ServerID: sid, Tag: tag, Name: "移除隧道：" + deletionText(servers[sid]["name"]) + " / " + tag, Payload: raw})
		}
	}
}

// Every hop listens on the chain's same port. Reusing a server within one path
// creates a loop, so a nonempty group alone does not prove an alternative works.
func deletionChainHasPath(groups []int64, members map[int64][]map[string]any) bool {
	assigned := map[int64]int{}
	var assign func(int, map[int64]bool) bool
	assign = func(hop int, seen map[int64]bool) bool {
		for _, member := range members[groups[hop]] {
			id := deletionID(member["server_id"])
			if seen[id] {
				continue
			}
			seen[id] = true
			previous, used := assigned[id]
			if !used || assign(previous, seen) {
				assigned[id] = hop
				return true
			}
		}
		return false
	}
	for hop := range groups {
		if !assign(hop, map[int64]bool{}) {
			return false
		}
	}
	return true
}

type serverDeletionInbound struct {
	ServerID int64  `json:"server_id"`
	Tag      string `json:"tag"`
}
type serverDeletionIdentity struct {
	ServerID      int64  `json:"server_id"`
	Tag           string `json:"tag"`
	Identity      string `json:"identity,omitempty"`
	CredentialKey string `json:"credential_key,omitempty"`
}

func planDeletionIdentities(data serverDeletionInventory, p *serverDeletionPlan, nodes map[int64]map[string]any, reasons map[int64]string, servers map[int64]map[string]any) {
	serverIDs := map[string]int64{}
	for id, s := range servers {
		serverIDs[deletionText(s["name"])] = id
	}
	protected := map[string]bool{}
	identities := []serverDeletionIdentity{}
	refKey := func(id int64, tag, value string) string {
		return strconv.FormatInt(id, 10) + "\x00" + tag + "\x00" + value
	}
	for _, table := range []string{"user_inbound_configs", "package_assignment_inbound_configs", "user_subaccounts", "package_assignment_subaccounts"} {
		for _, row := range data[table] {
			id, tag, protocol := deletionID(row["server_id"]), deletionText(row["inbound_tag"]), deletionText(row["protocol"])
			nodeID := deletionID(row["routed_node_id"])
			n := nodes[nodeID]
			if nodeID > 0 {
				id = serverIDs[deletionText(n["original_server"])]
				tag = deletionText(n["inbound_tag"])
				protocol = deletionText(n["protocol"])
			}
			raw := deletionText(row["credential_json"])
			identity := deletionText(row["email"])
			if identity == "" {
				identity = extractProtocolIdentity(raw)
			}
			key := ""
			if c := deletionObject(raw); c != nil && nonEmptyCredentialValue(c, lifecycleCredentialPrimaryKey(protocol)) {
				key = accessCredentialKey(protocol, c)
			}
			ref := serverDeletionIdentity{ServerID: id, Tag: tag, Identity: identity, CredentialKey: key}
			if nodeID > 0 && reasons[nodeID] != "" && id != p.ServerID {
				identities = append(identities, ref)
			} else {
				if identity != "" {
					protected[refKey(id, tag, identity)] = true
				}
				if key != "" {
					protected[refKey(id, tag, key)] = true
				}
			}
		}
	}
	for _, ref := range identities {
		if protected[refKey(ref.ServerID, ref.Tag, ref.Identity)] {
			ref.Identity = ""
		}
		if protected[refKey(ref.ServerID, ref.Tag, ref.CredentialKey)] {
			ref.CredentialKey = ""
		}
		if ref.ServerID > 0 && (ref.Identity != "" || ref.CredentialKey != "") {
			p.Identities = append(p.Identities, ref)
		}
	}
	seen := map[string]bool{}
	for _, n := range p.Nodes {
		if n.Direct || n.InboundTag == "" || n.SourceServerID == 0 {
			continue
		}
		allDeleted := true
		for id, other := range nodes {
			if deletionText(other["original_server"]) == n.ServerName && deletionText(other["inbound_tag"]) == n.InboundTag && reasons[id] == "" {
				allDeleted = false
			}
		}
		key := refKey(n.SourceServerID, n.InboundTag, "")
		if allDeleted && !seen[key] {
			seen[key] = true
			p.Inbounds = append(p.Inbounds, serverDeletionInbound{ServerID: n.SourceServerID, Tag: n.InboundTag})
			p.Steps = append(p.Steps, serverDeletionStep{Kind: "inbound", ServerID: n.SourceServerID, Tag: n.InboundTag, Name: "删除失效入站：" + n.ServerName + " / " + n.InboundTag})
		}
	}
}
