package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
)

// A resumed official routed delete can fail after its remote configuration was
// already removed. Only fall back to the node op when no live route or credential
// remains, or when the machine cannot be reached and is recorded for cleanup.
func (a *app) finishServerDeletionRoutedNode(ctx context.Context, token string, p *serverDeletionPlan, row map[string]any) error {
	id := deletionID(row["id"])
	var sourceID int64
	for _, node := range p.Nodes {
		if node.ID == id {
			sourceID = node.SourceServerID
		}
	}
	if sourceID == 0 {
		return errors.New("路由节点的服务器归属无法确认")
	}
	s := a.adminStore.(*postgresAdminSessionStore)
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM remote_servers WHERE id=$1`, sourceID).Scan(&status); err != nil {
		return err
	}
	if sourceID != p.ServerID && status == "connected" {
		config, err := a.fetchOfficialXrayConfig(ctx, token, sourceID)
		if err != nil {
			return err
		}
		tag := deletionText(row["routed_outbound_tag"])
		identities := map[string]bool{}
		for _, raw := range []any{row["routed_admin_credential"], row["clash_config"], row["parsed_config"]} {
			for _, key := range []string{"id", "uuid", "password", "email"} {
				if value := deletionText(deletionObject(raw)[key]); value != "" {
					identities[value] = true
				}
			}
		}
		for _, table := range []string{"user_subaccounts", "package_assignment_subaccounts"} {
			rows, err := s.db.QueryContext(ctx, `SELECT credential_json,email FROM `+table+` WHERE routed_node_id=$1`, id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var raw, email string
				if err = rows.Scan(&raw, &email); err != nil {
					break
				}
				identities[email] = email != ""
				for _, key := range []string{"id", "uuid", "password"} {
					if value := deletionText(deletionObject(raw)[key]); value != "" {
						identities[value] = true
					}
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
		}
		var retained func(any) bool
		retained = func(value any) bool {
			switch v := value.(type) {
			case map[string]any:
				for key, item := range v {
					text := deletionText(item)
					if ((key == "tag" || key == "outboundTag") && tag != "" && text == tag) || ((key == "id" || key == "password" || key == "email") && identities[text]) || retained(item) {
						return true
					}
				}
			case []any:
				for _, item := range v {
					if retained(item) {
						return true
					}
				}
			}
			return false
		}
		if retained(config) {
			return errors.New("官方路由删除失败，远程仍保留该路由或凭据，须重试")
		}
	} else if sourceID != p.ServerID {
		p.Machine.Outstanding = appendUniqueString(p.Machine.Outstanding, "依赖服务器 #"+strconv.FormatInt(sourceID, 10)+" 离线，路由节点 "+deletionText(row["node_name"])+" 的机器端配置仍需人工清理")
	}
	return a.officialLifecycleJSON(ctx, token, http.MethodDelete, "/api/admin/nodes/"+strconv.FormatInt(id, 10), nil, nil)
}

func (a *app) removeServerDeletionRoutedTunnel(ctx context.Context, token string, step *serverDeletionStep, expected map[string]any, config map[string]any) error {
	findOutbound := func(config map[string]any) map[string]any {
		items, _ := config["outbounds"].([]any)
		for _, value := range items {
			outbound := deletionObject(value)
			if outbound["tag"] == step.Tag {
				return outbound
			}
		}
		return nil
	}
	if outbound := findOutbound(config); outbound != nil {
		settings := deletionObject(outbound["settings"])
		host, port, err := net.SplitHostPort(deletionText(settings["redirect"]))
		if err != nil || deletionHost(host) != deletionHost(expected["target_address"]) || deletionID(port) != deletionID(expected["target_port"]) {
			return errors.New("路由隧道目标已变化或无法核对，未移除")
		}
	}
	path := "/api/admin/remote/routing?server_id=" + strconv.FormatInt(step.ServerID, 10)
	for attempt := 0; attempt < 50; attempt++ {
		rules, _ := deletionObject(config["routing"])["rules"].([]any)
		index := -1
		for i, value := range rules {
			if deletionObject(value)["outboundTag"] == step.Tag {
				index = i
				break
			}
		}
		if index < 0 {
			if findOutbound(config) == nil {
				return nil
			}
			if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, "/api/admin/remote/outbounds?server_id="+strconv.FormatInt(step.ServerID, 10), map[string]any{"action": "remove", "tag": step.Tag}, nil); err != nil {
				return err
			}
			verified, err := a.fetchOfficialXrayConfig(ctx, token, step.ServerID)
			if err != nil {
				return err
			}
			if findOutbound(verified) != nil {
				return errors.New("路由隧道出站仍存在")
			}
			return nil
		}
		if err := a.officialLifecycleJSON(ctx, token, http.MethodPost, path, map[string]any{"action": "remove_rule", "index": index}, nil); err != nil {
			return err
		}
		var err error
		config, err = a.fetchOfficialXrayConfig(ctx, token, step.ServerID)
		if err != nil {
			return err
		}
	}
	return errors.New("路由隧道规则尚未全部移除，请继续重试")
}
