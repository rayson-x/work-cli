// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package worklineauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/keychain"
)

type memoryKeychain struct{ values map[string]string }

func (m *memoryKeychain) Get(service, account string) (string, error) {
	return m.values[service+"/"+account], nil
}

func (m *memoryKeychain) Set(service, account, value string) error {
	m.values[service+"/"+account] = value
	return nil
}

func (m *memoryKeychain) Remove(service, account string) error {
	delete(m.values, service+"/"+account)
	return nil
}

func TestEnsureAPIKeyExchangesAndStoresKey(t *testing.T) {
	store := &memoryKeychain{values: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/oauth" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer feishu-token" {
			t.Fatalf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"api_key":"fwk_v1_public_secret"}`))
	}))
	defer server.Close()
	t.Setenv(MediaServerURLEnv, server.URL)

	created, err := EnsureAPIKey(context.Background(), server.Client(), store, "cli_x", "ou_x", "feishu-token")
	if err != nil || !created {
		t.Fatalf("EnsureAPIKey() = (%v, %v), want (true, nil)", created, err)
	}
	got, err := APIKey(store, "cli_x", "ou_x")
	if err != nil || got != "fwk_v1_public_secret" {
		t.Fatalf("APIKey() = %q, %v", got, err)
	}
}

func TestEnsureAPIKeyReusesStoredKey(t *testing.T) {
	store := &memoryKeychain{values: map[string]string{}}
	if err := store.Set(keychain.LarkCliService, account("cli_x", "ou_x"), "existing-key"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("existing key must prevent another exchange")
	}))
	defer server.Close()
	t.Setenv(MediaServerURLEnv, server.URL)

	created, err := EnsureAPIKey(context.Background(), server.Client(), store, "cli_x", "ou_x", "feishu-token")
	if err != nil || created {
		t.Fatalf("EnsureAPIKey() = (%v, %v), want (false, nil)", created, err)
	}
}

func TestServerURLUsesBuiltInDefaultWhenNotOverridden(t *testing.T) {
	t.Setenv(MediaServerURLEnv, "")
	if got := ServerURL(); got != DefaultMediaServerURL {
		t.Fatalf("ServerURL() = %q, want %q", got, DefaultMediaServerURL)
	}
}

func TestEnsureAPIKeyMapsAllowlistRejectionToAuthorization(t *testing.T) {
	store := &memoryKeychain{values: map[string]string{}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"oauth_user_not_allowed","message":"not allowed"}}`))
	}))
	defer server.Close()
	t.Setenv(MediaServerURLEnv, server.URL)

	_, err := EnsureAPIKey(context.Background(), server.Client(), store, "cli_x", "ou_x", "feishu-token")
	var permissionErr *errs.PermissionError
	if !errors.As(err, &permissionErr) || permissionErr.Subtype != errs.SubtypePermissionDenied {
		t.Fatalf("error = %T %v, want permission_denied", err, err)
	}
}

func TestRemoveAPIKeyClearsOnlyTheTargetAccount(t *testing.T) {
	store := &memoryKeychain{values: map[string]string{}}
	_ = store.Set(keychain.LarkCliService, account("cli_x", "ou_x"), "key-x")
	_ = store.Set(keychain.LarkCliService, account("cli_x", "ou_y"), "key-y")
	if err := RemoveAPIKey(store, "cli_x", "ou_x"); err != nil {
		t.Fatalf("RemoveAPIKey() error = %v", err)
	}
	if got, _ := APIKey(store, "cli_x", "ou_x"); got != "" {
		t.Fatalf("removed API key = %q", got)
	}
	if got, _ := APIKey(store, "cli_x", "ou_y"); got != "key-y" {
		t.Fatalf("other API key = %q", got)
	}
}
