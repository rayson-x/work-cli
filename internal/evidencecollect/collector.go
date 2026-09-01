// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

// Package evidencecollect adapts bounded local WeChat output into immutable
// source Evidence. It deliberately does not infer products, people, styles,
// responsibilities, or events.
package evidencecollect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/larksuite/cli/internal/caseclient"
)

const CollectorVersion = "wechat-evidence.v1"

type SourceIdentity struct{ SourceKey, DisplayName, WeChatID string }
type Attachment struct {
	Ordinal                                               int `json:"ordinal"`
	Kind, MIME, Name, LocalPath, ContentHash, ExportError string
	Coordinates                                           map[string]any `json:"coordinates,omitempty"`
	RawLocator                                            map[string]any `json:"raw_locator,omitempty"`
}
type Message struct {
	Owner, Conversation, ConversationType, ID, SourceTime, Text, ForwardPath, ReplyTo string
	OwnerIdentity, Forwarder, Speaker                                                 SourceIdentity
	ForwardParentID                                                                   string
	ForwardParentPath                                                                 string
	StructuralContext                                                                 bool
	Quote                                                                             map[string]any
	RawLocator                                                                        map[string]any
	Attachments                                                                       []Attachment
}
type Scope struct {
	Owner            string   `json:"owner"`
	Conversation     string   `json:"conversation"`
	ConversationType string   `json:"conversation_type"`
	ParticipantIDs   []string `json:"participant_ids"`
	From             string   `json:"from"`
	To               string   `json:"to"`
	MessageIDs       []string `json:"message_ids"`
}
type Options struct{ MaxMessagesPerBundle int }
type Collector struct{ max int }

func New(o Options) *Collector {
	if o.MaxMessagesPerBundle <= 0 {
		o.MaxMessagesPerBundle = 500
	}
	return &Collector{max: o.MaxMessagesPerBundle}
}

func (c *Collector) CollectBundles(messages []Message, scope Scope) ([]caseclient.EvidenceBundle, error) {
	if strings.TrimSpace(scope.Owner) == "" || strings.TrimSpace(scope.Conversation) == "" {
		return nil, fmt.Errorf("owner and conversation are required")
	}
	from, err := parseSourceTime(scope.From)
	if err != nil {
		return nil, fmt.Errorf("invalid scope from time: %w", err)
	}
	to, err := parseSourceTime(scope.To)
	if err != nil {
		return nil, fmt.Errorf("invalid scope to time: %w", err)
	}
	normalized := make([]Message, 0, len(messages))
	wanted := map[string]bool{}
	for _, id := range scope.MessageIDs {
		wanted[id] = true
	}
	for _, m := range messages {
		if m.Owner == "" {
			m.Owner = scope.Owner
		}
		if m.Conversation == "" {
			m.Conversation = scope.Conversation
		}
		if m.OwnerIdentity.SourceKey == "" || m.OwnerIdentity.SourceKey == "wechat|owner=" {
			m.OwnerIdentity.SourceKey = "wechat|owner=" + m.Owner
		}
		if m.Owner != scope.Owner || m.Conversation != scope.Conversation {
			continue
		}
		_, timeErr := parseSourceTime(m.SourceTime)
		if timeErr != nil {
			return nil, fmt.Errorf("invalid message %s time: %w", m.ID, timeErr)
		}
		normalized = append(normalized, m)
	}
	selected := make([]bool, len(normalized))
	for index, m := range normalized {
		when, _ := parseSourceTime(m.SourceTime)
		selected[index] = (len(wanted) == 0 || wanted[m.ID]) && (len(scope.ParticipantIDs) == 0 || participantMatch(m.Speaker, scope.ParticipantIDs)) && (from == nil || when != nil && !when.Before(*from)) && (to == nil || when != nil && !when.After(*to))
	}
	if len(scope.ParticipantIDs) > 0 || len(wanted) > 0 {
		changed := true
		for changed {
			changed = false
			for index, m := range normalized {
				if !selected[index] || m.ForwardParentID == "" {
					continue
				}
				for parentIndex, parent := range normalized {
					if selected[parentIndex] || parent.ID != m.ForwardParentID || parent.ForwardPathOrRoot() != m.ForwardParentPath {
						continue
					}
					selected[parentIndex] = true
					normalized[parentIndex].StructuralContext = true
					changed = true
				}
			}
		}
	}
	filtered := make([]Message, 0, len(normalized))
	for index, m := range normalized {
		if selected[index] {
			filtered = append(filtered, m)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		left, _ := parseSourceTime(filtered[i].SourceTime)
		right, _ := parseSourceTime(filtered[j].SourceTime)
		if left == nil || right == nil {
			if filtered[i].SourceTime == filtered[j].SourceTime {
				return filtered[i].ID < filtered[j].ID
			}
			return filtered[i].SourceTime < filtered[j].SourceTime
		}
		if left.Equal(*right) {
			return filtered[i].ID < filtered[j].ID
		}
		return left.Before(*right)
	})
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no messages match requested scope")
	}
	var out []caseclient.EvidenceBundle
	for start := 0; start < len(filtered); start += c.max {
		end := start + c.max
		if end > len(filtered) {
			end = len(filtered)
		}
		b := c.bundle(filtered[start:end], scope, start/c.max)
		out = append(out, b)
	}
	return out, nil
}
func (c *Collector) bundle(messages []Message, scope Scope, index int) caseclient.EvidenceBundle {
	items := make([]caseclient.EvidenceItem, 0)
	relations := make([]caseclient.EvidenceRelation, 0)
	failures := []string{}
	seen := map[string]string{}
	messageKeys := map[string]string{}
	for _, m := range messages {
		parent := makeItem(m, -1)
		messageKeys[m.Owner+"\x1f"+m.Conversation+"\x1f"+m.ID+"\x1f"+m.ForwardPathOrRoot()] = parent.ClientEvidenceKey
	}
	for _, m := range messages {
		parent := makeItem(m, -1)
		items = append(items, parent)
		for _, a := range m.Attachments {
			child := makeItem(m, a.Ordinal)
			child.Kind = attachmentKind(a.Kind)
			child.MediaRef = ""
			child.ContentHash = a.ContentHash
			child.ImmutablePayload["attachment_kind"] = a.Kind
			child.ImmutablePayload["mime_type"] = a.MIME
			child.ImmutablePayload["filename"] = a.Name
			child.ImmutablePayload["local_path"] = a.LocalPath
			child.ImmutablePayload["export_error"] = a.ExportError
			child.SourceLocator["attachment_ordinal"] = a.Ordinal
			child.SourceLocator["coordinates"] = a.Coordinates
			child.SourceLocator["attachment_locator"] = a.RawLocator
			items = append(items, child)
			relations = append(relations, caseclient.EvidenceRelation{FromClientEvidenceKey: child.ClientEvidenceKey, ToClientEvidenceKey: parent.ClientEvidenceKey, Type: "attachment_of"})
			if a.ExportError != "" {
				failures = append(failures, child.SourceKey)
			}
			if a.ContentHash != "" {
				if prior := seen[a.ContentHash]; prior != "" {
					child.ImmutablePayload["same_content_as"] = prior
					relations = append(relations, caseclient.EvidenceRelation{FromClientEvidenceKey: child.ClientEvidenceKey, ToClientEvidenceKey: prior, Type: "same_content_as"})
				} else {
					seen[a.ContentHash] = child.ClientEvidenceKey
				}
			}
		}
		if m.ReplyTo != "" {
			if target := findMessageKey(messages, m.Owner, m.Conversation, m.ReplyTo); target != "" {
				relations = append(relations, caseclient.EvidenceRelation{FromClientEvidenceKey: parent.ClientEvidenceKey, ToClientEvidenceKey: target, Type: "reply_to"})
			}
		}
		if m.Quote != nil {
			if quoted := stringField(m.Quote, "message_id", "id"); quoted != "" {
				if target := findMessageKey(messages, m.Owner, m.Conversation, quoted); target != "" {
					relations = append(relations, caseclient.EvidenceRelation{FromClientEvidenceKey: parent.ClientEvidenceKey, ToClientEvidenceKey: target, Type: "quotes"})
				}
			}
		}
		if m.ForwardParentID != "" {
			if target := messageKeys[m.Owner+"\x1f"+m.Conversation+"\x1f"+m.ForwardParentID+"\x1f"+m.ForwardParentPath]; target != "" {
				relations = append(relations, caseclient.EvidenceRelation{FromClientEvidenceKey: parent.ClientEvidenceKey, ToClientEvidenceKey: target, Type: "forward_contains"})
			}
		}
	}
	coverage := map[string]any{"collector_version": CollectorVersion, "platform": "wechat", "owner": scope.Owner, "conversation": scope.Conversation, "conversation_type": scope.ConversationType, "participant_ids": scope.ParticipantIDs, "requested_from": scope.From, "requested_to": scope.To, "requested_message_ids": scope.MessageIDs, "message_start": messages[0].ID, "message_end": messages[len(messages)-1].ID, "source_time_start": messages[0].SourceTime, "source_time_end": messages[len(messages)-1].SourceTime, "complete": true, "missing_reasons": []string{}, "media_export_failures": failures, "media_complete": len(failures) == 0, "bundle_index": index}
	body := map[string]any{"coverage": coverage, "items": items, "relations": relations}
	key := "wechat-bundle:" + hash(body)
	return caseclient.EvidenceBundle{Coverage: coverage, CollectionStartedAt: messages[0].SourceTime, CollectionEndedAt: messages[len(messages)-1].SourceTime, Items: items, Relations: relations, Key: key}
}
func makeItem(m Message, ordinal int) caseclient.EvidenceItem {
	forward := m.ForwardPath
	if forward == "" {
		forward = "root"
	}
	ord := "message"
	if ordinal >= 0 {
		ord = strconv.Itoa(ordinal)
	}
	source := stableSource(m.Owner, m.Conversation, m.ID, forward, ord)
	speaker := m.Speaker
	if speaker.SourceKey == "" {
		speaker.SourceKey = sourceIdentityKey(m.Owner, m.Conversation, forward, speaker)
	}
	locator := map[string]any{"platform": "wechat", "owner": m.Owner, "conversation_id": m.Conversation, "message_id": m.ID, "forward_path": forward, "attachment_ordinal": ordinal, "quote": m.Quote, "reply_to_message_id": m.ReplyTo, "raw": m.RawLocator}
	locator["account_owner"] = m.OwnerIdentity
	locator["forwarder"] = m.Forwarder
	locator["speaker"] = speaker
	locator["structural_context"] = m.StructuralContext
	payload := map[string]any{"text": m.Text, "account_owner": m.OwnerIdentity, "forwarder": m.Forwarder, "speaker": speaker, "quote": m.Quote, "reply_to_message_id": m.ReplyTo, "forward_path": forward, "forward_parent_message_id": m.ForwardParentID, "forward_parent_path": m.ForwardParentPath}
	payload["structural_context"] = m.StructuralContext
	return caseclient.EvidenceItem{ClientEvidenceKey: "evidence:" + hash(source), SourceKey: source, Kind: "message", SourceTime: m.SourceTime, SpeakerSourceKey: speaker.SourceKey, RawText: m.Text, SourceLocator: locator, ImmutablePayload: payload}
}
func sourceIdentityKey(owner, conversation, forward string, identity SourceIdentity) string {
	seed := identity.DisplayName
	if seed == "" {
		seed = "unknown"
	}
	return "wechat|owner=" + owner + "|conversation=" + conversation + "|forward=" + forward + "|speaker_hash=" + hash(seed)
}
func stableSource(owner, conversation, message, forward, ordinal string) string {
	v := url.Values{}
	v.Set("platform", "wechat")
	v.Set("owner", owner)
	v.Set("conversation", conversation)
	v.Set("message", message)
	v.Set("forward_path", forward)
	v.Set("attachment_ordinal", ordinal)
	return "wechat:" + v.Encode()
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func parseSourceTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("unsupported timestamp %q", value)
}
func participantMatch(identity SourceIdentity, wanted []string) bool {
	for _, candidate := range wanted {
		if candidate != "" && (candidate == identity.WeChatID || candidate == identity.SourceKey) {
			return true
		}
	}
	return false
}
func attachmentKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "image", "photo", "picture":
		return "image"
	case "video", "movie":
		return "video"
	case "audio", "voice":
		return "audio"
	default:
		return "file"
	}
}
func findMessageKey(messages []Message, owner, conversation, id string) string {
	for _, message := range messages {
		if message.Owner == owner && message.Conversation == conversation && message.ID == id {
			return "evidence:" + hash(stableSource(owner, conversation, id, func() string {
				if message.ForwardPath != "" {
					return message.ForwardPath
				}
				return "root"
			}(), "message"))
		}
	}
	return ""
}

func Decode(raw []byte) ([]Message, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		return decodeValue(v)
	}
	// `wechat history --format analysis` is JSONL. Decode each record without
	// losing line order; malformed lines are reported rather than skipped.
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	messages := make([]Message, 0, len(lines))
	for number, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("invalid WeChat JSONL line %d: %w", number+1, err)
		}
		decoded, err := decodeValue(record)
		if err != nil {
			return nil, fmt.Errorf("invalid WeChat JSONL line %d: %w", number+1, err)
		}
		messages = append(messages, decoded...)
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("wechat output contains no messages")
	}
	return messages, nil
}
func decodeValue(v any) ([]Message, error) {
	var arr []any
	switch x := v.(type) {
	case []any:
		arr = x
	case map[string]any:
		if _, hasMessageID := x["message_id"]; hasMessageID {
			m, ok := decodeMap(x)
			if !ok {
				return nil, fmt.Errorf("invalid message record")
			}
			return appendForwarded([]Message{m}, m, x, m.ForwardPathOrRoot()), nil
		}
		for _, k := range []string{"messages", "data", "items"} {
			if n, ok := x[k]; ok {
				if m, e := decodeValue(n); e == nil && len(m) > 0 {
					return m, nil
				}
			}
		}
		return nil, fmt.Errorf("wechat output does not contain messages")
	default:
		return nil, fmt.Errorf("wechat output must be JSON object or array")
	}
	out := make([]Message, 0, len(arr))
	for _, raw := range arr {
		record, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid message record")
		}
		m, ok := decodeMap(record)
		if !ok {
			return nil, fmt.Errorf("invalid message record")
		}
		out = append(out, m)
		out = appendForwarded(out, m, record, m.ForwardPathOrRoot())
	}
	return out, nil
}
func decodeMap(v map[string]any) (Message, bool) {
	m := Message{}
	m.Owner = stringField(v, "owner", "owner_id", "wechat_owner_id")
	if owner, ok := v["owner_identity"].(map[string]any); ok {
		m.OwnerIdentity = identity(owner)
	}
	if m.OwnerIdentity.SourceKey == "" {
		m.OwnerIdentity.SourceKey = "wechat|owner=" + m.Owner
	}
	m.Conversation = stringField(v, "conversation_id", "conversation", "chat_id")
	m.ID = stringField(v, "message_id", "id")
	m.SourceTime = stringField(v, "timestamp", "source_time", "time", "created_at")
	m.Text = stringField(v, "content", "text", "raw_text")
	if m.Text == "" {
		m.Text = stringField(v, "message_text")
	}
	if m.Conversation == "" {
		m.Conversation = stringField(v, "conversation_identity")
	}
	m.ForwardPath = stringField(v, "forward_path")
	m.ReplyTo = stringField(v, "reply_to_message_id", "reply_to")
	if s, ok := v["speaker"].(map[string]any); ok {
		m.Speaker = identity(s)
	} else if s, ok := v["from"].(map[string]any); ok {
		m.Speaker = identity(s)
	} else {
		m.Speaker.SourceKey = stringField(v, "speaker_source_key", "speaker_id", "sender_identity")
		m.Speaker.DisplayName = stringField(v, "speaker_name", "display_name", "sender_name")
		m.Speaker.WeChatID = stringField(v, "speaker_wechat_id", "wechat_id")
	}
	if s, ok := v["forwarder"].(map[string]any); ok {
		m.Forwarder = identity(s)
	} else if s, ok := v["forwarded_by"].(map[string]any); ok {
		m.Forwarder = identity(s)
	}
	if q, ok := v["quote"].(map[string]any); ok {
		m.Quote = q
	} else if rich, ok := v["rich_content"].(map[string]any); ok {
		if q, ok := rich["quote"].(map[string]any); ok {
			m.Quote = q
		}
	}
	if l, ok := v["raw_locator"].(map[string]any); ok {
		m.RawLocator = l
	}
	if a, ok := v["attachments"].([]any); ok {
		for i, x := range a {
			if am, ok := x.(map[string]any); ok {
				m.Attachments = append(m.Attachments, Attachment{Ordinal: intField(am, "ordinal", i), Kind: stringField(am, "type", "kind", "content_type"), MIME: stringField(am, "mime", "mime_type"), Name: stringField(am, "name", "filename"), LocalPath: stringField(am, "local_path", "path"), ContentHash: stringField(am, "content_hash", "hash"), ExportError: stringField(am, "export_error", "error"), Coordinates: mapField(am, "coordinates"), RawLocator: mapField(am, "raw_locator")})
			}
		}
	}
	if len(m.Attachments) == 0 {
		if resources, ok := v["resources"].([]any); ok {
			m.Attachments = decodeResources(resources)
		}
	}
	if len(m.Attachments) == 0 {
		if path := stringField(v, "resource_path"); path != "" {
			m.Attachments = []Attachment{{Ordinal: 0, Kind: stringField(v, "message_type", "type"), LocalPath: path}}
		}
		if status := stringField(v, "resource_status"); status != "" && len(m.Attachments) == 0 {
			m.Attachments = []Attachment{{Ordinal: 0, Kind: stringField(v, "message_type", "type"), ExportError: status}}
		}
	}
	if m.Speaker.SourceKey == "" {
		m.Speaker.SourceKey = sourceIdentityKey(m.Owner, m.Conversation, m.ForwardPathOrRoot(), m.Speaker)
	}
	return m, m.ID != ""
}
func appendForwarded(out []Message, parent Message, record map[string]any, path string) []Message {
	children, ok := record["forwarded_messages"].([]any)
	if !ok {
		if rich, yes := record["rich_content"].(map[string]any); yes {
			children, _ = rich["forwarded_messages"].([]any)
		}
	}
	for index, raw := range children {
		childRecord, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		child, ok := decodeMap(childRecord)
		if !ok {
			continue
		}
		if child.Owner == "" {
			child.Owner = parent.Owner
		}
		if child.Conversation == "" {
			child.Conversation = parent.Conversation
		}
		child.ForwardPath = path + "/" + strconv.Itoa(index)
		child.ForwardParentID = parent.ID
		child.ForwardParentPath = path
		out = append(out, child)
		out = appendForwarded(out, child, childRecord, child.ForwardPath)
	}
	return out
}
func decodeResources(resources []any) []Attachment {
	out := make([]Attachment, 0, len(resources))
	for index, raw := range resources {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Attachment{Ordinal: index, Kind: stringField(value, "kind", "type"), MIME: stringField(value, "mime", "mime_type"), Name: stringField(value, "name", "filename"), LocalPath: stringField(value, "filepath", "local_path", "path"), ContentHash: stringField(value, "content_hash", "hash"), ExportError: stringField(value, "error", "export_error")})
	}
	return out
}
func (m Message) ForwardPathOrRoot() string {
	if m.ForwardPath != "" {
		return m.ForwardPath
	}
	return "root"
}
func identity(v map[string]any) SourceIdentity {
	return SourceIdentity{SourceKey: stringField(v, "source_key", "source_identity_key", "id", "wechat_id"), DisplayName: stringField(v, "display_name", "name", "nickname"), WeChatID: stringField(v, "wechat_id", "id")}
}
func stringField(v map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := v[k].(string); ok {
			return s
		}
	}
	return ""
}
func mapField(v map[string]any, key string) map[string]any {
	if value, ok := v[key].(map[string]any); ok {
		return value
	}
	return nil
}
func intField(v map[string]any, key string, fallback int) int {
	if n, ok := v[key].(float64); ok {
		return int(n)
	}
	return fallback
}
