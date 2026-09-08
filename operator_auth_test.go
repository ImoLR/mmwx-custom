package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeAdminSessionStore struct {
	authorized bool
	server     bool
	err        error
}

func (s *fakeAdminSessionStore) AuthorizeAdmin(context.Context, string) (bool, error) {
	return s.authorized, s.err
}

func (s *fakeAdminSessionStore) RemoteServerExists(context.Context, string) (bool, error) {
	return s.server, s.err
}

func (s *fakeAdminSessionStore) Close() error { return nil }

func TestOperatorAuthorizationUsesIndependentBearerToken(t *testing.T) {
	app := &app{apiToken: "operator-token"}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/dashboard/system", nil)
	request.Header.Set("Authorization", "Bearer operator-token")
	if err := app.authorizeOperatorRequest(request); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorAuthorizationUsesDatabaseAdminSession(t *testing.T) {
	app := &app{apiToken: "operator-token", adminStore: &fakeAdminSessionStore{authorized: true, server: true}}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/servers/6/connections", nil)
	request.Header.Set("MM-Authorization", "valid-admin-session")
	if err := app.authorizeOperatorServerRequest(request, "6"); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorAuthorizationRejectsMissingAndNonAdminSessions(t *testing.T) {
	app := &app{apiToken: "operator-token", adminStore: &fakeAdminSessionStore{}}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/dashboard/system", nil)
	if err := app.authorizeOperatorRequest(request); !errors.Is(err, errOperatorAuthorizationMissing) {
		t.Fatalf("missing authorization returned %v", err)
	}
	request.Header.Set("MM-Authorization", "non-admin-session")
	if err := app.authorizeOperatorRequest(request); !errors.Is(err, errOperatorAuthorizationInvalid) {
		t.Fatalf("non-admin authorization returned %v", err)
	}
}

func TestOperatorAuthorizationFailsClosedWhenDatabaseIsUnavailable(t *testing.T) {
	app := &app{adminStore: &fakeAdminSessionStore{err: errors.New("database unavailable")}}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/dashboard/system", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	if err := app.authorizeOperatorRequest(request); !errors.Is(err, errOperatorAuthorizationUnavailable) {
		t.Fatalf("database failure returned %v", err)
	}
}

func TestOperatorAuthorizationRequiresExistingRemoteServer(t *testing.T) {
	app := &app{adminStore: &fakeAdminSessionStore{authorized: true}}
	request := httptest.NewRequest(http.MethodGet, "/api/custom/servers/999/connections", nil)
	request.Header.Set("MM-Authorization", "admin-session")
	if err := app.authorizeOperatorServerRequest(request, "999"); !errors.Is(err, errOperatorServerNotFound) {
		t.Fatalf("missing server returned %v", err)
	}
}
