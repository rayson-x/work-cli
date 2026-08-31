// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package core

import (
	"path/filepath"
	"testing"
)

func TestConfigPathIgnoresAgentHostMarkers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", dir)
	t.Setenv("HERMES_HOME", t.TempDir())
	t.Setenv("OPENCLAW_HOME", t.TempDir())
	t.Setenv("LARK_CHANNEL", "1")

	if got := GetRuntimeDir(); got != dir {
		t.Fatalf("GetRuntimeDir() = %q, want fixed root %q", got, dir)
	}
	if got := GetConfigDir(); got != dir {
		t.Fatalf("GetConfigDir() = %q, want fixed root %q", got, dir)
	}
	want := filepath.Join(dir, "config.json")
	if got := GetConfigPath(); got != want {
		t.Fatalf("GetConfigPath() = %q, want %q", got, want)
	}
}
