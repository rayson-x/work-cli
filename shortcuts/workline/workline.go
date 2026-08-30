// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package workline contains the small, deterministic Workline interface used
// by the fashion-style agent.  It intentionally uses the existing shortcut
// runtime and the Base v3 records API; there is no second client or ORM.
package workline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/larksuite/cli/errs"
	"github.com/larksuite/cli/internal/core"
	basecmd "github.com/larksuite/cli/shortcuts/base"
	"github.com/larksuite/cli/shortcuts/common"
)

const (
	InterfaceVersion = "workline.v1"
	SchemaVersion    = "workline.schema.v1"
)

// Tables is the frozen MVP physical schema.  Field names deliberately match
// the interface ticket and are also used as the primary field names in Base.
var Tables = map[string][]string{
	"_Meta":              {"key", "value", "interface_version", "schema_version", "enterprise_key", "updated_at"},
	"People":             {"person_id", "name", "organization", "functions", "identity_status", "notes"},
	"SourceIdentities":   {"identity_id", "platform", "wechat_id", "source_identity_key", "identity_kind", "identity_scope", "display_name", "person", "mapping_status", "mapping_basis"},
	"RoleClaims":         {"role_claim_id", "person", "source_identity", "function", "organization", "scope_type", "scope_key", "valid_from", "valid_to", "supporting_evidence", "status"},
	"Evidence":           {"evidence_id", "source_key", "wechat_owner_id", "conversation_id", "conversation_type", "message_id", "forward_path", "speaker_identity", "source_time", "reply_to_evidence_id", "content_type", "excerpt", "image", "raw_locator", "content_hash"},
	"EvidenceEventLinks": {"link_id", "evidence", "event", "support_type", "interpretation", "record_state"},
	"Events":             {"event_id", "summary", "event_type", "expression_mode", "actors", "actor_identities", "occurred_at", "time_basis", "record_state", "canonical_event_id", "created_operation_id", "revision"},
	"Styles":             {"style_id", "name", "style_code", "aliases", "representative_images", "attributes", "style_status", "created_from_event_id", "created_from_evidence_id", "record_state", "canonical_style_id", "revision"},
	"StyleIdentifiers":   {"identifier_id", "style", "issuer_or_scope", "identifier_kind", "value", "normalized_value", "supporting_evidence"},
	"EventStyleLinks":    {"link_id", "event", "style", "link_status", "basis", "created_operation_id", "revision"},
	"EventRelations":     {"relation_id", "from_event", "relation_type", "to_event", "relation_status", "basis", "created_operation_id"},
	"Operations":         {"operation_id", "operation_type", "request_hash", "targets", "winner_id", "step", "state", "payload", "error", "created_at", "updated_at"},
}

var tableOrder = []string{"_Meta", "People", "SourceIdentities", "RoleClaims", "Evidence", "EvidenceEventLinks", "Events", "Styles", "StyleIdentifiers", "EventStyleLinks", "EventRelations", "Operations"}

var knownTableIDs = map[string]string{}
var knownRecordIDs = map[string]string{}

// applyReadState is scoped to one RuntimeContext.  Workline actions used to
// re-list the target table for every upsert (and link resolution), which made
// a large apply quadratic in API calls.  During +apply we retain the decoded
// snapshot and update it as writes complete.  The state is deliberately not
// global to a Base: two concurrent invocations must never observe each
// other's in-memory rows.
type applyReadState struct {
	mu      sync.Mutex
	loaded  map[string][]map[string]any
	decoded map[string]bool
}

var applyStates sync.Map // map[*common.RuntimeContext]*applyReadState

// schemaType is intentionally explicit. Relationship columns use native Base
// links; the command maps stable business IDs to record IDs at write time.
func schemaType(table, field string) string {
	switch field {
	case "source_time", "occurred_at", "valid_from", "valid_to":
		return "datetime"
	case "created_at":
		return "created_at"
	case "updated_at":
		return "updated_at"
	case "revision":
		return "number"
	case "identity_status", "mapping_status", "identity_kind", "scope_type", "status", "record_state", "expression_mode", "time_basis", "support_type", "link_status", "relation_status", "state", "conversation_type", "content_type", "operation_type", "functions", "platform", "style_status":
		return "select"
	case "image", "representative_images":
		return "attachment"
	}
	if target, _ := relationTarget(table, field); target != "" {
		return "link"
	}
	return "text"
}

func relationTarget(table, field string) (target string, multiple bool) {
	switch table + "." + field {
	case "SourceIdentities.person", "RoleClaims.person":
		return "People", false
	case "RoleClaims.source_identity":
		return "SourceIdentities", false
	case "RoleClaims.supporting_evidence", "StyleIdentifiers.supporting_evidence":
		return "Evidence", true
	case "Evidence.speaker_identity":
		return "SourceIdentities", false
	case "Styles.created_from_evidence_id":
		return "Evidence", false
	case "Events.actors":
		return "People", true
	case "Events.actor_identities":
		return "SourceIdentities", true
	case "StyleIdentifiers.style":
		return "Styles", false
	case "EvidenceEventLinks.evidence":
		return "Evidence", false
	case "EvidenceEventLinks.event":
		return "Events", false
	case "EventStyleLinks.event":
		return "Events", false
	case "EventStyleLinks.style":
		return "Styles", false
	case "EventRelations.from_event", "EventRelations.to_event":
		return "Events", false
	default:
		return "", false
	}
}

func isSystemField(field string) bool { return field == "created_at" || field == "updated_at" }

func schemaOptions(field string) []map[string]any {
	choices := map[string][]string{
		"identity_status": {"inferred", "confirmed"}, "mapping_status": {"unresolved", "inferred", "confirmed"},
		"identity_kind": {"wechat_id", "forward_hash"},
		"scope_type":    {"global", "conversation", "style", "time_range"}, "status": {"inferred", "confirmed", "rejected"},
		"record_state": {"active", "excluded", "merged", "removed"}, "expression_mode": {"fact", "plan", "commitment", "question", "feedback", "reported"},
		"time_basis": {"explicit", "inferred", "message_time"}, "support_type": {"direct", "supporting", "confirming", "contradicting", "reported"},
		"link_status": {"proposed", "confirmed", "rejected", "removed"}, "relation_status": {"proposed", "confirmed", "rejected"},
		"style_status": {"candidate", "confirmed"},
		"state":        {"pending", "running", "completed", "failed"}, "operation_type": {"event_merge", "event_split", "style_merge", "relink"}, "conversation_type": {"private", "group"}, "content_type": {"text", "image", "video"},
		"platform": {"wechat"}, "functions": {"设计", "跟单", "版师", "供应链"},
	}
	values := choices[field]
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		out = append(out, map[string]any{"name": value})
	}
	return out
}

func Shortcuts() []common.Shortcut {
	return []common.Shortcut{queryShortcut(), applyShortcut(), styleEventsShortcut()}
}

func baseFlags() []common.Flag {
	return []common.Flag{{Name: "base-token", Desc: "Workline Base token; may also be supplied by WORKLINE_BASE_TOKEN"}}
}

func queryShortcut() common.Shortcut {
	return common.Shortcut{Service: "workline", Command: "+query", Description: "Query Workline evidence, events, styles, people, context, or operations", Risk: "read", UserScopes: []string{"base:record:read", "base:table:read", "base:field:read"}, BotScopes: []string{"base:record:read", "base:table:read", "base:field:read"}, AuthTypes: []string{"user", "bot"}, Flags: append(baseFlags(), common.Flag{Name: "json", Desc: "query request JSON", Required: true, Input: []string{common.File, common.Stdin}}), DryRun: dryRunQuery, Validate: validateQuery, Execute: executeQuery}
}

func applyShortcut() common.Shortcut {
	return common.Shortcut{Service: "workline", Command: "+apply", Description: "Apply deterministic Workline actions", Risk: "write", UserScopes: []string{"base:record:read", "base:record:create", "base:record:update", "base:table:read", "base:field:read"}, BotScopes: []string{"base:record:read", "base:record:create", "base:record:update", "base:table:read", "base:field:read"}, ConditionalUserScopes: []string{"base:app:create", "base:table:create", "base:field:create", "docs:document.media:upload"}, ConditionalBotScopes: []string{"base:app:create", "base:table:create", "base:field:create", "docs:document.media:upload", "docs:permission.member:create"}, AuthTypes: []string{"user", "bot"}, Flags: append(baseFlags(), common.Flag{Name: "json", Desc: "apply request JSON", Required: true, Input: []string{common.File, common.Stdin}}), DryRun: dryRunApply, Validate: validateApply, Execute: executeApply}
}

func styleEventsShortcut() common.Shortcut {
	return common.Shortcut{Service: "workline", Command: "+style-events", Description: "Read the current effective events for a Style", Risk: "read", UserScopes: []string{"base:record:read", "base:table:read", "base:field:read"}, BotScopes: []string{"base:record:read", "base:table:read", "base:field:read"}, AuthTypes: []string{"user", "bot"}, Flags: append(baseFlags(), common.Flag{Name: "style-id", Desc: "Style business ID", Required: true}), DryRun: dryRunStyleEvents, Validate: func(_ context.Context, r *common.RuntimeContext) error {
		if strings.TrimSpace(r.Str("style-id")) == "" {
			return invalid("--style-id is required")
		}
		return nil
	}, Execute: executeStyleEvents}
}

func invalid(format string, args ...any) error {
	return errs.NewValidationError(errs.SubtypeInvalidArgument, format, args...)
}

func parseJSON(runtime *common.RuntimeContext) (map[string]any, []byte, error) {
	raw := strings.TrimSpace(runtime.Str("json"))
	if raw == "" {
		return nil, nil, invalid("--json must not be empty")
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return nil, nil, errs.NewValidationError(errs.SubtypeInvalidArgument, "invalid --json: %v", err).WithCause(err)
	}
	if req == nil {
		return nil, nil, invalid("--json must be a JSON object")
	}
	if v, ok := req["interface_version"].(string); !ok || strings.TrimSpace(v) == "" {
		return nil, nil, invalid("interface_version is required")
	} else if v != InterfaceVersion {
		return nil, nil, invalid("unsupported interface_version %q", v)
	}
	return req, []byte(raw), nil
}

func validateQuery(_ context.Context, r *common.RuntimeContext) error {
	req, _, err := parseJSON(r)
	if err != nil {
		return err
	}
	if strings.TrimSpace(stringValue(req["operation_id"])) == "" {
		return invalid("operation_id is required")
	}
	q, _ := req["query"].(string)
	switch q {
	case "evidence", "event", "style", "person", "context", "operation":
		filters, exists := req["filters"]
		if !exists {
			return nil
		}
		values, ok := filters.(map[string]any)
		if !ok {
			return invalid("filters must be an object")
		}
		allowed := queryFilterFields(q)
		for key := range values {
			if !allowed[key] {
				return invalid("filter %q is not supported for query %q", key, q)
			}
		}
		return nil
	default:
		return invalid("query must be one of evidence, event, style, person, context, operation")
	}
}

func queryFilterFields(query string) map[string]bool {
	allowed := map[string]bool{}
	add := func(table string) {
		for _, field := range Tables[table] {
			allowed[field] = true
		}
	}
	switch query {
	case "evidence":
		add("Evidence")
		allowed["event_id"] = true
		allowed["from"], allowed["to"] = true, true
	case "event":
		add("Events")
		allowed["style_id"], allowed["evidence_id"] = true, true
		allowed["from"], allowed["to"] = true, true
	case "style":
		add("Styles")
		add("StyleIdentifiers")
		allowed["event_id"] = true
	case "person":
		add("People")
		add("SourceIdentities")
		add("RoleClaims")
	case "context":
		add("Evidence")
		allowed["from"], allowed["to"] = true, true
	case "operation":
		add("Operations")
	}
	return allowed
}

var allowedActions = map[string]bool{"identity.upsert": true, "role_claim.upsert": true, "evidence.upsert": true, "event.create": true, "event.attach_evidence": true, "event.relate": true, "style.create": true, "style_identifier.upsert": true, "event_style.set": true, "event.merge": true, "event.split": true, "style.merge": true}

func validateApply(_ context.Context, r *common.RuntimeContext) error {
	req, _, err := parseJSON(r)
	if err != nil {
		return err
	}
	if strings.TrimSpace(stringValue(req["operation_id"])) == "" {
		return invalid("operation_id is required")
	}
	actions, ok := req["actions"].([]any)
	if !ok || len(actions) == 0 {
		return invalid("actions must be a non-empty array")
	}
	for i, raw := range actions {
		a, ok := raw.(map[string]any)
		if !ok || !allowedActions[stringValue(a["type"])] {
			return invalid("actions[%d].type is unsupported", i)
		}
		payload, _ := a["payload"].(map[string]any)
		if payload == nil {
			return invalid("actions[%d].payload must be an object", i)
		}
		if err := validateActionPayload(stringValue(a["type"]), payload); err != nil {
			return invalid("actions[%d]: %v", i, err)
		}
	}
	return nil
}

func validateActionPayload(action string, p map[string]any) error {
	if err := validateEnumFields(p); err != nil {
		return err
	}
	require := func(keys ...string) error {
		for _, key := range keys {
			if stringValue(p[key]) == "" {
				return fmt.Errorf("%s requires %s", action, key)
			}
		}
		return nil
	}
	switch action {
	case "identity.upsert":
		if stringValue(p["wechat_id"]) == "" && stringValue(p["source_identity_key"]) == "" {
			return fmt.Errorf("identity.upsert requires wechat_id or source_identity_key")
		}
		return nil
	case "role_claim.upsert":
		person, identity := stringValue(p["person"]), stringValue(p["source_identity"])
		if (person == "") == (identity == "") {
			return fmt.Errorf("role_claim.upsert requires exactly one of person or source_identity")
		}
		if err := require("function", "scope_type"); err != nil {
			return err
		}
		return validateEnumValue("status", stringValue(p["status"]))
	case "evidence.upsert":
		if err := require("wechat_owner_id", "conversation_id", "message_id"); err != nil {
			return err
		}
		return require("content_type")
	case "event.create":
		if err := require("summary", "expression_mode"); err != nil {
			return err
		}
		if len(actionIDs(p, "evidence_ids", "evidence_id")) == 0 {
			return fmt.Errorf("event.create requires evidence_id or evidence_ids")
		}
	case "event.attach_evidence":
		if stringValue(p["event"]) == "" {
			p["event"] = stringValue(p["event_id"])
		}
		if stringValue(p["evidence"]) == "" {
			p["evidence"] = stringValue(p["evidence_id"])
		}
		return require("event", "evidence")
	case "event.relate":
		return require("from_event", "relation_type", "to_event", "relation_status")
	case "style.create":
		if stringValue(p["created_from_event_id"]) == "" && stringValue(p["created_from_evidence_id"]) == "" {
			return fmt.Errorf("style.create requires created_from_event_id or created_from_evidence_id")
		}
		if stringValue(p["name"]) == "" && stringValue(p["style_code"]) == "" && stringValue(p["aliases"]) == "" && !hasAttachmentInput(p["representative_images"]) {
			return fmt.Errorf("style.create requires name, style_code, aliases, or representative_images")
		}
		if status := stringValue(p["link_status"]); status != "" && status != "proposed" && status != "confirmed" {
			return fmt.Errorf("style.create link_status has unsupported value %q", status)
		}
	case "style_identifier.upsert":
		if err := require("style", "issuer_or_scope", "identifier_kind"); err != nil {
			return err
		}
		if stringValue(p["normalized_value"]) == "" && stringValue(p["value"]) == "" {
			return fmt.Errorf("style_identifier.upsert requires normalized_value or value")
		}
	case "event_style.set":
		if stringValue(p["event"]) == "" {
			p["event"] = stringValue(p["event_id"])
		}
		if stringValue(p["style"]) == "" {
			p["style"] = stringValue(p["style_id"])
		}
		if stringValue(p["link_status"]) == "" {
			p["link_status"] = stringValue(p["status"])
		}
		if err := require("event", "style", "link_status"); err != nil {
			return err
		}
		return validateEnumValue("link_status", stringValue(p["link_status"]))
	case "event.merge", "style.merge":
		if err := require("winner"); err != nil {
			return err
		}
		if len(stringValues(p["losers"])) == 0 {
			return fmt.Errorf("%s requires losers", action)
		}
	case "event.split":
		if err := require("event_id"); err != nil {
			return err
		}
		if events, ok := p["events"].([]any); !ok || len(events) == 0 {
			return fmt.Errorf("event.split requires events")
		} else {
			for index, raw := range events {
				item, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("event.split events[%d] must be an object", index)
				}
				if err := validateEnumFields(item); err != nil {
					return fmt.Errorf("event.split events[%d]: %v", index, err)
				}
				if stringValue(item["summary"]) == "" || stringValue(item["expression_mode"]) == "" {
					return fmt.Errorf("event.split events[%d] requires summary and expression_mode", index)
				}
				if len(actionIDs(item, "evidence_ids", "evidence_id")) == 0 {
					return fmt.Errorf("event.split events[%d] requires evidence_id or evidence_ids", index)
				}
			}
		}
	}
	return nil
}

func validateEnumFields(fields map[string]any) error {
	for _, field := range []string{"identity_status", "mapping_status", "identity_kind", "scope_type", "record_state", "expression_mode", "time_basis", "support_type", "link_status", "relation_status", "conversation_type", "content_type", "style_status"} {
		value := stringValue(fields[field])
		if err := validateEnumValue(field, value); err != nil {
			return err
		}
	}
	return nil
}

func validateEnumValue(field, value string) error {
	if value == "" {
		return nil
	}
	for _, option := range schemaOptions(field) {
		if stringValue(option["name"]) == value {
			return nil
		}
	}
	return fmt.Errorf("%s has unsupported value %q", field, value)
}

func dryRunQuery(_ context.Context, r *common.RuntimeContext) *common.DryRunAPI {
	req, _, _ := parseJSON(r)
	d := common.NewDryRunAPI().GET("/open-apis/base/v3/bases/:base_token/tables/:table_id/records").Params(map[string]any{"limit": 200}).Body(map[string]any{"workline_query": req}).Set("base_token", tokenFor(r)).Set("table_id", "<resolved_table_id>")
	return d
}
func dryRunApply(_ context.Context, r *common.RuntimeContext) *common.DryRunAPI {
	req, _, _ := parseJSON(r)
	return common.NewDryRunAPI().POST("/open-apis/base/v3/bases/:base_token/tables/:table_id/records").Body(map[string]any{"fields": map[string]any{"workline_payload": req}}).Set("base_token", tokenFor(r)).Set("table_id", "<resolved_table_id>")
}
func dryRunStyleEvents(_ context.Context, r *common.RuntimeContext) *common.DryRunAPI {
	return common.NewDryRunAPI().GET("/open-apis/base/v3/bases/:base_token/tables/:table_id/records").Params(map[string]any{"limit": 200}).Body(map[string]any{"style_id": r.Str("style-id")}).Set("base_token", tokenFor(r)).Set("table_id", "<EventStyleLinks>")
}

func tokenFor(r *common.RuntimeContext) string {
	if t := strings.TrimSpace(r.Str("base-token")); t != "" {
		return t
	}
	if r.Config != nil {
		if t := strings.TrimSpace(r.Config.WorklineBaseToken); t != "" {
			return t
		}
	}
	// Some shortcut runtime paths attach credentials without carrying custom
	// profile extensions into RuntimeContext.Config. Read the active profile as
	// a deterministic fallback so the shared Base remains reusable across new
	// agent sessions without an environment variable or a separate init step.
	if cfg, err := core.LoadMultiAppConfig(); err == nil {
		profile := ""
		if r.Config != nil {
			profile = r.Config.ProfileName
		}
		if app := cfg.CurrentAppConfig(profile); app != nil && app.Workline != nil {
			if t := strings.TrimSpace(app.Workline.BaseToken); t != "" {
				return t
			}
		}
	}
	if t := strings.TrimSpace(os.Getenv("WORKLINE_BASE_TOKEN")); t != "" {
		return t
	}
	return "<workline.base_token>"
}
func requireToken(r *common.RuntimeContext) (string, error) {
	t := tokenFor(r)
	if t == "" || strings.HasPrefix(t, "<") {
		return "", invalid("Base token is required; pass --base-token or configure WORKLINE_BASE_TOKEN")
	}
	return t, nil
}

func ensureBaseToken(r *common.RuntimeContext) (string, error) {
	if token := tokenFor(r); !strings.HasPrefix(token, "<") && token != "" {
		return token, nil
	}
	data, err := basecmd.WorklineBaseV3Call(r, "POST", "/open-apis/base/v3/bases", nil, map[string]any{"name": "Workline"})
	if err != nil {
		return "", err
	}
	token := stringValue(data["base_token"])
	if token == "" {
		token = stringValue(data["app_token"])
	}
	if token == "" {
		return "", errs.NewInternalError(errs.SubtypeInvalidResponse, "Workline Base create response has no base_token/app_token")
	}
	// A bot-created Base is usable by the bot immediately. Best-effort grant
	// keeps the same Base visible to the currently logged-in user without
	// turning that convenience into a second initialization workflow.
	_ = common.AutoGrantCurrentUserDrivePermission(r, token, "bitable")
	// Persist in the active profile when the base was auto-created.  Failure to
	// persist must not claim the remote operation failed; the response still
	// contains the usable token for a caller to save explicitly.
	if r.Config != nil {
		if cfg, loadErr := core.LoadMultiAppConfig(); loadErr == nil {
			if app := cfg.FindApp(r.Config.ProfileName); app != nil {
				if app.Workline == nil {
					app.Workline = &core.WorklineProfile{}
				}
				app.Workline.BaseToken = token
				_ = core.SaveMultiAppConfig(cfg)
			}
		}
	}
	return token, nil
}
func stringValue(v any) string {
	s, _ := v.(string)
	if s != "" {
		return s
	}
	if m, ok := v.(map[string]any); ok {
		if s = stringValue(m["id"]); s != "" {
			return s
		}
		if s = stringValue(m["record_id"]); s != "" {
			return s
		}
		for _, key := range []string{"person_id", "evidence_id", "event_id", "style_id", "identity_id", "role_claim_id"} {
			if s = stringValue(m[key]); s != "" {
				return s
			}
		}
		return stringValue(m["value"])
	}
	if values, ok := v.([]any); ok && len(values) > 0 {
		return stringValue(values[0])
	}
	if values, ok := v.([]string); ok && len(values) > 0 {
		return values[0]
	}
	return ""
}

func projectFields(table string, in map[string]any) map[string]any {
	allowed := map[string]bool{}
	for _, field := range Tables[table] {
		if !isSystemField(field) {
			allowed[field] = true
		}
	}
	out := map[string]any{}
	for key, value := range in {
		if allowed[key] {
			out[key] = value
		}
	}
	return out
}

func mergePersonValue(value any) string {
	if s := stringValue(value); s != "" {
		return s
	}
	if m, ok := value.(map[string]any); ok {
		return stringValue(m["person_id"])
	}
	return ""
}
func newID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.New().String()
}

func stableID(seed string) string {
	return uuid.NewMD5(uuid.Nil, []byte("workline:"+seed)).String()
}
func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func evidenceSourceKey(fields map[string]any) string {
	return hashBytes([]byte(strings.Join([]string{stringValue(fields["wechat_owner_id"]), stringValue(fields["conversation_id"]), stringValue(fields["message_id"]), stringValue(fields["forward_path"])}, "\x1f")))
}

type tableInfo struct {
	ID     string
	Name   string
	Fields map[string]bool
}

func listTables(r *common.RuntimeContext, base string) (map[string]tableInfo, error) {
	out := map[string]tableInfo{}
	for offset := 0; ; {
		d, err := basecmd.WorklineBaseV3Call(r, "GET", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables", map[string][]string{"offset": {strconv.Itoa(offset)}, "limit": {"100"}}, nil)
		if err != nil {
			return nil, err
		}
		vals, _ := d["items"].([]any)
		if vals == nil {
			vals, _ = d["tables"].([]any)
		}
		for _, v := range vals {
			m, _ := v.(map[string]any)
			id := stringValue(m["table_id"])
			if id == "" {
				id = stringValue(m["id"])
			}
			name := stringValue(m["name"])
			if id != "" {
				fields, fieldErr := listFields(r, base, id)
				if fieldErr != nil {
					return nil, fieldErr
				}
				out[name] = tableInfo{ID: id, Name: name, Fields: fields}
				knownTableIDs[name] = id
			}
		}
		more, _ := d["has_more"].(bool)
		if !more || len(vals) == 0 {
			break
		}
		offset += len(vals)
	}
	return out, nil
}

func listFields(r *common.RuntimeContext, base, table string) (map[string]bool, error) {
	fields := map[string]bool{}
	for offset := 0; ; {
		d, err := basecmd.WorklineBaseV3Call(r, "GET", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables/"+url.PathEscape(table)+"/fields", map[string][]string{"offset": {strconv.Itoa(offset)}, "limit": {"200"}}, nil)
		if err != nil {
			return nil, err
		}
		values, _ := d["items"].([]any)
		if values == nil {
			values, _ = d["fields"].([]any)
		}
		for _, value := range values {
			if field, ok := value.(map[string]any); ok {
				fields[stringValue(field["name"])] = true
				fields[stringValue(field["field_name"])] = true
			}
		}
		more, _ := d["has_more"].(bool)
		if !more || len(values) == 0 {
			break
		}
		offset += len(values)
	}
	return fields, nil
}
func ensureTables(r *common.RuntimeContext, base string, write bool) (map[string]tableInfo, error) {
	t, err := listTables(r, base)
	if err != nil {
		return nil, err
	}
	if !write {
		return t, nil
	}
	for _, name := range tableOrder {
		if _, ok := t[name]; ok {
			continue
		}
		// Create every table with its primary text field first. Link fields
		// require the target table ID, so they are added in the second phase
		// after all table IDs have been resolved.
		primary := Tables[name][0]
		fields := []map[string]any{{"name": primary, "type": "text"}}
		d, e := basecmd.WorklineBaseV3Call(r, "POST", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables", nil, map[string]any{"name": name, "fields": fields})
		if e != nil {
			return nil, e
		}
		id := stringValue(d["table_id"])
		if id == "" {
			id = stringValue(d["id"])
		}
		if id == "" {
			return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "table %q create response has no table_id", name)
		}
		t[name] = tableInfo{ID: id, Name: name, Fields: map[string]bool{primary: true}}
		knownTableIDs[name] = id
	}
	// The frozen schema is additive.  If the list response exposes existing
	// fields, add only missing columns; no destructive or generic migration is
	// attempted.
	for _, name := range tableOrder {
		ti := t[name]
		for _, field := range Tables[name] {
			if ti.Fields[field] {
				continue
			}
			body := schemaFieldBody(name, field, t)
			if options := schemaOptions(field); len(options) > 0 {
				body["options"] = options
			}
			if field == "functions" {
				body["multiple"] = true
			}
			// Feishu Base applies schema writes asynchronously. In particular, a
			// native Link field can be rejected with a misleading "link_table"
			// validation error when it immediately follows another field write.
			// The Base shortcut documents a 0.5-1s write-conflict window; wait at
			// its lower bound before Link writes while keeping ordinary additive
			// fields fast.
			if schemaType(name, field) == "link" {
				time.Sleep(500 * time.Millisecond)
			}
			_, err := basecmd.WorklineBaseV3Call(r, "POST", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables/"+url.PathEscape(ti.ID)+"/fields", nil, body)
			if err != nil {
				return nil, fmt.Errorf("create Workline field %s.%s with body %s: %w", name, field, mustJSON(body), err)
			}
			ti.Fields[field] = true
		}
		t[name] = ti
	}
	return t, nil
}

func schemaFieldBody(table, field string, tables map[string]tableInfo) map[string]any {
	body := map[string]any{"name": field, "type": schemaType(table, field)}
	if target, multiple := relationTarget(table, field); target != "" {
		if ti, ok := tables[target]; ok {
			body["link_table"] = ti.ID
		}
		_ = multiple // Base link cells are arrays; Field schema has no multiple property.
	}
	if field == "functions" {
		body["multiple"] = true
	}
	return body
}

func ensureMeta(r *common.RuntimeContext, base string, tables map[string]tableInfo) error {
	ti, ok := tables["_Meta"]
	if !ok {
		return errs.NewValidationError(errs.SubtypeNotFound, "Workline _Meta table is missing from Base schema")
	}
	rows, err := listRecords(r, base, ti.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		seen[stringValue(f["key"])] = true
	}
	enterpriseKey := strings.TrimSpace(os.Getenv("WORKLINE_ENTERPRISE_KEY"))
	if enterpriseKey == "" {
		enterpriseKey = base
	}
	for key, value := range map[string]string{"interface_version": InterfaceVersion, "schema_version": SchemaVersion, "enterprise_key": enterpriseKey} {
		if seen[key] {
			continue
		}
		if _, err := createRecord(r, base, ti.ID, map[string]any{"key": key, "value": value, "interface_version": InterfaceVersion, "schema_version": SchemaVersion, "enterprise_key": enterpriseKey}); err != nil {
			return err
		}
	}
	return nil
}

func listRecords(r *common.RuntimeContext, base, table string) ([]map[string]any, error) {
	return listRecordsWithDecode(r, base, table, true)
}

func listRecordsRaw(r *common.RuntimeContext, base, table string) ([]map[string]any, error) {
	return listRecordsWithDecode(r, base, table, false)
}

func listRecordsWithDecode(r *common.RuntimeContext, base, table string, decode bool) ([]map[string]any, error) {
	if strings.TrimSpace(table) == "" {
		return nil, errs.NewValidationError(errs.SubtypeNotFound, "Workline table is missing from Base schema")
	}
	if state, ok := applyStates.Load(r); ok {
		cached := state.(*applyReadState)
		cached.mu.Lock()
		rows, hit := cached.loaded[table]
		decoded := cached.decoded[table]
		cached.mu.Unlock()
		if hit {
			if decode && !decoded {
				decodeLinkedFields(r, base, table, rows)
				cached.mu.Lock()
				if cached.decoded == nil {
					cached.decoded = map[string]bool{}
				}
				cached.decoded[table] = true
				cached.mu.Unlock()
			}
			return rows, nil
		}
	}
	out := make([]map[string]any, 0)
	for offset := 0; ; {
		d, err := basecmd.WorklineBaseV3Call(r, "GET", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables/"+url.PathEscape(table)+"/records", map[string][]string{"offset": {strconv.Itoa(offset)}, "limit": {"200"}}, nil)
		if err != nil {
			return nil, err
		}
		raw, _ := d["items"].([]any)
		page := make([]map[string]any, 0, len(raw))
		for _, v := range raw {
			if m, ok := v.(map[string]any); ok {
				page = append(page, m)
			}
		}
		if len(page) == 0 {
			page = matrixRecordRows(d)
		}
		out = append(out, page...)
		more, _ := d["has_more"].(bool)
		if !more || len(page) == 0 {
			break
		}
		offset += len(page)
	}
	if decode {
		decodeLinkedFields(r, base, table, out)
	}
	if state, ok := applyStates.Load(r); ok {
		cached := state.(*applyReadState)
		cached.mu.Lock()
		// A concurrent first read may have won the race; either snapshot is
		// equivalent, so keep the first one and avoid replacing fresh writes.
		if _, exists := cached.loaded[table]; !exists {
			if cached.decoded == nil {
				cached.decoded = map[string]bool{}
			}
			cached.loaded[table] = out
			cached.decoded[table] = decode
		} else {
			out = cached.loaded[table]
		}
		cached.mu.Unlock()
	}
	return out, nil
}

// Base v3 record reads use a compact matrix response: fields describes the
// columns, data contains rows, and record_id_list is parallel to data. Convert
// that transport shape into the object rows used by Workline's deterministic
// query, dedupe, and relationship logic.
func matrixRecordRows(data map[string]any) []map[string]any {
	fields := stringValues(data["fields"])
	recordIDs := stringValues(data["record_id_list"])
	rawRows, _ := data["data"].([]any)
	rows := make([]map[string]any, 0, len(rawRows))
	for index, raw := range rawRows {
		values, _ := raw.([]any)
		fieldMap := make(map[string]any, len(fields))
		for column, field := range fields {
			if column < len(values) {
				fieldMap[field] = values[column]
			}
		}
		row := map[string]any{"fields": fieldMap}
		if index < len(recordIDs) {
			row["record_id"] = recordIDs[index]
		}
		rows = append(rows, row)
	}
	return rows
}

func decodeLinkedFields(r *common.RuntimeContext, base, table string, rows []map[string]any) {
	name := ""
	for candidate, id := range knownTableIDs {
		if id == table {
			name = candidate
			break
		}
	}
	if name == "" {
		return
	}
	for _, field := range Tables[name] {
		target, multiple := relationTarget(name, field)
		if target == "" {
			continue
		}
		targetID := knownTableIDs[target]
		if targetID == "" {
			continue
		}
		targetRows, err := listRecordsRaw(r, base, targetID)
		if err != nil {
			continue
		}
		byRecord := map[string]string{}
		key := Tables[target][0]
		for _, targetRow := range targetRows {
			if fields, ok := targetRow["fields"].(map[string]any); ok {
				byRecord[rowRecordID(targetRow)] = stringValue(fields[key])
			}
		}
		for _, row := range rows {
			fields, _ := row["fields"].(map[string]any)
			value, exists := fields[field]
			if !exists {
				continue
			}
			decoded := decodeLinkValue(value, byRecord)
			if !multiple {
				if list, ok := decoded.([]any); ok && len(list) > 0 {
					decoded = list[0]
				}
			}
			fields[field] = decoded
		}
	}
}

func decodeLinkValue(value any, byRecord map[string]string) any {
	if list, ok := value.([]any); ok {
		out := make([]any, 0, len(list))
		for _, item := range list {
			id := stringValue(item)
			if business := byRecord[id]; business != "" {
				out = append(out, business)
			} else {
				out = append(out, item)
			}
		}
		return out
	}
	if list, ok := value.([]string); ok {
		out := make([]any, 0, len(list))
		for _, item := range list {
			if business := byRecord[item]; business != "" {
				out = append(out, business)
			} else {
				out = append(out, item)
			}
		}
		return out
	}
	if id := stringValue(value); byRecord[id] != "" {
		return byRecord[id]
	}
	return value
}
func createRecord(r *common.RuntimeContext, base, table string, fields map[string]any) (map[string]any, error) {
	prepared, err := prepareFields(r, base, table, fields)
	if err != nil {
		return nil, err
	}
	created, err := basecmd.WorklineBaseV3Call(r, "POST", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables/"+url.PathEscape(table)+"/records", nil, prepared)
	if err != nil {
		return nil, err
	}
	// The live Base v3 create endpoint returns record_id_list even for one
	// record. Normalize it so operation recovery and relationship writes have
	// the durable record ID immediately, without waiting for list consistency.
	if stringValue(created["record_id"]) == "" {
		if ids := stringValues(created["record_id_list"]); len(ids) > 0 {
			created["record_id"] = ids[0]
		}
	}
	cacheCreatedRecord(r, table, fields, created)
	return created, nil
}
func updateRecord(r *common.RuntimeContext, base, table, rec string, fields map[string]any) (map[string]any, error) {
	prepared, err := prepareFields(r, base, table, fields)
	if err != nil {
		return nil, err
	}
	updated, callErr := basecmd.WorklineBaseV3Call(r, "PATCH", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables/"+url.PathEscape(table)+"/records/"+url.PathEscape(rec), nil, prepared)
	if callErr == nil {
		cacheUpdatedRecord(r, table, rec, fields)
	}
	return updated, callErr
}

func cacheCreatedRecord(r *common.RuntimeContext, table string, fields map[string]any, created map[string]interface{}) {
	stateValue, ok := applyStates.Load(r)
	if !ok {
		return
	}
	recordID := stringValue(created["record_id"])
	if recordID == "" {
		recordID = stringValue(created["id"])
	}
	if recordID == "" {
		return
	}
	rowFields := map[string]any{}
	for key, value := range fields {
		rowFields[key] = value
	}
	state := stateValue.(*applyReadState)
	state.mu.Lock()
	if rows, exists := state.loaded[table]; exists {
		state.loaded[table] = append(rows, map[string]any{"record_id": recordID, "fields": rowFields})
	}
	state.mu.Unlock()
}

func cacheUpdatedRecord(r *common.RuntimeContext, table, recordID string, fields map[string]any) {
	stateValue, ok := applyStates.Load(r)
	if !ok {
		return
	}
	state := stateValue.(*applyReadState)
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, row := range state.loaded[table] {
		if rowRecordID(row) != recordID {
			continue
		}
		rowFields, _ := row["fields"].(map[string]any)
		if rowFields == nil {
			rowFields = map[string]any{}
			row["fields"] = rowFields
		}
		for key, value := range fields {
			rowFields[key] = value
		}
		return
	}
}

// Encode structured values consistently before resolving native Link fields.
func coerceFields(table string, fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		if schemaType(table, key) == "select" {
			switch typed := value.(type) {
			case []any, []string:
				out[key] = typed
			default:
				if text := stringValue(value); text != "" {
					out[key] = []string{text}
				} else {
					out[key] = value
				}
			}
			continue
		}
		if schemaType(table, key) == "text" {
			switch value.(type) {
			case []any, []string, map[string]any:
				out[key] = mustJSON(value)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func prepareFields(r *common.RuntimeContext, base, table string, fields map[string]any) (map[string]any, error) {
	out := coerceFields(table, fields)
	for field, value := range fields {
		// relationTarget is the schema owner for native Base links. Using it here
		// keeps write conversion aligned with field creation; a second field-only
		// map previously omitted Events.actor_identities and wrote JSON text instead.
		target, multiple := relationTargetForTableID(table, field)
		if target == "" || schemaTypeForTableID(table, field) != "link" {
			continue
		}
		targetID, err := findTableID(r, base, target)
		if err != nil {
			return nil, err
		}
		refs, err := linkReferences(r, base, targetID, target, value)
		if err != nil {
			return nil, err
		}
		if !multiple && len(refs) > 1 {
			refs = refs[:1]
		}
		out[field] = refs
	}
	return out, nil
}

func relationTargetForTableID(tableID, field string) (string, bool) {
	for name, id := range knownTableIDs {
		if id == tableID {
			return relationTarget(name, field)
		}
	}

	// Some focused callers/tests prepare a table before the process-wide table
	// registry is populated. Fall back only when every schema occurrence of the
	// field agrees on the same relationship, avoiding an ambiguous conversion.
	target, multiple := "", false
	for name := range Tables {
		candidate, candidateMultiple := relationTarget(name, field)
		if candidate == "" {
			continue
		}
		if target != "" && (target != candidate || multiple != candidateMultiple) {
			return "", false
		}
		target, multiple = candidate, candidateMultiple
	}
	return target, multiple
}

// Record helpers receive table IDs, while the frozen schema is keyed by name.
// Field names are unique enough for link fields; this guard keeps text fields
// such as Styles.created_from_event_id from being treated as links.
func schemaTypeForTableID(tableID, field string) string {
	for name, id := range knownTableIDs {
		if id == tableID {
			return schemaType(name, field)
		}
	}
	// Fallback for tests and callers that did not populate knownTableIDs yet.
	for name := range Tables {
		if target, _ := relationTarget(name, field); target != "" {
			return "link"
		}
	}
	return "text"
}

func findTableID(r *common.RuntimeContext, base, name string) (string, error) {
	if id := strings.TrimSpace(knownTableIDs[name]); id != "" {
		return id, nil
	}
	d, err := basecmd.WorklineBaseV3Call(r, "GET", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables", map[string][]string{"offset": {"0"}, "limit": {"100"}}, nil)
	if err != nil {
		return "", err
	}
	items, _ := d["items"].([]any)
	if items == nil {
		items, _ = d["tables"].([]any)
	}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if stringValue(item["name"]) == name {
			id := stringValue(item["table_id"])
			if id == "" {
				id = stringValue(item["id"])
			}
			if id != "" {
				knownTableIDs[name] = id
				return id, nil
			}
		}
	}
	return "", errs.NewValidationError(errs.SubtypeNotFound, "Workline linked table %q is missing", name)
}

func linkReferences(r *common.RuntimeContext, base, tableID, target string, value any) ([]map[string]any, error) {
	values := []any{value}
	if list, ok := value.([]any); ok {
		values = list
	} else if list, ok := value.([]string); ok {
		values = make([]any, len(list))
		for i := range list {
			values[i] = list[i]
		}
	} else if encoded, ok := value.(string); ok && strings.HasPrefix(strings.TrimSpace(encoded), "[") {
		_ = json.Unmarshal([]byte(encoded), &values)
	}
	rows, err := listRecords(r, base, tableID)
	if err != nil {
		return nil, err
	}
	key := Tables[target][0]
	refs := make([]map[string]any, 0, len(values))
	for _, value := range values {
		id := stringValue(value)
		if id == "" {
			continue
		}
		if strings.HasPrefix(id, "rec_") {
			refs = append(refs, map[string]any{"id": id})
			continue
		}
		found := false
		for _, row := range rows {
			fields, _ := row["fields"].(map[string]any)
			if stringValue(fields[key]) == id {
				if recordID := rowRecordID(row); recordID != "" {
					refs = append(refs, map[string]any{"id": recordID})
				}
				found = true
				break
			}
		}
		if !found {
			// The legacy process-wide map is useful outside +apply, but the
			// current Base snapshot must win: the same Workline business ID may
			// legitimately exist in two Bases or two test runtimes.
			if recordID := knownRecordIDs[target+"\x1f"+id]; recordID != "" {
				refs = append(refs, map[string]any{"id": recordID})
				continue
			}
		}
		if !found {
			return nil, errs.NewValidationError(errs.SubtypeNotFound, "linked %s record %q was not found", target, id)
		}
	}
	return refs, nil
}

func recordsForQuery(r *common.RuntimeContext, req map[string]any, tables map[string]tableInfo, base string) (map[string]any, error) {
	q := stringValue(req["query"])
	names := []string{"Evidence", "EvidenceEventLinks", "Events"}
	switch q {
	case "event":
		names = []string{"Events", "EvidenceEventLinks", "Evidence", "EventStyleLinks", "Styles", "StyleIdentifiers", "EventRelations", "People", "SourceIdentities", "RoleClaims"}
	case "style":
		names = []string{"Styles", "StyleIdentifiers", "EventStyleLinks", "Events", "EvidenceEventLinks", "Evidence", "EventRelations"}
	case "person":
		names = []string{"People", "SourceIdentities", "RoleClaims"}
	case "context":
		names = tableOrder[:len(tableOrder)-1]
	case "operation":
		names = []string{"Operations"}
	}
	result := map[string]any{"query": q}
	for _, n := range names {
		ti, ok := tables[n]
		if !ok {
			continue
		}
		rows, err := listRecords(r, base, ti.ID)
		if err != nil {
			return nil, err
		}
		if (q == "event" || q == "style" || q == "context") && n == "Events" {
			if filters, ok := req["filters"].(map[string]any); ok {
				if id := stringValue(filters["event_id"]); id != "" {
					filters["event_id"] = canonicalID(rows, "event_id", "canonical_event_id", id)
				}
			}
			if q == "event" {
				rows = canonicalRows(rows, "event_id", "canonical_event_id")
			}
		}
		if (q == "event" || q == "style" || q == "context") && n == "Styles" {
			if filters, ok := req["filters"].(map[string]any); ok {
				if id := stringValue(filters["style_id"]); id != "" {
					filters["style_id"] = canonicalID(rows, "style_id", "canonical_style_id", id)
				}
			}
			if q == "style" {
				rows = canonicalRows(rows, "style_id", "canonical_style_id")
			}
		}
		if shouldDirectFilter(q, n) {
			rows = filterRowsForTable(rows, req, n)
		}
		result[strings.ToLower(n)] = rows
	}
	// Apply relationship predicates to the focused table first, then derive
	// associated Events/Styles/Evidence/Relations from that final selection.
	// This keeps a filtered response from retaining associations belonging to
	// rows that the relationship filter removed.
	applyRelationshipFilters(result, q, req)
	attachQueryRelations(result, q)
	filterReturnedRelations(result)
	return result, nil
}

func filterReturnedRelations(result map[string]any) {
	rows := func(key string) []map[string]any { value, _ := result[key].([]map[string]any); return value }
	evidenceIDs := map[string]bool{}
	for _, row := range rows("evidence") {
		f, _ := row["fields"].(map[string]any)
		evidenceIDs[stringValue(f["evidence_id"])] = true
	}
	eventIDs := map[string]bool{}
	for _, row := range rows("events") {
		f, _ := row["fields"].(map[string]any)
		eventIDs[stringValue(f["event_id"])] = true
	}
	styleIDs := map[string]bool{}
	for _, row := range rows("styles") {
		f, _ := row["fields"].(map[string]any)
		styleIDs[stringValue(f["style_id"])] = true
	}
	if _, exists := result["evidenceeventlinks"]; exists {
		kept := []map[string]any{}
		for _, row := range rows("evidenceeventlinks") {
			f, _ := row["fields"].(map[string]any)
			if evidenceIDs[stringValue(f["evidence"])] && eventIDs[stringValue(f["event"])] && stringValue(f["record_state"]) != "removed" {
				kept = append(kept, row)
			}
		}
		result["evidenceeventlinks"] = kept
	}
	if _, exists := result["eventstylelinks"]; exists {
		kept := []map[string]any{}
		for _, row := range rows("eventstylelinks") {
			f, _ := row["fields"].(map[string]any)
			if eventIDs[stringValue(f["event"])] && styleIDs[stringValue(f["style"])] && stringValue(f["link_status"]) != "removed" && stringValue(f["link_status"]) != "rejected" {
				kept = append(kept, row)
			}
		}
		result["eventstylelinks"] = kept
	}
	if _, exists := result["styleidentifiers"]; exists {
		kept := []map[string]any{}
		for _, row := range rows("styleidentifiers") {
			f, _ := row["fields"].(map[string]any)
			if styleIDs[stringValue(f["style"])] {
				kept = append(kept, row)
			}
		}
		result["styleidentifiers"] = kept
	}
	if _, exists := result["eventrelations"]; exists {
		kept := []map[string]any{}
		for _, row := range rows("eventrelations") {
			f, _ := row["fields"].(map[string]any)
			if stringValue(f["relation_status"]) != "rejected" && (eventIDs[stringValue(f["from_event"])] || eventIDs[stringValue(f["to_event"])]) {
				kept = append(kept, row)
			}
		}
		result["eventrelations"] = kept
	}
}

func shouldDirectFilter(query, table string) bool {
	switch query {
	case "evidence":
		return table == "Evidence"
	case "event":
		return table == "Events"
	case "style":
		return table == "Styles" || table == "StyleIdentifiers"
	case "person":
		return table == "People" || table == "SourceIdentities" || table == "RoleClaims"
	case "context":
		return table == "Evidence"
	case "operation":
		return table == "Operations"
	default:
		return false
	}
}

func filterRowsForTable(rows []map[string]any, req map[string]any, table string) []map[string]any {
	filters, _ := req["filters"].(map[string]any)
	selected := map[string]any{}
	for key, value := range filters {
		if key == "from" || key == "to" {
			selected[key] = value
			continue
		}
		for _, row := range rows {
			fields, _ := row["fields"].(map[string]any)
			if _, exists := fields[key]; exists {
				selected[key] = value
				break
			}
		}
	}
	copyReq := map[string]any{"filters": selected, "table": table}
	return filterRows(rows, copyReq)
}

func canonicalRows(rows []map[string]any, idField, canonicalField string) []map[string]any {
	byID := map[string]map[string]any{}
	for _, row := range rows {
		fields, _ := row["fields"].(map[string]any)
		id := stringValue(fields[idField])
		if id != "" {
			byID[id] = row
		}
	}
	out := make([]map[string]any, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		fields, _ := row["fields"].(map[string]any)
		id := stringValue(fields[idField])
		canonical := canonicalID(rows, idField, canonicalField, id)
		if canonical != id || seen[id] || stringValue(fields["record_state"]) == "merged" || stringValue(fields["record_state"]) == "excluded" {
			continue
		}
		if _, ok := byID[canonical]; !ok {
			continue
		}
		seen[id] = true
		out = append(out, row)
	}
	return out
}
func filterRows(rows []map[string]any, req map[string]any) []map[string]any {
	f, _ := req["filters"].(map[string]any)
	if len(f) == 0 {
		return rows
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		fields, _ := row["fields"].(map[string]any)
		match := true
		for k, want := range f {
			if k == "from" || k == "to" {
				continue
			}
			if !fieldMatchesNamed(k, fields[k], want) {
				match = false
				break
			}
		}
		for _, bound := range []string{"from", "to"} {
			if want, ok := f[bound].(string); ok {
				field := stringValue(fields["source_time"])
				if field == "" {
					field = stringValue(fields["occurred_at"])
				}
				if bound == "from" && field != "" && field < want {
					match = false
				}
				if bound == "to" && field != "" && field > want {
					match = false
				}
			}
		}
		if match {
			out = append(out, row)
		}
	}
	return out
}

func fieldMatches(value, want any) bool {
	if fmt.Sprint(value) == fmt.Sprint(want) {
		return true
	}
	for _, item := range stringValues(value) {
		if item == stringValue(want) {
			return true
		}
	}
	return false
}

func fieldMatchesNamed(field string, value, want any) bool {
	if fieldMatches(value, want) {
		return true
	}
	if field == "name" || field == "display_name" || field == "aliases" {
		return strings.Contains(strings.ToLower(fmt.Sprint(value)), strings.ToLower(strings.TrimSpace(fmt.Sprint(want))))
	}
	return false
}

func attachQueryRelations(result map[string]any, query string) {
	rows := func(key string) []map[string]any { v, _ := result[key].([]map[string]any); return v }
	if query == "evidence" || query == "context" {
		eventRows := rows("events")
		styleRows := rows("styles")
		wanted := map[string]bool{}
		for _, row := range rows("evidence") {
			f, _ := row["fields"].(map[string]any)
			wanted[stringValue(f["evidence_id"])] = true
		}
		eventIDs := map[string]bool{}
		for _, row := range rows("evidenceeventlinks") {
			f, _ := row["fields"].(map[string]any)
			if wanted[stringValue(f["evidence"])] && stringValue(f["record_state"]) != "removed" {
				eventIDs[canonicalID(eventRows, "event_id", "canonical_event_id", stringValue(f["event"]))] = true
			}
		}
		filtered := []map[string]any{}
		for _, row := range eventRows {
			f, _ := row["fields"].(map[string]any)
			id := canonicalID(eventRows, "event_id", "canonical_event_id", stringValue(f["event_id"]))
			if eventIDs[id] && stringValue(f["record_state"]) == "active" {
				filtered = append(filtered, row)
			}
		}
		result["events"] = filtered
		if query == "context" {
			styleIDs := map[string]bool{}
			for _, row := range rows("eventstylelinks") {
				f, _ := row["fields"].(map[string]any)
				linkedEvent := canonicalID(eventRows, "event_id", "canonical_event_id", stringValue(f["event"]))
				if eventIDs[linkedEvent] && stringValue(f["link_status"]) != "removed" && stringValue(f["link_status"]) != "rejected" {
					styleIDs[canonicalID(styleRows, "style_id", "canonical_style_id", stringValue(f["style"]))] = true
				}
			}
			styles := []map[string]any{}
			for _, row := range rows("styles") {
				f, _ := row["fields"].(map[string]any)
				styleID := stringValue(f["style_id"])
				if styleIDs[styleID] && canonicalID(styleRows, "style_id", "canonical_style_id", styleID) == styleID && stringValue(f["record_state"]) == "active" {
					styles = append(styles, row)
				}
			}
			result["styles"] = styles
			// Context responses should explain the people involved in the
			// selected evidence/events, rather than returning the entire People
			// table. Native link values have already been decoded to business IDs.
			personIDs := map[string]bool{}
			identityIDs := map[string]bool{}
			for _, row := range rows("evidence") {
				f, _ := row["fields"].(map[string]any)
				if identityID := stringValue(f["speaker_identity"]); identityID != "" {
					identityIDs[identityID] = true
				}
			}
			for _, row := range filtered {
				f, _ := row["fields"].(map[string]any)
				for _, actorID := range stringValues(f["actors"]) {
					if actorID != "" {
						personIDs[actorID] = true
					}
				}
				for _, identityID := range stringValues(f["actor_identities"]) {
					if identityID != "" {
						identityIDs[identityID] = true
					}
				}
			}
			for _, row := range rows("sourceidentities") {
				f, _ := row["fields"].(map[string]any)
				if identityIDs[stringValue(f["identity_id"])] {
					if personID := stringValue(f["person"]); personID != "" {
						personIDs[personID] = true
					}
				}
			}
			people := []map[string]any{}
			for _, row := range rows("people") {
				f, _ := row["fields"].(map[string]any)
				if personIDs[stringValue(f["person_id"])] {
					people = append(people, row)
				}
			}
			identities := []map[string]any{}
			for _, row := range rows("sourceidentities") {
				f, _ := row["fields"].(map[string]any)
				if personIDs[stringValue(f["person"])] || identityIDs[stringValue(f["identity_id"])] {
					identities = append(identities, row)
				}
			}
			claims := []map[string]any{}
			for _, row := range rows("roleclaims") {
				f, _ := row["fields"].(map[string]any)
				if personIDs[stringValue(f["person"])] || identityIDs[stringValue(f["source_identity"])] {
					claims = append(claims, row)
				}
			}
			result["people"], result["sourceidentities"], result["roleclaims"] = people, identities, claims
		}
	}
	if query == "event" {
		eventRows := rows("events")
		styleRows := rows("styles")
		eventIDs := map[string]bool{}
		for _, row := range eventRows {
			f, _ := row["fields"].(map[string]any)
			if stringValue(f["record_state"]) == "active" {
				eventIDs[stringValue(f["event_id"])] = true
			}
		}
		styleIDs := map[string]bool{}
		for _, row := range rows("eventstylelinks") {
			f, _ := row["fields"].(map[string]any)
			linkedEvent := canonicalID(eventRows, "event_id", "canonical_event_id", stringValue(f["event"]))
			if eventIDs[linkedEvent] && stringValue(f["link_status"]) != "removed" && stringValue(f["link_status"]) != "rejected" {
				styleIDs[canonicalID(styleRows, "style_id", "canonical_style_id", stringValue(f["style"]))] = true
			}
		}
		styles := []map[string]any{}
		for _, row := range rows("styles") {
			f, _ := row["fields"].(map[string]any)
			styleID := stringValue(f["style_id"])
			if styleIDs[styleID] && canonicalID(styleRows, "style_id", "canonical_style_id", styleID) == styleID && stringValue(f["record_state"]) == "active" {
				styles = append(styles, row)
			}
		}
		result["styles"] = styles
		// Return only evidence supporting the selected Events, and only
		// non-rejected relations touching them.
		evidenceIDs := map[string]bool{}
		for _, row := range rows("evidenceeventlinks") {
			f, _ := row["fields"].(map[string]any)
			linkedEvent := canonicalID(eventRows, "event_id", "canonical_event_id", stringValue(f["event"]))
			if eventIDs[linkedEvent] && stringValue(f["record_state"]) != "removed" {
				evidenceIDs[stringValue(f["evidence"])] = true
			}
		}
		evidenceRows := []map[string]any{}
		for _, row := range rows("evidence") {
			f, _ := row["fields"].(map[string]any)
			if evidenceIDs[stringValue(f["evidence_id"])] {
				evidenceRows = append(evidenceRows, row)
			}
		}
		result["evidence"] = evidenceRows
		personIDs := map[string]bool{}
		identityIDs := map[string]bool{}
		for _, row := range eventRows {
			f, _ := row["fields"].(map[string]any)
			if !eventIDs[stringValue(f["event_id"])] {
				continue
			}
			for _, id := range stringValues(f["actors"]) {
				personIDs[id] = true
			}
			for _, id := range stringValues(f["actor_identities"]) {
				identityIDs[id] = true
			}
		}
		for _, row := range evidenceRows {
			f, _ := row["fields"].(map[string]any)
			if id := stringValue(f["speaker_identity"]); id != "" {
				identityIDs[id] = true
			}
		}
		for _, row := range rows("sourceidentities") {
			f, _ := row["fields"].(map[string]any)
			if identityIDs[stringValue(f["identity_id"])] {
				if id := stringValue(f["person"]); id != "" {
					personIDs[id] = true
				}
			}
		}
		result["people"] = keepRowsByID(rows("people"), "person_id", personIDs)
		result["sourceidentities"] = keepIdentities(rows("sourceidentities"), personIDs, identityIDs)
		result["roleclaims"] = keepRoleClaims(rows("roleclaims"), personIDs, identityIDs)
		relations := []map[string]any{}
		for _, row := range rows("eventrelations") {
			f, _ := row["fields"].(map[string]any)
			if stringValue(f["relation_status"]) != "rejected" && (eventIDs[stringValue(f["from_event"])] || eventIDs[stringValue(f["to_event"])]) {
				relations = append(relations, row)
			}
		}
		result["eventrelations"] = relations
	}
	if query == "style" {
		eventRows := rows("events")
		styleIDs := map[string]bool{}
		for _, row := range rows("styles") {
			f, _ := row["fields"].(map[string]any)
			styleIDs[stringValue(f["style_id"])] = true
		}
		eventIDs := map[string]bool{}
		for _, row := range rows("eventstylelinks") {
			f, _ := row["fields"].(map[string]any)
			if styleIDs[stringValue(f["style"])] && stringValue(f["link_status"]) != "removed" && stringValue(f["link_status"]) != "rejected" {
				eventIDs[canonicalID(eventRows, "event_id", "canonical_event_id", stringValue(f["event"]))] = true
			}
		}
		events := []map[string]any{}
		for _, row := range eventRows {
			f, _ := row["fields"].(map[string]any)
			id := canonicalID(eventRows, "event_id", "canonical_event_id", stringValue(f["event_id"]))
			if eventIDs[id] && stringValue(f["record_state"]) == "active" {
				events = append(events, row)
			}
		}
		result["events"] = events
		evidenceIDs := map[string]bool{}
		for _, row := range rows("evidenceeventlinks") {
			f, _ := row["fields"].(map[string]any)
			linkedEvent := canonicalID(eventRows, "event_id", "canonical_event_id", stringValue(f["event"]))
			if eventIDs[linkedEvent] && stringValue(f["record_state"]) != "removed" {
				evidenceIDs[stringValue(f["evidence"])] = true
			}
		}
		evidence := []map[string]any{}
		for _, row := range rows("evidence") {
			f, _ := row["fields"].(map[string]any)
			if evidenceIDs[stringValue(f["evidence_id"])] {
				evidence = append(evidence, row)
			}
		}
		result["evidence"] = evidence
	}
}

func applyRelationshipFilters(result map[string]any, query string, req map[string]any) {
	filters, _ := req["filters"].(map[string]any)
	if len(filters) == 0 {
		return
	}
	rows := func(key string) []map[string]any { value, _ := result[key].([]map[string]any); return value }
	if query == "evidence" {
		if wanted, ok := filters["event_id"]; ok {
			eventID := canonicalID(rows("events"), "event_id", "canonical_event_id", stringValue(wanted))
			allowed := map[string]bool{}
			for _, row := range rows("evidenceeventlinks") {
				f, _ := row["fields"].(map[string]any)
				linkedEvent := canonicalID(rows("events"), "event_id", "canonical_event_id", stringValue(f["event"]))
				if linkedEvent == eventID && stringValue(f["record_state"]) != "removed" {
					allowed[stringValue(f["evidence"])] = true
				}
			}
			kept := []map[string]any{}
			for _, row := range rows("evidence") {
				f, _ := row["fields"].(map[string]any)
				if allowed[stringValue(f["evidence_id"])] {
					kept = append(kept, row)
				}
			}
			result["evidence"] = kept
		}
	}
	if query == "event" {
		sets := []map[string]bool{}
		if wanted, ok := filters["style_id"]; ok {
			wantedStyle := canonicalID(rows("styles"), "style_id", "canonical_style_id", stringValue(wanted))
			allowed := map[string]bool{}
			for _, row := range rows("eventstylelinks") {
				f, _ := row["fields"].(map[string]any)
				linkedStyle := canonicalID(rows("styles"), "style_id", "canonical_style_id", stringValue(f["style"]))
				if linkedStyle == wantedStyle && stringValue(f["link_status"]) != "removed" && stringValue(f["link_status"]) != "rejected" {
					allowed[canonicalID(rows("events"), "event_id", "canonical_event_id", stringValue(f["event"]))] = true
				}
			}
			sets = append(sets, allowed)
		}
		if wanted, ok := filters["evidence_id"]; ok {
			allowed := map[string]bool{}
			for _, row := range rows("evidenceeventlinks") {
				f, _ := row["fields"].(map[string]any)
				if stringValue(f["evidence"]) == stringValue(wanted) && stringValue(f["record_state"]) != "removed" {
					allowed[canonicalID(rows("events"), "event_id", "canonical_event_id", stringValue(f["event"]))] = true
				}
			}
			sets = append(sets, allowed)
		}
		if len(sets) > 0 {
			kept := []map[string]any{}
			for _, row := range rows("events") {
				f, _ := row["fields"].(map[string]any)
				id := stringValue(f["event_id"])
				matches := true
				for _, allowed := range sets {
					if !allowed[id] {
						matches = false
						break
					}
				}
				if matches {
					kept = append(kept, row)
				}
			}
			result["events"] = kept
		}
	}
	if query == "style" {
		if wanted, ok := filters["event_id"]; ok {
			wantedEvent := canonicalID(rows("events"), "event_id", "canonical_event_id", stringValue(wanted))
			allowed := map[string]bool{}
			for _, row := range rows("eventstylelinks") {
				f, _ := row["fields"].(map[string]any)
				linkedEvent := canonicalID(rows("events"), "event_id", "canonical_event_id", stringValue(f["event"]))
				if linkedEvent == wantedEvent && stringValue(f["link_status"]) != "removed" && stringValue(f["link_status"]) != "rejected" {
					allowed[canonicalID(rows("styles"), "style_id", "canonical_style_id", stringValue(f["style"]))] = true
				}
			}
			kept := []map[string]any{}
			for _, row := range rows("styles") {
				f, _ := row["fields"].(map[string]any)
				if allowed[stringValue(f["style_id"])] {
					kept = append(kept, row)
				}
			}
			result["styles"] = kept
		}
		identifierFilter := false
		for _, key := range []string{"identifier_id", "style", "issuer_or_scope", "identifier_kind", "value", "normalized_value", "supporting_evidence"} {
			if _, ok := filters[key]; ok {
				identifierFilter = true
				break
			}
		}
		if identifierFilter {
			allowed := map[string]bool{}
			for _, row := range rows("styleidentifiers") {
				f, _ := row["fields"].(map[string]any)
				allowed[canonicalID(rows("styles"), "style_id", "canonical_style_id", stringValue(f["style"]))] = true
			}
			kept := []map[string]any{}
			for _, row := range rows("styles") {
				f, _ := row["fields"].(map[string]any)
				if allowed[stringValue(f["style_id"])] {
					kept = append(kept, row)
				}
			}
			result["styles"] = kept
		}
	}
	if query == "person" {
		people := rows("people")
		identities := rows("sourceidentities")
		claims := rows("roleclaims")

		// A source identity is the stable bridge from a WeChat account to the
		// real person. When the caller searches by WeChat-side facts, derive the
		// matching People and RoleClaims through that bridge.
		identityFilter := false
		for _, key := range []string{"identity_id", "platform", "wechat_id", "source_identity_key", "identity_kind", "identity_scope", "display_name"} {
			if _, ok := filters[key]; ok {
				identityFilter = true
				break
			}
		}
		if identityFilter {
			identityIDs := map[string]bool{}
			personIDs := map[string]bool{}
			for _, row := range identities {
				f, _ := row["fields"].(map[string]any)
				if id := stringValue(f["identity_id"]); id != "" {
					identityIDs[id] = true
				}
				if id := stringValue(f["person"]); id != "" {
					personIDs[id] = true
				}
			}
			people = keepRowsByID(people, "person_id", personIDs)
			claims = keepRoleClaims(claims, personIDs, identityIDs)
		}
		claimFilter := false
		for _, key := range []string{"role_claim_id", "person", "source_identity", "function", "scope_type", "scope_key", "valid_from", "valid_to", "status"} {
			if _, ok := filters[key]; ok {
				claimFilter = true
				break
			}
		}
		if claimFilter {
			claimPeople := map[string]bool{}
			claimIdentities := map[string]bool{}
			for _, row := range claims {
				f, _ := row["fields"].(map[string]any)
				if id := stringValue(f["person"]); id != "" {
					claimPeople[id] = true
				}
				if id := stringValue(f["source_identity"]); id != "" {
					claimIdentities[id] = true
				}
			}
			people = keepRowsByID(people, "person_id", claimPeople)
			identities = keepIdentities(identities, claimPeople, claimIdentities)
		}

		if !identityFilter && !claimFilter {
			// Conversely, a person-side lookup returns the identities and role
			// claims that explain how that person should be interpreted.
			personIDs := map[string]bool{}
			for _, row := range people {
				f, _ := row["fields"].(map[string]any)
				if id := stringValue(f["person_id"]); id != "" {
					personIDs[id] = true
				}
			}
			identities = keepRowsByLink(identities, "person", personIDs)
			claims = keepRoleClaims(claims, personIDs, nil)
		}
		result["people"] = people
		result["sourceidentities"] = identities
		result["roleclaims"] = claims
	}
}

func keepRoleClaims(rows []map[string]any, people, identities map[string]bool) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		if people[stringValue(f["person"])] || identities[stringValue(f["source_identity"])] {
			out = append(out, row)
		}
	}
	return out
}

func keepIdentities(rows []map[string]any, people, identities map[string]bool) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		if people[stringValue(f["person"])] || identities[stringValue(f["identity_id"])] {
			out = append(out, row)
		}
	}
	return out
}

func keepRowsByID(rows []map[string]any, field string, allowed map[string]bool) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		if allowed[stringValue(f[field])] {
			out = append(out, row)
		}
	}
	return out
}

func keepRowsByLink(rows []map[string]any, field string, allowed map[string]bool) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		if allowed[stringValue(f[field])] {
			out = append(out, row)
		}
	}
	return out
}

func executeQuery(_ context.Context, r *common.RuntimeContext) error {
	req, _, _ := parseJSON(r)
	base, err := requireToken(r)
	if err != nil {
		return err
	}
	tables, err := ensureTables(r, base, false)
	if err != nil {
		return err
	}
	result, err := recordsForQuery(r, req, tables, base)
	if err != nil {
		return err
	}
	r.Out(response(r, stringValue(req["operation_id"]), result, nil), nil)
	return nil
}

func response(r *common.RuntimeContext, op string, result any, e error) map[string]any {
	out := map[string]any{"ok": e == nil, "interface_version": InterfaceVersion, "operation_id": op, "result": result, "warnings": []any{}}
	if e != nil {
		out["error"] = map[string]any{"type": "feishu_error", "message": e.Error()}
	}
	return out
}

func executeApply(_ context.Context, r *common.RuntimeContext) error {
	req, raw, _ := parseJSON(r)
	op := stringValue(req["operation_id"])
	base, err := ensureBaseToken(r)
	if err != nil {
		return err
	}
	tables, err := ensureTables(r, base, true)
	if err != nil {
		return err
	}
	if err := ensureMeta(r, base, tables); err != nil {
		return err
	}
	_ = raw
	requestHash := hashBytes([]byte(mustJSON(req)))
	priorID, priorHash, priorState, priorPayload, priorStep, err := findOperation(r, base, tables["Operations"].ID, op)
	if err != nil {
		return err
	}
	if priorID != "" {
		if priorHash != requestHash {
			return invalid("conflict: operation_id %q was already used for a different request", op)
		}
		if priorState == "completed" {
			var saved any
			if priorPayload != "" {
				_ = json.Unmarshal([]byte(priorPayload), &saved)
			}
			if saved == nil {
				saved = map[string]any{"operation_id": op, "state": "completed"}
			}
			r.Out(response(r, op, saved, nil), nil)
			return nil
		}
	}
	applyState := &applyReadState{loaded: map[string][]map[string]any{}, decoded: map[string]bool{}}
	applyStates.Store(r, applyState)
	defer applyStates.Delete(r)
	var opRec map[string]any
	if priorID != "" {
		opRec = map[string]any{"record_id": priorID}
	} else {
		opFields := map[string]any{"operation_id": op, "operation_type": operationType(req["actions"]), "request_hash": requestHash, "targets": operationTargets(req["actions"]), "step": "running", "state": "running", "payload": mustJSON(map[string]any{"results": []any{}})}
		opRec, err = createRecord(r, base, tables["Operations"].ID, opFields)
		if err != nil {
			return err
		}
	}
	results := operationResults(priorPayload)
	actions, _ := req["actions"].([]any)
	completedStep := completedActionIndex(priorStep)
	results, err = applyActionsWithBatches(r, base, tables, op, actions, results, completedStep, priorState, opRec)
	if err != nil {
		return err
	}
	result := map[string]any{"base_token": base, "actions": results}
	if err := updateOperation(r, base, tables["Operations"].ID, opRec, map[string]any{"state": "completed", "step": "completed", "payload": mustJSON(result), "error": ""}); err != nil {
		return err
	}
	// Base writes are eventually consistent. A successful completion update is
	// authoritative for this invocation; immediate list/readback can lag and
	// must not turn an acknowledged write into a false failure. Callers that
	// need later recovery can query the immutable operation ID explicitly.
	r.Out(response(r, op, result, nil), nil)
	return nil
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func operationResults(payload string) []any {
	if strings.TrimSpace(payload) == "" {
		return []any{}
	}
	var saved map[string]any
	if json.Unmarshal([]byte(payload), &saved) != nil {
		return []any{}
	}
	if results, ok := saved["results"].([]any); ok {
		return results
	}
	if results, ok := saved["actions"].([]any); ok {
		return results
	}
	return []any{}
}

func operationTargets(value any) string {
	targets := []map[string]any{}
	actions, _ := value.([]any)
	for _, raw := range actions {
		action, _ := raw.(map[string]any)
		payload, _ := action["payload"].(map[string]any)
		entry := map[string]any{"type": stringValue(action["type"])}
		for _, key := range []string{"event_id", "event", "style_id", "style", "winner", "losers", "evidence_id", "evidence"} {
			if current, ok := payload[key]; ok {
				entry[key] = current
			}
		}
		targets = append(targets, entry)
	}
	return mustJSON(targets)
}

func operationType(value any) string {
	for _, action := range stringValues(value) {
		switch action {
		case "event.merge":
			return "event_merge"
		case "event.split":
			return "event_split"
		case "style.merge":
			return "style_merge"
		}
	}
	if actions, ok := value.([]any); ok {
		for _, raw := range actions {
			if action, ok := raw.(map[string]any); ok {
				switch stringValue(action["type"]) {
				case "event.merge":
					return "event_merge"
				case "event.split":
					return "event_split"
				case "style.merge":
					return "style_merge"
				}
			}
		}
	}
	return "relink"
}

func completedActionIndex(step string) int {
	if step == "" || step == "running" || step == "completed" {
		return -1
	}
	var index int
	if _, err := fmt.Sscanf(step, "%d:", &index); err != nil {
		return -1
	}
	return index
}

func findOperation(r *common.RuntimeContext, base, table, operationID string) (recordID, requestHash, state, payload, step string, err error) {
	rows, err := listRecords(r, base, table)
	if err != nil {
		return "", "", "", "", "", err
	}
	for _, row := range rows {
		fields, _ := row["fields"].(map[string]any)
		if stringValue(fields["operation_id"]) != operationID {
			continue
		}
		recordID = stringValue(row["record_id"])
		if recordID == "" {
			recordID = stringValue(row["id"])
		}
		return recordID, stringValue(fields["request_hash"]), stringValue(fields["state"]), stringValue(fields["payload"]), stringValue(fields["step"]), nil
	}
	return "", "", "", "", "", nil
}

func updateOperation(r *common.RuntimeContext, base, table string, rec map[string]any, fields map[string]any) error {
	id := stringValue(rec["record_id"])
	if id == "" {
		id = stringValue(rec["id"])
	}
	if id == "" {
		return nil
	}
	_, err := updateRecord(r, base, table, id, fields)
	return err
}

func applyAction(r *common.RuntimeContext, base string, tables map[string]tableInfo, op string, action map[string]any) (map[string]any, error) {
	typ := stringValue(action["type"])
	p, _ := action["payload"].(map[string]any)
	if p == nil {
		p = map[string]any{}
	}
	switch typ {
	case "identity.upsert":
		return applyIdentity(r, base, tables, p)
	case "role_claim.upsert":
		// Link cells are submitted as record references, but the interface
		// accepts either a business ID or the convenient nested person object.
		// Normalize before comparing the composite key so retries reuse the
		// existing claim regardless of which input shape was used.
		if personID := mergePersonValue(p["person"]); personID != "" {
			p["person"] = personID
		}
		p = projectFields("RoleClaims", p)
		if stringValue(p["role_claim_id"]) == "" {
			p["role_claim_id"] = roleClaimID(p)
		}
		return upsertByComposite(r, base, tables["RoleClaims"].ID, p, []string{"person", "source_identity", "function", "scope_type", "scope_key"}, "role_claim_id")
	case "evidence.upsert":
		if speaker, ok := p["speaker_identity"].(map[string]any); ok {
			identity, err := applyIdentity(r, base, tables, speaker)
			if err != nil {
				return nil, err
			}
			if id := stringValue(identity["business_id"]); id != "" {
				p["speaker_identity"] = id
			}
		}
		if stringValue(p["source_key"]) == "" {
			p["source_key"] = evidenceSourceKey(p)
		}
		if err := uploadAttachmentFields(r, base, p, "image"); err != nil {
			return nil, err
		}
		return upsertEvidence(r, base, tables["Evidence"].ID, projectFields("Evidence", p))
	case "event.create":
		if stringValue(p["event_id"]) == "" {
			p["event_id"] = stableID(op + ":event.create:" + mustJSON(p))
		}
		p["created_operation_id"] = op
		if stringValue(p["record_state"]) == "" {
			p["record_state"] = "active"
		}
		if p["revision"] == nil {
			p["revision"] = 1
		}
		storedEvent := projectFields("Events", p)
		result, err := upsertByID(r, base, tables["Events"].ID, storedEvent, "event_id", "event_id")
		if err != nil {
			return nil, err
		}
		if ids := actionIDs(p, "evidence_ids", "evidence_id"); len(ids) > 0 {
			for _, id := range ids {
				if _, err := linkRecord(r, base, tables["EvidenceEventLinks"].ID, map[string]any{"evidence": id, "event": stringValue(p["event_id"])}, "evidence", "event", "supporting"); err != nil {
					return nil, err
				}
			}
		}
		for _, styleID := range actionIDs(p, "style_ids", "style_id") {
			if _, err := styleLinkRecord(r, base, tables["EventStyleLinks"].ID, projectFields("EventStyleLinks", map[string]any{"event": stringValue(p["event_id"]), "style": styleID, "link_status": "proposed"})); err != nil {
				return nil, err
			}
		}
		if rels, ok := p["relations"].([]any); ok {
			for _, rawRel := range rels {
				if rel, ok := rawRel.(map[string]any); ok {
					if _, err := relationRecord(r, base, tables["EventRelations"].ID, projectFields("EventRelations", rel)); err != nil {
						return nil, err
					}
				}
			}
		}
		return result, nil
	case "event.attach_evidence":
		normalizeActionRefs(p)
		return linkRecord(r, base, tables["EvidenceEventLinks"].ID, projectFields("EvidenceEventLinks", p), "evidence", "event", "supporting")
	case "event.relate":
		normalizeActionRefs(p)
		return relationRecord(r, base, tables["EventRelations"].ID, projectFields("EventRelations", p))
	case "style.create":
		if stringValue(p["style_id"]) == "" {
			p["style_id"] = stableID(op + ":style.create:" + mustJSON(p))
		}
		eventID := stringValue(p["created_from_event_id"])
		evidenceID := stringValue(p["created_from_evidence_id"])
		if eventID == "" && evidenceID == "" {
			return nil, invalid("style.create requires created_from_event_id or created_from_evidence_id")
		}
		styleStatus, linkStatus := styleCreateStatuses(p)
		if err := validateEnumValue("style_status", styleStatus); err != nil {
			return nil, invalid("%v", err)
		}
		if stringValue(p["link_status"]) != "" && linkStatus != "proposed" && linkStatus != "confirmed" {
			return nil, invalid("style.create link_status has unsupported value %q", linkStatus)
		}
		if eventID != "" {
			events, err := listRecords(r, base, tables["Events"].ID)
			if err != nil {
				return nil, err
			}
			if !hasBusinessID(events, "event_id", eventID) {
				return nil, errs.NewValidationError(errs.SubtypeNotFound, "created_from_event_id %q was not found", eventID)
			}
		}
		if evidenceID != "" {
			evidence, err := listRecords(r, base, tables["Evidence"].ID)
			if err != nil {
				return nil, err
			}
			if !hasBusinessID(evidence, "evidence_id", evidenceID) {
				return nil, errs.NewValidationError(errs.SubtypeNotFound, "created_from_evidence_id %q was not found", evidenceID)
			}
		}
		if err := uploadAttachmentFields(r, base, p, "representative_images"); err != nil {
			return nil, err
		}
		p["style_status"] = styleStatus
		p["record_state"] = "active"
		p["revision"] = 1
		result, err := upsertByID(r, base, tables["Styles"].ID, projectFields("Styles", p), "style_id", "style_id")
		if err != nil {
			return nil, err
		}
		if linkFields := styleCreateLinkFields(eventID, stringValue(p["style_id"]), linkStatus, op); linkFields != nil {
			if _, err := styleLinkRecord(r, base, tables["EventStyleLinks"].ID, linkFields); err != nil {
				return nil, err
			}
		}
		return result, nil
	case "style_identifier.upsert":
		normalizeStyleIdentifierPayload(p)
		fields := projectFields("StyleIdentifiers", p)
		if stringValue(fields["identifier_id"]) == "" {
			fields["identifier_id"] = styleIdentifierID(fields)
		}
		return upsertByComposite(r, base, tables["StyleIdentifiers"].ID, fields, []string{"style", "issuer_or_scope", "identifier_kind", "normalized_value"}, "identifier_id")
	case "event_style.set":
		normalizeActionRefs(p)
		return styleLinkRecord(r, base, tables["EventStyleLinks"].ID, projectFields("EventStyleLinks", p))
	case "event.merge":
		return mergeEvents(r, base, tables, p)
	case "event.split":
		return splitEvent(r, base, tables, p, op)
	case "style.merge":
		return mergeStyles(r, base, tables, p)
	default:
		return nil, invalid("unsupported action %q", typ)
	}
}

// batchDescriptor is the write-only portion of a simple Workline action. More
// involved actions (merge, split, or actions with attachment/upload side
// effects) continue through applyAction so their ordering and error semantics
// stay unchanged.
type batchDescriptor struct {
	table       string
	key         string
	businessKey string
	fields      map[string]any
	match       func(map[string]any) bool
	update      func(map[string]any) map[string]any
	action      string
	sideEffects func() []*batchDescriptor
}

func batchDescriptorFor(r *common.RuntimeContext, base string, tables map[string]tableInfo, op string, action map[string]any) (*batchDescriptor, bool, error) {
	typ := stringValue(action["type"])
	p, _ := action["payload"].(map[string]any)
	if p == nil {
		return nil, false, nil
	}
	// A nested person/speaker and all attachment inputs have dependent writes;
	// leave those actions on the sequential path.
	if typ == "identity.upsert" {
		if _, nested := p["person"].(map[string]any); nested {
			return nil, false, nil
		}
		identityKey, identityKind, err := normalizeIdentityPayload(p)
		if err != nil {
			return nil, false, err
		}
		if stringValue(p["mapping_status"]) == "" {
			p["mapping_status"] = "inferred"
		}
		if stringValue(p["identity_id"]) == "" {
			p["identity_id"] = identityID(identityKind, identityKey)
		}
		fields := projectFields("SourceIdentities", p)
		id := stringValue(fields["identity_id"])
		return &batchDescriptor{table: tables["SourceIdentities"].ID, key: "identity_id", businessKey: "identity_id", fields: fields, action: typ,
			match:  func(existing map[string]any) bool { return stringValue(existing["identity_id"]) == id },
			update: func(existing map[string]any) map[string]any { return withoutField(fields, "identity_id") }}, true, nil
	}
	if typ == "role_claim.upsert" {
		if personID := mergePersonValue(p["person"]); personID != "" {
			p["person"] = personID
		}
		fields := projectFields("RoleClaims", p)
		if stringValue(fields["role_claim_id"]) == "" {
			fields["role_claim_id"] = roleClaimID(fields)
		}
		person, identity, function := stringValue(fields["person"]), stringValue(fields["source_identity"]), stringValue(fields["function"])
		scopeType, scopeKey := stringValue(fields["scope_type"]), stringValue(fields["scope_key"])
		return &batchDescriptor{table: tables["RoleClaims"].ID, key: "role_claim_id", businessKey: "role_claim_id", fields: fields, action: typ,
			match: func(existing map[string]any) bool {
				return stringValue(existing["person"]) == person && stringValue(existing["source_identity"]) == identity && stringValue(existing["function"]) == function && stringValue(existing["scope_type"]) == scopeType && stringValue(existing["scope_key"]) == scopeKey
			},
			update: func(existing map[string]any) map[string]any {
				out := withoutField(fields, "role_claim_id")
				out["supporting_evidence"] = unionStrings(existing["supporting_evidence"], fields["supporting_evidence"])
				return out
			}}, true, nil
	}
	if typ == "evidence.upsert" {
		if _, nested := p["speaker_identity"].(map[string]any); nested || hasAttachmentInput(p["image"]) {
			return nil, false, nil
		}
		if stringValue(p["source_key"]) == "" {
			p["source_key"] = evidenceSourceKey(p)
		}
		fields := projectFields("Evidence", p)
		if stringValue(fields["source_key"]) == "" {
			return nil, false, invalid("evidence.upsert requires source_key or source identity fields")
		}
		if stringValue(fields["evidence_id"]) == "" {
			fields["evidence_id"] = newID()
		}
		sourceKey := stringValue(fields["source_key"])
		return &batchDescriptor{table: tables["Evidence"].ID, key: "source_key", businessKey: "evidence_id", fields: fields, action: typ,
			match: func(existing map[string]any) bool { return stringValue(existing["source_key"]) == sourceKey },
			update: func(existing map[string]any) map[string]any {
				out := withoutFields(fields, "source_key", "evidence_id", "wechat_owner_id", "conversation_id", "message_id", "forward_path", "source_time", "raw_locator")
				return out
			}}, true, nil
	}
	if typ == "event.attach_evidence" {
		normalizeActionRefs(p)
		fields := projectFields("EvidenceEventLinks", p)
		return linkBatchDescriptor(tables["EvidenceEventLinks"].ID, fields, typ), true, nil
	}
	if typ == "event.relate" {
		normalizeActionRefs(p)
		fields := projectFields("EventRelations", p)
		return relationBatchDescriptor(tables["EventRelations"].ID, fields, typ), true, nil
	}
	if typ == "event_style.set" {
		normalizeActionRefs(p)
		fields := projectFields("EventStyleLinks", p)
		return styleLinkBatchDescriptor(tables["EventStyleLinks"].ID, fields, typ), true, nil
	}
	if typ == "event.create" && !hasAttachmentInput(p["image"]) {
		if stringValue(p["event_id"]) == "" {
			p["event_id"] = stableID(op + ":event.create:" + mustJSON(p))
		}
		p["created_operation_id"] = op
		if stringValue(p["record_state"]) == "" {
			p["record_state"] = "active"
		}
		if p["revision"] == nil {
			p["revision"] = 1
		}
		fields := projectFields("Events", p)
		eventID := stringValue(p["event_id"])
		d := idBatchDescriptor(tables["Events"].ID, "event_id", fields, typ)
		d.sideEffects = func() []*batchDescriptor {
			effects := []*batchDescriptor{}
			for _, evidenceID := range actionIDs(p, "evidence_ids", "evidence_id") {
				effects = append(effects, linkBatchDescriptor(tables["EvidenceEventLinks"].ID, projectFields("EvidenceEventLinks", map[string]any{"evidence": evidenceID, "event": eventID}), "event.attach_evidence"))
			}
			for _, styleID := range actionIDs(p, "style_ids", "style_id") {
				effects = append(effects, styleLinkBatchDescriptor(tables["EventStyleLinks"].ID, projectFields("EventStyleLinks", map[string]any{"event": eventID, "style": styleID, "link_status": "proposed"}), "event_style.set"))
			}
			if rels, ok := p["relations"].([]any); ok {
				for _, rawRel := range rels {
					if rel, ok := rawRel.(map[string]any); ok {
						effects = append(effects, relationBatchDescriptor(tables["EventRelations"].ID, projectFields("EventRelations", rel), "event.relate"))
					}
				}
			}
			return effects
		}
		return d, true, nil
	}
	if typ == "style.create" && !hasAttachmentInput(p["representative_images"]) {
		if stringValue(p["style_id"]) == "" {
			p["style_id"] = stableID(op + ":style.create:" + mustJSON(p))
		}
		eventID, evidenceID := stringValue(p["created_from_event_id"]), stringValue(p["created_from_evidence_id"])
		if eventID == "" && evidenceID == "" {
			return nil, false, invalid("style.create requires created_from_event_id or created_from_evidence_id")
		}
		styleStatus, linkStatus := styleCreateStatuses(p)
		if err := validateEnumValue("style_status", styleStatus); err != nil {
			return nil, false, invalid("%v", err)
		}
		if eventID != "" {
			rows, err := listRecords(r, base, tables["Events"].ID)
			if err != nil {
				return nil, false, err
			}
			if !hasBusinessID(rows, "event_id", eventID) {
				return nil, false, errs.NewValidationError(errs.SubtypeNotFound, "created_from_event_id %q was not found", eventID)
			}
		}
		if evidenceID != "" {
			rows, err := listRecords(r, base, tables["Evidence"].ID)
			if err != nil {
				return nil, false, err
			}
			if !hasBusinessID(rows, "evidence_id", evidenceID) {
				return nil, false, errs.NewValidationError(errs.SubtypeNotFound, "created_from_evidence_id %q was not found", evidenceID)
			}
		}
		p["style_status"], p["record_state"], p["revision"] = styleStatus, "active", 1
		fields := projectFields("Styles", p)
		d := idBatchDescriptor(tables["Styles"].ID, "style_id", fields, typ)
		styleID := stringValue(p["style_id"])
		d.sideEffects = func() []*batchDescriptor {
			effects := []*batchDescriptor{}
			if linkFields := styleCreateLinkFields(eventID, styleID, linkStatus, op); linkFields != nil {
				effects = append(effects, styleLinkBatchDescriptor(tables["EventStyleLinks"].ID, linkFields, "event_style.set"))
			}
			return effects
		}
		return d, true, nil
	}
	return nil, false, nil
}

func withoutField(fields map[string]any, field string) map[string]any {
	return withoutFields(fields, field)
}

func withoutFields(fields map[string]any, omitted ...string) map[string]any {
	remove := map[string]bool{}
	for _, field := range omitted {
		remove[field] = true
	}
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		if !remove[key] {
			out[key] = value
		}
	}
	return out
}

func idBatchDescriptor(table, key string, fields map[string]any, typ string) *batchDescriptor {
	id := stringValue(fields[key])
	return &batchDescriptor{table: table, key: key, businessKey: key, fields: fields, action: typ,
		match:  func(existing map[string]any) bool { return stringValue(existing[key]) == id },
		update: func(_ map[string]any) map[string]any { return withoutField(fields, key) }}
}

func linkBatchDescriptor(table string, fields map[string]any, typ string) *batchDescriptor {
	support := stringValue(fields["support_type"])
	if support == "" {
		support = "supporting"
		fields["support_type"] = support
	}
	id := stringValue(fields["link_id"])
	evidenceEventLink := isEvidenceEventLinkTable(table)
	if evidenceEventLink {
		// Evidence-event identity is the pair, not the current interpretation
		// (supporting/direct/confirming). Interpretations are corrections to the
		// same relationship and must update one canonical row.
		id = hashBytes([]byte(strings.Join([]string{stringValue(fields["evidence"]), stringValue(fields["event"])}, "\x1f")))
	} else if id == "" {
		id = hashBytes([]byte(strings.Join([]string{stringValue(fields["evidence"]), stringValue(fields["event"]), support}, "\x1f")))
		fields["link_id"] = id
	}
	if evidenceEventLink {
		fields["link_id"] = id
	}
	fields["record_state"] = "active"
	return &batchDescriptor{table: table, key: "link_id", businessKey: "link_id", fields: fields, action: typ,
		match: func(existing map[string]any) bool {
			if evidenceEventLink {
				state := stringValue(existing["record_state"])
				return stringValue(existing["evidence"]) == stringValue(fields["evidence"]) && stringValue(existing["event"]) == stringValue(fields["event"]) && state == "active"
			}
			return stringValue(existing["link_id"]) == id
		},
		update: func(_ map[string]any) map[string]any { return withoutField(fields, "link_id") }}
}

func isEvidenceEventLinkTable(table string) bool {
	return table != "" && table == knownTableIDs["EvidenceEventLinks"]
}

func relationBatchDescriptor(table string, fields map[string]any, typ string) *batchDescriptor {
	id := stringValue(fields["relation_id"])
	if id == "" {
		id = hashBytes([]byte(strings.Join([]string{stringValue(fields["from_event"]), stringValue(fields["relation_type"]), stringValue(fields["to_event"])}, "\x1f")))
		fields["relation_id"] = id
	}
	return &batchDescriptor{table: table, key: "relation_id", businessKey: "relation_id", fields: fields, action: typ,
		match:  func(existing map[string]any) bool { return stringValue(existing["relation_id"]) == id },
		update: func(_ map[string]any) map[string]any { return withoutField(fields, "relation_id") }}
}

func styleLinkBatchDescriptor(table string, fields map[string]any, typ string) *batchDescriptor {
	id := stringValue(fields["link_id"])
	if id == "" {
		id = hashBytes([]byte(strings.Join([]string{stringValue(fields["event"]), stringValue(fields["style"])}, "\x1f")))
		fields["link_id"] = id
	}
	if fields["link_status"] == nil {
		fields["link_status"] = "confirmed"
	}
	if fields["revision"] == nil {
		fields["revision"] = 1
	}
	return &batchDescriptor{table: table, key: "link_id", businessKey: "link_id", fields: fields, action: typ,
		match:  func(existing map[string]any) bool { return stringValue(existing["link_id"]) == id },
		update: func(_ map[string]any) map[string]any { return withoutField(fields, "link_id") }}
}

func applyActionsWithBatches(r *common.RuntimeContext, base string, tables map[string]tableInfo, op string, actions []any, results []any, completedStep int, priorState string, opRec map[string]any) ([]any, error) {
	for index := 0; index < len(actions); {
		if (priorState == "running" || priorState == "failed") && index <= completedStep {
			index++
			continue
		}
		action, _ := actions[index].(map[string]any)
		desc, batchable, err := batchDescriptorFor(r, base, tables, op, action)
		if err != nil {
			return nil, failApplyBatch(r, base, tables, opRec, index, err)
		}
		if batchable {
			start := index
			descs := []*batchDescriptor{desc}
			index++
			for index < len(actions) {
				if (priorState == "running" || priorState == "failed") && index <= completedStep {
					index++
					continue
				}
				next, ok, nextErr := batchDescriptorFor(r, base, tables, op, actions[index].(map[string]any))
				if nextErr != nil {
					return nil, failApplyBatch(r, base, tables, opRec, index, nextErr)
				}
				if !ok || next.table != desc.table {
					break
				}
				descs = append(descs, next)
				index++
			}
			batchResults, batchErr := batchUpsertRecords(r, base, descs)
			if batchErr != nil {
				return nil, failApplyBatch(r, base, tables, opRec, start, batchErr)
			}
			for _, batchResult := range batchResults {
				results = append(results, batchResult)
			}
			sideEffects := map[string][]*batchDescriptor{}
			for _, descriptor := range descs {
				if descriptor.sideEffects == nil {
					continue
				}
				for _, effect := range descriptor.sideEffects() {
					if effect != nil {
						sideEffects[effect.table] = append(sideEffects[effect.table], effect)
					}
				}
			}
			orderedSideEffectTables := []string{tables["EvidenceEventLinks"].ID, tables["EventStyleLinks"].ID, tables["EventRelations"].ID}
			for _, table := range orderedSideEffectTables {
				effects := sideEffects[table]
				if len(effects) == 0 {
					continue
				}
				if _, err := batchUpsertRecords(r, base, effects); err != nil {
					return nil, failApplyBatch(r, base, tables, opRec, start, fmt.Errorf("batch side effects for %s: %w", tableNameForID(table), err))
				}
			}
			if err := updateOperation(r, base, tables["Operations"].ID, opRec, map[string]any{"step": fmt.Sprintf("%d:batch:%s", index-1, tableNameForID(desc.table)), "payload": mustJSON(map[string]any{"results": results})}); err != nil {
				return nil, err
			}
			continue
		}
		result, actionErr := applyAction(r, base, tables, op, action)
		if actionErr != nil {
			return nil, failApplyBatch(r, base, tables, opRec, index, actionErr)
		}
		results = append(results, result)
		if err := updateOperation(r, base, tables["Operations"].ID, opRec, map[string]any{"step": fmt.Sprintf("%d:%s", index, stringValue(action["type"])), "payload": mustJSON(map[string]any{"results": results})}); err != nil {
			return nil, err
		}
		index++
	}
	return results, nil
}

func failApplyBatch(r *common.RuntimeContext, base string, tables map[string]tableInfo, opRec map[string]any, index int, err error) error {
	step := "running"
	if index > 0 {
		step = fmt.Sprintf("%d:completed", index-1)
	}
	_ = updateOperation(r, base, tables["Operations"].ID, opRec, map[string]any{"state": "failed", "step": step, "error": err.Error()})
	return err
}

func batchUpsertRecords(r *common.RuntimeContext, base string, descriptors []*batchDescriptor) ([]map[string]any, error) {
	if len(descriptors) == 0 {
		return nil, nil
	}
	if _, err := listRecords(r, base, descriptors[0].table); err != nil {
		return nil, err
	}
	stateValue, _ := applyStates.Load(r)
	state := stateValue.(*applyReadState)
	table := descriptors[0].table
	state.mu.Lock()
	rows := append([]map[string]any(nil), state.loaded[table]...)
	state.mu.Unlock()
	results := make([]map[string]any, len(descriptors))
	creates := make([]int, 0)
	updates := make([]int, 0)
	recordIDs := make([]string, len(descriptors))
	prepared := make([]map[string]any, len(descriptors))
	updatePayloads := make([]map[string]any, len(descriptors))
	businessIDs := make([]string, len(descriptors))
	repairUpdates := map[string]map[string]any{}
	duplicateOf := make([]int, len(descriptors))
	for index := range duplicateOf {
		duplicateOf[index] = -1
	}
	pending := map[string]int{}
	for index, descriptor := range descriptors {
		var existing map[string]any
		if isEvidenceEventLinkTable(table) {
			matching := make([]map[string]any, 0)
			for _, row := range rows {
				fields, _ := row["fields"].(map[string]any)
				if descriptor.match(fields) {
					matching = append(matching, row)
				}
			}
			if len(matching) > 0 {
				canonicalID := stringValue(descriptor.fields["link_id"])
				for _, row := range matching {
					fields, _ := row["fields"].(map[string]any)
					if stringValue(fields["link_id"]) == canonicalID {
						existing = row
						break
					}
				}
				if existing == nil {
					// Record IDs are opaque but stable; lexical ordering gives a
					// deterministic winner when no row already has the canonical ID.
					sort.SliceStable(matching, func(i, j int) bool { return rowRecordID(matching[i]) < rowRecordID(matching[j]) })
					existing = matching[0]
				}
				for _, row := range matching {
					if rowRecordID(row) != rowRecordID(existing) && rowRecordID(row) != "" {
						repairUpdates[rowRecordID(row)] = map[string]any{"record_state": "removed"}
					}
				}
			}
		} else {
			for _, row := range rows {
				fields, _ := row["fields"].(map[string]any)
				if descriptor.match(fields) {
					existing = row
					break
				}
			}
		}
		if existing != nil {
			recordIDs[index] = rowRecordID(existing)
			existingFields, _ := existing["fields"].(map[string]any)
			businessIDs[index] = stringValue(existingFields[descriptor.businessKey])
			if isEvidenceEventLinkTable(table) {
				businessIDs[index] = stringValue(descriptor.fields[descriptor.businessKey])
			}
			updatePayloads[index] = descriptor.update(existingFields)
			if isEvidenceEventLinkTable(table) {
				updatePayloads[index]["link_id"] = stringValue(descriptor.fields["link_id"])
			}
			var prepareErr error
			prepared[index], prepareErr = prepareFields(r, base, table, updatePayloads[index])
			if prepareErr != nil {
				return nil, prepareErr
			}
			updates = append(updates, index)
		} else {
			pendingKey := descriptor.table + "\x1f" + descriptor.key + "\x1f" + stringValue(descriptor.fields[descriptor.key])
			if previous, exists := pending[pendingKey]; exists {
				// A batch cannot create the same business key twice. Keep one
				// canonical write, but let the later payload win (not silently
				// discard an explicit correction such as direct vs supporting).
				duplicateOf[index] = previous
				descriptors[previous].fields = descriptor.fields
				descriptors[previous].sideEffects = descriptor.sideEffects
				businessIDs[previous] = stringValue(descriptor.fields[descriptor.businessKey])
				var prepareErr error
				prepared[previous], prepareErr = prepareFields(r, base, table, descriptor.fields)
				if prepareErr != nil {
					return nil, prepareErr
				}
				continue
			}
			pending[pendingKey] = index
			businessIDs[index] = stringValue(descriptor.fields[descriptor.businessKey])
			var prepareErr error
			prepared[index], prepareErr = prepareFields(r, base, table, descriptor.fields)
			if prepareErr != nil {
				return nil, prepareErr
			}
			creates = append(creates, index)
		}
	}
	for start := 0; start < len(creates); start += 200 {
		end := start + 200
		if end > len(creates) {
			end = len(creates)
		}
		createRecords := make([]any, 0, end-start)
		for _, index := range creates[start:end] {
			createRecords = append(createRecords, prepared[index])
		}
		response, err := basecmd.WorklineBaseV3Call(r, "POST", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables/"+url.PathEscape(table)+"/records/batch_create", nil, map[string]any{"create_records": createRecords})
		if err != nil {
			return nil, err
		}
		ids := stringValues(response["record_id_list"])
		if len(ids) == 0 {
			if data, ok := response["data"].(map[string]any); ok {
				ids = stringValues(data["record_id_list"])
			}
		}
		if len(ids) != len(createRecords) {
			return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "batch create response returned %d record IDs for %d records", len(ids), len(createRecords))
		}
		for offset, index := range creates[start:end] {
			recordIDs[index] = ids[offset]
		}
	}
	updateByRecord := map[string]map[string]any{}
	for _, index := range updates {
		if recordIDs[index] == "" {
			return nil, errs.NewInternalError(errs.SubtypeInvalidResponse, "batch update is missing record ID")
		}
		updateByRecord[recordIDs[index]] = prepared[index]
	}
	for recordID, fields := range repairUpdates {
		if recordID != "" {
			updateByRecord[recordID] = fields
		}
	}
	repairIDs := make([]string, 0, len(updateByRecord))
	for recordID := range updateByRecord {
		repairIDs = append(repairIDs, recordID)
	}
	sort.Strings(repairIDs)
	for start := 0; start < len(repairIDs); start += 200 {
		end := start + 200
		if end > len(repairIDs) {
			end = len(repairIDs)
		}
		updateRecords := map[string]any{}
		for _, recordID := range repairIDs[start:end] {
			updateRecords[recordID] = updateByRecord[recordID]
		}
		if _, err := basecmd.WorklineBaseV3Call(r, "POST", "/open-apis/base/v3/bases/"+url.PathEscape(base)+"/tables/"+url.PathEscape(table)+"/records/batch_update", nil, map[string]any{"update_records": updateRecords}); err != nil {
			return nil, err
		}
	}
	for index, previous := range duplicateOf {
		if previous >= 0 {
			recordIDs[index] = recordIDs[previous]
			businessIDs[index] = businessIDs[previous]
			prepared[index] = prepared[previous]
		}
	}
	for index, descriptor := range descriptors {
		reused := containsInt(updates, index) || duplicateOf[index] >= 0
		result := map[string]any{"business_id": businessIDs[index], "record_id": recordIDs[index], "record": map[string]any{"record_id": recordIDs[index], "fields": prepared[index]}, "reused": reused}
		results[index] = result
		if result["business_id"] != "" {
			knownRecordIDs[tableNameForID(table)+"\x1f"+stringValue(result["business_id"])] = recordIDs[index]
		}
		if duplicateOf[index] >= 0 {
			continue
		}
		if containsInt(updates, index) {
			cacheUpdatedRecord(r, table, recordIDs[index], updatePayloads[index])
		} else {
			cacheCreatedRecord(r, table, descriptor.fields, map[string]interface{}{"record_id": recordIDs[index]})
		}
	}
	for recordID, fields := range repairUpdates {
		cacheUpdatedRecord(r, table, recordID, fields)
	}
	return results, nil
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func styleCreateStatuses(payload map[string]any) (styleStatus, linkStatus string) {
	styleStatus = stringValue(payload["style_status"])
	if styleStatus == "" {
		styleStatus = "confirmed"
	}
	linkStatus = stringValue(payload["link_status"])
	if linkStatus == "" {
		if styleStatus == "candidate" {
			linkStatus = "proposed"
		} else {
			linkStatus = "confirmed"
		}
	}
	return styleStatus, linkStatus
}

func styleCreateLinkFields(eventID, styleID, linkStatus, operationID string) map[string]any {
	if eventID == "" {
		return nil
	}
	return map[string]any{
		"event":                eventID,
		"style":                styleID,
		"link_status":          linkStatus,
		"basis":                "created_from_event",
		"created_operation_id": operationID,
	}
}

func actionIDs(payload map[string]any, plural, singular string) []string {
	values := stringValues(payload[plural])
	if len(values) == 0 {
		if value := stringValue(payload[singular]); value != "" {
			values = []string{value}
		}
	}
	return values
}

func normalizeActionRefs(payload map[string]any) {
	if stringValue(payload["event"]) == "" {
		payload["event"] = stringValue(payload["event_id"])
	}
	if stringValue(payload["style"]) == "" {
		payload["style"] = stringValue(payload["style_id"])
	}
	if stringValue(payload["evidence"]) == "" {
		payload["evidence"] = stringValue(payload["evidence_id"])
	}
	if stringValue(payload["link_status"]) == "" {
		payload["link_status"] = stringValue(payload["status"])
	}
}

func hasBusinessID(rows []map[string]any, field, id string) bool {
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		if stringValue(f[field]) == id {
			return true
		}
	}
	return false
}

func applyIdentity(r *common.RuntimeContext, base string, tables map[string]tableInfo, p map[string]any) (map[string]any, error) {
	identityKey, identityKind, err := normalizeIdentityPayload(p)
	if err != nil {
		return nil, err
	}
	if stringValue(p["mapping_status"]) == "" {
		p["mapping_status"] = "inferred"
	}
	person, _ := p["person"].(map[string]any)
	personID := ""
	if person != nil {
		if stringValue(person["person_id"]) == "" {
			person["person_id"] = stableID(strings.Join([]string{"person", identityKind, identityKey}, "\x1f"))
		}
		personID = stringValue(person["person_id"])
		if _, err = upsertByID(r, base, tables["People"].ID, projectFields("People", person), "person_id", "person_id"); err != nil {
			return nil, err
		}
	}
	if personID == "" {
		personID = mergePersonValue(p["person"])
	}
	if stringValue(p["identity_id"]) == "" {
		p["identity_id"] = identityID(identityKind, identityKey)
	}
	identity := projectFields("SourceIdentities", p)
	if personID != "" {
		identity["person"] = personID
	}
	return upsertByID(r, base, tables["SourceIdentities"].ID, identity, "identity_id", "identity_id")
}

// normalizeIdentityPayload keeps legacy WeChat identities stable while also
// admitting source-scoped identities extracted from merged forwards. A real
// WeChat identity gets a canonical source key automatically; an anonymous
// forwarded speaker never occupies the wechat_id column.
func normalizeIdentityPayload(p map[string]any) (identityKey, identityKind string, err error) {
	wechatID := strings.TrimSpace(stringValue(p["wechat_id"]))
	identityKey = strings.TrimSpace(stringValue(p["source_identity_key"]))
	if identityKey == "" {
		identityKey = wechatID
	}
	if identityKey == "" {
		return "", "", invalid("identity.upsert requires wechat_id or source_identity_key")
	}

	identityKind = strings.TrimSpace(stringValue(p["identity_kind"]))
	if identityKind == "" {
		if wechatID != "" {
			identityKind = "wechat_id"
		} else {
			identityKind = "forward_hash"
		}
	}
	if err := validateEnumValue("identity_kind", identityKind); err != nil {
		return "", "", invalid("%v", err)
	}

	p["platform"] = "wechat"
	p["source_identity_key"] = identityKey
	p["identity_kind"] = identityKind
	if identityKind == "forward_hash" {
		delete(p, "wechat_id")
	} else if wechatID != "" {
		p["wechat_id"] = wechatID
	}
	if scope := strings.TrimSpace(stringValue(p["identity_scope"])); scope != "" {
		p["identity_scope"] = scope
	}
	return identityKey, identityKind, nil
}

func identityID(kind, key string) string {
	seedKind := kind
	if kind == "wechat_id" {
		// Preserve the original identity_id algorithm for existing WeChat IDs.
		seedKind = "wechat"
	}
	return hashBytes([]byte(strings.Join([]string{seedKind, key}, "\x1f")))
}

func roleClaimID(fields map[string]any) string {
	return hashBytes([]byte(strings.Join([]string{
		stringValue(fields["person"]),
		stringValue(fields["source_identity"]),
		stringValue(fields["function"]),
		stringValue(fields["scope_type"]),
		stringValue(fields["scope_key"]),
	}, "\x1f")))
}

func normalizeStyleIdentifierPayload(fields map[string]any) {
	value := strings.TrimSpace(stringValue(fields["value"]))
	normalized := strings.TrimSpace(stringValue(fields["normalized_value"]))
	if normalized == "" {
		normalized = strings.ToUpper(value)
	}
	if value == "" {
		value = normalized
	}
	fields["value"] = value
	fields["normalized_value"] = normalized
}

func styleIdentifierID(fields map[string]any) string {
	return hashBytes([]byte(strings.Join([]string{
		stringValue(fields["style"]),
		stringValue(fields["issuer_or_scope"]),
		stringValue(fields["identifier_kind"]),
		stringValue(fields["normalized_value"]),
	}, "\x1f")))
}

func upsertByID(r *common.RuntimeContext, base, table string, fields map[string]any, key, _ string) (map[string]any, error) {
	rows, err := listRecords(r, base, table)
	if err != nil {
		return nil, err
	}
	wanted := stringValue(fields[key])
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		if stringValue(f[key]) == wanted && wanted != "" {
			id := stringValue(row["record_id"])
			if id == "" {
				id = stringValue(row["id"])
			}
			delete(fields, key)
			updated, e := updateRecord(r, base, table, id, fields)
			if e != nil {
				return nil, e
			}
			knownRecordIDs[tableNameForID(table)+"\x1f"+wanted] = id
			return map[string]any{"business_id": wanted, "record_id": id, "record": updated, "reused": true}, nil
		}
	}
	created, err := createRecord(r, base, table, fields)
	if err != nil {
		return nil, err
	}
	id := stringValue(created["record_id"])
	knownRecordIDs[tableNameForID(table)+"\x1f"+wanted] = id
	return map[string]any{"business_id": wanted, "record_id": id, "record": created, "reused": false}, nil
}

func tableNameForID(tableID string) string {
	for name, id := range knownTableIDs {
		if id == tableID {
			return name
		}
	}
	return tableID
}

func upsertByComposite(r *common.RuntimeContext, base, table string, fields map[string]any, keys []string, idKey string) (map[string]any, error) {
	rows, err := listRecords(r, base, table)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		existing, _ := row["fields"].(map[string]any)
		same := true
		for _, key := range keys {
			// Composite-key members are text or single link values.  Compare
			// their business-ID representation so a native link readback and a
			// caller's nested/map form still identify the same claim.
			if stringValue(existing[key]) != stringValue(fields[key]) {
				same = false
				break
			}
		}
		if !same {
			continue
		}
		recordID := stringValue(row["record_id"])
		if recordID == "" {
			recordID = stringValue(row["id"])
		}
		id := stringValue(existing[idKey])
		update := projectFields(table, fields)
		if idKey == "role_claim_id" || idKey == "identifier_id" {
			update["supporting_evidence"] = unionStrings(existing["supporting_evidence"], fields["supporting_evidence"])
		}
		delete(update, idKey)
		updated, e := updateRecord(r, base, table, recordID, update)
		if e != nil {
			return nil, e
		}
		return map[string]any{"business_id": id, "record_id": recordID, "record": updated, "reused": true}, nil
	}
	return upsertByID(r, base, table, fields, idKey, idKey)
}

func unionStrings(values ...any) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		for _, item := range stringValues(value) {
			if item != "" && !seen[item] {
				seen[item] = true
				out = append(out, item)
			}
		}
	}
	return out
}

// upsertEvidence keys on source_key, which is the immutable source identity;
// evidence_id is generated only for a new source and is never replaced on a
// retry or on a correction to the derived excerpt/hash fields.
func upsertEvidence(r *common.RuntimeContext, base, table string, fields map[string]any) (map[string]any, error) {
	rows, err := listRecords(r, base, table)
	if err != nil {
		return nil, err
	}
	wanted := stringValue(fields["source_key"])
	if wanted == "" {
		return nil, invalid("evidence.upsert requires source_key or source identity fields")
	}
	for _, row := range rows {
		stored, _ := row["fields"].(map[string]any)
		if stringValue(stored["source_key"]) != wanted {
			continue
		}
		recordID := stringValue(row["record_id"])
		if recordID == "" {
			recordID = stringValue(row["id"])
		}
		// Source envelope, time and locator are immutable evidence. The speaker
		// link is derived identity resolution and may be filled or corrected by a
		// later pass without rewriting the underlying source message.
		for _, k := range []string{"source_key", "evidence_id", "wechat_owner_id", "conversation_id", "message_id", "forward_path", "source_time", "raw_locator"} {
			delete(fields, k)
		}
		updated, updateErr := updateRecord(r, base, table, recordID, fields)
		if updateErr != nil {
			return nil, updateErr
		}
		businessID := stringValue(stored["evidence_id"])
		knownRecordIDs["Evidence\x1f"+businessID] = recordID
		return map[string]any{"business_id": businessID, "record_id": recordID, "record": updated, "reused": true}, nil
	}
	if stringValue(fields["evidence_id"]) == "" {
		fields["evidence_id"] = newID()
	}
	created, err := createRecord(r, base, table, fields)
	if err != nil {
		return nil, err
	}
	businessID := stringValue(fields["evidence_id"])
	recordID := stringValue(created["record_id"])
	knownRecordIDs["Evidence\x1f"+businessID] = recordID
	return map[string]any{"business_id": businessID, "record_id": recordID, "record": created, "reused": false}, nil
}
func linkRecord(r *common.RuntimeContext, base, table string, p map[string]any, left, right, support string) (map[string]any, error) {
	if current := stringValue(p["support_type"]); current != "" {
		support = current
	} else {
		p["support_type"] = support
	}
	p["record_state"] = "active"
	if isEvidenceEventLinkTable(table) {
		p["link_id"] = hashBytes([]byte(strings.Join([]string{stringValue(p[left]), stringValue(p[right])}, "\x1f")))
		return upsertEvidenceEventLink(r, base, table, p)
	}
	id := stringValue(p["link_id"])
	if id == "" {
		id = hashBytes([]byte(strings.Join([]string{stringValue(p[left]), stringValue(p[right]), support}, "\x1f")))
		p["link_id"] = id
	}
	return upsertByID(r, base, table, p, "link_id", "link_id")
}

func upsertEvidenceEventLink(r *common.RuntimeContext, base, table string, fields map[string]any) (map[string]any, error) {
	rows, err := listRecords(r, base, table)
	if err != nil {
		return nil, err
	}
	evidence, event := stringValue(fields["evidence"]), stringValue(fields["event"])
	matching := make([]map[string]any, 0)
	canonicalID := stringValue(fields["link_id"])
	for _, row := range rows {
		stored, _ := row["fields"].(map[string]any)
		if stringValue(stored["evidence"]) != evidence || stringValue(stored["event"]) != event || stringValue(stored["record_state"]) != "active" {
			continue
		}
		matching = append(matching, row)
	}
	if len(matching) > 0 {
		var winner map[string]any
		for _, row := range matching {
			stored, _ := row["fields"].(map[string]any)
			if stringValue(stored["link_id"]) == canonicalID {
				winner = row
				break
			}
		}
		if winner == nil {
			sort.SliceStable(matching, func(i, j int) bool { return rowRecordID(matching[i]) < rowRecordID(matching[j]) })
			winner = matching[0]
		}
		recordID := rowRecordID(winner)
		update := withoutField(fields, "link_id")
		update["link_id"] = canonicalID
		updated, updateErr := updateRecord(r, base, table, recordID, update)
		if updateErr != nil {
			return nil, updateErr
		}
		for _, row := range matching {
			loserID := rowRecordID(row)
			if loserID == "" || loserID == recordID {
				continue
			}
			if _, updateErr := updateRecord(r, base, table, loserID, map[string]any{"record_state": "removed"}); updateErr != nil {
				return nil, updateErr
			}
		}
		return map[string]any{"business_id": canonicalID, "record_id": recordID, "record": updated, "reused": true}, nil
	}
	return upsertByID(r, base, table, fields, "link_id", "link_id")
}
func relationRecord(r *common.RuntimeContext, base, table string, p map[string]any) (map[string]any, error) {
	id := stringValue(p["relation_id"])
	if id == "" {
		id = hashBytes([]byte(strings.Join([]string{stringValue(p["from_event"]), stringValue(p["relation_type"]), stringValue(p["to_event"])}, "\x1f")))
		p["relation_id"] = id
	}
	return upsertByID(r, base, table, p, "relation_id", "relation_id")
}
func styleLinkRecord(r *common.RuntimeContext, base, table string, p map[string]any) (map[string]any, error) {
	id := stringValue(p["link_id"])
	if id == "" {
		id = hashBytes([]byte(strings.Join([]string{stringValue(p["event"]), stringValue(p["style"])}, "\x1f")))
		p["link_id"] = id
	}
	if p["link_status"] == nil {
		p["link_status"] = "confirmed"
	}
	if p["revision"] == nil {
		p["revision"] = 1
	}
	return upsertByID(r, base, table, p, "link_id", "link_id")
}

func mergeEvents(r *common.RuntimeContext, base string, tables map[string]tableInfo, p map[string]any) (map[string]any, error) {
	winner := stringValue(p["winner"])
	losers := make([]any, 0, len(stringValues(p["losers"])))
	for _, id := range stringValues(p["losers"]) {
		losers = append(losers, id)
	}
	if winner == "" || len(losers) == 0 {
		return nil, invalid("event.merge requires winner and losers")
	}
	events, err := listRecords(r, base, tables["Events"].ID)
	if err != nil {
		return nil, err
	}
	canonicalWinner := canonicalID(events, "event_id", "canonical_event_id", winner)
	if !hasBusinessID(events, "event_id", winner) {
		return nil, errs.NewValidationError(errs.SubtypeNotFound, "event winner %q was not found", winner)
	}
	winner = canonicalWinner
	resolvedLosers := make([]any, 0, len(losers))
	seenLoser := map[string]bool{}
	for _, value := range losers {
		id := stringValue(value)
		if id == "" || !hasBusinessID(events, "event_id", id) {
			return nil, errs.NewValidationError(errs.SubtypeNotFound, "event loser %q was not found", id)
		}
		id = canonicalID(events, "event_id", "canonical_event_id", id)
		if id == winner || seenLoser[id] {
			continue
		}
		seenLoser[id] = true
		resolvedLosers = append(resolvedLosers, id)
	}
	if len(resolvedLosers) == 0 {
		return map[string]any{"winner_id": winner, "merged_ids": losers, "reused": true}, nil
	}
	// Migrate supporting links and relations without deleting source rows.
	if err := migrateEventLinks(r, base, tables, winner, resolvedLosers); err != nil {
		return nil, err
	}
	if err := migrateEventRelations(r, base, tables, winner, resolvedLosers); err != nil {
		return nil, err
	}
	// Only mark old objects after every dependent relationship has been
	// migrated. This ordering makes a retry safe after a partial failure.
	for _, value := range resolvedLosers {
		id := stringValue(value)
		for _, row := range events {
			f, _ := row["fields"].(map[string]any)
			if stringValue(f["event_id"]) != id {
				continue
			}
			rec := rowRecordID(row)
			_, err = updateRecord(r, base, tables["Events"].ID, rec, map[string]any{"record_state": "merged", "canonical_event_id": winner, "revision": nextRevision(f["revision"])})
			if err != nil {
				return nil, err
			}
		}
	}
	for _, row := range events {
		f, _ := row["fields"].(map[string]any)
		if stringValue(f["event_id"]) == winner {
			if _, err = updateRecord(r, base, tables["Events"].ID, rowRecordID(row), map[string]any{"revision": nextRevision(f["revision"])}); err != nil {
				return nil, err
			}
			break
		}
	}
	return map[string]any{"winner_id": winner, "merged_ids": resolvedLosers}, nil
}

func rowRecordID(row map[string]any) string {
	if id := stringValue(row["record_id"]); id != "" {
		return id
	}
	return stringValue(row["id"])
}

func nextRevision(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number) + 1
	case float32:
		return int(number) + 1
	case int:
		return number + 1
	case int64:
		return int(number) + 1
	case json.Number:
		if n, err := number.Int64(); err == nil {
			return int(n) + 1
		}
	}
	return 2
}

func migrateEventLinks(r *common.RuntimeContext, base string, tables map[string]tableInfo, winner string, losers []any) error {
	for _, tableName := range []string{"EvidenceEventLinks", "EventStyleLinks"} {
		rows, err := listRecords(r, base, tables[tableName].ID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			f, _ := row["fields"].(map[string]any)
			if !containsString(losers, stringValue(f["event"])) {
				continue
			}
			if tableName == "EvidenceEventLinks" && stringValue(f["record_state"]) == "removed" {
				continue
			}
			if tableName == "EventStyleLinks" && (stringValue(f["link_status"]) == "removed" || stringValue(f["link_status"]) == "rejected") {
				continue
			}
			copyFields := map[string]any{}
			for k, v := range f {
				copyFields[k] = v
			}
			copyFields["event"] = winner
			if tableName == "EvidenceEventLinks" {
				copyFields["link_id"] = hashBytes([]byte(strings.Join([]string{stringValue(copyFields["evidence"]), winner}, "\x1f")))
			} else {
				copyFields["link_id"] = hashBytes([]byte(strings.Join([]string{winner, stringValue(copyFields["style"])}, "\x1f")))
				copyFields["link_status"] = strongestEventMergeStyleStatus(rows, winner, losers, stringValue(copyFields["style"]))
				copyFields["revision"] = nextRevision(f["revision"])
			}
			if tableName == "EvidenceEventLinks" {
				if _, err := linkRecord(r, base, tables[tableName].ID, copyFields, "evidence", "event", stringValue(copyFields["support_type"])); err != nil {
					return err
				}
			} else if _, err := upsertByID(r, base, tables[tableName].ID, copyFields, "link_id", "link_id"); err != nil {
				return err
			}
			rec := stringValue(row["record_id"])
			if rec == "" {
				rec = stringValue(row["id"])
			}
			removed := map[string]any{"record_state": "removed"}
			if tableName == "EventStyleLinks" {
				removed = map[string]any{"link_status": "removed"}
			}
			_, err = updateRecord(r, base, tables[tableName].ID, rec, removed)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func strongestEventMergeStyleStatus(rows []map[string]any, winner string, losers []any, style string) string {
	status := "proposed"
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		if stringValue(f["style"]) != style || (stringValue(f["event"]) != winner && !containsString(losers, stringValue(f["event"]))) {
			continue
		}
		if stringValue(f["link_status"]) == "confirmed" {
			return "confirmed"
		}
	}
	return status
}

func migrateEventRelations(r *common.RuntimeContext, base string, tables map[string]tableInfo, winner string, losers []any) error {
	rows, err := listRecords(r, base, tables["EventRelations"].ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		from, to := stringValue(f["from_event"]), stringValue(f["to_event"])
		if !containsString(losers, from) && !containsString(losers, to) {
			continue
		}
		if containsString(losers, from) {
			from = winner
		}
		if containsString(losers, to) {
			to = winner
		}
		if from == to {
			if _, err := updateRecord(r, base, tables["EventRelations"].ID, rowRecordID(row), map[string]any{"relation_status": "rejected"}); err != nil {
				return err
			}
			continue
		}
		copyFields := map[string]any{}
		for k, v := range f {
			copyFields[k] = v
		}
		copyFields["from_event"], copyFields["to_event"] = from, to
		copyFields["relation_id"] = hashBytes([]byte(strings.Join([]string{from, stringValue(copyFields["relation_type"]), to}, "\x1f")))
		if _, err := upsertByID(r, base, tables["EventRelations"].ID, copyFields, "relation_id", "relation_id"); err != nil {
			return err
		}
		if from != stringValue(f["from_event"]) || to != stringValue(f["to_event"]) {
			rec := stringValue(row["record_id"])
			if rec == "" {
				rec = stringValue(row["id"])
			}
			if _, err := updateRecord(r, base, tables["EventRelations"].ID, rec, map[string]any{"relation_status": "rejected"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func containsString(values []any, want string) bool {
	for _, v := range values {
		if stringValue(v) == want {
			return true
		}
	}
	return false
}
func splitEvent(r *common.RuntimeContext, base string, tables map[string]tableInfo, p map[string]any, op string) (map[string]any, error) {
	defs, _ := p["events"].([]any)
	if len(defs) == 0 {
		return nil, invalid("event.split requires events")
	}
	old := stringValue(p["event_id"])
	if old == "" {
		return nil, invalid("event.split requires event_id")
	}
	existing, err := listRecords(r, base, tables["Events"].ID)
	if err != nil {
		return nil, err
	}
	if !hasBusinessID(existing, "event_id", old) {
		return nil, errs.NewValidationError(errs.SubtypeNotFound, "event %q was not found", old)
	}
	ids := []string{}
	for index, d := range defs {
		m, ok := d.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(m["event_id"]) == "" {
			m["event_id"] = stableID(op + fmt.Sprintf(":event.split:%d:%s", index, mustJSON(m)))
		}
		m["created_operation_id"] = op
		if m["revision"] == nil {
			m["revision"] = 1
		}
		out, err := upsertByID(r, base, tables["Events"].ID, projectFields("Events", m), "event_id", "event_id")
		if err != nil {
			return nil, err
		}
		ids = append(ids, stringValue(out["business_id"]))
		for _, evidenceID := range actionIDs(m, "evidence_ids", "evidence_id") {
			_, err = linkRecord(r, base, tables["EvidenceEventLinks"].ID, projectFields("EvidenceEventLinks", map[string]any{"evidence": evidenceID, "event": stringValue(m["event_id"])}), "evidence", "event", "supporting")
			if err != nil {
				return nil, err
			}
		}
		for _, styleID := range actionIDs(m, "style_ids", "style_id") {
			_, err = styleLinkRecord(r, base, tables["EventStyleLinks"].ID, projectFields("EventStyleLinks", map[string]any{"event": stringValue(m["event_id"]), "style": styleID, "link_status": "proposed"}))
			if err != nil {
				return nil, err
			}
		}
		if rels, ok := m["relations"].([]any); ok {
			for _, rawRel := range rels {
				if rel, ok := rawRel.(map[string]any); ok {
					if _, err = relationRecord(r, base, tables["EventRelations"].ID, projectFields("EventRelations", rel)); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	{
		rows, err := listRecords(r, base, tables["Events"].ID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			f, _ := row["fields"].(map[string]any)
			if stringValue(f["event_id"]) == old {
				if _, err := updateRecord(r, base, tables["Events"].ID, rowRecordID(row), map[string]any{"record_state": "excluded", "revision": nextRevision(f["revision"])}); err != nil {
					return nil, err
				}
			}
		}
	}
	return map[string]any{"event_ids": ids}, nil
}

func stringValues(v any) []string {
	var out []string
	switch values := v.(type) {
	case []any:
		for _, value := range values {
			out = append(out, stringValue(value))
		}
	case []string:
		out = append(out, values...)
	case string:
		var decoded []string
		if strings.HasPrefix(strings.TrimSpace(values), "[") && json.Unmarshal([]byte(values), &decoded) == nil {
			out = append(out, decoded...)
		}
	}
	return out
}
func mergeStyles(r *common.RuntimeContext, base string, tables map[string]tableInfo, p map[string]any) (map[string]any, error) {
	winner := stringValue(p["winner"])
	losers := make([]any, 0, len(stringValues(p["losers"])))
	for _, id := range stringValues(p["losers"]) {
		losers = append(losers, id)
	}
	if winner == "" || len(losers) == 0 {
		return nil, invalid("style.merge requires winner and losers")
	}
	styles, err := listRecords(r, base, tables["Styles"].ID)
	if err != nil {
		return nil, err
	}
	if !hasBusinessID(styles, "style_id", winner) {
		return nil, errs.NewValidationError(errs.SubtypeNotFound, "style winner %q was not found", winner)
	}
	winner = canonicalID(styles, "style_id", "canonical_style_id", winner)
	resolvedLosers := make([]any, 0, len(losers))
	seenLoser := map[string]bool{}
	for _, value := range losers {
		id := stringValue(value)
		if id == "" || !hasBusinessID(styles, "style_id", id) {
			return nil, errs.NewValidationError(errs.SubtypeNotFound, "style loser %q was not found", id)
		}
		id = canonicalID(styles, "style_id", "canonical_style_id", id)
		if id != winner && !seenLoser[id] {
			seenLoser[id] = true
			resolvedLosers = append(resolvedLosers, id)
		}
	}
	if len(resolvedLosers) == 0 {
		return map[string]any{"winner_id": winner, "merged_ids": losers, "reused": true}, nil
	}
	links, err := listRecords(r, base, tables["EventStyleLinks"].ID)
	if err != nil {
		return nil, err
	}
	for _, row := range links {
		f, _ := row["fields"].(map[string]any)
		for _, l := range resolvedLosers {
			if stringValue(f["style"]) == stringValue(l) && (stringValue(f["link_status"]) == "confirmed" || stringValue(f["link_status"]) == "proposed") {
				copyFields := map[string]any{}
				for k, v := range f {
					copyFields[k] = v
				}
				copyFields["style"] = winner
				copyFields["link_status"] = strongestStyleMergeLinkStatus(links, stringValue(copyFields["event"]), winner, resolvedLosers)
				copyFields["link_id"] = hashBytes([]byte(strings.Join([]string{stringValue(copyFields["event"]), winner}, "\x1f")))
				copyFields["revision"] = nextRevision(f["revision"])
				if _, err := upsertByID(r, base, tables["EventStyleLinks"].ID, copyFields, "link_id", "link_id"); err != nil {
					return nil, err
				}
				rec := stringValue(row["record_id"])
				if rec == "" {
					rec = stringValue(row["id"])
				}
				if _, err := updateRecord(r, base, tables["EventStyleLinks"].ID, rec, map[string]any{"link_status": "removed"}); err != nil {
					return nil, err
				}
			}
		}
	}
	var winnerRow map[string]any
	aliases := []string{}
	images := []any{}
	for _, row := range styles {
		f, _ := row["fields"].(map[string]any)
		if stringValue(f["style_id"]) == winner {
			winnerRow = row
			aliases = append(aliases, strings.Split(stringValue(f["aliases"]), "\n")...)
			images = appendJSONValues(images, f["representative_images"])
		}
	}
	for _, row := range styles {
		f, _ := row["fields"].(map[string]any)
		for _, l := range resolvedLosers {
			if stringValue(f["style_id"]) == stringValue(l) {
				aliases = append(aliases, strings.Split(stringValue(f["name"]), "\n")...)
				aliases = append(aliases, strings.Split(stringValue(f["aliases"]), "\n")...)
				images = appendJSONValues(images, f["representative_images"])
			}
		}
	}
	if winnerRow != nil {
		rec := stringValue(winnerRow["record_id"])
		if rec == "" {
			rec = stringValue(winnerRow["id"])
		}
		uniq := []string{}
		seen := map[string]bool{}
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if alias != "" && !seen[alias] && alias != stringValue(winnerRow["fields"].(map[string]any)["name"]) {
				seen[alias] = true
				uniq = append(uniq, alias)
			}
		}
		winnerFields := winnerRow["fields"].(map[string]any)
		update := map[string]any{
			"aliases":      strings.Join(uniq, "\n"),
			"style_status": mergedStyleStatus(winnerFields),
			"revision":     nextRevision(winnerFields["revision"]),
		}
		if images = uniqueJSONValues(images); len(images) > 0 {
			update["representative_images"] = images
		}
		if _, err := updateRecord(r, base, tables["Styles"].ID, rec, update); err != nil {
			return nil, err
		}
	}
	// Mark old styles only after all links have been copied. This allows a
	// retry to observe the canonical links and avoid duplicate records.
	for _, row := range styles {
		f, _ := row["fields"].(map[string]any)
		for _, l := range resolvedLosers {
			if stringValue(f["style_id"]) == stringValue(l) {
				if _, err := updateRecord(r, base, tables["Styles"].ID, rowRecordID(row), map[string]any{"record_state": "merged", "canonical_style_id": winner, "revision": nextRevision(f["revision"])}); err != nil {
					return nil, err
				}
			}
		}
	}
	return map[string]any{"winner_id": winner, "merged_ids": resolvedLosers}, nil
}

// A merge does not constitute confirmation. Preserve the winner's explicit
// candidate state; legacy rows without this additive field retain the old
// confirmed semantics.
func mergedStyleStatus(winnerFields map[string]any) string {
	if stringValue(winnerFields["style_status"]) == "candidate" {
		return "candidate"
	}
	return "confirmed"
}

func strongestStyleMergeLinkStatus(rows []map[string]any, event, winner string, losers []any) string {
	status := "proposed"
	for _, row := range rows {
		f, _ := row["fields"].(map[string]any)
		if stringValue(f["event"]) != event || (stringValue(f["style"]) != winner && !containsString(losers, stringValue(f["style"]))) {
			continue
		}
		if stringValue(f["link_status"]) == "confirmed" {
			return "confirmed"
		}
	}
	return status
}

func appendJSONValues(dst []any, value any) []any {
	switch values := value.(type) {
	case []any:
		return append(dst, values...)
	case []string:
		for _, item := range values {
			dst = append(dst, item)
		}
	case string:
		var decoded []any
		if json.Unmarshal([]byte(values), &decoded) == nil {
			dst = append(dst, decoded...)
		}
	}
	return dst
}

func uniqueJSONValues(values []any) []any {
	out := make([]any, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		key := stringValue(value)
		if key == "" {
			key = mustJSON(value)
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func uploadAttachmentFields(r *common.RuntimeContext, base string, payload map[string]any, field string) error {
	value, ok := payload[field]
	if !ok || value == nil {
		return nil
	}
	items := make([]any, 0)
	for _, raw := range attachmentValues(value) {
		if token := stringValue(raw); strings.HasPrefix(token, "file_") {
			items = append(items, map[string]any{"file_token": token})
			continue
		}
		path := ""
		if item, ok := raw.(map[string]any); ok {
			path = stringValue(item["path"])
			if path == "" {
				path = stringValue(item["file"])
			}
			if token := stringValue(item["file_token"]); token != "" {
				items = append(items, item)
				continue
			}
		} else {
			path = stringValue(raw)
		}
		if path == "" {
			continue
		}
		info, err := r.FileIO().Stat(path)
		if err != nil {
			return errs.NewValidationError(errs.SubtypeInvalidArgument, "attachment %q is not readable: %v", path, err).WithCause(err)
		}
		attachment, err := basecmd.UploadWorklineAttachment(r, path, filepath.Base(path), info.Size(), base)
		if err != nil {
			return err
		}
		items = append(items, attachment)
	}
	if len(items) > 0 {
		payload[field] = items
	}
	return nil
}

func attachmentValues(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	if list, ok := value.([]string); ok {
		out := make([]any, len(list))
		for i := range list {
			out[i] = list[i]
		}
		return out
	}
	if encoded, ok := value.(string); ok && strings.HasPrefix(strings.TrimSpace(encoded), "[") {
		var out []any
		if json.Unmarshal([]byte(encoded), &out) == nil {
			return out
		}
	}
	return []any{value}
}

func hasAttachmentInput(value any) bool {
	for _, raw := range attachmentValues(value) {
		if stringValue(raw) != "" {
			return true
		}
		if item, ok := raw.(map[string]any); ok {
			for _, key := range []string{"path", "file", "file_token"} {
				if stringValue(item[key]) != "" {
					return true
				}
			}
		}
	}
	return false
}

func executeStyleEvents(_ context.Context, r *common.RuntimeContext) error {
	base, err := requireToken(r)
	if err != nil {
		return err
	}
	tables, err := ensureTables(r, base, false)
	if err != nil {
		return err
	}
	styles, err := listRecords(r, base, tables["Styles"].ID)
	if err != nil {
		return err
	}
	wanted := r.Str("style-id")
	if !hasBusinessID(styles, "style_id", wanted) {
		return errs.NewValidationError(errs.SubtypeNotFound, "style %q was not found", wanted)
	}
	wanted = canonicalID(styles, "style_id", "canonical_style_id", wanted)
	links, err := listRecords(r, base, tables["EventStyleLinks"].ID)
	if err != nil {
		return err
	}
	events, err := listRecords(r, base, tables["Events"].ID)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, row := range links {
		f, _ := row["fields"].(map[string]any)
		styleID := canonicalID(styles, "style_id", "canonical_style_id", stringValue(f["style"]))
		if styleID == wanted && (stringValue(f["link_status"]) == "confirmed" || stringValue(f["link_status"]) == "proposed") {
			ids[canonicalID(events, "event_id", "canonical_event_id", stringValue(f["event"]))] = true
		}
	}
	evidenceLinks, err := listRecords(r, base, tables["EvidenceEventLinks"].ID)
	if err != nil {
		return err
	}
	evidenceByEvent := map[string][]map[string]any{}
	for _, row := range evidenceLinks {
		f, _ := row["fields"].(map[string]any)
		if stringValue(f["record_state"]) == "removed" {
			continue
		}
		eventID := canonicalID(events, "event_id", "canonical_event_id", stringValue(f["event"]))
		if ids[eventID] {
			evidenceByEvent[eventID] = append(evidenceByEvent[eventID], row)
		}
	}
	evidence, err := listRecords(r, base, tables["Evidence"].ID)
	if err != nil {
		return err
	}
	evidenceByID := map[string]map[string]any{}
	for _, row := range evidence {
		f, _ := row["fields"].(map[string]any)
		evidenceByID[stringValue(f["evidence_id"])] = row
	}
	relations, err := listRecords(r, base, tables["EventRelations"].ID)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, row := range events {
		f, _ := row["fields"].(map[string]any)
		eventID := canonicalID(events, "event_id", "canonical_event_id", stringValue(f["event_id"]))
		if ids[eventID] && stringValue(f["record_state"]) == "active" {
			eventTime := stringValue(f["occurred_at"])
			item := map[string]any{"event": row, "evidence": []map[string]any{}, "relations": []map[string]any{}, "occurred_at": eventTime}
			for _, link := range evidenceByEvent[eventID] {
				lf, _ := link["fields"].(map[string]any)
				if ev := evidenceByID[stringValue(lf["evidence"])]; ev != nil {
					item["evidence"] = append(item["evidence"].([]map[string]any), ev)
					ef, _ := ev["fields"].(map[string]any)
					if earliest := stringValue(ef["source_time"]); eventTime == "" && earliest != "" && (item["occurred_at"] == "" || earliest < item["occurred_at"].(string)) {
						item["occurred_at"] = earliest
					}
				}
			}
			for _, rel := range relations {
				rf, _ := rel["fields"].(map[string]any)
				if stringValue(rf["relation_status"]) == "rejected" {
					continue
				}
				if canonicalID(events, "event_id", "canonical_event_id", stringValue(rf["from_event"])) == eventID || canonicalID(events, "event_id", "canonical_event_id", stringValue(rf["to_event"])) == eventID {
					item["relations"] = append(item["relations"].([]map[string]any), rel)
				}
			}
			out = append(out, item)
		}
	}
	sortStyleEventItems(out)
	r.Out(response(r, "", map[string]any{"style_id": wanted, "events": out}, nil), nil)
	return nil
}

func sortStyleEventItems(out []map[string]any) {
	sort.SliceStable(out, func(i, j int) bool {
		left := stringValue(out[i]["occurred_at"])
		right := stringValue(out[j]["occurred_at"])
		if left == "" {
			return false
		}
		if right == "" {
			return true
		}
		return left < right
	})
}

func canonicalID(rows []map[string]any, idField, canonicalField, id string) string {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		next := ""
		for _, row := range rows {
			f, _ := row["fields"].(map[string]any)
			if stringValue(f[idField]) == id {
				next = stringValue(f[canonicalField])
				break
			}
		}
		if next == "" || next == id {
			return id
		}
		id = next
	}
	return id
}
