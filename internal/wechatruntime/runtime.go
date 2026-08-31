// Package wechatruntime materializes the bundled local WeChat reader.
package wechatruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const runtimeDirEnv = "WORK_CLI_WECHAT_RUNTIME_DIR"

// loadEmbeddedRuntime is a seam for testing installation behavior without a
// platform-specific executable. Production builds always use embeddedRuntime.
var loadEmbeddedRuntime = embeddedRuntime

type state struct {
	EmbeddedSHA256 string `json:"embedded_sha256"`
}

// Ensure returns the private executable for the currently bundled WeChat CLI.
// A child self-update is intentionally retained until the parent binary changes
// its embedded payload, at which point the new bundled baseline replaces it.
func Ensure() (string, error) {
	payload, filename, supported := loadEmbeddedRuntime()
	if !supported || len(payload) == 0 {
		return "", fmt.Errorf("local WeChat reading is not bundled for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	digest := sha256.Sum256(payload)
	embeddedHash := hex.EncodeToString(digest[:])
	dir, err := runtimeDirectory(embeddedHash)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	statePath := filepath.Join(dir, "runtime.json")
	current, _ := readState(statePath)
	if current.EmbeddedSHA256 == embeddedHash {
		if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
			return path, nil
		}
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create WeChat runtime directory: %w", err)
	}
	if err := writeAtomically(path, payload, executableMode()); err != nil {
		return "", fmt.Errorf("install bundled WeChat runtime: %w", err)
	}
	if err := writeState(statePath, state{EmbeddedSHA256: embeddedHash}); err != nil {
		return "", fmt.Errorf("record bundled WeChat runtime: %w", err)
	}
	return path, nil
}

func runtimeDirectory(hash string) (string, error) {
	if configured := os.Getenv(runtimeDirEnv); configured != "" {
		return filepath.Join(configured, hash), nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache directory: %w", err)
	}
	return filepath.Join(base, "work-cli", "runtime", "wechat", hash), nil
}

func executableMode() os.FileMode {
	if runtime.GOOS == "windows" {
		return 0o600
	}
	return 0o700
}

func readState(path string) (state, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return state{}, err
	}
	var parsed state
	if err := json.Unmarshal(data, &parsed); err != nil {
		return state{}, err
	}
	return parsed, nil
}

func writeState(path string, value state) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeAtomically(path, data, 0o600)
}

func writeAtomically(path string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".runtime-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// Windows does not replace an existing destination with Rename.
		_ = os.Remove(path)
	}
	return os.Rename(temporaryPath, path)
}
