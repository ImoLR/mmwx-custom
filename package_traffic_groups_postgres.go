package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const packageTrafficGroupsSchema = `
CREATE TABLE IF NOT EXISTS mmwxc_package_traffic_groups (
    id BIGSERIAL PRIMARY KEY,
    package_id BIGINT NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    limit_bytes BIGINT NOT NULL CHECK (limit_bytes > 0),
    node_ids JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_mmwxc_package_traffic_groups_package
    ON mmwxc_package_traffic_groups(package_id);
CREATE TABLE IF NOT EXISTS mmwxc_package_traffic_group_blocks (
    username TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
    package_id BIGINT NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    assignment_id BIGINT NOT NULL REFERENCES user_package_assignments(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES mmwxc_package_traffic_groups(id) ON DELETE CASCADE,
    cycle_start TIMESTAMPTZ NOT NULL,
    used_bytes BIGINT NOT NULL,
    blocked_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (assignment_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_mmwxc_package_traffic_group_blocks_username
    ON mmwxc_package_traffic_group_blocks(username);
`

func (s *postgresAdminSessionStore) EnsurePackageTrafficGroupsSchema(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(ctx, packageTrafficGroupsSchema)
	if err != nil {
		return fmt.Errorf("migrate package traffic groups: %w", err)
	}
	return nil
}

func (s *postgresAdminSessionStore) trafficGroupPackage(ctx context.Context, id int64) (trafficGroupPackage, error) {
	var pkg trafficGroupPackage
	var nodes, limits string
	err := s.db.QueryRowContext(ctx, `SELECT id,traffic_limit_bytes,COALESCE(nodes,'[]'),COALESCE(node_traffic_limits,'{}'),traffic_mode FROM packages WHERE id=$1`, id).
		Scan(&pkg.ID, &pkg.Limit, &nodes, &limits, &pkg.TrafficMode)
	if err != nil {
		return pkg, err
	}
	if err := json.Unmarshal([]byte(nodes), &pkg.NodeIDs); err != nil {
		return pkg, fmt.Errorf("package nodes: %w", err)
	}
	if err := json.Unmarshal([]byte(limits), &pkg.NodeLimits); err != nil {
		return pkg, fmt.Errorf("package node limits: %w", err)
	}
	pkg.Groups = []packageTrafficGroup{}
	rows, err := s.db.QueryContext(ctx, `SELECT id,package_id,name,limit_bytes,node_ids,created_at,updated_at FROM mmwxc_package_traffic_groups WHERE package_id=$1 ORDER BY id`, id)
	if err != nil {
		return pkg, err
	}
	defer rows.Close()
	for rows.Next() {
		group, err := scanTrafficGroup(rows)
		if err != nil {
			return pkg, err
		}
		pkg.Groups = append(pkg.Groups, group)
	}
	return pkg, rows.Err()
}

func scanTrafficGroup(row interface{ Scan(...any) error }) (packageTrafficGroup, error) {
	var group packageTrafficGroup
	var raw []byte
	if err := row.Scan(&group.ID, &group.PackageID, &group.Name, &group.Limit, &raw, &group.CreatedAt, &group.UpdatedAt); err != nil {
		return group, err
	}
	err := json.Unmarshal(raw, &group.NodeIDs)
	return group, err
}

func (s *postgresAdminSessionStore) replaceTrafficGroups(ctx context.Context, id int64, groups []packageTrafficGroup) ([]packageTrafficGroup, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var pkg trafficGroupPackage
	var nodeJSON, limitsJSON string
	if err := tx.QueryRowContext(ctx, `SELECT id,traffic_limit_bytes,COALESCE(nodes,'[]'),COALESCE(node_traffic_limits,'{}') FROM packages WHERE id=$1 FOR UPDATE`, id).
		Scan(&pkg.ID, &pkg.Limit, &nodeJSON, &limitsJSON); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(nodeJSON), &pkg.NodeIDs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(limitsJSON), &pkg.NodeLimits); err != nil {
		return nil, err
	}
	nodeRows, err := tx.QueryContext(ctx, `SELECT id FROM nodes`)
	if err != nil {
		return nil, err
	}
	nodes := map[int64]trafficGroupNode{}
	for nodeRows.Next() {
		var nodeID int64
		if err := nodeRows.Scan(&nodeID); err != nil {
			nodeRows.Close()
			return nil, err
		}
		nodes[nodeID] = trafficGroupNode{ID: nodeID}
	}
	if err := nodeRows.Err(); err != nil {
		nodeRows.Close()
		return nil, err
	}
	nodeRows.Close()
	if err := validatePackageTrafficGroups(groups, pkg, nodes); err != nil {
		return nil, err
	}
	result := make([]packageTrafficGroup, 0, len(groups))
	ids := make([]int64, 0, len(groups))
	for _, group := range groups {
		group.Name = strings.TrimSpace(group.Name)
		raw, _ := json.Marshal(group.NodeIDs)
		var row *sql.Row
		if group.ID == 0 {
			row = tx.QueryRowContext(ctx, `INSERT INTO mmwxc_package_traffic_groups(package_id,name,limit_bytes,node_ids) VALUES($1,$2,$3,$4) RETURNING id,package_id,name,limit_bytes,node_ids,created_at,updated_at`, id, group.Name, group.Limit, raw)
		} else {
			row = tx.QueryRowContext(ctx, `UPDATE mmwxc_package_traffic_groups SET name=$3,limit_bytes=$4,node_ids=$5,updated_at=CURRENT_TIMESTAMP WHERE package_id=$1 AND id=$2 RETURNING id,package_id,name,limit_bytes,node_ids,created_at,updated_at`, id, group.ID, group.Name, group.Limit, raw)
		}
		saved, err := scanTrafficGroup(row)
		if err == sql.ErrNoRows {
			return nil, trafficGroupValidationError{"共享组编号不属于此套餐，请刷新后重试"}
		}
		if err != nil {
			return nil, err
		}
		result = append(result, saved)
		ids = append(ids, saved.ID)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mmwxc_package_traffic_groups WHERE package_id=$1 AND NOT(id=ANY($2::bigint[]))`, id, ids); err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

type trafficGroupNode struct {
	ID         int64
	Name       string
	ServerID   int64
	ServerName string
	Mode       string
	Tag        string
	Owner      string
	Routed     bool
}

func (s *postgresAdminSessionStore) trafficGroupNodes(ctx context.Context) (map[int64]trafficGroupNode, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n.id,n.node_name,COALESCE(s.id,0),COALESCE(n.original_server,''),COALESCE(s.xray_mode,''),COALESCE(n.inbound_tag,''),n.username,COALESCE(n.node_type,'physical')='routed' FROM nodes n LEFT JOIN remote_servers s ON s.name=n.original_server`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := map[int64]trafficGroupNode{}
	for rows.Next() {
		var node trafficGroupNode
		if err := rows.Scan(&node.ID, &node.Name, &node.ServerID, &node.ServerName, &node.Mode, &node.Tag, &node.Owner, &node.Routed); err != nil {
			return nil, err
		}
		nodes[node.ID] = node
	}
	return nodes, rows.Err()
}

func (s *postgresAdminSessionStore) trafficGroupAssignments(ctx context.Context, packageID int64) ([]trafficGroupAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.package_id,a.username,COALESCE(a.package_start_date,a.created_at),a.package_end_date,a.last_reset_at,a.is_reset<>0,a.reset_day,a.traffic_limit_override FROM user_package_assignments a JOIN users u ON u.username=a.username WHERE a.package_id=$1 AND a.status='active' ORDER BY a.username,a.id`, packageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []trafficGroupAssignment{}
	for rows.Next() {
		var assignment trafficGroupAssignment
		if err := rows.Scan(&assignment.ID, &assignment.PackageID, &assignment.Username, &assignment.Start, &assignment.End, &assignment.LastReset, &assignment.MonthlyReset, &assignment.ResetDay, &assignment.LimitOverride); err != nil {
			return nil, err
		}
		result = append(result, assignment)
	}
	return result, rows.Err()
}

type trafficGroupBlock struct {
	AssignmentID int64
	GroupID      int64
	PackageID    int64
	Username     string
	CycleStart   time.Time
	Used         int64
}

func (s *postgresAdminSessionStore) syncTrafficGroupBlocks(ctx context.Context, blocks []trafficGroupBlock) ([]trafficGroupBlock, []trafficGroupBlock, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT assignment_id,group_id,package_id,username,cycle_start,used_bytes FROM mmwxc_package_traffic_group_blocks`)
	if err != nil {
		return nil, nil, err
	}
	previous := map[[2]int64]trafficGroupBlock{}
	for rows.Next() {
		var block trafficGroupBlock
		if err := rows.Scan(&block.AssignmentID, &block.GroupID, &block.PackageID, &block.Username, &block.CycleStart, &block.Used); err != nil {
			rows.Close()
			return nil, nil, err
		}
		previous[[2]int64{block.AssignmentID, block.GroupID}] = block
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	var added, removed []trafficGroupBlock
	for _, block := range blocks {
		key := [2]int64{block.AssignmentID, block.GroupID}
		old, exists := previous[key]
		if !exists || !old.CycleStart.Equal(block.CycleStart) {
			if exists {
				removed = append(removed, old)
			}
			added = append(added, block)
			_, err := tx.ExecContext(ctx, `INSERT INTO mmwxc_package_traffic_group_blocks(username,package_id,assignment_id,group_id,cycle_start,used_bytes) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(assignment_id,group_id) DO UPDATE SET cycle_start=EXCLUDED.cycle_start,used_bytes=EXCLUDED.used_bytes,blocked_at=CURRENT_TIMESTAMP`, block.Username, block.PackageID, block.AssignmentID, block.GroupID, block.CycleStart, block.Used)
			if err != nil {
				return nil, nil, err
			}
		}
		delete(previous, key)
	}
	for _, block := range previous {
		if _, err := tx.ExecContext(ctx, `DELETE FROM mmwxc_package_traffic_group_blocks WHERE assignment_id=$1 AND group_id=$2`, block.AssignmentID, block.GroupID); err != nil {
			return nil, nil, err
		}
		removed = append(removed, block)
	}
	return added, removed, tx.Commit()
}
