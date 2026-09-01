package evidencecollect

import (
	"encoding/json"
	"testing"

	"github.com/larksuite/cli/internal/caseclient"
)

func TestCollectPreservesOccurrencesIdentitiesAndAttachmentCoordinates(t *testing.T) {
	messages := []Message{
		{
			Owner: "owner-1", Conversation: "chat-1", ConversationType: "group", ID: "m-1", SourceTime: "2026-09-01T10:00:00+08:00",
			Speaker: SourceIdentity{SourceKey: "wechat|forward=outer/a", DisplayName: "转发来源", WeChatID: ""}, Text: "两件一起寄出",
			ForwardPath: "outer/a", ReplyTo: "m-0", Quote: map[string]any{"message_id": "m-0"}, RawLocator: map[string]any{"row": 9},
			Attachments: []Attachment{{Ordinal: 0, Kind: "image", MIME: "image/jpeg", Name: "左右样衣.jpg", LocalPath: "media/left-right.jpg", ContentHash: "same-image", Coordinates: map[string]any{"x": 0.1, "y": 0.2, "width": 0.8, "height": 0.7}}},
		},
		{
			Owner: "owner-1", Conversation: "chat-1", ConversationType: "group", ID: "m-2", SourceTime: "2026-09-01T10:01:00+08:00",
			Speaker: SourceIdentity{SourceKey: "wechat|wechat_id=wx-factory", DisplayName: "工厂", WeChatID: "wx-factory"}, Text: "收到",
			ForwardPath: "", Attachments: []Attachment{{Ordinal: 0, Kind: "image", MIME: "image/jpeg", Name: "copy.jpg", LocalPath: "media/copy.jpg", ContentHash: "same-image"}},
		},
	}
	bundles, err := New(Options{}).CollectBundles(messages, Scope{Owner: "owner-1", Conversation: "chat-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 1 || len(bundles[0].Items) != 4 {
		t.Fatalf("bundles=%#v", bundles)
	}
	if bundles[0].Items[0].SourceKey == bundles[0].Items[2].SourceKey {
		t.Fatal("message occurrences must differ")
	}
	if bundles[0].Relations[0].Type != "attachment_of" || bundles[0].Relations[0].ToClientEvidenceKey != bundles[0].Items[0].ClientEvidenceKey {
		t.Fatalf("relations=%#v", bundles[0].Relations)
	}
	if bundles[0].Items[0].SpeakerSourceKey == "" || bundles[0].Items[0].SourceLocator["forward_path"] != "outer/a" {
		t.Fatalf("item=%#v", bundles[0].Items[0])
	}
	if bundles[0].Items[1].SourceLocator["coordinates"] == nil {
		t.Fatal("media coordinates were lost")
	}
	if bundles[0].Items[1].ImmutablePayload["text"] != "两件一起寄出" {
		t.Fatal("raw text was lost")
	}
	if bundles[0].Items[3].ImmutablePayload["same_content_as"] == nil {
		t.Fatal("duplicate content relation was not retained")
	}
}

func TestCollectDoesNotInferProductOrEventAndSplitsLongInputStably(t *testing.T) {
	messages := make([]Message, 5)
	for i := range messages {
		messages[i] = Message{Owner: "o", Conversation: "c", ID: string(rune('a' + i)), SourceTime: "2026-09-01T00:00:00Z", Text: "两件一起寄出"}
	}
	a := New(Options{MaxMessagesPerBundle: 2})
	one, err := a.CollectBundles(messages, Scope{Owner: "o", Conversation: "c"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := a.CollectBundles(messages, Scope{Owner: "o", Conversation: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 3 || len(two) != 3 {
		t.Fatalf("bundle count=%d/%d", len(one), len(two))
	}
	for i := range one {
		if one[i].Key != two[i].Key || one[i].Items[0].ClientEvidenceKey != two[i].Items[0].ClientEvidenceKey {
			t.Fatal("bundle splitting is not stable")
		}
		if _, ok := one[i].Items[0].ImmutablePayload["style"]; ok {
			t.Fatal("collector inferred style")
		}
		if _, ok := one[i].Items[0].ImmutablePayload["event"]; ok {
			t.Fatal("collector inferred event")
		}
	}
	var raw map[string]any
	b, _ := json.Marshal(one[0].Coverage)
	_ = json.Unmarshal(b, &raw)
	if raw["complete"] != true {
		t.Fatalf("coverage=%#v", raw)
	}
}

func TestDecodeReaderPayloadAcceptsMessagesEnvelopeAndMarksMediaFailure(t *testing.T) {
	input := []byte(`{"data":{"messages":[{"owner":"o","conversation_id":"c","message_id":"m","timestamp":"2026-09-01T00:00:00Z","from":{"display_name":"转发人"},"content":"hello","attachments":[{"type":"video","ordinal":1,"export_error":"not exported"}]}]}}`)
	messages, err := Decode(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "m" || messages[0].Attachments[0].ExportError != "not exported" {
		t.Fatalf("messages=%#v", messages)
	}
	bundles, err := New(Options{}).CollectBundles(messages, Scope{Owner: "o", Conversation: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles[0].Coverage["media_export_failures"].([]string)) != 1 {
		t.Fatalf("coverage=%#v", bundles[0].Coverage)
	}
}

func TestDecodeJSONLAndParticipantScopeUseSourceIdentityOnly(t *testing.T) {
	raw := []byte("{\"owner\":\"o\",\"conversation_id\":\"c\",\"message_id\":\"m1\",\"timestamp\":\"2026-09-01T00:00:00Z\",\"from\":{\"display_name\":\"没有ID\"}}\n" +
		"{\"owner\":\"o\",\"conversation_id\":\"c\",\"message_id\":\"m2\",\"timestamp\":\"2026-09-01T00:01:00Z\",\"from\":{\"wechat_id\":\"wx-2\",\"display_name\":\"工厂\"}}\n")
	messages, err := Decode(raw)
	if err != nil || len(messages) != 2 {
		t.Fatalf("decode=%#v err=%v", messages, err)
	}
	if messages[0].Speaker.SourceKey == "" || messages[0].Speaker.WeChatID != "" {
		t.Fatalf("missing-id identity=%#v", messages[0].Speaker)
	}
	bundles, err := New(Options{}).CollectBundles(messages, Scope{Owner: "o", Conversation: "c", ParticipantIDs: []string{"wx-2"}})
	if err != nil || len(bundles) != 1 || len(bundles[0].Items) != 1 {
		t.Fatalf("participant filter bundles=%#v err=%v", bundles, err)
	}
	if bundles[0].Coverage["participant_ids"].([]string)[0] != "wx-2" {
		t.Fatalf("coverage=%#v", bundles[0].Coverage)
	}
}

func TestDecodeAnalysisProjectionPreservesResourcesAndNestedForward(t *testing.T) {
	raw := []byte("{\"timestamp\":\"2026-09-01T00:00:00Z\",\"sender_identity\":\"wx-a\",\"sender_name\":\"Alice\",\"message_type\":\"forward\",\"text\":\"转发\",\"message_id\":\"outer\",\"conversation_identity\":\"chat\",\"forwarded_messages\":[{\"message_id\":\"inner\",\"sender_identity\":\"hash-b\",\"sender_name\":\"Bob\",\"message_type\":\"image\",\"resource_path\":\"C:/media/a.jpg\"}]}\n")
	messages, err := Decode(raw)
	if err != nil || len(messages) != 2 {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	if messages[1].ForwardPath != "root/0" || messages[1].Speaker.SourceKey != "hash-b" || len(messages[1].Attachments) != 1 || messages[1].Attachments[0].LocalPath != "C:/media/a.jpg" {
		t.Fatalf("nested=%#v", messages[1])
	}
}

var _ = caseclient.ContractVersion
