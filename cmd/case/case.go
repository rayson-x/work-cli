// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package casecmd

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/caseclient"
	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/output"
	"github.com/larksuite/cli/internal/worklineauth"
	"github.com/spf13/cobra"
)

type options struct {
	factory   *cmdutil.Factory
	serverURL string
	key       string
}

// NewCmdCase creates the cloud Case client command group. It only transports
// evidence and reads cloud results; it cannot write canonical domain records.
func NewCmdCase(f *cmdutil.Factory) *cobra.Command {
	o := &options{factory: f}
	cmd := &cobra.Command{Use: "case", Short: "Submit evidence to the cloud Case service and read results"}
	cmd.PersistentFlags().StringVar(&o.serverURL, "server-url", "", "override the Case service URL")
	cmd.PersistentFlags().StringVar(&o.key, "api-key", "", "override the Case service API key")
	_ = cmd.PersistentFlags().MarkHidden("server-url")
	_ = cmd.PersistentFlags().MarkHidden("api-key")
	cmd.AddCommand(newCreate(o), newSubmit(o), newSeal(o), newStartRun(o), newStatus(o), newEvidence(o), newRun(o), newInterpretation(o), newMediaUpload(o), newMediaBatch(o))
	cmdutil.DisableAuthCheck(cmd)
	return cmd
}

func newCreate(o *options) *cobra.Command {
	var purpose, scope, key string
	cmd := &cobra.Command{Use: "create", Short: "Create a Case", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(purpose) == "" || strings.TrimSpace(scope) == "" {
			return invalid("--purpose and --source-scope-json are required")
		}
		var source map[string]any
		if err := json.Unmarshal([]byte(scope), &source); err != nil || source == nil {
			return invalid("--source-scope-json must be a JSON object")
		}
		c, err := o.client()
		if err != nil {
			return err
		}
		result, err := c.CreateCase(cmd.Context(), caseclient.CreateCaseRequest{Purpose: purpose, SourceScope: source, Key: key})
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	cmd.Flags().StringVar(&purpose, "purpose", "", "Case purpose")
	cmd.Flags().StringVar(&scope, "source-scope-json", "", "source scope JSON object")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "stable idempotency key")
	return cmd
}

func newSubmit(o *options) *cobra.Command {
	var file, key string
	cmd := &cobra.Command{Use: "submit <case_ref>", Short: "Submit an Evidence Bundle", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := readJSON(cmd, file)
		if err != nil {
			return err
		}
		var input caseclient.EvidenceBundle
		if err := json.Unmarshal(raw, &input); err != nil {
			return invalid("--bundle-json is invalid: %v", err)
		}
		input.Key = key
		c, err := o.client()
		if err != nil {
			return err
		}
		result, err := c.SubmitEvidenceBundle(cmd.Context(), args[0], input)
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	cmd.Flags().StringVar(&file, "bundle-json", "", "Evidence Bundle JSON file, or - for stdin")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "stable idempotency key")
	_ = cmd.MarkFlagRequired("bundle-json")
	return cmd
}

func newSeal(o *options) *cobra.Command {
	return simple(o, "seal <case_ref> <bundle_ref>", "Seal an Evidence Bundle", 2, func(ctx context.Context, c *caseclient.Client, args []string) (any, error) {
		return c.SealEvidenceBundle(ctx, args[0], args[1])
	})
}

func newStartRun(o *options) *cobra.Command {
	var revision int
	var pipeline, key string
	cmd := &cobra.Command{Use: "start-run <case_ref>", Short: "Start a cloud inference run", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if revision < 0 || strings.TrimSpace(pipeline) == "" {
			return invalid("--base-revision and --pipeline are required")
		}
		c, err := o.client()
		if err != nil {
			return err
		}
		result, err := c.StartInferenceRun(cmd.Context(), args[0], caseclient.InferenceRunRequest{BaseCaseRevision: revision, Pipeline: pipeline, Key: key})
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	cmd.Flags().IntVar(&revision, "base-revision", -1, "Case revision used as the inference input")
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "inference pipeline name")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "stable idempotency key")
	return cmd
}

func newStatus(o *options) *cobra.Command {
	return simple(o, "status <case_ref>", "Read Case status and audit data", 1, func(ctx context.Context, c *caseclient.Client, args []string) (any, error) {
		return c.GetCase(ctx, args[0])
	})
}
func newEvidence(o *options) *cobra.Command {
	return simple(o, "evidence <case_ref>", "Read all submitted Evidence", 1, func(ctx context.Context, c *caseclient.Client, args []string) (any, error) {
		return c.GetEvidence(ctx, args[0])
	})
}
func newRun(o *options) *cobra.Command {
	return simple(o, "run <case_ref> <run_ref>", "Read an inference run", 2, func(ctx context.Context, c *caseclient.Client, args []string) (any, error) {
		return c.GetRun(ctx, args[0], args[1])
	})
}

func newInterpretation(o *options) *cobra.Command {
	var view string
	cmd := &cobra.Command{Use: "interpretation <case_ref>", Short: "Read the cloud interpretation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := o.client()
		if err != nil {
			return err
		}
		result, err := c.GetInterpretation(cmd.Context(), args[0], view)
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	cmd.Flags().StringVar(&view, "view", "confirmed", "confirmed or audit")
	return cmd
}

func newMediaUpload(o *options) *cobra.Command {
	var mimeType string
	cmd := &cobra.Command{Use: "media-upload <file>", Short: "Upload one original media file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		file, closeFile, err := openMedia(args[0], mimeType)
		if err != nil {
			return err
		}
		defer closeFile()
		c, err := o.client()
		if err != nil {
			return err
		}
		result, err := c.UploadMedia(cmd.Context(), file)
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	cmd.Flags().StringVar(&mimeType, "mime-type", "", "override media MIME type")
	return cmd
}

func newMediaBatch(o *options) *cobra.Command {
	var mimeType string
	cmd := &cobra.Command{Use: "media-batch <file> [<file> ...]", Short: "Upload multiple original media files with partial recovery", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		files := make([]caseclient.MediaFile, 0, len(args))
		closers := make([]io.Closer, 0, len(args))
		for _, path := range args {
			file, closeFile, err := openMedia(path, mimeType)
			if err != nil {
				for _, closer := range closers {
					_ = closer.Close()
				}
				return err
			}
			files = append(files, file)
			closers = append(closers, closerFunc(closeFile))
		}
		defer func() {
			for _, closer := range closers {
				_ = closer.Close()
			}
		}()
		c, err := o.client()
		if err != nil {
			return err
		}
		result, err := c.UploadMediaBatch(cmd.Context(), files)
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	cmd.Flags().StringVar(&mimeType, "mime-type", "", "override media MIME type for all files")
	return cmd
}

type closerFunc func()

func (f closerFunc) Close() error { f(); return nil }
func openMedia(path, override string) (caseclient.MediaFile, func(), error) {
	f, err := cmdutil.OpenLocalFile(path)
	if err != nil {
		return caseclient.MediaFile{}, func() {}, invalid("media_not_exported: %v", err)
	}
	mimeType := override
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	}
	if mimeType == "" {
		_ = f.Close()
		return caseclient.MediaFile{}, func() {}, invalid("unsupported media MIME type for %q", path)
	}
	return caseclient.MediaFile{Name: filepath.Base(path), MIME: mimeType, Reader: f}, func() { _ = f.Close() }, nil
}

func simple(o *options, use, short string, n int, run func(context.Context, *caseclient.Client, []string) (any, error)) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := o.client()
		if err != nil {
			return err
		}
		result, err := run(cmd.Context(), c, args)
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	return cmd
}

func (o *options) client() (*caseclient.Client, error) {
	if o.factory == nil {
		return nil, invalid("case client is unavailable")
	}
	key := strings.TrimSpace(o.key)
	if key == "" {
		key = strings.TrimSpace(os.Getenv(worklineauth.MediaAPIKeyEnv))
	}
	if key == "" {
		cfg, err := o.factory.Config()
		if err != nil {
			return nil, err
		}
		key, err = worklineauth.APIKey(o.factory.Keychain, cfg.AppID, cfg.UserOpenId)
		if err != nil {
			return nil, err
		}
	}
	if key == "" {
		return nil, invalid("Case service API key is not configured")
	}
	h, err := o.factory.ExternalHTTPClient()
	if err != nil {
		return nil, err
	}
	return caseclient.NewWithError(caseclient.Options{BaseURL: o.serverURL, APIKey: key, HTTP: h})
}

func readJSON(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	f, err := cmdutil.OpenLocalFile(path)
	if err != nil {
		return nil, invalid("read JSON file: %v", err)
	}
	defer f.Close()
	return io.ReadAll(f)
}
func emit(f *cmdutil.Factory, value any) error {
	output.PrintJson(f.IOStreams.Out, map[string]any{"ok": true, "data": value})
	return nil
}
func invalid(format string, args ...any) error {
	return errs.NewValidationError(errs.SubtypeInvalidArgument, format, args...)
}
