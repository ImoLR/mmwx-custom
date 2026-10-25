package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// The machine's listening ports with their owner process, reported so the
// Custom inbound editor can refuse a port another program already holds
// (contract: inbound-port-check). TCP LISTEN sockets are always listed. An
// unconnected UDP socket is listed when it is a kernel socket, the twin of a
// TCP listener, or bound outside the kernel's ephemeral range; anything else
// may be a short-lived outbound UDP socket. The four socket tables are read
// every report, but the owner walk over /proc/*/fd only runs when the set of
// listeners changed (at most every listeningMinGap) or listeningRefresh passed.
const (
	listeningRefresh    = 60 * time.Second
	listeningMinGap     = 5 * time.Second
	listeningOwnerCore  = "core"
	listeningOwnerAgent = "agent"
)

type listeningPortOwner struct {
	Port     uint32 `json:"port"`
	Protocol string `json:"protocol"`
	// PID 0 with no process is a kernel socket or an owner that exited.
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"`
	// Owner marks the official runtime: "core" for the Xray Core serving the
	// official config, "agent" for the official Agent; empty for anything else.
	Owner string `json:"owner,omitempty"`
	// InboundTag is the official config inbound on this port, for core/agent.
	InboundTag string `json:"inbound_tag,omitempty"`
}

type listeningPortsReport struct {
	ScannedAt time.Time            `json:"scanned_at"`
	Ports     []listeningPortOwner `json:"ports"`
	Truncated bool                 `json:"truncated,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type listeningRow struct {
	Inode    uint64
	Port     uint32
	Protocol string
}

type listeningPortsScanner struct {
	procRoot     string
	portRange    string
	configPath   string
	coreBinaries []string

	key       string
	scannedAt time.Time
	report    *listeningPortsReport
}

func (scanner *listeningPortsScanner) defaults() {
	if scanner.procRoot == "" {
		scanner.procRoot = "/proc"
	}
	if scanner.portRange == "" {
		scanner.portRange = ipLocalPortRangePath
	}
	if scanner.configPath == "" {
		scanner.configPath = officialConfigPath
	}
	if scanner.coreBinaries == nil {
		scanner.coreBinaries = []string{coreBinaryPath, officialXrayBinaryPath}
	}
}

// current returns the listening report, rescanning owners only when due.
func (scanner *listeningPortsScanner) current(now time.Time) *listeningPortsReport {
	scanner.defaults()
	rows, err := readListeningRows(scanner.procRoot, scanner.portRange)
	if err != nil {
		if scanner.report == nil || now.Sub(scanner.scannedAt) >= listeningRefresh {
			scanner.report = &listeningPortsReport{ScannedAt: now.UTC(), Ports: []listeningPortOwner{}, Error: err.Error()}
			scanner.scannedAt, scanner.key = now, ""
		}
		return scanner.report
	}
	key := listeningRowsKey(rows)
	age := now.Sub(scanner.scannedAt)
	due := scanner.report == nil || age >= listeningRefresh || age < 0 || key != scanner.key && age >= listeningMinGap
	if due {
		scanner.report = scanner.scan(rows, now)
		scanner.key, scanner.scannedAt = key, now
	}
	return scanner.report
}

func (scanner *listeningPortsScanner) scan(rows []listeningRow, now time.Time) *listeningPortsReport {
	wanted := map[uint64]struct{}{}
	for _, row := range rows {
		if row.Inode != 0 {
			wanted[row.Inode] = struct{}{}
		}
	}
	owners := socketOwnerPIDs(scanner.procRoot, wanted)
	processes := map[int]listeningPortOwner{}
	var tags map[int]string
	seen := map[string]struct{}{}
	report := &listeningPortsReport{ScannedAt: now.UTC(), Ports: []listeningPortOwner{}}
	for _, row := range rows {
		pids := owners[row.Inode]
		if len(pids) == 0 {
			pids = []int{0}
		}
		for _, pid := range pids {
			id := fmt.Sprint(row.Protocol, "/", row.Port, "/", pid)
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			entry, known := processes[pid]
			if !known && pid > 0 {
				entry = listeningPortOwner{PID: pid, Process: processComm(scanner.procRoot, pid)}
				exe := filepath.Join(scanner.procRoot, strconv.Itoa(pid), "exe")
				for _, binary := range scanner.coreBinaries {
					if sameFile(exe, binary) {
						entry.Owner = listeningOwnerCore
					}
				}
				if entry.Owner == "" && entry.Process == officialAgentComm {
					entry.Owner = listeningOwnerAgent
				}
				processes[pid] = entry
			}
			entry.Port, entry.Protocol = row.Port, row.Protocol
			if entry.Owner != "" {
				if tags == nil {
					tags = officialInboundTags(scanner.configPath)
				}
				entry.InboundTag = tags[int(row.Port)]
			}
			if len(report.Ports) >= flvx.MaxBusyPorts {
				report.Truncated = true
				continue
			}
			report.Ports = append(report.Ports, entry)
		}
	}
	sort.SliceStable(report.Ports, func(i, j int) bool {
		a, b := report.Ports[i], report.Ports[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		return a.PID < b.PID
	})
	return report
}

// readListeningRows reads the listeners of the four socket tables.
func readListeningRows(root, portRange string) ([]listeningRow, error) {
	var tcp, udp []listeningRow
	readable := 0
	for _, table := range []struct {
		name      string
		ipv6, udp bool
	}{{"tcp", false, false}, {"tcp6", true, false}, {"udp", false, true}, {"udp6", true, true}} {
		file, err := os.Open(filepath.Join(root, "net", table.name))
		if err != nil {
			continue
		}
		readable++
		scanner := bufio.NewScanner(file)
		for first := true; scanner.Scan(); first = false {
			fields := strings.Fields(scanner.Text())
			if first || len(fields) < 10 {
				continue
			}
			state := strings.ToUpper(fields[3])
			if table.udp {
				if _, remotePort, err := parseProcAddress(fields[2], table.ipv6); err != nil || state != tcpClose || remotePort != 0 {
					continue
				}
			} else if state != tcpListen {
				continue
			}
			_, port, err := parseProcAddress(fields[1], table.ipv6)
			inode, inodeErr := strconv.ParseUint(fields[9], 10, 64)
			if err != nil || inodeErr != nil || port == 0 {
				continue
			}
			row := listeningRow{Inode: inode, Port: port, Protocol: "tcp"}
			if table.udp {
				row.Protocol = "udp"
				udp = append(udp, row)
			} else {
				tcp = append(tcp, row)
			}
		}
		file.Close()
	}
	if readable == 0 {
		return nil, fmt.Errorf("no /proc/net socket tables are readable")
	}
	tcpPorts := map[uint32]bool{}
	for _, row := range tcp {
		tcpPorts[row.Port] = true
	}
	low, high, rangeKnown := readPortRange(portRange)
	rows := tcp
	for _, row := range udp {
		if row.Inode == 0 || tcpPorts[row.Port] || !rangeKnown || row.Port < low || row.Port > high {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func listeningRowsKey(rows []listeningRow) string {
	values := make([]string, len(rows))
	for index, row := range rows {
		values[index] = fmt.Sprint(row.Protocol, row.Port, ":", row.Inode)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

// socketOwnerPIDs maps each wanted socket inode to the processes holding it,
// stopping once every inode has an owner.
func socketOwnerPIDs(root string, wanted map[uint64]struct{}) map[uint64][]int {
	result := map[uint64][]int{}
	if len(wanted) == 0 {
		return result
	}
	handle, err := os.Open(root)
	if err != nil {
		return result
	}
	names, _ := handle.Readdirnames(-1)
	handle.Close()
	pids := make([]int, 0, len(names))
	for _, name := range names {
		if pid, err := strconv.Atoi(name); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	for _, pid := range pids {
		inodes := map[uint64]struct{}{}
		collectSocketInodes(filepath.Join(root, strconv.Itoa(pid), "fd"), inodes)
		for inode := range inodes {
			if _, ok := wanted[inode]; ok {
				result[inode] = append(result[inode], pid)
			}
		}
		if len(result) == len(wanted) {
			break
		}
	}
	return result
}

// officialInboundTags maps the official config's inbound ports to their tags.
func officialInboundTags(path string) map[int]string {
	tags := map[int]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return tags
	}
	var config struct {
		Inbounds []struct {
			Tag  string          `json:"tag"`
			Port json.RawMessage `json:"port"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(data, &config) != nil {
		return tags
	}
	for _, inbound := range config.Inbounds {
		port, err := strconv.Atoi(strings.Trim(strings.TrimSpace(string(inbound.Port)), `"`))
		if err == nil && port > 0 && port <= 65535 && tags[port] == "" {
			tags[port] = inbound.Tag
		}
	}
	return tags
}
