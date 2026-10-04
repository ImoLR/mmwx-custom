package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/curve25519"
)

type lifecycleTestStore struct {
	mu              sync.Mutex
	preview         managedUserDeletionPreview
	refs            []lifecycleCredentialRef
	businessRefs    map[string][]lifecycleCredentialRef
	defaults        map[string][]map[string]any
	packages        []lifecyclePackageBinding
	deletionData    lifecycleDeletionData
	deletedPackages []int64
	operationID     string
	savedPlans      [][]lifecyclePlanItem
	marked          []lifecyclePlanItem
	finishedPending int
	finalized       bool
	finalizeErr     error
	backups         []lifecycleCredentialBackup
	accessOperation string
}

func (s *lifecycleTestStore) AuthorizeAdmin(context.Context, string) (bool, error) { return true, nil }
func (s *lifecycleTestStore) RemoteServerExists(context.Context, string) (bool, error) {
	return true, nil
}
func (s *lifecycleTestStore) Close() error { return nil }
func (s *lifecycleTestStore) ManagedUserState(_ context.Context, username string) (managedUserState, error) {
	return managedUserState{Username: username, Exists: !s.finalized, Role: "user", IsActive: true}, nil
}
func (s *lifecycleTestStore) ManagedUserDeletionPreview(_ context.Context, username string) (managedUserDeletionPreview, error) {
	preview := s.preview
	preview.Username = username
	if preview.Role == "" {
		preview.Role = "user"
	}
	preview.Exists = true
	return preview, nil
}
func (s *lifecycleTestStore) LifecycleStates(context.Context) (map[string]managedUserLifecycle, error) {
	return map[string]managedUserLifecycle{}, nil
}
func (s *lifecycleTestStore) LifecycleCredentialRefs(context.Context, string) ([]lifecycleCredentialRef, error) {
	return append([]lifecycleCredentialRef(nil), s.refs...), nil
}
func (s *lifecycleTestStore) LifecycleDisabledCredentials(context.Context, string) ([]lifecycleCredentialBackup, error) {
	return append([]lifecycleCredentialBackup(nil), s.backups...), nil
}
func (s *lifecycleTestStore) LatestAccessOperation(context.Context, string, string) (string, error) {
	return s.accessOperation, nil
}
func (s *lifecycleTestStore) SaveAccessPlan(_ context.Context, _, _, operationID string, items []lifecyclePlanItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessOperation = operationID
	s.savedPlans = append(s.savedPlans, append([]lifecyclePlanItem(nil), items...))
	for _, item := range items {
		for _, backup := range item.accessCredentials {
			found := false
			for index := range s.backups {
				if s.backups[index].ServerID == backup.ServerID && s.backups[index].InboundTag == backup.InboundTag && s.backups[index].CredentialKey == backup.CredentialKey {
					s.backups[index] = backup
					found = true
				}
			}
			if !found {
				s.backups = append(s.backups, backup)
			}
		}
	}
	return nil
}
func (s *lifecycleTestStore) FinishAccessAttempt(_ context.Context, _, _, operation string, pending int, _ string) error {
	s.finishedPending = pending
	if operation == lifecycleOperationEnable && pending == 0 {
		s.backups = nil
	}
	return nil
}
func (s *lifecycleTestStore) LifecycleInboundBusinessRefs(_ context.Context, serverID int64, _ string, tag, _ string) ([]lifecycleCredentialRef, error) {
	return append([]lifecycleCredentialRef(nil), s.businessRefs[fmt.Sprintf("%d/%s", serverID, tag)]...), nil
}
func (s *lifecycleTestStore) LifecycleDefaultAdminCredentials(_ context.Context, serverID int64, tag string) ([]map[string]any, error) {
	return append([]map[string]any(nil), s.defaults[fmt.Sprintf("%d/%s", serverID, tag)]...), nil
}
func (s *lifecycleTestStore) LifecyclePackageBindings(context.Context, string) ([]lifecyclePackageBinding, error) {
	return append([]lifecyclePackageBinding(nil), s.packages...), nil
}
func (s *lifecycleTestStore) DeleteExclusivePackage(_ context.Context, packageID int64, _ string, _ lifecyclePackageRecheck) error {
	s.deletedPackages = append(s.deletedPackages, packageID)
	return nil
}
func (s *lifecycleTestStore) LatestDeleteOperation(context.Context, string) (string, error) {
	return s.operationID, nil
}
func (s *lifecycleTestStore) SaveDeletePlan(_ context.Context, _, operationID string, items []lifecyclePlanItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operationID = operationID
	s.savedPlans = append(s.savedPlans, append([]lifecyclePlanItem(nil), items...))
	return nil
}
func (s *lifecycleTestStore) MarkLifecycleItem(_ context.Context, _ string, item lifecyclePlanItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marked = append(s.marked, item)
	return nil
}
func (s *lifecycleTestStore) FinishDeleteAttempt(_ context.Context, _, _ string, pending int, _ string) error {
	s.finishedPending = pending
	return nil
}
func (s *lifecycleTestStore) FinalizeManagementUserDeletion(context.Context, string, string) error {
	if s.finalizeErr != nil {
		return s.finalizeErr
	}
	s.finalized = true
	return nil
}

type lifecycleAgentFixture struct {
	mu                sync.Mutex
	configs           map[int64]map[string]any
	actions           []map[string]any
	sessions          map[string]*officialSecureChannel
	runtimePrivateKey []byte
	failTagOnce       string
	failed            bool
	ignoreMutations   bool
	extraHandler      func(http.ResponseWriter, *http.Request) bool
	removeNodes       func(int64, string) error
}

func newLifecycleAgentFixture(configs map[int64]map[string]any) (*lifecycleAgentFixture, *httptest.Server) {
	runtimePrivateKey := []byte{
		0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48,
		0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50,
		0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58,
		0x59, 0x5a, 0x5b, 0x5c, 0x5d, 0x5e, 0x5f, 0x60,
	}
	runtimePublicKey, err := curve25519.X25519(runtimePrivateKey, curve25519.Basepoint)
	if err != nil {
		panic(err)
	}
	copy(officialSecureRuntimePublicKey[:], runtimePublicKey)
	fixture := &lifecycleAgentFixture{
		configs: configs, sessions: make(map[string]*officialSecureChannel), runtimePrivateKey: runtimePrivateKey,
	}
	server := httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	return fixture, server
}

func (f *lifecycleAgentFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/api/securechan/handshake" {
		f.handshake(w, r)
		return
	}
	if r.Header.Get("X-Secure-Channel") != officialSecureChannelVersion {
		writeJSON(w, http.StatusForbidden, map[string]any{"code": "SECURE_CHANNEL_REQUIRED"})
		return
	}
	f.mu.Lock()
	channel := f.sessions[r.Header.Get("X-Session-Id")]
	f.mu.Unlock()
	if channel == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]any{"code": "session_expired"})
		return
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		envelope, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			http.Error(w, "decode body", http.StatusBadRequest)
			return
		}
		plaintext, err := channel.decrypt(envelope)
		if err != nil {
			http.Error(w, "decrypt body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(plaintext)))
		r.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	f.servePlain(recorder, r)
	w.Header().Set("X-Secure-Channel", officialSecureChannelVersion)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(recorder.Code)
	_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(channel.encrypt(recorder.Body.Bytes()))))
}

func (f *lifecycleAgentFixture) handshake(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ClientPublicKey string `json:"client_pub_b64"`
		Audience        string `json:"audience"`
		Proto           string `json:"proto"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil || request.Proto != "v2" || request.Audience == "" {
		http.Error(w, "invalid handshake", http.StatusBadRequest)
		return
	}
	clientPublicKey, err := base64.StdEncoding.DecodeString(request.ClientPublicKey)
	if err != nil || len(clientPublicKey) != curve25519.PointSize {
		http.Error(w, "invalid client key", http.StatusBadRequest)
		return
	}
	serverPrivateKey := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(serverPrivateKey); err != nil {
		http.Error(w, "key generation", http.StatusInternalServerError)
		return
	}
	serverPublicKey, err := curve25519.X25519(serverPrivateKey, curve25519.Basepoint)
	if err != nil {
		http.Error(w, "key generation", http.StatusInternalServerError)
		return
	}
	sharedSecret, err := curve25519.X25519(serverPrivateKey, clientPublicKey)
	if err != nil {
		http.Error(w, "key agreement", http.StatusBadRequest)
		return
	}
	runtimeSharedSecret, err := curve25519.X25519(f.runtimePrivateKey, clientPublicKey)
	if err != nil {
		http.Error(w, "runtime key agreement", http.StatusBadRequest)
		return
	}
	keyMaterial := append(append(make([]byte, 0, len(sharedSecret)+len(runtimeSharedSecret)), sharedSecret...), runtimeSharedSecret...)
	sessionID := fmt.Sprintf("test-session-%d", time.Now().UnixNano())
	info := []byte("securechan-v2\n" + sessionID)
	channel, err := deriveOfficialSecureChannel(sessionID, keyMaterial, clientPublicKey, serverPublicKey, info, true)
	if err != nil {
		http.Error(w, "key derivation", http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	f.sessions[sessionID] = channel
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"proto":          "v2",
		"session_id":     sessionID,
		"server_pub_b64": base64.StdEncoding.EncodeToString(serverPublicKey),
		"runtime_proof":  "test-runtime-proof",
	})
}

func (f *lifecycleAgentFixture) servePlain(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.extraHandler != nil && f.extraHandler(w, r) {
		return
	}
	serverID, _ := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
	if r.Method == http.MethodGet && r.URL.Path == "/api/admin/remote/xray/config" {
		raw, _ := json.Marshal(f.configs[serverID])
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": string(raw)})
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/admin/remote/inbounds" {
		http.NotFound(w, r)
		return
	}
	var action map[string]any
	if json.NewDecoder(r.Body).Decode(&action) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false})
		return
	}
	f.actions = append(f.actions, action)
	tag := strings.TrimSpace(fmt.Sprint(action["tag"]))
	if tag == f.failTagOnce && !f.failed {
		f.failed = true
		writeJSON(w, http.StatusBadGateway, map[string]any{"success": false})
		return
	}
	if !f.ignoreMutations {
		f.apply(serverID, action)
		if action["action"] == "remove" && f.removeNodes != nil {
			if err := f.removeNodes(serverID, tag); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false})
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (f *lifecycleAgentFixture) apply(serverID int64, action map[string]any) {
	config := f.configs[serverID]
	inbounds, _ := config["inbounds"].([]any)
	tag := strings.TrimSpace(fmt.Sprint(action["tag"]))
	if action["action"] == "remove" {
		filtered := inbounds[:0:0]
		for _, raw := range inbounds {
			inbound, _ := raw.(map[string]any)
			if fmt.Sprint(inbound["tag"]) != tag {
				filtered = append(filtered, raw)
			}
		}
		config["inbounds"] = filtered
		return
	}
	if action["action"] == "replace" {
		replacement, _ := action["inbound"].(map[string]any)
		for index, raw := range inbounds {
			inbound, _ := raw.(map[string]any)
			if fmt.Sprint(inbound["tag"]) == tag {
				inbounds[index] = replacement
				config["inbounds"] = inbounds
				return
			}
		}
		return
	}
	credential, _ := action["client"].(map[string]any)
	for _, raw := range inbounds {
		inbound, _ := raw.(map[string]any)
		if fmt.Sprint(inbound["tag"]) != tag {
			continue
		}
		entries, key, _ := inboundCredentialEntries(inbound)
		filtered := make([]any, 0, len(entries))
		for _, entry := range entries {
			if !credentialsMatch(entry, credential, fmt.Sprint(inbound["protocol"])) {
				filtered = append(filtered, entry)
			}
		}
		inbound["settings"].(map[string]any)[key] = filtered
	}
}

func lifecycleConfig(inbounds ...map[string]any) map[string]any {
	items := make([]any, 0, len(inbounds))
	for _, inbound := range inbounds {
		items = append(items, inbound)
	}
	return map[string]any{"inbounds": items}
}

func lifecycleInbound(tag, protocol string, credentials ...map[string]any) map[string]any {
	key := "clients"
	if protocol == "snell" || protocol == "mieru" || protocol == "anytls" {
		key = "users"
	}
	items := make([]any, 0, len(credentials))
	for _, credential := range credentials {
		items = append(items, credential)
	}
	return map[string]any{"tag": tag, "protocol": protocol, "settings": map[string]any{key: items}}
}

func lifecycleRef(serverID int64, tag, protocol string, credential map[string]any) lifecycleCredentialRef {
	raw, _ := json.Marshal(credential)
	return lifecycleCredentialRef{ServerID: serverID, ServerName: fmt.Sprintf("server-%d", serverID), InboundTag: tag, Protocol: protocol, CredentialRaw: string(raw), Source: "user_inbound_configs"}
}

func lifecycleTestApp(t *testing.T, store *lifecycleTestStore, server *httptest.Server) *app {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &app{adminStore: store, officialInternalTarget: target, userLifecycleLocks: make(map[string]*sync.Mutex)}
}

func TestAnalyzeLifecycleInboundChoosesSharedAndExclusiveActions(t *testing.T) {
	alice := map[string]any{"email": "alice__tag", "psk": "alice-secret"}
	bob := map[string]any{"email": "bob__tag", "psk": "bob-secret"}
	bobRef := lifecycleRef(5, "tag", "snell", bob)
	bobRef.Username = "bob"
	shared := analyzeLifecycleInbound(lifecycleConfig(lifecycleInbound("tag", "snell", alice, bob)), []lifecycleCredentialRef{lifecycleRef(5, "tag", "snell", alice)}, []lifecycleCredentialRef{bobRef}, nil)
	if shared.Action != lifecycleActionRemoveUser || shared.RemainingUsers != 1 || len(shared.nonTargetHashes) != 1 {
		t.Fatalf("unexpected shared plan: %#v", shared)
	}
	exclusive := analyzeLifecycleInbound(lifecycleConfig(lifecycleInbound("tag", "snell", alice)), []lifecycleCredentialRef{lifecycleRef(5, "tag", "snell", alice)}, nil, nil)
	if exclusive.Action != lifecycleActionDeleteWhole || exclusive.Status != lifecycleItemPending {
		t.Fatalf("exclusive Snell must delete whole inbound: %#v", exclusive)
	}
	admin := map[string]any{"email": "admin@example.com", "psk": "admin-default"}
	defaultOnly := analyzeLifecycleInbound(lifecycleConfig(lifecycleInbound("tag", "snell", admin, alice)), []lifecycleCredentialRef{lifecycleRef(5, "tag", "snell", alice)}, nil, []map[string]any{admin})
	if defaultOnly.Action != lifecycleActionDeleteWhole || defaultOnly.DefaultCredentials != 1 || defaultOnly.UnknownCredentials != 0 {
		t.Fatalf("default administrator must not make inbound shared: %#v", defaultOnly)
	}
	unknown := map[string]any{"email": "unknown@example.com", "psk": "unknown"}
	conflict := analyzeLifecycleInbound(lifecycleConfig(lifecycleInbound("tag", "snell", alice, unknown)), []lifecycleCredentialRef{lifecycleRef(5, "tag", "snell", alice)}, nil, nil)
	if conflict.Action != lifecycleActionConflict || conflict.UnknownCredentials != 1 || conflict.Status != lifecycleItemFailed {
		t.Fatalf("unknown runtime credential must conflict: %#v", conflict)
	}
	missingRef := lifecycleRef(5, "tag", "snell", bob)
	missingRef.Username = "bob"
	missingRuntime := analyzeLifecycleInbound(lifecycleConfig(lifecycleInbound("tag", "snell", alice)), []lifecycleCredentialRef{lifecycleRef(5, "tag", "snell", alice)}, []lifecycleCredentialRef{missingRef}, nil)
	if missingRuntime.Action != lifecycleActionConflict || missingRuntime.Status != lifecycleItemFailed || !strings.Contains(missingRuntime.LastError, "runtime credential") {
		t.Fatalf("missing explicit business credential must conflict: %#v", missingRuntime)
	}
}

func TestDeleteLifecycleNoInboundFinalizesUser(t *testing.T) {
	store := &lifecycleTestStore{}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	result := application.executeDeletePlan(context.Background(), "session", "alice", "op-1", nil)
	if !result.UserDeleted || !store.finalized || result.PendingCount != 0 || len(fixture.actions) != 0 {
		t.Fatalf("unexpected no-inbound result: %#v finalized=%t actions=%d", result, store.finalized, len(fixture.actions))
	}
}

func TestDeleteLifecycleDeletesOnlyExclusivePackages(t *testing.T) {
	store := &lifecycleTestStore{packages: []lifecyclePackageBinding{
		{ID: 11, Name: "exclusive", NodeIDs: []int64{65, 73, 74}, Bound: true},
		{ID: 12, Name: "shared", NodeIDs: []int64{80}, Bound: true, BindingConflict: true},
	}}
	_, server := newLifecycleAgentFixture(map[int64]map[string]any{})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	plan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil || len(plan) != 2 {
		t.Fatalf("package plan=%#v err=%v", plan, err)
	}
	if plan[0].Action != lifecycleActionDeletePackage || plan[1].Action != lifecycleActionConflict {
		t.Fatalf("unexpected package decisions: %#v", plan)
	}
	result := application.executeDeletePlan(context.Background(), "session", "alice", "op-packages", plan)
	if result.UserDeleted || result.PendingCount != 1 || len(store.deletedPackages) != 1 || store.deletedPackages[0] != 11 {
		t.Fatalf("exclusive package handling failed: result=%#v deleted=%v", result, store.deletedPackages)
	}
}

func TestDeleteLifecyclePlanOwnedExternalNodesAndSharedServerNode(t *testing.T) {
	alice := lifecycleRef(5, "shared", "vless", map[string]any{"id": "alice-secret"})
	alice.Username = "alice"
	bob := lifecycleRef(5, "shared", "vless", map[string]any{"id": "bob-secret"})
	bob.Username = "bob"
	store := &lifecycleTestStore{
		refs: []lifecycleCredentialRef{alice}, businessRefs: map[string][]lifecycleCredentialRef{"5/shared": {bob}},
		packages: []lifecyclePackageBinding{{ID: 1, Bound: true, NodeIDs: []int64{10, 11, 12}}},
		deletionData: lifecycleDeletionData{
			Nodes: map[int64]lifecycleDeletionNode{
				10: {lifecycleNodeLabel: lifecycleNodeLabel{ID: 10, Name: "Alice external"}, Username: "alice"},
				11: {lifecycleNodeLabel: lifecycleNodeLabel{ID: 11, Name: "Admin external"}, Username: "admin"},
				12: {lifecycleNodeLabel: lifecycleNodeLabel{ID: 12, Name: "Shared server node"}, Username: "alice", ServerID: 5, ServerName: "server-5", InboundTag: "shared"},
			},
			Refs: []lifecycleCredentialRef{alice, bob},
		},
	}
	_, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("shared", "vless", map[string]any{"id": "alice-secret"}, map[string]any{"id": "bob-secret"}))})
	defer server.Close()
	plan, err := lifecycleTestApp(t, store, server).buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil || len(plan) != 3 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for _, item := range plan {
		switch item.ItemKind {
		case lifecycleItemKindNode:
			if item.Action != lifecycleActionDeleteNode || len(item.NodeIDs) != 1 || item.NodeIDs[0] != 10 || len(item.OwnNodes) != 1 || item.OwnNodes[0].ID != 10 {
				t.Fatalf("external node deletion=%+v", item)
			}
		case lifecycleItemKindInbound:
			if item.Action != lifecycleActionRemoveUser || len(item.NodeIDs) != 0 || !strings.Contains(item.DecisionNote, "保留该用户名下的服务器节点：Shared server node（ID 12）") {
				t.Fatalf("owned shared server node must be preserved: %+v", item)
			}
		case lifecycleItemKindPackage:
			if item.Action != lifecycleActionKeepPackage || len(item.OwnNodes) != 1 || item.OwnNodes[0].ID != 10 || len(item.DeletedNodeIDs) != 1 || item.DeletedNodeIDs[0] != 10 ||
				len(item.NeutralNodes) != 1 || item.NeutralNodes[0].ID != 11 || len(item.OtherUserNodes) != 1 || item.OtherUserNodes[0].ID != 12 {
				t.Fatalf("node ownership classification=%+v", item)
			}
		}
	}
}

func TestDeleteLifecycleSharedPreservesOtherCredentialAndExclusiveSnellUsesWholeRemove(t *testing.T) {
	aliceShared := map[string]any{"email": "alice__shared", "password": "alice-pass"}
	bobShared := map[string]any{"email": "bob__shared", "password": "bob-pass"}
	aliceSnell := map[string]any{"email": "alice__solo", "psk": "alice-psk"}
	bobRef := lifecycleRef(5, "shared", "shadowsocks", bobShared)
	bobRef.Username = "bob"
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{
		lifecycleRef(5, "shared", "shadowsocks", aliceShared),
		lifecycleRef(5, "solo", "snell", aliceSnell),
	}, businessRefs: map[string][]lifecycleCredentialRef{"5/shared": {bobRef}}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(
		lifecycleInbound("shared", "shadowsocks", aliceShared, bobShared),
		lifecycleInbound("solo", "snell", aliceSnell),
	)})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	plan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDeletePlan(context.Background(), "alice", "op-1", plan); err != nil {
		t.Fatal(err)
	}
	result := application.executeDeletePlan(context.Background(), "session", "alice", "op-1", plan)
	if !result.UserDeleted || !store.finalized {
		t.Fatalf("deletion did not finalize: %#v", result)
	}
	if len(fixture.actions) != 2 || fixture.actions[0]["action"] != "remove-client" || fixture.actions[1]["action"] != "remove" {
		t.Fatalf("unexpected action sequence: %#v", fixture.actions)
	}
	shared := findConfigInbound(fixture.configs[5], "shared")
	entries, _, err := inboundCredentialEntries(shared)
	if err != nil || len(entries) != 1 || !credentialsMatch(entries[0], bobShared, "shadowsocks") {
		t.Fatalf("shared user was not preserved: entries=%#v err=%v", entries, err)
	}
	if findConfigInbound(fixture.configs[5], "solo") != nil {
		t.Fatal("exclusive Snell inbound still exists")
	}
}

func TestDeleteLifecyclePartialFailurePersistsSuccessAndRetrySkipsCompleted(t *testing.T) {
	aliceA := map[string]any{"email": "alice__a", "id": "alice-a"}
	aliceB := map[string]any{"email": "alice__b", "id": "alice-b"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{
		lifecycleRef(5, "a", "vless", aliceA),
		lifecycleRef(5, "b", "vless", aliceB),
	}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(
		lifecycleInbound("a", "vless", aliceA), lifecycleInbound("b", "vless", aliceB),
	)})
	fixture.failTagOnce = "b"
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	firstPlan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SaveDeletePlan(context.Background(), "alice", "op-retry", firstPlan)
	first := application.executeDeletePlan(context.Background(), "session", "alice", "op-retry", firstPlan)
	if first.UserDeleted || first.PendingCount != 1 || findConfigInbound(fixture.configs[5], "a") != nil || findConfigInbound(fixture.configs[5], "b") == nil {
		t.Fatalf("unexpected partial result: %#v", first)
	}
	actionsAfterFirst := len(fixture.actions)
	secondPlan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SaveDeletePlan(context.Background(), "alice", "op-retry", secondPlan)
	restartedApplication := lifecycleTestApp(t, store, server)
	second := restartedApplication.executeDeletePlan(context.Background(), "session", "alice", "op-retry", secondPlan)
	if !second.UserDeleted || second.PendingCount != 0 || len(fixture.actions) != actionsAfterFirst+1 {
		t.Fatalf("retry did not target only pending item: %#v actions=%#v", second, fixture.actions)
	}
}

func TestDeleteLifecycleStopsWhenExclusiveInboundGainsAnotherCredential(t *testing.T) {
	alice := map[string]any{"email": "shared-email", "id": "alice-id"}
	bob := map[string]any{"email": "shared-email", "id": "bob-id"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "tag", "vless", alice)}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("tag", "vless", alice))})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	plan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil || len(plan) != 1 || plan[0].Action != lifecycleActionDeleteWhole {
		t.Fatalf("unexpected initial plan: %#v err=%v", plan, err)
	}
	fixture.configs[5] = lifecycleConfig(lifecycleInbound("tag", "vless", alice, bob))
	_ = store.SaveDeletePlan(context.Background(), "alice", "op-drift", plan)
	result := application.executeDeletePlan(context.Background(), "session", "alice", "op-drift", plan)
	if result.UserDeleted || result.PendingCount != 1 || len(fixture.actions) != 0 || !strings.Contains(result.LastError, "发生变化") {
		t.Fatalf("concurrent credential drift was not blocked: %#v actions=%#v", result, fixture.actions)
	}
	if credentialsMatch(alice, bob, "vless") {
		t.Fatal("same email must not override distinct protocol primary credentials")
	}
}

func TestDeleteLifecycleRejectsHTTP200WithoutRealConfigChange(t *testing.T) {
	alice := map[string]any{"email": "alice__tag", "id": "alice-id"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(15, "tag", "vless", alice)}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{15: lifecycleConfig(lifecycleInbound("tag", "vless", alice))})
	fixture.ignoreMutations = true
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	plan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SaveDeletePlan(context.Background(), "alice", "op-no-change", plan)
	result := application.executeDeletePlan(context.Background(), "session", "alice", "op-no-change", plan)
	if result.UserDeleted || result.PendingCount != 1 || store.finalized || !strings.Contains(result.LastError, "仍存在") {
		t.Fatalf("unchanged HTTP 200 must fail verification: %#v", result)
	}
}

func TestDeleteLifecycleDatabaseFinalizeFailureKeepsUserPending(t *testing.T) {
	store := &lifecycleTestStore{finalizeErr: errors.New("database unavailable")}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	result := application.executeDeletePlan(context.Background(), "session", "alice", "op-db", nil)
	if result.UserDeleted || result.PendingCount != 1 || result.State != lifecycleStateDeletePartial || store.finalized {
		t.Fatalf("database failure must retain user: %#v", result)
	}
	_ = fixture
}

func TestAccessLifecycleSharedAndExclusiveSnellDisableEnable(t *testing.T) {
	aliceShared := map[string]any{"email": "alice__shared", "psk": "alice-shared"}
	bobShared := map[string]any{"email": "bob__shared", "psk": "bob-shared"}
	aliceSolo := map[string]any{"email": "alice__solo", "psk": "alice-solo"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{
		lifecycleRef(5, "shared", "snell", aliceShared),
		lifecycleRef(5, "solo", "snell", aliceSolo),
	}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(
		lifecycleInbound("shared", "snell", aliceShared, bobShared),
		lifecycleInbound("solo", "snell", aliceSolo),
	)})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	disablePlan, err := application.buildAccessPlan(context.Background(), "session", "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationDisable, "disable-1", disablePlan); err != nil {
		t.Fatal(err)
	}
	disabled := application.executeAccessPlan(context.Background(), "session", "alice", "disable-1", lifecycleOperationDisable, disablePlan)
	if disabled.PendingCount != 0 || disabled.State != lifecycleStateDisabled || len(fixture.actions) != 2 || len(store.backups) != 2 {
		t.Fatalf("disable failed: %#v actions=%#v backups=%d", disabled, fixture.actions, len(store.backups))
	}
	sharedEntries, _, _ := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "shared"))
	soloEntries, _, _ := inboundCredentialEntries(findConfigInbound(fixture.configs[5], "solo"))
	if len(sharedEntries) != 2 || len(soloEntries) != 1 || credentialsMatch(sharedEntries[0], aliceShared, "snell") || !credentialsMatch(sharedEntries[1], bobShared, "snell") || credentialsMatch(soloEntries[0], aliceSolo, "snell") {
		t.Fatalf("disable did not preserve non-empty shared/exclusive Snell: shared=%#v solo=%#v", sharedEntries, soloEntries)
	}
	enablePlan, err := application.buildAccessPlan(context.Background(), "session", "alice", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationEnable, "enable-1", enablePlan); err != nil {
		t.Fatal(err)
	}
	enabled := application.executeAccessPlan(context.Background(), "session", "alice", "enable-1", lifecycleOperationEnable, enablePlan)
	if enabled.PendingCount != 0 || enabled.State != lifecycleStateEnabled {
		t.Fatalf("enable failed: %#v", enabled)
	}
	sharedEntries, _, _ = inboundCredentialEntries(findConfigInbound(fixture.configs[5], "shared"))
	soloEntries, _, _ = inboundCredentialEntries(findConfigInbound(fixture.configs[5], "solo"))
	if !credentialsMatch(sharedEntries[0], aliceShared, "snell") || !credentialsMatch(sharedEntries[1], bobShared, "snell") || !credentialsMatch(soloEntries[0], aliceSolo, "snell") {
		t.Fatalf("enable did not restore credentials: shared=%#v solo=%#v", sharedEntries, soloEntries)
	}
}

func TestAccessLifecyclePartialRetrySkipsCompleted(t *testing.T) {
	aliceA := map[string]any{"email": "alice__a", "id": "11111111-1111-4111-8111-111111111111"}
	aliceB := map[string]any{"email": "alice__b", "id": "22222222-2222-4222-8222-222222222222"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{
		lifecycleRef(5, "a", "vless", aliceA), lifecycleRef(5, "b", "vless", aliceB),
	}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(
		lifecycleInbound("a", "vless", aliceA), lifecycleInbound("b", "vless", aliceB),
	)})
	fixture.failTagOnce = "b"
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	firstPlan, err := application.buildAccessPlan(context.Background(), "session", "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationDisable, "disable-retry", firstPlan)
	first := application.executeAccessPlan(context.Background(), "session", "alice", "disable-retry", lifecycleOperationDisable, firstPlan)
	if first.PendingCount != 1 || credentialsMatch(findCredential(t, fixture.configs[5], "a", 0), aliceA, "vless") == true || !credentialsMatch(findCredential(t, fixture.configs[5], "b", 0), aliceB, "vless") {
		t.Fatalf("unexpected partial disable: %#v", first)
	}
	actionsAfterFirst := len(fixture.actions)
	secondPlan, err := application.buildAccessPlan(context.Background(), "session", "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationDisable, "disable-retry", secondPlan)
	second := lifecycleTestApp(t, store, server).executeAccessPlan(context.Background(), "session", "alice", "disable-retry", lifecycleOperationDisable, secondPlan)
	if second.PendingCount != 0 || len(fixture.actions) != actionsAfterFirst+1 {
		t.Fatalf("retry did not process only remaining item: %#v actions=%#v", second, fixture.actions)
	}
}

func TestAccessLifecycleRejectsHTTP200WithoutAgentChange(t *testing.T) {
	alice := map[string]any{"email": "alice__tag", "psk": "alice-psk"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(15, "tag", "snell", alice)}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{15: lifecycleConfig(lifecycleInbound("tag", "snell", alice))})
	fixture.ignoreMutations = true
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	plan, err := application.buildAccessPlan(context.Background(), "session", "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationDisable, "disable-nochange", plan)
	result := application.executeAccessPlan(context.Background(), "session", "alice", "disable-nochange", lifecycleOperationDisable, plan)
	if result.PendingCount != 1 || !strings.Contains(result.LastError, "真实 Agent 配置未变化") {
		t.Fatalf("unchanged HTTP 200 must fail: %#v", result)
	}
}

func TestAccessLifecycleEnableRejectsCredentialDrift(t *testing.T) {
	alice := map[string]any{"email": "alice__tag", "psk": "alice-psk"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "tag", "snell", alice)}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("tag", "snell", alice))})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	plan, _ := application.buildAccessPlan(context.Background(), "session", "alice", false)
	_ = store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationDisable, "disable-drift", plan)
	if result := application.executeAccessPlan(context.Background(), "session", "alice", "disable-drift", lifecycleOperationDisable, plan); result.PendingCount != 0 {
		t.Fatalf("disable failed: %#v", result)
	}
	credential := findCredential(t, fixture.configs[5], "tag", 0)
	credential["level"] = float64(7)
	enablePlan, err := application.buildAccessPlan(context.Background(), "session", "alice", true)
	if err != nil || len(enablePlan) != 1 || enablePlan[0].Status != lifecycleItemFailed || !strings.Contains(enablePlan[0].LastError, "漂移") {
		t.Fatalf("credential drift was not rejected: %#v err=%v", enablePlan, err)
	}
}

func TestDeleteLifecycleRecognizesDisabledCredentialWithoutRestore(t *testing.T) {
	alice := map[string]any{"email": "alice__tag", "psk": "alice-psk"}
	store := &lifecycleTestStore{refs: []lifecycleCredentialRef{lifecycleRef(5, "tag", "snell", alice)}}
	fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(lifecycleInbound("tag", "snell", alice))})
	defer server.Close()
	application := lifecycleTestApp(t, store, server)
	disablePlan, _ := application.buildAccessPlan(context.Background(), "session", "alice", false)
	_ = store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationDisable, "disable-delete", disablePlan)
	if result := application.executeAccessPlan(context.Background(), "session", "alice", "disable-delete", lifecycleOperationDisable, disablePlan); result.PendingCount != 0 {
		t.Fatalf("disable failed: %#v", result)
	}
	deletePlan, err := application.buildDeletionPlan(context.Background(), "session", "alice")
	if err != nil || len(deletePlan) != 1 || deletePlan[0].Action != lifecycleActionDeleteWhole {
		t.Fatalf("stale owner blocked disabled credential whole-inbound deletion: %#v err=%v", deletePlan, err)
	}
	_ = store.SaveDeletePlan(context.Background(), "alice", "delete-disabled", deletePlan)
	result := application.executeDeletePlan(context.Background(), "session", "alice", "delete-disabled", deletePlan)
	if !result.UserDeleted || findConfigInbound(fixture.configs[5], "tag") != nil {
		t.Fatalf("disabled user deletion failed: %#v", result)
	}
}

func findCredential(t *testing.T, config map[string]any, tag string, index int) map[string]any {
	t.Helper()
	entries, _, err := inboundCredentialEntries(findConfigInbound(config, tag))
	if err != nil || index >= len(entries) {
		t.Fatalf("credential %s[%d] unavailable: %v", tag, index, err)
	}
	return entries[index]
}

func TestLifecycleRelationSourcesCoverManagementPackageSubaccountRoutedAndOwnership(t *testing.T) {
	want := []string{"user_inbound_configs", "package_assignment_inbound_configs", "user_subaccounts", "package_assignment_subaccounts", "user_package_assignments", "user_outbounds", "mmwxc_connection_assignments"}
	joined := strings.Join(lifecycleRelationTables, ",")
	for _, source := range want {
		if !strings.Contains(joined, source) {
			t.Errorf("missing lifecycle relation source %s", source)
		}
	}
}

func TestUserLifecycleIsolatedPostgresRelationsAndFinalCleanup(t *testing.T) {
	dsn := os.Getenv("MMWXC_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MMWXC_TEST_POSTGRES_DSN is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("mmwxc_lifecycle_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE users (username text PRIMARY KEY, role text NOT NULL, is_active bigint NOT NULL, package_id bigint, telegram_id bigint, speed_limit_override bigint, device_limit_override bigint, traffic_limit_override bigint, node_speed_limit_overrides text, node_device_limit_overrides text)`,
		`CREATE TABLE remote_servers (id bigint PRIMARY KEY, name text NOT NULL UNIQUE)`,
		`CREATE TABLE packages (id bigint PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE nodes (id bigint PRIMARY KEY, username text NOT NULL, original_server text, inbound_tag text, protocol text, raw_url text, parsed_config text, clash_config text, node_type text)`,
		`CREATE TABLE user_inbound_configs (id bigint PRIMARY KEY, username text NOT NULL, server_id bigint NOT NULL, inbound_tag text NOT NULL, protocol text, credential_json text)`,
		`CREATE TABLE package_assignment_inbound_configs (id bigint PRIMARY KEY, username text NOT NULL, server_id bigint NOT NULL, inbound_tag text NOT NULL, protocol text, credential_json text)`,
		`CREATE TABLE user_subaccounts (id bigint PRIMARY KEY, username text NOT NULL, routed_node_id bigint NOT NULL, email text, credential_json text)`,
		`CREATE TABLE package_assignment_subaccounts (id bigint PRIMARY KEY, username text NOT NULL, routed_node_id bigint NOT NULL, email text, credential_json text)`,
		`CREATE TABLE user_outbounds (id bigint PRIMARY KEY, username text NOT NULL, server_id bigint NOT NULL, inbound_tag text NOT NULL)`,
		`INSERT INTO users VALUES ('admin','admin',1,NULL,NULL,NULL,NULL,NULL,'{}','{}'),('alice','user',1,NULL,NULL,NULL,NULL,NULL,'{}','{}'),('bob','user',1,NULL,NULL,NULL,NULL,NULL,'{}','{}')`,
		`INSERT INTO remote_servers VALUES (5,'server-five')`,
		`INSERT INTO packages VALUES (9,'shared-template')`,
		`INSERT INTO nodes VALUES (10,'alice','server-five','routed','vless','','{"id":"alice-node"}','{}','physical'),(11,'owner','server-five','routed','vless','','{}','{}','routed')`,
		`INSERT INTO user_inbound_configs VALUES (1,'alice',5,'shared','vless','{"id":"alice-id","email":"alice__shared"}'),(2,'bob',5,'shared','vless','{"id":"bob-id","email":"bob__shared"}')`,
		`INSERT INTO package_assignment_inbound_configs VALUES (3,'alice',5,'package-tag','snell','{"psk":"alice-psk"}')`,
		`INSERT INTO user_subaccounts VALUES (4,'alice',11,'alice__routed','{"id":"alice-route"}')`,
		`INSERT INTO package_assignment_subaccounts VALUES (5,'alice',11,'alice__package-route','{"id":"alice-package-route"}')`,
		`INSERT INTO user_outbounds VALUES (6,'alice',5,'outbound-tag')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("isolated lifecycle schema: %v\n%s", err, statement)
		}
	}
	store := &postgresAdminSessionStore{db: db}
	if err := store.EnsureConnectionOwnershipSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mmwxc_connection_assignments(server_id,inbound_tag,management_username,protocol_identity,source,assignment_type) VALUES(5,'manual-tag','alice','alice-manual','manual','identity')`); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureUserManagementSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureUserLifecycleSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	accessCredential := map[string]any{"psk": "temporary-access-test"}
	accessItem := lifecyclePlanItem{
		ServerID: 5, ServerName: "server-five", InboundTag: "access-tag", Protocol: "snell",
		Action: lifecycleActionReplaceCredential, Status: lifecycleItemPending,
		accessCredentials: []lifecycleCredentialBackup{{
			Username: "alice", ServerID: 5, ServerName: "server-five", InboundTag: "access-tag", Protocol: "snell",
			CredentialKey: "access-key", OriginalCredential: accessCredential,
			DisabledCredential: map[string]any{"psk": "temporary-disabled-test"},
			OriginalHash:       hashJSON(accessCredential), DisabledHash: hashJSON(map[string]any{"psk": "temporary-disabled-test"}),
		}},
	}
	if err := store.SaveAccessPlan(context.Background(), "alice", lifecycleOperationDisable, "op-access-postgres", []lifecyclePlanItem{accessItem}); err != nil {
		t.Fatalf("save access plan: %v", err)
	}
	var accessState string
	var backupCount int
	if err := db.QueryRow(`SELECT effective_state FROM mmwxc_user_lifecycle WHERE username='alice'`).Scan(&accessState); err != nil || accessState != lifecycleStateDisabling {
		t.Fatalf("access lifecycle state=%q err=%v", accessState, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM mmwxc_user_disabled_credentials WHERE username='alice'`).Scan(&backupCount); err != nil || backupCount != 1 {
		t.Fatalf("access credential backups=%d err=%v", backupCount, err)
	}
	refs, err := store.LifecycleCredentialRefs(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	tags := make(map[string]bool)
	for _, ref := range refs {
		tags[ref.InboundTag] = true
	}
	for _, tag := range []string{"shared", "package-tag", "routed", "outbound-tag", "manual-tag"} {
		if !tags[tag] {
			t.Errorf("missing relation-derived inbound %s in %#v", tag, refs)
		}
	}
	consumers, err := store.LifecycleInboundBusinessRefs(context.Background(), 5, "server-five", "shared", "alice")
	if err != nil || len(consumers) != 1 || consumers[0].Username != "bob" {
		t.Fatalf("shared consumer refs=%#v err=%v", consumers, err)
	}
	if err := store.SaveDeletePlan(context.Background(), "alice", "op-postgres", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeManagementUserDeletion(context.Background(), "alice", "op-postgres"); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM users WHERE username='alice'`:                                                         0,
		`SELECT COUNT(*) FROM users WHERE username='bob'`:                                                           1,
		`SELECT COUNT(*) FROM remote_servers WHERE id=5`:                                                            1,
		`SELECT COUNT(*) FROM packages WHERE id=9`:                                                                  1,
		`SELECT COUNT(*) FROM user_inbound_configs WHERE username='alice'`:                                          0,
		`SELECT COUNT(*) FROM mmwxc_connection_assignments WHERE management_username='alice'`:                       0,
		`SELECT COUNT(*) FROM mmwxc_user_lifecycle_operations WHERE operation_id='op-postgres' AND state='deleted'`: 1,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Errorf("query %q got=%d want=%d err=%v", query, got, want, err)
		}
	}
}

func (s *lifecycleTestStore) LifecycleDeletionData(context.Context, string) (lifecycleDeletionData, error) {
	data := s.deletionData
	data.Packages = s.packages
	return data, nil
}
