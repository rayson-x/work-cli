// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package cmdupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/build"
	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/output"
	"github.com/larksuite/cli/internal/update"
)

const (
	worklineRepoURL      = "https://github.com/rayson-x/work-cli"
	worklineDownloadBase = worklineRepoURL + "/releases/download"
	worklineMaxArchive   = 128 << 20
	worklineMaxChecksums = 1 << 20
)

type WorklineUpdateOptions struct {
	Factory *cmdutil.Factory
	JSON    bool
	Force   bool
	Check   bool
}

var worklineUpdateHTTPClient = &http.Client{Timeout: 3 * time.Minute}

// NewCmdWorklineUpdate creates the updater used by the Workline distribution.
// It only consumes signed release metadata and checksum-pinned assets from the
// rayson-x/work-cli GitHub repository.
func NewCmdWorklineUpdate(f *cmdutil.Factory) *cobra.Command {
	opts := &WorklineUpdateOptions{Factory: f}
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update work-cli to the latest release",
		Long: `Update work-cli from the rayson-x/work-cli GitHub Releases page.

The updater selects the current operating system and CPU architecture,
downloads checksums.txt and the matching archive, verifies SHA-256, and then
replaces the current executable. Use --check to inspect availability without
changing the installation.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWorklineUpdate(cmd.Context(), opts)
		},
	}
	cmdutil.DisableAuthCheck(cmd)
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "structured JSON output")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "reinstall even when already up to date")
	cmd.Flags().BoolVar(&opts.Check, "check", false, "check for updates without installing")
	cmdutil.SetRisk(cmd, "high-risk-write")
	return cmd
}

func runWorklineUpdate(ctx context.Context, opts *WorklineUpdateOptions) error {
	latest, err := update.FetchLatest()
	if err != nil {
		return errs.NewNetworkError(errs.SubtypeNetworkTransport, "failed to check work-cli releases: %s", err).WithCause(err)
	}
	current := strings.TrimPrefix(build.Version, "v")
	releaseURL := worklineRepoURL + "/releases/tag/v" + latest
	newer := update.IsNewer(latest, current)
	if opts.Check {
		action := "already_up_to_date"
		if newer {
			action = "update_available"
		}
		return emitWorklineUpdate(opts, map[string]interface{}{
			"ok": true, "action": action, "current_version": current,
			"latest_version": latest, "release_url": releaseURL,
		})
	}
	if !newer && !opts.Force {
		return emitWorklineUpdate(opts, map[string]interface{}{
			"ok": true, "action": "already_up_to_date", "current_version": current,
			"latest_version": latest, "release_url": releaseURL,
		})
	}
	asset, err := worklineReleaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp("", "work-cli-update-")
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "create update workspace: %s", err).WithCause(err)
	}
	defer os.RemoveAll(tempDir)
	archivePath := filepath.Join(tempDir, asset)
	checksumsPath := filepath.Join(tempDir, "checksums.txt")
	base := worklineDownloadBase + "/v" + latest + "/"
	if err := downloadWorklineReleaseFile(ctx, base+"checksums.txt", checksumsPath, worklineMaxChecksums); err != nil {
		return err
	}
	if err := downloadWorklineReleaseFile(ctx, base+asset, archivePath, worklineMaxArchive); err != nil {
		return err
	}
	if err := verifyWorklineChecksum(archivePath, checksumsPath, asset); err != nil {
		return err
	}
	extracted := filepath.Join(tempDir, "payload", worklineBinaryName(runtime.GOOS))
	if err := extractWorklineBinary(archivePath, extracted); err != nil {
		return err
	}
	installedPath, err := replaceWorklineExecutable(extracted, latest)
	if err != nil {
		return err
	}
	return emitWorklineUpdate(opts, map[string]interface{}{
		"ok": true, "action": "updated", "previous_version": current,
		"current_version": latest, "latest_version": latest,
		"release_url": releaseURL, "path": installedPath,
	})
}

func emitWorklineUpdate(opts *WorklineUpdateOptions, result map[string]interface{}) error {
	if opts.JSON {
		output.PrintJson(opts.Factory.IOStreams.Out, result)
		return nil
	}
	action, _ := result["action"].(string)
	current := fmt.Sprint(result["current_version"])
	latest := fmt.Sprint(result["latest_version"])
	switch action {
	case "updated":
		fmt.Fprintf(opts.Factory.IOStreams.ErrOut, "[OK] work-cli updated to %s\n", current)
	case "update_available":
		fmt.Fprintf(opts.Factory.IOStreams.ErrOut, "work-cli %s available, current %s\n", latest, current)
		fmt.Fprintf(opts.Factory.IOStreams.ErrOut, "Run `work-cli update` to install: %s\n", result["release_url"])
	default:
		fmt.Fprintf(opts.Factory.IOStreams.ErrOut, "[OK] work-cli %s is already up to date\n", current)
	}
	return nil
}

func worklineReleaseAsset(goos, goarch string) (string, error) {
	if goarch != "amd64" && goarch != "arm64" {
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "work-cli releases do not support architecture %s", goarch)
	}
	switch goos {
	case "windows":
		return fmt.Sprintf("work-cli_windows_%s.zip", goarch), nil
	case "darwin", "linux":
		return fmt.Sprintf("work-cli_%s_%s.tar.gz", goos, goarch), nil
	default:
		return "", errs.NewValidationError(errs.SubtypeInvalidArgument, "work-cli releases do not support operating system %s", goos)
	}
}

func worklineBinaryName(goos string) string {
	if goos == "windows" {
		return "work-cli.exe"
	}
	return "work-cli"
}

func downloadWorklineReleaseFile(ctx context.Context, url, destination string, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "build release request: %s", err).WithCause(err)
	}
	req.Header.Set("User-Agent", "work-cli-updater")
	resp, err := worklineUpdateHTTPClient.Do(req)
	if err != nil {
		return errs.NewNetworkError(errs.SubtypeNetworkTransport, "download work-cli release: %s", err).WithCause(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errs.NewNetworkError(errs.SubtypeNetworkProtocol, "download work-cli release: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "work-cli release file exceeds the size limit")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "create release directory: %s", err).WithCause(err)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "create release file: %s", err).WithCause(err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil {
		return errs.NewNetworkError(errs.SubtypeNetworkTransport, "save work-cli release: %s", copyErr).WithCause(copyErr)
	}
	if closeErr != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "close work-cli release: %s", closeErr).WithCause(closeErr)
	}
	if written > limit {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "work-cli release file exceeds the size limit")
	}
	return nil
}

func verifyWorklineChecksum(archivePath, checksumsPath, asset string) error {
	checksums, err := os.ReadFile(checksumsPath)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "read work-cli checksums: %s", err).WithCause(err)
	}
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == asset {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if len(expected) != sha256.Size*2 {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "checksum for %s is missing", asset)
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "open work-cli archive: %s", err).WithCause(err)
	}
	defer archive.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, archive); err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "hash work-cli archive: %s", err).WithCause(err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "checksum verification failed for %s", asset)
	}
	return nil
}

func extractWorklineBinary(archivePath, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "create extraction directory: %s", err).WithCause(err)
	}
	if strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return extractWorklineZip(archivePath, destination)
	}
	return extractWorklineTarGz(archivePath, destination)
}

func extractWorklineZip(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "open work-cli zip: %s", err).WithCause(err)
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if filepath.Base(entry.Name) != filepath.Base(destination) || entry.FileInfo().IsDir() {
			continue
		}
		source, err := entry.Open()
		if err != nil {
			return errs.NewInternalError(errs.SubtypeInvalidResponse, "open work-cli binary in zip: %s", err).WithCause(err)
		}
		err = writeExtractedWorklineBinary(source, destination)
		source.Close()
		return err
	}
	return errs.NewInternalError(errs.SubtypeInvalidResponse, "work-cli binary is missing from release archive")
}

func extractWorklineTarGz(archivePath, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "open work-cli archive: %s", err).WithCause(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "open work-cli gzip stream: %s", err).WithCause(err)
	}
	defer gz.Close()
	tarReader := tar.NewReader(gz)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errs.NewInternalError(errs.SubtypeInvalidResponse, "read work-cli tar archive: %s", err).WithCause(err)
		}
		if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == filepath.Base(destination) {
			return writeExtractedWorklineBinary(tarReader, destination)
		}
	}
	return errs.NewInternalError(errs.SubtypeInvalidResponse, "work-cli binary is missing from release archive")
}

func writeExtractedWorklineBinary(source io.Reader, destination string) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "create extracted work-cli: %s", err).WithCause(err)
	}
	_, copyErr := io.Copy(file, io.LimitReader(source, worklineMaxArchive+1))
	closeErr := file.Close()
	if copyErr != nil {
		return errs.NewInternalError(errs.SubtypeInvalidResponse, "extract work-cli binary: %s", copyErr).WithCause(copyErr)
	}
	if closeErr != nil {
		return errs.NewInternalError(errs.SubtypeUnknown, "close extracted work-cli: %s", closeErr).WithCause(closeErr)
	}
	return nil
}

func replaceWorklineExecutable(source, expectedVersion string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "locate work-cli executable: %s", err).WithCause(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "resolve work-cli executable: %s", err).WithCause(err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "inspect work-cli executable: %s", err).WithCause(err)
	}
	staged, err := os.CreateTemp(filepath.Dir(executable), ".work-cli-update-*.new")
	if err != nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "stage work-cli update: %s", err).WithCause(err)
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	sourceFile, err := os.Open(source)
	if err != nil {
		staged.Close()
		return "", errs.NewInternalError(errs.SubtypeUnknown, "open extracted work-cli: %s", err).WithCause(err)
	}
	_, copyErr := io.Copy(staged, sourceFile)
	sourceFile.Close()
	closeErr := staged.Close()
	if copyErr != nil || closeErr != nil {
		if copyErr == nil {
			copyErr = closeErr
		}
		return "", errs.NewInternalError(errs.SubtypeUnknown, "stage work-cli update: %s", copyErr).WithCause(copyErr)
	}
	if err := os.Chmod(stagedPath, info.Mode().Perm()); err != nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "set work-cli permissions: %s", err).WithCause(err)
	}
	backup := executable + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(executable, backup); err != nil {
		return "", errs.NewInternalError(errs.SubtypeUnknown, "prepare work-cli replacement: %s", err).WithCause(err)
	}
	restore := func() {
		_ = os.Remove(executable)
		_ = os.Rename(backup, executable)
	}
	if err := os.Rename(stagedPath, executable); err != nil {
		restore()
		return "", errs.NewInternalError(errs.SubtypeUnknown, "install work-cli update: %s", err).WithCause(err)
	}
	verifyContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	verification, err := exec.CommandContext(verifyContext, executable, "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(verification), expectedVersion) {
		restore()
		return "", errs.NewInternalError(errs.SubtypeInvalidResponse, "updated work-cli failed version verification")
	}
	_ = os.Remove(backup)
	return executable, nil
}
