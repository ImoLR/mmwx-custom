package main

import (
	"sort"
	"time"
)

type onlineIPTracker struct {
	lastSeen map[uint32]map[string]time.Time
	now      func() time.Time
}

func newOnlineIPTracker() *onlineIPTracker {
	return &onlineIPTracker{lastSeen: make(map[uint32]map[string]time.Time), now: time.Now}
}

func (tracker *onlineIPTracker) aggregate(entries []socketEntry, core coreSnapshotResponse, settings connectionSettings) ([]inboundSnapshot, []proxyUserSnapshot) {
	now := tracker.now()
	grace := time.Duration(settings.OnlineIPGracePeriodSeconds) * time.Second
	if grace <= 0 {
		grace = 30 * time.Second
	}
	settingsByIdentity := make(map[coreIdentity]userConnectionSettings, len(settings.Users))
	for _, item := range settings.Users {
		settingsByIdentity[item.Identity] = item
	}
	usersByPort := make(map[uint32][]coreUserSnapshot)
	for _, user := range core.Users {
		if user.Attributed && user.InboundPort > 0 {
			usersByPort[user.InboundPort] = append(usersByPort[user.InboundPort], user)
		}
	}
	type portStats struct {
		established int64
		timeWait    int64
		closeWait   int64
		connections map[string]int64
	}
	byPort := make(map[uint32]*portStats, len(usersByPort))
	for port := range usersByPort {
		byPort[port] = &portStats{connections: make(map[string]int64)}
	}
	for _, entry := range entries {
		stats := byPort[entry.LocalPort]
		if stats == nil {
			continue
		}
		switch entry.State {
		case tcpEstablished:
			stats.established++
		case tcpTimeWait:
			stats.timeWait++
		case tcpCloseWait:
			stats.closeWait++
		}
		if entry.State == tcpEstablished || entry.State == tcpSynRecv || entry.State == tcpCloseWait {
			if ip := normalizeOnlineIP(entry.RemoteIP); ip != "" {
				stats.connections[ip]++
				if tracker.lastSeen[entry.LocalPort] == nil {
					tracker.lastSeen[entry.LocalPort] = make(map[string]time.Time)
				}
				tracker.lastSeen[entry.LocalPort][ip] = now
			}
		}
	}
	for port, seen := range tracker.lastSeen {
		stats := byPort[port]
		if stats == nil {
			delete(tracker.lastSeen, port)
			continue
		}
		for ip, lastSeen := range seen {
			if now.Sub(lastSeen) > grace {
				delete(seen, ip)
				continue
			}
			if _, active := stats.connections[ip]; !active {
				stats.connections[ip] = 0
			}
		}
	}

	var inbounds []inboundSnapshot
	for port, users := range usersByPort {
		stats := byPort[port]
		inbound := inboundSnapshot{
			Port:        port,
			InboundTag:  users[0].Identity.InboundTag,
			Protocol:    users[0].InboundName,
			Established: stats.established,
			TimeWait:    stats.timeWait,
			CloseWait:   stats.closeWait,
			Attribution: "inbound_port",
		}
		if len(users) == 1 {
			inbound.User = users[0].Identity.User
			inbound.Attribution = "single_user_inbound"
			inbound.MaxOnlineIPs = settingsByIdentity[users[0].Identity].MaxInboundOnlineIPs
		}
		for ip, count := range stats.connections {
			inbound.OnlineIPs = append(inbound.OnlineIPs, onlineIP{IP: ip, Connections: count})
		}
		sort.Slice(inbound.OnlineIPs, func(i, j int) bool { return inbound.OnlineIPs[i].IP < inbound.OnlineIPs[j].IP })
		inbound.OnlineIPCount = len(inbound.OnlineIPs)
		inbounds = append(inbounds, inbound)
	}
	sort.Slice(inbounds, func(i, j int) bool {
		if inbounds[i].Port != inbounds[j].Port {
			return inbounds[i].Port < inbounds[j].Port
		}
		return inbounds[i].InboundTag < inbounds[j].InboundTag
	})

	proxyUsers := make([]proxyUserSnapshot, 0, len(core.Users))
	for _, user := range core.Users {
		if !user.Attributed {
			continue
		}
		limit := settingsByIdentity[user.Identity]
		proxyUsers = append(proxyUsers, proxyUserSnapshot{
			Identity:                   user.Identity,
			InboundTag:                 user.Identity.InboundTag,
			User:                       user.Identity.User,
			InboundPort:                user.InboundPort,
			InboundActive:              user.InboundActive,
			OutboundActive:             user.OutboundActive,
			OutboundNewRate:            user.OutboundNewRate,
			OutboundNewTotal:           user.OutboundNewTotal,
			OutboundRejectedTotal:      user.OutboundRejectedTotal,
			MaxInboundOnlineIPs:        limit.MaxInboundOnlineIPs,
			MaxOutboundTCPActive:       limit.MaxOutboundTCPActive,
			MaxOutboundTCPNewPerSecond: limit.MaxOutboundTCPNewPerSecond,
			CloseWaitTimeoutSeconds:    effectiveCloseWait(settings, limit),
			Source:                     "xray_core_runtime",
		})
	}
	return inbounds, proxyUsers
}

func effectiveCloseWait(settings connectionSettings, user userConnectionSettings) *int64 {
	if user.CloseWaitTimeoutSeconds != nil {
		return user.CloseWaitTimeoutSeconds
	}
	return settings.DefaultCloseWaitTimeoutSeconds
}
