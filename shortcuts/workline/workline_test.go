package workline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/larksuite/cli/internal/cmdutil"
	"github.com/larksuite/cli/internal/core"
	"github.com/larksuite/cli/internal/httpmock"
	"github.com/larksuite/cli/shortcuts/common"
)

func TestFrozenSchemaAndProjection(t *testing.T) {
	if schemaType("Evidence", "source_time") != "datetime" || schemaType("Evidence", "image") != "attachment" {
		t.Fatalf("Evidence native field types were not preserved")
	}
	if schemaType("People", "functions") != "select" || schemaType("Events", "revision") != "number" {
		t.Fatalf("People/Events native field types were not preserved")
	}
	body := schemaFieldBody("Events", "actors", map[string]tableInfo{"People": {ID: "tbl_people"}})
	if schemaType("Events", "actors") != "link" || body["link_table"] != "tbl_people" {
		t.Fatalf("native relationship field was not configured with link_table")
	}
	singleBody := schemaFieldBody("EvidenceEventLinks", "event", map[string]tableInfo{"Events": {ID: "tbl_events"}})
	if schemaType("EvidenceEventLinks", "event") != "link" || singleBody["type"] != "link" || singleBody["link_table"] != "tbl_events" {
		t.Fatalf("single-value relationship field was not configured as a native link: %#v", singleBody)
	}
	if _, exists := body["multiple"]; exists {
		t.Fatalf("Base link field schema must not contain multiple: %#v", body)
	}
	if schemaType("Operations", "created_at") != "created_at" || schemaType("Operations", "updated_at") != "updated_at" {
		t.Fatalf("system time fields were not configured as Base system fields")
	}
	identityRoleBody := schemaFieldBody("RoleClaims", "source_identity", map[string]tableInfo{"SourceIdentities": {ID: "tbl_identities"}})
	actorIdentityBody := schemaFieldBody("Events", "actor_identities", map[string]tableInfo{"SourceIdentities": {ID: "tbl_identities"}})
	identifierStyleBody := schemaFieldBody("StyleIdentifiers", "style", map[string]tableInfo{"Styles": {ID: "tbl_styles"}})
	identifierEvidenceBody := schemaFieldBody("StyleIdentifiers", "supporting_evidence", map[string]tableInfo{"Evidence": {ID: "tbl_evidence"}})
	if identityRoleBody["link_table"] != "tbl_identities" || actorIdentityBody["link_table"] != "tbl_identities" || identifierStyleBody["link_table"] != "tbl_styles" || identifierEvidenceBody["link_table"] != "tbl_evidence" {
		t.Fatalf("new identity/style relationships are not native links: role=%#v actors=%#v identifier=%#v evidence=%#v", identityRoleBody, actorIdentityBody, identifierStyleBody, identifierEvidenceBody)
	}
	styleEvidenceBody := schemaFieldBody("Styles", "created_from_evidence_id", map[string]tableInfo{"Evidence": {ID: "tbl_evidence"}})
	if schemaType("Styles", "style_status") != "select" || schemaType("Styles", "created_from_evidence_id") != "link" || styleEvidenceBody["link_table"] != "tbl_evidence" {
		t.Fatalf("candidate Style fields were not configured with native types: %#v", styleEvidenceBody)
	}
	fields := projectFields("Events", map[string]any{
		"event_id": "evt_1", "summary": "a", "evidence_ids": []any{"ev_1"}, "style_ids": []any{"sty_1"}, "relations": []any{},
	})
	if fields["evidence_ids"] != nil || fields["style_ids"] != nil || fields["relations"] != nil {
		t.Fatalf("event auxiliary fields leaked into the Events record: %#v", fields)
	}
	if got := coerceFields("Events", map[string]any{"summary": map[string]any{"source": "agent"}})["summary"]; got != `{"source":"agent"}` {
		t.Fatalf("text JSON encoding = %#v", got)
	}
	if got, ok := coerceFields("Events", map[string]any{"record_state": "active"})["record_state"].([]string); !ok || len(got) != 1 || got[0] != "active" {
		t.Fatalf("select scalar was not encoded as an option array: %#v", got)
	}
}

func TestTokenForUsesEnterpriseDefault(t *testing.T) {
	cmd := &cobra.Command{}
	runtime := &common.RuntimeContext{
		Cmd:    cmd,
		Config: &core.CliConfig{WorklineBaseToken: "config-token"},
	}

	if got := tokenFor(runtime); got != core.DefaultWorklineBaseToken {
		t.Fatalf("tokenFor() = %q, want enterprise default", got)
	}
}

func TestRequireTokenUsesEnterpriseDefault(t *testing.T) {
	cmd := &cobra.Command{}
	runtime := &common.RuntimeContext{Cmd: cmd, Config: &core.CliConfig{}}

	got, err := requireToken(runtime)
	if err != nil {
		t.Fatalf("requireToken() error = %v", err)
	}
	if got != core.DefaultWorklineBaseToken {
		t.Fatalf("requireToken() = %q, want enterprise default", got)
	}
}

func TestEvidenceSourceKeyIsStable(t *testing.T) {
	input := map[string]any{"wechat_owner_id": "owner", "conversation_id": "conv", "message_id": "msg", "forward_path": "0/1"}
	first := evidenceSourceKey(input)
	second := evidenceSourceKey(input)
	if first == "" || first != second {
		t.Fatalf("source key is not stable: %q %q", first, second)
	}
	if first == evidenceSourceKey(map[string]any{"wechat_owner_id": "owner", "conversation_id": "conv", "message_id": "msg", "forward_path": "0/2"}) {
		t.Fatalf("forward path must distinguish source keys")
	}
	left := evidenceSourceKey(map[string]any{"wechat_owner_id": "a", "conversation_id": "bc", "message_id": "d", "forward_path": "e"})
	right := evidenceSourceKey(map[string]any{"wechat_owner_id": "ab", "conversation_id": "c", "message_id": "d", "forward_path": "e"})
	if left == right {
		t.Fatalf("source key components must have unambiguous boundaries")
	}
}

func TestMatrixRecordRows(t *testing.T) {
	rows := matrixRecordRows(map[string]any{
		"fields":         []any{"event_id", "summary"},
		"record_id_list": []any{"rec_event"},
		"data":           []any{[]any{"event-1", "确认打样"}},
	})
	if len(rows) != 1 || stringValue(rows[0]["record_id"]) != "rec_event" {
		t.Fatalf("matrix rows did not preserve record identity: %#v", rows)
	}
	fields, _ := rows[0]["fields"].(map[string]any)
	if stringValue(fields["event_id"]) != "event-1" || stringValue(fields["summary"]) != "确认打样" {
		t.Fatalf("matrix columns were not mapped to field names: %#v", rows)
	}
}

func TestCanonicalFilteringAndStyleEventFallbackHelpers(t *testing.T) {
	rows := []map[string]any{
		{"fields": map[string]any{"event_id": "evt_old", "canonical_event_id": "evt_new", "record_state": "merged"}},
		{"fields": map[string]any{"event_id": "evt_new", "record_state": "active"}},
	}
	current := canonicalRows(rows, "event_id", "canonical_event_id")
	if len(current) != 1 || stringValue(current[0]["fields"].(map[string]any)["event_id"]) != "evt_new" {
		t.Fatalf("canonical rows = %#v", current)
	}
	filtered := filterRows(current, map[string]any{"filters": map[string]any{"event_id": "evt_new"}})
	if len(filtered) != 1 {
		t.Fatalf("event_id filter did not retain canonical event: %#v", filtered)
	}
	chain := []map[string]any{
		{"fields": map[string]any{"event_id": "evt_a", "canonical_event_id": "evt_b", "record_state": "merged"}},
		{"fields": map[string]any{"event_id": "evt_b", "canonical_event_id": "evt_c", "record_state": "merged"}},
		{"fields": map[string]any{"event_id": "evt_c", "record_state": "active"}},
	}
	if got := canonicalID(chain, "event_id", "canonical_event_id", "evt_a"); got != "evt_c" {
		t.Fatalf("canonical chain = %q, want evt_c", got)
	}
	if completedActionIndex("2:event.create") != 2 || completedActionIndex("running") != -1 {
		t.Fatalf("operation recovery step parsing failed")
	}
	items := []map[string]any{{"occurred_at": "2026-02-01"}, {"occurred_at": "2026-01-01"}, {"occurred_at": ""}}
	sortStyleEventItems(items)
	if stringValue(items[0]["occurred_at"]) != "2026-01-01" || stringValue(items[2]["occurred_at"]) != "" {
		t.Fatalf("style event ordering = %#v", items)
	}
	images := uniqueJSONValues([]any{map[string]any{"file_token": "file_a"}, map[string]any{"file_token": "file_a"}, "file_b"})
	if len(images) != 2 {
		t.Fatalf("representative image dedupe = %#v", images)
	}
}

func TestWorklineScopes(t *testing.T) {
	shortcuts := Shortcuts()
	if len(shortcuts) != 3 {
		t.Fatalf("Shortcuts() = %d, want 3", len(shortcuts))
	}
	for _, shortcut := range shortcuts {
		for _, scope := range append(shortcut.UserScopes, shortcut.BotScopes...) {
			if scope == "base:record:write" {
				t.Fatal("obsolete base:record:write scope is present")
			}
		}
	}
	apply := shortcuts[1]
	for _, scope := range []string{"base:record:create", "base:record:update"} {
		found := false
		for _, candidate := range apply.BotScopes {
			if candidate == scope {
				found = true
			}
		}
		if !found {
			t.Fatalf("apply bot scopes missing %q", scope)
		}
	}
	for _, scope := range []string{"base:app:create", "base:table:create", "base:field:create", "docs:document.media:upload", "docs:permission.member:create"} {
		found := false
		for _, candidate := range apply.ConditionalBotScopes {
			if candidate == scope {
				found = true
			}
		}
		if !found {
			t.Fatalf("apply conditional bot scopes missing %q", scope)
		}
	}
}

func TestPayloadAndFilterContract(t *testing.T) {
	if err := validateActionPayload("identity.upsert", map[string]any{}); err == nil {
		t.Fatal("identity without wechat_id or source_identity_key was accepted")
	}
	if err := validateActionPayload("identity.upsert", map[string]any{"source_identity_key": "forward:owner/message/hash", "identity_kind": "forward_hash"}); err != nil {
		t.Fatalf("forward identity payload rejected: %v", err)
	}
	if err := validateActionPayload("identity.upsert", map[string]any{"wechat_id": "wxid_one", "identity_kind": "unknown"}); err == nil {
		t.Fatal("unknown identity_kind was accepted")
	}
	if err := validateActionPayload("role_claim.upsert", map[string]any{"source_identity": "identity-forward", "function": "供应链", "scope_type": "conversation"}); err != nil {
		t.Fatalf("SourceIdentity role claim rejected: %v", err)
	}
	if err := validateActionPayload("role_claim.upsert", map[string]any{"person": "person-1", "source_identity": "identity-forward", "function": "供应链", "scope_type": "conversation"}); err == nil {
		t.Fatal("role claim accepted both person and source_identity")
	}
	if err := validateActionPayload("role_claim.upsert", map[string]any{"function": "供应链", "scope_type": "conversation"}); err == nil {
		t.Fatal("role claim accepted neither person nor source_identity")
	}
	if err := validateActionPayload("evidence.upsert", map[string]any{"source_key": "hash", "content_type": "text"}); err == nil {
		t.Fatal("Evidence without a source envelope was accepted")
	}
	if err := validateActionPayload("evidence.upsert", map[string]any{
		"source_key": "hash", "wechat_owner_id": "owner", "conversation_id": "chat", "message_id": "msg", "content_type": "text",
	}); err != nil {
		t.Fatalf("valid Evidence payload rejected: %v", err)
	}
	if err := validateActionPayload("evidence.upsert", map[string]any{
		"source_key": "video-hash", "wechat_owner_id": "owner", "conversation_id": "chat", "message_id": "video-msg", "content_type": "video",
	}); err != nil {
		t.Fatalf("video Evidence payload rejected: %v", err)
	}
	if err := validateActionPayload("style.create", map[string]any{"name": "候选款", "created_from_evidence_id": "evidence-1", "style_status": "candidate"}); err != nil {
		t.Fatalf("Evidence-backed candidate Style rejected: %v", err)
	}
	if err := validateActionPayload("style.create", map[string]any{"name": "缺少来源"}); err == nil {
		t.Fatal("Style without an Event or Evidence source was accepted")
	}
	if err := validateActionPayload("style.create", map[string]any{"name": "候选款", "created_from_evidence_id": "evidence-1", "style_status": "draft"}); err == nil {
		t.Fatal("unknown style_status was accepted")
	}
	if err := validateActionPayload("style.create", map[string]any{"name": "候选款", "created_from_event_id": "event-1", "link_status": "removed"}); err == nil {
		t.Fatal("style.create accepted a non-creation link_status")
	}
	if err := validateActionPayload("style_identifier.upsert", map[string]any{"style": "style-1", "issuer_or_scope": "factory-a", "identifier_kind": "factory_style_code", "value": "xf-001"}); err != nil {
		t.Fatalf("valid StyleIdentifier rejected: %v", err)
	}
	if err := validateActionPayload("style_identifier.upsert", map[string]any{"style": "style-1", "identifier_kind": "factory_style_code", "value": "xf-001"}); err == nil {
		t.Fatal("StyleIdentifier without issuer_or_scope was accepted")
	}
	styleLink := map[string]any{"event_id": "evt", "style_id": "sty", "status": "proposed"}
	if err := validateActionPayload("event_style.set", styleLink); err != nil {
		t.Fatalf("valid event_style alias payload rejected: %v", err)
	}
	if stringValue(styleLink["link_status"]) != "proposed" {
		t.Fatalf("event_style status alias was not normalized: %#v", styleLink)
	}
	if queryFilterFields("context")["style_id"] || !queryFilterFields("context")["source_key"] {
		t.Fatalf("context filters exceed the Evidence-scoped contract: %#v", queryFilterFields("context"))
	}
	if !queryFilterFields("event")["style_id"] || !queryFilterFields("style")["event_id"] {
		t.Fatal("relationship query filters are missing")
	}
	if !queryFilterFields("style")["style_status"] || !queryFilterFields("style")["created_from_evidence_id"] {
		t.Fatal("candidate Style query filters are missing")
	}
	for _, field := range []string{"source_identity_key", "identity_kind", "identity_scope"} {
		if !queryFilterFields("person")[field] {
			t.Fatalf("person query does not support SourceIdentities.%s", field)
		}
	}
}

func TestUnresolvedIdentityRoleClaimsRemainQueryable(t *testing.T) {
	result := map[string]any{
		"people": []map[string]any{},
		"sourceidentities": []map[string]any{
			{"fields": map[string]any{"identity_id": "identity-forward", "source_identity_key": "forward/hash", "mapping_status": "unresolved"}},
			{"fields": map[string]any{"identity_id": "identity-other", "source_identity_key": "forward/other", "mapping_status": "unresolved"}},
		},
		"roleclaims": []map[string]any{
			{"fields": map[string]any{"role_claim_id": "claim-forward", "source_identity": "identity-forward", "function": "供应链", "scope_type": "conversation"}},
			{"fields": map[string]any{"role_claim_id": "claim-other", "source_identity": "identity-other", "function": "物流", "scope_type": "conversation"}},
		},
	}
	req := map[string]any{"filters": map[string]any{"source_identity_key": "forward/hash"}}
	result["sourceidentities"] = filterRowsForTable(result["sourceidentities"].([]map[string]any), req, "SourceIdentities")
	applyRelationshipFilters(result, "person", req)
	identities := result["sourceidentities"].([]map[string]any)
	claims := result["roleclaims"].([]map[string]any)
	if len(identities) != 1 || stringValue(identities[0]["fields"].(map[string]any)["identity_id"]) != "identity-forward" {
		t.Fatalf("unresolved identity query = %#v", identities)
	}
	if len(claims) != 1 || stringValue(claims[0]["fields"].(map[string]any)["role_claim_id"]) != "claim-forward" {
		t.Fatalf("identity-scoped role claims were not returned: %#v", claims)
	}
}

func TestStyleIdentifierNormalizationAndIdempotencyKey(t *testing.T) {
	first := map[string]any{"style": "style-1", "issuer_or_scope": "factory-a", "identifier_kind": "factory_style_code", "value": " xf-268466 "}
	second := map[string]any{"style": "style-1", "issuer_or_scope": "factory-a", "identifier_kind": "factory_style_code", "normalized_value": "XF-268466"}
	normalizeStyleIdentifierPayload(first)
	normalizeStyleIdentifierPayload(second)
	if stringValue(first["normalized_value"]) != "XF-268466" || styleIdentifierID(first) != styleIdentifierID(second) {
		t.Fatalf("StyleIdentifier key is not stable: %#v / %#v", first, second)
	}
	designCode := map[string]any{"style": "style-1", "issuer_or_scope": "designer-a", "identifier_kind": "design_style_code", "normalized_value": "F27CM005F"}
	normalizeStyleIdentifierPayload(designCode)
	if styleIdentifierID(first) == styleIdentifierID(designCode) {
		t.Fatal("different identifier namespaces collapsed")
	}
}

func TestStyleIdentifierMigrationPreservesClaimAndEvidence(t *testing.T) {
	original := map[string]any{
		"identifier_id":       "old-id",
		"style":               "style-loser",
		"issuer_or_scope":     "factory-a",
		"identifier_kind":     "factory_style_code",
		"value":               " xf-268466 ",
		"supporting_evidence": []any{"evidence-1", "evidence-2"},
	}
	migrated := migratedStyleIdentifierFields(original, "style-winner")
	if stringValue(migrated["style"]) != "style-winner" || stringValue(migrated["value"]) != "xf-268466" || stringValue(migrated["normalized_value"]) != "XF-268466" {
		t.Fatalf("migrated identifier = %#v", migrated)
	}
	if stringValue(migrated["identifier_id"]) == "old-id" || styleIdentifierID(migrated) != stringValue(migrated["identifier_id"]) {
		t.Fatalf("migrated identifier ID was not re-keyed: %#v", migrated)
	}
	if got := stringValues(migrated["supporting_evidence"]); len(got) != 2 || got[0] != "evidence-1" || got[1] != "evidence-2" {
		t.Fatalf("supporting Evidence was not preserved: %#v", migrated)
	}
}

func TestCandidateStyleDefaultsAndFiltering(t *testing.T) {
	styleStatus, linkStatus := styleCreateStatuses(map[string]any{"style_status": "candidate"})
	if styleStatus != "candidate" || linkStatus != "confirmed" {
		t.Fatalf("candidate defaults = (%q, %q), want (candidate, confirmed)", styleStatus, linkStatus)
	}
	styleStatus, linkStatus = styleCreateStatuses(map[string]any{})
	if styleStatus != "confirmed" || linkStatus != "confirmed" {
		t.Fatalf("legacy defaults = (%q, %q), want (confirmed, confirmed)", styleStatus, linkStatus)
	}
	_, linkStatus = styleCreateStatuses(map[string]any{"style_status": "candidate", "link_status": "confirmed"})
	if linkStatus != "confirmed" {
		t.Fatalf("explicit link_status was not preserved: %q", linkStatus)
	}
	if link := styleCreateLinkFields("", "style-1", "proposed", "operation-1"); link != nil {
		t.Fatalf("Evidence-only candidate unexpectedly entered the Event timeline: %#v", link)
	}
	legacyLink := styleCreateLinkFields("event-1", "style-1", "confirmed", "operation-1")
	if stringValue(legacyLink["event"]) != "event-1" || stringValue(legacyLink["link_status"]) != "confirmed" {
		t.Fatalf("legacy Event-backed Style link changed: %#v", legacyLink)
	}

	rows := []map[string]any{
		{"fields": map[string]any{"style_id": "style-1", "style_status": "candidate", "created_from_evidence_id": "evidence-1"}},
		{"fields": map[string]any{"style_id": "style-2", "style_status": "confirmed", "created_from_event_id": "event-1"}},
	}
	filtered := filterRowsForTable(rows, map[string]any{"filters": map[string]any{"style_status": "candidate", "created_from_evidence_id": "evidence-1"}}, "Styles")
	if len(filtered) != 1 || stringValue(filtered[0]["fields"].(map[string]any)["style_id"]) != "style-1" {
		t.Fatalf("candidate Style filtering = %#v", filtered)
	}
}

func TestNewEventInvariantRequiresConfirmedStyleInSameOperation(t *testing.T) {
	event := func(payload map[string]any) map[string]any {
		return map[string]any{"type": "event.create", "payload": payload}
	}
	style := func(payload map[string]any) map[string]any {
		return map[string]any{"type": "style.create", "payload": payload}
	}

	if err := validateNewEventInvariants([]any{event(map[string]any{"summary": "received", "expression_mode": "fact", "evidence_ids": []any{"ev-1"}})}); err == nil {
		t.Fatal("event without a confirmed Style link was accepted")
	}
	if err := validateNewEventInvariants([]any{event(map[string]any{"summary": "received", "expression_mode": "fact", "evidence_ids": []any{"ev-1"}, "style_ids": []any{"style-1"}})}); err != nil {
		t.Fatalf("event with direct Style link was rejected: %v", err)
	}
	if err := validateNewEventInvariants([]any{
		event(map[string]any{"event_id": "event-1", "summary": "received", "expression_mode": "fact", "evidence_ids": []any{"ev-1"}}),
		style(map[string]any{"style_id": "style-1", "name": "Blue sample", "style_status": "candidate", "created_from_event_id": "event-1"}),
	}); err != nil {
		t.Fatalf("Event-backed candidate Style should confirm its origin link: %v", err)
	}
	if err := validateNewEventInvariants([]any{map[string]any{"type": "event.split", "payload": map[string]any{
		"event_id": "event-old",
		"events":   []any{map[string]any{"summary": "fact", "expression_mode": "fact", "evidence_ids": []any{"ev-1"}}},
	}}}); err == nil {
		t.Fatal("split child without a confirmed Style link was accepted")
	}
}

func TestStyleMergePreservesWinnerConfirmationState(t *testing.T) {
	if got := mergedStyleStatus(map[string]any{"style_status": "candidate"}); got != "candidate" {
		t.Fatalf("candidate winner was silently upgraded: %q", got)
	}
	if got := mergedStyleStatus(map[string]any{"style_status": "confirmed"}); got != "confirmed" {
		t.Fatalf("confirmed winner was downgraded: %q", got)
	}
	if got := mergedStyleStatus(map[string]any{}); got != "confirmed" {
		t.Fatalf("legacy winner lost backward-compatible confirmed state: %q", got)
	}
}

func TestIdentityNormalizationSupportsManyAccountsAndAnonymousForwards(t *testing.T) {
	first := map[string]any{"wechat_id": " wxid_one ", "display_name": "同名", "person": "person-shared"}
	second := map[string]any{"wechat_id": "wxid_two", "display_name": "同名", "person": "person-shared"}
	firstKey, firstKind, err := normalizeIdentityPayload(first)
	if err != nil {
		t.Fatalf("normalize first WeChat identity: %v", err)
	}
	secondKey, secondKind, err := normalizeIdentityPayload(second)
	if err != nil {
		t.Fatalf("normalize second WeChat identity: %v", err)
	}
	if firstKey != "wxid_one" || secondKey != "wxid_two" || firstKind != "wechat_id" || secondKind != "wechat_id" {
		t.Fatalf("WeChat normalization = (%q, %q) / (%q, %q)", firstKey, firstKind, secondKey, secondKind)
	}
	if stringValue(first["source_identity_key"]) != "wxid_one" || stringValue(second["source_identity_key"]) != "wxid_two" {
		t.Fatalf("canonical WeChat source keys missing: %#v / %#v", first, second)
	}
	if mergePersonValue(first["person"]) != "person-shared" || mergePersonValue(second["person"]) != "person-shared" {
		t.Fatalf("multiple accounts did not retain the explicit shared Person link")
	}
	if identityID(firstKind, firstKey) == identityID(secondKind, secondKey) {
		t.Fatal("different WeChat accounts collapsed because their display names matched")
	}

	forwardKey := "owner-1/outer-message-9/forward-path-0/sha256-speaker"
	forward := map[string]any{
		"source_identity_key": forwardKey,
		"identity_kind":       "forward_hash",
		"identity_scope":      "owner-1/outer-message-9",
		"display_name":        "不是微信好友",
		"wechat_id":           "must-not-be-stored",
	}
	gotKey, gotKind, err := normalizeIdentityPayload(forward)
	if err != nil {
		t.Fatalf("normalize forwarded identity: %v", err)
	}
	if gotKey != forwardKey || gotKind != "forward_hash" {
		t.Fatalf("forward normalization = (%q, %q)", gotKey, gotKind)
	}
	if _, exists := forward["wechat_id"]; exists {
		t.Fatalf("forward_hash masqueraded as a WeChat ID: %#v", forward)
	}
	if _, exists := forward["person"]; exists {
		t.Fatalf("anonymous forward unexpectedly acquired a Person: %#v", forward)
	}
	firstForwardID, secondForwardID := identityID(gotKind, gotKey), identityID(gotKind, gotKey)
	if firstForwardID == "" || firstForwardID != secondForwardID {
		t.Fatalf("forward identity ID is not stable: %q / %q", firstForwardID, secondForwardID)
	}
}

func TestIdentityIDPreservesLegacyWechatAlgorithm(t *testing.T) {
	const wechatID = "wxid_legacy"
	want := hashBytes([]byte(strings.Join([]string{"wechat", wechatID}, "\x1f")))
	if got := identityID("wechat_id", wechatID); got != want {
		t.Fatalf("identityID(legacy WeChat) = %q, want %q", got, want)
	}
	key := "owner/outer/full-forward-hash"
	if first, second := identityID("forward_hash", key), identityID("forward_hash", key); first == "" || first != second {
		t.Fatalf("forward identity ID is not stable: %q / %q", first, second)
	}
}

func TestEvidenceUpsertCanBackfillSpeakerIdentity(t *testing.T) {
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", t.TempDir())
	cfg := &core.CliConfig{AppID: "test-app", AppSecret: "test-secret", Brand: core.BrandFeishu}
	factory, _, _, registry := cmdutil.TestFactory(t, cfg)
	runtime := common.TestNewRuntimeContextForAPI(context.Background(), &cobra.Command{Use: "workline-test"}, cfg, factory, core.AsBot)

	const recordsURL = "/open-apis/base/v3/bases/base-test/tables/tbl-evidence/records"
	registry.Register(&httpmock.Stub{Method: "GET", URL: recordsURL, Body: map[string]any{"code": 0, "data": map[string]any{"items": []any{}, "has_more": false}}})
	registry.Register(&httpmock.Stub{Method: "POST", URL: recordsURL, Body: map[string]any{"code": 0, "data": map[string]any{"record_id_list": []any{"rec-evidence"}}}})

	initial := map[string]any{
		"evidence_id":          "evidence-1",
		"source_key":           "source-1",
		"wechat_owner_id":      "owner-original",
		"conversation_id":      "conversation-original",
		"message_id":           "message-original",
		"forward_path":         "0/1",
		"source_time":          "2026-08-30T01:00:00Z",
		"raw_locator":          "raw-original",
		"content_type":         "text",
		"excerpt":              "original",
		"reply_to_evidence_id": "",
	}
	first, err := upsertEvidence(runtime, "base-test", "tbl-evidence", initial)
	if err != nil {
		t.Fatalf("initial evidence upsert: %v", err)
	}
	if first["reused"] != false {
		t.Fatalf("initial evidence unexpectedly reused: %#v", first)
	}

	registry.Register(&httpmock.Stub{
		Method: "GET",
		URL:    recordsURL,
		Body: map[string]any{"code": 0, "data": map[string]any{
			"items": []any{map[string]any{"record_id": "rec-evidence", "fields": map[string]any{
				"evidence_id": "evidence-1", "source_key": "source-1", "wechat_owner_id": "owner-original", "conversation_id": "conversation-original",
				"message_id": "message-original", "forward_path": "0/1", "source_time": "2026-08-30T01:00:00Z", "raw_locator": "raw-original",
			}}},
			"has_more": false,
		}},
	})
	registry.Register(&httpmock.Stub{
		Method: "GET",
		URL:    "/open-apis/base/v3/bases/base-test/tables",
		Body: map[string]any{"code": 0, "data": map[string]any{
			"items": []any{map[string]any{"table_id": "tbl-identities", "name": "SourceIdentities"}},
		}},
	})
	registry.Register(&httpmock.Stub{
		Method: "GET",
		URL:    "/open-apis/base/v3/bases/base-test/tables/tbl-identities/records",
		Body: map[string]any{"code": 0, "data": map[string]any{
			"items":    []any{map[string]any{"record_id": "rec-identity", "fields": map[string]any{"identity_id": "identity-1"}}},
			"has_more": false,
		}},
	})
	patch := &httpmock.Stub{Method: "PATCH", URL: recordsURL + "/rec-evidence", Body: map[string]any{"code": 0, "data": map[string]any{}}}
	registry.Register(patch)

	backfill := map[string]any{
		"source_key":       "source-1",
		"wechat_owner_id":  "owner-must-not-change",
		"conversation_id":  "conversation-must-not-change",
		"message_id":       "message-must-not-change",
		"forward_path":     "9/9",
		"source_time":      "2099-01-01T00:00:00Z",
		"raw_locator":      "raw-must-not-change",
		"speaker_identity": "identity-1",
		"excerpt":          "derived correction",
	}
	second, err := upsertEvidence(runtime, "base-test", "tbl-evidence", backfill)
	if err != nil {
		t.Fatalf("speaker identity backfill: %v", err)
	}
	if second["reused"] != true || stringValue(second["business_id"]) != "evidence-1" {
		t.Fatalf("backfill did not reuse the original evidence: %#v", second)
	}

	var body map[string]any
	if err := json.Unmarshal(patch.CapturedBody, &body); err != nil {
		t.Fatalf("decode update body: %v (%s)", err, patch.CapturedBody)
	}
	refs, _ := body["speaker_identity"].([]any)
	if len(refs) != 1 || stringValue(refs[0]) != "rec-identity" {
		t.Fatalf("speaker_identity was not backfilled as a native link: %#v", body)
	}
	for _, immutable := range []string{"source_key", "evidence_id", "wechat_owner_id", "conversation_id", "message_id", "forward_path", "source_time", "raw_locator"} {
		if _, exists := body[immutable]; exists {
			t.Fatalf("immutable evidence field %q leaked into update: %#v", immutable, body)
		}
	}
	if stringValue(body["excerpt"]) != "derived correction" {
		t.Fatalf("derived evidence correction missing from update: %#v", body)
	}
}

func TestBatchUpsertRecordsUsesBatchEndpointsAndReusesSnapshot(t *testing.T) {
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", t.TempDir())
	cfg := &core.CliConfig{AppID: "test-app", AppSecret: "test-secret", Brand: core.BrandFeishu}
	factory, _, _, registry := cmdutil.TestFactory(t, cfg)
	runtime := common.TestNewRuntimeContextForAPI(context.Background(), &cobra.Command{Use: "workline-test"}, cfg, factory, core.AsBot)
	knownTableIDs["People"] = "tbl-people"
	state := &applyReadState{loaded: map[string][]map[string]any{}, decoded: map[string]bool{}}
	applyStates.Store(runtime, state)
	defer applyStates.Delete(runtime)
	const recordsURL = "/open-apis/base/v3/bases/base-test/tables/tbl-people/records"
	registry.Register(&httpmock.Stub{Method: "GET", URL: recordsURL, Body: map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"record_id": "rec-existing", "fields": map[string]any{"person_id": "person-existing"}}}, "has_more": false}}})
	createStub := &httpmock.Stub{Method: "POST", URL: recordsURL + "/batch_create", Body: map[string]any{"code": 0, "data": map[string]any{"record_id_list": []any{"rec-new-1", "rec-new-2"}}}}
	registry.Register(createStub)
	updateStub := &httpmock.Stub{Method: "POST", URL: recordsURL + "/batch_update", Body: map[string]any{"code": 0, "data": map[string]any{}}}
	registry.Register(updateStub)

	newDescriptor := func(id string) *batchDescriptor {
		fields := map[string]any{"person_id": id, "name": id}
		return &batchDescriptor{table: "tbl-people", key: "person_id", businessKey: "person_id", fields: fields,
			match:  func(existing map[string]any) bool { return stringValue(existing["person_id"]) == id },
			update: func(_ map[string]any) map[string]any { return withoutField(fields, "person_id") }}
	}
	duplicate := newDescriptor("person-new-2")
	duplicate.fields["name"] = "newer-payload"
	descriptors := []*batchDescriptor{newDescriptor("person-existing"), newDescriptor("person-new-1"), newDescriptor("person-new-2"), duplicate}
	results, err := batchUpsertRecords(runtime, "base-test", descriptors)
	if err != nil {
		t.Fatalf("batch upsert: %v", err)
	}
	if len(results) != 4 || results[0]["reused"] != true || results[1]["reused"] != false || results[2]["record_id"] != results[3]["record_id"] {
		t.Fatalf("unexpected batch results: %#v", results)
	}
	var createBody map[string]any
	if err := json.Unmarshal(createStub.CapturedBody, &createBody); err != nil {
		t.Fatalf("decode batch create body: %v", err)
	}
	if records, ok := createBody["create_records"].([]any); !ok || len(records) != 2 {
		t.Fatalf("batch create records = %#v", createBody["create_records"])
	}
	if !strings.Contains(string(createStub.CapturedBody), "newer-payload") {
		t.Fatalf("later duplicate payload was discarded: %s", createStub.CapturedBody)
	}
	var updateBody map[string]any
	if err := json.Unmarshal(updateStub.CapturedBody, &updateBody); err != nil {
		t.Fatalf("decode batch update body: %v", err)
	}
	if records, ok := updateBody["update_records"].(map[string]any); !ok || len(records) != 1 || records["rec-existing"] == nil {
		t.Fatalf("batch update records = %#v", updateBody["update_records"])
	}
	if rows := state.loaded["tbl-people"]; len(rows) != 3 {
		t.Fatalf("snapshot was not updated after batch writes: %#v", rows)
	}
}

func TestExistingEventUpsertPersistsActorIdentityLinks(t *testing.T) {
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", t.TempDir())
	cfg := &core.CliConfig{AppID: "test-app", AppSecret: "test-secret", Brand: core.BrandFeishu}
	factory, _, _, registry := cmdutil.TestFactory(t, cfg)
	runtime := common.TestNewRuntimeContextForAPI(context.Background(), &cobra.Command{Use: "workline-test"}, cfg, factory, core.AsBot)
	state := &applyReadState{loaded: map[string][]map[string]any{}, decoded: map[string]bool{}}
	applyStates.Store(runtime, state)
	defer applyStates.Delete(runtime)

	oldEvents, oldIdentities := knownTableIDs["Events"], knownTableIDs["SourceIdentities"]
	knownTableIDs["Events"] = "tbl-events"
	knownTableIDs["SourceIdentities"] = "tbl-identities"
	defer func() {
		knownTableIDs["Events"] = oldEvents
		knownTableIDs["SourceIdentities"] = oldIdentities
	}()

	registry.Register(&httpmock.Stub{
		Method: "GET",
		URL:    "/open-apis/base/v3/bases/base-test/tables/tbl-events/records",
		Body: map[string]any{"code": 0, "data": map[string]any{
			"items":    []any{map[string]any{"record_id": "rec-event", "fields": map[string]any{"event_id": "evt-1", "summary": "original"}}},
			"has_more": false,
		}},
	})
	registry.Register(&httpmock.Stub{
		Method: "GET",
		URL:    "/open-apis/base/v3/bases/base-test/tables/tbl-identities/records",
		Body: map[string]any{"code": 0, "data": map[string]any{
			"items": []any{
				map[string]any{"record_id": "rec-identity-a", "fields": map[string]any{"identity_id": "sid-a"}},
				map[string]any{"record_id": "rec-identity-b", "fields": map[string]any{"identity_id": "sid-b"}},
			},
			"has_more": false,
		}},
	})
	update := &httpmock.Stub{
		Method: "POST",
		URL:    "/open-apis/base/v3/bases/base-test/tables/tbl-events/records/batch_update",
		Body:   map[string]any{"code": 0, "data": map[string]any{}},
	}
	registry.Register(update)

	fields := map[string]any{
		"event_id":         "evt-1",
		"summary":          "corrected",
		"actor_identities": []any{"sid-a", "sid-b"},
	}
	descriptor := idBatchDescriptor("tbl-events", "event_id", fields, "event.create")
	results, err := batchUpsertRecords(runtime, "base-test", []*batchDescriptor{descriptor})
	if err != nil {
		t.Fatalf("upsert existing event: %v", err)
	}
	if len(results) != 1 || results[0]["reused"] != true || results[0]["record_id"] != "rec-event" {
		t.Fatalf("existing event was not reused: %#v", results)
	}

	var body map[string]any
	if err := json.Unmarshal(update.CapturedBody, &body); err != nil {
		t.Fatalf("decode event batch update body: %v (%s)", err, update.CapturedBody)
	}
	updates, _ := body["update_records"].(map[string]any)
	eventUpdate, _ := updates["rec-event"].(map[string]any)
	refs, _ := eventUpdate["actor_identities"].([]any)
	if len(refs) != 2 || stringValue(refs[0]) != "rec-identity-a" || stringValue(refs[1]) != "rec-identity-b" {
		t.Fatalf("actor identities were not converted to native links: %#v", eventUpdate["actor_identities"])
	}
	if stringValue(eventUpdate["summary"]) != "corrected" {
		t.Fatalf("event correction missing from update: %#v", eventUpdate)
	}
}

func TestEventCreateSideEffectsAreAggregatedAndExistingLinksUpdate(t *testing.T) {
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", t.TempDir())
	cfg := &core.CliConfig{AppID: "test-app", AppSecret: "test-secret", Brand: core.BrandFeishu}
	factory, _, _, registry := cmdutil.TestFactory(t, cfg)
	runtime := common.TestNewRuntimeContextForAPI(context.Background(), &cobra.Command{Use: "workline-test"}, cfg, factory, core.AsBot)
	state := &applyReadState{loaded: map[string][]map[string]any{}, decoded: map[string]bool{}}
	applyStates.Store(runtime, state)
	defer applyStates.Delete(runtime)
	tables := map[string]tableInfo{
		"Events": {ID: "tbl-events"}, "Evidence": {ID: "tbl-evidence"},
		"EvidenceEventLinks": {ID: "tbl-evidence-event-links"}, "EventStyleLinks": {ID: "tbl-event-style-links"}, "EventRelations": {ID: "tbl-relations"},
	}
	for name, info := range tables {
		knownTableIDs[name] = info.ID
	}
	registry.Register(&httpmock.Stub{Method: "GET", URL: "/open-apis/base/v3/bases/base-test/tables/tbl-events/records", Body: map[string]any{"code": 0, "data": map[string]any{"items": []any{}, "has_more": false}}})
	registry.Register(&httpmock.Stub{Method: "POST", URL: "/open-apis/base/v3/bases/base-test/tables/tbl-events/records/batch_create", Body: map[string]any{"code": 0, "data": map[string]any{"record_id_list": []any{"rec-event-1", "rec-event-2"}}}})
	registry.Register(&httpmock.Stub{Method: "GET", URL: "/open-apis/base/v3/bases/base-test/tables/tbl-evidence/records", Body: map[string]any{"code": 0, "data": map[string]any{"items": []any{
		map[string]any{"record_id": "rec-evidence-1", "fields": map[string]any{"evidence_id": "ev1"}},
		map[string]any{"record_id": "rec-evidence-2", "fields": map[string]any{"evidence_id": "ev2"}},
		map[string]any{"record_id": "rec-evidence-3", "fields": map[string]any{"evidence_id": "ev3"}},
	}, "has_more": false}}})
	existingLinkID := hashBytes([]byte(strings.Join([]string{"ev1", "evt1", "supporting"}, "\x1f")))
	duplicateLinkID := hashBytes([]byte(strings.Join([]string{"ev1", "evt1", "confirming"}, "\x1f")))
	registry.Register(&httpmock.Stub{Method: "GET", URL: "/open-apis/base/v3/bases/base-test/tables/tbl-evidence-event-links/records", Body: map[string]any{"code": 0, "data": map[string]any{"items": []any{
		map[string]any{"record_id": "rec-link-a", "fields": map[string]any{"link_id": existingLinkID, "evidence": "ev1", "event": "evt1", "support_type": "supporting", "record_state": "active"}},
		map[string]any{"record_id": "rec-link-b", "fields": map[string]any{"link_id": duplicateLinkID, "evidence": "ev1", "event": "evt1", "support_type": "confirming", "record_state": "active"}},
	}, "has_more": false}}})
	linkCreate := &httpmock.Stub{Method: "POST", URL: "/open-apis/base/v3/bases/base-test/tables/tbl-evidence-event-links/records/batch_create", Body: map[string]any{"code": 0, "data": map[string]any{"record_id_list": []any{"rec-link-2", "rec-link-3"}}}}
	registry.Register(linkCreate)
	linkUpdate := &httpmock.Stub{Method: "POST", URL: "/open-apis/base/v3/bases/base-test/tables/tbl-evidence-event-links/records/batch_update", Body: map[string]any{"code": 0, "data": map[string]any{}}}
	registry.Register(linkUpdate)
	replayUpdate := &httpmock.Stub{Method: "POST", URL: "/open-apis/base/v3/bases/base-test/tables/tbl-evidence-event-links/records/batch_update", Body: map[string]any{"code": 0, "data": map[string]any{}}}
	registry.Register(replayUpdate)

	makeAction := func(eventID string, evidenceIDs []any) map[string]any {
		return map[string]any{"type": "event.create", "payload": map[string]any{"event_id": eventID, "summary": eventID, "expression_mode": "fact", "evidence_ids": evidenceIDs}}
	}
	actions := []map[string]any{makeAction("evt1", []any{"ev1", "ev2"}), makeAction("evt2", []any{"ev3"})}
	descriptors := make([]*batchDescriptor, 0, len(actions))
	for _, action := range actions {
		descriptor, ok, err := batchDescriptorFor(runtime, "base-test", tables, "op-test", action)
		if err != nil || !ok {
			t.Fatalf("event descriptor: ok=%v err=%v", ok, err)
		}
		descriptors = append(descriptors, descriptor)
	}
	if _, err := batchUpsertRecords(runtime, "base-test", descriptors); err != nil {
		t.Fatalf("batch event create: %v", err)
	}
	effects := []*batchDescriptor{}
	for _, descriptor := range descriptors {
		effects = append(effects, descriptor.sideEffects()...)
	}
	attach, ok, err := batchDescriptorFor(runtime, "base-test", tables, "op-test", map[string]any{"type": "event.attach_evidence", "payload": map[string]any{"event": "evt1", "evidence": "ev1", "support_type": "direct", "interpretation": "explicit correction"}})
	if err != nil || !ok {
		t.Fatalf("attach descriptor: ok=%v err=%v", ok, err)
	}
	effects = append(effects, attach)
	if _, err := batchUpsertRecords(runtime, "base-test", effects); err != nil {
		t.Fatalf("batch event links: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(linkCreate.CapturedBody, &body); err != nil {
		t.Fatalf("decode link create body: %v", err)
	}
	if records, ok := body["create_records"].([]any); !ok || len(records) != 2 {
		t.Fatalf("expected one link batch_create with two new links: %#v", body["create_records"])
	}
	if len(linkCreate.CapturedBodies) != 1 {
		t.Fatalf("expected exactly one link batch_create call, got %d", len(linkCreate.CapturedBodies))
	}
	var update map[string]any
	if err := json.Unmarshal(linkUpdate.CapturedBody, &update); err != nil {
		t.Fatalf("decode link update body: %v", err)
	}
	if records, ok := update["update_records"].(map[string]any); !ok || len(records) != 2 || records["rec-link-a"] == nil || records["rec-link-b"] == nil {
		t.Fatalf("expected canonical winner and removed loser in one batch update: %#v", update["update_records"])
	}
	updatedFields, _ := update["update_records"].(map[string]any)
	updated, _ := updatedFields["rec-link-a"].(map[string]any)
	if stringValue(updated["support_type"]) != "direct" || stringValue(updated["interpretation"]) != "explicit correction" {
		t.Fatalf("explicit attach did not win over default supporting: %#v", updated)
	}
	loser, _ := updatedFields["rec-link-b"].(map[string]any)
	if stringValue(loser["record_state"]) != "removed" {
		t.Fatalf("duplicate active link was not retired: %#v", loser)
	}
	if _, err := batchUpsertRecords(runtime, "base-test", effects); err != nil {
		t.Fatalf("replay event links: %v", err)
	}
	if len(linkCreate.CapturedBodies) != 1 {
		t.Fatalf("replay created a second link batch: %d calls", len(linkCreate.CapturedBodies))
	}
	if len(replayUpdate.CapturedBodies) != 1 {
		t.Fatalf("replay did not use one batch update: %d calls", len(replayUpdate.CapturedBodies))
	}
}
