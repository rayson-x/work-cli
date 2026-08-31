// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package imagegen exposes the synchronous CLI facade for Workline's
// asynchronous image service.
package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/worklineauth"
	"github.com/larksuite/cli/shortcuts/common"
)

const (
	pollInterval     = 3 * time.Second
	operationTimeout = 30 * time.Minute
)

var httpClient = &http.Client{Timeout: 2 * time.Minute}

type apiClient struct {
	base   *url.URL
	key    string
	client *http.Client
}

type submission struct {
	TaskRef   string `json:"task_ref"`
	Status    string `json:"status"`
	Reused    bool   `json:"reused"`
	ResultRef any    `json:"result_ref"`
}

type jobOutput struct {
	OutputRef    string `json:"output_ref"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	VariantIndex *int   `json:"variant_index,omitempty"`
	ArtifactRef  string `json:"artifact_ref,omitempty"`
	MediaRef     string `json:"media_ref,omitempty"`
	Format       string `json:"format,omitempty"`
}

type jobSnapshot struct {
	TaskRef string      `json:"task_ref"`
	Status  string      `json:"status"`
	Stage   string      `json:"stage"`
	Attempt int         `json:"attempt"`
	Updated string      `json:"updated_at"`
	Outputs []jobOutput `json:"outputs"`
}

type artifact map[string]any

type reference struct {
	Path string
	Role string
}

func Shortcuts() []common.Shortcut {
	legacyGenerate, legacyEdit, legacyJob := generateShortcut(), editShortcut(), jobShortcut()
	legacyGenerate.Hidden, legacyEdit.Hidden, legacyJob.Hidden = true, true, true
	return []common.Shortcut{scriptGenerateShortcut(), scriptEditShortcut(), scriptBatchShortcut(), legacyGenerate, legacyEdit, legacyJob}
}

func sharedFlags() []common.Flag {
	return []common.Flag{
		{Name: "prompt", Desc: "Image request; accepts @file or stdin", Required: true, Input: []string{common.File, common.Stdin}},
		{Name: "reference", Type: "string_array", Desc: "Reference image as path=role; repeat up to five times"},
		{Name: "variants", Type: "int", Default: "1", Desc: "Number of image variants (1-8)"},
		{Name: "size", Desc: "Optional output size: auto or WIDTHxHEIGHT"},
		{Name: "quality", Desc: "Optional quality", Enum: []string{"low", "medium", "high", "auto"}},
		{Name: "background", Desc: "Optional background mode"},
		{Name: "output-format", Desc: "Output format", Enum: []string{"png", "jpeg", "webp"}},
		{Name: "out-dir", Default: "output/imagegen", Desc: "Directory for downloaded images"},
	}
}

func generateShortcut() common.Shortcut {
	flags := append(sharedFlags(), common.Flag{Name: "transparent", Type: "bool", Desc: "Request a transparent background"})
	return common.Shortcut{
		Service: "image", Command: "+generate", Description: "Generate images and wait for local files", Risk: "write",
		Scopes: []string{}, AuthTypes: []string{"user", "bot"}, Flags: flags, Validate: validateGenerate, Execute: executeGenerate,
	}
}

func editShortcut() common.Shortcut {
	flags := append(sharedFlags(),
		common.Flag{Name: "input", Desc: "Image to edit", Required: true},
		common.Flag{Name: "mask", Desc: "Optional edit mask"},
	)
	return common.Shortcut{
		Service: "image", Command: "+edit", Description: "Edit an image and wait for the local result", Risk: "write",
		Scopes: []string{}, AuthTypes: []string{"user", "bot"}, Flags: flags, Validate: validateEdit, Execute: executeEdit,
	}
}

func jobShortcut() common.Shortcut {
	return common.Shortcut{
		Service: "image", Command: "+job", Description: "Read or resume an image job", Risk: "read", AuthTypes: []string{"user", "bot"},
		Scopes: []string{},
		Flags: []common.Flag{
			{Name: "task-ref", Desc: "Image task reference", Required: true},
			{Name: "wait", Type: "bool", Desc: "Wait for completion and download results"},
			{Name: "out-dir", Default: "output/imagegen", Desc: "Directory for downloaded images when waiting"},
		},
		Execute: executeJob,
	}
}

func validateGenerate(_ context.Context, r *common.RuntimeContext) error {
	if err := validateShared(r, nil); err != nil {
		return err
	}
	return validateImageOptions(r.Str("size"), r.Str("output-format"), r.Str("background"), r.Bool("transparent"))
}

func validateEdit(_ context.Context, r *common.RuntimeContext) error {
	input := strings.TrimSpace(r.Str("input"))
	if input == "" {
		return invalid("--input is required")
	}
	if err := requireRegularFile(input, "--input"); err != nil {
		return err
	}
	if mask := strings.TrimSpace(r.Str("mask")); mask != "" {
		if err := requireRegularFile(mask, "--mask"); err != nil {
			return err
		}
	}
	if err := validateShared(r, &reference{Path: input, Role: "edit_target"}); err != nil {
		return err
	}
	return validateImageOptions(r.Str("size"), r.Str("output-format"), r.Str("background"), false)
}

func validateShared(r *common.RuntimeContext, primary *reference) error {
	prompt := strings.TrimSpace(r.Str("prompt"))
	if prompt == "" || len(prompt) > 8_000 {
		return invalid("--prompt must contain 1-8000 bytes")
	}
	variants := r.Int("variants")
	if variants < 1 || variants > 8 {
		return invalid("--variants must be between 1 and 8")
	}
	references, err := parseReferences(r.StrArray("reference"))
	if err != nil {
		return err
	}
	count := len(references)
	if primary != nil {
		count++
	}
	if count > 5 {
		return invalid("at most five reference images are supported")
	}
	if strings.TrimSpace(r.Str("out-dir")) == "" {
		return invalid("--out-dir must not be blank")
	}
	return nil
}

func executeGenerate(ctx context.Context, r *common.RuntimeContext) error {
	refs, _ := parseReferences(r.StrArray("reference"))
	request := map[string]any{
		"operation": "generate", "prompt": strings.TrimSpace(r.Str("prompt")), "variant_count": r.Int("variants"),
	}
	addImageOptions(request, r)
	if r.Bool("transparent") {
		request["transparent_background"] = true
	}
	return executeImage(ctx, r, request, refs, "")
}

func executeEdit(ctx context.Context, r *common.RuntimeContext) error {
	refs, _ := parseReferences(r.StrArray("reference"))
	refs = append([]reference{{Path: r.Str("input"), Role: "edit_target"}}, refs...)
	request := map[string]any{
		"operation": "edit", "prompt": strings.TrimSpace(r.Str("prompt")), "variant_count": r.Int("variants"),
	}
	addImageOptions(request, r)
	return executeImage(ctx, r, request, refs, strings.TrimSpace(r.Str("mask")))
}

func addImageOptions(request map[string]any, r *common.RuntimeContext) {
	for flag, field := range map[string]string{"size": "size", "quality": "quality", "background": "background", "output-format": "output_format"} {
		if value := strings.TrimSpace(r.Str(flag)); value != "" {
			request[field] = value
		}
	}
}

func executeImage(ctx context.Context, r *common.RuntimeContext, request map[string]any, refs []reference, mask string) error {
	client, err := newAPIClient(r)
	if err != nil {
		return err
	}
	remoteRefs := make([]map[string]string, 0, len(refs))
	for _, ref := range refs {
		mediaRef, uploadErr := client.upload(ctx, ref.Path)
		if uploadErr != nil {
			return uploadErr
		}
		remoteRefs = append(remoteRefs, map[string]string{"media_ref": mediaRef, "role": ref.Role})
	}
	if len(remoteRefs) > 0 {
		request["references"] = remoteRefs
	}
	if mask != "" {
		mediaRef, uploadErr := client.upload(ctx, mask)
		if uploadErr != nil {
			return uploadErr
		}
		request["mask_media_ref"] = mediaRef
	}
	submitted, err := client.submit(ctx, request)
	if err != nil {
		return err
	}
	job, err := client.wait(ctx, submitted.TaskRef)
	if err != nil {
		return err
	}
	result, err := client.download(ctx, job, r.Str("out-dir"))
	if err != nil {
		return err
	}
	result["reused"] = submitted.Reused
	r.Out(result, nil)
	return nil
}

func executeJob(ctx context.Context, r *common.RuntimeContext) error {
	client, err := newAPIClient(r)
	if err != nil {
		return err
	}
	job, err := client.job(ctx, r.Str("task-ref"))
	if err != nil {
		return err
	}
	if !r.Bool("wait") {
		r.Out(job, nil)
		return nil
	}
	job, err = client.wait(ctx, job.TaskRef)
	if err != nil {
		return err
	}
	result, err := client.download(ctx, job, r.Str("out-dir"))
	if err != nil {
		return err
	}
	r.Out(result, nil)
	return nil
}

func newAPIClient(r *common.RuntimeContext) (*apiClient, error) {
	if r == nil || r.Factory == nil || r.Config == nil {
		return nil, errs.NewInternalError(errs.SubtypeSDKError, "Workline image runtime is unavailable")
	}
	key := strings.TrimSpace(os.Getenv(worklineauth.MediaAPIKeyEnv))
	if key == "" {
		var err error
		key, err = worklineauth.APIKey(r.Factory.Keychain, r.Config.AppID, r.UserOpenId())
		if err != nil {
			return nil, err
		}
	}
	if key == "" {
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "%s is not configured", worklineauth.MediaAPIKeyEnv).
			WithHint("run `work-cli auth login` and retry")
	}
	base, err := url.Parse(worklineauth.ServerURL())
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errs.NewConfigError(errs.SubtypeNotConfigured, "%s is not configured as an absolute URL", worklineauth.MediaServerURLEnv).
			WithField(worklineauth.MediaServerURLEnv)
	}
	if base.Scheme != "https" && base.Scheme != "http" {
		return nil, errs.NewConfigError(errs.SubtypeInvalidConfig, "%s must use HTTP or HTTPS", worklineauth.MediaServerURLEnv).
			WithField(worklineauth.MediaServerURLEnv).
			WithHint("set an absolute Workline media service URL")
	}
	client, err := r.Factory.ExternalHTTPClient()
	if err != nil {
		return nil, err
	}
	return &apiClient{base: base, key: key, client: client}, nil
}

func (c *apiClient) upload(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", invalid("cannot open image %q: %v", path, err)
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filepath.Base(path)))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return "", protocolError("create media upload", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", protocolError("read media upload", err)
	}
	if err := writer.Close(); err != nil {
		return "", protocolError("finish media upload", err)
	}
	var response struct {
		MediaRef string `json:"media_ref"`
	}
	if err := c.requestJSON(ctx, http.MethodPost, "/v1/media", &body, writer.FormDataContentType(), &response); err != nil {
		return "", err
	}
	if response.MediaRef == "" {
		return "", errs.NewInternalError(errs.SubtypeInvalidResponse, "media upload response has no media_ref")
	}
	return response.MediaRef, nil
}

func (c *apiClient) submit(ctx context.Context, request map[string]any) (submission, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return submission{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "encode image request: %v", err).WithCause(err)
	}
	var result submission
	if err := c.requestJSON(ctx, http.MethodPost, "/v1/image-jobs", bytes.NewReader(body), "application/json", &result); err != nil {
		return submission{}, err
	}
	if result.TaskRef == "" {
		return submission{}, errs.NewInternalError(errs.SubtypeInvalidResponse, "image submission response has no task_ref")
	}
	return result, nil
}

func (c *apiClient) job(ctx context.Context, taskRef string) (jobSnapshot, error) {
	var result jobSnapshot
	err := c.requestJSON(ctx, http.MethodGet, "/v1/image-jobs/"+url.PathEscape(taskRef), nil, "", &result)
	return result, err
}

func (c *apiClient) wait(ctx context.Context, taskRef string) (jobSnapshot, error) {
	deadline := time.Now().Add(operationTimeout)
	for {
		job, err := c.job(ctx, taskRef)
		if err != nil {
			return jobSnapshot{}, err
		}
		switch job.Status {
		case "succeeded":
			return job, nil
		case "failed":
			return jobSnapshot{}, errs.NewAPIError(errs.SubtypeUnknown, "image job %s failed at stage %s", taskRef, job.Stage)
		}
		if time.Now().After(deadline) {
			return jobSnapshot{}, errs.NewNetworkError(errs.SubtypeNetworkTimeout, "image job %s did not finish within %s", taskRef, operationTimeout).WithRetryable()
		}
		select {
		case <-ctx.Done():
			return jobSnapshot{}, errs.NewNetworkError(errs.SubtypeNetworkTransport, "image job wait interrupted").WithCause(ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

func (c *apiClient) download(ctx context.Context, job jobSnapshot, outDir string) (map[string]any, error) {
	absDir, err := filepath.Abs(outDir)
	if err != nil {
		return nil, invalid("invalid --out-dir: %v", err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, errs.NewInternalError(errs.SubtypeStorage, "create output directory: %v", err).WithCause(err)
	}
	outputs := make([]map[string]any, 0, len(job.Outputs))
	for index, output := range job.Outputs {
		if output.Status != "succeeded" || output.MediaRef == "" || output.ArtifactRef == "" {
			continue
		}
		var details artifact
		if err := c.requestJSON(ctx, http.MethodGet, "/v1/artifacts/"+url.PathEscape(output.ArtifactRef), nil, "", &details); err != nil {
			return nil, err
		}
		ext := extensionFor(output.Format)
		path := uniquePath(filepath.Join(absDir, fmt.Sprintf("image-%02d%s", index+1, ext)))
		if err := c.downloadFile(ctx, output.MediaRef, path); err != nil {
			return nil, err
		}
		item := map[string]any{
			"output_ref": output.OutputRef, "kind": output.Kind, "artifact_ref": output.ArtifactRef,
			"media_ref": output.MediaRef, "mime_type": output.Format, "path": path,
		}
		if output.VariantIndex != nil {
			item["variant_index"] = *output.VariantIndex
		}
		for _, field := range []string{"prompt", "revised_prompt", "size_bytes", "transparent_background", "reference_media"} {
			if value, exists := details[field]; exists {
				item[field] = value
			}
		}
		outputs = append(outputs, item)
	}
	if len(outputs) == 0 {
		return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "completed image job has no downloadable output")
	}
	return map[string]any{
		"task_ref": job.TaskRef, "status": job.Status, "stage": job.Stage, "attempt": job.Attempt,
		"updated_at": job.Updated, "outputs": outputs,
	}, nil
}

func (c *apiClient) downloadFile(ctx context.Context, mediaRef, path string) error {
	req, err := c.request(ctx, http.MethodGet, "/v1/media/"+url.PathEscape(mediaRef)+"/content", nil, "")
	if err != nil {
		return err
	}
	response, err := c.httpClient().Do(req)
	if err != nil {
		return networkError("download generated image", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeAPIError(response)
	}
	temporary := path + ".partial"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return errs.NewInternalError(errs.SubtypeStorage, "create image output: %v", err).WithCause(err)
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		if copyErr != nil {
			return networkError("download generated image", copyErr)
		}
		return errs.NewInternalError(errs.SubtypeStorage, "close image output: %v", closeErr).WithCause(closeErr)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return errs.NewInternalError(errs.SubtypeStorage, "publish image output: %v", err).WithCause(err)
	}
	return nil
}

func (c *apiClient) requestJSON(ctx context.Context, method, path string, body io.Reader, contentType string, target any) error {
	req, err := c.request(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	response, err := c.httpClient().Do(req)
	if err != nil {
		return networkError("call Workline image service", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeAPIError(response)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return errs.NewNetworkError(errs.SubtypeNetworkProtocol, "decode Workline image response: %v", err).WithCause(err)
	}
	return nil
}

func (c *apiClient) request(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	endpoint := c.base.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "build Workline image request: %v", err).WithCause(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func (c *apiClient) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return httpClient
}

func decodeAPIError(response *http.Response) error {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
	message := payload.Error.Message
	if message == "" {
		message = "Workline image service returned HTTP " + strconv.Itoa(response.StatusCode)
	}
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return errs.NewAuthenticationError(errs.SubtypeTokenInvalid, "%s", message)
	case http.StatusNotFound:
		return errs.NewAPIError(errs.SubtypeNotFound, "%s", message)
	case http.StatusTooManyRequests:
		return errs.NewAPIError(errs.SubtypeRateLimit, "%s", message).WithRetryable()
	case http.StatusBadRequest:
		return errs.NewAPIError(errs.SubtypeInvalidParameters, "%s%s", message, errorCodeSuffix(payload.Error.Code))
	default:
		if response.StatusCode >= 500 {
			return errs.NewNetworkError(errs.SubtypeNetworkServer, "%s", message).WithRetryable()
		}
		return errs.NewAPIError(errs.SubtypeUnknown, "%s%s", message, errorCodeSuffix(payload.Error.Code))
	}
}

func errorCodeSuffix(code string) string {
	if code == "" {
		return ""
	}
	return " (" + code + ")"
}

func parseReferences(values []string) ([]reference, error) {
	result := make([]reference, 0, len(values))
	for _, value := range values {
		path, role, ok := strings.Cut(value, "=")
		path, role = strings.TrimSpace(path), strings.TrimSpace(role)
		if !ok || path == "" || role == "" {
			return nil, invalid("--reference must use path=role")
		}
		if err := requireRegularFile(path, "--reference"); err != nil {
			return nil, err
		}
		result = append(result, reference{Path: path, Role: role})
	}
	return result, nil
}

func validateImageOptions(size, outputFormat, background string, transparent bool) error {
	size = strings.TrimSpace(size)
	if size != "" && size != "auto" {
		widthText, heightText, ok := strings.Cut(size, "x")
		width, widthErr := strconv.ParseInt(widthText, 10, 64)
		height, heightErr := strconv.ParseInt(heightText, 10, 64)
		if !ok || widthErr != nil || heightErr != nil || width < 1 || height < 1 || width > 16_000_000/height {
			return invalid("--size must be auto or WIDTHxHEIGHT with at most 16000000 pixels")
		}
	}
	if strings.TrimSpace(outputFormat) == "jpeg" && (transparent || strings.TrimSpace(background) == "transparent") {
		return invalid("JPEG output cannot use a transparent background")
	}
	return nil
}

func requireRegularFile(path, flag string) error {
	info, err := os.Stat(path)
	if err != nil {
		return invalid("%s image %q is unavailable: %v", flag, path, err)
	}
	if !info.Mode().IsRegular() {
		return invalid("%s must reference a regular file", flag)
	}
	return nil
}

func extensionFor(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/vnd.adobe.photoshop":
		return ".psd"
	default:
		return ".png"
	}
}

func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for version := 2; ; version++ {
		candidate := fmt.Sprintf("%s-v%d%s", base, version, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func invalid(format string, args ...any) error {
	return errs.NewValidationError(errs.SubtypeInvalidArgument, format, args...)
}

func protocolError(action string, err error) error {
	return errs.NewInternalError(errs.SubtypeInvalidResponse, "%s: %v", action, err).WithCause(err)
}

func networkError(action string, err error) error {
	return errs.NewNetworkError(errs.SubtypeNetworkTransport, "%s: %v", action, err).WithCause(err).WithRetryable()
}
