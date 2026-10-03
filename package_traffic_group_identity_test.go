package main

import (
	"testing"
)

func trafficGroupIdentityFixture(t *testing.T) (trafficGroupIdentityData, trafficGroupAssignment, map[int64]trafficGroupNode) {
	t.Helper()
	configured, err := trafficGroupConfiguredIdentities(`{"inbounds":[{"tag":"in-a","protocol":"vless","settings":{"clients":[{"email":"alice-a","id":"uuid-alice"},{"email":"bob-a","id":"uuid-bob"}]}},{"tag":"in-b","protocol":"vless","settings":{"clients":[{"email":"alice-b","id":"uuid-b"}]}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	data := trafficGroupIdentityData{
		ServerID: 4, Configured: configured, NodeIdentities: map[int64][]string{},
		Refs:     []trafficGroupIdentityRef{{AssignmentID: 7, Username: "alice", Tag: "in-a", Identity: "alice-a", Credential: `{"email":"alice-a","id":"uuid-alice"}`}},
		Bindings: []trafficGroupIdentityBinding{{AssignmentID: 7, Username: "alice", NodeIDs: []int64{1, 2}}},
	}
	assignment := trafficGroupAssignment{ID: 7, PackageID: 3, Username: "alice"}
	nodes := map[int64]trafficGroupNode{
		1: {ID: 1, Name: "A", ServerID: 4, ServerName: "server", Tag: "in-a", Owner: "admin"},
		2: {ID: 2, Name: "B", ServerID: 4, ServerName: "server", Tag: "in-b", Owner: "admin"},
	}
	return data, assignment, nodes
}

func TestTrafficGroupIdentityResolution(t *testing.T) {
	tests := []struct {
		name   string
		change func(*trafficGroupIdentityData, map[int64]trafficGroupNode, *[]int64)
		want   bool
	}{
		{name: "assignment credential", want: true},
		{name: "legacy credential", want: true, change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Refs[0].AssignmentID = 0
		}},
		{name: "configured binding email", want: true, change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Refs = nil
			d.Bindings[0].Email = "alice-a"
		}},
		{name: "missing configured identity", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) { d.Configured = nil }},
		{name: "missing binding", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) { d.Bindings = nil }},
		{name: "changed credential", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Refs[0].Credential = `{"email":"alice-a","id":"changed"}`
		}},
		{name: "wrong server", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) { d.ServerID = 99 }},
		{name: "no group membership", change: func(_ *trafficGroupIdentityData, _ map[int64]trafficGroupNode, group *[]int64) { *group = []int64{2} }},
		{name: "shared inbound outside group", change: func(_ *trafficGroupIdentityData, nodes map[int64]trafficGroupNode, _ *[]int64) {
			n := nodes[2]
			n.Tag = "in-a"
			nodes[2] = n
		}},
		{name: "all shared nodes in group", want: true, change: func(_ *trafficGroupIdentityData, nodes map[int64]trafficGroupNode, group *[]int64) {
			n := nodes[2]
			n.Tag = "in-a"
			nodes[2] = n
			*group = []int64{1, 2}
		}},
		{name: "empty selection means all nodes", change: func(d *trafficGroupIdentityData, nodes map[int64]trafficGroupNode, _ *[]int64) {
			d.Bindings[0].NodeIDs = nil
			n := nodes[2]
			n.Tag = "in-a"
			nodes[2] = n
		}},
		{name: "duplicate configured email", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Configured["in-a"] = append(d.Configured["in-a"], d.Configured["in-a"][0])
		}},
		{name: "duplicate authentication secret", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Configured["in-a"][1].Credential["id"] = "uuid-alice"
		}},
		{name: "authentication UUID case aliases", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Configured["in-a"][1].Credential["id"] = "UUID-ALICE"
		}},
		{name: "conflicting explicit owner", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Ownership.Relations = []connectionOwnershipRelation{{InboundTag: "in-a", ManagementUsername: "bob", ProtocolIdentity: "alice-a", Source: connectionSourceManual}}
		}},
		{name: "unknown port owner", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Ownership.Relations = []connectionOwnershipRelation{{InboundTag: "in-a", ManagementUsername: "bob", Source: connectionSourceOwner}}
		}},
		{name: "unresolved third party credential", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Refs = append(d.Refs, trafficGroupIdentityRef{Username: "bob", Tag: "in-a", Credential: "{}"})
		}},
		{name: "same identity another user", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			ref := d.Refs[0]
			ref.Username = "bob"
			d.Refs = append(d.Refs, ref)
		}},
		{name: "different configured user is safe", want: true, change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Refs = append(d.Refs, trafficGroupIdentityRef{AssignmentID: 8, Username: "bob", Tag: "in-a", Identity: "bob-a", Credential: `{"email":"bob-a","id":"uuid-bob"}`})
			d.Bindings = append(d.Bindings, trafficGroupIdentityBinding{AssignmentID: 8, Username: "bob", NodeIDs: []int64{1}})
		}},
		{name: "shared default node credential", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.NodeIdentities[1] = []string{"alice-a"}
			d.Bindings = append(d.Bindings, trafficGroupIdentityBinding{AssignmentID: 8, Username: "bob", NodeIDs: []int64{1}})
		}},
		{name: "unresolved bound user on multi-user inbound", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Bindings = append(d.Bindings, trafficGroupIdentityBinding{AssignmentID: 8, Username: "bob", NodeIDs: []int64{1}})
		}},
		{name: "single configured secret with another user", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode, _ *[]int64) {
			d.Configured["in-a"] = d.Configured["in-a"][:1]
			d.Bindings = append(d.Bindings, trafficGroupIdentityBinding{AssignmentID: 8, Username: "bob", NodeIDs: []int64{1}})
		}},
		{name: "legacy credential reaches unbound alias", change: func(d *trafficGroupIdentityData, nodes map[int64]trafficGroupNode, _ *[]int64) {
			d.Refs[0].AssignmentID = 0
			nodes[3] = trafficGroupNode{ID: 3, ServerID: 4, Tag: "in-a"}
		}},
		{name: "owned alias outside group", change: func(_ *trafficGroupIdentityData, nodes map[int64]trafficGroupNode, _ *[]int64) {
			nodes[3] = trafficGroupNode{ID: 3, ServerID: 4, Tag: "in-a", Owner: "alice"}
		}},
		{name: "same tag another server", want: true, change: func(d *trafficGroupIdentityData, nodes map[int64]trafficGroupNode, _ *[]int64) {
			n := nodes[2]
			n.Tag = "in-a"
			n.ServerID = 5
			nodes[2] = n
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, assignment, nodes := trafficGroupIdentityFixture(t)
			group := []int64{1}
			if test.change != nil {
				test.change(&data, nodes, &group)
			}
			identity, reason := resolveTrafficGroupIdentity(data, assignment, nodes[1], group, nodes)
			if (reason == "") != test.want || (test.want && identity != (serverConnectionIdentity{InboundTag: "in-a", User: "alice-a"})) || (!test.want && identity != (serverConnectionIdentity{})) {
				t.Fatalf("identity=%#v reason=%q, want safe=%t", identity, reason, test.want)
			}
		})
	}
}

func TestTrafficGroupIdentityOtherAssignment(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		data, assignment, nodes := trafficGroupIdentityFixture(t)
		nodes[3] = trafficGroupNode{ID: 3, ServerID: 4, Tag: "in-a"}
		data.Bindings = append(data.Bindings, trafficGroupIdentityBinding{AssignmentID: 8, Username: "alice", NodeIDs: []int64{3}})
		ref := data.Refs[0]
		ref.AssignmentID = 8
		if distinct {
			ref.Identity, ref.Credential = "bob-a", `{"email":"bob-a","id":"uuid-bob"}`
		}
		data.Refs = append(data.Refs, ref)
		_, reason := resolveTrafficGroupIdentity(data, assignment, nodes[1], []int64{1}, nodes)
		if (reason == "") != distinct {
			t.Fatalf("other assignment distinct=%t reason=%q", distinct, reason)
		}
	}
}

func TestTrafficGroupIdentityRoutedNodesRequireTheirOwnSubaccount(t *testing.T) {
	data, assignment, nodes := trafficGroupIdentityFixture(t)
	for id, node := range nodes {
		node.Routed, node.Tag = true, "in-a"
		nodes[id] = node
	}
	base := data.Refs[0]
	data.Refs = []trafficGroupIdentityRef{
		base,
		{AssignmentID: 7, Username: "alice", NodeID: 1, Tag: "in-a", Identity: "alice-a", Credential: `{"id":"uuid-alice"}`},
		{AssignmentID: 7, Username: "alice", NodeID: 2, Tag: "in-a", Identity: "bob-a", Credential: `{"id":"uuid-bob"}`},
	}
	identity, reason := resolveTrafficGroupIdentity(data, assignment, nodes[1], []int64{1}, nodes)
	if reason != "" || identity.User != "alice-a" {
		t.Fatalf("distinct routed identities were not resolved: %#v %q", identity, reason)
	}
	data.Refs = []trafficGroupIdentityRef{base}
	if _, reason := resolveTrafficGroupIdentity(data, assignment, nodes[1], []int64{1}, nodes); reason == "" {
		t.Fatal("routed node incorrectly used the physical inbound identity")
	}
}

func TestTrafficGroupConfiguredIdentitiesIgnoreSingleSecretAndNestedEmails(t *testing.T) {
	configured, err := trafficGroupConfiguredIdentities(`{"inbounds":[{"tag":"single","protocol":"shadowsocks","settings":{"password":"shared","email":"shared-user"}},{"tag":"nested","protocol":"vless","settings":{"clients":[{"id":"uuid","metadata":{"email":"not-an-identity"}}]}},{"tag":"clients","protocol":"vless","settings":{"clients":[{"id":"uuid-client","email":"client"}]}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(configured["single"]) != 0 || len(configured["nested"]) != 1 || configured["nested"][0].Identity != "" || len(configured["clients"]) != 1 {
		t.Fatalf("unsafe config identities included: %#v", configured)
	}
	if identities := trafficGroupRefIdentities(trafficGroupIdentityRef{Credential: `{"id":"uuid"}`}, configured["nested"]); len(identities) != 0 {
		t.Fatal("anonymous credential must not resolve to a blocking identity")
	}
}

func TestTrafficGroupIdentityAnonymousCredentialCollision(t *testing.T) {
	data, assignment, nodes := trafficGroupIdentityFixture(t)
	var err error
	data.Configured, err = trafficGroupConfiguredIdentities(`{"inbounds":[{"tag":"in-a","protocol":"vless","settings":{"clients":[{"id":"uuid-alice","email":"alice-a"},{"id":"uuid-alice"}]}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, reason := resolveTrafficGroupIdentity(data, assignment, nodes[1], []int64{1}, nodes); reason == "" {
		t.Fatal("anonymous duplicate auth credential can share the blocked identity")
	}
}

func TestTrafficGroupSS2022OwnershipAndIsolation(t *testing.T) {
	config := `{"inbounds":[{"tag":"in-a","protocol":"shadowsocks","settings":{"method":"2022-blake3-aes-128-gcm","password":"server-key","clients":[{"email":"admin-a","password":"owner-key"},{"email":"alice-a","password":"alice-key"},{"email":"bob-a","password":"bob-key"}]}}]}`
	for _, test := range []struct {
		name   string
		change func(*trafficGroupIdentityData, map[int64]trafficGroupNode)
		want   bool
	}{
		{name: "independent users and node owner", want: true},
		{name: "unresolved node owner", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Ownership.Relations[0].ProtocolIdentity = ""
		}},
		{name: "shared authentication key", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Configured["in-a"][0].Credential["password"] = "alice-key"
		}},
		{name: "shared identity owner", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Ownership.Relations[0].ProtocolIdentity = "alice-a"
		}},
		{name: "duplicate identity", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Configured["in-a"] = append(d.Configured["in-a"], d.Configured["in-a"][1])
		}},
		{name: "group external node shares inbound", change: func(_ *trafficGroupIdentityData, nodes map[int64]trafficGroupNode) {
			node := nodes[2]
			node.Tag = "in-a"
			nodes[2] = node
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, assignment, nodes := trafficGroupIdentityFixture(t)
			var err error
			data.Configured, err = trafficGroupConfiguredIdentities(config)
			if err != nil {
				t.Fatal(err)
			}
			owners := matchNodeProtocolIdentities([]string{`{"password":"server-key:owner-key"}`}, extractCoreInboundCredentials(config)["in-a"])
			if len(owners) != 1 || owners[0] != "admin-a" {
				t.Fatalf("SS2022 node owner was not matched exactly: %v", owners)
			}
			data.NodeIdentities[1] = owners
			data.Ownership.Relations = []connectionOwnershipRelation{{InboundTag: "in-a", ManagementUsername: "admin", ProtocolIdentity: owners[0], Source: connectionSourceOwner}}
			data.Refs[0].Credential = `{"email":"alice-a","password":"alice-key"}`
			data.Refs = append(data.Refs, trafficGroupIdentityRef{AssignmentID: 8, Username: "bob", Tag: "in-a", Identity: "bob-a", Credential: `{"email":"bob-a","password":"bob-key"}`})
			data.Bindings = append(data.Bindings, trafficGroupIdentityBinding{AssignmentID: 8, Username: "bob", NodeIDs: []int64{1}})
			if test.change != nil {
				test.change(&data, nodes)
			}
			identity, reason := resolveTrafficGroupIdentity(data, assignment, nodes[1], []int64{1}, nodes)
			if (reason == "") != test.want || (test.want && identity != (serverConnectionIdentity{InboundTag: "in-a", User: "alice-a"})) || (!test.want && identity != (serverConnectionIdentity{})) {
				t.Fatalf("identity=%#v reason=%q, want safe=%t", identity, reason, test.want)
			}
		})
	}
}
