package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Machine protection port source 4, the official Embedded inbounds. In
// Embedded mode the official Agent serves the node inbounds itself, so no mmwx
// Core lists them. The Helper reads what the Agent process really listens on:
// its own non-loopback TCP listeners and unconnected UDP sockets, minus its
// management port. Custom's node rows cannot replace this: they hold the
// client-facing port (relay copies and NAT entries differ from the local
// listener) and miss inbounds that have no node row.
const (
	officialAgentComm = "mmw-agent"
	// The official Agent's own API port when config.yaml does not name one.
	defaultOfficialAgentPort = 23889
	// A rescan (Agent process, its fds, the four socket tables) runs at most
	// every officialPortsRefresh, or earlier when the machine's TCP listeners
	// change (a node was added or removed), but never twice within
	// officialPortsMinGap.
	officialPortsRefresh = 30 * time.Second
	officialPortsMinGap  = 5 * time.Second
	ipLocalPortRangePath = "/proc/sys/net/ipv4/ip_local_port_range"
)

// officialAgentView is the per-tick, scan-free view of the official Agent.
// Only Embedded counts: on External the inbounds belong to the mmwx Core.
type officialAgentView struct {
	Embedded       bool
	ManagementPort uint32
	// TCPListen keys this tick's non-loopback TCP LISTEN ports of any owner.
	TCPListen string
}

func officialAgentInput(configPath string, externalOwned bool, sockets []socketEntry) officialAgentView {
	if externalOwned {
		return officialAgentView{}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return officialAgentView{}
	}
	if mode, _, err := replaceAgentXrayMode(data, ""); err != nil || mode != "embedded" {
		return officialAgentView{}
	}
	return officialAgentView{Embedded: true, ManagementPort: officialAgentListenPort(data), TCPListen: tcpListenKey(sockets)}
}

func officialAgentListenPort(data []byte) uint32 {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "listen_port:") {
			continue
		}
		value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "listen_port:")), `"'`)
		if port, err := strconv.ParseUint(value, 10, 16); err == nil && port > 0 {
			return uint32(port)
		}
	}
	return defaultOfficialAgentPort
}

func tcpListenKey(sockets []socketEntry) string {
	var ports []uint32
	for _, socket := range sockets {
		if socket.State == tcpListen && !socket.LocalIP.IsLoopback() {
			ports = append(ports, socket.LocalPort)
		}
	}
	ports = normalizeMachinePorts(ports)
	values := make([]string, len(ports))
	for index, port := range ports {
		values[index] = strconv.FormatUint(uint64(port), 10)
	}
	return strings.Join(values, ",")
}

type officialPortsScanner struct {
	procRoot  string
	portRange string
	comm      string

	active    bool
	scannedAt time.Time
	listenKey string
	mgmt      uint32
	pids      []int
	ports     machinePorts
	err       string
}

func newOfficialPortsScanner() officialPortsScanner {
	return officialPortsScanner{procRoot: "/proc", portRange: ipLocalPortRangePath, comm: officialAgentComm}
}

// current returns the cached official ports, rescanning only when due.
func (scanner *officialPortsScanner) current(view officialAgentView, now time.Time) (machinePorts, string) {
	if !view.Embedded {
		scanner.active, scanner.pids, scanner.ports, scanner.err = false, nil, machinePorts{}, ""
		return machinePorts{}, ""
	}
	age := now.Sub(scanner.scannedAt)
	due := !scanner.active || age >= officialPortsRefresh || age < 0 || view.ManagementPort != scanner.mgmt ||
		view.TCPListen != scanner.listenKey && age >= officialPortsMinGap
	if due {
		scanner.active, scanner.scannedAt, scanner.listenKey, scanner.mgmt = true, now, view.TCPListen, view.ManagementPort
		ports, err := scanner.scan(view.ManagementPort)
		scanner.ports, scanner.err = ports, ""
		if err != nil {
			scanner.err = err.Error()
		}
	}
	return scanner.ports, scanner.err
}

func (scanner *officialPortsScanner) scan(management uint32) (machinePorts, error) {
	root := scanner.procRoot
	if root == "" {
		root = "/proc"
	}
	pids, err := scanner.agentPIDs(root)
	if err != nil {
		return machinePorts{}, err
	}
	inodes := map[uint64]struct{}{}
	for _, pid := range pids {
		collectSocketInodes(filepath.Join(root, strconv.Itoa(pid), "fd"), inodes)
	}
	if len(inodes) == 0 {
		return machinePorts{}, nil
	}
	var tcp, udp []uint32
	for _, table := range []struct {
		name      string
		ipv6, udp bool
	}{{"tcp", false, false}, {"tcp6", true, false}, {"udp", false, true}, {"udp6", true, true}} {
		ports := agentSocketPorts(filepath.Join(root, "net", table.name), table.ipv6, table.udp, inodes)
		if table.udp {
			udp = append(udp, ports...)
		} else {
			tcp = append(tcp, ports...)
		}
	}
	result := machinePorts{}
	for _, port := range normalizeMachinePorts(tcp) {
		if port != management {
			result.TCP = append(result.TCP, port)
		}
	}
	// An Agent UDP socket is an inbound when its port is also an Agent TCP
	// listener or was bound outside the kernel's ephemeral range; anything else
	// may be an outbound relay socket, which is never guarded.
	low, high, rangeKnown := readPortRange(scanner.portRange)
	for _, port := range normalizeMachinePorts(udp) {
		if port != management && (containsMachinePort(result.TCP, port) || rangeKnown && (port < low || port > high)) {
			result.UDP = append(result.UDP, port)
		}
	}
	return result.normalized(), nil
}

// agentPIDs keeps the last found Agent PIDs while they still run the Agent;
// otherwise it walks /proc once.
func (scanner *officialPortsScanner) agentPIDs(root string) ([]int, error) {
	comm := scanner.comm
	if comm == "" {
		comm = officialAgentComm
	}
	alive := len(scanner.pids) > 0
	for _, pid := range scanner.pids {
		alive = alive && processComm(root, pid) == comm
	}
	if alive {
		return scanner.pids, nil
	}
	scanner.pids = nil
	handle, err := os.Open(root)
	if err != nil {
		return nil, err
	}
	names, err := handle.Readdirnames(-1)
	handle.Close()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if pid, err := strconv.Atoi(name); err == nil && pid > 0 && processComm(root, pid) == comm {
			scanner.pids = append(scanner.pids, pid)
		}
	}
	if len(scanner.pids) == 0 {
		return nil, errors.New("official Agent process not found")
	}
	sort.Ints(scanner.pids)
	return scanner.pids, nil
}

func processComm(root string, pid int) string {
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func collectSocketInodes(fdDir string, inodes map[uint64]struct{}) {
	handle, err := os.Open(fdDir)
	if err != nil {
		return
	}
	names, _ := handle.Readdirnames(-1)
	handle.Close()
	for _, name := range names {
		link, err := os.Readlink(filepath.Join(fdDir, name))
		if err != nil || !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
			continue
		}
		if inode, err := strconv.ParseUint(link[len("socket:["):len(link)-1], 10, 64); err == nil && inode > 0 {
			inodes[inode] = struct{}{}
		}
	}
}

// agentSocketPorts returns the non-loopback local ports of the given sockets
// that accept new peers: TCP LISTEN, or UDP without a connected peer.
func agentSocketPorts(path string, ipv6, udp bool, inodes map[uint64]struct{}) []uint32 {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var ports []uint32
	scanner := bufio.NewScanner(file)
	for first := true; scanner.Scan(); first = false {
		fields := strings.Fields(scanner.Text())
		if first || len(fields) < 10 {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		if _, ours := inodes[inode]; !ours {
			continue
		}
		state := strings.ToUpper(fields[3])
		if udp {
			if _, remotePort, err := parseProcAddress(fields[2], ipv6); err != nil || state != tcpClose || remotePort != 0 {
				continue
			}
		} else if state != tcpListen {
			continue
		}
		local, port, err := parseProcAddress(fields[1], ipv6)
		if err != nil || local.IsLoopback() {
			continue
		}
		ports = append(ports, port)
	}
	return ports
}

func readPortRange(path string) (uint32, uint32, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return 0, 0, false
	}
	low, errLow := strconv.ParseUint(fields[0], 10, 16)
	high, errHigh := strconv.ParseUint(fields[1], 10, 16)
	if errLow != nil || errHigh != nil || low > high {
		return 0, 0, false
	}
	return uint32(low), uint32(high), true
}

func containsMachinePort(ports []uint32, wanted uint32) bool {
	index := sort.Search(len(ports), func(i int) bool { return ports[i] >= wanted })
	return index < len(ports) && ports[index] == wanted
}
