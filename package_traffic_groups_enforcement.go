package main

import (
	"context"
	"log"
	"math"
	"sort"
	"strconv"
	"time"
)

func trafficGroupCycle(assignment trafficGroupAssignment, now time.Time) (time.Time, *time.Time) {
	start := trafficGroupLocalTimestamp(assignment.Start, now.Location())
	var end *time.Time
	if assignment.End.Valid {
		value := trafficGroupLocalTimestamp(assignment.End.Time, now.Location())
		end = &value
	}
	if assignment.MonthlyReset {
		day := assignment.ResetDay
		if day < 1 {
			day = 1
		}
		if day > 31 {
			day = 31
		}
		monthDay := func(year int, month time.Month) time.Time {
			last := time.Date(year, month+1, 0, 0, 0, 0, 0, now.Location()).Day()
			return time.Date(year, month, min(day, last), 0, 0, 0, 0, now.Location())
		}
		reset := monthDay(now.Year(), now.Month())
		if reset.After(now) {
			previous := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, now.Location())
			reset = monthDay(previous.Year(), previous.Month())
		}
		if reset.After(start) {
			start = reset
		}
		next := time.Date(reset.Year(), reset.Month()+1, 1, 0, 0, 0, 0, now.Location())
		next = monthDay(next.Year(), next.Month())
		if end == nil || next.Before(*end) {
			end = &next
		}
	}
	if reset := trafficGroupLocalTimestamp(assignment.LastReset.Time, now.Location()); assignment.LastReset.Valid && reset.After(start) {
		start = reset
	}
	return start, end
}

// Official timestamps have no timezone. pgx scans them in UTC; interpret their
// wall-clock fields in the controller/official process timezone before comparing.
func trafficGroupLocalTimestamp(value time.Time, location *time.Location) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), location)
}

// The official per-node enforcer sums weighted upload AND download in both
// traffic modes, then subtracts its baseline. Missing/stale baselines use daily
// rows in the current cycle; Custom never creates or changes official baselines.
func packageNodeTrafficUsage(allTime, inCycle float64, baseline *float64, baselineUpdated, cycleStart time.Time) int64 {
	used := inCycle
	if baseline != nil && !trafficGroupLocalTimestamp(baselineUpdated, cycleStart.Location()).Before(cycleStart) {
		used = allTime - *baseline
	}
	if math.IsNaN(used) || used <= 0 {
		return 0
	}
	if used >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(used)
}

func trafficGroupShouldBlock(used, limit int64, nodeCount int, start time.Time, end *time.Time, now time.Time) bool {
	return nodeCount > 0 && limit > 0 && used >= limit && !now.Before(start) && (end == nil || now.Before(*end))
}

func (s *postgresAdminSessionStore) trafficGroupNodeUsage(ctx context.Context, assignment trafficGroupAssignment, start time.Time) (map[int64]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.node_id,SUM(t.weighted_uplink+t.weighted_downlink),
		SUM(CASE WHEN t.date >= $3 THEN t.weighted_uplink+t.weighted_downlink ELSE 0 END),
		b.baseline,b.updated_at
		FROM traffic_daily_user_nodes t
		LEFT JOIN package_user_node_traffic_baselines b ON b.username=t.username AND b.package_id=$2 AND b.node_id=t.node_id
		WHERE t.username=$1 GROUP BY t.node_id,b.baseline,b.updated_at`, assignment.Username, assignment.PackageID, start.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := map[int64]int64{}
	for rows.Next() {
		var id int64
		var allTime, inCycle float64
		var baseline *float64
		var updated *time.Time
		if err := rows.Scan(&id, &allTime, &inCycle, &baseline, &updated); err != nil {
			return nil, err
		}
		var updatedAt time.Time
		if updated != nil {
			updatedAt = *updated
		}
		usage[id] = packageNodeTrafficUsage(allTime, inCycle, baseline, updatedAt, start)
	}
	return usage, rows.Err()
}

func trafficGroupCapability(node trafficGroupNode, record serverDetailedConnectionRecord, now time.Time) trafficGroupNodeStatus {
	status := trafficGroupNodeStatus{NodeID: node.ID, NodeName: node.Name, ServerName: node.ServerName, Status: "enforced"}
	switch {
	case node.ServerID == 0:
		status.Status, status.Reason = "external_node", "外部节点无法强制执行共享组额度"
	case node.Mode != "external":
		status.Status, status.Reason = "embedded", "该节点为 Embedded 模式，共享组额度无法强制执行"
	case !helperSupportsTrafficBlocks(record.HelperVersion):
		status.Status, status.Reason = "helper_outdated", "需要 Helper v0.6.8 或更新版本"
	case now.Sub(record.UpdatedAt) > helperStaleTimeout:
		status.Status, status.Reason = "helper_outdated", "Helper 未连接或上报已过期，无法确认强制执行"
	case record.Snapshot.Core.Version < 7:
		status.Status, status.Reason = "core_outdated", "Core 接口版本过旧，需要 v7 或更新版本"
	case !record.Snapshot.Core.Available || !record.Snapshot.Core.TrafficBlockSupported:
		status.Status, status.Reason = "core_outdated", "Core 未就绪或尚未确认支持流量拦截"
	}
	return status
}

func (a *app) refreshTrafficGroupsLocked(ctx context.Context, store *postgresAdminSessionStore) error {
	nodes, err := store.trafficGroupNodes(ctx)
	if err != nil {
		return err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id FROM packages ORDER BY id`)
	if err != nil {
		return err
	}
	var packageIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		packageIDs = append(packageIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	a.connectionMu.Lock()
	records := make(map[string]serverDetailedConnectionRecord, len(a.detailedConnections))
	for id, record := range a.detailedConnections {
		records[id] = record
	}
	a.connectionMu.Unlock()
	now := time.Now()
	identityData := map[int64]trafficGroupIdentityData{}
	responses := map[int64]packageTrafficGroupUsageResponse{}
	desired := map[string]map[serverConnectionIdentity]bool{}
	blocks := []trafficGroupBlock{}
	for _, packageID := range packageIDs {
		pkg, err := store.trafficGroupPackage(ctx, packageID)
		if err != nil {
			return err
		}
		pkg.Groups = filterTrafficGroupNodes(pkg.Groups, pkg, nodes)
		response := packageTrafficGroupUsageResponse{Usage: []packageTrafficGroupUsage{}, Nodes: []trafficGroupNodeStatus{}}
		allowed := map[int64]bool{}
		for _, id := range pkg.NodeIDs {
			allowed[id] = true
		}
		for id, node := range nodes {
			if len(pkg.NodeIDs) == 0 || allowed[id] {
				response.Nodes = append(response.Nodes, trafficGroupCapability(node, records[strconv.FormatInt(node.ServerID, 10)], now))
			}
		}
		sort.Slice(response.Nodes, func(i, j int) bool { return response.Nodes[i].NodeID < response.Nodes[j].NodeID })
		if len(pkg.Groups) == 0 {
			responses[packageID] = response
			continue
		}
		assignments, err := store.trafficGroupAssignments(ctx, packageID)
		if err != nil {
			return err
		}
		for _, assignment := range assignments {
			start, end := trafficGroupCycle(assignment, now)
			nodeUsage, err := store.trafficGroupNodeUsage(ctx, assignment, start)
			if err != nil {
				return err
			}
			for _, group := range pkg.Groups {
				usage := packageTrafficGroupUsage{Username: assignment.Username, AssignmentID: assignment.ID, GroupID: group.ID, GroupName: group.Name, Limit: group.Limit, CycleStart: start, CycleEnd: end, Nodes: []trafficGroupNodeStatus{}}
				for _, id := range group.NodeIDs {
					if math.MaxInt64-usage.Used < nodeUsage[id] {
						usage.Used = math.MaxInt64
					} else {
						usage.Used += nodeUsage[id]
					}
				}
				overLimit := trafficGroupShouldBlock(usage.Used, usage.Limit, len(group.NodeIDs), start, end, now)
				if overLimit {
					blocks = append(blocks, trafficGroupBlock{AssignmentID: assignment.ID, GroupID: group.ID, PackageID: packageID, Username: assignment.Username, CycleStart: start, Used: usage.Used})
				}
				for _, id := range group.NodeIDs {
					node := nodes[id]
					serverID := strconv.FormatInt(node.ServerID, 10)
					status := trafficGroupCapability(node, records[serverID], now)
					if node.ServerID != 0 && node.Mode == "external" {
						if _, exists := identityData[node.ServerID]; !exists {
							data, err := store.trafficGroupIdentityData(ctx, node.ServerID)
							if err != nil {
								return err
							}
							identityData[node.ServerID] = data
						}
						identity, reason := resolveTrafficGroupIdentity(identityData[node.ServerID], assignment, node, group.NodeIDs, nodes)
						if reason != "" {
							if status.Status == "enforced" {
								status.Status, status.Reason = "no_identity", reason
							}
						} else if overLimit {
							if desired[serverID] == nil {
								desired[serverID] = map[serverConnectionIdentity]bool{}
							}
							desired[serverID][identity] = true
							usage.Blocked = usage.Blocked || status.Status == "enforced"
						}
					}
					usage.Nodes = append(usage.Nodes, status)
				}
				response.Usage = append(response.Usage, usage)
			}
		}
		responses[packageID] = response
	}
	added, removed, err := store.syncTrafficGroupBlocks(ctx, blocks)
	if err != nil {
		return err
	}
	// FK cascades may already have removed rows after unbinding or deleting a
	// group. Retain the previous successful evaluation for transition logging.
	if a.trafficGroupActive != nil {
		added, removed = trafficGroupBlockTransitions(a.trafficGroupActive, blocks)
	}
	for _, block := range removed {
		log.Printf("[mmwx-custom] traffic group unblock user=%s package=%d assignment=%d group=%d cycle=%s", block.Username, block.PackageID, block.AssignmentID, block.GroupID, block.CycleStart.Format(time.RFC3339))
	}
	for _, block := range added {
		log.Printf("[mmwx-custom] traffic group block user=%s package=%d assignment=%d group=%d used=%d cycle=%s", block.Username, block.PackageID, block.AssignmentID, block.GroupID, block.Used, block.CycleStart.Format(time.RFC3339))
	}
	serverBlocks := map[string][]serverConnectionIdentity{}
	for serverID, identities := range desired {
		for identity := range identities {
			serverBlocks[serverID] = append(serverBlocks[serverID], identity)
		}
		sort.Slice(serverBlocks[serverID], func(i, j int) bool {
			left, right := serverBlocks[serverID][i], serverBlocks[serverID][j]
			if left.InboundTag != right.InboundTag {
				return left.InboundTag < right.InboundTag
			}
			return left.User < right.User
		})
	}
	a.trafficGroupBlocks, a.trafficGroupUsage, a.trafficGroupsReady = serverBlocks, responses, true
	a.trafficGroupActive = blocks
	return nil
}

func trafficGroupBlockTransitions(previous, next []trafficGroupBlock) (added, removed []trafficGroupBlock) {
	old := map[[2]int64]trafficGroupBlock{}
	for _, block := range previous {
		old[[2]int64{block.AssignmentID, block.GroupID}] = block
	}
	for _, block := range next {
		key := [2]int64{block.AssignmentID, block.GroupID}
		before, exists := old[key]
		if !exists || !before.CycleStart.Equal(block.CycleStart) {
			added = append(added, block)
			if exists {
				removed = append(removed, before)
			}
		}
		delete(old, key)
	}
	for _, block := range old {
		removed = append(removed, block)
	}
	return added, removed
}
