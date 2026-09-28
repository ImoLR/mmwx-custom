package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeUserManagementStore struct {
	state   managedUserState
	preview managedUserDeletionPreview
	err     error
}

func (s *fakeUserManagementStore) AuthorizeAdmin(context.Context, string) (bool, error) {
	return true, nil
}

func (s *fakeUserManagementStore) RemoteServerExists(context.Context, string) (bool, error) {
	return false, nil
}

func (s *fakeUserManagementStore) Close() error { return nil }

func (s *fakeUserManagementStore) ManagedUserState(context.Context, string) (managedUserState, error) {
	return s.state, s.err
}

func (s *fakeUserManagementStore) ManagedUserDeletionPreview(context.Context, string) (managedUserDeletionPreview, error) {
	return s.preview, s.err
}

func TestUserManagementStateReturnsAuthoritativeDatabaseValue(t *testing.T) {
	store := &fakeUserManagementStore{state: managedUserState{Username: "alice", Exists: true, IsActive: false, Role: "user"}}
	application := &app{adminStore: store}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/users/alice/state", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	response := httptest.NewRecorder()

	application.userManagementHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"is_active":false`) || !strings.Contains(response.Body.String(), `"exists":true`) {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("missing no-store header")
	}
}

func TestUserManagementStateReportsDeletedUser(t *testing.T) {
	store := &fakeUserManagementStore{state: managedUserState{Username: "gone", Exists: false}}
	application := &app{adminStore: store}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/users/gone/state", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	response := httptest.NewRecorder()

	application.userManagementHandler(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"exists":false`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestUserDeletionPreviewReturnsServerCounts(t *testing.T) {
	store := &fakeUserManagementStore{preview: managedUserDeletionPreview{
		Username: "alice", Exists: true, Role: "user", PackageBindings: 1,
		Subaccounts: 2, InboundBindings: 3, RoutedRelations: 4,
		SharedPreserved: []string{"packages", "remote_servers"}, Details: map[string]int64{"user_subaccounts": 2},
	}}
	application := &app{adminStore: store}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/users/alice/deletion-preview", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	response := httptest.NewRecorder()

	application.userManagementHandler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, fragment := range []string{`"package_bindings":1`, `"subaccounts":2`, `"inbound_bindings":3`, `"routed_relations":4`} {
		if !strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("response missing %s: %s", fragment, response.Body.String())
		}
	}
}

func TestUserDeletionPreviewRejectsAdminAndMissingUser(t *testing.T) {
	for _, test := range []struct {
		name    string
		preview managedUserDeletionPreview
		status  int
	}{
		{"missing", managedUserDeletionPreview{Username: "gone"}, http.StatusNotFound},
		{"admin", managedUserDeletionPreview{Username: "root", Exists: true, Role: "admin"}, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			application := &app{adminStore: &fakeUserManagementStore{preview: test.preview}}
			request := httptest.NewRequest(http.MethodGet, "/api/custom/users/"+test.preview.Username+"/deletion-preview", nil)
			request.Header.Set("MM-Authorization", "admin-session")
			response := httptest.NewRecorder()
			application.userManagementHandler(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestUserManagementHandlerRejectsUnauthorizedAndStoreFailure(t *testing.T) {
	application := &app{adminStore: &fakeUserManagementStore{err: errors.New("database unavailable")}}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/users/alice/state", nil)
	response := httptest.NewRecorder()
	application.userManagementHandler(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/custom/users/alice/state", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	response = httptest.NewRecorder()
	application.userManagementHandler(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("store failure status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestUserManagementCascadeCoversPrivateRelationsOnly(t *testing.T) {
	for _, relation := range []string{
		"user_api_tokens:username",
		"user_inbound_configs:username",
		"user_outbounds:username",
		"user_subaccounts:username",
		"package_user_node_traffic_baselines:username",
		"package_node_traffic_suspensions:username",
		"renewal_requests:username",
		"mmwxc_connection_assignments:management_username",
		"nodes:username",
		"user_email_traffic_snapshots",
		"invite_codes WHERE bind_username",
	} {
		if !strings.Contains(userManagementCascadeSchema, relation) {
			t.Fatalf("cascade schema does not cover %s", relation)
		}
	}
	for _, shared := range []string{"DELETE FROM packages", "DELETE FROM remote_servers", "DELETE FROM server_xray_config_snapshots", "DELETE FROM subscribe_files"} {
		if strings.Contains(userManagementCascadeSchema, shared) {
			t.Fatalf("cascade schema must preserve shared entity: %s", shared)
		}
	}
}
