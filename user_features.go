package main

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const disabledUserFeatureWarning = "该用户处于禁用状态，此操作可能使其节点凭据重新可用"

type managedUserFeatureRequest struct {
	Username               string          `json:"username"`
	PackageID              int64           `json:"package_id"`
	AssignmentID           int64           `json:"assignment_id"`
	StartDate              string          `json:"start_date"`
	ExpireDate             string          `json:"expire_date"`
	Permanent              *bool           `json:"permanent"`
	IsReset                *bool           `json:"is_reset"`
	ResetDay               *int            `json:"reset_day"`
	InheritExpireDate      *bool           `json:"inherit_expire_date"`
	InheritTraffic         *bool           `json:"inherit_traffic"`
	TrafficLimitOverrideGB json.RawMessage `json:"traffic_limit_override_gb"`
	Days                   int             `json:"days"`
	ConfirmDisabled        bool            `json:"confirm_disabled"`
}

func validateManagedUserFeature(action string, body managedUserFeatureRequest) error {
	if (action == "assign-package" || action == "add-assignment") && body.PackageID <= 0 {
		return errors.New("请选择套餐")
	}
	if (action == "edit-assignment" || action == "unbind-assignment") && body.AssignmentID <= 0 {
		return errors.New("请选择已绑定的套餐")
	}
	if action == "renew" && (body.Days < 1 || body.Days > 3650) {
		return errors.New("续期天数必须为 1–3650 的整数")
	}
	if body.ResetDay != nil && (*body.ResetDay < 1 || *body.ResetDay > 31) {
		return errors.New("每月重置日必须为 1–31")
	}
	if len(body.TrafficLimitOverrideGB) > 0 && string(body.TrafficLimitOverrideGB) != "null" {
		var value float64
		if json.Unmarshal(body.TrafficLimitOverrideGB, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= float64(math.MaxInt64)/(1<<30) {
			return errors.New("流量覆写必须为空或非负数，单位为 GiB")
		}
	}
	for _, value := range []string{body.StartDate, body.ExpireDate} {
		if value != "" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return errors.New("日期格式必须为 YYYY-MM-DD")
			}
		}
	}
	return nil
}

func userFeatureLifecycleError(state managedUserLifecycle, writesCredentials, confirmed bool) error {
	if state.DesiredState == lifecycleStateDeleted || state.EffectiveState == lifecycleStateDeleting || state.EffectiveState == lifecycleStateDeletePartial || state.EffectiveState == lifecycleStateDeleted {
		return errors.New("用户正在删除或删除未完成，请先完成删除，不能执行此操作")
	}
	needsConfirm := state.DesiredState == lifecycleStateDisabled || state.EffectiveState == lifecycleStateDisabled || strings.HasPrefix(state.EffectiveState, "partially_") || state.EffectiveState == lifecycleStateEnabling || state.EffectiveState == lifecycleStateDisabling
	if writesCredentials && needsConfirm && !confirmed {
		return errors.New(disabledUserFeatureWarning)
	}
	return nil
}

func managedUserPackageOwnerCheck(ctx context.Context, db lifecycleQueryer, username string, packageID int64) error {
	var exists, otherOwner bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM packages WHERE id=$1),
		EXISTS(SELECT 1 FROM users WHERE package_id=$1 AND username<>$2) OR
		EXISTS(SELECT 1 FROM user_package_assignments WHERE package_id=$1 AND username<>$2)`, packageID, username).Scan(&exists, &otherOwner); err != nil {
		return err
	}
	if !exists {
		return errors.New("套餐不存在")
	}
	if otherOwner {
		return errors.New("该套餐已绑定其他用户，一个套餐只能绑定一个用户")
	}
	return nil
}

func (s *postgresAdminSessionStore) availableManagedUserPackages(ctx context.Context, username string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id FROM packages p
		WHERE NOT EXISTS(SELECT 1 FROM users u WHERE u.package_id=p.id AND u.username<>$1)
		AND NOT EXISTS(SELECT 1 FROM user_package_assignments a WHERE a.package_id=p.id AND a.username<>$1)
		ORDER BY p.id`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *postgresAdminSessionStore) lockManagedUserPackage(ctx context.Context, username string, packageID int64) (func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("mmwxc-user-package:%d", packageID)
	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil {
		conn.Close()
		return nil, err
	}
	if !locked {
		conn.Close()
		return nil, errors.New("套餐正在变更，请重试")
	}
	unlock := func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		conn.Close()
	}
	if err := managedUserPackageOwnerCheck(ctx, conn, username, packageID); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

func (a *app) userFeatureHandler(w http.ResponseWriter, r *http.Request, username, action string) {
	username, err := url.PathUnescape(username)
	if err != nil || strings.TrimSpace(username) == "" || len(username) > 256 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "用户名无效"})
		return
	}
	store, ok := a.adminStore.(*postgresAdminSessionStore)
	if !ok || store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "用户管理数据库不可用"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if action == "imported-nodes" && r.Method == http.MethodGet {
		token := strings.TrimSpace(r.Header.Get("MM-Authorization"))
		var result map[string]any
		if token == "" || a.officialLifecycleJSON(r.Context(), token, http.MethodGet, "/api/admin/nodes/user-imported?username="+url.QueryEscape(username), nil, &result) != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "message": "读取导入节点失败"})
			return
		}
		pending, _, err := store.importedNodeCleanupSnapshot(r.Context(), username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取节点清理状态失败"})
			return
		}
		result["cleanup_pending"] = pending != ""
		writeJSON(w, http.StatusOK, result)
		return
	}
	if action == "available-packages" && r.Method == http.MethodGet {
		ids, err := store.availableManagedUserPackages(r.Context(), username)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取可用套餐失败"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"package_ids": ids})
		return
	}
	method, path, op := "", "", ""
	writesCredentials := true
	switch action {
	case "assign-package":
		method, path = http.MethodPost, "/api/admin/packages/assign"
	case "unassign-package":
		method, path, writesCredentials = http.MethodPost, "/api/admin/packages/unassign", false
	case "add-assignment":
		method, path = http.MethodPost, "/api/admin/package-assignments"
	case "edit-assignment":
		method, path = http.MethodPut, "/api/admin/package-assignments"
	case "unbind-assignment":
		method, path, writesCredentials = http.MethodDelete, "/api/admin/package-assignments", false
	case "renew":
		method, path = http.MethodPost, "/api/admin/users/extend"
	case "replace-credentials":
		op = "77d5514260062000"
	case "repair-credentials":
		method, path = http.MethodPost, "/api/admin/users/repair-node-credentials"
	case "clear-imported-nodes":
		writesCredentials = false
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "not found"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	var body managedUserFeatureRequest
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求内容无效"})
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var payload map[string]any
	if decoder.Decode(&body) != nil || json.Unmarshal(raw, &payload) != nil || payload == nil || (body.Username != "" && body.Username != username) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求内容或用户名无效"})
		return
	}
	if err := validateManagedUserFeature(action, body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
		return
	}
	token := strings.TrimSpace(r.Header.Get("MM-Authorization"))
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "需要官方管理员会话"})
		return
	}
	unlock := a.lockUserLifecycle(username)
	defer unlock()
	state, err := store.ManagedUserState(r.Context(), username)
	if err != nil || !state.Exists {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "用户不存在或无法读取"})
		return
	}
	states, err := store.LifecycleStates(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取用户生命周期状态失败"})
		return
	}
	if err := userFeatureLifecycleError(states[username], writesCredentials, body.ConfirmDisabled); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": err.Error()})
		return
	}
	if action == "replace-credentials" || action == "repair-credentials" {
		operator, err := store.AdminUsername(r.Context(), token)
		if err != nil || operator != username || state.Role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]any{"success": false, "message": "只能操作当前登录管理员自己的凭据"})
			return
		}
	}
	if action == "edit-assignment" || action == "unbind-assignment" {
		var owner string
		if err := store.db.QueryRowContext(r.Context(), `SELECT username,package_id FROM user_package_assignments WHERE id=$1`, body.AssignmentID).Scan(&owner, &body.PackageID); err != nil || owner != username {
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": "套餐绑定不存在或不属于该用户"})
			return
		}
	}
	if body.PackageID > 0 && action != "unbind-assignment" {
		unlockPackage, err := store.lockManagedUserPackage(r.Context(), username, body.PackageID)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": err.Error()})
			return
		}
		defer unlockPackage()
	}
	if action == "clear-imported-nodes" {
		result, err := a.clearManagedUserImportedNodes(r.Context(), token, username, store)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	delete(payload, "confirm_disabled")
	payload["username"] = username
	if action == "edit-assignment" || action == "unbind-assignment" {
		delete(payload, "package_id")
	}
	if action == "replace-credentials" || action == "repair-credentials" {
		payload = nil
	}
	var result map[string]any
	var upstreamBody any = payload
	if op != "" {
		method, path = http.MethodPost, "/api/v3"
		upstreamBody = map[string]any{"op": op, "payload": payload}
	}
	if err := a.officialLifecycleJSON(r.Context(), token, method, path, upstreamBody, &result); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "message": err.Error()})
		return
	}
	if action == "assign-package" && len(body.TrafficLimitOverrideGB) > 0 {
		var override any
		_ = json.Unmarshal(body.TrafficLimitOverrideGB, &override)
		if err := a.officialLifecycleJSON(r.Context(), token, http.MethodPut, "/api/admin/users/traffic-limit", map[string]any{"username": username, "traffic_limit_override_gb": override}, nil); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"success": false, "message": "套餐已保存，但流量覆写保存失败，请重新保存：" + err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, result)
}
