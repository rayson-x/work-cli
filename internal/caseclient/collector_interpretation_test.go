// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package caseclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func collectorFixture() (CollectorInterpretation, []EvidenceBundle) {
	bundles := []EvidenceBundle{{
		Coverage: map[string]any{"complete": true},
		Items: []EvidenceItem{{
			ClientEvidenceKey: "evidence-1",
			SourceKey:         "wechat|conversation=chat|message=m1",
			Kind:              "message",
			SourceLocator:     map[string]any{"message_id": "m1"},
			ImmutablePayload:  map[string]any{"text": "样衣已寄出"},
		}},
	}}
	packet := CollectorInterpretation{
		ContractVersion: CollectorInterpretationContractVersion,
		CollectorRunKey: "collector-page-1",
		Model:           "gpt-5.6-luna",
		PromptVersion:   "style-track-collector.v1",
		Coverage:        map[string]any{"messages": 1, "media_inspected": true},
		Episodes: []CollectorEpisode{{
			Key:          "episode-1",
			Summary:      "寄样",
			EvidenceRefs: []CollectorEvidenceRef{{SourceKey: "wechat|conversation=chat|message=m1"}},
		}},
		Hypotheses: []CollectorHypothesis{{
			Key:          "event-1",
			Statement:    "可能已寄出",
			Status:       "proposed",
			EvidenceRefs: []CollectorEvidenceRef{{ClientEvidenceKey: "evidence-1"}},
		}},
		Alternatives: []CollectorAlternative{{
			Key:          "alternative-1",
			Statement:    "也可能只是准备寄出",
			Status:       "proposed",
			EvidenceRefs: []CollectorEvidenceRef{{SourceKey: "wechat|conversation=chat|message=m1"}},
		}},
		EvidenceLinks: []CollectorEvidenceLink{{
			SourceKey:         "wechat|conversation=chat|message=m1",
			EvidenceRef:       "old-cloud-ref",
			ClientEvidenceKey: "evidence-1",
			Relation:          "supporting",
			Note:              "原始消息",
		}},
		MissingEvidence: []CollectorMissingEvidence{{
			Key:         "missing-photo",
			Description: "缺少包裹照片",
		}},
	}
	return packet, bundles
}

func TestSubmitCollectorInterpretationUsesCaseEnvelopeAndStableKey(t *testing.T) {
	packet, bundles := collectorFixture()
	var requestCount int
	var firstKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cases/case-1/collector-interpretations" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		requestCount++
		if requestCount == 1 {
			firstKey = r.Header.Get("Idempotency-Key")
		} else if r.Header.Get("Idempotency-Key") != firstKey {
			t.Fatalf("idempotency key changed: %q then %q", firstKey, r.Header.Get("Idempotency-Key"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["contract_version"] != ContractVersion || body["model"] != packet.Model || body["prompt_version"] != packet.PromptVersion {
			t.Fatalf("request envelope=%#v", body)
		}
		payload, ok := body["payload"].(map[string]any)
		if !ok || payload["episodes"] == nil || payload["hypotheses"] == nil || payload["missing_evidence"] == nil {
			t.Fatalf("request payload=%#v", body["payload"])
		}
		links, ok := body["evidence_links"].([]any)
		if !ok || len(links) != 1 {
			t.Fatalf("normalized evidence links=%#v", body["evidence_links"])
		}
		link := links[0].(map[string]any)
		if link["source_key"] != "wechat|conversation=chat|message=m1" || link["evidence_ref"] != nil || link["client_evidence_key"] != nil {
			t.Fatalf("normalized evidence link=%#v", link)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"collector_interpretation_ref":"collector-1","case_ref":"case-1","status":"proposed","disposition":"accepted"}`))
	}))
	defer server.Close()
	c := New(Options{BaseURL: server.URL, APIKey: "key", HTTP: server.Client(), StatePath: filepath.Join(t.TempDir(), "state.json"), MaxRetries: 0})
	first, err := c.SubmitCollectorInterpretation(context.Background(), "case-1", packet, bundles)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.SubmitCollectorInterpretation(context.Background(), "case-1", packet, bundles)
	if err != nil {
		t.Fatal(err)
	}
	if first.CollectorInterpretationRef != "collector-1" || second.CollectorInterpretationRef != first.CollectorInterpretationRef {
		t.Fatalf("receipts=%#v %#v", first, second)
	}
	if requestCount != 2 {
		t.Fatalf("request count=%d", requestCount)
	}
}

func TestValidateCollectorInterpretationRejectsCrossCollectionAndConfirmedCandidates(t *testing.T) {
	packet, bundles := collectorFixture()
	packet.Episodes[0].EvidenceRefs[0].SourceKey = "other-case/message-1"
	if err := ValidateCollectorInterpretation(packet, bundles); err == nil {
		t.Fatal("expected cross-collection reference error")
	}
	packet, bundles = collectorFixture()
	packet.Hypotheses[0].Status = "confirmed"
	if err := ValidateCollectorInterpretation(packet, bundles); err == nil {
		t.Fatal("expected confirmed candidate error")
	}
}

func TestSubmitCollectorInterpretationMapsClientEvidenceKeyWithoutNetworkOnMiss(t *testing.T) {
	packet, bundles := collectorFixture()
	packet.EvidenceLinks = []CollectorEvidenceLink{{ClientEvidenceKey: "evidence-1", Relation: "contextual"}}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		links := body["evidence_links"].([]any)
		link := links[0].(map[string]any)
		if link["source_key"] != "wechat|conversation=chat|message=m1" || link["client_evidence_key"] != nil {
			t.Fatalf("normalized client link=%#v", link)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"collector_interpretation_ref":"collector-1","case_ref":"case-1","status":"proposed"}`))
	}))
	defer server.Close()
	c := New(Options{BaseURL: server.URL, APIKey: "key", HTTP: server.Client(), StatePath: filepath.Join(t.TempDir(), "state.json")})
	if _, err := c.SubmitCollectorInterpretation(context.Background(), "case-1", packet, bundles); err != nil {
		t.Fatal("expected server response")
	}
	if requests != 1 {
		t.Fatalf("mapped client key requests=%d", requests)
	}
	packet.EvidenceLinks[0].ClientEvidenceKey = "not-in-bundles"
	requests = 0
	if _, err := c.SubmitCollectorInterpretation(context.Background(), "case-1", packet, bundles); err == nil {
		t.Fatal("expected local client key validation error")
	}
	if requests != 0 {
		t.Fatalf("unknown client key requests=%d", requests)
	}
}
