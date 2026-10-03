package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type packageTrafficGroup struct {
	ID        int64     `json:"id"`
	PackageID int64     `json:"package_id"`
	Name      string    `json:"name"`
	Limit     int64     `json:"limit_bytes"`
	NodeIDs   []int64   `json:"node_ids"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type trafficGroupPackage struct {
	ID          int64
	Limit       int64
	NodeIDs     []int64
	NodeLimits  map[string]float64
	TrafficMode string
	Groups      []packageTrafficGroup
}

type trafficGroupAssignment struct {
	ID            int64
	PackageID     int64
	Username      string
	Start         time.Time
	End           sql.NullTime
	LastReset     sql.NullTime
	MonthlyReset  bool
	ResetDay      int
	LimitOverride sql.NullInt64
}

type trafficGroupNodeStatus struct {
	NodeID     int64  `json:"node_id"`
	NodeName   string `json:"node_name"`
	ServerName string `json:"server_name"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

type packageTrafficGroupUsage struct {
	Username     string                   `json:"username"`
	AssignmentID int64                    `json:"assignment_id"`
	GroupID      int64                    `json:"group_id"`
	GroupName    string                   `json:"group_name"`
	Used         int64                    `json:"used_bytes"`
	Limit        int64                    `json:"limit_bytes"`
	Blocked      bool                     `json:"blocked"`
	CycleStart   time.Time                `json:"cycle_start"`
	CycleEnd     *time.Time               `json:"cycle_end"`
	Nodes        []trafficGroupNodeStatus `json:"nodes"`
}

type packageTrafficGroupUsageResponse struct {
	Usage []packageTrafficGroupUsage `json:"usage"`
	Nodes []trafficGroupNodeStatus   `json:"nodes"`
}

type trafficGroupValidationError struct{ message string }

func (e trafficGroupValidationError) Error() string { return e.message }

func validatePackageTrafficGroups(groups []packageTrafficGroup, pkg trafficGroupPackage, nodes map[int64]trafficGroupNode) error {
	allowed := map[int64]bool{}
	for _, id := range pkg.NodeIDs {
		allowed[id] = true
	}
	seenNodes, seenIDs := map[int64]bool{}, map[int64]bool{}
	for _, group := range groups {
		if strings.TrimSpace(group.Name) == "" || len([]rune(group.Name)) > 100 {
			return trafficGroupValidationError{"共享组名称不能为空且不能超过 100 个字符"}
		}
		if group.Limit <= 0 || pkg.Limit < 0 || (pkg.Limit > 0 && group.Limit > pkg.Limit) {
			return trafficGroupValidationError{"共享额度必须大于 0 且不能超过套餐总额度"}
		}
		if group.ID < 0 || (group.ID > 0 && seenIDs[group.ID]) {
			return trafficGroupValidationError{"共享组编号无效或重复"}
		}
		seenIDs[group.ID] = true
		if len(group.NodeIDs) == 0 {
			return trafficGroupValidationError{"每个共享组至少需要一个节点"}
		}
		for _, id := range group.NodeIDs {
			if _, exists := nodes[id]; !exists || (len(pkg.NodeIDs) > 0 && !allowed[id]) {
				return trafficGroupValidationError{"共享组成员必须属于套餐关联节点"}
			}
			if seenNodes[id] {
				return trafficGroupValidationError{"每个节点只能属于一个共享组，且不能重复选择"}
			}
			if math.Floor(pkg.NodeLimits[strconv.FormatInt(id, 10)]*float64(1<<30)) > float64(group.Limit) {
				return trafficGroupValidationError{"成员节点的单节点额度不能超过所在共享组额度"}
			}
			seenNodes[id] = true
		}
	}
	return nil
}

func filterTrafficGroupNodes(groups []packageTrafficGroup, pkg trafficGroupPackage, nodes map[int64]trafficGroupNode) []packageTrafficGroup {
	allowed := map[int64]bool{}
	for _, id := range pkg.NodeIDs {
		allowed[id] = true
	}
	result := make([]packageTrafficGroup, 0, len(groups))
	for _, group := range groups {
		group.NodeIDs = append([]int64{}, group.NodeIDs...)
		kept := group.NodeIDs[:0]
		for _, id := range group.NodeIDs {
			if _, exists := nodes[id]; exists && (len(pkg.NodeIDs) == 0 || allowed[id]) {
				kept = append(kept, id)
			}
		}
		group.NodeIDs = kept
		result = append(result, group)
	}
	return result
}

func (a *app) packageTrafficGroupsHandler(w http.ResponseWriter, r *http.Request) {
	if err := a.authorizeOperatorRequest(r); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/custom/packages/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[1] != "traffic-groups" || (len(parts) == 3 && parts[2] != "usage") {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	store, ok := a.adminStore.(*postgresAdminSessionStore)
	if !ok || store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "共享组数据库不可用"})
		return
	}
	if r.Method != http.MethodGet && (r.Method != http.MethodPut || len(parts) == 3) {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	a.trafficGroupsMu.Lock()
	defer a.trafficGroupsMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if len(parts) == 3 {
		var pkg trafficGroupPackage
		if pkg, err = store.trafficGroupPackage(ctx, id); err == nil {
			err = a.refreshTrafficGroupsLocked(ctx, store)
			if err != nil {
				a.trafficGroupsReady = false
			}
		}
		if err == nil {
			response, exists := a.trafficGroupUsage[id]
			if !exists {
				var nodes map[int64]trafficGroupNode
				nodes, err = store.trafficGroupNodes(ctx)
				if err == nil {
					a.connectionMu.Lock()
					response = packageTrafficGroupUsageResponse{Usage: []packageTrafficGroupUsage{}, Nodes: trafficGroupPackageNodeStatuses(pkg, nodes, a.detailedConnections, time.Now())}
					a.connectionMu.Unlock()
				}
			}
			if err == nil {
				writeJSON(w, http.StatusOK, response)
				return
			}
		}
	} else if r.Method == http.MethodPut {
		var body struct {
			Groups []packageTrafficGroup `json:"groups"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&body); err != nil || decoder.Decode(&struct{}{}) != io.EOF || body.Groups == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "请提供有效的共享组列表（清空时使用 []）"})
			return
		}
		var groups []packageTrafficGroup
		groups, err = store.replaceTrafficGroups(ctx, id, body.Groups)
		if err == nil {
			// Persisted groups are authoritative even if a later usage refresh fails.
			a.trafficGroupsReady = false
			if refreshErr := a.refreshTrafficGroupsLocked(ctx, store); refreshErr != nil {
				log.Printf("[mmwx-custom] traffic groups refresh after save: %v", refreshErr)
			}
			writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
			return
		}
	} else {
		var pkg trafficGroupPackage
		pkg, err = store.trafficGroupPackage(ctx, id)
		if err == nil {
			var nodes map[int64]trafficGroupNode
			nodes, err = store.trafficGroupNodes(ctx)
			if err == nil {
				writeJSON(w, http.StatusOK, map[string]any{"groups": filterTrafficGroupNodes(pkg.Groups, pkg, nodes)})
				return
			}
		}
	}
	var validation trafficGroupValidationError
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "套餐不存在"})
	} else if errors.As(err, &validation) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": validation.Error()})
	} else {
		log.Printf("[mmwx-custom] traffic groups package=%d: %v", id, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": "共享组读取或保存失败"})
	}
}

func (a *app) runTrafficGroups(ctx context.Context) {
	store, ok := a.adminStore.(*postgresAdminSessionStore)
	if !ok || store == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		a.trafficGroupsMu.Lock()
		refreshCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := a.refreshTrafficGroupsLocked(refreshCtx, store)
		cancel()
		if err != nil {
			a.trafficGroupsReady = false
			log.Printf("[mmwx-custom] traffic groups refresh failed: %v", err)
		}
		a.trafficGroupsMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func helperSupportsTrafficBlocks(version string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	version, _, _ = strings.Cut(version, "-")
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil || fmt.Sprintf("%d.%d.%d", major, minor, patch) != version || major < 0 || minor < 0 || patch < 0 {
		return false
	}
	return major > 0 || minor > 6 || (minor == 6 && patch >= 8)
}

func (a *app) trafficBlocksForHelper(settings serverConnectionSettings, serverID, version string) serverConnectionSettings {
	settings.BlockedIdentities = nil
	if !helperSupportsTrafficBlocks(version) {
		return settings
	}
	a.trafficGroupsMu.Lock()
	defer a.trafficGroupsMu.Unlock()
	if a.trafficGroupsReady {
		identities := append([]serverConnectionIdentity{}, a.trafficGroupBlocks[serverID]...)
		settings.BlockedIdentities = &identities
	}
	return settings
}
