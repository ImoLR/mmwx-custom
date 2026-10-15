package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const journalCap = "[Journal]\nSystemMaxUse=128M\nMaxRetentionSec=7day\n"
const agentLogrotate = `/var/log/mmw-agent/*.log {
    size 20M
    rotate 2
    compress
    missingok
    notifempty
    copytruncate
}
`

type logHousekeepingStatus struct {
	JournaldCapped    bool                   `json:"journald_capped"`
	AgentLogrotate    bool                   `json:"agent_logrotate"`
	JournaldDiskUsage string                 `json:"journald_disk_usage,omitempty"`
	AgentLogBytes     int64                  `json:"agent_log_bytes"`
	CheckedAt         time.Time              `json:"checked_at"`
	UnitLines         map[string]int         `json:"unit_lines_10m,omitempty"`
	SampleComplete    bool                   `json:"sample_complete"`
	Before            *logHousekeepingStatus `json:"before,omitempty"`
}

type logHousekeeping struct {
	status logHousekeepingStatus
	before *logHousekeepingStatus
}

func journaldCapped(root string) (bool, error) {
	paths := []string{filepath.Join(root, "etc/systemd/journald.conf")}
	dropins, err := filepath.Glob(filepath.Join(root, "etc/systemd/journald.conf.d/*"))
	if err != nil {
		return false, err
	}
	paths = append(paths, dropins...)
	for _, path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.IsDir() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, value, found := strings.Cut(strings.TrimSpace(line), "=")
			if found && strings.TrimSpace(key) == "SystemMaxUse" && strings.TrimSpace(value) != "" {
				return true, nil
			}
		}
	}
	return false, nil
}

func installLogHousekeeping(ctx context.Context, root string, run systemctlRunner, report func(string)) {
	// Run once per Helper start. Never replace an administrator's configuration.
	capped, err := journaldCapped(root)
	if err != nil {
		report("journald cap check failed: " + err.Error())
	} else if !capped {
		path := filepath.Join(root, "etc/systemd/journald.conf.d/90-mmwxc-cap.conf")
		if err := createHousekeepingFile(path, journalCap); err != nil {
			report("journald cap: " + err.Error())
		} else if err := run(ctx, "restart", "systemd-journald.service"); err != nil {
			report("journald cap installed; restart failed: " + err.Error())
		} else {
			report("journald cap installed (128M, 7 days)")
		}
	}
	path := filepath.Join(root, "etc/logrotate.d/mmw-agent")
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return
	}
	if info, err := os.Stat(filepath.Join(root, "var/log/mmw-agent")); err != nil || !info.IsDir() {
		return
	}
	if err := createHousekeepingFile(path, agentLogrotate); err != nil {
		report("agent logrotate: " + err.Error())
	} else {
		report("agent logrotate installed (20M, 2 rotations)")
	}
}

func createHousekeepingFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if err := file.Chmod(0644); err != nil {
		_ = file.Close()
		return err
	}
	_, writeErr := file.WriteString(contents)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (h *logHousekeeping) snapshot(ctx context.Context) *logHousekeepingStatus {
	if h == nil {
		return nil
	}
	if time.Since(h.status.CheckedAt) < time.Minute {
		return &h.status
	}
	status := logHousekeepingStatus{AgentLogBytes: -1, CheckedAt: time.Now().UTC(), Before: h.before}
	status.JournaldCapped, _ = journaldCapped("/")
	_, err := os.Stat("/etc/logrotate.d/mmw-agent")
	status.AgentLogrotate = err == nil
	if info, err := os.Stat("/var/log/mmw-agent/mmw-agent.log"); err == nil {
		status.AgentLogBytes = info.Size()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "journalctl", "--disk-usage").Output(); err == nil {
		status.JournaldDiskUsage = strings.TrimSpace(string(output))
	}
	command := exec.CommandContext(ctx, "journalctl", "--since", "10 minutes ago", "--no-pager", "-o", "json", "--output-fields=_SYSTEMD_UNIT")
	if output, err := command.StdoutPipe(); err == nil {
		if err := command.Start(); err == nil {
			scanner := bufio.NewScanner(output)
			scanner.Buffer(make([]byte, 4096), 16384)
			status.UnitLines, status.SampleComplete = journalUnitCounts(scanner, 50000)
			if !status.SampleComplete {
				_ = command.Process.Kill()
			}
			status.SampleComplete = command.Wait() == nil && status.SampleComplete
			status.UnitLines = topJournalUnits(status.UnitLines)
		}
	}
	h.status = status
	return &h.status
}

func topJournalUnits(counts map[string]int) map[string]int {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	result := map[string]int{}
	for _, key := range []string{"mmwxc-helper.service", "mmw-agent.service", "xray.service", "systemd-journald.service"} {
		result[key] = counts[key]
	}
	for i, key := range keys {
		if i == 12 {
			break
		}
		result[key] = counts[key]
	}
	return result
}

func journalUnitCounts(scanner *bufio.Scanner, limit int) (map[string]int, bool) {
	counts := map[string]int{}
	for lines := 0; scanner.Scan(); lines++ {
		if lines >= limit {
			return counts, false
		}
		var entry struct {
			Unit string `json:"_SYSTEMD_UNIT"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			return counts, false
		}
		if entry.Unit == "" {
			entry.Unit = "(unknown)"
		}
		counts[entry.Unit]++
	}
	return counts, scanner.Err() == nil
}
