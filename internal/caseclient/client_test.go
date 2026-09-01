// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package caseclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestCreateCaseUsesStableIdempotencyAndCaseContract(t *testing.T) {
	var requests int
	var firstKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cases" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer case-key" {
			t.Fatalf("authorization = %q", got)
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			t.Fatal("missing idempotency key")
		}
		if firstKey == "" {
			firstKey = key
		} else if key != firstKey {
			t.Fatalf("idempotency key changed: %q then %q", firstKey, key)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["contract_version"] != "case.v1" || body["purpose"] != "style-track" {
			t.Fatalf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"case_id":"case-1","purpose":"style-track","status":"open","revision":0,"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z","disposition":"created"}`))
	}))
	defer server.Close()

	c := New(Options{BaseURL: server.URL, APIKey: "case-key", HTTP: server.Client(), StatePath: t.TempDir() + "/case-operations.json"})
	input := CreateCaseRequest{Purpose: "style-track", SourceScope: map[string]any{"platform": "wechat"}}
	first, err := c.CreateCase(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.CreateCase(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.CaseRef != "case-1" || second.CaseRef != first.CaseRef {
		t.Fatalf("results = %#v %#v", first, second)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestAPIErrorCarriesRetryHintAndDoesNotExposeToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"try later"}}`))
	}))
	defer server.Close()

	c := New(Options{BaseURL: server.URL, APIKey: "do-not-leak", HTTP: server.Client(), StatePath: t.TempDir() + "/case-operations.json"})
	_, err := c.GetCase(context.Background(), "case-1")
	apiErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error = %T %v, want *Error", err, err)
	}
	if apiErr.Status != http.StatusTooManyRequests || !apiErr.Retryable || apiErr.RetryAfter != 3*1e9 {
		t.Fatalf("error = %#v", apiErr)
	}
	if got := apiErr.Error(); got == "" || contains(got, "do-not-leak") {
		t.Fatalf("error leaks or is empty: %q", got)
	}
}

func TestEvidenceSealRunAndReadsUseCaseContractRoutes(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/cases/case-1/evidence-bundles":
			_, _ = w.Write([]byte(`{"bundle_ref":"bundle-1","case_ref":"case-1","case_revision":1,"status":"accepted","evidence_refs":{},"warnings":[]}`))
		case "/v1/cases/case-1/evidence-bundles/bundle-1/seal":
			_, _ = w.Write([]byte(`{"case_id":"case-1","bundle_ref":"bundle-1","case_revision":2,"status":"sealed"}`))
		case "/v1/cases/case-1/inference-runs":
			_, _ = w.Write([]byte(`{"run_ref":"run-1","case_ref":"case-1","base_case_revision":2,"status":"running"}`))
		case "/v1/cases/case-1":
			_, _ = w.Write([]byte(`{"case_id":"case-1","status":"open"}`))
		case "/v1/cases/case-1/evidence":
			_, _ = w.Write([]byte(`{"case_id":"case-1","items":[]}`))
		case "/v1/cases/case-1/inference-runs/run-1":
			_, _ = w.Write([]byte(`{"run_ref":"run-1","status":"succeeded"}`))
		case "/v1/cases/case-1/interpretation":
			if r.URL.Query().Get("view") != "audit" {
				t.Fatalf("view = %q", r.URL.Query().Get("view"))
			}
			_, _ = w.Write([]byte(`{"case_id":"case-1","view":"audit"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := New(Options{BaseURL: server.URL, APIKey: "case-key", HTTP: server.Client(), StatePath: t.TempDir() + "/case-operations.json", RetryBase: time.Millisecond})
	item := EvidenceItem{ClientEvidenceKey: "e-1", SourceKey: "wechat:m-1", Kind: "message", SourceLocator: map[string]any{"message_id": "m-1"}, ImmutablePayload: map[string]any{"text": "hello"}}
	if _, err := c.SubmitEvidenceBundle(context.Background(), "case-1", EvidenceBundle{Coverage: map[string]any{"source": "wechat"}, Items: []EvidenceItem{item}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SealEvidenceBundle(context.Background(), "case-1", "bundle-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartInferenceRun(context.Background(), "case-1", InferenceRunRequest{BaseCaseRevision: 2, Pipeline: "style-track"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetCase(context.Background(), "case-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetEvidence(context.Background(), "case-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetRun(context.Background(), "case-1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetInterpretation(context.Background(), "case-1", "audit"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 7 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestSealDoesNotRetryNonIdempotentPostAndRecoversFromCaseRead(t *testing.T) {
	var sealRequests, sleeps, caseReads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/cases/case-1/evidence-bundles/bundle-1/seal" {
			sealRequests++
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"case_store_unavailable","message":"temporary"}}`))
			return
		}
		if r.URL.Path == "/v1/cases/case-1" {
			caseReads++
			_, _ = w.Write([]byte(`{"case_id":"case-1","revision":3,"evidence":{"bundles":[{"bundle_ref":"bundle-1","status":"sealed"}]}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	c := New(Options{BaseURL: server.URL, APIKey: "case-key", HTTP: server.Client(), StatePath: t.TempDir() + "/case-operations.json", MaxRetries: 4, Sleep: func(time.Duration) error { sleeps++; return nil }})
	result, err := c.SealEvidenceBundle(context.Background(), "case-1", "bundle-1")
	if err != nil {
		t.Fatal(err)
	}
	if result["recovered"] != true || sealRequests != 1 || caseReads != 1 || sleeps != 0 {
		t.Fatalf("result=%#v seal=%d reads=%d sleeps=%d", result, sealRequests, caseReads, sleeps)
	}
}

func TestConcurrentClientsKeepPersistentOperationStateValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"case_id":"case-1","purpose":"style-track","status":"open","revision":0}`))
	}))
	defer server.Close()
	statePath := t.TempDir() + "/case-operations.json"
	input := CreateCaseRequest{Purpose: "style-track", SourceScope: map[string]any{"platform": "wechat"}}
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = New(Options{BaseURL: server.URL, APIKey: "case-key", HTTP: server.Client(), StatePath: statePath, RetryBase: time.Millisecond}).CreateCase(context.Background(), input)
		}()
	}
	group.Wait()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state stateFile
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("state is invalid JSON: %v", err)
	}
	if len(state.Operations) != 1 || state.Operations[0].Hash == "" {
		t.Fatalf("state = %#v", state)
	}
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
