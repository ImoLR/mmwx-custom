package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ownershipSnapshot(tag string, port uint32, identities ...string) serverDetailedConnectionSnapshot {
	snapshot := serverDetailedConnectionSnapshot{
		Inbounds: []serverInboundConnections{{InboundTag: tag, Port: port, Protocol: "shadowsocks"}},
	}
	for index, identity := range identities {
		snapshot.ProxyUsers = append(snapshot.ProxyUsers, serverProxyUserConnections{
			Identity: serverConnectionIdentity{InboundTag: tag, User: identity}, InboundTag: tag, User: identity,
			InboundPort: port, OutboundActive: int64(index + 1), OutboundNewRate: index + 1,
		})
	}
	return snapshot
}

func TestBindingOverridesOwnerAndMapsAllIdentitiesForSingleBindingUser(t *testing.T) {
	snapshot := ownershipSnapshot("ss-10015", 10015, "base-owner", "ken__ss-10015")
	ownership := connectionOwnershipData{Relations: []connectionOwnershipRelation{
		{InboundTag: "ss-10015", ManagementUsername: "imolr", ProtocolIdentity: "base-owner", Source: connectionSourceOwner},
		{InboundTag: "ss-10015", ManagementUsername: "ken", ProtocolIdentity: "ken__ss-10015", Source: connectionSourceBinding},
	}}
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(view.Users) != 1 || view.Users[0].Username != "ken" || view.Users[0].Source != connectionSourceBinding || len(view.Users[0].Ports) != 1 {
		t.Fatalf("binding did not override owner: %#v", view)
	}
	if len(mappings) != 2 || view.Users[0].Aggregate.OutboundActive != 0 || view.Users[0].Ports[0].Aggregate.OutboundActive != 3 {
		t.Fatalf("single binding mapping/port aggregate = %#v mappings=%#v", view.Users[0], mappings)
	}
}

func TestOwnerFallbackAndSingleSecretPort(t *testing.T) {
	snapshot := ownershipSnapshot("ss-12311", 12311)
	ownership := connectionOwnershipData{Relations: []connectionOwnershipRelation{{InboundTag: "ss-12311", ManagementUsername: "imolr", Source: connectionSourceOwner}}}
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(view.Users) != 1 || view.Users[0].Username != "imolr" || len(mappings) != 1 || mappings[0].Identity.User != "" {
		t.Fatalf("single-secret owner fallback = %#v mappings=%#v", view, mappings)
	}
}

func TestManagementAggregateUsesCoreGroupAcrossMultiplePorts(t *testing.T) {
	snapshot := ownershipSnapshot("in-a", 10015, "proto-a")
	second := ownershipSnapshot("in-b", 10016, "proto-b")
	snapshot.Inbounds = append(snapshot.Inbounds, second.Inbounds...)
	snapshot.ProxyUsers = append(snapshot.ProxyUsers, second.ProxyUsers...)
	snapshot.ManagementGroups = []serverManagementGroupConnections{{Username: "ken", OutboundActive: 18, OutboundPending: 2, InboundActive: 7, CurrentTotal: 27, OutboundNewRate: 4}}
	ownership := connectionOwnershipData{Relations: []connectionOwnershipRelation{
		{InboundTag: "in-a", ManagementUsername: "ken", ProtocolIdentity: "proto-a", Source: connectionSourceBinding},
		{InboundTag: "in-b", ManagementUsername: "ken", ProtocolIdentity: "proto-b", Source: connectionSourceBinding},
	}}
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(view.Users) != 1 || len(view.Users[0].Ports) != 2 || len(mappings) != 2 || view.Users[0].Aggregate.OutboundActive != 18 || view.Users[0].Aggregate.CurrentTotal != 27 {
		t.Fatalf("cross-port management aggregate = view=%#v mappings=%#v", view, mappings)
	}
}

func TestMultipleBindingsUseOnlyExactProtocolIdentities(t *testing.T) {
	snapshot := ownershipSnapshot("shared", 443, "proto-a", "proto-b", "unmatched")
	ownership := connectionOwnershipData{Relations: []connectionOwnershipRelation{
		{InboundTag: "shared", ManagementUsername: "alice", ProtocolIdentity: "proto-a", Source: connectionSourceBinding},
		{InboundTag: "shared", ManagementUsername: "bob", ProtocolIdentity: "proto-b", Source: connectionSourceBinding},
	}}
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(view.Users) != 2 || len(mappings) != 2 || len(view.Warnings) == 0 {
		t.Fatalf("multi-binding exact mapping = %#v mappings=%#v", view, mappings)
	}
	if view.Users[0].Ports[0].Aggregate.OutboundActive != 1 || view.Users[1].Ports[0].Aggregate.OutboundActive != 2 {
		t.Fatalf("multi-binding stats duplicated or misplaced: %#v", view.Users)
	}
}

func TestUnassignedAndManualPriority(t *testing.T) {
	snapshot := ownershipSnapshot("orphan", 10016, "proto")
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), connectionOwnershipData{})
	if len(view.UnassignedPorts) != 1 || len(view.Users) != 0 || len(mappings) != 0 {
		t.Fatalf("orphan inbound was not unassigned: %#v", view)
	}
	manual := connectionOwnershipData{Relations: []connectionOwnershipRelation{{InboundTag: "orphan", ManagementUsername: "alice", Source: connectionSourceManual}}}
	view, mappings = buildManagementView(snapshot, defaultServerConnectionSettings(), manual)
	if len(view.Users) != 1 || view.Users[0].Source != connectionSourceManual || len(mappings) != 1 {
		t.Fatalf("manual assignment not applied: %#v mappings=%#v", view, mappings)
	}
	ownerAndManual := connectionOwnershipData{Relations: append(manual.Relations, connectionOwnershipRelation{InboundTag: "orphan", ManagementUsername: "owner", Source: connectionSourceOwner})}
	view, mappings = buildManagementView(snapshot, defaultServerConnectionSettings(), ownerAndManual)
	if len(view.Users) != 2 || len(mappings) != 1 || mappings[0].Group != "owner" {
		t.Fatalf("owner runtime attribution and additive manual relation = view=%#v mappings=%#v", view, mappings)
	}
	if view.Users[0].Username != "alice" || view.Users[0].Ports[0].RuntimeAttributed || view.Users[0].Ports[0].Aggregate.OutboundActive != 0 || view.Users[1].Username != "owner" || !view.Users[1].Ports[0].RuntimeAttributed || view.Users[1].Ports[0].Aggregate.OutboundActive != 1 {
		t.Fatalf("manual relation duplicated or replaced authoritative runtime stats: %#v", view.Users)
	}
}

func TestManualExactIdentityCanAddASecondUserWithoutStealingOfficialIdentity(t *testing.T) {
	snapshot := ownershipSnapshot("shared", 443, "official-id", "manual-id")
	ownership := connectionOwnershipData{Relations: []connectionOwnershipRelation{
		{InboundTag: "shared", ManagementUsername: "official", ProtocolIdentity: "official-id", Source: connectionSourceBinding},
		{InboundTag: "shared", ManagementUsername: "observer", ProtocolIdentity: "manual-id", Source: connectionSourceManual},
	}}
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(view.Users) != 2 || len(mappings) != 2 {
		t.Fatalf("additive exact relation missing: view=%#v mappings=%#v", view, mappings)
	}
	if mappings[0].Identity.User != "manual-id" || mappings[0].Group != "official" || mappings[1].Identity.User != "official-id" || mappings[1].Group != "official" {
		t.Fatalf("single official binding must remain authoritative for every identity: %#v", mappings)
	}
	if view.Users[0].Username != "observer" || view.Users[0].Ports[0].RuntimeAttributed || view.Users[0].Ports[0].Aggregate.OutboundActive != 0 {
		t.Fatalf("manual relationship fabricated runtime attribution: %#v", view.Users)
	}
}

func TestManualIdentityAssignmentsStayExactAndCanCoverMultipleUsers(t *testing.T) {
	snapshot := ownershipSnapshot("orphan", 10016, "proto-a", "proto-b")
	ownership := connectionOwnershipData{Relations: []connectionOwnershipRelation{
		{InboundTag: "orphan", ManagementUsername: "alice", ProtocolIdentity: "proto-a", Source: connectionSourceManual},
	}}
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(mappings) != 1 || mappings[0].Identity.User != "proto-a" || len(view.UnassignedPorts) != 1 || len(view.UnassignedPorts[0].ProtocolIdentities) != 1 || view.UnassignedPorts[0].ProtocolIdentities[0] != "proto-b" {
		t.Fatalf("partial manual mapping was not exact: view=%#v mappings=%#v", view, mappings)
	}
	ownership.Relations = append(ownership.Relations, connectionOwnershipRelation{InboundTag: "orphan", ManagementUsername: "bob", ProtocolIdentity: "proto-b", Source: connectionSourceManual})
	view, mappings = buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(mappings) != 2 || len(view.Users) != 2 || len(view.UnassignedPorts) != 0 || view.Users[0].Ports[0].Aggregate.OutboundActive != 1 || view.Users[1].Ports[0].Aggregate.OutboundActive != 2 {
		t.Fatalf("multi-user exact manual mapping = view=%#v mappings=%#v", view, mappings)
	}
}

func TestProtocolIdentityIsNeverMatchedByManagementUsername(t *testing.T) {
	snapshot := ownershipSnapshot("shared", 443, "alice", "bob")
	ownership := connectionOwnershipData{Relations: []connectionOwnershipRelation{
		{InboundTag: "shared", ManagementUsername: "alice", Source: connectionSourceOwner},
		{InboundTag: "shared", ManagementUsername: "bob", Source: connectionSourceOwner},
	}}
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(mappings) != 0 || len(view.Warnings) == 0 {
		t.Fatalf("management names were guessed as protocol identities: %#v mappings=%#v", view, mappings)
	}
}

func TestAmbiguousExactIdentityIsNotAssignedToFirstUser(t *testing.T) {
	snapshot := ownershipSnapshot("shared", 443, "same-identity")
	ownership := connectionOwnershipData{Relations: []connectionOwnershipRelation{
		{InboundTag: "shared", ManagementUsername: "alice", ProtocolIdentity: "same-identity", Source: connectionSourceBinding},
		{InboundTag: "shared", ManagementUsername: "bob", ProtocolIdentity: "same-identity", Source: connectionSourceBinding},
	}}
	view, mappings := buildManagementView(snapshot, defaultServerConnectionSettings(), ownership)
	if len(mappings) != 0 || len(view.Warnings) == 0 || view.Users[0].Ports[0].Aggregate.OutboundActive != 0 || view.Users[1].Ports[0].Aggregate.OutboundActive != 0 {
		t.Fatalf("ambiguous exact identity was falsely attributed: view=%#v mappings=%#v", view, mappings)
	}
}

func TestExactCredentialExtractionDoesNotExposeOrUseEmailAsSecret(t *testing.T) {
	raw := `{"email":"proto-a","password":"secret-value"}`
	if identity := extractProtocolIdentity(raw); identity != "proto-a" {
		t.Fatalf("identity = %q", identity)
	}
	secrets := credentialSecrets(map[string]any{"email": "proto-a", "password": "secret-value"})
	if _, exists := secrets["proto-a"]; exists {
		t.Fatal("email was treated as a credential secret")
	}
	if _, exists := secrets["secret-value"]; !exists {
		t.Fatal("credential secret was not available for exact matching")
	}
}

func TestConnectionOwnershipSchemaPersistsNormalizedManualRelation(t *testing.T) {
	for _, fragment := range []string{"server_id", "inbound_tag", "management_username", "protocol_identity", "source", "assignment_type", "created_at", "updated_at", "PRIMARY KEY", "FOREIGN KEY", "UNIQUE INDEX"} {
		if !strings.Contains(connectionOwnershipSchema, fragment) {
			t.Fatalf("persistent manual assignment schema missing %s", fragment)
		}
	}
	if !strings.Contains(connectionOwnershipSchema, "WHERE protocol_identity <> ''") {
		t.Fatal("tag-only multi-user relations must not be blocked by the exact-identity unique index")
	}
}

type fakePersistentConnectionStore struct {
	fakeAdminSessionStore
	data connectionOwnershipData
}

func (s *fakePersistentConnectionStore) EnsureConnectionOwnershipSchema(context.Context) error {
	return nil
}
func (s *fakePersistentConnectionStore) ConnectionOwnership(context.Context, string) (connectionOwnershipData, error) {
	return s.data, nil
}
func (s *fakePersistentConnectionStore) SaveManualConnectionAssignment(_ context.Context, _ string, relation connectionOwnershipRelation) error {
	s.data.Relations = append(s.data.Relations, relation)
	return nil
}
func (s *fakePersistentConnectionStore) DeleteManualConnectionAssignment(_ context.Context, _ string, relation connectionOwnershipRelation) error {
	kept := s.data.Relations[:0]
	for _, item := range s.data.Relations {
		if item.InboundTag != relation.InboundTag || item.ManagementUsername != relation.ManagementUsername || item.ProtocolIdentity != relation.ProtocolIdentity || item.Source != connectionSourceManual {
			kept = append(kept, item)
		}
	}
	s.data.Relations = kept
	return nil
}

func TestManualAssignmentEndpointPersistsAcrossAppRecreation(t *testing.T) {
	store := &fakePersistentConnectionStore{fakeAdminSessionStore: fakeAdminSessionStore{authorized: true, server: true}, data: connectionOwnershipData{ManagementUsers: []string{"alice"}}}
	state, err := openHelperState(filepath.Join(t.TempDir(), "state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	record := serverDetailedConnectionRecord{Snapshot: ownershipSnapshot("orphan", 10016, "proto-a")}
	makeApp := func() *app {
		return &app{apiToken: "operator-token", adminStore: store, helperState: state, detailedConnections: map[string]serverDetailedConnectionRecord{"12": record}}
	}
	request := httptest.NewRequest(http.MethodPut, "/api/custom/servers/12/connection-assignments", bytes.NewBufferString(`{"inbound_tag":"orphan","management_username":"alice"}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	makeApp().connectionAssignmentsHandler(response, request, "12")
	if response.Code != http.StatusOK {
		t.Fatalf("assignment failed: %d %s", response.Code, response.Body.String())
	}
	view, mappings := buildManagementView(record.Snapshot, defaultServerConnectionSettings(), mustOwnership(t, makeApp(), "12"))
	if len(view.Users) != 1 || view.Users[0].Username != "alice" || len(mappings) != 1 {
		t.Fatalf("assignment did not survive app recreation: view=%#v mappings=%#v", view, mappings)
	}
}

func TestManualAssignmentEndpointAllowsAddingRelationshipToOwnedPort(t *testing.T) {
	store := &fakePersistentConnectionStore{fakeAdminSessionStore: fakeAdminSessionStore{authorized: true, server: true}, data: connectionOwnershipData{
		ManagementUsers: []string{"owner", "observer"},
		Relations:       []connectionOwnershipRelation{{InboundTag: "owned", ManagementUsername: "owner", Source: connectionSourceOwner}},
	}}
	state, err := openHelperState(filepath.Join(t.TempDir(), "state.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	record := serverDetailedConnectionRecord{Snapshot: ownershipSnapshot("owned", 10015, "proto-a")}
	app := &app{apiToken: "operator-token", adminStore: store, helperState: state, detailedConnections: map[string]serverDetailedConnectionRecord{"5": record}}
	request := httptest.NewRequest(http.MethodPut, "/api/custom/servers/5/connection-assignments", bytes.NewBufferString(`{"inbound_tag":"owned","management_username":"observer"}`))
	request.Header.Set("Authorization", "Bearer operator-token")
	response := httptest.NewRecorder()
	app.connectionAssignmentsHandler(response, request, "5")
	if response.Code != http.StatusOK {
		t.Fatalf("additive assignment failed: %d %s", response.Code, response.Body.String())
	}
	view, mappings := buildManagementView(record.Snapshot, defaultServerConnectionSettings(), mustOwnership(t, app, "5"))
	if len(view.Users) != 2 || len(mappings) != 1 || mappings[0].Group != "owner" {
		t.Fatalf("additive assignment changed runtime owner: view=%#v mappings=%#v", view, mappings)
	}
}

func mustOwnership(t *testing.T, app *app, serverID string) connectionOwnershipData {
	t.Helper()
	value, err := app.connectionOwnership(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
