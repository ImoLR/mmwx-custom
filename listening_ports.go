package main

import (
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// The Helper's machine listening ports (contract: inbound-port-check), kept
// with the latest detailed snapshot in memory only and served read-only to an
// authorised operator for the inbound editor's port check.

type serverListeningPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	// Address is the bound IP, or "*" for a wildcard bind ("" if unknown).
	Address    string `json:"address,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Process    string `json:"process,omitempty"`
	Owner      string `json:"owner,omitempty"`
	InboundTag string `json:"inbound_tag,omitempty"`
}

type serverListeningPorts struct {
	ScannedAt time.Time             `json:"scanned_at"`
	Ports     []serverListeningPort `json:"ports"`
	Truncated bool                  `json:"truncated,omitempty"`
	Error     string                `json:"error,omitempty"`
}

// sanitizeListeningPorts drops malformed entries instead of rejecting the
// whole Helper report, which also carries connection metrics and commands.
func sanitizeListeningPorts(report *serverListeningPorts) *serverListeningPorts {
	if report == nil {
		return nil
	}
	clean := &serverListeningPorts{ScannedAt: report.ScannedAt, Ports: []serverListeningPort{}, Truncated: report.Truncated, Error: listeningText(report.Error, 512)}
	for _, entry := range report.Ports {
		if len(clean.Ports) >= flvx.MaxBusyPorts {
			clean.Truncated = true
			break
		}
		if entry.Port < 1 || entry.Port > 65535 || entry.Protocol != "tcp" && entry.Protocol != "udp" || entry.PID < 0 {
			continue
		}
		if entry.Owner != "" && entry.Owner != "core" && entry.Owner != "agent" {
			entry.Owner = ""
		}
		if entry.Address != "*" {
			if address, err := netip.ParseAddr(entry.Address); err == nil {
				entry.Address = address.String()
			} else {
				entry.Address = ""
			}
		}
		entry.Process = listeningText(entry.Process, 64)
		entry.InboundTag = listeningText(entry.InboundTag, 256)
		clean.Ports = append(clean.Ports, entry)
	}
	return clean
}

func listeningText(value string, limit int) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	for len(value) > limit {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

func parseListeningPortsPath(value string) (string, bool) {
	const prefix, suffix = "/api/custom/servers/", "/listening-ports"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			return "", false
		}
	}
	return id, id != "0"
}

// listeningPortsHandler: GET only. available = a fresh Helper report exists;
// supported = that Helper reports listening ports (v0.7.14+).
func (a *app) listeningPortsHandler(w http.ResponseWriter, r *http.Request, serverID string) {
	if err := a.authorizeOperatorServerRequest(r, serverID); err != nil {
		writeOperatorAuthorizationError(w, err)
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	a.connectionMu.Lock()
	record, exists := a.detailedConnections[serverID]
	a.connectionMu.Unlock()
	available := exists && time.Since(record.UpdatedAt) <= helperStaleTimeout
	listening := record.Snapshot.Listening
	response := map[string]any{
		"success": true, "available": available, "supported": available && listening != nil,
		"helper_version": record.HelperVersion, "stale_timeout_seconds": int(helperStaleTimeout.Seconds()), "listening": listening,
	}
	if exists {
		response["updated_at"] = record.UpdatedAt
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}
