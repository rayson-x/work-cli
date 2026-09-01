// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package casecmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/core"
	"github.com/larksuite/cli/internal/worklineauth"
)

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
