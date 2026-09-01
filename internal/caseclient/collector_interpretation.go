// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package caseclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// CollectorInterpretationContractVersion identifies the local, evidence-grounded
// interpretation packet. It is deliberately separate from the cloud canonical
// interpretation schema: candidates are submitted as proposed context only.
// The collector packet is carried by the case.v1 HTTP envelope. The packet's
// own shape is versioned by the typed DTO and stored in payload; keeping the
// common envelope at case.v1 lets the Case API apply its normal auth and
// idempotency contract.
const CollectorInterpretationContractVersion = ContractVersion

type CollectorEvidenceRef struct {
	SourceKey         string `json:"source_key,omitempty"`
	EvidenceRef       string `json:"evidence_ref,omitempty"`
	ClientEvidenceKey string `json:"client_evidence_key,omitempty"`
}

type CollectorEvidenceLink struct {
	SourceKey         string `json:"source_key,omitempty"`
	EvidenceRef       string `json:"evidence_ref,omitempty"`
	ClientEvidenceKey string `json:"client_evidence_key,omitempty"`
	// EvidenceAnchorID is populated only when a caller already has a cloud
	// anchor. Local collection normally uses source_key/evidence_ref instead;
	// those remain in payload for server-side resolution.
	EvidenceAnchorID string `json:"evidence_anchor_id,omitempty"`
	Relation         string `json:"relation"`
	Note             string `json:"note,omitempty"`
}

type CollectorEpisode struct {
	Key          string                 `json:"key"`
	Summary      string                 `json:"summary,omitempty"`
	EvidenceRefs []CollectorEvidenceRef `json:"evidence_refs"`
}

type CollectorHypothesis struct {
	Key          string                 `json:"key"`
	Statement    string                 `json:"statement"`
	Status       string                 `json:"status"`
	EvidenceRefs []CollectorEvidenceRef `json:"evidence_refs"`
}

type CollectorAlternative struct {
	Key          string                 `json:"key"`
	Statement    string                 `json:"statement"`
	Status       string                 `json:"status"`
	EvidenceRefs []CollectorEvidenceRef `json:"evidence_refs"`
}

type CollectorMissingEvidence struct {
	Key          string                 `json:"key"`
	Description  string                 `json:"description"`
	EvidenceRefs []CollectorEvidenceRef `json:"evidence_refs,omitempty"`
}

// CollectorInterpretation is a bounded local interpretation packet. Its
// hypotheses and alternatives are candidates; the client never writes Style,
// Event, Person, or other canonical domain records.
type CollectorInterpretation struct {
	ContractVersion string                     `json:"contract_version"`
	CollectorRunKey string                     `json:"collector_run_key"`
	Model           string                     `json:"model"`
	PromptVersion   string                     `json:"prompt_version"`
	Coverage        map[string]any             `json:"coverage"`
	Episodes        []CollectorEpisode         `json:"episodes"`
	Hypotheses      []CollectorHypothesis      `json:"hypotheses"`
	Alternatives    []CollectorAlternative     `json:"alternatives"`
	EvidenceLinks   []CollectorEvidenceLink    `json:"evidence_links"`
	MissingEvidence []CollectorMissingEvidence `json:"missing_evidence"`
	Key             string                     `json:"-"`
}

type CollectorInterpretationResult struct {
	CollectorInterpretationRef string `json:"collector_interpretation_ref"`
	CaseRef                    string `json:"case_ref"`
	Status                     string `json:"status"`
	InferenceStatus            string `json:"inference_status"`
	Disposition                string `json:"disposition,omitempty"`
}

// ValidateCollectorInterpretation checks packet shape and that every evidence
// reference belongs to the just-collected bundles. This keeps malformed or
// cross-Case packets from causing a network side effect.
func ValidateCollectorInterpretation(packet CollectorInterpretation, bundles []EvidenceBundle) error {
	if packet.ContractVersion != CollectorInterpretationContractVersion {
		return fmt.Errorf("contract_version must be %q", CollectorInterpretationContractVersion)
	}
	if strings.TrimSpace(packet.CollectorRunKey) == "" || strings.TrimSpace(packet.Model) == "" || strings.TrimSpace(packet.PromptVersion) == "" {
		return fmt.Errorf("collector_run_key, model, and prompt_version are required")
	}
	if packet.Coverage == nil {
		return fmt.Errorf("coverage is required")
	}
	sourceKeys := make(map[string]struct{})
	evidenceKeys := make(map[string]struct{})
	for _, bundle := range bundles {
		for _, item := range bundle.Items {
			if item.SourceKey != "" {
				sourceKeys[item.SourceKey] = struct{}{}
			}
			if item.ClientEvidenceKey != "" {
				evidenceKeys[item.ClientEvidenceKey] = struct{}{}
			}
		}
	}
	checkRef := func(sourceKey, evidenceRef, clientEvidenceKey string) error {
		sourceKey = strings.TrimSpace(sourceKey)
		evidenceRef = strings.TrimSpace(evidenceRef)
		clientEvidenceKey = strings.TrimSpace(clientEvidenceKey)
		if sourceKey == "" && evidenceRef == "" && clientEvidenceKey == "" {
			return fmt.Errorf("evidence reference must include source_key, evidence_ref, or client_evidence_key")
		}
		if evidenceRef != "" && sourceKey == "" && clientEvidenceKey == "" {
			return fmt.Errorf("evidence_ref must be paired with source_key or client_evidence_key for local validation")
		}
		if sourceKey != "" {
			if _, ok := sourceKeys[sourceKey]; !ok {
				return fmt.Errorf("collector interpretation references evidence outside this collection: source_key %q", sourceKey)
			}
		}
		if clientEvidenceKey != "" {
			if _, ok := evidenceKeys[clientEvidenceKey]; !ok {
				return fmt.Errorf("collector interpretation references evidence outside this collection: client_evidence_key %q", clientEvidenceKey)
			}
		}
		return nil
	}
	checkRefs := func(refs []CollectorEvidenceRef) error {
		for _, ref := range refs {
			if err := checkRef(ref.SourceKey, ref.EvidenceRef, ref.ClientEvidenceKey); err != nil {
				return err
			}
		}
		return nil
	}
	for _, episode := range packet.Episodes {
		if strings.TrimSpace(episode.Key) == "" {
			return fmt.Errorf("episode key is required")
		}
		if err := checkRefs(episode.EvidenceRefs); err != nil {
			return err
		}
	}
	for _, hypothesis := range packet.Hypotheses {
		if strings.TrimSpace(hypothesis.Key) == "" || strings.TrimSpace(hypothesis.Statement) == "" {
			return fmt.Errorf("hypothesis key and statement are required")
		}
		if strings.EqualFold(strings.TrimSpace(hypothesis.Status), "confirmed") {
			return fmt.Errorf("collector hypotheses must remain candidates, not confirmed")
		}
		if err := checkRefs(hypothesis.EvidenceRefs); err != nil {
			return err
		}
	}
	for _, alternative := range packet.Alternatives {
		if strings.TrimSpace(alternative.Key) == "" || strings.TrimSpace(alternative.Statement) == "" {
			return fmt.Errorf("alternative key and statement are required")
		}
		if strings.EqualFold(strings.TrimSpace(alternative.Status), "confirmed") {
			return fmt.Errorf("collector alternatives must remain candidates, not confirmed")
		}
		if err := checkRefs(alternative.EvidenceRefs); err != nil {
			return err
		}
	}
	for _, link := range packet.EvidenceLinks {
		if link.Relation != "supporting" && link.Relation != "contradicting" && link.Relation != "contextual" {
			return fmt.Errorf("unsupported evidence link relation %q", link.Relation)
		}
		if err := checkRef(link.SourceKey, link.EvidenceRef, link.ClientEvidenceKey); err != nil {
			return err
		}
	}
	for _, missing := range packet.MissingEvidence {
		if strings.TrimSpace(missing.Key) == "" || strings.TrimSpace(missing.Description) == "" {
			return fmt.Errorf("missing evidence key and description are required")
		}
		if err := checkRefs(missing.EvidenceRefs); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) SubmitCollectorInterpretation(ctx context.Context, caseRef string, packet CollectorInterpretation, bundles []EvidenceBundle) (CollectorInterpretationResult, error) {
	if strings.TrimSpace(caseRef) == "" {
		return CollectorInterpretationResult{}, &Error{Operation: "collector-interpretation.submit", Status: 422, Code: "invalid_input", Message: "case_ref is required"}
	}
	if err := ValidateCollectorInterpretation(packet, bundles); err != nil {
		return CollectorInterpretationResult{}, &Error{Operation: "collector-interpretation.submit", Status: 422, Code: "invalid_input", Message: err.Error(), Cause: err}
	}
	key := packet.Key
	if key == "" {
		key = packet.CollectorRunKey
	}
	body := collectorInterpretationRequest(packet)
	if key == "" {
		key = stableKey("collector-interpretation:"+caseRef, body)
	}
	if err := c.reserve("collector-interpretation.submit", key, hashPayload(body)); err != nil {
		return CollectorInterpretationResult{}, err
	}
	var out CollectorInterpretationResult
	path := "/v1/cases/" + url.PathEscape(caseRef) + "/collector-interpretations"
	if err := c.jsonRequest(ctx, http.MethodPost, path, key, body, &out); err != nil {
		return CollectorInterpretationResult{}, err
	}
	if err := c.complete("collector-interpretation.submit", key, out.CollectorInterpretationRef); err != nil {
		return out, err
	}
	return out, nil
}

func collectorInterpretationRequest(packet CollectorInterpretation) map[string]any {
	payload := map[string]any{
		"contract_version":  packet.ContractVersion,
		"collector_run_key": packet.CollectorRunKey,
		"episodes":          packet.Episodes,
		"hypotheses":        packet.Hypotheses,
		"alternatives":      packet.Alternatives,
		"evidence_links":    packet.EvidenceLinks,
		"missing_evidence":  packet.MissingEvidence,
	}
	request := map[string]any{
		"contract_version":  ContractVersion,
		"collector_run_key": packet.CollectorRunKey,
		"model":             packet.Model,
		"prompt_version":    packet.PromptVersion,
		"coverage":          packet.Coverage,
		"payload":           payload,
	}
	if len(packet.EvidenceLinks) > 0 {
		// Keep source_key/evidence_ref links in the envelope as well as in the
		// payload. The Case API resolves them against the tenant's Evidence
		// anchors; client_evidence_key is explicitly a local-only fallback.
		request["evidence_links"] = packet.EvidenceLinks
	}
	return request
}

func DecodeCollectorInterpretation(raw []byte) (CollectorInterpretation, error) {
	var packet CollectorInterpretation
	if err := json.Unmarshal(raw, &packet); err != nil {
		return CollectorInterpretation{}, fmt.Errorf("collector interpretation JSON is invalid: %w", err)
	}
	return packet, nil
}

// GetCollectorInterpretations returns the tenant-scoped provisional packets
// that the Case read model exposes for a Case.
func (c *Client) GetCollectorInterpretations(ctx context.Context, caseRef string) (map[string]any, error) {
	if strings.TrimSpace(caseRef) == "" {
		return nil, &Error{Operation: "collector-interpretation.get", Status: 422, Code: "invalid_input", Message: "case_ref is required"}
	}
	var out map[string]any
	err := c.jsonRequest(ctx, http.MethodGet, "/v1/cases/"+url.PathEscape(caseRef)+"/collector-interpretations", "", nil, &out)
	return out, err
}
