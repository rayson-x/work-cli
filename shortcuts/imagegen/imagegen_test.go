// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package imagegen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestImageClientUploadsWaitsAndDownloadsLocalFile(t *testing.T) {
	const key = "test-key"
	imageBytes := []byte("generated-image-bytes")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/media":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Fatalf("form file: %v", err)
			}
			defer file.Close()
			if header.Header.Get("Content-Type") != "image/png" {
				t.Fatalf("content type = %q", header.Header.Get("Content-Type"))
			}
			writeJSON(t, w, map[string]any{"media_ref": "med_input", "task_ref": "tsk_observe", "status": "queued", "reused": false})
		case "/v1/image-jobs":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode submit: %v", err)
			}
			refs, _ := request["references"].([]any)
			if len(refs) != 1 {
				t.Fatalf("references = %#v", request["references"])
			}
			writeJSON(t, w, map[string]any{"task_ref": "tsk_image", "status": "queued", "reused": false, "result_ref": nil})
		case "/v1/image-jobs/tsk_image":
			writeJSON(t, w, map[string]any{
				"task_ref": "tsk_image", "status": "succeeded", "stage": "completed", "attempt": 1, "updated_at": "2026-08-31T00:00:00Z",
				"outputs": []any{map[string]any{"output_ref": "out_1", "kind": "final_image", "status": "succeeded", "artifact_ref": "art_1", "media_ref": "med_1", "format": "image/png"}},
			})
		case "/v1/artifacts/art_1":
			writeJSON(t, w, map[string]any{"artifact_ref": "art_1", "type": "generated_image", "media_ref": "med_1", "prompt": "red dress", "mime_type": "image/png", "size_bytes": len(imageBytes)})
		case "/v1/media/med_1/content":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imageBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &apiClient{base: base, key: key}
	input := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(input, []byte("input"), 0o644); err != nil {
		t.Fatal(err)
	}
	mediaRef, err := client.upload(context.Background(), input)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	submitted, err := client.submit(context.Background(), map[string]any{
		"operation": "generate", "prompt": "red dress", "references": []map[string]string{{"media_ref": mediaRef, "role": "style_reference"}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	job, err := client.wait(context.Background(), submitted.TaskRef)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	result, err := client.download(context.Background(), job, t.TempDir())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	outputs, _ := result["outputs"].([]map[string]any)
	if len(outputs) != 1 {
		t.Fatalf("outputs = %#v", result["outputs"])
	}
	path, _ := outputs[0]["path"].(string)
	if !filepath.IsAbs(path) {
		t.Fatalf("path is not absolute: %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded image: %v", err)
	}
	if string(got) != string(imageBytes) {
		t.Fatalf("downloaded bytes = %q", got)
	}
}

func TestValidateImageOptionsMatchesServerContract(t *testing.T) {
	valid := []struct {
		size        string
		format      string
		background  string
		transparent bool
	}{
		{size: ""},
		{size: "auto"},
		{size: "1024x1536", format: "png", background: "transparent"},
		{size: "4000x4000", format: "webp"},
	}
	for _, test := range valid {
		if err := validateImageOptions(test.size, test.format, test.background, test.transparent); err != nil {
			t.Fatalf("valid options %#v: %v", test, err)
		}
	}

	invalid := []struct {
		size        string
		format      string
		background  string
		transparent bool
	}{
		{size: "1024"},
		{size: "0x1024"},
		{size: "4001x4000"},
		{format: "jpeg", background: "transparent"},
		{format: "jpeg", transparent: true},
	}
	for _, test := range invalid {
		if err := validateImageOptions(test.size, test.format, test.background, test.transparent); err == nil {
			t.Fatalf("expected invalid options %#v", test)
		}
	}
}

func TestScriptCommandSurfaceMatchesCodexWorkflow(t *testing.T) {
	wanted := map[string]bool{"generate": false, "edit": false, "generate-batch": false}
	for _, shortcut := range Shortcuts() {
		if _, exists := wanted[shortcut.Command]; exists {
			wanted[shortcut.Command] = true
			if shortcut.Hidden {
				t.Fatalf("canonical command %q is hidden", shortcut.Command)
			}
		}
		if strings.HasPrefix(shortcut.Command, "+") && !shortcut.Hidden {
			t.Fatalf("legacy command %q must be hidden", shortcut.Command)
		}
	}
	for command, found := range wanted {
		if !found {
			t.Fatalf("missing canonical command %q", command)
		}
	}
}

func TestScriptOutputPathsMatchImageGenPyNaming(t *testing.T) {
	root := t.TempDir()
	paths, err := scriptOutputPaths(filepath.Join(root, "dress.png"), "", "png", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "dress-1.png"), filepath.Join(root, "dress-2.png"), filepath.Join(root, "dress-3.png")}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}

	paths, err = scriptOutputPaths("ignored.png", root, "webp", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{filepath.Join(root, "image_1.webp"), filepath.Join(root, "image_2.webp")}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("out-dir paths = %#v, want %#v", paths, want)
	}
}

func TestValidateGPTImage2SizeMatchesCodexScript(t *testing.T) {
	for _, size := range []string{"auto", "1024x1024", "2048x1152", "3840x2160"} {
		if err := validateGPTImage2Size(size); err != nil {
			t.Fatalf("valid size %q: %v", size, err)
		}
	}
	for _, size := range []string{"1024", "1000x1000", "4096x4096", "256x256", "3840x1024"} {
		if err := validateGPTImage2Size(size); err == nil {
			t.Fatalf("expected invalid size %q", size)
		}
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
