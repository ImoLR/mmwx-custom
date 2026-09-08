package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	tcpEstablished = "01"
	tcpSynSent     = "02"
	tcpSynRecv     = "03"
	tcpTimeWait    = "06"
	tcpCloseWait   = "08"
)

type socketEntry struct {
	LocalIP   netip.Addr
	LocalPort uint32
	RemoteIP  netip.Addr
	State     string
}

func readTCPSockets() ([]socketEntry, error) {
	var entries []socketEntry
	readable := 0
	for _, source := range []struct {
		path string
		ipv6 bool
	}{{"/proc/net/tcp", false}, {"/proc/net/tcp6", true}} {
		file, err := os.Open(source.path)
		if err != nil {
			continue
		}
		parsed, parseErr := parseProcNetTCP(file, source.ipv6)
		_ = file.Close()
		if parseErr != nil {
			return nil, fmt.Errorf("parse %s: %w", source.path, parseErr)
		}
		readable++
		entries = append(entries, parsed...)
	}
	if readable == 0 {
		return nil, fmt.Errorf("no /proc/net TCP tables are readable")
	}
	return entries, nil
}

func readConnections() (connectionSnapshot, error) {
	tcp, okTCP := countProcNetRows("/proc/net/tcp")
	tcp6, okTCP6 := countProcNetRows("/proc/net/tcp6")
	udp, okUDP := countProcNetRows("/proc/net/udp")
	udp6, okUDP6 := countProcNetRows("/proc/net/udp6")
	if !okTCP && !okTCP6 && !okUDP && !okUDP6 {
		return connectionSnapshot{}, fmt.Errorf("no /proc/net socket tables are readable")
	}
	tcpCount := tcp + tcp6
	udpCount := udp + udp6
	return connectionSnapshot{TCPCount: tcpCount, UDPCount: udpCount, ConnectionCount: tcpCount + udpCount, SampledAt: time.Now().UTC()}, nil
}

func countProcNetRows(path string) (int64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	var lines int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines++
	}
	if scanner.Err() != nil {
		return 0, false
	}
	if lines == 0 {
		return 0, true
	}
	return lines - 1, true
}

func parseProcNetTCP(reader io.Reader, ipv6 bool) ([]socketEntry, error) {
	scanner := bufio.NewScanner(reader)
	first := true
	var entries []socketEntry
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		localIP, localPort, err := parseProcAddress(fields[1], ipv6)
		if err != nil {
			return nil, err
		}
		remoteIP, _, err := parseProcAddress(fields[2], ipv6)
		if err != nil {
			return nil, err
		}
		entries = append(entries, socketEntry{
			LocalIP:   localIP,
			LocalPort: localPort,
			RemoteIP:  remoteIP,
			State:     strings.ToUpper(fields[3]),
		})
	}
	return entries, scanner.Err()
}

func parseProcAddress(value string, ipv6 bool) (netip.Addr, uint32, error) {
	rawAddress, rawPort, ok := strings.Cut(value, ":")
	if !ok {
		return netip.Addr{}, 0, fmt.Errorf("invalid socket address %q", value)
	}
	port, err := strconv.ParseUint(rawPort, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid socket port %q", rawPort)
	}
	bytes, err := hex.DecodeString(rawAddress)
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("invalid socket IP %q", rawAddress)
	}
	if ipv6 {
		if len(bytes) != 16 {
			return netip.Addr{}, 0, fmt.Errorf("invalid IPv6 socket address %q", rawAddress)
		}
		for offset := 0; offset < len(bytes); offset += 4 {
			reverse(bytes[offset : offset+4])
		}
		var value [16]byte
		copy(value[:], bytes)
		return netip.AddrFrom16(value).Unmap(), uint32(port), nil
	}
	if len(bytes) != 4 {
		return netip.Addr{}, 0, fmt.Errorf("invalid IPv4 socket address %q", rawAddress)
	}
	reverse(bytes)
	var value4 [4]byte
	copy(value4[:], bytes)
	return netip.AddrFrom4(value4), uint32(port), nil
}

func reverse(value []byte) {
	for left, right := 0, len(value)-1; left < right; left, right = left+1, right-1 {
		value[left], value[right] = value[right], value[left]
	}
}

func normalizeOnlineIP(address netip.Addr) string {
	if !address.IsValid() || address.IsUnspecified() {
		return ""
	}
	address = address.Unmap()
	if address.Is6() {
		// Count a client IPv6 /64 as one online source so privacy addresses do
		// not multiply slots while still separating normal delegated prefixes.
		return netip.PrefixFrom(address, 64).Masked().String()
	}
	return address.String()
}

func summarizeTCP(entries []socketEntry) tcpStateCounts {
	counts := tcpStateCounts{Total: int64(len(entries))}
	for _, entry := range entries {
		switch entry.State {
		case tcpEstablished:
			counts.Established++
		case tcpTimeWait:
			counts.TimeWait++
		case tcpCloseWait:
			counts.CloseWait++
		case tcpSynSent:
			counts.SynSent++
		case tcpSynRecv:
			counts.SynRecv++
		}
	}
	return counts
}
