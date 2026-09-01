// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package casecmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/larksuite/cli/internal/caseclient"
	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/core"
	"github.com/larksuite/cli/internal/worklineauth"
	"github.com/larksuite/cli/internal/output"
)

func TestMediaBatchEmitsPartialFailureAndNonZeroSignal(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/media" {
			http.NotFound(w, r)
			return
		}
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte(`{"media_ref":"media-1","status":"ready","reused":false}`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"media_not_exported","message":"not exported"}}`))
	}))
	defer server.Close()
	t.Setenv(worklineauth.MediaAPIKeyEnv, "media-key")
	factory, stdout, _, _ := cmdutil.TestFactory(t, &core.CliConfig{AppID: "batch-test"})
	factory.HttpClient = func() (*http.Client, error) { return server.Client(), nil }
	dir := t.TempDir()
	first := filepath.Join(dir, "first.png")
	second := filepath.Join(dir, "second.png")
	if err := os.WriteFile(first, []byte("one"), 0o600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(second, []byte("two"), 0o600); err != nil { t.Fatal(err) }
	cmd := NewCmdCase(factory)
	cmd.SetArgs([]string{"--server-url", server.URL, "media-batch", first, second})
	err := cmd.Execute()
	var partial *output.PartialFailureError
	if !errors.As(err, &partial) || partial.Code != output.ExitAPI {
		t.Fatalf("error=%T %v", err, err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil { t.Fatalf("stdout=%q: %v", stdout.String(), err) }
	if envelope["ok"] != false { t.Fatalf("partial envelope=%#v", envelope) }
	if requests != 2 { t.Fatalf("media requests=%d", requests) }
}

func TestCollectUploadsExtensionMIMEAndPreservesStickerKind(t *testing.T) {
	var uploadedTypes []string
	var submitted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/cases":
			_, _ = w.Write([]byte(`{"case_id":"case-media","purpose":"style-track","status":"open","revision":0}`))
		case "/v1/media":
			reader, err := r.MultipartReader()
			if err != nil {
				t.Fatalf("multipart reader: %v", err)
			}
			part, err := reader.NextPart()
			if err != nil {
				t.Fatalf("multipart part: %v", err)
			}
			if part.FormName() != "file" {
				t.Fatalf("multipart field=%q", part.FormName())
			}
			uploadedTypes = append(uploadedTypes, part.Header.Get("Content-Type"))
			_, _ = io.ReadAll(part)
			_, _ = w.Write([]byte(`{"media_ref":"media-` + string(rune('0'+len(uploadedTypes))) + `","status":"ready","reused":false}`))
		case "/v1/cases/case-media/evidence-bundles":
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Fatalf("decode evidence bundle: %v", err)
			}
			_, _ = w.Write([]byte(`{"bundle_ref":"bundle-media","case_ref":"case-media","case_revision":1,"status":"accepted"}`))
		case "/v1/cases/case-media/evidence-bundles/bundle-media/seal":
			_, _ = w.Write([]byte(`{"bundle_ref":"bundle-media","status":"sealed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv(worklineauth.MediaAPIKeyEnv, "media-key")
	factory, _, _, _ := cmdutil.TestFactory(t, &core.CliConfig{AppID: "media-test"})
	factory.HttpClient = func() (*http.Client, error) { return server.Client(), nil }
	mediaPath := filepath.Join(t.TempDir(), "sample.PNG")
	if err := os.WriteFile(mediaPath, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := `{"data":{"messages":[` +
		`{"owner":"owner","conversation_id":"chat","message_id":"m-sticker","timestamp":"2026-09-01T00:00:00Z","message_type":"sticker","resource_path":"` + strings.ReplaceAll(mediaPath, `\`, `/`) + `","resource_status":"available"},` +
		`{"owner":"owner","conversation_id":"chat","message_id":"m-emoji","timestamp":"2026-09-01T00:01:00Z","message_type":"emoji","resource_path":"` + strings.ReplaceAll(mediaPath, `\`, `/`) + `","resource_status":"available"}` +
		`]}}`
	cmd := NewCmdCase(factory)
	cmd.SetIn(bytes.NewBufferString(raw))
	cmd.SetArgs([]string{"--server-url", server.URL, "collect", "--scope-json", `{"owner":"owner","conversation":"chat"}`, "--messages-json", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(uploadedTypes) != 2 || uploadedTypes[0] != "image/png" || uploadedTypes[1] != "image/png" {
		t.Fatalf("uploaded MIME types=%#v", uploadedTypes)
	}
	items, ok := submitted["items"].([]any)
	if !ok || len(items) != 4 {
		t.Fatalf("submitted items=%#v", submitted["items"])
	}
	wantKinds := []string{"sticker", "emoji"}
	seenKinds := map[string]bool{}
	for _, rawItem := range items {
		item := rawItem.(map[string]any)
		if item["kind"] == "message" {
			continue
		}
		if item["kind"] != "image" {
			t.Fatalf("uploaded attachment kind=%#v", item["kind"])
		}
		payload := item["immutable_payload"].(map[string]any)
		kind, _ := payload["attachment_kind"].(string)
		seenKinds[kind] = true
		if item["media_ref"] == nil || item["media_ref"] == "" {
			t.Fatalf("attachment missing media ref=%#v", item)
		}
	}
	for _, kind := range wantKinds {
		if !seenKinds[kind] {
			t.Fatalf("attachment_kind %q was not preserved: %#v", kind, seenKinds)
		}
	}
}

func TestCollectCommandTransportsRawOccurrencesWithoutProductInference(t *testing.T) {
	var submitted map[string]any
	var createdScope map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/cases":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			createdScope = body["source_scope"].(map[string]any)
			_, _ = w.Write([]byte(`{"case_id":"case-1","purpose":"style-track","status":"open","revision":0}`))
		case "/v1/cases/case-1/evidence-bundles":
			_ = json.NewDecoder(r.Body).Decode(&submitted)
			for _, rawItem := range submitted["items"].([]any) {
				item := rawItem.(map[string]any)
				kind := item["kind"].(string)
				if kind != "message" && kind != "image" && kind != "video" && kind != "audio" && kind != "file" {
					t.Fatalf("invalid evidence kind %q", kind)
				}
				if kind != "message" && item["media_ref"] == nil {
					payload := item["immutable_payload"].(map[string]any)
					if payload["export_error"] == "" {
						t.Fatalf("missing media ref without export error: %#v", item)
					}
				}
			}
			_, _ = w.Write([]byte(`{"bundle_ref":"bundle-1","case_ref":"case-1","case_revision":1,"status":"accepted"}`))
		case "/v1/cases/case-1/evidence-bundles/bundle-1/seal":
			_, _ = w.Write([]byte(`{"bundle_ref":"bundle-1","status":"sealed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv(worklineauth.MediaServerURLEnv, server.URL)
	t.Setenv(worklineauth.MediaAPIKeyEnv, "collect-key")
	factory, stdout, _, _ := cmdutil.TestFactory(t, &core.CliConfig{AppID: "collect-test"})
	factory.HttpClient = func() (*http.Client, error) { return server.Client(), nil }
	raw := `{"data":{"messages":[{"owner":"owner","conversation_id":"chat","message_id":"m1","timestamp":"2026-09-01T00:00:00Z","from":{"display_name":"外层转发人"},"content":"两件一起寄出","forward_path":"f/1","attachments":[{"type":"image","ordinal":0,"name":"左右样衣.jpg","local_path":"missing.jpg","content_hash":"h1","coordinates":{"x":0.1,"y":0.2}},{"type":"image","ordinal":1,"name":"左右样衣.jpg","local_path":"missing.jpg","content_hash":"h2","coordinates":{"x":0.5,"y":0.2}}]},{"owner":"owner","conversation_id":"chat","message_id":"m2","timestamp":"2026-09-01T00:01:00Z","speaker_source_key":"forward/no-id","content":"再转一次","attachments":[{"type":"image","ordinal":0,"name":"copy.jpg","content_hash":"h1"}]}]}}`
	cmd := NewCmdCase(factory)
	cmd.SetIn(bytes.NewBufferString(raw))
	cmd.SetArgs([]string{"--server-url", server.URL, "collect", "--scope-json", `{"owner":"owner","conversation":"chat"}`, "--messages-json", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(submitted) == 0 {
		t.Fatal("bundle was not submitted")
	}
	if createdScope["conversation_ref"] != "chat" {
		t.Fatalf("source scope=%#v", createdScope)
	}
	items, ok := submitted["items"].([]any)
	if !ok || len(items) != 5 {
		t.Fatalf("submitted items = %#v", submitted["items"])
	}
	for _, rawItem := range items {
		item := rawItem.(map[string]any)
		payload := item["immutable_payload"].(map[string]any)
		if _, ok := payload["style"]; ok {
			t.Fatal("collector inferred style")
		}
		if _, ok := payload["event"]; ok {
			t.Fatal("collector inferred event")
		}
	}
	coverage := submitted["coverage"].(map[string]any)
	if coverage["complete"] != true || coverage["collector_version"] != "wechat-evidence.v1" {
		t.Fatalf("coverage = %#v", coverage)
	}
	if coverage["media_complete"] != false {
		t.Fatalf("media_complete=%#v", coverage["media_complete"])
	}
	if failures, ok := coverage["media_export_failures"].([]any); !ok || len(failures) != 3 {
		t.Fatalf("media failures=%#v", coverage["media_export_failures"])
	}
	if missing, ok := coverage["missing_reasons"].([]any); !ok || len(missing) != 3 {
		t.Fatalf("missing reasons=%#v", coverage["missing_reasons"])
	}
	failures := coverage["media_export_failures"].([]any)
	unique := map[string]bool{}
	for _, raw := range failures {
		key, ok := raw.(string)
		if !ok || unique[key] {
			t.Fatalf("failure source keys=%#v", failures)
		}
		unique[key] = true
	}
	for _, raw := range coverage["missing_reasons"].([]any) {
		reason := raw.(map[string]any)
		if !unique[reason["source_key"].(string)] {
			t.Fatalf("missing reason not aligned=%#v", reason)
		}
	}
	relations := submitted["relations"].([]any)
	if len(relations) != 4 {
		t.Fatalf("relations = %#v", relations)
	}
	for _, rawRelation := range relations {
		relation := rawRelation.(map[string]any)
		from, to := relation["from_client_evidence_key"].(string), relation["to_client_evidence_key"].(string)
		if from == "" || to == "" {
			t.Fatalf("relation endpoints=%#v", relation)
		}
		if relation["type"] == "attachment_of" && from == to {
			t.Fatalf("self attachment relation=%#v", relation)
		}
	}
	for _, rawItem := range items {
		item := rawItem.(map[string]any)
		source := item["source_key"].(string)
		for _, part := range []string{"platform=wechat", "owner=owner", "conversation=chat", "message="} {
			if !strings.Contains(source, part) {
				t.Fatalf("source key %q missing %q", source, part)
			}
		}
	}
	var output map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output["ok"] != true {
		t.Fatalf("output = %s err=%v", stdout.String(), err)
	}
}

func TestCollectExistingCaseRejectsDifferentConversationBeforeBundleSubmit(t *testing.T) {
	var bundleRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/cases/existing" {
			_, _ = w.Write([]byte(`{"case_id":"existing","source_scope":{"platform":"wechat","owner":"owner","conversation_ref":"other-chat"},"status":"open","revision":1}`))
			return
		}
		if r.URL.Path == "/v1/cases/existing/evidence-bundles" {
			bundleRequests++
			t.Fatalf("bundle submitted after scope mismatch")
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	t.Setenv(worklineauth.MediaAPIKeyEnv, "scope-key")
	factory, _, _, _ := cmdutil.TestFactory(t, &core.CliConfig{AppID: "scope-test"})
	factory.HttpClient = func() (*http.Client, error) { return server.Client(), nil }
	cmd := NewCmdCase(factory)
	cmd.SetIn(bytes.NewBufferString(`{"owner":"owner","conversation_id":"chat","message_id":"m1","timestamp":"2026-09-01T00:00:00Z","text":"x"}`))
	cmd.SetArgs([]string{"--server-url", server.URL, "collect", "--case-ref", "existing", "--scope-json", `{"owner":"owner","conversation":"chat"}`, "--messages-json", "-"})
	err := cmd.Execute()
	apiErr, ok := err.(*caseclient.Error)
	if !ok || apiErr.Status != 422 || apiErr.Code != "scope_mismatch" {
		t.Fatalf("error=%T %#v", err, err)
	}
	if bundleRequests != 0 {
		t.Fatalf("bundle requests=%d", bundleRequests)
	}
}

func TestCollectAbortsBundleOnTransientMediaFailureAndRetriesCompleteOnce(t *testing.T) {
	mediaAttempts, bundleRequests := 0, 0
	var submitted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/cases":
			_, _ = w.Write([]byte(`{"case_id":"case-retry","purpose":"style-track","status":"open","revision":0}`))
		case "/v1/cases/case-retry":
			_, _ = w.Write([]byte(`{"case_id":"case-retry","source_scope":{"platform":"wechat","owner":"owner","conversation_ref":"chat"},"status":"open","revision":0}`))
		case "/v1/media":
			mediaAttempts++
			if mediaAttempts <= 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"code":"temporary","message":"try again"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"media_ref":"media-1","status":"ready","reused":false}`))
		case "/v1/cases/case-retry/evidence-bundles":
			bundleRequests++
			_ = json.NewDecoder(r.Body).Decode(&submitted)
			_, _ = w.Write([]byte(`{"bundle_ref":"bundle-1","case_ref":"case-retry","case_revision":1,"status":"accepted"}`))
		case "/v1/cases/case-retry/evidence-bundles/bundle-1/seal":
			_, _ = w.Write([]byte(`{"bundle_ref":"bundle-1","status":"sealed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv(worklineauth.MediaAPIKeyEnv, "retry-key")
	factory, _, _, _ := cmdutil.TestFactory(t, &core.CliConfig{AppID: "retry-test"})
	factory.HttpClient = func() (*http.Client, error) { return server.Client(), nil }
	mediaPath := filepath.Join(t.TempDir(), "sample.jpg")
	if err := os.WriteFile(mediaPath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := `{"owner":"owner","conversation_id":"chat","message_id":"m1","timestamp":"2026-09-01T00:00:00Z","text":"样衣","attachments":[{"type":"image","ordinal":0,"local_path":"` + strings.ReplaceAll(mediaPath, `\`, `/`) + `","mime_type":"image/jpeg","content_hash":"image-hash"}]}`
	first := NewCmdCase(factory)
	first.SetIn(bytes.NewBufferString(input))
	first.SetArgs([]string{"--server-url", server.URL, "collect", "--scope-json", `{"owner":"owner","conversation":"chat"}`, "--messages-json", "-"})
	if err := first.Execute(); err == nil {
		t.Fatal("transient media failure should abort before bundle submit")
	} else if apiErr, ok := err.(*caseclient.Error); !ok || !apiErr.Retryable {
		t.Fatalf("transient media error=%T %#v", err, err)
	}
	if bundleRequests != 0 {
		t.Fatalf("first bundle requests=%d", bundleRequests)
	}
	second := NewCmdCase(factory)
	second.SetIn(bytes.NewBufferString(input))
	second.SetArgs([]string{"--server-url", server.URL, "collect", "--case-ref", "case-retry", "--scope-json", `{"owner":"owner","conversation":"chat"}`, "--messages-json", "-"})
	if err := second.Execute(); err != nil {
		t.Fatal(err)
	}
	if bundleRequests != 1 || submitted == nil {
		t.Fatalf("bundle requests=%d submitted=%#v", bundleRequests, submitted)
	}
	items := submitted["items"].([]any)
	keys := map[string]bool{}
	for _, raw := range items {
		key := raw.(map[string]any)["source_key"].(string)
		if keys[key] {
			t.Fatalf("duplicate source key=%s", key)
		}
		keys[key] = true
	}
	if len(items) != 2 || items[1].(map[string]any)["media_ref"] != "media-1" {
		t.Fatalf("submitted items=%#v", items)
	}
}
