// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package media implements synchronous local-media understanding commands.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/output"
	"github.com/larksuite/cli/internal/worklineauth"
	"github.com/spf13/cobra"
)

const (
	defaultPollInterval = 2 * time.Second
	defaultTimeout      = 30 * time.Minute
	maxResponseBytes    = 8 << 20
)

type options struct {
	factory       *cmdutil.Factory
	serverURL     string
	pollInterval  int64
	timeout       int64
	allowInsecure bool
}

// NewCmdMedia creates the local-media understanding command group.
func NewCmdMedia(f *cmdutil.Factory) *cobra.Command {
	o := &options{factory: f}
	cmd := &cobra.Command{
		Use:   "media",
		Short: "Understand exported local images, videos, and audio",
		Long:  "Understand local image, video, and audio files. Resolve one image or video, transcribe one audio file, group image-only batches, read completed results, or inspect a video interval. Commands return structured JSON.",
	}
	cmd.PersistentFlags().StringVar(&o.serverURL, "server-url", "", "override the media endpoint URL")
	cmd.PersistentFlags().Int64Var(&o.pollInterval, "poll-ms", 0, "task polling interval in milliseconds")
	cmd.PersistentFlags().Int64Var(&o.timeout, "timeout-ms", 0, "maximum synchronous wait in milliseconds")
	cmd.PersistentFlags().BoolVar(&o.allowInsecure, "allow-insecure-http", false, "allow HTTP only for loopback or trusted development servers")
	_ = cmd.PersistentFlags().MarkHidden("server-url")
	_ = cmd.PersistentFlags().MarkHidden("poll-ms")
	_ = cmd.PersistentFlags().MarkHidden("allow-insecure-http")
	cmd.AddCommand(newResolveCmd(o), newTranscribeCmd(o), newResolveBatchCmd(o), newBatchCmd(o), newTaskCmd(o), newTranscriptCmd(o), newArtifactCmd(o), newObserveCmd(o), newInquireCmd(o))
	cmdutil.DisableAuthCheck(cmd)
	return cmd
}

func newResolveCmd(o *options) *cobra.Command {
	var mimeType string
	cmd := &cobra.Command{
		Use:   "resolve <file>",
		Short: "Upload an image or video and wait for its result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient(cmd.Context(), o)
			if err != nil {
				return err
			}
			result, err := c.resolve(cmd.Context(), args[0], mimeType)
			if err != nil {
				return err
			}
			return emit(o.factory, result)
		},
	}
	cmd.Flags().StringVar(&mimeType, "mime-type", "", "override detected media MIME type")
	setReadRisk(cmd)
	return cmd
}

func newTranscribeCmd(o *options) *cobra.Command {
	var mimeType string
	cmd := &cobra.Command{
		Use:   "transcribe <audio>",
		Short: "Upload an audio file and wait for its transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient(cmd.Context(), o)
			if err != nil {
				return err
			}
			result, err := c.transcribe(cmd.Context(), args[0], mimeType)
			if err != nil {
				return err
			}
			return emit(o.factory, result)
		},
	}
	cmd.Flags().StringVar(&mimeType, "mime-type", "", "override detected audio MIME type")
	setReadRisk(cmd)
	return cmd
}

func newTaskCmd(o *options) *cobra.Command {
	return simpleReadCmd(o, "task <task_ref>", "Read a media task", func(ctx context.Context, c *client, ref string) (any, error) { return c.task(ctx, ref) })
}
func newTranscriptCmd(o *options) *cobra.Command {
	return simpleReadCmd(o, "transcript <media_ref>", "Read a media transcript", func(ctx context.Context, c *client, ref string) (any, error) { return c.transcript(ctx, ref) })
}
func newArtifactCmd(o *options) *cobra.Command {
	return simpleReadCmd(o, "artifact <artifact_ref>", "Read a media artifact", func(ctx context.Context, c *client, ref string) (any, error) { return c.artifact(ctx, ref) })
}

func newBatchCmd(o *options) *cobra.Command {
	return simpleReadCmd(o, "batch <batch_ref>", "Read a media batch", func(ctx context.Context, c *client, ref string) (any, error) { return c.batch(ctx, ref) })
}

func newResolveBatchCmd(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resolve-batch <image> [<image> ...]",
		Short: "Upload image files and wait for the completed batch",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient(cmd.Context(), o)
			if err != nil {
				return err
			}
			result, err := c.resolveBatch(cmd.Context(), args)
			if err != nil {
				return err
			}
			return emit(o.factory, result)
		},
	}
	setReadRisk(cmd)
	return cmd
}

func simpleReadCmd(o *options, use, short string, run func(context.Context, *client, string) (any, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient(cmd.Context(), o)
			if err != nil {
				return err
			}
			result, err := run(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			return emit(o.factory, result)
		},
	}
	setReadRisk(cmd)
	return cmd
}

func newObserveCmd(o *options) *cobra.Command {
	var start, end int64
	var segment, quote, startWord, endWord string
	cmd := &cobra.Command{Use: "observe <media_ref>", Short: "Request and wait for an observation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		selector, err := observationSelector(start, end, segment, quote, startWord, endWord)
		if err != nil {
			return err
		}
		c, err := newClient(cmd.Context(), o)
		if err != nil {
			return err
		}
		result, err := c.observe(cmd.Context(), args[0], selector)
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	cmd.Flags().Int64Var(&start, "start-ms", -1, "interval start in milliseconds")
	cmd.Flags().Int64Var(&end, "end-ms", -1, "interval end in milliseconds")
	cmd.Flags().StringVar(&segment, "segment-ref", "", "transcript segment reference")
	cmd.Flags().StringVar(&quote, "quote", "", "quoted text within --segment-ref")
	cmd.Flags().StringVar(&startWord, "start-word-ref", "", "starting word reference")
	cmd.Flags().StringVar(&endWord, "end-word-ref", "", "ending word reference")
	setReadRisk(cmd)
	return cmd
}

func newInquireCmd(o *options) *cobra.Command {
	var prompt, contextJSON string
	cmd := &cobra.Command{Use: "inquire <artifact_ref>", Short: "Ask a question about an artifact and wait for the answer", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(prompt) == "" {
			return invalid("--prompt is required")
		}
		var contextItems []any
		if strings.TrimSpace(contextJSON) != "" {
			if err := json.Unmarshal([]byte(contextJSON), &contextItems); err != nil || contextItems == nil {
				return invalid("--context-json must be a JSON array")
			}
		}
		c, err := newClient(cmd.Context(), o)
		if err != nil {
			return err
		}
		result, err := c.inquire(cmd.Context(), args[0], prompt, contextItems)
		if err != nil {
			return err
		}
		return emit(o.factory, result)
	}}
	cmd.Flags().StringVar(&prompt, "prompt", "", "question to ask")
	cmd.Flags().StringVar(&contextJSON, "context-json", "", "optional JSON context array")
	setReadRisk(cmd)
	return cmd
}

func setReadRisk(cmd *cobra.Command) { cmdutil.DisableAuthCheck(cmd); cmdutil.SetRisk(cmd, "read") }
func emit(f *cmdutil.Factory, data any) error {
	output.PrintJson(f.IOStreams.Out, map[string]any{"ok": true, "data": data})
	return nil
}

type client struct {
	base          *url.URL
	key           string
	http          *http.Client
	poll, timeout time.Duration
}
type taskResult struct {
	TaskRef   string `json:"task_ref"`
	Status    string `json:"status"`
	ResultRef string `json:"result_ref"`
	Error     any    `json:"error"`
	raw       map[string]any
}
type uploadResult struct {
	MediaRef  string `json:"media_ref"`
	TaskRef   string `json:"task_ref"`
	Status    string `json:"status"`
	Reused    bool   `json:"reused"`
	ResultRef string `json:"result_ref"`
}

func newClient(_ context.Context, o *options) (*client, error) {
	if o == nil || o.factory == nil {
		return nil, errs.NewInternalError(errs.SubtypeSDKError, "media command is unavailable")
	}
	key := strings.TrimSpace(os.Getenv(worklineauth.MediaAPIKeyEnv))
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
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "media access is not ready").WithHint("run `work-cli auth login` and retry")
	}
	raw := strings.TrimSpace(o.serverURL)
	if raw == "" {
		raw = worklineauth.ServerURL()
	}
	allowInsecure := o.allowInsecure || os.Getenv("WORKLINE_MEDIA_ALLOW_INSECURE_HTTP") == "1"
	base, err := parseBaseURL(raw, allowInsecure)
	if err != nil {
		return nil, err
	}
	h, err := o.factory.ExternalHTTPClient()
	if err != nil {
		return nil, err
	}
	poll, timeout, err := resolveDurations(o)
	if err != nil {
		return nil, err
	}
	return &client{base: base, key: key, http: h, poll: poll, timeout: timeout}, nil
}

func parseBaseURL(raw string, allowInsecure bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errs.NewConfigError(errs.SubtypeInvalidConfig, "media service address is invalid")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errs.NewConfigError(errs.SubtypeInvalidConfig, "media service address is invalid")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) && !isBuiltInServerURL(raw) && !allowInsecure {
		return nil, errs.NewSecurityPolicyError(errs.SubtypeAccessDenied, "media service address is blocked")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u, nil
}

// isBuiltInServerURL permits the deployment endpoint compiled into
// worklineauth. Operator-supplied public HTTP endpoints remain rejected unless
// --allow-insecure-http is explicitly set.
func isBuiltInServerURL(raw string) bool {
	return strings.TrimRight(strings.TrimSpace(raw), "/") == strings.TrimRight(worklineauth.DefaultMediaServerURL, "/")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func resolveDurations(o *options) (time.Duration, time.Duration, error) {
	pollMS, timeoutMS := o.pollInterval, o.timeout
	if pollMS == 0 {
		pollMS = durationEnv("WORKLINE_MEDIA_POLL_INTERVAL_MS", defaultPollInterval)
	}
	if timeoutMS == 0 {
		timeoutMS = durationEnv("WORKLINE_MEDIA_TIMEOUT_MS", defaultTimeout)
	}
	if pollMS <= 0 || timeoutMS <= 0 {
		return 0, 0, invalid("--poll-ms and --timeout-ms must be positive durations")
	}
	return time.Duration(pollMS) * time.Millisecond, time.Duration(timeoutMS) * time.Millisecond, nil
}
func durationEnv(name string, fallback time.Duration) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback.Milliseconds()
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func (c *client) resolve(ctx context.Context, file, overrideMIME string) (map[string]any, error) {
	mimeType, err := mediaMIME(file, overrideMIME)
	if err != nil {
		return nil, err
	}
	upload, err := c.upload(ctx, file, mimeType)
	if err != nil {
		return nil, err
	}
	task, err := c.wait(ctx, upload.TaskRef)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"media_ref": upload.MediaRef, "task_ref": upload.TaskRef, "reused": upload.Reused, "task": task.output()}
	if strings.HasPrefix(mimeType, "video/") {
		transcript, err := c.transcript(ctx, upload.MediaRef)
		if err != nil {
			return nil, err
		}
		result["kind"], result["result"] = "transcript", transcript
		return result, nil
	}
	ref := firstRef(task.ResultRef, upload.ResultRef)
	if ref == "" {
		return nil, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media task completed without result_ref")
	}
	artifact, err := c.artifact(ctx, ref)
	if err != nil {
		return nil, err
	}
	result["kind"], result["result"] = "image_observation", artifact
	return result, nil
}

func (c *client) transcribe(ctx context.Context, file, overrideMIME string) (map[string]any, error) {
	mimeType, err := audioMIME(file, overrideMIME)
	if err != nil {
		return nil, err
	}
	upload, err := c.uploadTo(ctx, "/v1/audio/transcriptions", file, mimeType)
	if err != nil {
		return nil, err
	}
	task, err := c.wait(ctx, upload.TaskRef)
	if err != nil {
		return nil, err
	}
	transcript, err := c.transcript(ctx, upload.MediaRef)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"kind":      "transcript",
		"media_ref": upload.MediaRef,
		"task_ref":  upload.TaskRef,
		"reused":    upload.Reused,
		"task":      task.output(),
		"result":    transcript,
	}, nil
}

func (c *client) resolveBatch(ctx context.Context, paths []string) (any, error) {
	batch, err := c.uploadImages(ctx, paths)
	if err != nil {
		return nil, err
	}
	pending, err := batchPending(batch)
	if err != nil {
		return nil, err
	}
	if pending == 0 {
		return batch, nil
	}
	batchRef, _ := batch["batch_ref"].(string)
	if batchRef == "" {
		return nil, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media batch response has no batch_ref")
	}
	return c.waitBatch(ctx, batchRef)
}

func (c *client) task(ctx context.Context, ref string) (any, error) {
	var out any
	err := c.request(ctx, http.MethodGet, "/v1/tasks/"+url.PathEscape(ref), nil, "", &out)
	return out, err
}

func (c *client) batch(ctx context.Context, ref string) (any, error) {
	var out any
	err := c.request(ctx, http.MethodGet, "/v1/media/batches/"+url.PathEscape(ref), nil, "", &out)
	return out, err
}
func (c *client) transcript(ctx context.Context, ref string) (any, error) {
	var out any
	err := c.request(ctx, http.MethodGet, "/v1/media/"+url.PathEscape(ref)+"/transcript", nil, "", &out)
	return out, err
}
func (c *client) artifact(ctx context.Context, ref string) (any, error) {
	var out any
	err := c.request(ctx, http.MethodGet, "/v1/artifacts/"+url.PathEscape(ref), nil, "", &out)
	return out, err
}
func (c *client) upload(ctx context.Context, path, mimeType string) (uploadResult, error) {
	return c.uploadTo(ctx, "/v1/media", path, mimeType)
}

func (c *client) uploadTo(ctx context.Context, endpoint, path, mimeType string) (uploadResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return uploadResult{}, invalid("cannot open media file %q: %v", path, err)
	}
	defer file.Close()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filepath.Base(path)))
	h.Set("Content-Type", mimeType)
	part, err := w.CreatePart(h)
	if err != nil {
		return uploadResult{}, errs.NewInternalError(errs.SubtypeSDKError, "create media upload: %v", err).WithCause(err)
	}
	if _, err = io.Copy(part, file); err != nil {
		return uploadResult{}, errs.NewNetworkError(errs.SubtypeNetworkTransport, "read media upload: %v", err).WithCause(err)
	}
	if err = w.Close(); err != nil {
		return uploadResult{}, errs.NewInternalError(errs.SubtypeSDKError, "finish media upload: %v", err).WithCause(err)
	}
	var out uploadResult
	if err = c.request(ctx, http.MethodPost, endpoint, &body, w.FormDataContentType(), &out); err != nil {
		return out, err
	}
	if out.MediaRef == "" || out.TaskRef == "" {
		return out, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media upload response is incomplete")
	}
	return out, nil
}

func (c *client) uploadImages(ctx context.Context, paths []string) (map[string]any, error) {
	if len(paths) == 0 {
		return nil, invalid("at least one image file is required")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, path := range paths {
		mimeType, err := mediaMIME(path, "")
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(mimeType, "image/") {
			return nil, invalid("resolve-batch requires image files")
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, invalid("cannot open image file %q: %v", path, err)
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="images"; filename=%q`, filepath.Base(path)))
		header.Set("Content-Type", mimeType)
		part, err := writer.CreatePart(header)
		if err == nil {
			_, err = io.Copy(part, file)
		}
		closeErr := file.Close()
		if err != nil {
			return nil, errs.NewNetworkError(errs.SubtypeNetworkTransport, "read image upload: %v", err).WithCause(err)
		}
		if closeErr != nil {
			return nil, errs.NewInternalError(errs.SubtypeFileIO, "close image upload: %v", closeErr).WithCause(closeErr)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeSDKError, "finish media batch upload: %v", err).WithCause(err)
	}
	var out map[string]any
	if err := c.request(ctx, http.MethodPost, "/v1/media/batches", &body, writer.FormDataContentType(), &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(stringValue(out["batch_ref"])) == "" {
		return nil, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media batch response has no batch_ref")
	}
	if _, err := batchPending(out); err != nil {
		return nil, err
	}
	return out, nil
}
func (c *client) observe(ctx context.Context, ref string, selector map[string]any) (map[string]any, error) {
	submitted, err := c.submit(ctx, "/v1/media/"+url.PathEscape(ref)+"/observations", map[string]any{"selector": selector})
	if err != nil {
		return nil, err
	}
	task, err := c.wait(ctx, submitted.TaskRef)
	if err != nil {
		return nil, err
	}
	resultRef := firstRef(task.ResultRef, submitted.ResultRef)
	if resultRef == "" {
		return nil, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media task completed without result_ref")
	}
	artifact, err := c.artifact(ctx, resultRef)
	if err != nil {
		return nil, err
	}
	return map[string]any{"media_ref": ref, "task_ref": task.TaskRef, "reused": submitted.Reused, "task": task.output(), "result": artifact}, nil
}
func (c *client) inquire(ctx context.Context, ref, prompt string, contextItems []any) (map[string]any, error) {
	submitted, err := c.submit(ctx, "/v1/artifacts/"+url.PathEscape(ref)+"/inquiries", map[string]any{"prompt": prompt, "context": contextItems})
	if err != nil {
		return nil, err
	}
	task, err := c.wait(ctx, submitted.TaskRef)
	if err != nil {
		return nil, err
	}
	resultRef := firstRef(task.ResultRef, submitted.ResultRef)
	if resultRef == "" {
		return nil, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media task completed without result_ref")
	}
	artifact, err := c.artifact(ctx, resultRef)
	if err != nil {
		return nil, err
	}
	return map[string]any{"base_artifact_ref": ref, "task_ref": task.TaskRef, "reused": submitted.Reused, "task": task.output(), "result": artifact}, nil
}
func (c *client) submit(ctx context.Context, path string, payload any) (uploadResult, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return uploadResult{}, errs.NewInternalError(errs.SubtypeSDKError, "encode media request: %v", err).WithCause(err)
	}
	var out uploadResult
	err = c.request(ctx, http.MethodPost, path, bytes.NewReader(raw), "application/json", &out)
	if err != nil {
		return out, err
	}
	if out.TaskRef == "" {
		return out, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media task response has no task_ref")
	}
	return out, nil
}
func (c *client) wait(ctx context.Context, ref string) (taskResult, error) {
	deadline := time.Now().Add(c.timeout)
	for {
		raw, err := c.task(ctx, ref)
		if err != nil {
			return taskResult{}, err
		}
		task, ok := raw.(map[string]any)
		if !ok {
			return taskResult{}, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "invalid media task response")
		}
		status, _ := task["status"].(string)
		if status == "succeeded" {
			return decodeTask(task)
		}
		if status == "failed" {
			return taskResult{}, errs.NewAPIError(errs.SubtypeUnknown, "media task %s failed", ref)
		}
		if time.Now().After(deadline) {
			return taskResult{}, errs.NewNetworkError(errs.SubtypeNetworkTimeout, "media task %s did not finish within %s", ref, c.timeout).WithRetryable()
		}
		select {
		case <-ctx.Done():
			return taskResult{}, errs.NewNetworkError(errs.SubtypeNetworkTransport, "media task wait interrupted").WithCause(ctx.Err())
		case <-time.After(c.poll):
		}
	}
}

func (c *client) waitBatch(ctx context.Context, ref string) (any, error) {
	deadline := time.Now().Add(c.timeout)
	for {
		raw, err := c.batch(ctx, ref)
		if err != nil {
			return nil, err
		}
		batch, ok := raw.(map[string]any)
		if !ok {
			return nil, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "invalid media batch response")
		}
		pending, err := batchPending(batch)
		if err != nil {
			return nil, err
		}
		if pending == 0 {
			return batch, nil
		}
		if time.Now().After(deadline) {
			return nil, errs.NewNetworkError(errs.SubtypeNetworkTimeout, "media batch %s did not finish within %s", ref, c.timeout).WithRetryable()
		}
		select {
		case <-ctx.Done():
			return nil, errs.NewNetworkError(errs.SubtypeNetworkTransport, "media batch wait interrupted").WithCause(ctx.Err())
		case <-time.After(c.poll):
		}
	}
}

func batchPending(batch map[string]any) (int64, error) {
	pending, ok := batch["pending"]
	if !ok {
		return 0, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media batch response has no pending count")
	}
	switch value := pending.(type) {
	case float64:
		if value >= 0 && value == float64(int64(value)) {
			return int64(value), nil
		}
	case json.Number:
		if parsed, err := value.Int64(); err == nil && parsed >= 0 {
			return parsed, nil
		}
	case int64:
		if value >= 0 {
			return value, nil
		}
	case int:
		if value >= 0 {
			return int64(value), nil
		}
	}
	return 0, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media batch response has invalid pending count")
}
func decodeTask(value map[string]any) (taskResult, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return taskResult{}, err
	}
	var out taskResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return taskResult{}, errs.NewNetworkError(errs.SubtypeNetworkProtocol, "decode media task: %v", err).WithCause(err)
	}
	out.raw = value
	return out, nil
}
func (t taskResult) output() any {
	if t.raw != nil {
		return t.raw
	}
	return t
}
func (c *client) request(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	relative, err := url.Parse(strings.TrimPrefix(path, "/"))
	if err != nil {
		return errs.NewInternalError(errs.SubtypeSDKError, "prepare media request: %v", err).WithCause(err)
	}
	u := c.base.ResolveReference(relative)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeSDKError, "build media request: %v", err).WithCause(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return errs.NewNetworkError(errs.SubtypeNetworkTransport, "connect to media service: %v", err).WithCause(err).WithRetryable()
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return errs.NewNetworkError(errs.SubtypeNetworkTransport, "read media response: %v", err).WithCause(err)
	}
	if len(raw) > maxResponseBytes {
		return errs.NewNetworkError(errs.SubtypeNetworkProtocol, "media response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errs.NewNetworkError(errs.SubtypeNetworkProtocol, "decode media response: %v", err).WithCause(err)
	}
	return nil
}
func apiError(status int, raw []byte) error {
	var payload struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.Unmarshal(raw, &payload)
	message := strings.TrimSpace(payload.Error.Message)
	if message == "" {
		message = fmt.Sprintf("media service returned HTTP %d", status)
	}
	switch status {
	case http.StatusUnauthorized:
		return errs.NewAuthenticationError(errs.SubtypeTokenInvalid, "%s", message)
	case http.StatusForbidden:
		return errs.NewPermissionError(errs.SubtypePermissionDenied, "%s", message)
	case http.StatusTooManyRequests:
		return errs.NewAPIError(errs.SubtypeRateLimit, "%s", message).WithRetryable()
	default:
		if status >= 500 {
			return errs.NewNetworkError(errs.SubtypeNetworkServer, "%s", message).WithRetryable()
		}
		return errs.NewAPIError(errs.SubtypeUnknown, "%s", message)
	}
}
func mediaMIME(path, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg", nil
	case ".png":
		return "image/png", nil
	case ".webp":
		return "image/webp", nil
	case ".gif":
		return "image/gif", nil
	case ".mp4":
		return "video/mp4", nil
	case ".mov":
		return "video/quicktime", nil
	case ".webm":
		return "video/webm", nil
	case ".mkv":
		return "video/x-matroska", nil
	}
	return "", invalid("unsupported media extension")
}

func audioMIME(path, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		if !strings.HasPrefix(strings.ToLower(override), "audio/") {
			return "", invalid("--mime-type must be an audio MIME type")
		}
		return override, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return "audio/mpeg", nil
	case ".m4a":
		return "audio/mp4", nil
	case ".wav":
		return "audio/wav", nil
	case ".aac":
		return "audio/aac", nil
	case ".flac":
		return "audio/flac", nil
	case ".ogg", ".opus":
		return "audio/ogg", nil
	}
	return "", invalid("unsupported audio extension")
}
func observationSelector(start, end int64, segment, quote, startWord, endWord string) (map[string]any, error) {
	hasInterval := start >= 0 || end >= 0
	hasSegment := strings.TrimSpace(segment) != ""
	hasWords := strings.TrimSpace(startWord) != "" || strings.TrimSpace(endWord) != ""
	if boolCount(hasInterval, hasSegment, hasWords) != 1 {
		return nil, invalid("observe requires exactly one selector mode")
	}
	if hasInterval {
		if start < 0 || end < 0 {
			return nil, invalid("--start-ms and --end-ms are required together")
		}
		return map[string]any{"type": "interval", "start_at_ms": start, "end_at_ms": end}, nil
	}
	if hasSegment {
		result := map[string]any{"type": "segment", "segment_ref": segment}
		if quote != "" {
			result["type"], result["text"] = "quote", quote
		}
		return result, nil
	}
	if startWord == "" || endWord == "" {
		return nil, invalid("--start-word-ref and --end-word-ref are required together")
	}
	return map[string]any{"type": "words", "start_word_ref": startWord, "end_word_ref": endWord}, nil
}
func boolCount(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}
func firstRef(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
func invalid(format string, args ...any) error {
	return errs.NewValidationError(errs.SubtypeInvalidArgument, format, args...)
}
