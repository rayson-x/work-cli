// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package casecmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/core"
	"github.com/larksuite/cli/internal/worklineauth"
	"github.com/stretchr/testify/require"
)

func TestMediaUploadCommandUsesExistingAuthAndJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer command-key", r.Header.Get("Authorization"))
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/media", r.URL.Path)
		require.NoError(t, r.ParseMultipartForm(1<<20))
		file, _, err := r.FormFile("file")
		require.NoError(t, err)
		defer file.Close()
		content, err := io.ReadAll(file)
		require.NoError(t, err)
		require.Equal(t, []byte("image"), content)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"media_ref":"media-1","task_ref":"task-1","status":"queued","reused":false}`))
	}))
	defer server.Close()
	t.Setenv(worklineauth.MediaServerURLEnv, server.URL)
	t.Setenv(worklineauth.MediaAPIKeyEnv, "command-key")
	factory, stdout, _, _ := cmdutil.TestFactory(t, &core.CliConfig{AppID: "case-test"})
	factory.HttpClient = func() (*http.Client, error) { return server.Client(), nil }
	path := filepath.Join(t.TempDir(), "sample.png")
	require.NoError(t, os.WriteFile(path, []byte("image"), 0o600))
	cmd := NewCmdCase(factory)
	cmd.SetArgs([]string{"media-upload", path})
	require.NoError(t, cmd.Execute())
	var envelope map[string]any
	require.NoError(t, json.NewDecoder(bytes.NewReader(stdout.Bytes())).Decode(&envelope))
	require.Equal(t, true, envelope["ok"])
}

func TestCaseCommandDoesNotExposeCanonicalWriteCommands(t *testing.T) {
	cmd := NewCmdCase(&cmdutil.Factory{})
	for _, child := range cmd.Commands() {
		if child.Name() == "style" || child.Name() == "person" || child.Name() == "event" {
			t.Fatalf("canonical write command exposed: %s", child.Name())
		}
	}
}
