package idp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRegisteredAccountsHaveAnAdministratorDeleteEntry(t *testing.T) {
	server, endpoint := newTestServer(t, nil)
	defer endpoint.Close()
	defer server.Close()
	admin := newTestClient(t, endpoint.URL)
	member := newTestClient(t, endpoint.URL)
	registerTestUser(t, admin, endpoint.URL, "owner@example.com")
	registerTestUser(t, member, endpoint.URL, "private@example.com")
	body := getBody(t, admin, endpoint.URL+"/idp/users?lang=zh-CN", http.StatusOK)
	if !strings.Contains(body, "private@example.com") || !strings.Contains(body, "删除账号") {
		t.Fatalf("administrator cannot delete a registered account: %s", body)
	}
	getBody(t, member, endpoint.URL+"/idp/users", http.StatusForbidden)
}

func TestDeleteRegisteredAccountRevokesSessionsAndProtectsTheAdministrator(t *testing.T) {
	server, endpoint := newTestServer(t, nil)
	defer endpoint.Close()
	defer server.Close()
	admin := newTestClient(t, endpoint.URL)
	member := newTestClient(t, endpoint.URL)
	registerTestUser(t, admin, endpoint.URL, "owner@example.com")
	registerTestUser(t, member, endpoint.URL, "private@example.com")
	owner, err := server.store.UserByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	target, err := server.store.UserByEmail(context.Background(), "private@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.store.LinkIdentity(context.Background(), "test", "external-identity", target.ID, target.Email); err != nil {
		t.Fatal(err)
	}
	if err := server.store.CreateAccessToken(context.Background(), "access-hash", target.ID, "openid", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	cleanupFailed := true
	server.opts.DeleteNetwork = func(ctx context.Context, id string) error {
		if id != target.ID {
			t.Fatalf("deleted the wrong network: %s", id)
		}
		if _, err := server.store.UserByAccessToken(ctx, "access-hash", time.Now()); !errors.Is(err, ErrNotFound) {
			t.Fatal("account token still works during network cleanup")
		}
		if cleanupFailed {
			return errors.New("network service unavailable")
		}
		return nil
	}
	path := endpoint.URL + "/idp/users/" + target.ID + "/delete"
	body := getBody(t, admin, path, http.StatusOK)
	// A member cannot bypass the UI with a valid CSRF token of their own.
	memberPage := getBody(t, member, endpoint.URL+"/idp/account", http.StatusOK)
	response := postForm(t, member, path, url.Values{"csrf_token": {csrfToken(t, memberPage)}, "confirm_email": {target.Email}})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("member delete status: %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = postForm(t, admin, path, url.Values{"csrf_token": {csrfToken(t, body)}, "confirm_email": {"wrong@example.com"}})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("confirmation status: %d", response.StatusCode)
	}
	_ = response.Body.Close()
	response = postForm(t, admin, path, url.Values{"csrf_token": {csrfToken(t, body)}, "confirm_email": {target.Email}})
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failed cleanup status: %d", response.StatusCode)
	}
	_ = response.Body.Close()
	getBody(t, member, endpoint.URL+"/idp/account", http.StatusSeeOther)
	login := getBody(t, member, endpoint.URL+"/idp/login", http.StatusOK)
	response = postForm(t, member, endpoint.URL+"/idp/login", url.Values{"csrf_token": {csrfToken(t, login)}, "email": {target.Email}, "password": {"correct horse battery"}})
	if response.StatusCode != http.StatusOK || response.Header.Get("Location") != "" {
		t.Fatal("disabled account could sign in")
	}
	_ = response.Body.Close()
	cleanupFailed = false
	response = postForm(t, admin, path, url.Values{"csrf_token": {csrfToken(t, body)}, "confirm_email": {target.Email}})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete retry status: %d", response.StatusCode)
	}
	_ = response.Body.Close()
	if _, err := server.store.UserByID(context.Background(), target.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("account remains after deletion")
	}
	if _, err := server.store.IdentityForUser(context.Background(), "test", target.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("third-party identity remains after deletion")
	}
	getBody(t, admin, endpoint.URL+"/idp/users/"+owner.ID+"/delete", http.StatusConflict)
	getBody(t, admin, endpoint.URL+"/idp/users", http.StatusOK)
}
