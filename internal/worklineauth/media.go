// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package worklineauth manages local Workline access state.
package worklineauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/keychain"
)

const (
	DefaultMediaServerURL = "http://54.151.241.139:3000"
	MediaServerURLEnv     = "WORKLINE_MEDIA_SERVER_URL"
	MediaAPIKeyEnv        = "WORKLINE_MEDIA_API_KEY"
	disabledServerURL     = "off"
	mediaKeyPrefix        = "workline-media:v1"
)

// ServerURL returns the built-in Workline media endpoint, unless an operator
// supplies an environment override.
func ServerURL() string {
	if configured := strings.TrimSpace(os.Getenv(MediaServerURLEnv)); configured == disabledServerURL {
		return ""
	} else if configured != "" {
		return configured
	}
	return DefaultMediaServerURL
}

// APIKey returns locally stored Workline access state for one account.
func APIKey(store keychain.KeychainAccess, appID, openID string) (string, error) {
	if store == nil {
		return "", errs.NewInternalError(errs.SubtypeStorage, "local keychain is unavailable")
	}
	key, err := store.Get(keychain.LarkCliService, account(appID, openID))
	if err != nil {
		return "", errs.NewInternalError(errs.SubtypeStorage, "read Workline access state from credential storage failed").WithCause(err)
	}
	return strings.TrimSpace(key), nil
}

// RemoveAPIKey clears local Workline access state for one account.
func RemoveAPIKey(store keychain.KeychainAccess, appID, openID string) error {
	if store == nil {
		return errs.NewInternalError(errs.SubtypeStorage, "local keychain is unavailable")
	}
	if err := store.Remove(keychain.LarkCliService, account(appID, openID)); err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "remove Workline access state from credential storage failed").WithCause(err)
	}
	return nil
}

// EnsureAPIKey prepares local Workline access after login. Its first return
// value reports whether this call stored new local state.
func EnsureAPIKey(ctx context.Context, client *http.Client, store keychain.KeychainAccess, appID, openID, feishuToken string) (bool, error) {
	rawURL := ServerURL()
	if rawURL == "" {
		return false, nil
	}
	if strings.TrimSpace(feishuToken) == "" {
		return false, errs.NewAuthenticationError(errs.SubtypeTokenMissing, "Workline access requires `work-cli auth login`")
	}
	if client == nil {
		return false, errs.NewInternalError(errs.SubtypeSDKError, "HTTP client is unavailable")
	}
	existing, err := APIKey(store, appID, openID)
	if err != nil {
		return false, err
	}
	if existing != "" {
		return false, nil
	}

	endpoint, err := exchangeEndpoint(rawURL)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return false, errs.NewInternalError(errs.SubtypeInvalidResponse, "build Workline media login request: %v", err).WithCause(err)
	}
	req.Header.Set("Authorization", "Bearer "+feishuToken)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, errs.NewNetworkError(errs.SubtypeNetworkTransport, "connect to Workline: %v", err).WithCause(err).WithRetryable()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return false, exchangeError(resp)
	}
	var result struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return false, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "decode Workline response: %v", err).WithCause(err)
	}
	if strings.TrimSpace(result.APIKey) == "" {
		return false, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "Workline access response is incomplete")
	}
	if err := store.Set(keychain.LarkCliService, account(appID, openID), result.APIKey); err != nil {
		return false, errs.NewInternalError(errs.SubtypeStorage, "save Workline access state to credential storage failed").WithCause(err)
	}
	return true, nil
}

func exchangeEndpoint(rawURL string) (*url.URL, error) {
	base, err := url.Parse(rawURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errs.NewConfigError(errs.SubtypeInvalidConfig, "%s must be an absolute URL", MediaServerURLEnv).
			WithField(MediaServerURLEnv)
	}
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, errs.NewConfigError(errs.SubtypeInvalidConfig, "%s must use HTTP or HTTPS", MediaServerURLEnv).
			WithField(MediaServerURLEnv)
	}
	return base.ResolveReference(&url.URL{Path: "/v1/oauth"}), nil
}

func exchangeError(resp *http.Response) error {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
	message := strings.TrimSpace(payload.Error.Message)
	if message == "" {
		message = fmt.Sprintf("Workline request returned HTTP %d", resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return errs.NewAuthenticationError(errs.SubtypeTokenInvalid, "Workline access was rejected: %s", message)
	case http.StatusForbidden:
		return errs.NewPermissionError(errs.SubtypePermissionDenied, "Workline access is not available: %s", message).
			WithHint("contact the Workline administrator")
	case http.StatusTooManyRequests:
		return errs.NewAPIError(errs.SubtypeRateLimit, "%s", message).WithRetryable()
	default:
		if resp.StatusCode >= 500 {
			return errs.NewNetworkError(errs.SubtypeNetworkServer, "%s", message).WithRetryable()
		}
		return errs.NewAPIError(errs.SubtypeUnknown, "%s", message)
	}
}

func account(appID, openID string) string {
	return mediaKeyPrefix + ":" + appID + ":" + openID
}
