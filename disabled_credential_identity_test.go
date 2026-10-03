package main

import (
	"encoding/json"
	"testing"
)

func disabledIdentityFixture(t *testing.T) (trafficGroupIdentityData, trafficGroupAssignment, map[int64]trafficGroupNode) {
	t.Helper()
	data, assignment, nodes := trafficGroupIdentityFixture(t)
	original := cloneLifecycleMap(data.Configured["in-a"][1].Credential)
	disabled := cloneLifecycleMap(original)
	disabled["id"] = "disabled-bob-secret"
	data.Configured["in-a"][1].Credential = disabled
	data.DisabledCredentials = []lifecycleCredentialBackup{{
		Username: "bob", ServerID: data.ServerID, InboundTag: "in-a", Protocol: "vless",
		OriginalCredential: original, DisabledCredential: cloneLifecycleMap(disabled),
		OriginalHash: hashJSON(original), DisabledHash: hashJSON(disabled),
	}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	data.Refs = append(data.Refs, trafficGroupIdentityRef{AssignmentID: 8, Username: "bob", Tag: "in-a", Identity: "bob-a", Credential: string(raw)})
	data.Bindings = append(data.Bindings, trafficGroupIdentityBinding{AssignmentID: 8, Username: "bob", NodeIDs: []int64{1}})
	return data, assignment, nodes
}

func TestTrafficGroupDisabledUserIsolation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*trafficGroupIdentityData, map[int64]trafficGroupNode)
		want   bool
	}{
		{name: "disabled user on the same inbound", want: true},
		{name: "primary matches despite unrelated entry metadata", want: true, change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Configured["in-a"][1].Credential["level"] = float64(1)
		}},
		{name: "backup belongs to another user", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.DisabledCredentials[0].Username = "alice"
		}},
		{name: "backup belongs to another server", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) { d.DisabledCredentials[0].ServerID++ }},
		{name: "backup belongs to another tag", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.DisabledCredentials[0].InboundTag = "in-b"
		}},
		{name: "backup belongs to another protocol", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.DisabledCredentials[0].Protocol = "trojan"
		}},
		{name: "same email but current secret has drifted", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Configured["in-a"][1].Credential["id"] = "unrelated-secret"
		}},
		{name: "reference does not match original secret", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Refs[1].Credential = `{"email":"bob-a","id":"unrelated-secret"}`
		}},
		{name: "reference belongs to another user", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) { d.Refs[1].Username = "charlie" }},
		{name: "duplicate authentication still refused", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Configured["in-a"][0].Credential["id"] = "disabled-bob-secret"
			d.Refs[0].Credential = `{"email":"alice-a","id":"disabled-bob-secret"}`
		}},
		{name: "ambiguous ref still refused", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Refs = append(d.Refs, trafficGroupIdentityRef{Username: "charlie", Tag: "in-a", Credential: "{}"})
		}},
		{name: "shared port owner still refused", change: func(d *trafficGroupIdentityData, _ map[int64]trafficGroupNode) {
			d.Ownership.Relations = []connectionOwnershipRelation{{InboundTag: "in-a", ManagementUsername: "charlie", Source: connectionSourceOwner}}
		}},
		{name: "legacy entitled group external alias still refused", change: func(d *trafficGroupIdentityData, nodes map[int64]trafficGroupNode) {
			d.Refs[0].AssignmentID = 0
			nodes[3] = trafficGroupNode{ID: 3, ServerID: 4, Tag: "in-a"}
			d.Bindings[0].NodeIDs = append(d.Bindings[0].NodeIDs, 3)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, assignment, nodes := disabledIdentityFixture(t)
			if test.change != nil {
				test.change(&data, nodes)
			}
			identity, reason := resolveTrafficGroupIdentity(data, assignment, nodes[1], []int64{1}, nodes)
			if (reason == "") != test.want || (test.want && identity.User != "alice-a") || (!test.want && identity != (serverConnectionIdentity{})) {
				t.Fatalf("identity=%+v reason=%q, want safe=%t", identity, reason, test.want)
			}
		})
	}
}

func TestDisabledNodeOwnerIdentity(t *testing.T) {
	for _, protocol := range []string{"vless", "shadowsocks"} {
		for _, test := range []struct {
			name   string
			change func(*trafficGroupIdentityData, *trafficGroupConfiguredIdentity, *string, *[]string)
			want   bool
		}{
			{name: "own disabled credential", want: true},
			{name: "same primary with metadata drift", want: true, change: func(_ *trafficGroupIdentityData, c *trafficGroupConfiguredIdentity, _ *string, _ *[]string) {
				c.Credential["level"] = float64(2)
			}},
			{name: "different owner", change: func(_ *trafficGroupIdentityData, _ *trafficGroupConfiguredIdentity, username *string, _ *[]string) {
				*username = "alice"
			}},
			{name: "different server", change: func(d *trafficGroupIdentityData, _ *trafficGroupConfiguredIdentity, _ *string, _ *[]string) {
				d.ServerID++
			}},
			{name: "different tag", change: func(d *trafficGroupIdentityData, _ *trafficGroupConfiguredIdentity, _ *string, _ *[]string) {
				d.DisabledCredentials[0].InboundTag = "other-inbound"
			}},
			{name: "different protocol", change: func(d *trafficGroupIdentityData, _ *trafficGroupConfiguredIdentity, _ *string, _ *[]string) {
				d.DisabledCredentials[0].Protocol = "trojan"
			}},
			{name: "another identity using the disabled secret", change: func(_ *trafficGroupIdentityData, c *trafficGroupConfiguredIdentity, _ *string, _ *[]string) {
				c.Identity = "alice-a"
				c.Credential["email"] = "alice-a"
			}},
			{name: "current secret drift", change: func(_ *trafficGroupIdentityData, c *trafficGroupConfiguredIdentity, _ *string, _ *[]string) {
				c.Credential[trafficGroupAuthenticationKey(c.Protocol)] = "unrelated-current"
			}},
			{name: "email only current entry", change: func(_ *trafficGroupIdentityData, c *trafficGroupConfiguredIdentity, _ *string, _ *[]string) {
				delete(c.Credential, trafficGroupAuthenticationKey(c.Protocol))
			}},
			{name: "node original credential drift", change: func(_ *trafficGroupIdentityData, _ *trafficGroupConfiguredIdentity, _ *string, raw *[]string) {
				*raw = []string{`{"id":"wrong-original","password":"server-key:wrong-original"}`}
			}},
			{name: "missing backup", change: func(d *trafficGroupIdentityData, _ *trafficGroupConfiguredIdentity, _ *string, _ *[]string) {
				d.DisabledCredentials = nil
			}},
		} {
			t.Run(protocol+"/"+test.name, func(t *testing.T) {
				data, _, _ := disabledIdentityFixture(t)
				candidate := data.Configured["in-a"][1]
				raw := []string{`{"id":"uuid-bob"}`}
				if protocol == "shadowsocks" {
					candidate.Protocol, candidate.SS2022ServerKey = protocol, "server-key"
					candidate.Credential = map[string]any{"email": "bob-a", "password": "disabled-bob-secret"}
					backup := &data.DisabledCredentials[0]
					backup.Protocol = protocol
					backup.OriginalCredential = map[string]any{"email": "bob-a", "password": "bob-original"}
					backup.DisabledCredential = cloneLifecycleMap(candidate.Credential)
					backup.DisabledHash = hashJSON(candidate.Credential)
					raw = []string{`{"password":"server-key:bob-original"}`}
				}
				username := "bob"
				if test.change != nil {
					test.change(&data, &candidate, &username, &raw)
				}
				identities := matchDisabledNodeProtocolIdentities(data.ServerID, username, "in-a", raw, []trafficGroupConfiguredIdentity{candidate}, data.DisabledCredentials)
				if (len(identities) == 1) != test.want || (test.want && identities[0] != "bob-a") {
					t.Fatalf("identities=%v, want match=%t", identities, test.want)
				}
				if protocol == "shadowsocks" && test.want {
					candidate.SS2022ServerKey = "wrong-server-key"
					if got := matchDisabledNodeProtocolIdentities(data.ServerID, username, "in-a", raw, []trafficGroupConfiguredIdentity{candidate}, data.DisabledCredentials); len(got) != 0 {
						t.Fatalf("wrong SS2022 server key matched disabled owner: %v", got)
					}
				}
			})
		}
	}
}

func TestDisabledIdentityCannotBeAttributedToAnotherRef(t *testing.T) {
	data, _, _ := disabledIdentityFixture(t)
	ref := data.Refs[1]
	if got := data.trafficGroupRefIdentities(ref, data.Configured["in-a"]); len(got) != 1 || got[0] != "bob-a" {
		t.Fatalf("disabled user's original ref did not resolve: %v", got)
	}
	ref.Username = "alice"
	if got := data.trafficGroupRefIdentities(ref, data.Configured["in-a"]); len(got) != 0 {
		t.Fatalf("disabled credential attributed to another user: %v", got)
	}
}
