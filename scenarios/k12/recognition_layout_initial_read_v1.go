package k12

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const RecognitionLayoutManifestWithContentV1 = "manifest_with_content_v1"

// 首读正文只在内部结算返回，不进入公开计划控制面。
type RecognitionLayoutInitialCandidateV1 struct {
	CandidateID    string                                     `json:"candidate_id"`
	Classification RecognitionLayoutCandidateClassificationV2 `json:"classification"`
	ResultKind     RecognitionLayoutCandidateResultKindV2     `json:"result_kind,omitempty"`
	FirstReadJSON  json.RawMessage                            `json:"first_read_json,omitempty"`
	ResultJSON     json.RawMessage                            `json:"result_json,omitempty"`
}

type RecognitionLayoutInitialReadReceiptV1 struct {
	RecognitionLayoutInitialCandidateV1
	FirstReadDigest string `json:"first_read_digest,omitempty"`
	ResultDigest    string `json:"result_digest,omitempty"`
}

// RecognitionLayoutFirstReadDigestV1 绑定首读正文及其冻结来源，不等同于正文的裸摘要。
func RecognitionLayoutFirstReadDigestV1(parent string, settlement RecognitionLayoutInitialReadSettlementV1, candidate RecognitionLayoutInitialCandidateV1) (string, error) {
	raw, err := json.Marshal(struct {
		Contract string `json:"contract"`
		Parent   string `json:"parent"`
		Value    any    `json:"value"`
	}{"recognition_layout_first_read_v1", parent, struct {
		Plan, Source, SourceDigest, Candidate string
		FirstRead                             json.RawMessage
	}{settlement.PlanDigest, settlement.SourcePhysicalInvocationID, settlement.SourcePhysicalResultDigest, candidate.CandidateID, candidate.FirstReadJSON}})
	if err != nil {
		return "", err
	}
	return recognitionLayoutSHA256(raw), nil
}

// RecognitionLayoutCandidateResultDigestV2 保留候选结果的来源身份与规范 JSON 摘要合同。
func RecognitionLayoutCandidateResultDigestV2(parent string, settlement RecognitionLayoutPrimaryBatchSettlementV2, candidate RecognitionLayoutCandidateSettlementV2) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(candidate.ResultJSON))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return "", fmt.Errorf("%w: candidate result must be a JSON object", ErrRecognitionLayoutPlanInvalid)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: candidate result contains trailing JSON", ErrRecognitionLayoutPlanInvalid)
	}
	canonical, err := json.Marshal(object)
	if err != nil || !bytes.Equal(canonical, candidate.ResultJSON) {
		return "", fmt.Errorf("%w: candidate result JSON is not canonical", ErrRecognitionLayoutPlanInvalid)
	}
	encoded, err := json.Marshal(struct {
		Contract                   string                                 `json:"contract"`
		ParentInvocationID         string                                 `json:"parent_invocation_id"`
		PlanDigest                 string                                 `json:"plan_digest"`
		CandidateID                string                                 `json:"candidate_id"`
		SourcePhysicalInvocationID string                                 `json:"source_physical_invocation_id"`
		SourcePhysicalResultDigest string                                 `json:"source_physical_result_digest"`
		SourcePhysicalUnit         RecognitionPhysicalUnit                `json:"source_physical_unit"`
		ResultKind                 RecognitionLayoutCandidateResultKindV2 `json:"result_kind"`
		Result                     json.RawMessage                        `json:"result"`
	}{
		Contract:                   "recognition_layout_candidate_result_v2",
		ParentInvocationID:         parent,
		PlanDigest:                 settlement.PlanDigest,
		CandidateID:                candidate.CandidateID,
		SourcePhysicalInvocationID: settlement.SourcePhysicalInvocationID,
		SourcePhysicalResultDigest: settlement.SourcePhysicalResultDigest,
		SourcePhysicalUnit:         settlement.SourcePhysicalUnit,
		ResultKind:                 candidate.ResultKind,
		Result:                     canonical,
	})
	if err != nil {
		return "", fmt.Errorf("k12storage: encode candidate result digest: %w", err)
	}
	return recognitionLayoutSHA256(encoded), nil
}

type RecognitionLayoutInitialReadSettlementV1 struct {
	PlanDigest                 string                                 `json:"plan_digest"`
	SourcePhysicalInvocationID string                                 `json:"source_physical_invocation_id"`
	SourcePhysicalUnit         RecognitionPhysicalUnit                `json:"source_physical_unit"`
	SourcePhysicalResultDigest string                                 `json:"source_physical_result_digest"`
	Classification             RecognitionLayoutBatchClassificationV2 `json:"classification"`
	AmbiguityKind              RecognitionLayoutBatchAmbiguityKindV2  `json:"ambiguity_kind,omitempty"`
	Candidates                 []RecognitionLayoutInitialCandidateV1  `json:"candidates,omitempty"`
}

type RecognitionLayoutReviewMemberAuthorizationV1 struct {
	AuthorizationID     string `json:"authorization_id"`
	AuthorizationDigest string `json:"authorization_digest"`
	CandidateID         string `json:"candidate_id"`
	ReviewRound         int    `json:"review_round"`
}

type RecognitionLayoutInitialReadSettlementResultV1 struct {
	Classification       RecognitionLayoutBatchClassificationV2         `json:"classification"`
	AmbiguityKind        RecognitionLayoutBatchAmbiguityKindV2          `json:"ambiguity_kind,omitempty"`
	SettlementDigest     string                                         `json:"settlement_digest"`
	FirstReads           []RecognitionLayoutInitialReadReceiptV1        `json:"first_reads,omitempty"`
	FrozenResults        []RecognitionLayoutCandidateResultReceiptV2    `json:"frozen_results,omitempty"`
	ReviewAuthorizations []RecognitionLayoutReviewMemberAuthorizationV1 `json:"review_authorizations,omitempty"`
}

type RecognitionLayoutReviewBatchAuthorizationRequestV1 struct {
	PlanDigest        string                                         `json:"plan_digest"`
	PhysicalUnit      RecognitionPhysicalUnit                        `json:"physical_unit"`
	Members           []RecognitionLayoutReviewMemberAuthorizationV1 `json:"members"`
	InputDigest       string                                         `json:"input_digest"`
	ImageDigest       string                                         `json:"image_digest"`
	ImageWidth        int                                            `json:"image_width"`
	ImageHeight       int                                            `json:"image_height"`
	PromptDigest      string                                         `json:"prompt_digest"`
	RecognitionFormat string                                         `json:"recognition_format"`
}

type RecognitionLayoutReviewBatchAuthorizationV1 struct {
	RecognitionLayoutReviewBatchAuthorizationRequestV1
	AuthorizationID     string   `json:"authorization_id"`
	AuthorizationDigest string   `json:"authorization_digest"`
	OrderedTargetIDs    []string `json:"ordered_target_ids"`
	ExactSetDigest      string   `json:"exact_set_digest"`
	ReviewRound         int      `json:"review_round"`
}

type RecognitionLayoutReviewBatchSettlementV1 struct {
	PlanDigest                 string                                   `json:"plan_digest"`
	AuthorizationID            string                                   `json:"authorization_id"`
	AuthorizationDigest        string                                   `json:"authorization_digest"`
	SourcePhysicalInvocationID string                                   `json:"source_physical_invocation_id"`
	SourcePhysicalUnit         RecognitionPhysicalUnit                  `json:"source_physical_unit"`
	SourcePhysicalResultDigest string                                   `json:"source_physical_result_digest"`
	Classification             RecognitionLayoutBatchClassificationV2   `json:"classification"`
	AmbiguityKind              RecognitionLayoutBatchAmbiguityKindV2    `json:"ambiguity_kind,omitempty"`
	Candidates                 []RecognitionLayoutCandidateSettlementV2 `json:"candidates,omitempty"`
}

type RecognitionLayoutReviewBatchSettlementResultV1 struct {
	Classification         RecognitionLayoutBatchClassificationV2      `json:"classification"`
	SettlementDigest       string                                      `json:"settlement_digest"`
	FrozenResults          []RecognitionLayoutCandidateResultReceiptV2 `json:"frozen_results,omitempty"`
	UnresolvedCandidateIDs []string                                    `json:"unresolved_candidate_ids,omitempty"`
}

func RecognitionLayoutReviewUnitV1(index int) (RecognitionPhysicalUnit, error) {
	return recognitionLayoutPhysicalUnitV2("layout_review_batch_", index)
}

// 输入摘要同时绑定有序授权成员、图像几何与独立复读提示；不包含首读正文。
func RecognitionLayoutReviewBatchInputDigestV1(request RecognitionLayoutReviewBatchAuthorizationRequestV1) (string, error) {
	if request.RecognitionFormat != RecognitionLayoutCompactV4 || !validRecognitionLayoutSHA256(request.PlanDigest) || !validRecognitionLayoutSHA256(request.ImageDigest) || !validRecognitionLayoutSHA256(request.PromptDigest) || request.ImageWidth <= 0 || request.ImageHeight <= 0 || len(request.Members) < 1 || len(request.Members) > RecognitionLayoutBatchTargetLimitV2 {
		return "", fmt.Errorf("%w: invalid review batch input", ErrRecognitionLayoutPlanInvalid)
	}
	request.InputDigest = ""
	raw, err := json.Marshal(struct {
		Contract string `json:"contract"`
		RecognitionLayoutReviewBatchAuthorizationRequestV1
	}{"recognition_layout_review_batch_input_v1", request})
	if err != nil {
		return "", err
	}
	return recognitionLayoutSHA256(raw), nil
}

var initialReadLatexEscapeV1 = regexp.MustCompile(`\\(?:times|div|cdot|pm|mp|leq|geq|neq|le|ge|ne|approx|infty|pi|degree|sqrt|frac|text|right|mathrm|mathbf|mathit|mathsf|mathtt|operatorname)`)

func sanitizeInitialReadModelJSONV1(s string) string {
	var out strings.Builder
	out.Grow(len(s) + 16)
	inString := false
	for i := 0; i < len(s); i++ {
		char := s[i]
		if !inString {
			out.WriteByte(char)
			if char == '"' {
				inString = true
			}
			continue
		}
		if char == '"' {
			out.WriteByte(char)
			inString = false
			continue
		}
		if char != '\\' {
			out.WriteByte(char)
			continue
		}

		// \times、\text、\frac、\ne、\right 等以 JSON 的合法 \t/\f/\n/\r 开头；如果不先保护，
		// json.Unmarshal 会把它们吞成控制字符，字段级数学规范化已无法恢复。
		if match := initialReadLatexEscapeV1.FindStringIndex(s[i:]); match != nil && match[0] == 0 {
			end := i + match[1]
			// LaTeX 命令只由英文字母组成；后接数字时也必须保护反斜杠。
			hasLetterSuffix := end < len(s) && ((s[end] >= 'a' && s[end] <= 'z') || (s[end] >= 'A' && s[end] <= 'Z'))
			if !hasLetterSuffix {
				out.WriteByte('\\')
				out.WriteString(s[i:end])
				i = end - 1
				continue
			}
		}

		if i+1 >= len(s) {
			out.WriteByte('\\')
			continue
		}
		next := s[i+1]
		if strings.ContainsRune(`"\/bfnrtu`, rune(next)) {
			out.WriteByte('\\')
			out.WriteByte(next)
			i++
			continue
		}
		// 未知 \x 变成 JSON 字符串中的字面反斜杠：\\x。
		out.WriteString(`\\`)
	}
	return out.String()
}

// CanonicalRecognitionLayoutInitialReadEntriesV1 统一首读解析与持久来源对照的封套和转义语义。
func CanonicalRecognitionLayoutInitialReadEntriesV1(raw string) ([]json.RawMessage, error) {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("%w: initial read envelope missing", ErrRecognitionProtocolInvalid)
	}
	payload := []byte(sanitizeInitialReadModelJSONV1(raw[start : end+1]))
	var envelope map[string]json.RawMessage
	err := json.Unmarshal(payload, &envelope)
	if err != nil {
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			return nil, err
		}
		index := int(syntax.Offset) - 1
		if index <= 0 || index >= len(payload) || payload[index] != '}' {
			return nil, err
		}
		before, after := strings.TrimSpace(string(payload[:index])), strings.TrimSpace(string(payload[index+1:]))
		if !strings.HasSuffix(before, ",") || !strings.HasPrefix(after, ",") {
			return nil, err
		}
		remainder := strings.TrimSpace(after[1:])
		if !strings.HasPrefix(remainder, "{") {
			return nil, err
		}
		candidate := append(append([]byte(nil), payload[:index]...), remainder...)
		if json.Unmarshal(candidate, &envelope) != nil {
			return nil, err
		}
	}
	if len(envelope) != 1 {
		return nil, fmt.Errorf("%w: initial read envelope fields invalid", ErrRecognitionProtocolInvalid)
	}
	var entries []json.RawMessage
	if json.Unmarshal(envelope["targets"], &entries) != nil || len(entries) < 1 || len(entries) > 32 {
		return nil, fmt.Errorf("%w: initial read targets invalid", ErrRecognitionProtocolInvalid)
	}
	for i, entry := range entries {
		var object map[string]any
		decoder := json.NewDecoder(bytes.NewReader(entry))
		decoder.UseNumber()
		if decoder.Decode(&object) != nil || object == nil {
			return nil, fmt.Errorf("%w: initial read target invalid", ErrRecognitionProtocolInvalid)
		}
		entries[i], err = json.Marshal(object)
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}
