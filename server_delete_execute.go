package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errServerDeletionWaiting = errors.New("等待 Helper 回传停止结果")

func (a *app) serverDeletionMachine(id int64) serverDeletionMachine {
	m := serverDeletionMachine{Helper: "unavailable", OfficialAgent: "卸载默认开启；官方返回成功仅代表已接受请求", Outstanding: []string{}, ManualCommand: serverDeletionManualCommand()}
	if a.helperState != nil {
		a.helperState.mu.Lock()
		identity, exists := a.helperState.data.Servers[strconv.FormatInt(id, 10)]
		status := a.helperState.data.AgentStatuses[strconv.FormatInt(id, 10)]
		a.helperState.mu.Unlock()
		if exists && time.Since(identity.LastSeenAt) <= helperStaleTimeout {
			m.Helper = "outdated"
			for _, c := range status.Capabilities {
				if c == "helper.uninstall" {
					m.Helper = "online"
				}
			}
		}
	}
	if m.Helper != "online" {
		m.Outstanding = append(m.Outstanding, "Helper 离线、未安装或版本过旧；机器上的入站可能仍接受连接，删除后须手动清理")
	}
	return m
}

func (a *app) executeServerDeletionStep(ctx context.Context, token string, p *serverDeletionPlan) error {
	if p.State == "deleted" {
		return nil
	}
	store := a.adminStore.(*postgresAdminSessionStore)
	p.State = "deleting"
	for i := range p.Steps {
		step := &p.Steps[i]
		if step.Status == lifecycleItemCompleted {
			continue
		}
		err := a.performServerDeletionStep(ctx, token, p, step)
		if errors.Is(err, errServerDeletionWaiting) {
			return store.saveServerDeletion(ctx, p)
		}
		step.Attempts++
		if err != nil {
			step.Status = lifecycleItemFailed
			step.LastError = lifecycleSafeError(err)
			p.State = "delete_partial"
		} else {
			step.Status = lifecycleItemCompleted
			step.LastError = ""
			step.Payload = nil
		}
		if step.Kind == "verify" && err == nil {
			p.State = "deleted"
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		saveErr := store.saveServerDeletion(persistCtx, p)
		cancel()
		if saveErr != nil {
			return saveErr
		}
		log.Printf("[mmwx-custom] server-delete operation=%s server_id=%d step=%s status=%s", p.OperationID, p.ServerID, step.Kind, step.Status)
		return nil
	}
	return nil
}

func (a *app) performServerDeletionStep(ctx context.Context, token string, p *serverDeletionPlan, step *serverDeletionStep) error {
	s := a.adminStore.(*postgresAdminSessionStore)
	op := func(hash string, id int64, payload any) error {
		body := map[string]any{"op": hash, "payload": payload}
		if id > 0 {
			body["p"] = []string{strconv.FormatInt(id, 10)}
		}
		return a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", body, nil)
	}
	exists := func(table string, id int64) (bool, error) {
		var found bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1)`, id).Scan(&found)
		return found, err
	}
	switch step.Kind {
	case "package":
		// This is the existing user-delete executor, including its empty-package
		// transaction and concurrent package-membership recheck.
		return a.executeLifecycleDeleteItem(ctx, token, "", step.Item)
	case "node", "trim-node":
		var raw []byte
		err := s.db.QueryRowContext(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, step.ID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var row map[string]any
		if err = json.Unmarshal(raw, &row); err != nil {
			return err
		}
		if step.Kind == "trim-node" {
			var payload map[string]any
			if err := json.Unmarshal(step.Payload, &payload); err != nil {
				return err
			}
			if hashJSON(deletionIDs(row["relay_group_node_ids"])) == hashJSON(deletionIDs(payload["relay_group_node_ids"])) {
				return nil
			}
		}
		if deletionNodeHash(row) != step.Hash {
			return errors.New("节点内容在预览后发生变化，未删除；请核对原操作范围")
		}
		if step.Kind == "trim-node" {
			var payload map[string]any
			_ = json.Unmarshal(step.Payload, &payload)
			delete(payload, "created_at")
			delete(payload, "updated_at")
			return a.officialLifecycleJSON(ctx, token, http.MethodPut, "/api/admin/nodes/"+strconv.FormatInt(step.ID, 10), payload, nil)
		}
		path := "/api/admin/nodes/" + strconv.FormatInt(step.ID, 10)
		if deletionText(row["node_type"]) == "routed" {
			path = "/api/admin/routed-outbound?id=" + strconv.FormatInt(step.ID, 10)
		}
		if err = a.officialLifecycleJSON(ctx, token, http.MethodDelete, path, nil, nil); err != nil {
			if deletionText(row["node_type"]) != "routed" {
				return err
			}
			if fallbackErr := a.finishServerDeletionRoutedNode(ctx, token, p, row); fallbackErr != nil {
				return fallbackErr
			}
		}
		found, err := exists("nodes", step.ID)
		if err == nil && found {
			err = errors.New("官方节点仍存在")
		}
		return err
	case "chain":
		found, err := exists("forward_chains", step.ID)
		if err != nil || !found {
			return err
		}
		return op("3f0f274e09b1bb7b", step.ID, nil)
	case "group":
		found, err := exists("forward_groups", step.ID)
		if err != nil || !found {
			return err
		}
		var otherMembers int
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM forward_group_members WHERE group_id=$1 AND server_id<>$2`, step.ID, p.ServerID).Scan(&otherMembers); err != nil {
			return err
		}
		if otherMembers > 0 {
			return errors.New("转发组新增了其他成员，未删除")
		}
		return a.officialLifecycleJSON(ctx, token, http.MethodDelete, "/api/admin/forward/groups/"+strconv.FormatInt(step.ID, 10), nil, nil)
	case "trim-group":
		var raw []byte
		if err := s.db.QueryRowContext(ctx, `SELECT to_jsonb(g) FROM forward_groups g WHERE id=$1`, step.ID).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT server_id,weight FROM forward_group_members WHERE group_id=$1 ORDER BY seq,server_id`, step.ID)
		if err != nil {
			return err
		}
		members := []map[string]any{}
		removed := false
		for rows.Next() {
			var id, weight int64
			if err = rows.Scan(&id, &weight); err != nil {
				break
			}
			if id == p.ServerID {
				removed = true
			} else {
				members = append(members, map[string]any{"server_id": id, "weight": weight})
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if rowErr != nil {
			return rowErr
		}
		if !removed {
			return nil
		}
		if len(members) == 0 {
			return errors.New("转发组已失去预览中的替代成员，未更新")
		}
		payload["members"] = members
		payload["failover_enabled"] = deletionID(payload["failover_enabled"]) != 0
		delete(payload, "created_at")
		delete(payload, "updated_at")
		return op("f7c0e7cac4816f02", step.ID, payload)

	case "subscription", "empty-subscription":
		var selected, stats string
		err := s.db.QueryRowContext(ctx, `SELECT selected_node_ids,stats_server_ids FROM subscribe_files WHERE id=$1`, step.ID).Scan(&selected, &stats)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal(step.Payload, &payload); err != nil {
			return err
		}
		if step.Kind == "subscription" && hashJSON(deletionIDs(selected)) == hashJSON(deletionIDs(payload["selected_node_ids"])) && stats == deletionText(payload["stats_server_ids"]) {
			return nil
		}
		if step.Hash != "" && hashJSON([]any{selected, stats}) != step.Hash {
			return errors.New("订阅选择在预览后发生变化，未修改")
		}
		if step.Kind == "empty-subscription" {
			return op("beb8910a68dcb189", step.ID, nil)
		}
		return op("0ca64033f5007a7b", step.ID, payload)

	case "certificate":
		found, err := exists("certificates", step.ID)
		if err != nil || !found {
			return err
		}
		if err = op("ceb11d5b8270d6e4", 0, map[string]any{"id": step.ID, "auto_renew": false}); err != nil {
			return err
		}
		return op("4efc5db6d20613cb", 0, map[string]any{"id": step.ID, "auto_deploy": false})
	case "inbound":
		var newNodes int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM nodes n JOIN remote_servers s ON s.name=n.original_server WHERE s.id=$1 AND n.inbound_tag=$2 AND NOT (n.id=ANY($3))`, step.ServerID, step.Tag, deletionPlanIDs(p)).Scan(&newNodes); err != nil {
			return err
		}
		if newNodes > 0 {
			return errors.New("依赖入站新增了节点，未移除")
		}
		config, err := a.fetchOfficialXrayConfig(ctx, token, step.ServerID)
		if err != nil {
			return a.serverDeletionRemoteFailure(ctx, p, step, err)
		}
		if findConfigInbound(config, step.Tag) == nil {
			return nil
		}
		if err = a.officialLifecycleJSON(ctx, token, http.MethodPost, serverDeletionInboundPath(step.ServerID), map[string]any{"action": "remove", "tag": step.Tag}, nil); err != nil {
			return a.serverDeletionRemoteFailure(ctx, p, step, err)
		}
		config, err = a.fetchOfficialXrayConfig(ctx, token, step.ServerID)
		if err != nil {
			return err
		}
		if findConfigInbound(config, step.Tag) != nil {
			return errors.New("失效入站仍存在")
		}
		return nil
	case "tunnel":
		var expected map[string]any
		if err := json.Unmarshal(step.Payload, &expected); err != nil {
			return err
		}
		config, err := a.fetchOfficialXrayConfig(ctx, token, step.ServerID)
		if err != nil {
			var status string
			if e := s.db.QueryRowContext(ctx, `SELECT status FROM remote_servers WHERE id=$1`, step.ServerID).Scan(&status); e == nil && status != "connected" {
				p.Machine.Outstanding = appendUniqueString(p.Machine.Outstanding, "依赖服务器 #"+strconv.FormatInt(step.ServerID, 10)+" 离线，其隧道 "+step.Tag+" 尚需人工移除")
				return nil
			}
			return err
		}
		if expected["kind"] == "routed" {
			return a.removeServerDeletionRoutedTunnel(ctx, token, step, expected, config)
		}
		inbound := findConfigInbound(config, step.Tag)
		if inbound == nil {
			return nil
		}
		settings := deletionObject(inbound["settings"])
		if (inbound["protocol"] != "dokodemo-door" && inbound["protocol"] != "tunnel") || deletionID(inbound["port"]) != deletionID(expected["listen_port"]) || deletionHost(settings["address"]) != deletionHost(expected["target_address"]) || deletionID(settings["port"]) != deletionID(expected["target_port"]) {
			return errors.New("隧道内容已变化，未移除")
		}
		if err = a.officialLifecycleJSON(ctx, token, http.MethodPost, serverDeletionInboundPath(step.ServerID), map[string]any{"action": "remove", "tag": step.Tag}, nil); err != nil {
			return err
		}
		config, err = a.fetchOfficialXrayConfig(ctx, token, step.ServerID)
		if err != nil {
			return err
		}
		if findConfigInbound(config, step.Tag) != nil {
			return errors.New("官方隧道仍存在")
		}
		return nil

	case "helper":
		return a.uninstallDeletedServerHelper(ctx, p)
	case "server":
		var raw []byte
		err := s.db.QueryRowContext(ctx, `SELECT to_jsonb(s) FROM remote_servers s WHERE id=$1`, p.ServerID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var server map[string]any
		_ = json.Unmarshal(raw, &server)
		if deletionServerHash(server) != p.ServerHash {
			return errors.New("服务器身份已变化，拒绝删除")
		}
		// Never let delete_nodes=true include a newly created node outside the saved plan.
		var remaining int
		err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM nodes WHERE original_server=$1`, p.ServerName).Scan(&remaining)
		if err != nil {
			return err
		}
		if remaining != 0 {
			return errors.New("服务器又有节点或节点尚未删除；未执行官方整服删除")
		}
		if p.UninstallAgent {
			// The official uninstall stream acknowledges before the remote shell exits.
			// Stop and inspect the runtime while the server still exists in official.
			stopCtx, stopCancel := context.WithTimeout(ctx, 8*time.Second)
			stopErr := a.officialLifecycleJSON(stopCtx, token, http.MethodPost, "/api/admin/remote/services/control?server_id="+strconv.FormatInt(p.ServerID, 10), map[string]any{"service": "xray", "action": "stop"}, nil)
			for stopErr == nil {
				var status struct {
					Xray *struct {
						Running bool `json:"running"`
					} `json:"xray"`
				}
				stopErr = a.officialLifecycleJSON(stopCtx, token, http.MethodGet, "/api/admin/remote/services/status?server_id="+strconv.FormatInt(p.ServerID, 10), nil, &status)
				if stopErr != nil || (status.Xray != nil && !status.Xray.Running) {
					break
				}
				if status.Xray == nil {
					stopErr = errors.New("官方运行状态未确认停止")
					break
				}
				select {
				case <-stopCtx.Done():
					stopErr = stopCtx.Err()
				case <-time.After(300 * time.Millisecond):
				}
			}
			stopCancel()
			if stopErr != nil {
				p.Machine.Outstanding = appendUniqueString(p.Machine.Outstanding, "官方 Core 停止未获确认；入站可能仍接受连接，请执行手动清理命令")
			}
		} else {
			p.Machine.Outstanding = appendUniqueString(p.Machine.Outstanding, "未选择卸载官方 Agent；内置或官方 Core 可能仍接受连接，请手动清理")
		}
		if err = op("348cdff26a5310c6", 0, map[string]any{"id": p.ServerID, "delete_nodes": true, "uninstall_agent": p.UninstallAgent}); err != nil {
			return err
		}
		return a.retireDeletedServerState(p)
	case "sweep":
		if err := s.sweepServerDeletion(ctx, p); err != nil {
			return err
		}
		if err := a.retireDeletedServerState(p); err != nil {
			return err
		}
		a.trafficGroupsMu.Lock()
		err := a.refreshTrafficGroupsLocked(ctx, s)
		a.trafficGroupsMu.Unlock()
		return err
	case "verify":
		var response struct {
			Servers []map[string]any `json:"servers"`
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "034e094d05aa3f83"}, &response); err != nil {
			return err
		}
		for _, server := range response.Servers {
			if deletionID(server["id"]) == p.ServerID {
				return errors.New("官方仍返回该服务器")
			}
		}
		var nodes struct {
			Nodes []map[string]any `json:"nodes"`
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/v3", map[string]any{"op": "c87c168b92b5f22d"}, &nodes); err != nil {
			return err
		}
		for _, n := range nodes.Nodes {
			if deletionText(n["original_server"]) == p.ServerName {
				return errors.New("官方仍返回该服务器的节点")
			}
			for _, deleted := range p.Nodes {
				if deletionID(n["id"]) == deleted.ID {
					return errors.New("官方仍返回已删除的依赖节点")
				}
			}
		}
		var packages struct {
			Packages []map[string]any `json:"packages"`
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodGet, "/api/admin/packages", nil, &packages); err != nil {
			return err
		}
		for _, pkg := range packages.Packages {
			for _, id := range deletionIDs(pkg["nodes"]) {
				for _, deleted := range p.Nodes {
					if id == deleted.ID {
						return errors.New("官方套餐仍引用已删除节点")
					}
				}
			}
		}
		return s.verifyServerDeletion(ctx, p)
	default:
		return fmt.Errorf("未知删除步骤 %s", step.Kind)
	}
}

func (a *app) uninstallDeletedServerHelper(ctx context.Context, p *serverDeletionPlan) error {
	if a.helperState == nil {
		return nil
	}
	id := strconv.FormatInt(p.ServerID, 10)
	if p.Machine.CommandID == "" {
		current := a.serverDeletionMachine(p.ServerID)
		if current.Helper != "online" {
			p.Machine.Helper = current.Helper
			p.Machine.Outstanding = appendUniqueString(p.Machine.Outstanding, "Helper 无法执行卸载；机器上的入站可能仍接受连接，请手动清理")
			return nil
		}
		a.helperState.mu.Lock()
		delete(a.helperState.data.CoreModeIntents, id)
		a.helperState.data.ManagementCommands[id] = nil
		err := a.helperState.saveLocked()
		a.helperState.mu.Unlock()
		if err != nil {
			return err
		}
		command, err := a.helperState.enqueueManagementCommand(id, "helper.uninstall", nil)
		if err != nil {
			return err
		}
		p.Machine.CommandID = command.ID
		now := time.Now().UTC()
		p.Machine.StartedAt = &now
		p.Machine.Helper = "waiting"
		return errServerDeletionWaiting
	}
	a.helperState.mu.Lock()
	results := append([]managementResult(nil), a.helperState.data.ManagementResults[id]...)
	a.helperState.mu.Unlock()
	for _, result := range results {
		if result.CommandID != p.Machine.CommandID {
			continue
		}
		var acknowledgement struct {
			CoreStopped bool `json:"core_stopped"`
		}
		_ = json.Unmarshal(result.Data, &acknowledgement)
		if result.Success && acknowledgement.CoreStopped && result.Action == "helper.uninstall" {
			p.Machine.Helper = "core_stopped"
			kept := []string{}
			for _, warning := range p.Machine.Outstanding {
				if !strings.HasPrefix(warning, "Helper ") {
					kept = append(kept, warning)
				}
			}
			p.Machine.Outstanding = kept
			return nil
		}
		p.Machine.Helper = "failed"
		p.Machine.CommandID = ""
		return errors.New("Helper 卸载失败：" + result.Message)
	}
	if p.Machine.StartedAt != nil && time.Since(*p.Machine.StartedAt) > 45*time.Second {
		p.Machine.Helper = "unconfirmed"
		p.Machine.Outstanding = appendUniqueString(p.Machine.Outstanding, "Helper 卸载未回传确认；机器可能仍接受连接，请手动清理")
		return nil
	}
	return errServerDeletionWaiting
}

func (a *app) retireDeletedServerState(p *serverDeletionPlan) error {
	id := strconv.FormatInt(p.ServerID, 10)
	retiredIdentity := func(serverID string, identity serverConnectionIdentity) bool {
		for _, ref := range p.Identities {
			if strconv.FormatInt(ref.ServerID, 10) == serverID && ref.Tag == identity.InboundTag && ref.Identity != "" && ref.Identity == identity.User {
				return true
			}
		}
		for _, inbound := range p.Inbounds {
			if strconv.FormatInt(inbound.ServerID, 10) == serverID && inbound.Tag == identity.InboundTag {
				return true
			}
		}
		return false
	}
	if a.helperState != nil {
		a.helperState.mu.Lock()
		delete(a.helperState.data.Servers, id)
		delete(a.helperState.data.AgentStatuses, id)
		delete(a.helperState.data.CoreModeIntents, id)
		delete(a.helperState.data.ManagementCommands, id)
		delete(a.helperState.data.ManagementResults, id)
		delete(a.helperState.data.ConnectionSettings, id)
		delete(a.helperState.data.MachineProtectionDefaults, id)
		delete(a.helperState.data.ExternalConfigRepairs, id)
		for key, record := range a.helperState.data.InstallTokens {
			if record.OfficialServerID == id {
				delete(a.helperState.data.InstallTokens, key)
			}
		}
		for key, record := range a.helperState.data.RebindTokens {
			if record.OfficialServerID == id {
				delete(a.helperState.data.RebindTokens, key)
			}
		}
		for sid, settings := range a.helperState.data.ConnectionSettings {
			retired := func(identity serverConnectionIdentity) bool {
				return retiredIdentity(sid, identity)
			}
			users := settings.Users[:0]
			for _, u := range settings.Users {
				if !retired(u.Identity) {
					users = append(users, u)
				}
			}
			settings.Users = users
			mappings := settings.ManagementMappings[:0]
			for _, m := range settings.ManagementMappings {
				if !retired(m.Identity) {
					mappings = append(mappings, m)
				}
			}
			settings.ManagementMappings = mappings
			ports := settings.Ports[:0]
			for _, port := range settings.Ports {
				if !retired(serverConnectionIdentity{InboundTag: port.InboundTag}) {
					ports = append(ports, port)
				}
			}
			settings.Ports = ports
			if settings.BlockedIdentities != nil {
				kept := []serverConnectionIdentity{}
				for _, identity := range *settings.BlockedIdentities {
					if !retired(identity) {
						kept = append(kept, identity)
					}
				}
				settings.BlockedIdentities = &kept
			}
			a.helperState.data.ConnectionSettings[sid] = settings
		}

		err := a.helperState.saveLocked()
		a.helperState.mu.Unlock()
		if err != nil {
			return err
		}
	}
	a.coreModeSessions.Delete(id)
	affectedServers := map[string]bool{id: true}
	for _, node := range p.Nodes {
		if node.SourceServerID > 0 {
			affectedServers[strconv.FormatInt(node.SourceServerID, 10)] = true
		}
	}
	a.connectionMu.Lock()
	delete(a.connectionMetrics, id)
	for serverID := range affectedServers {
		delete(a.detailedConnections, serverID)
	}
	delete(a.helperRate, id)
	a.connectionMu.Unlock()
	a.userRateMu.Lock()
	delete(a.userRateOwnership, id)
	for serverID := range affectedServers {
		delete(a.userRateOwnership, serverID)
	}
	for key := range a.userRateBaselines {
		if affectedServers[strings.SplitN(key, "\x00", 2)[0]] {
			delete(a.userRateBaselines, key)
		}
	}
	a.userRateMu.Unlock()
	a.trafficGroupsMu.Lock()
	delete(a.trafficGroupBlocks, id)
	for user, servers := range a.disabledUserBlocks {
		delete(servers, id)
		for serverID, identities := range servers {
			kept := identities[:0]
			for _, identity := range identities {
				if !retiredIdentity(serverID, identity) {
					kept = append(kept, identity)
				}
			}
			servers[serverID] = kept
		}
		a.disabledUserBlocks[user] = servers
	}
	for user, items := range a.disabledUserAccess {
		kept := items[:0]
		for _, item := range items {
			if item.ServerID != p.ServerID && !containsLifecycleNode(deletionPlanIDs(p), item.NodeID) && !retiredIdentity(strconv.FormatInt(item.ServerID, 10), serverConnectionIdentity{InboundTag: item.InboundTag}) {
				kept = append(kept, item)
			}
		}
		a.disabledUserAccess[user] = kept
	}
	a.trafficGroupsMu.Unlock()
	return nil
}

func (a *app) serverDeletionIndexHandler(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	s, ok := a.adminStore.(*postgresAdminSessionStore)
	if !ok {
		writeJSON(w, 503, map[string]any{"message": "删除记录不可用"})
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT plan FROM mmwxc_server_deletions ORDER BY created_at DESC LIMIT 50`)
	if err != nil {
		writeJSON(w, 500, map[string]any{"message": "读取删除记录失败"})
		return
	}
	defer rows.Close()
	plans := []serverDeletionPlan{}
	for rows.Next() {
		var raw json.RawMessage
		if rows.Scan(&raw) != nil {
			writeJSON(w, 500, map[string]any{"message": "读取删除记录失败"})
			return
		}
		var plan serverDeletionPlan
		if json.Unmarshal(raw, &plan) != nil {
			writeJSON(w, 500, map[string]any{"message": "删除记录损坏"})
			return
		}
		plans = append(plans, publicServerDeletion(plan))
	}
	writeJSON(w, 200, map[string]any{"success": true, "plans": plans})
}

// Kept separate from public URL construction so no Helper tokens enter plans.
func serverDeletionInboundPath(serverID int64) string {
	return "/api/admin/remote/inbounds?server_id=" + url.QueryEscape(strconv.FormatInt(serverID, 10))
}

func (a *app) serverDeletionRemoteFailure(ctx context.Context, p *serverDeletionPlan, step *serverDeletionStep, err error) error {
	var status string
	s := a.adminStore.(*postgresAdminSessionStore)
	if e := s.db.QueryRowContext(ctx, `SELECT status FROM remote_servers WHERE id=$1`, step.ServerID).Scan(&status); e == nil && status != "connected" {
		p.Machine.Outstanding = appendUniqueString(p.Machine.Outstanding, "依赖服务器 #"+strconv.FormatInt(step.ServerID, 10)+" 离线，入站 "+step.Tag+" 尚需人工清理")
		return nil
	}
	return err
}

func serverDeletionManualCommand() string {
	ref := customVersion
	if !strings.HasPrefix(ref, "v") {
		ref = "main"
	}
	return "# 仅在已删除的服务器执行（systemd）\n" +
		"bash -o pipefail -c 'curl -fsSL https://raw.githubusercontent.com/ImoLR/mmwx-custom/" + ref + "/scripts/uninstall-helper.sh | sudo bash' && " +
		`sudo bash -c 'set -e
for unit in mmw-agent.service mmwx-guard-agent.service xray.service; do
  if systemctl cat "$unit" >/dev/null 2>&1; then
    systemctl stop "$unit"
    systemctl disable "$unit"
    if systemctl is-active --quiet "$unit"; then echo "停止失败: $unit" >&2; exit 1; fi
  fi
done
rm -f /etc/systemd/system/mmw-agent.service /etc/systemd/system/mmwx-guard-agent.service /usr/local/bin/mmw-agent /usr/local/bin/mmwx-guardd-agent /usr/local/share/mmwx-guard/agent.manifest
rm -rf /etc/mmw-agent
systemctl daemon-reload'`
}
