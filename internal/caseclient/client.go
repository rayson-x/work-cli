// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package caseclient is the small, transport-only client for the cloud Case API.
// It deliberately has no apparel-domain write methods: canonical Style, Person,
// Responsibility, and Event records are produced by the cloud inference agent.
package caseclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/larksuite/cli/internal/core"
	"github.com/larksuite/cli/internal/validate"
	"github.com/larksuite/cli/internal/vfs"
	"github.com/larksuite/cli/internal/worklineauth"
)

const ContractVersion = "case.v1"

type Options struct {
	BaseURL    string
	APIKey     string
	HTTP       *http.Client
	StatePath  string
	MaxRetries int
	RetryBase  time.Duration
	Sleep      func(time.Duration) error
}

type Client struct {
	base       *url.URL
	apiKey     string
	http       *http.Client
	statePath  string
	maxRetries int
	retryBase  time.Duration
	sleep      func(time.Duration) error
	mu         sync.Mutex
}

type CreateCaseRequest struct {
	Purpose     string         `json:"purpose"`
	SourceScope map[string]any `json:"source_scope"`
	Key         string         `json:"-"`
}

type Case struct {
	CaseRef     string `json:"case_id"`
	Purpose     string `json:"purpose"`
	SourceScope any    `json:"source_scope"`
	Status      string `json:"status"`
	Revision    int    `json:"revision"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Disposition string `json:"disposition,omitempty"`
}

type EvidenceItem struct {
	ClientEvidenceKey string         `json:"client_evidence_key"`
	SourceKey         string         `json:"source_key"`
	Kind              string         `json:"kind"`
	SourceTime        string         `json:"source_time,omitempty"`
	SpeakerSourceKey  string         `json:"speaker_source_key,omitempty"`
	RawText           string         `json:"raw_text,omitempty"`
	MediaRef          string         `json:"media_ref,omitempty"`
	ContentHash       string         `json:"content_hash,omitempty"`
	SourceLocator     map[string]any `json:"source_locator"`
	ImmutablePayload  map[string]any `json:"immutable_payload"`
}

type EvidenceRelation struct {
	FromClientEvidenceKey string `json:"from_client_evidence_key"`
	ToClientEvidenceKey   string `json:"to_client_evidence_key"`
	Type                  string `json:"type"`
}

type EvidenceBundle struct {
	Coverage              map[string]any    `json:"coverage"`
	CollectionStartedAt   string             `json:"collection_started_at,omitempty"`
	CollectionEndedAt     string             `json:"collection_ended_at,omitempty"`
	Items                 []EvidenceItem     `json:"items"`
	Relations             []EvidenceRelation `json:"relations,omitempty"`
	Key                   string             `json:"-"`
}

type EvidenceBundleResult struct {
	BundleRef   string            `json:"bundle_ref"`
	CaseRef     string            `json:"case_ref"`
	CaseRevision int               `json:"case_revision"`
	Status      string            `json:"status"`
	EvidenceRefs map[string]string `json:"evidence_refs"`
	Warnings    []string          `json:"warnings"`
}

type InferenceRunRequest struct {
	BaseCaseRevision int    `json:"base_case_revision"`
	Pipeline         string `json:"pipeline"`
	Key              string `json:"-"`
}

type InferenceRun struct {
	RunRef           string `json:"run_ref"`
	CaseRef          string `json:"case_ref"`
	BaseCaseRevision int    `json:"base_case_revision"`
	Status           string `json:"status"`
	Disposition      string `json:"disposition,omitempty"`
}

type Error struct {
	Operation  string
	Status     int
	Code       string
	Message    string
	Retryable  bool
	RetryAfter time.Duration
	Cause      error
}

func (e *Error) Error() string {
	if e == nil { return "" }
	if e.Message == "" { return fmt.Sprintf("case api request failed (HTTP %d)", e.Status) }
	return e.Message
}
func (e *Error) Unwrap() error { if e == nil { return nil }; return e.Cause }

type operation struct {
	Key       string `json:"key"`
	Hash      string `json:"request_hash"`
	Operation string `json:"operation"`
	Resource  string `json:"resource_ref,omitempty"`
	UpdatedAt string `json:"updated_at"`
}
type stateFile struct { Version int `json:"version"`; Operations []operation `json:"operations"` }

func New(options Options) *Client {
	raw := strings.TrimSpace(options.BaseURL)
	if raw == "" { raw = worklineauth.ServerURL() }
	base, err := url.Parse(raw)
	if err != nil || base.Scheme == "" || base.Host == "" { panic("caseclient: invalid BaseURL") }
	if !strings.HasSuffix(base.Path, "/") { base.Path += "/" }
	key := strings.TrimSpace(options.APIKey)
	if key == "" { panic("caseclient: APIKey is required") }
	h := options.HTTP
	if h == nil { h = http.DefaultClient }
	retries := options.MaxRetries
	if retries == 0 { retries = 2 }
	if retries < 0 { retries = 0 }
	baseDelay := options.RetryBase
	if baseDelay <= 0 { baseDelay = 250 * time.Millisecond }
	sleep := options.Sleep
	if sleep == nil { sleep = func(d time.Duration) error { time.Sleep(d); return nil } }
	statePath := options.StatePath
	if statePath == "" { statePath = filepath.Join(core.GetConfigDir(), "case-operations.json") }
	return &Client{base: base, apiKey: key, http: h, statePath: statePath, maxRetries: retries, retryBase: baseDelay, sleep: sleep}
}

func (c *Client) CreateCase(ctx context.Context, input CreateCaseRequest) (Case, error) {
	if strings.TrimSpace(input.Purpose) == "" || len(input.SourceScope) == 0 { return Case{}, &Error{Operation: "case.create", Status: 422, Code: "invalid_input", Message: "purpose and source_scope are required"} }
	body := map[string]any{"contract_version": ContractVersion, "purpose": input.Purpose, "source_scope": input.SourceScope}
	key := input.Key
	if key == "" { key = stableKey("case", body) }
	hash := hashPayload(body)
	if err := c.reserve("case.create", key, hash); err != nil { return Case{}, err }
	var out Case
	if err := c.jsonRequest(ctx, http.MethodPost, "/v1/cases", key, body, &out); err != nil { return Case{}, err }
	_ = c.complete("case.create", key, out.CaseRef)
	return out, nil
}

func (c *Client) SubmitEvidenceBundle(ctx context.Context, caseRef string, input EvidenceBundle) (EvidenceBundleResult, error) {
	if strings.TrimSpace(caseRef) == "" || len(input.Items) == 0 || input.Coverage == nil { return EvidenceBundleResult{}, &Error{Operation: "evidence.submit", Status: 422, Code: "invalid_input", Message: "case_ref, coverage, and items are required"} }
	body := map[string]any{"contract_version": ContractVersion, "coverage": input.Coverage, "items": input.Items}
	if input.CollectionStartedAt != "" { body["collection_started_at"] = input.CollectionStartedAt }
	if input.CollectionEndedAt != "" { body["collection_ended_at"] = input.CollectionEndedAt }
	if len(input.Relations) > 0 { body["relations"] = input.Relations }
	key := input.Key
	if key == "" { key = stableKey("bundle:"+caseRef, body) }
	hash := hashPayload(body)
	if err := c.reserve("evidence.submit", key, hash); err != nil { return EvidenceBundleResult{}, err }
	var out EvidenceBundleResult
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/evidence-bundles"
	if err := c.jsonRequest(ctx, http.MethodPost, path, key, body, &out); err != nil { return EvidenceBundleResult{}, err }
	_ = c.complete("evidence.submit", key, out.BundleRef)
	return out, nil
}

func (c *Client) SealEvidenceBundle(ctx context.Context, caseRef, bundleRef string) (map[string]any, error) {
	if caseRef == "" || bundleRef == "" { return nil, &Error{Operation: "evidence.seal", Status: 422, Code: "invalid_input", Message: "case_ref and bundle_ref are required"} }
	var out map[string]any
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/evidence-bundles/" + url.PathEscape(bundleRef) + "/seal"
	if err := c.jsonRequest(ctx, http.MethodPost, path, "", map[string]any{}, &out); err != nil { return nil, err }
	return out, nil
}

func (c *Client) StartInferenceRun(ctx context.Context, caseRef string, input InferenceRunRequest) (InferenceRun, error) {
	if caseRef == "" || input.BaseCaseRevision < 0 || strings.TrimSpace(input.Pipeline) == "" { return InferenceRun{}, &Error{Operation: "run.start", Status: 422, Code: "invalid_input", Message: "case_ref, non-negative revision, and pipeline are required"} }
	body := map[string]any{"contract_version": ContractVersion, "base_case_revision": input.BaseCaseRevision, "pipeline": input.Pipeline}
	key := input.Key
	if key == "" { key = stableKey("run:"+caseRef, body) }
	hash := hashPayload(body)
	if err := c.reserve("run.start", key, hash); err != nil { return InferenceRun{}, err }
	var out InferenceRun
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/inference-runs"
	if err := c.jsonRequest(ctx, http.MethodPost, path, key, body, &out); err != nil { return InferenceRun{}, err }
	_ = c.complete("run.start", key, out.RunRef)
	return out, nil
}

func (c *Client) GetCase(ctx context.Context, caseRef string) (map[string]any, error) { var out map[string]any; err := c.jsonRequest(ctx, http.MethodGet, "/v1/cases/"+url.PathEscape(caseRef), "", nil, &out); return out, err }
func (c *Client) GetEvidence(ctx context.Context, caseRef string) (map[string]any, error) { var out map[string]any; err := c.jsonRequest(ctx, http.MethodGet, "/v1/cases/"+url.PathEscape(caseRef)+"/evidence", "", nil, &out); return out, err }
func (c *Client) GetRun(ctx context.Context, caseRef, runRef string) (map[string]any, error) { var out map[string]any; err := c.jsonRequest(ctx, http.MethodGet, "/v1/cases/"+url.PathEscape(caseRef)+"/inference-runs/"+url.PathEscape(runRef), "", nil, &out); return out, err }
func (c *Client) GetInterpretation(ctx context.Context, caseRef, view string) (map[string]any, error) {
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/interpretation"
	if view != "" { path += "?view=" + url.QueryEscape(view) }
	var out map[string]any; err := c.jsonRequest(ctx, http.MethodGet, path, "", nil, &out); return out, err
}

func (c *Client) jsonRequest(ctx context.Context, method, path, idempotencyKey string, payload any, out any) error {
	var raw []byte
	var err error
	if payload != nil { raw, err = json.Marshal(payload); if err != nil { return &Error{Operation: method+" "+path, Code: "encode_request", Message: "encode request failed", Cause: err} } }
	for attempt := 0; ; attempt++ {
		reqURL, parseErr := c.base.Parse(strings.TrimPrefix(path, "/"))
		if parseErr != nil { return &Error{Operation: method+" "+path, Code: "invalid_url", Message: "case service URL is invalid", Cause: parseErr} }
		req, buildErr := http.NewRequestWithContext(ctx, method, reqURL.String(), bytes.NewReader(raw))
		if buildErr != nil { return &Error{Operation: method+" "+path, Code: "build_request", Message: "build case request failed", Cause: buildErr} }
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		if payload != nil { req.Header.Set("Content-Type", "application/json") }
		if idempotencyKey != "" { req.Header.Set("Idempotency-Key", idempotencyKey) }
		resp, doErr := c.http.Do(req)
		if doErr != nil {
			if attempt < c.maxRetries { if err := c.sleep(c.retryBase * time.Duration(attempt+1)); err != nil { return &Error{Operation: method+" "+path, Code: "retry_interrupted", Message: "retry interrupted", Cause: err} }; continue }
			return &Error{Operation: method+" "+path, Code: "network_transport", Message: "connect to case service failed", Retryable: true, Cause: doErr}
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil { return &Error{Operation: method+" "+path, Status: resp.StatusCode, Code: "network_read", Message: "read case service response failed", Retryable: true, Cause: readErr} }
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			e := decodeError(method+" "+path, resp, body)
			if e.Retryable && attempt < c.maxRetries { delay := e.RetryAfter; if delay <= 0 { delay = c.retryBase * time.Duration(attempt+1) }; if err := c.sleep(delay); err != nil { e.Cause = err; return e }; continue }
			return e
		}
		if out == nil || len(bytes.TrimSpace(body)) == 0 { return nil }
		if err := json.Unmarshal(body, out); err != nil { return &Error{Operation: method+" "+path, Status: resp.StatusCode, Code: "invalid_response", Message: "case service returned invalid JSON", Cause: err} }
		return nil
	}
}

func decodeError(operation string, resp *http.Response, body []byte) *Error {
	var envelope struct { Error struct { Code string `json:"code"`; Message string `json:"message"` } `json:"error"` }
	_ = json.Unmarshal(body, &envelope)
	message := strings.TrimSpace(envelope.Error.Message); if message == "" { message = http.StatusText(resp.StatusCode) }
	e := &Error{Operation: operation, Status: resp.StatusCode, Code: envelope.Error.Code, Message: message}
	if e.Code == "" { e.Code = "http_" + strconv.Itoa(resp.StatusCode) }
	switch resp.StatusCode { case 429, 503: e.Retryable = true; case 409, 412, 422, 401, 403: e.Retryable = false; default: e.Retryable = resp.StatusCode >= 500 }
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" { if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 { e.RetryAfter = time.Duration(seconds) * time.Second } }
	return e
}

func stableKey(prefix string, payload any) string { return prefix + ":" + hashPayload(payload) }
func hashPayload(payload any) string { raw, _ := json.Marshal(payload); sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func (c *Client) reserve(op, key, hash string) error {
	c.mu.Lock(); defer c.mu.Unlock()
	s, err := c.loadState(); if err != nil { return err }
	for _, item := range s.Operations { if item.Operation == op && item.Key == key && item.Hash != hash { return &Error{Operation: op, Status: 409, Code: "idempotency_conflict", Message: "idempotency key is already associated with a different payload"} } }
	for _, item := range s.Operations { if item.Operation == op && item.Key == key { return nil } }
	s.Operations = append(s.Operations, operation{Operation: op, Key: key, Hash: hash, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	return c.saveState(s)
}
func (c *Client) complete(op, key, ref string) error { c.mu.Lock(); defer c.mu.Unlock(); s, err := c.loadState(); if err != nil { return err }; for i := range s.Operations { if s.Operations[i].Operation == op && s.Operations[i].Key == key { s.Operations[i].Resource = ref; s.Operations[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano) } }; return c.saveState(s) }
func (c *Client) loadState() (stateFile, error) { raw, err := vfs.ReadFile(c.statePath); if err != nil { if errors.Is(err, os.ErrNotExist) { return stateFile{Version: 1}, nil }; return stateFile{}, err }; var s stateFile; if err := json.Unmarshal(raw, &s); err != nil { return stateFile{}, err }; if s.Version == 0 { s.Version = 1 }; return s, nil }
func (c *Client) saveState(s stateFile) error { if err := vfs.MkdirAll(filepath.Dir(c.statePath), 0o700); err != nil { return err }; raw, err := json.MarshalIndent(s, "", "  "); if err != nil { return err }; return validate.AtomicWrite(c.statePath, append(raw, '\n'), 0o600) }
