package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	connectionSourceBinding = "binding"
	connectionSourceOwner   = "owner"
	connectionSourceManual  = "manual"
)

type connectionOwnershipRelation struct {
	InboundTag         string `json:"inbound_tag"`
	ManagementUsername string `json:"management_username"`
	ProtocolIdentity   string `json:"protocol_identity,omitempty"`
	Source             string `json:"source"`
	AssignmentType     string `json:"assignment_type,omitempty"`
}

type connectionOwnershipData struct {
	ManagementUsers []string                      `json:"management_users"`
	Relations       []connectionOwnershipRelation `json:"relations"`
}

type connectionOwnershipStore interface {
	EnsureConnectionOwnershipSchema(context.Context) error
	ConnectionOwnership(context.Context, string) (connectionOwnershipData, error)
	SaveManualConnectionAssignment(context.Context, string, connectionOwnershipRelation) error
	DeleteManualConnectionAssignment(context.Context, string, connectionOwnershipRelation) error
}

type serverManagementUserSettings struct {
	Username                   string `json:"username"`
	MaxOutboundTCPActive       *int64 `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int   `json:"max_outbound_tcp_new_per_second"`
}

type serverManagementMapping struct {
	Identity serverConnectionIdentity `json:"identity"`
	Group    string                   `json:"group"`
}

type serverManagementGroupConnections struct {
	Username                   string               `json:"group"`
	CurrentTotal               int64                `json:"current_total"`
	InboundActive              int64                `json:"inbound_active"`
	InboundTCP                 serverTCPStateCounts `json:"inbound_tcp"`
	InboundOnlineIPs           []serverOnlineIP     `json:"inbound_online_ips"`
	OutboundActive             int64                `json:"outbound_active"`
	OutboundPending            int64                `json:"outbound_pending"`
	OutboundTCP                serverTCPStateCounts `json:"outbound_tcp"`
	OutboundNewRate            int                  `json:"outbound_new_rate"`
	OutboundNewTotal           uint64               `json:"outbound_new_total"`
	OutboundRejectedTotal      uint64               `json:"outbound_rejected_total"`
	RejectedUserTotalLimit     uint64               `json:"rejected_user_total_limit"`
	RejectedUserNewRateLimit   uint64               `json:"rejected_user_new_rate_limit"`
	RejectedPortTotalLimit     uint64               `json:"rejected_port_total_limit"`
	RejectedPortNewRateLimit   uint64               `json:"rejected_port_new_rate_limit"`
	RejectedOnlineIPLimit      uint64               `json:"rejected_online_ip_limit"`
	RejectedGlobalTotalLimit   uint64               `json:"rejected_global_total_limit"`
	MaxOutboundTCPActive       *int64               `json:"max_outbound_tcp_active"`
	MaxOutboundTCPNewPerSecond *int                 `json:"max_outbound_tcp_new_per_second"`
}

type serverManagementPort struct {
	InboundTag         string                       `json:"inbound_tag"`
	Port               uint32                       `json:"port"`
	Protocol           string                       `json:"protocol,omitempty"`
	Source             string                       `json:"source"`
	ProtocolIdentities []string                     `json:"protocol_identities"`
	ManualAssignments  []string                     `json:"manual_assignments,omitempty"`
	RuntimeAttributed  bool                         `json:"runtime_attributed"`
	Aggregate          serverProxyUserConnections   `json:"aggregate"`
	Limits             serverPortConnectionSettings `json:"limits"`
}

type serverManagementUser struct {
	Username  string                           `json:"username"`
	Source    string                           `json:"source"`
	Aggregate serverManagementGroupConnections `json:"aggregate"`
	Limits    serverManagementUserSettings     `json:"limits"`
	Ports     []serverManagementPort           `json:"ports"`
}

type serverUnassignedPort struct {
	InboundTag         string   `json:"inbound_tag"`
	Port               uint32   `json:"port"`
	Protocol           string   `json:"protocol,omitempty"`
	ProtocolIdentities []string `json:"protocol_identities"`
	Reason             string   `json:"reason"`
}

type serverManagementView struct {
	Users           []serverManagementUser `json:"users"`
	UnassignedPorts []serverUnassignedPort `json:"unassigned_ports"`
	AssignableUsers []string               `json:"assignable_users"`
	Warnings        []string               `json:"warnings"`
}

type manualConnectionAssignmentRequest struct {
	InboundTag       string `json:"inbound_tag"`
	ManagementUser   string `json:"management_username"`
	ProtocolIdentity string `json:"protocol_identity,omitempty"`
}

func managementSettingByUsername(settings serverConnectionSettings) map[string]serverManagementUserSettings {
	result := make(map[string]serverManagementUserSettings, len(settings.ManagementUsers))
	for _, item := range settings.ManagementUsers {
		result[item.Username] = item
	}
	return result
}

func buildManagementView(snapshot serverDetailedConnectionSnapshot, settings serverConnectionSettings, ownership connectionOwnershipData) (serverManagementView, []serverManagementMapping) {
	view := serverManagementView{
		Users:           []serverManagementUser{},
		UnassignedPorts: []serverUnassignedPort{},
		AssignableUsers: append([]string(nil), ownership.ManagementUsers...),
		Warnings:        []string{},
	}
	sort.Strings(view.AssignableUsers)
	limitsByUser := managementSettingByUsername(settings)
	limitsByPort := make(map[string]serverPortConnectionSettings, len(settings.Ports))
	for _, item := range settings.Ports {
		limitsByPort[item.InboundTag] = item
	}
	groupRuntime := make(map[string]serverManagementGroupConnections, len(snapshot.ManagementGroups))
	for _, group := range snapshot.ManagementGroups {
		groupRuntime[group.Username] = group
	}
	runtimeByTag := make(map[string][]serverProxyUserConnections)
	for _, runtime := range snapshot.ProxyUsers {
		runtimeByTag[runtime.InboundTag] = append(runtimeByTag[runtime.InboundTag], runtime)
	}
	inboundByTag := make(map[string]serverInboundConnections, len(snapshot.Inbounds))
	for _, inbound := range snapshot.Inbounds {
		if inbound.InboundTag != "" {
			inboundByTag[inbound.InboundTag] = inbound
		}
	}

	relationsByTag := make(map[string][]connectionOwnershipRelation)
	for _, relation := range ownership.Relations {
		if relation.InboundTag == "" || relation.ManagementUsername == "" {
			continue
		}
		relationsByTag[relation.InboundTag] = append(relationsByTag[relation.InboundTag], relation)
	}

	tags := make(map[string]struct{}, len(inboundByTag)+len(runtimeByTag))
	for tag := range inboundByTag {
		tags[tag] = struct{}{}
	}
	for tag := range runtimeByTag {
		if tag != "" {
			tags[tag] = struct{}{}
		}
	}
	orderedTags := make([]string, 0, len(tags))
	for tag := range tags {
		orderedTags = append(orderedTags, tag)
	}
	sort.Strings(orderedTags)

	users := make(map[string]*serverManagementUser)
	mappings := make(map[serverConnectionIdentity]string)
	for _, tag := range orderedTags {
		inbound := inboundByTag[tag]
		if inbound.InboundTag == "" {
			inbound.InboundTag = tag
			if len(runtimeByTag[tag]) > 0 {
				inbound.Port = runtimeByTag[tag][0].InboundPort
			}
		}
		runtimes := runtimeByTag[tag]
		identities := uniqueRuntimeIdentities(runtimes)
		relations := effectiveOwnershipRelations(relationsByTag[tag])
		if len(relations) == 0 {
			view.UnassignedPorts = append(view.UnassignedPorts, serverUnassignedPort{
				InboundTag: tag, Port: inbound.Port, Protocol: inbound.Protocol,
				ProtocolIdentities: identities, Reason: "no_binding_owner_or_manual_assignment",
			})
			continue
		}

		resolved := resolveRelationIdentities(relations, identities)
		if resolved.Warning != "" {
			view.Warnings = append(view.Warnings, fmt.Sprintf("%s: %s", tag, resolved.Warning))
		}
		for _, relation := range relations {
			user := users[relation.ManagementUsername]
			if user == nil {
				limit := limitsByUser[relation.ManagementUsername]
				limit.Username = relation.ManagementUsername
				aggregate := groupRuntime[relation.ManagementUsername]
				aggregate.Username = relation.ManagementUsername
				if aggregate.MaxOutboundTCPActive == nil {
					aggregate.MaxOutboundTCPActive = limit.MaxOutboundTCPActive
				}
				if aggregate.MaxOutboundTCPNewPerSecond == nil {
					aggregate.MaxOutboundTCPNewPerSecond = limit.MaxOutboundTCPNewPerSecond
				}
				user = &serverManagementUser{Username: relation.ManagementUsername, Source: relation.Source, Aggregate: aggregate, Limits: limit, Ports: []serverManagementPort{}}
				users[relation.ManagementUsername] = user
			} else if sourcePriority(relation.Source) > sourcePriority(user.Source) {
				user.Source = relation.Source
			}
			port := findOrAppendManagementPort(user, inbound, relation.Source, limitsByPort[tag])
			if relation.Source == connectionSourceManual {
				port.ManualAssignments = appendUniqueString(port.ManualAssignments, relation.ProtocolIdentity)
			}
			for _, identity := range resolved.ByUser[relation.ManagementUsername] {
				key := serverConnectionIdentity{InboundTag: tag, User: identity}
				if existing, exists := mappings[key]; exists && existing != relation.ManagementUsername {
					view.Warnings = append(view.Warnings, fmt.Sprintf("%s/%s has conflicting management users", tag, identity))
					continue
				}
				mappings[key] = relation.ManagementUsername
				port.ProtocolIdentities = appendUniqueString(port.ProtocolIdentities, identity)
				port.RuntimeAttributed = true
			}
		}
		if len(resolved.Unassigned) > 0 {
			view.UnassignedPorts = append(view.UnassignedPorts, serverUnassignedPort{
				InboundTag: tag, Port: inbound.Port, Protocol: inbound.Protocol,
				ProtocolIdentities: resolved.Unassigned, Reason: "manual_identity_assignment_incomplete",
			})
		}
		for _, runtime := range runtimes {
			group := mappings[runtime.Identity]
			if group == "" {
				continue
			}
			user := users[group]
			if user == nil {
				continue
			}
			for index := range user.Ports {
				if user.Ports[index].InboundTag == tag {
					addProxyUserConnections(&user.Ports[index].Aggregate, runtime)
				}
			}
		}
	}

	usernames := make([]string, 0, len(users))
	for username := range users {
		usernames = append(usernames, username)
	}
	sort.Strings(usernames)
	for _, username := range usernames {
		user := users[username]
		for index := range user.Ports {
			sort.Strings(user.Ports[index].ProtocolIdentities)
		}
		sort.Slice(user.Ports, func(i, j int) bool {
			if user.Ports[i].Port != user.Ports[j].Port {
				return user.Ports[i].Port < user.Ports[j].Port
			}
			return user.Ports[i].InboundTag < user.Ports[j].InboundTag
		})
		view.Users = append(view.Users, *user)
	}
	resultMappings := make([]serverManagementMapping, 0, len(mappings))
	for identity, group := range mappings {
		resultMappings = append(resultMappings, serverManagementMapping{Identity: identity, Group: group})
	}
	sort.Slice(resultMappings, func(i, j int) bool {
		if resultMappings[i].Identity.InboundTag != resultMappings[j].Identity.InboundTag {
			return resultMappings[i].Identity.InboundTag < resultMappings[j].Identity.InboundTag
		}
		return resultMappings[i].Identity.User < resultMappings[j].Identity.User
	})
	return view, resultMappings
}

type resolvedRelationIdentities struct {
	ByUser     map[string][]string
	Unassigned []string
	Warning    string
}

func resolveRelationIdentities(relations []connectionOwnershipRelation, identities []string) resolvedRelationIdentities {
	result := resolvedRelationIdentities{ByUser: make(map[string][]string)}
	identitySet := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		identitySet[identity] = struct{}{}
	}
	official := make([]connectionOwnershipRelation, 0, len(relations))
	manual := make([]connectionOwnershipRelation, 0, len(relations))
	for _, relation := range relations {
		if relation.Source == connectionSourceManual {
			manual = append(manual, relation)
		} else {
			official = append(official, relation)
		}
	}
	if len(official) > 0 {
		resolveAuthoritativeRelations(&result, official, identities, identitySet)
		resolveRemainingManualIdentities(&result, manual, identities, identitySet)
		return result
	}
	users := make(map[string]struct{})
	for _, relation := range manual {
		users[relation.ManagementUsername] = struct{}{}
		if relation.ProtocolIdentity != "" {
			if _, exists := identitySet[relation.ProtocolIdentity]; exists {
				result.ByUser[relation.ManagementUsername] = appendUniqueString(result.ByUser[relation.ManagementUsername], relation.ProtocolIdentity)
			}
		}
	}
	if len(users) == 1 {
		var username string
		for value := range users {
			username = value
		}
		manualOnly := true
		hasPortAssignment := false
		for _, relation := range manual {
			manualOnly = manualOnly && relation.Source == connectionSourceManual
			hasPortAssignment = hasPortAssignment || relation.ProtocolIdentity == ""
		}
		if manualOnly && !hasPortAssignment {
			for _, identity := range identities {
				if !containsString(result.ByUser[username], identity) {
					result.Unassigned = append(result.Unassigned, identity)
				}
			}
			if len(result.Unassigned) > 0 {
				result.Warning = "manual identity assignments do not yet cover every protocol identity"
			}
			return result
		}
		for _, identity := range identities {
			result.ByUser[username] = appendUniqueString(result.ByUser[username], identity)
		}
		if len(identities) == 0 {
			result.ByUser[username] = append(result.ByUser[username], "")
		}
		return result
	}
	if len(identities) == 0 {
		result.Warning = "multiple management users share a single-secret inbound without distinct protocol identities"
		return result
	}
	for _, identity := range identities {
		owners := []string{}
		for username, values := range result.ByUser {
			for _, candidate := range values {
				if candidate == identity {
					owners = append(owners, username)
				}
			}
		}
		if len(owners) != 1 {
			result.Warning = "one or more protocol identities do not have one exact management-user relation"
			for _, username := range owners {
				result.ByUser[username] = removeString(result.ByUser[username], identity)
			}
		}
	}
	return result
}

func resolveAuthoritativeRelations(result *resolvedRelationIdentities, relations []connectionOwnershipRelation, identities []string, identitySet map[string]struct{}) {
	users := make(map[string]struct{})
	for _, relation := range relations {
		users[relation.ManagementUsername] = struct{}{}
		if relation.ProtocolIdentity != "" {
			if _, exists := identitySet[relation.ProtocolIdentity]; exists {
				result.ByUser[relation.ManagementUsername] = appendUniqueString(result.ByUser[relation.ManagementUsername], relation.ProtocolIdentity)
			}
		}
	}
	if len(users) == 1 {
		var username string
		for value := range users {
			username = value
		}
		for _, identity := range identities {
			result.ByUser[username] = appendUniqueString(result.ByUser[username], identity)
		}
		if len(identities) == 0 {
			result.ByUser[username] = appendUniqueString(result.ByUser[username], "")
		}
		return
	}
	if len(identities) == 0 {
		result.Warning = "multiple management users share a single-secret inbound without distinct protocol identities"
		return
	}
	removeAmbiguousIdentities(result, identities)
}

func resolveRemainingManualIdentities(result *resolvedRelationIdentities, relations []connectionOwnershipRelation, identities []string, identitySet map[string]struct{}) {
	for _, relation := range relations {
		if relation.ProtocolIdentity == "" {
			continue
		}
		if _, exists := identitySet[relation.ProtocolIdentity]; !exists || mappedIdentityOwner(result.ByUser, relation.ProtocolIdentity) != "" {
			continue
		}
		owners := []string{}
		for _, candidate := range relations {
			if candidate.ProtocolIdentity == relation.ProtocolIdentity {
				owners = appendUniqueString(owners, candidate.ManagementUsername)
			}
		}
		if len(owners) == 1 {
			result.ByUser[relation.ManagementUsername] = appendUniqueString(result.ByUser[relation.ManagementUsername], relation.ProtocolIdentity)
		}
	}
	for _, identity := range identities {
		if mappedIdentityOwner(result.ByUser, identity) == "" {
			result.Unassigned = appendUniqueString(result.Unassigned, identity)
		}
	}
	if len(result.Unassigned) > 0 && result.Warning == "" {
		result.Warning = "one or more protocol identities do not have one exact management-user relation"
	}
}

func removeAmbiguousIdentities(result *resolvedRelationIdentities, identities []string) {
	for _, identity := range identities {
		owners := []string{}
		for username, values := range result.ByUser {
			if containsString(values, identity) {
				owners = append(owners, username)
			}
		}
		if len(owners) == 1 {
			continue
		}
		result.Warning = "one or more protocol identities do not have one exact management-user relation"
		result.Unassigned = appendUniqueString(result.Unassigned, identity)
		for _, username := range owners {
			result.ByUser[username] = removeString(result.ByUser[username], identity)
		}
	}
}

func mappedIdentityOwner(byUser map[string][]string, identity string) string {
	owner := ""
	for username, values := range byUser {
		if !containsString(values, identity) {
			continue
		}
		if owner != "" && owner != username {
			return "*"
		}
		owner = username
	}
	return owner
}

func removeString(values []string, unwanted string) []string {
	result := values[:0]
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}

func effectiveOwnershipRelations(relations []connectionOwnershipRelation) []connectionOwnershipRelation {
	priority := 0
	for _, relation := range relations {
		if relation.Source != connectionSourceManual {
			if value := sourcePriority(relation.Source); value > priority {
				priority = value
			}
		}
	}
	result := make([]connectionOwnershipRelation, 0, len(relations))
	seen := make(map[string]struct{})
	for _, relation := range relations {
		if relation.Source != connectionSourceManual && sourcePriority(relation.Source) != priority {
			continue
		}
		key := relation.ManagementUsername + "\x00" + relation.ProtocolIdentity
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, relation)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ManagementUsername != result[j].ManagementUsername {
			return result[i].ManagementUsername < result[j].ManagementUsername
		}
		return result[i].ProtocolIdentity < result[j].ProtocolIdentity
	})
	return result
}

func sourcePriority(source string) int {
	switch source {
	case connectionSourceBinding:
		return 3
	case connectionSourceOwner:
		return 2
	case connectionSourceManual:
		return 1
	default:
		return 0
	}
}

func uniqueRuntimeIdentities(users []serverProxyUserConnections) []string {
	result := []string{}
	for _, user := range users {
		result = appendUniqueString(result, user.User)
	}
	sort.Strings(result)
	return result
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func findOrAppendManagementPort(user *serverManagementUser, inbound serverInboundConnections, source string, limits serverPortConnectionSettings) *serverManagementPort {
	for index := range user.Ports {
		if user.Ports[index].InboundTag == inbound.InboundTag {
			return &user.Ports[index]
		}
	}
	user.Ports = append(user.Ports, serverManagementPort{
		InboundTag: inbound.InboundTag, Port: inbound.Port, Protocol: inbound.Protocol, Source: source,
		ProtocolIdentities: []string{}, Aggregate: emptyServerProxyUserConnections(inbound.InboundTag, inbound.Port), Limits: limits,
		ManualAssignments: []string{},
	})
	return &user.Ports[len(user.Ports)-1]
}

func emptyServerProxyUserConnections(tag string, port uint32) serverProxyUserConnections {
	return serverProxyUserConnections{InboundTag: tag, InboundPort: port, InboundOnlineIPs: []serverOnlineIP{}}
}

func addProxyUserConnections(target *serverProxyUserConnections, source serverProxyUserConnections) {
	target.CurrentTotal += source.CurrentTotal
	target.InboundActive += source.InboundActive
	target.OutboundActive += source.OutboundActive
	target.OutboundPending += source.OutboundPending
	target.OutboundNewRate += source.OutboundNewRate
	target.OutboundNewTotal += source.OutboundNewTotal
	target.OutboundRejectedTotal += source.OutboundRejectedTotal
	target.RejectedActiveLimit += source.RejectedActiveLimit
	target.RejectedNewRateLimit += source.RejectedNewRateLimit
	target.RejectedUserTotalLimit += source.RejectedUserTotalLimit
	target.RejectedPortTotalLimit += source.RejectedPortTotalLimit
	target.RejectedUserNewRateLimit += source.RejectedUserNewRateLimit
	target.RejectedPortNewRateLimit += source.RejectedPortNewRateLimit
	target.RejectedOnlineIPLimit += source.RejectedOnlineIPLimit
	target.RejectedGlobalTotalLimit += source.RejectedGlobalTotalLimit
	addServerTCPCounts(&target.InboundTCP, source.InboundTCP)
	addServerTCPCounts(&target.OutboundTCP, source.OutboundTCP)
	ipCounts := make(map[string]int64)
	for _, ip := range target.InboundOnlineIPs {
		ipCounts[ip.IP] += ip.Connections
	}
	for _, ip := range source.InboundOnlineIPs {
		ipCounts[ip.IP] += ip.Connections
	}
	target.InboundOnlineIPs = target.InboundOnlineIPs[:0]
	for ip, count := range ipCounts {
		target.InboundOnlineIPs = append(target.InboundOnlineIPs, serverOnlineIP{IP: ip, Connections: count})
	}
	sort.Slice(target.InboundOnlineIPs, func(i, j int) bool { return target.InboundOnlineIPs[i].IP < target.InboundOnlineIPs[j].IP })
}

func addServerTCPCounts(target *serverTCPStateCounts, source serverTCPStateCounts) {
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

func validateManualAssignmentRequest(request manualConnectionAssignmentRequest) (connectionOwnershipRelation, error) {
	relation := connectionOwnershipRelation{
		InboundTag: strings.TrimSpace(request.InboundTag), ManagementUsername: strings.TrimSpace(request.ManagementUser),
		ProtocolIdentity: strings.TrimSpace(request.ProtocolIdentity), Source: connectionSourceManual, AssignmentType: "manual",
	}
	if relation.InboundTag == "" || relation.ManagementUsername == "" {
		return connectionOwnershipRelation{}, errors.New("inbound_tag and management_username are required")
	}
	if len(relation.InboundTag) > 512 || len(relation.ManagementUsername) > 255 || len(relation.ProtocolIdentity) > 512 {
		return connectionOwnershipRelation{}, errors.New("manual assignment is too long")
	}
	return relation, nil
}
