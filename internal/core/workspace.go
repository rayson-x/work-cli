// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package core

import (
	"os"
	"path/filepath"

	"github.com/larksuite/cli/internal/vfs"
)

// GetBaseConfigDir returns the single CLI configuration root.
// LARKSUITE_CLI_CONFIG_DIR is the explicit environment override used by tests
// and managed deployments; host-Agent markers never participate in path
// resolution.
func GetBaseConfigDir() string {
	if dir := os.Getenv("LARKSUITE_CLI_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := vfs.UserHomeDir()
	if err != nil || home == "" {
		home = ""
	}
	return filepath.Join(home, ".lark-cli")
}

// GetRuntimeDir returns the same single configuration root for every host.
func GetRuntimeDir() string {
	return GetBaseConfigDir()
}
