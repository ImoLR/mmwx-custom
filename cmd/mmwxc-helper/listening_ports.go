package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
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
// may be a short-lived outbound UDP socket. Each report reuses the TCP rows the
// tick already read for the machine counts; the four socket tables and the
// owner walk over /proc/*/fd only run again when the TCP listeners changed (at
// most every listeningMinGap) or listeningRefresh passed.
const (
	listeningRefresh    = 60 * time.Second
	listeningMinGap     = 5 * time.Second
	listeningOwnerCore  = "core"
	listeningOwnerAgent = "agent"
	// listeningAnyAddress is the wire form of a wildcard bind (0.0.0.0 or ::).
	listeningAnyAddress = "*"
)

type listeningPortOwner struct {
	Port     uint32 `json:"port"`
	Protocol string `json:"protocol"`
	// Address is the bound local IP, or "*" for a wildcard bind.
	Address string `json:"address"`
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

// listenerSocket is a TCP LISTEN or bound, unconnected UDP socket.
type listenerSocket struct {
	Protocol string
	Address  netip.Addr
	Port     uint32
	Inode    uint64
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

// current returns the listening report, rescanning only when due. tcp are this
// tick's parsed TCP rows (nil when unusable: then only the refresh applies).
func (scanner *listeningPortsScanner) current(tcp []socketEntry, now time.Time) *listeningPortsReport {
	scanner.defaults()
	key := scanner.key
	if tcp != nil {
		key = tcpListenersKey(tcp)
	}
	age := now.Sub(scanner.scannedAt)
	due := scanner.report == nil || age >= listeningRefresh || age < 0 || key != scanner.key && age >= listeningMinGap
	if !due {
		return scanner.report
	}
	scanner.key, scanner.scannedAt = key, now
	sockets, err := readListenerSockets(scanner.procRoot)
	if err != nil {
		scanner.report = &listeningPortsReport{ScannedAt: now.UTC(), Ports: []listeningPortOwner{}, Error: err.Error()}
		return scanner.report
	}
	scanner.report = scanner.scan(listeningRows(sockets, scanner.portRange), now)
	return scanner.report
}

// tcpListenersKey identifies the machine's TCP listeners, loopback included.
func tcpListenersKey(sockets []socketEntry) string {
	var values []string
	for _, socket := range sockets {
		if socket.State == tcpListen {
			values = append(values, socket.LocalIP.String()+"/"+strconv.FormatUint(uint64(socket.LocalPort), 10))
		}
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

// listeningRows keeps every TCP listener and the UDP sockets that look like a
// service (see the file comment).
func listeningRows(sockets []listenerSocket, portRange string) []listenerSocket {
	tcpPorts := map[uint32]bool{}
	for _, socket := range sockets {
		if socket.Protocol == "tcp" {
			tcpPorts[socket.Port] = true
		}
	}
	low, high, rangeKnown := readPortRange(portRange)
	rows := make([]listenerSocket, 0, len(sockets))
	for _, socket := range sockets {
		if socket.Protocol == "tcp" || socket.Inode == 0 || tcpPorts[socket.Port] || !rangeKnown || socket.Port < low || socket.Port > high {
			rows = append(rows, socket)
		}
	}
	return rows
}

func (scanner *listeningPortsScanner) scan(rows []listenerSocket, now time.Time) *listeningPortsReport {
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
		address := listeningAnyAddress
		if !row.Address.IsUnspecified() {
			address = row.Address.String()
		}
		pids := owners[row.Inode]
		if len(pids) == 0 {
			pids = []int{0}
		}
		for _, pid := range pids {
			// tcp and tcp6 wildcard binds of one process are one entry.
			id := fmt.Sprint(row.Protocol, "/", row.Port, "/", pid, "/", address)
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			entry, known := processes[pid]
			if !known && pid > 0 {
				entry = listeningPortOwner{PID: pid, Process: processComm(scanner.procRoot, pid)}
				exe := filepath.Join(scanner.procRoot, strconv.Itoa(pid), "exe")
				for _, binary := range scanner.coreBinaries {
					if runsBinary(exe, binary) {
						entry.Owner = listeningOwnerCore
					}
				}
				if entry.Owner == "" && entry.Process == officialAgentComm {
					entry.Owner = listeningOwnerAgent
				}
				processes[pid] = entry
			}
			entry.Port, entry.Protocol, entry.Address = row.Port, row.Protocol, address
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
		if a.PID != b.PID {
			return a.PID < b.PID
		}
		return a.Address < b.Address
	})
	return report
}

// runsBinary reports whether the process behind exe runs binary, also when
// the file was replaced on disk since (exe then reads "<path> (deleted)").
func runsBinary(exe, binary string) bool {
	if sameFile(exe, binary) {
		return true
	}
	link, err := os.Readlink(exe)
	return err == nil && filepath.Clean(strings.TrimSuffix(link, " (deleted)")) == filepath.Clean(binary)
}

// readListenerSockets returns every TCP LISTEN socket and every bound,
// unconnected UDP socket (state 07, no peer) of the four socket tables under
// root, of any owner and address, loopback included.
func readListenerSockets(root string) ([]listenerSocket, error) {
	var sockets []listenerSocket
	readable := 0
	for _, table := range []struct {
		name      string
		ipv6, udp bool
	}{{"tcp", false, false}, {"tcp6", true, false}, {"udp", false, true}, {"udp6", true, true}} {
		path := filepath.Join(root, "net", table.name)
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		rows, err := parseListenerSockets(file, table.ipv6, table.udp)
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		readable++
		sockets = append(sockets, rows...)
	}
	if readable == 0 {
		return nil, fmt.Errorf("no /proc/net socket tables are readable")
	}
	return sockets, nil
}

func parseListenerSockets(reader io.Reader, ipv6, udp bool) ([]listenerSocket, error) {
	protocol, state := "tcp", tcpListen
	if udp {
		protocol, state = "udp", tcpClose
	}
	var sockets []listenerSocket
	scanner := bufio.NewScanner(reader)
	for first := true; scanner.Scan(); first = false {
		fields := strings.Fields(scanner.Text())
		if first || len(fields) < 4 || strings.ToUpper(fields[3]) != state {
			continue
		}
		local, port, err := parseProcAddress(fields[1], ipv6)
		if err != nil {
			return nil, err
		}
		remote, _, err := parseProcAddress(fields[2], ipv6)
		if err != nil {
			return nil, err
		}
		if udp && remote.IsValid() && !remote.IsUnspecified() {
			continue
		}
		var inode uint64
		if len(fields) > 9 {
			if inode, err = strconv.ParseUint(fields[9], 10, 64); err != nil {
				return nil, fmt.Errorf("invalid socket inode %q", fields[9])
			}
		}
		sockets = append(sockets, listenerSocket{Protocol: protocol, Address: local, Port: port, Inode: inode})
	}
	return sockets, scanner.Err()
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
