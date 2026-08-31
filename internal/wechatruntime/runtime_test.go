package wechatruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureInstallsAndKeepsChildUpdate(t *testing.T) {
	t.Setenv(runtimeDirEnv, t.TempDir())
	original := loadEmbeddedRuntime
	t.Cleanup(func() { loadEmbeddedRuntime = original })

	loadEmbeddedRuntime = func() ([]byte, string, bool) { return []byte("version-one"), "wechat-cli", true }
	path, err := Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "version-one" {
		t.Fatalf("installed content = %q, %v", got, err)
	}

	if err := os.WriteFile(path, []byte("child-update"), 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := Ensure()
	if err != nil || second != path {
		t.Fatalf("Ensure() after child update = %q, %v", second, err)
	}
	if got, _ := os.ReadFile(second); string(got) != "child-update" {
		t.Fatalf("child update was overwritten: %q", got)
	}
}

func TestEnsureUsesNewEmbeddedVersion(t *testing.T) {
	t.Setenv(runtimeDirEnv, t.TempDir())
	original := loadEmbeddedRuntime
	t.Cleanup(func() { loadEmbeddedRuntime = original })

	loadEmbeddedRuntime = func() ([]byte, string, bool) { return []byte("old"), "wechat-cli", true }
	oldPath, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	loadEmbeddedRuntime = func() ([]byte, string, bool) { return []byte("new"), "wechat-cli", true }
	newPath, err := Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if oldPath == newPath || filepath.Dir(oldPath) == filepath.Dir(newPath) {
		t.Fatalf("new embedded runtime reused old path: %q", newPath)
	}
	if got, _ := os.ReadFile(newPath); string(got) != "new" {
		t.Fatalf("new content = %q", got)
	}
}
