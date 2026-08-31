package media

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/core"
	"github.com/larksuite/cli/internal/worklineauth"
	"github.com/stretchr/testify/require"
)

func TestResolveUploadsWaitsAndFetchesImageArtifact(t *testing.T) {
	t.Helper()
	var seenUpload, seenTask, seenArtifact bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer local-key", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/media":
			seenUpload = true
			require.Equal(t, http.MethodPost, r.Method)
			require.NoError(t, r.ParseMultipartForm(1<<20))
			file, _, err := r.FormFile("file")
			require.NoError(t, err)
			defer file.Close()
			_, err = io.ReadAll(file)
			require.NoError(t, err)
			writeJSON(w, map[string]any{"media_ref": "media/1", "task_ref": "task/1", "reused": false})
		case "/v1/tasks/task/1":
			seenTask = true
			writeJSON(w, map[string]any{"task_ref": "task/1", "status": "succeeded", "result_ref": "artifact/1"})
		case "/v1/artifacts/artifact/1":
			seenArtifact = true
			writeJSON(w, map[string]any{"artifact_ref": "artifact/1", "summary": "blue shirt"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	file := filepath.Join(t.TempDir(), "sample.png")
	require.NoError(t, os.WriteFile(file, []byte("png"), 0o600))
	c := testClient(t, server.URL)
	result, err := c.resolve(context.Background(), file, "")
	require.NoError(t, err)
	require.True(t, seenUpload)
	require.True(t, seenTask)
	require.True(t, seenArtifact)
	require.Equal(t, "image_observation", result["kind"])
	require.Equal(t, "media/1", result["media_ref"])
}

func TestTranscribeUploadsWaitsAndFetchesTranscript(t *testing.T) {
	t.Helper()
	var seenUpload, seenTask, seenTranscript bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer local-key", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/audio/transcriptions":
			seenUpload = true
			require.Equal(t, http.MethodPost, r.Method)
			require.NoError(t, r.ParseMultipartForm(1<<20))
			file, header, err := r.FormFile("file")
			require.NoError(t, err)
			defer file.Close()
			require.Equal(t, "audio/mpeg", header.Header.Get("Content-Type"))
			_, err = io.ReadAll(file)
			require.NoError(t, err)
			writeJSON(w, map[string]any{"media_ref": "media/audio-1", "task_ref": "task/audio-1", "reused": false})
		case "/v1/tasks/task/audio-1":
			seenTask = true
			writeJSON(w, map[string]any{"task_ref": "task/audio-1", "status": "succeeded", "result_ref": "artifact/transcript-1"})
		case "/v1/media/media/audio-1/transcript":
			seenTranscript = true
			writeJSON(w, map[string]any{"media_ref": "media/audio-1", "segments": []any{map[string]any{"text": "adjust the cuff"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	file := filepath.Join(t.TempDir(), "voice.mp3")
	require.NoError(t, os.WriteFile(file, []byte("mp3"), 0o600))
	result, err := testClient(t, server.URL).transcribe(context.Background(), file, "")
	require.NoError(t, err)
	require.True(t, seenUpload)
	require.True(t, seenTask)
	require.True(t, seenTranscript)
	require.Equal(t, "transcript", result["kind"])
	require.Equal(t, "media/audio-1", result["media_ref"])
}

func TestObservePostsSelectorAndWaitsForArtifact(t *testing.T) {
	var gotSelector map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/media/media-1/observations":
			var body map[string]map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			gotSelector = body["selector"]
			writeJSON(w, map[string]any{"task_ref": "task-1", "reused": true, "result_ref": "artifact-1"})
		case "/v1/tasks/task-1":
			writeJSON(w, map[string]any{"task_ref": "task-1", "status": "succeeded", "result_ref": "artifact-1"})
		case "/v1/artifacts/artifact-1":
			writeJSON(w, map[string]any{"artifact_ref": "artifact-1", "answer": "confirmed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := testClient(t, server.URL)
	result, err := c.observe(context.Background(), "media-1", map[string]any{"type": "interval", "start_at_ms": int64(10), "end_at_ms": int64(20)})
	require.NoError(t, err)
	require.Equal(t, "interval", gotSelector["type"])
	require.Equal(t, true, result["reused"])
}

func TestResolveBatchUploadsImagesAndWaits(t *testing.T) {
	var uploadCount int
	var batchReads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/media/batches":
			require.Equal(t, http.MethodPost, r.Method)
			require.NoError(t, r.ParseMultipartForm(1<<20))
			require.Len(t, r.MultipartForm.File["images"], 2)
			uploadCount++
			writeJSON(w, map[string]any{"batch_ref": "batch-1", "pending": 1, "items": []any{}})
		case "/v1/media/batches/batch-1":
			batchReads++
			writeJSON(w, map[string]any{"batch_ref": "batch-1", "pending": 0, "status": "succeeded", "items": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	first := filepath.Join(dir, "first.png")
	second := filepath.Join(dir, "second.jpg")
	require.NoError(t, os.WriteFile(first, []byte("first"), 0o600))
	require.NoError(t, os.WriteFile(second, []byte("second"), 0o600))

	result, err := testClient(t, server.URL).resolveBatch(context.Background(), []string{first, second})
	require.NoError(t, err)
	require.Equal(t, 1, uploadCount)
	require.Equal(t, 1, batchReads)
	batch := result.(map[string]any)
	require.Equal(t, "succeeded", batch["status"])
}

func TestRequestMapsServiceErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "bad key"}})
	}))
	defer server.Close()
	_, err := testClient(t, server.URL).artifact(context.Background(), "artifact-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "bad key")
}

func TestCommandUsesEnvironmentKeyWithoutFeishuLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer environment-key", r.Header.Get("Authorization"))
		writeJSON(w, map[string]any{"task_ref": "task-1", "status": "queued"})
	}))
	defer server.Close()
	t.Setenv(worklineauth.MediaAPIKeyEnv, "environment-key")
	factory, stdout, _, _ := cmdutil.TestFactory(t, &core.CliConfig{AppID: "app-test"})
	factory.HttpClient = func() (*http.Client, error) { return server.Client(), nil }
	command := NewCmdMedia(factory)
	command.SetArgs([]string{"task", "task-1", "--server-url", server.URL})
	require.NoError(t, command.Execute())
	var envelope map[string]any
	require.NoError(t, json.NewDecoder(bytes.NewReader(stdout.Bytes())).Decode(&envelope))
	require.Equal(t, true, envelope["ok"])
	require.Equal(t, "queued", envelope["data"].(map[string]any)["status"])
}

func TestTransportAndSelectorValidation(t *testing.T) {
	_, err := parseBaseURL("http://example.test", false)
	require.Error(t, err)
	_, err = parseBaseURL("http://127.0.0.1:3000", false)
	require.NoError(t, err)
	_, err = parseBaseURL(worklineauth.DefaultMediaServerURL, false)
	require.NoError(t, err)
	_, err = observationSelector(0, -1, "", "", "", "")
	require.Error(t, err)
	selector, err := observationSelector(-1, -1, "segment-1", "quoted", "", "")
	require.NoError(t, err)
	require.Equal(t, "quote", selector["type"])
	_, err = audioMIME("voice.mp3", "")
	require.NoError(t, err)
	_, err = audioMIME("voice.mp3", "video/mp4")
	require.Error(t, err)
}

func testClient(t *testing.T, rawURL string) *client {
	t.Helper()
	base, err := parseBaseURL(rawURL, true)
	require.NoError(t, err)
	return &client{base: base, key: "local-key", http: &http.Client{Timeout: time.Second}, poll: time.Millisecond, timeout: time.Second}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
