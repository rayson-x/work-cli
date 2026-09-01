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
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/core"
	"github.com/larksuite/cli/internal/lockfile"
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
	ClientEvidenceKey   string         `json:"client_evidence_key"`
	SourceKey           string         `json:"source_key"`
	Kind                string         `json:"kind"`
	SourceTime          string         `json:"source_time,omitempty"`
	SpeakerSourceKey    string         `json:"speaker_source_key,omitempty"`
	SpeakerDisplayName  string         `json:"speaker_display_name,omitempty"`
	SpeakerIdentityKind string         `json:"speaker_identity_kind,omitempty"`
	RawText             string         `json:"raw_text,omitempty"`
	MediaRef            string         `json:"media_ref,omitempty"`
	ContentHash         string         `json:"content_hash,omitempty"`
	SourceLocator       map[string]any `json:"source_locator"`
	ImmutablePayload    map[string]any `json:"immutable_payload"`
}

type EvidenceRelation struct {
	FromClientEvidenceKey string `json:"from_client_evidence_key"`
	ToClientEvidenceKey   string `json:"to_client_evidence_key"`
	Type                  string `json:"type"`
}

type EvidenceBundle struct {
	Coverage            map[string]any     `json:"coverage"`
	CollectionStartedAt string             `json:"collection_started_at,omitempty"`
	CollectionEndedAt   string             `json:"collection_ended_at,omitempty"`
	Items               []EvidenceItem     `json:"items"`
	Relations           []EvidenceRelation `json:"relations,omitempty"`
	Key                 string             `json:"-"`
}

type EvidenceBundleResult struct {
	BundleRef    string            `json:"bundle_ref"`
	CaseRef      string            `json:"case_ref"`
	CaseRevision int               `json:"case_revision"`
	Status       string            `json:"status"`
	EvidenceRefs map[string]string `json:"evidence_refs"`
	Warnings     []string          `json:"warnings"`
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

type MediaFile struct {
	Name   string
	MIME   string
	Reader io.Reader
}

type MediaUpload struct {
	MediaRef string `json:"media_ref"`
	TaskRef  string `json:"task_ref"`
	Status   string `json:"status"`
	Reused   bool   `json:"reused"`
}

type MediaBatch struct {
	BatchRef string           `json:"batch_ref"`
	Status   string           `json:"status"`
	Total    int              `json:"total"`
	Pending  int              `json:"pending"`
	Items    []map[string]any `json:"items"`
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

// As exposes Case API failures through the repository-wide typed error
// contract. The transport keeps the server's string code in ServerCode while
// the shared numeric Code is populated when the service returned a number.
// Reflection is needed for errs' intentionally private problem-carrier
// interface, which errors.As passes here as a pointer to that interface.
func (e *Error) As(target any) bool {
	if e == nil {
		return false
	}
	typed := e.typedError()
	if typed == nil {
		return false
	}
	want := reflect.ValueOf(target)
	if !want.IsValid() || want.Kind() != reflect.Pointer || want.IsNil() {
		return false
	}
	dst := want.Elem()
	if !dst.CanSet() {
		return false
	}
	value := reflect.ValueOf(typed)
	if value.Type().AssignableTo(dst.Type()) {
		dst.Set(value)
		return true
	}
	if dst.Kind() == reflect.Interface && value.Type().Implements(dst.Type()) {
		dst.Set(value)
		return true
	}
	return false
}

func (e *Error) typedError() error {
	serverCode := strings.TrimSpace(e.Code)
	numericCode, _ := strconv.Atoi(serverCode)
	message := e.Message
	if message == "" {
		message = fmt.Sprintf("case API request failed (HTTP %d)", e.Status)
	}
	withCommon := func(err error) error {
		switch typed := err.(type) {
		case *errs.ValidationError:
			typed.ServerCode, typed.Code, typed.Retryable = serverCode, numericCode, e.Retryable
			typed.Cause = e.Cause
		case *errs.AuthenticationError:
			typed.ServerCode, typed.Code, typed.Retryable = serverCode, numericCode, e.Retryable
			typed.Cause = e.Cause
		case *errs.APIError:
			typed.ServerCode, typed.Code, typed.Retryable = serverCode, numericCode, e.Retryable
			typed.Cause = e.Cause
			if e.RetryAfter > 0 {
				typed.RetryAfterSeconds = int(e.RetryAfter / time.Second)
			}
		case *errs.NetworkError:
			typed.ServerCode, typed.Code, typed.Retryable = serverCode, numericCode, e.Retryable
			typed.Cause = e.Cause
			if e.RetryAfter > 0 {
				typed.RetryAfterSeconds = int(e.RetryAfter / time.Second)
			}
		case *errs.ConfigError:
			typed.Code, typed.Retryable = numericCode, e.Retryable
			typed.Cause = e.Cause
		case *errs.InternalError:
			typed.Code, typed.Retryable = numericCode, e.Retryable
			typed.Cause = e.Cause
		case *errs.PermissionError:
			typed.ServerCode, typed.Code, typed.Retryable = serverCode, numericCode, e.Retryable
			typed.Cause = e.Cause
		}
		return err
	}
	switch e.Status {
	case http.StatusUnauthorized:
		return withCommon(errs.NewAuthenticationError(errs.SubtypeTokenInvalid, message))
	case http.StatusUnprocessableEntity:
		return withCommon(errs.NewValidationError(errs.SubtypeInvalidParameters, message))
	case http.StatusConflict:
		return withCommon(errs.NewAPIError(errs.SubtypeConflict, message))
	case http.StatusPreconditionFailed:
		return withCommon(errs.NewAPIError(errs.SubtypeFailedPrecondition, message))
	case http.StatusNotFound:
		return withCommon(errs.NewAPIError(errs.SubtypeNotFound, message))
	case http.StatusTooManyRequests:
		return withCommon(errs.NewAPIError(errs.SubtypeRateLimit, message))
	case http.StatusServiceUnavailable:
		return withCommon(errs.NewNetworkError(errs.SubtypeNetworkServer, message).WithRetryable())
	case http.StatusForbidden:
		return withCommon(errs.NewPermissionError(errs.SubtypePermissionDenied, message))
	}
	if e.Status >= 500 {
		return withCommon(errs.NewNetworkError(errs.SubtypeNetworkServer, message).WithRetryable())
	}
	if e.Status == 0 && (serverCode == "invalid_config" || strings.HasPrefix(serverCode, "state_")) {
		return withCommon(errs.NewConfigError(errs.SubtypeInvalidConfig, message))
	}
	if e.Status == 0 && e.Retryable {
		return withCommon(errs.NewNetworkError(errs.SubtypeNetworkTransport, message).WithRetryable())
	}
	return withCommon(errs.NewInternalError(errs.SubtypeUnknown, message))
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("case api request failed (HTTP %d)", e.Status)
	}
	return e.Message
}
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type operation struct {
	Key       string `json:"key"`
	Hash      string `json:"request_hash"`
	Operation string `json:"operation"`
	Resource  string `json:"resource_ref,omitempty"`
	UpdatedAt string `json:"updated_at"`
}
type stateFile struct {
	Version    int         `json:"version"`
	Operations []operation `json:"operations"`
}

func New(options Options) *Client {
	client, err := NewWithError(options)
	if err != nil {
		panic(err)
	}
	return client
}

// NewWithError constructs a client and returns configuration errors instead of
// panicking. New is retained as a concise constructor for trusted callers.
func NewWithError(options Options) (*Client, error) {
	raw := strings.TrimSpace(options.BaseURL)
	if raw == "" {
		raw = worklineauth.ServerURL()
	}
	base, err := url.Parse(raw)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, &Error{Operation: "client.new", Code: "invalid_config", Message: "case service URL is invalid", Cause: err}
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	key := strings.TrimSpace(options.APIKey)
	if key == "" {
		return nil, &Error{Operation: "client.new", Code: "invalid_config", Message: "case service API key is required"}
	}
	h := options.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	retries := options.MaxRetries
	if retries == 0 {
		retries = 2
	}
	if retries < 0 {
		retries = 0
	}
	baseDelay := options.RetryBase
	if baseDelay <= 0 {
		baseDelay = 250 * time.Millisecond
	}
	sleep := options.Sleep
	if sleep == nil {
		sleep = func(d time.Duration) error { time.Sleep(d); return nil }
	}
	statePath := options.StatePath
	if statePath == "" {
		statePath = filepath.Join(core.GetConfigDir(), "case-operations.json")
	}
	return &Client{base: base, apiKey: key, http: h, statePath: statePath, maxRetries: retries, retryBase: baseDelay, sleep: sleep}, nil
}

func (c *Client) CreateCase(ctx context.Context, input CreateCaseRequest) (Case, error) {
	if strings.TrimSpace(input.Purpose) == "" || len(input.SourceScope) == 0 {
		return Case{}, &Error{Operation: "case.create", Status: 422, Code: "invalid_input", Message: "purpose and source_scope are required"}
	}
	body := map[string]any{"contract_version": ContractVersion, "purpose": input.Purpose, "source_scope": input.SourceScope}
	key := input.Key
	if key == "" {
		key = stableKey("case", body)
	}
	hash := hashPayload(body)
	if err := c.reserve("case.create", key, hash); err != nil {
		return Case{}, err
	}
	var out Case
	if err := c.jsonRequest(ctx, http.MethodPost, "/v1/cases", key, body, &out); err != nil {
		return Case{}, err
	}
	if err := c.complete("case.create", key, out.CaseRef); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) SubmitEvidenceBundle(ctx context.Context, caseRef string, input EvidenceBundle) (EvidenceBundleResult, error) {
	if strings.TrimSpace(caseRef) == "" || len(input.Items) == 0 || input.Coverage == nil {
		return EvidenceBundleResult{}, &Error{Operation: "evidence.submit", Status: 422, Code: "invalid_input", Message: "case_ref, coverage, and items are required"}
	}
	body := map[string]any{"contract_version": ContractVersion, "coverage": input.Coverage, "items": input.Items}
	if input.CollectionStartedAt != "" {
		body["collection_started_at"] = input.CollectionStartedAt
	}
	if input.CollectionEndedAt != "" {
		body["collection_ended_at"] = input.CollectionEndedAt
	}
	if len(input.Relations) > 0 {
		body["relations"] = input.Relations
	}
	key := input.Key
	if key == "" {
		key = stableKey("bundle:"+caseRef, body)
	}
	hash := hashPayload(body)
	if err := c.reserve("evidence.submit", key, hash); err != nil {
		return EvidenceBundleResult{}, err
	}
	var out EvidenceBundleResult
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/evidence-bundles"
	if err := c.jsonRequest(ctx, http.MethodPost, path, key, body, &out); err != nil {
		return EvidenceBundleResult{}, err
	}
	if err := c.complete("evidence.submit", key, out.BundleRef); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) SealEvidenceBundle(ctx context.Context, caseRef, bundleRef string) (map[string]any, error) {
	if caseRef == "" || bundleRef == "" {
		return nil, &Error{Operation: "evidence.seal", Status: 422, Code: "invalid_input", Message: "case_ref and bundle_ref are required"}
	}
	body := map[string]any{"case_ref": caseRef, "bundle_ref": bundleRef}
	key := stableKey("seal", body)
	if err := c.reserve("evidence.seal", key, hashPayload(body)); err != nil {
		return nil, err
	}
	var out map[string]any
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/evidence-bundles/" + url.PathEscape(bundleRef) + "/seal"
	if err := c.jsonRequestNoRetry(ctx, http.MethodPost, path, "", map[string]any{}, &out); err != nil {
		if apiErr, ok := err.(*Error); ok && apiErr.Retryable {
			if recovered, recoveryErr := c.recoverSealedBundle(ctx, caseRef, bundleRef); recoveryErr == nil && recovered != nil {
				if completeErr := c.complete("evidence.seal", key, bundleRef); completeErr != nil {
					return recovered, completeErr
				}
				return recovered, nil
			}
		}
		return nil, err
	}
	if err := c.complete("evidence.seal", key, bundleRef); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) recoverSealedBundle(ctx context.Context, caseRef, bundleRef string) (map[string]any, error) {
	caseData, err := c.GetCase(ctx, caseRef)
	if err != nil {
		return nil, err
	}
	evidence, _ := caseData["evidence"].(map[string]any)
	bundles, _ := evidence["bundles"].([]any)
	for _, raw := range bundles {
		bundle, _ := raw.(map[string]any)
		if fmt.Sprint(bundle["bundle_ref"]) == bundleRef && fmt.Sprint(bundle["status"]) == "sealed" {
			return map[string]any{"case_id": caseRef, "bundle_ref": bundleRef, "case_revision": caseData["revision"], "status": "sealed", "recovered": true}, nil
		}
	}
	return nil, nil
}

func (c *Client) StartInferenceRun(ctx context.Context, caseRef string, input InferenceRunRequest) (InferenceRun, error) {
	if caseRef == "" || input.BaseCaseRevision < 0 || strings.TrimSpace(input.Pipeline) == "" {
		return InferenceRun{}, &Error{Operation: "run.start", Status: 422, Code: "invalid_input", Message: "case_ref, non-negative revision, and pipeline are required"}
	}
	body := map[string]any{"contract_version": ContractVersion, "base_case_revision": input.BaseCaseRevision, "pipeline": input.Pipeline}
	key := input.Key
	if key == "" {
		key = stableKey("run:"+caseRef, body)
	}
	hash := hashPayload(body)
	if err := c.reserve("run.start", key, hash); err != nil {
		return InferenceRun{}, err
	}
	var out InferenceRun
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/inference-runs"
	if err := c.jsonRequest(ctx, http.MethodPost, path, key, body, &out); err != nil {
		return InferenceRun{}, err
	}
	if err := c.complete("run.start", key, out.RunRef); err != nil {
		return out, err
	}
	return out, nil
}

// UploadMedia buffers one exported media file so an uncertain request can be
// retried with the identical content and idempotency key.
func (c *Client) UploadMedia(ctx context.Context, file MediaFile) (MediaUpload, error) {
	if strings.TrimSpace(file.Name) == "" || strings.TrimSpace(file.MIME) == "" || file.Reader == nil {
		return MediaUpload{}, &Error{Operation: "media.upload", Status: 422, Code: "media_not_exported", Message: "media name, MIME type, and content are required"}
	}
	content, err := io.ReadAll(io.LimitReader(file.Reader, (100<<20)+1))
	if err != nil {
		return MediaUpload{}, &Error{Operation: "media.upload", Code: "media_not_exported", Message: "read exported media failed", Cause: err}
	}
	if len(content) > 100<<20 {
		return MediaUpload{}, &Error{Operation: "media.upload", Status: 422, Code: "media_not_exported", Message: "exported media exceeds 100 MiB"}
	}
	key := stableKey("media", map[string]any{"name": file.Name, "mime": file.MIME, "content_hash": hashBytes(content)})
	form, contentType, err := multipartBody("file", file.Name, file.MIME, content)
	if err != nil {
		return MediaUpload{}, &Error{Operation: "media.upload", Code: "encode_request", Message: "encode media upload failed", Cause: err}
	}
	var out MediaUpload
	if err := c.multipartRequest(ctx, http.MethodPost, "/v1/media", key, form, contentType, &out, true); err != nil {
		return MediaUpload{}, err
	}
	return out, nil
}

// UploadMediaBatch uploads each item independently. Independent items make a
// partial response restartable: successful files remain reusable by content
// hash while failed items can be retried without replaying the whole batch.
func (c *Client) UploadMediaBatch(ctx context.Context, files []MediaFile) (MediaBatch, error) {
	if len(files) == 0 {
		return MediaBatch{}, &Error{Operation: "media.batch", Status: 422, Code: "invalid_input", Message: "at least one media file is required"}
	}
	result := MediaBatch{Status: "succeeded", Total: len(files), Items: make([]map[string]any, 0, len(files))}
	for index, file := range files {
		upload, err := c.UploadMedia(ctx, file)
		item := map[string]any{"index": index, "filename": file.Name}
		if err != nil {
			item["status"] = "failed"
			item["error"] = errorDetails(err)
			result.Status = "failed"
			result.Items = append(result.Items, item)
			continue
		}
		item["status"], item["media_ref"], item["task_ref"], item["reused"] = upload.Status, upload.MediaRef, upload.TaskRef, upload.Reused
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func multipartBody(field, name, mimeType string, content []byte) ([]byte, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filepath.Base(name)))
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(content); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
func errorDetails(err error) map[string]any {
	if apiErr, ok := err.(*Error); ok {
		return map[string]any{"code": apiErr.Code, "message": apiErr.Message, "status": apiErr.Status, "retryable": apiErr.Retryable}
	}
	return map[string]any{"code": "media_upload_failed", "message": err.Error(), "retryable": false}
}

func (c *Client) GetCase(ctx context.Context, caseRef string) (map[string]any, error) {
	var out map[string]any
	err := c.jsonRequest(ctx, http.MethodGet, "/v1/cases/"+url.PathEscape(caseRef), "", nil, &out)
	return out, err
}
func (c *Client) GetEvidence(ctx context.Context, caseRef string) (map[string]any, error) {
	var out map[string]any
	err := c.jsonRequest(ctx, http.MethodGet, "/v1/cases/"+url.PathEscape(caseRef)+"/evidence", "", nil, &out)
	return out, err
}
func (c *Client) GetRun(ctx context.Context, caseRef, runRef string) (map[string]any, error) {
	var out map[string]any
	err := c.jsonRequest(ctx, http.MethodGet, "/v1/cases/"+url.PathEscape(caseRef)+"/inference-runs/"+url.PathEscape(runRef), "", nil, &out)
	return out, err
}
func (c *Client) GetInterpretation(ctx context.Context, caseRef, view string) (map[string]any, error) {
	return c.GetInterpretationQuery(ctx, caseRef, view, nil)
}
func (c *Client) GetInterpretationQuery(ctx context.Context, caseRef, view string, query url.Values) (map[string]any, error) {
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/interpretation"
	if view != "" {
		query = cloneQuery(query)
		query.Set("view", view)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out map[string]any
	err := c.jsonRequest(ctx, http.MethodGet, path, "", nil, &out)
	return out, err
}

func cloneQuery(query url.Values) url.Values {
	if query == nil {
		return make(url.Values)
	}
	out := make(url.Values, len(query))
	for key, values := range query {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func (c *Client) jsonRequest(ctx context.Context, method, path, idempotencyKey string, payload any, out any) error {
	return c.jsonRequestRetry(ctx, method, path, idempotencyKey, payload, out, true)
}

func (c *Client) jsonRequestNoRetry(ctx context.Context, method, path, idempotencyKey string, payload any, out any) error {
	return c.jsonRequestRetry(ctx, method, path, idempotencyKey, payload, out, false)
}

func (c *Client) jsonRequestRetry(ctx context.Context, method, path, idempotencyKey string, payload any, out any, retry bool) error {
	var raw []byte
	var err error
	if payload != nil {
		raw, err = json.Marshal(payload)
		if err != nil {
			return &Error{Operation: method + " " + path, Code: "encode_request", Message: "encode request failed", Cause: err}
		}
	}
	for attempt := 0; ; attempt++ {
		reqURL, parseErr := c.base.Parse(strings.TrimPrefix(path, "/"))
		if parseErr != nil {
			return &Error{Operation: method + " " + path, Code: "invalid_url", Message: "case service URL is invalid", Cause: parseErr}
		}
		req, buildErr := http.NewRequestWithContext(ctx, method, reqURL.String(), bytes.NewReader(raw))
		if buildErr != nil {
			return &Error{Operation: method + " " + path, Code: "build_request", Message: "build case request failed", Cause: buildErr}
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		resp, doErr := c.http.Do(req)
		if doErr != nil {
			if retry && attempt < c.maxRetries {
				if err := c.sleep(c.retryBase * time.Duration(attempt+1)); err != nil {
					return &Error{Operation: method + " " + path, Code: "retry_interrupted", Message: "retry interrupted", Cause: err}
				}
				continue
			}
			return &Error{Operation: method + " " + path, Code: "network_transport", Message: "connect to case service failed", Retryable: true, Cause: doErr}
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return &Error{Operation: method + " " + path, Status: resp.StatusCode, Code: "network_read", Message: "read case service response failed", Retryable: true, Cause: readErr}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			e := decodeError(method+" "+path, resp, body)
			if retry && e.Retryable && attempt < c.maxRetries {
				delay := e.RetryAfter
				if delay <= 0 {
					delay = c.retryBase * time.Duration(attempt+1)
				}
				if err := c.sleep(delay); err != nil {
					e.Cause = err
					return e
				}
				continue
			}
			return e
		}
		if out == nil || len(bytes.TrimSpace(body)) == 0 {
			return nil
		}
		if err := json.Unmarshal(body, out); err != nil {
			return &Error{Operation: method + " " + path, Status: resp.StatusCode, Code: "invalid_response", Message: "case service returned invalid JSON", Cause: err}
		}
		return nil
	}
}

func (c *Client) multipartRequest(ctx context.Context, method, path, idempotencyKey string, body []byte, contentType string, out any, retry bool) error {
	for attempt := 0; ; attempt++ {
		reqURL, err := c.base.Parse(strings.TrimPrefix(path, "/"))
		if err != nil {
			return &Error{Operation: method + " " + path, Code: "invalid_url", Message: "case service URL is invalid", Cause: err}
		}
		req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bytes.NewReader(body))
		if err != nil {
			return &Error{Operation: method + " " + path, Code: "build_request", Message: "build media request failed", Cause: err}
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		resp, err := c.http.Do(req)
		if err != nil {
			if retry && attempt < c.maxRetries {
				if sleepErr := c.sleep(c.retryBase * time.Duration(attempt+1)); sleepErr != nil {
					return &Error{Operation: method + " " + path, Code: "retry_interrupted", Message: "retry interrupted", Cause: sleepErr}
				}
				continue
			}
			return &Error{Operation: method + " " + path, Code: "network_transport", Message: "connect to case service failed", Retryable: true, Cause: err}
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return &Error{Operation: method + " " + path, Status: resp.StatusCode, Code: "network_read", Message: "read case service response failed", Retryable: true, Cause: readErr}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			apiErr := decodeError(method+" "+path, resp, raw)
			if retry && apiErr.Retryable && attempt < c.maxRetries {
				delay := apiErr.RetryAfter
				if delay <= 0 {
					delay = c.retryBase * time.Duration(attempt+1)
				}
				if sleepErr := c.sleep(delay); sleepErr != nil {
					apiErr.Cause = sleepErr
					return apiErr
				}
				continue
			}
			return apiErr
		}
		if out != nil && len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return &Error{Operation: method + " " + path, Status: resp.StatusCode, Code: "invalid_response", Message: "case service returned invalid JSON", Cause: err}
			}
		}
		return nil
	}
}

func decodeError(operation string, resp *http.Response, body []byte) *Error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	message := strings.TrimSpace(envelope.Error.Message)
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	e := &Error{Operation: operation, Status: resp.StatusCode, Code: envelope.Error.Code, Message: message}
	if e.Code == "" {
		e.Code = "http_" + strconv.Itoa(resp.StatusCode)
	}
	switch resp.StatusCode {
	case 429, 503:
		e.Retryable = true
	case 409, 412, 422, 401, 403:
		e.Retryable = false
	default:
		e.Retryable = resp.StatusCode >= 500
	}
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
			e.RetryAfter = time.Duration(seconds) * time.Second
		}
	}
	return e
}

func stableKey(prefix string, payload any) string { return prefix + ":" + hashPayload(payload) }
func hashPayload(payload any) string {
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (c *Client) reserve(op, key, hash string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	s, err := c.loadState()
	if err != nil {
		return err
	}
	for _, item := range s.Operations {
		if item.Operation == op && item.Key == key && item.Hash != hash {
			return &Error{Operation: op, Status: 409, Code: "idempotency_conflict", Message: "idempotency key is already associated with a different payload"}
		}
	}
	for _, item := range s.Operations {
		if item.Operation == op && item.Key == key {
			return nil
		}
	}
	s.Operations = append(s.Operations, operation{Operation: op, Key: key, Hash: hash, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	return c.saveState(s)
}
func (c *Client) complete(op, key, ref string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	unlock, err := c.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	s, err := c.loadState()
	if err != nil {
		return err
	}
	for i := range s.Operations {
		if s.Operations[i].Operation == op && s.Operations[i].Key == key {
			s.Operations[i].Resource = ref
			s.Operations[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
	}
	return c.saveState(s)
}

func (c *Client) lockState() (func(), error) {
	if err := vfs.MkdirAll(filepath.Dir(c.statePath), 0o700); err != nil {
		return nil, &Error{Operation: "state", Code: "state_persist", Message: "create case state directory failed", Cause: err}
	}
	lock := lockfile.New(c.statePath + ".lock")
	for attempt := 0; attempt < 5; attempt++ {
		if err := lock.TryLock(); err == nil {
			return func() { _ = lock.Unlock() }, nil
		} else if !errors.Is(err, lockfile.ErrHeld) {
			return nil, &Error{Operation: "state", Code: "state_lock_failed", Message: "open case state lock failed", Retryable: true, Cause: err}
		} else if attempt < 4 {
			time.Sleep(10 * time.Millisecond)
		} else {
			return nil, &Error{Operation: "state", Code: "state_locked", Message: "case state is busy", Retryable: true, Cause: err}
		}
	}
	return nil, &Error{Operation: "state", Code: "state_locked", Message: "case state is busy", Retryable: true}
}
func (c *Client) loadState() (stateFile, error) {
	raw, err := vfs.ReadFile(c.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stateFile{Version: 1}, nil
		}
		return stateFile{}, err
	}
	var s stateFile
	if err := json.Unmarshal(raw, &s); err != nil {
		return stateFile{}, err
	}
	if s.Version == 0 {
		s.Version = 1
	}
	return s, nil
}
func (c *Client) saveState(s stateFile) error {
	if err := vfs.MkdirAll(filepath.Dir(c.statePath), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return validate.AtomicWrite(c.statePath, append(raw, '\n'), 0o600)
}
