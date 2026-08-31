// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package imagegen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/larksuite/cli/shortcuts/common"
)

const (
	defaultScriptModel        = "gpt-image-2"
	defaultScriptSize         = "auto"
	defaultScriptQuality      = "medium"
	defaultScriptOutputFormat = "png"
	defaultScriptOutput       = "output/imagegen/output.png"
	maxScriptBatchJobs        = 500
)

type scriptJob struct {
	request map[string]any
	refs    []reference
	mask    string
	outputs []string
	force   bool
}

func scriptGenerateShortcut() common.Shortcut {
	flags := append(scriptSharedFlags(), common.Flag{Name: "reference", Type: "string_array", Desc: "Reference image as path=role; repeat up to five times"})
	return common.Shortcut{
		Service: "image", Command: "generate", Description: "Create a new image", Risk: "write",
		Scopes: []string{}, AuthTypes: []string{"user", "bot"}, Flags: flags,
		DryRun: dryRunScriptGenerate, Validate: validateScriptGenerate, Execute: executeScriptGenerate,
	}
}

func scriptEditShortcut() common.Shortcut {
	flags := append(scriptSharedFlags(),
		common.Flag{Name: "image", Type: "string_array", Desc: "Input image; repeat for multiple ordered images"},
		common.Flag{Name: "mask", Desc: "Optional PNG mask"},
		common.Flag{Name: "input-fidelity", Desc: "Input fidelity compatibility option", Enum: []string{"low", "high"}},
	)
	return common.Shortcut{
		Service: "image", Command: "edit", Description: "Edit one or more existing images", Risk: "write",
		Scopes: []string{}, AuthTypes: []string{"user", "bot"}, Flags: flags,
		DryRun: dryRunScriptEdit, Validate: validateScriptEdit, Execute: executeScriptEdit,
	}
}

func scriptBatchShortcut() common.Shortcut {
	flags := append(scriptSharedFlags(),
		common.Flag{Name: "input", Desc: "JSONL file containing one generation job per line", Required: true},
		common.Flag{Name: "concurrency", Type: "int", Default: "5", Desc: "Maximum concurrent jobs (1-25)"},
		common.Flag{Name: "max-attempts", Type: "int", Default: "3", Desc: "Maximum attempts per job (1-10)"},
		common.Flag{Name: "fail-fast", Type: "bool", Desc: "Stop scheduling after the first failed job"},
	)
	return common.Shortcut{
		Service: "image", Command: "generate-batch", Description: "Generate multiple prompts from JSONL", Risk: "write",
		Scopes: []string{}, AuthTypes: []string{"user", "bot"}, Flags: flags,
		DryRun: dryRunScriptBatch, Validate: validateScriptBatch, Execute: executeScriptBatch,
	}
}

func scriptSharedFlags() []common.Flag {
	return []common.Flag{
		{Name: "model", Default: defaultScriptModel, Desc: "Compatibility model profile; currently supports gpt-image-2"},
		{Name: "prompt", Desc: "Image prompt"},
		{Name: "prompt-file", Desc: "UTF-8 file containing the image prompt"},
		{Name: "n", Type: "int", Default: "1", Desc: "Number of images (1-10)"},
		{Name: "size", Default: defaultScriptSize, Desc: "Output size: auto or WIDTHxHEIGHT"},
		{Name: "quality", Default: defaultScriptQuality, Desc: "Quality profile", Enum: []string{"low", "medium", "high", "auto"}},
		{Name: "background", Desc: "Background mode", Enum: []string{"transparent", "opaque", "auto"}},
		{Name: "output-format", Default: defaultScriptOutputFormat, Desc: "Output format", Enum: []string{"png", "jpeg", "jpg", "webp"}},
		{Name: "output-compression", Type: "int", Default: "-1", Desc: "JPEG or WebP compression quality (0-100)"},
		{Name: "moderation", Desc: "Moderation compatibility option", Enum: []string{"auto", "low"}},
		{Name: "out", Default: defaultScriptOutput, Desc: "Output file path"},
		{Name: "out-dir", Desc: "Output directory; names files image_1, image_2, and so on"},
		{Name: "force", Type: "bool", Desc: "Overwrite existing outputs"},
		{Name: "augment", Type: "bool", Desc: "Enable structured prompt augmentation (default)"},
		{Name: "no-augment", Type: "bool", Desc: "Disable structured prompt augmentation"},
		{Name: "use-case", Desc: "Prompt augmentation: use case"},
		{Name: "scene", Desc: "Prompt augmentation: scene or background"},
		{Name: "subject", Desc: "Prompt augmentation: subject"},
		{Name: "style", Desc: "Prompt augmentation: style or medium"},
		{Name: "composition", Desc: "Prompt augmentation: composition or framing"},
		{Name: "lighting", Desc: "Prompt augmentation: lighting or mood"},
		{Name: "palette", Desc: "Prompt augmentation: color palette"},
		{Name: "materials", Desc: "Prompt augmentation: materials or textures"},
		{Name: "text", Desc: "Prompt augmentation: exact text"},
		{Name: "constraints", Desc: "Prompt augmentation: constraints"},
		{Name: "negative", Desc: "Prompt augmentation: avoid list"},
	}
}

func validateScriptGenerate(_ context.Context, r *common.RuntimeContext) error {
	if err := validateScriptSingle(r, false); err != nil {
		return err
	}
	references, err := parseReferences(r.StrArray("reference"))
	if err != nil {
		return err
	}
	if len(references) > 5 {
		return invalid("at most five --reference images are supported")
	}
	return nil
}

func validateScriptEdit(_ context.Context, r *common.RuntimeContext) error {
	if err := validateScriptSingle(r, true); err != nil {
		return err
	}
	images := r.StrArray("image")
	if len(images) < 1 || len(images) > 16 {
		return invalid("--image must be repeated between 1 and 16 times")
	}
	for _, path := range images {
		if err := requireRegularFile(path, "--image"); err != nil {
			return err
		}
	}
	if mask := strings.TrimSpace(r.Str("mask")); mask != "" {
		if err := requireRegularFile(mask, "--mask"); err != nil {
			return err
		}
	}
	if fidelity := strings.TrimSpace(r.Str("input-fidelity")); fidelity != "" {
		return invalid("--input-fidelity is not supported by the gpt-image-2 compatibility profile")
	}
	return nil
}

func validateScriptBatch(_ context.Context, r *common.RuntimeContext) error {
	if strings.TrimSpace(r.Str("out-dir")) == "" {
		return invalid("generate-batch requires --out-dir")
	}
	if concurrency := r.Int("concurrency"); concurrency < 1 || concurrency > 25 {
		return invalid("--concurrency must be between 1 and 25")
	}
	if attempts := r.Int("max-attempts"); attempts < 1 || attempts > 10 {
		return invalid("--max-attempts must be between 1 and 10")
	}
	if err := validateScriptOptions(r); err != nil {
		return err
	}
	_, err := readBatchJobs(r.Str("input"), r)
	return err
}

func validateScriptSingle(r *common.RuntimeContext, edit bool) error {
	prompt, promptFile := strings.TrimSpace(r.Str("prompt")), strings.TrimSpace(r.Str("prompt-file"))
	if (prompt == "") == (promptFile == "") {
		return invalid("use exactly one of --prompt or --prompt-file")
	}
	if promptFile != "" {
		if err := requireRegularFile(promptFile, "--prompt-file"); err != nil {
			return err
		}
	}
	if edit && r.Bool("augment") && r.Bool("no-augment") {
		return invalid("--augment and --no-augment cannot be used together")
	}
	return validateScriptOptions(r)
}

func validateScriptOptions(r *common.RuntimeContext) error {
	if strings.TrimSpace(r.Str("model")) != defaultScriptModel {
		return invalid("only the gpt-image-2 compatibility profile is currently supported")
	}
	if n := r.Int("n"); n < 1 || n > 10 {
		return invalid("--n must be between 1 and 10")
	}
	if err := validateGPTImage2Size(strings.TrimSpace(r.Str("size"))); err != nil {
		return err
	}
	format := normalizedScriptFormat(r.Str("output-format"))
	background := strings.TrimSpace(r.Str("background"))
	if background == "transparent" && format != "png" && format != "webp" {
		return invalid("transparent background requires --output-format png or webp")
	}
	compression := r.Int("output-compression")
	if compression < -1 || compression > 100 {
		return invalid("--output-compression must be between 0 and 100")
	}
	if compression >= 0 && format != "jpeg" && format != "webp" {
		return invalid("--output-compression requires JPEG or WebP output")
	}
	if moderation := strings.TrimSpace(r.Str("moderation")); moderation == "low" {
		return invalid("the managed image tool does not expose --moderation low")
	}
	if r.Bool("augment") && r.Bool("no-augment") {
		return invalid("--augment and --no-augment cannot be used together")
	}
	return nil
}

func validateGPTImage2Size(size string) error {
	if size == "auto" {
		return nil
	}
	widthText, heightText, ok := strings.Cut(size, "x")
	width, widthErr := strconv.Atoi(widthText)
	height, heightErr := strconv.Atoi(heightText)
	if !ok || widthErr != nil || heightErr != nil || width < 1 || height < 1 {
		return invalid("--size must be auto or WIDTHxHEIGHT")
	}
	maxEdge, minEdge := width, height
	if maxEdge < minEdge {
		maxEdge, minEdge = minEdge, maxEdge
	}
	pixels := int64(width) * int64(height)
	if maxEdge > 3840 || width%16 != 0 || height%16 != 0 || float64(maxEdge)/float64(minEdge) > 3 || pixels < 655_360 || pixels > 8_294_400 {
		return invalid("--size must satisfy gpt-image-2 limits: edges <=3840, multiples of 16, ratio <=3:1, and 655360-8294400 pixels")
	}
	return nil
}

func executeScriptGenerate(ctx context.Context, r *common.RuntimeContext) error {
	job, err := prepareScriptSingle(r, false)
	if err != nil {
		return err
	}
	result, err := runScriptJob(ctx, r, job)
	if err != nil {
		return err
	}
	r.Out(result, nil)
	return nil
}

func executeScriptEdit(ctx context.Context, r *common.RuntimeContext) error {
	job, err := prepareScriptSingle(r, true)
	if err != nil {
		return err
	}
	result, err := runScriptJob(ctx, r, job)
	if err != nil {
		return err
	}
	r.Out(result, nil)
	return nil
}

func executeScriptBatch(ctx context.Context, r *common.RuntimeContext) error {
	jobs, err := readBatchJobs(r.Str("input"), r)
	if err != nil {
		return err
	}
	if err := ensureDistinctOutputs(jobs); err != nil {
		return err
	}
	results := make([]map[string]any, len(jobs))
	errorsByJob := make([]string, len(jobs))
	sem := make(chan struct{}, r.Int("concurrency"))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for index := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var runErr error
			for attempt := 1; attempt <= r.Int("max-attempts"); attempt++ {
				results[i], runErr = runScriptJob(ctx, r, jobs[i])
				if runErr == nil || ctx.Err() != nil {
					break
				}
				if attempt < r.Int("max-attempts") {
					time.Sleep(time.Duration(1<<min(attempt, 5)) * time.Second)
				}
			}
			if runErr != nil {
				errorsByJob[i] = runErr.Error()
				if r.Bool("fail-fast") {
					cancel()
				}
			}
		}(index)
	}
	wg.Wait()
	failed := 0
	items := make([]map[string]any, len(jobs))
	for index := range jobs {
		items[index] = map[string]any{"job": index + 1}
		if errorsByJob[index] != "" {
			failed++
			items[index]["status"] = "failed"
			items[index]["error"] = errorsByJob[index]
		} else if results[index] != nil {
			items[index]["status"] = "succeeded"
			items[index]["result"] = results[index]
		} else {
			failed++
			items[index]["status"] = "cancelled"
		}
	}
	r.Out(map[string]any{"status": map[bool]string{true: "partial_failure", false: "succeeded"}[failed > 0], "total": len(jobs), "failed": failed, "jobs": items}, nil)
	if failed > 0 {
		return fmt.Errorf("%d of %d image jobs failed", failed, len(jobs))
	}
	return nil
}

func prepareScriptSingle(r *common.RuntimeContext, edit bool) (scriptJob, error) {
	prompt, err := scriptPrompt(r.Str("prompt"), r.Str("prompt-file"))
	if err != nil {
		return scriptJob{}, err
	}
	prompt = augmentScriptPrompt(prompt, r, nil)
	request := scriptRequest(prompt, r.Int("n"), r.Str("size"), r.Str("quality"), r.Str("background"), r.Str("output-format"), r.Int("output-compression"))
	refs := []reference{}
	mask := ""
	if edit {
		request["operation"] = "edit"
		for index, path := range r.StrArray("image") {
			refs = append(refs, reference{Path: path, Role: fmt.Sprintf("IMAGE_%d", index+1)})
		}
		mask = strings.TrimSpace(r.Str("mask"))
	} else {
		refs, err = parseReferences(r.StrArray("reference"))
		if err != nil {
			return scriptJob{}, err
		}
	}
	outputs, err := scriptOutputPaths(r.Str("out"), r.Str("out-dir"), normalizedScriptFormat(r.Str("output-format")), r.Int("n"), r.Bool("force"))
	if err != nil {
		return scriptJob{}, err
	}
	return scriptJob{request: request, refs: refs, mask: mask, outputs: outputs, force: r.Bool("force")}, nil
}

func scriptRequest(prompt string, n int, size, quality, background, outputFormat string, compression int) map[string]any {
	request := map[string]any{"operation": "generate", "prompt": prompt, "n": n, "size": strings.TrimSpace(size), "quality": strings.TrimSpace(quality), "output_format": normalizedScriptFormat(outputFormat)}
	if background = strings.TrimSpace(background); background != "" {
		request["background"] = background
		if background == "transparent" {
			request["transparent_background"] = true
		}
	}
	if compression >= 0 {
		request["output_compression"] = compression
	}
	return request
}

func scriptPrompt(prompt, promptFile string) (string, error) {
	if strings.TrimSpace(promptFile) != "" {
		content, err := os.ReadFile(promptFile)
		if err != nil {
			return "", invalid("read --prompt-file: %v", err)
		}
		prompt = string(content)
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", invalid("image prompt must not be blank")
	}
	if len(prompt) > 8_000 {
		return "", invalid("image prompt must not exceed 8000 bytes")
	}
	return prompt, nil
}

func augmentScriptPrompt(prompt string, r *common.RuntimeContext, overrides map[string]string) string {
	if r.Bool("no-augment") {
		return prompt
	}
	fields := []struct{ flag, label string }{
		{"use-case", "Use case"}, {"scene", "Scene/background"}, {"subject", "Subject"}, {"style", "Style/medium"},
		{"composition", "Composition/framing"}, {"lighting", "Lighting/mood"}, {"palette", "Color palette"},
		{"materials", "Materials/textures"}, {"text", "Text (verbatim)"}, {"constraints", "Constraints"}, {"negative", "Avoid"},
	}
	sections := []string{}
	value := func(flag string) string {
		if overrides != nil && overrides[flag] != "" {
			return overrides[flag]
		}
		return strings.TrimSpace(r.Str(flag))
	}
	if useCase := value("use-case"); useCase != "" {
		sections = append(sections, "Use case: "+useCase)
	}
	sections = append(sections, "Primary request: "+prompt)
	for _, field := range fields[1:] {
		if item := value(field.flag); item != "" {
			if field.flag == "text" {
				item = `"` + item + `"`
			}
			sections = append(sections, field.label+": "+item)
		}
	}
	return strings.Join(sections, "\n")
}

func scriptOutputPaths(out, outDir, format string, count int, force bool) ([]string, error) {
	paths := make([]string, 0, count)
	ext := "." + format
	if strings.TrimSpace(outDir) != "" {
		for index := 1; index <= count; index++ {
			paths = append(paths, filepath.Join(outDir, fmt.Sprintf("image_%d%s", index, ext)))
		}
	} else {
		base := strings.TrimSpace(out)
		if filepath.Ext(base) == "" {
			base += ext
		}
		if count == 1 {
			paths = append(paths, base)
		} else {
			baseExt := filepath.Ext(base)
			stem := strings.TrimSuffix(base, baseExt)
			for index := 1; index <= count; index++ {
				paths = append(paths, fmt.Sprintf("%s-%d%s", stem, index, baseExt))
			}
		}
	}
	for index, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, invalid("invalid output path %q: %v", path, err)
		}
		paths[index] = absolute
		if _, err := os.Stat(absolute); err == nil && !force {
			return nil, invalid("output already exists: %s (use --force to overwrite)", absolute)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, invalid("inspect output %q: %v", absolute, err)
		}
	}
	return paths, nil
}

func normalizedScriptFormat(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultScriptOutputFormat
	}
	if value == "jpg" {
		return "jpeg"
	}
	return value
}

func runScriptJob(ctx context.Context, r *common.RuntimeContext, job scriptJob) (map[string]any, error) {
	client, err := newAPIClient(r)
	if err != nil {
		return nil, err
	}
	remoteRefs := make([]map[string]string, 0, len(job.refs))
	for _, ref := range job.refs {
		mediaRef, uploadErr := client.upload(ctx, ref.Path)
		if uploadErr != nil {
			return nil, uploadErr
		}
		remoteRefs = append(remoteRefs, map[string]string{"media_ref": mediaRef, "role": ref.Role})
	}
	if len(remoteRefs) > 0 {
		job.request["references"] = remoteRefs
	}
	if job.mask != "" {
		mediaRef, uploadErr := client.upload(ctx, job.mask)
		if uploadErr != nil {
			return nil, uploadErr
		}
		job.request["mask_media_ref"] = mediaRef
	}
	submitted, err := client.submit(ctx, job.request)
	if err != nil {
		return nil, err
	}
	snapshot, err := client.wait(ctx, submitted.TaskRef)
	if err != nil {
		return nil, err
	}
	result, err := client.downloadPlanned(ctx, snapshot, job.outputs, job.force)
	if err != nil {
		return nil, err
	}
	result["reused"] = submitted.Reused
	return result, nil
}

func (c *apiClient) downloadPlanned(ctx context.Context, job jobSnapshot, paths []string, force bool) (map[string]any, error) {
	outputs := make([]map[string]any, 0, len(job.Outputs))
	pathIndex := 0
	for _, output := range job.Outputs {
		if output.Status != "succeeded" || output.MediaRef == "" || output.ArtifactRef == "" {
			continue
		}
		if pathIndex >= len(paths) {
			return nil, invalid("image job returned more outputs than requested")
		}
		path := paths[pathIndex]
		pathIndex++
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, invalid("create output directory: %v", err)
		}
		if _, err := os.Stat(path); err == nil {
			if !force {
				return nil, invalid("output already exists: %s (use --force to overwrite)", path)
			}
			if err := os.Remove(path); err != nil {
				return nil, invalid("replace output %q: %v", path, err)
			}
		}
		var details artifact
		if err := c.requestJSON(ctx, "GET", "/v1/artifacts/"+output.ArtifactRef, nil, "", &details); err != nil {
			return nil, err
		}
		if err := c.downloadFile(ctx, output.MediaRef, path); err != nil {
			return nil, err
		}
		item := map[string]any{"output_ref": output.OutputRef, "kind": output.Kind, "artifact_ref": output.ArtifactRef, "media_ref": output.MediaRef, "mime_type": output.Format, "path": path}
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
	if len(outputs) != len(paths) {
		return nil, invalid("image job completed with %d downloadable outputs; expected %d", len(outputs), len(paths))
	}
	return map[string]any{"task_ref": job.TaskRef, "status": job.Status, "stage": job.Stage, "attempt": job.Attempt, "updated_at": job.Updated, "outputs": outputs}, nil
}

func dryRunScriptGenerate(_ context.Context, r *common.RuntimeContext) *common.DryRunAPI {
	job, _ := prepareScriptSingle(r, false)
	return dryRunScriptJob(job, "Generate image")
}

func dryRunScriptEdit(_ context.Context, r *common.RuntimeContext) *common.DryRunAPI {
	job, _ := prepareScriptSingle(r, true)
	body := cloneMap(job.request)
	body["images"] = pathsFromReferences(job.refs)
	if job.mask != "" {
		body["mask"] = job.mask
	}
	job.request = body
	return dryRunScriptJob(job, "Edit image")
}

func dryRunScriptBatch(_ context.Context, r *common.RuntimeContext) *common.DryRunAPI {
	jobs, _ := readBatchJobs(r.Str("input"), r)
	plan := common.NewDryRunAPI().Desc("Generate image batch")
	for index, job := range jobs {
		plan.POST("/v1/image-jobs").Desc(fmt.Sprintf("Job %d", index+1)).Body(job.request)
		for _, path := range job.outputs {
			plan.File(common.DryRunFileIntent{Name: path, IfExists: map[bool]string{true: "overwrite", false: "error"}[job.force]})
		}
	}
	return plan
}

func dryRunScriptJob(job scriptJob, description string) *common.DryRunAPI {
	plan := common.NewDryRunAPI().Desc(description).POST("/v1/image-jobs").Body(job.request)
	for _, path := range job.outputs {
		plan.File(common.DryRunFileIntent{Name: path, IfExists: map[bool]string{true: "overwrite", false: "error"}[job.force]})
	}
	return plan
}

func readBatchJobs(path string, r *common.RuntimeContext) ([]scriptJob, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, invalid("read --input: %v", err)
	}
	lines := strings.Split(string(content), "\n")
	jobs := []scriptJob{}
	outputDir := strings.TrimSpace(r.Str("out-dir"))
	for lineNumber, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry := map[string]any{}
		if strings.HasPrefix(line, "{") {
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				return nil, invalid("invalid JSON on line %d: %v", lineNumber+1, err)
			}
		} else {
			entry["prompt"] = line
		}
		prompt := strings.TrimSpace(stringFromAny(entry["prompt"]))
		if prompt == "" {
			return nil, invalid("missing prompt on line %d", lineNumber+1)
		}
		overrides := map[string]string{}
		for _, name := range []string{"use-case", "scene", "subject", "style", "composition", "lighting", "palette", "materials", "text", "constraints", "negative"} {
			jsonName := strings.ReplaceAll(name, "-", "_")
			overrides[name] = strings.TrimSpace(stringFromAny(entry[jsonName]))
		}
		prompt = augmentScriptPrompt(prompt, r, overrides)
		n := intFromAny(entry["n"], r.Int("n"))
		size := stringDefault(entry["size"], r.Str("size"))
		quality := stringDefault(entry["quality"], r.Str("quality"))
		background := stringDefault(entry["background"], r.Str("background"))
		format := normalizedScriptFormat(stringDefault(entry["output_format"], r.Str("output-format")))
		compression := intFromAny(entry["output_compression"], r.Int("output-compression"))
		if n < 1 || n > 10 {
			return nil, invalid("job %d n must be between 1 and 10", len(jobs)+1)
		}
		if err := validateGPTImage2Size(size); err != nil {
			return nil, err
		}
		if !map[string]bool{"low": true, "medium": true, "high": true, "auto": true}[quality] {
			return nil, invalid("job %d quality must be low, medium, high, or auto", len(jobs)+1)
		}
		if !map[string]bool{"": true, "transparent": true, "opaque": true, "auto": true}[background] {
			return nil, invalid("job %d background must be transparent, opaque, or auto", len(jobs)+1)
		}
		if !map[string]bool{"png": true, "jpeg": true, "webp": true}[format] {
			return nil, invalid("job %d output_format must be png, jpeg, jpg, or webp", len(jobs)+1)
		}
		if background == "transparent" && format == "jpeg" {
			return nil, invalid("job %d transparent background requires PNG or WebP", len(jobs)+1)
		}
		if compression < -1 || compression > 100 || (compression >= 0 && format != "jpeg" && format != "webp") {
			return nil, invalid("job %d output_compression must be 0-100 with JPEG or WebP", len(jobs)+1)
		}
		request := scriptRequest(prompt, n, size, quality, background, format, compression)
		explicitOut := strings.TrimSpace(stringFromAny(entry["out"]))
		if explicitOut == "" {
			explicitOut = fmt.Sprintf("%03d-%s.%s", len(jobs)+1, slugScriptPrompt(prompt), format)
		}
		outputs, err := scriptOutputPaths(filepath.Join(outputDir, filepath.Base(explicitOut)), "", format, n, r.Bool("force"))
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, scriptJob{request: request, outputs: outputs, force: r.Bool("force")})
	}
	if len(jobs) == 0 {
		return nil, invalid("no jobs found in --input")
	}
	if len(jobs) > maxScriptBatchJobs {
		return nil, invalid("too many jobs: maximum is %d", maxScriptBatchJobs)
	}
	return jobs, nil
}

func ensureDistinctOutputs(jobs []scriptJob) error {
	seen := map[string]bool{}
	for _, job := range jobs {
		for _, path := range job.outputs {
			key := strings.ToLower(filepath.Clean(path))
			if seen[key] {
				return invalid("multiple jobs resolve to the same output: %s", path)
			}
			seen[key] = true
		}
	}
	return nil
}

var scriptSlugPattern = regexp.MustCompile(`[^a-z0-9]+`)

func slugScriptPrompt(value string) string {
	value = strings.Trim(scriptSlugPattern.ReplaceAllString(strings.ToLower(value), "-"), "-")
	if value == "" {
		return "job"
	}
	if len(value) > 60 {
		value = value[:60]
	}
	return value
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func pathsFromReferences(refs []reference) []string {
	paths := make([]string, len(refs))
	for index, ref := range refs {
		paths[index] = ref.Path
	}
	return paths
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func stringDefault(value any, fallback string) string {
	if text := strings.TrimSpace(stringFromAny(value)); text != "" {
		return text
	}
	return strings.TrimSpace(fallback)
}

func intFromAny(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, _ := strconv.Atoi(string(typed))
		return parsed
	case string:
		parsed, err := strconv.Atoi(typed)
		if err == nil {
			return parsed
		}
	}
	return fallback
}
