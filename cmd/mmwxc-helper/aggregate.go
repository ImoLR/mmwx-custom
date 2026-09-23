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
	settingsByPort := make(map[string]portConnectionSettings, len(settings.Ports))
	for _, item := range settings.Ports {
		settingsByPort[item.InboundTag] = item
	}
	runtimeUsers := append([]coreUserSnapshot(nil), core.Users...)
	knownUsers := make(map[coreIdentity]struct{}, len(runtimeUsers))
	for _, user := range runtimeUsers {
		if user.Attributed {
			knownUsers[user.Identity] = struct{}{}
		}
	}
	for _, inbound := range core.Inbounds {
		for _, user := range inbound.Users {
			identity := coreIdentity{InboundTag: inbound.InboundTag, User: user}
			if identity.InboundTag == "" || identity.User == "" {
				continue
			}
			if _, exists := knownUsers[identity]; exists {
				continue
			}
			knownUsers[identity] = struct{}{}
			runtimeUsers = append(runtimeUsers, coreUserSnapshot{
				Identity: identity, InboundName: inbound.InboundName, InboundPort: inbound.InboundPort, Attributed: true,
			})
		}
	}
	usersByPort := make(map[uint32][]coreUserSnapshot)
	for _, user := range runtimeUsers {
		if user.Attributed && user.InboundPort > 0 {
			usersByPort[user.InboundPort] = append(usersByPort[user.InboundPort], user)
		}
	}
	type portStats struct {
		tcp         tcpStateCounts
		connections map[string]int64
	}
	byPort := make(map[uint32]*portStats, len(usersByPort)+len(core.Inbounds))
	configuredByPort := make(map[uint32]coreInboundSnapshot, len(core.Inbounds))
	for _, inbound := range core.Inbounds {
		if inbound.InboundPort == 0 {
			continue
		}
		configuredByPort[inbound.InboundPort] = inbound
		byPort[inbound.InboundPort] = &portStats{connections: make(map[string]int64)}
	}
	for port := range usersByPort {
		if byPort[port] == nil {
			byPort[port] = &portStats{connections: make(map[string]int64)}
		}
	}
	if core.Version >= 2 {
		for _, user := range runtimeUsers {
			stats := byPort[user.InboundPort]
			if stats == nil || !user.Attributed {
				continue
			}
			addTCPCounts(&stats.tcp, user.InboundTCP)
			for _, source := range user.InboundOnlineIPs {
				stats.connections[source.IP] += source.Connections
			}
		}
		for port := range byPort {
			delete(tracker.lastSeen, port)
		}
	} else {
		// Compatibility path for control interface v1. It can only attribute
		// kernel states to an inbound port, never to a user.
		for _, entry := range entries {
			stats := byPort[entry.LocalPort]
			if stats == nil {
				continue
			}
			addSocketState(&stats.tcp, entry.State)
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
	}
	if core.Version < 2 {
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
	}

	var inbounds []inboundSnapshot
	for port, stats := range byPort {
		users := usersByPort[port]
		configured := configuredByPort[port]
		inbound := inboundSnapshot{
			Port:        port,
			InboundTag:  configured.InboundTag,
			Protocol:    configured.InboundName,
			TCP:         stats.tcp,
			Established: stats.tcp.Established,
			SynRecv:     stats.tcp.SynRecv,
			FinWait1:    stats.tcp.FinWait1,
			FinWait2:    stats.tcp.FinWait2,
			TimeWait:    stats.tcp.TimeWait,
			CloseWait:   stats.tcp.CloseWait,
			LastAck:     stats.tcp.LastAck,
			Closing:     stats.tcp.Closing,
			Attribution: "core_inbound",
		}
		if len(users) > 0 {
			if inbound.InboundTag == "" {
				inbound.InboundTag = users[0].Identity.InboundTag
			}
			if inbound.Protocol == "" {
				inbound.Protocol = users[0].InboundName
			}
			inbound.Attribution = "inbound_port"
		}
		if len(users) == 1 {
			inbound.User = users[0].Identity.User
			inbound.Attribution = "single_user_inbound"
		}
		inbound.MaxOnlineIPs = settingsByPort[inbound.InboundTag].MaxInboundOnlineIPs
		if core.Version >= 2 && len(users) > 0 {
			inbound.Attribution = "core_identity_tuple"
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

	proxyUsers := make([]proxyUserSnapshot, 0, len(runtimeUsers))
	for _, user := range runtimeUsers {
		if !user.Attributed {
			continue
		}
		limit := settingsByIdentity[user.Identity]
		proxyUsers = append(proxyUsers, proxyUserSnapshot{
			Identity:                       user.Identity,
			InboundTag:                     user.Identity.InboundTag,
			User:                           user.Identity.User,
			InboundPort:                    user.InboundPort,
			InboundActive:                  user.InboundActive,
			InboundCurrent:                 user.InboundCurrent,
			CurrentTotal:                   user.CurrentTotal,
			InboundTCP:                     user.InboundTCP,
			InboundOnlineIPs:               append([]onlineIP(nil), user.InboundOnlineIPs...),
			OutboundActive:                 user.OutboundActive,
			OutboundPending:                user.OutboundPending,
			OutboundTCP:                    user.OutboundTCP,
			OutboundNewRate:                user.OutboundNewRate,
			OutboundNewTotal:               user.OutboundNewTotal,
			OutboundRejectedTotal:          user.OutboundRejectedTotal,
			RejectedActiveLimit:            user.RejectedActiveLimit,
			RejectedNewRateLimit:           user.RejectedNewRateLimit,
			RejectedUserTotalLimit:         user.RejectedUserTotalLimit,
			RejectedPortTotalLimit:         user.RejectedPortTotalLimit,
			RejectedUserNewRateLimit:       user.RejectedUserNewRateLimit,
			RejectedPortNewRateLimit:       user.RejectedPortNewRateLimit,
			RejectedOnlineIPLimit:          user.RejectedOnlineIPLimit,
			RejectedGlobalTotalLimit:       user.RejectedGlobalTotalLimit,
			RejectedUserInboundLimit:       user.RejectedUserInboundLimit,
			RejectedPortInboundLimit:       user.RejectedPortInboundLimit,
			RejectedUserOnlineIPLimit:      user.RejectedUserOnlineIPLimit,
			RejectedPortOnlineIPLimit:      user.RejectedPortOnlineIPLimit,
			RejectedGlobalInboundLimit:     user.RejectedGlobalInboundLimit,
			MaxPortOutboundTCPActive:       user.MaxPortOutboundTCPActive,
			MaxPortOutboundTCPNewPerSecond: user.MaxPortOutboundTCPNewPerSecond,
			MaxPortInboundConnections:      user.MaxPortInboundConnections,
			MaxPortInboundOnlineIPs:        user.MaxPortInboundOnlineIPs,
			CloseWaitTimeoutSeconds:        effectiveCloseWait(settings, limit),
			Source:                         "xray_core_runtime",
			ManagementGroup:                user.ManagementGroup,
		})
	}
	return inbounds, proxyUsers
}

func addTCPCounts(target *tcpStateCounts, source tcpStateCounts) {
	target.Total += source.Total
	target.Established += source.Established
	target.SynSent += source.SynSent
	target.SynRecv += source.SynRecv
	target.FinWait1 += source.FinWait1
	target.FinWait2 += source.FinWait2
	target.TimeWait += source.TimeWait
	target.CloseWait += source.CloseWait
	target.LastAck += source.LastAck
	target.Closing += source.Closing
	target.Close += source.Close
	target.Unknown += source.Unknown
}

func addSocketState(target *tcpStateCounts, state string) {
	target.Total++
	switch state {
	case tcpEstablished:
		target.Established++
	case tcpSynSent:
		target.SynSent++
	case tcpSynRecv, tcpNewSynRecv:
		target.SynRecv++
	case tcpFinWait1:
		target.FinWait1++
	case tcpFinWait2:
		target.FinWait2++
	case tcpTimeWait:
		target.TimeWait++
	case tcpCloseWait:
		target.CloseWait++
	case tcpLastAck:
		target.LastAck++
	case tcpClosing:
		target.Closing++
	case tcpClose:
		target.Close++
	case tcpListen:
	default:
		target.Unknown++
	}
}

func effectiveCloseWait(settings connectionSettings, user userConnectionSettings) *int64 {
	if user.CloseWaitTimeoutSeconds != nil {
		return user.CloseWaitTimeoutSeconds
	}
	return settings.DefaultCloseWaitTimeoutSeconds
}
