package k12

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const PrintSourceUnitSummary = "unit_summary"

// 单元资料、成功版本与生成尝试分别拥有稳定身份；PDF 字节由 PrintArtifact 持有。
type UnitSummaryDocument struct {
	DocumentID         string `json:"document_id"`
	OwnerID            string `json:"-"`
	AgentName          string `json:"agent"`
	Subject            string `json:"subject"`
	ScopeKey           string `json:"scope_key"`
	CurrentRevisionID  string `json:"current_revision_id,omitempty"`
	HeadVersion        int    `json:"head_version"`
	NextRequestSeq     int64  `json:"-"`
	AcceptedRequestSeq int64  `json:"-"`
	CreatedAt          int64  `json:"created_at"`
	UpdatedAt          int64  `json:"updated_at"`
}

type UnitSummarySource struct {
	Kind         string   `json:"kind,omitempty"`
	Ref          string   `json:"ref"`
	Origin       string   `json:"origin"`
	Label        string   `json:"label"`
	Locator      string   `json:"locator"`
	Page         string   `json:"page,omitempty"`
	PageCount    *int     `json:"page_count,omitempty"`
	DocumentID   string   `json:"document_id,omitempty"`
	ManifestID   string   `json:"manifest_id,omitempty"`
	Generation   int64    `json:"generation,omitempty"`
	SourceDigest string   `json:"source_digest"`
	SegmentRefs  []string `json:"segment_refs,omitempty"`
	Content      string   `json:"-"`
}

// 正文仅在服务端 checkpoint 中冻结，不从资料响应泄漏整册教材。
type UnitSummaryFrozenSource struct {
	UnitSummarySource
	Text string `json:"text"`
}

type UnitSummaryContext struct {
	BaseRevisionID  string                    `json:"base_revision_id,omitempty"`
	ProfileSnapshot WeeklyProfile             `json:"profile_snapshot"`
	ChildName       string                    `json:"child_name"`
	GradeTerm       string                    `json:"grade_term"`
	TextbookEdition string                    `json:"textbook_edition,omitempty"`
	Volume          string                    `json:"volume,omitempty"`
	Subject         string                    `json:"subject"`
	UnitID          string                    `json:"unit_id,omitempty"`
	UnitTitle       string                    `json:"unit_title"`
	UnitNumber      string                    `json:"unit_number,omitempty"`
	ProgressSource  string                    `json:"progress_source,omitempty"`
	Sources         []UnitSummaryFrozenSource `json:"sources"`
	Evidence        []UnitSummaryFrozenSource `json:"evidence"`
	Coverage        UnitSummaryCoverage       `json:"coverage"`
	ModelSnapshot   GradingModelSnapshot      `json:"model_snapshot"`
}

type UnitSummaryRequirements struct {
	Format       string `json:"format"`
	Instructions string `json:"instructions,omitempty"`
}

type UnitSummaryGoal struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	SourceRefs []string `json:"source_refs"`
}
type UnitSummaryKnowledge struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	BodyMD     string   `json:"body_md"`
	SourceRefs []string `json:"source_refs"`
}
type UnitSummaryParentGuide struct {
	Ask         string `json:"ask"`
	Explain     string `json:"explain"`
	Hint        string `json:"hint"`
	Alternative string `json:"alternative,omitempty"`
}
type UnitSummaryExample struct {
	ID              string                 `json:"id"`
	GoalIDs         []string               `json:"goal_ids"`
	Origin          string                 `json:"origin"`
	PromptMD        string                 `json:"prompt_md"`
	DemonstrationMD string                 `json:"demonstration_md"`
	ParentGuide     UnitSummaryParentGuide `json:"parent_guide"`
	SourceRefs      []string               `json:"source_refs"`
}
type UnitSummaryTransferCheck struct {
	ID         string `json:"id"`
	ExampleID  string `json:"example_id"`
	QuestionMD string `json:"question_md"`
}
type UnitSummaryReferenceExplanation struct {
	CheckID       string `json:"check_id"`
	ExplanationMD string `json:"explanation_md"`
}
type UnitSummaryPersonalGuidance struct {
	Text         string   `json:"text"`
	EvidenceRefs []string `json:"evidence_refs"`
}
type UnitSummaryCoverage struct {
	Level   string   `json:"level"`
	Covered []string `json:"covered"`
	Missing []string `json:"missing"`
}

type UnitSummaryContentV1 struct {
	SchemaVersion         int                               `json:"schema_version"`
	Subject               string                            `json:"subject"`
	Title                 string                            `json:"title"`
	ParentPlan            []string                          `json:"parent_plan"`
	Goals                 []UnitSummaryGoal                 `json:"goals"`
	Knowledge             []UnitSummaryKnowledge            `json:"knowledge"`
	Examples              []UnitSummaryExample              `json:"examples"`
	TransferChecks        []UnitSummaryTransferCheck        `json:"transfer_checks"`
	ReferenceExplanations []UnitSummaryReferenceExplanation `json:"reference_explanations"`
	CommonPitfalls        []string                          `json:"common_pitfalls"`
	PersonalGuidance      []UnitSummaryPersonalGuidance     `json:"personal_guidance"`
	Coverage              UnitSummaryCoverage               `json:"coverage"`
	Sources               []UnitSummarySource               `json:"sources"`
}

type UnitSummaryCandidate struct {
	CandidateID         string `json:"candidate_id"`
	ExpectedHeadVersion int    `json:"expected_head_version"`
	CandidateVersion    int    `json:"candidate_version"`
	GeneratedDate       string `json:"generated_date"`
	Timezone            string `json:"timezone"`
	Filename            string `json:"filename"`
	RenderContract      string `json:"render_contract"`
	CanonicalMarkdown   string `json:"canonical_markdown"`
	CanonicalDigest     string `json:"canonical_digest"`
	ArtifactID          string `json:"artifact_id,omitempty"`
	ByteDigest          string `json:"byte_digest,omitempty"`
}

type UnitSummaryAttempt struct {
	AttemptID            string                  `json:"id"`
	OwnerID              string                  `json:"-"`
	AgentName            string                  `json:"agent"`
	DocumentID           string                  `json:"document_id,omitempty"`
	SessionID            string                  `json:"session_id"`
	SourceMessageID      string                  `json:"source_message_id"`
	IdempotencyKey       string                  `json:"-"`
	RequestDigest        string                  `json:"-"`
	RequestSeq           int64                   `json:"-"`
	ReceivedSeq          int64                   `json:"-"`
	Intent               string                  `json:"intent"`
	State                string                  `json:"state"`
	Stage                string                  `json:"stage"`
	FinalUserText        string                  `json:"-"`
	RequestJSON          string                  `json:"-"`
	ResolutionJSON       string                  `json:"-"`
	ResolveInputJSON     string                  `json:"-"`
	Context              UnitSummaryContext      `json:"-"`
	Requirements         UnitSummaryRequirements `json:"requirements"`
	Content              *UnitSummaryContentV1   `json:"content,omitempty"`
	GeneratedContentJSON string                  `json:"-"`
	ContentDigest        string                  `json:"content_digest,omitempty"`
	InvocationRef        string                  `json:"-"`
	Candidate            *UnitSummaryCandidate   `json:"-"`
	ResultRevisionID     string                  `json:"revision_id,omitempty"`
	ArtifactID           string                  `json:"artifact_id,omitempty"`
	FailureKind          string                  `json:"failure_kind,omitempty"`
	FailureDetail        string                  `json:"failure_detail,omitempty"`
	Clarification        string                  `json:"clarification,omitempty"`
	LeaseOwner           string                  `json:"-"`
	LeaseEpoch           int                     `json:"-"`
	LeaseExpiresAt       int64                   `json:"-"`
	Revision             int                     `json:"revision"`
	CreatedAt            int64                   `json:"created_at"`
	UpdatedAt            int64                   `json:"updated_at"`
}

type UnitSummaryRevision struct {
	RevisionID        string                  `json:"revision_id"`
	DocumentID        string                  `json:"document_id"`
	AgentName         string                  `json:"agent"`
	Version           int                     `json:"version"`
	AttemptID         string                  `json:"attempt_id"`
	Content           UnitSummaryContentV1    `json:"content"`
	CanonicalMarkdown string                  `json:"canonical_markdown"`
	ContentDigest     string                  `json:"content_digest"`
	SourceSnapshot    []UnitSummarySource     `json:"source_snapshot"`
	PersonalSnapshot  []UnitSummarySource     `json:"personal_snapshot"`
	Requirements      UnitSummaryRequirements `json:"requirements"`
	Context           UnitSummaryContext      `json:"-"`
	GeneratedAt       int64                   `json:"generated_at"`
	GeneratedTimezone string                  `json:"generated_timezone"`
	GeneratedDate     string                  `json:"generated_date"`
	Filename          string                  `json:"filename"`
	ArtifactID        string                  `json:"artifact_id"`
	ByteDigest        string                  `json:"byte_digest"`
	ByteSize          int64                   `json:"byte_size"`
}

func UnitSummarySubjectAllowed(subject string) bool {
	switch subject {
	case "math", "chinese", "english", "science", "information_technology", "art":
		return true
	}
	return false
}
func UnitSummarySubjectLabel(subject string) string {
	switch subject {
	case "math":
		return "数学"
	case "chinese":
		return "语文"
	case "english":
		return "英语"
	case "science":
		return "科学"
	case "information_technology":
		return "信息科技"
	case "art":
		return "美术"
	}
	return subject
}

func UnitSummaryAttemptTerminal(state string) bool {
	switch state {
	case "succeeded", "reused", "superseded", "failed", "needs_input", "ignored":
		return true
	}
	return false
}

func UnitSummaryDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ValidateUnitSummaryContent 只约束本领域规范内容及真实引用，不影响普通辅导。
func ValidateUnitSummaryContent(c UnitSummaryContentV1, frozen UnitSummaryContext) error {
	if c.SchemaVersion != 1 || c.Subject != frozen.Subject || !UnitSummarySubjectAllowed(c.Subject) || strings.TrimSpace(c.Title) == "" || len(c.ParentPlan) == 0 || len(c.Knowledge) == 0 || len(c.Goals) == 0 || len(c.Examples) == 0 {
		return fmt.Errorf("unit summary content is incomplete or has an inconsistent subject")
	}
	if c.Coverage.Level != frozen.Coverage.Level || (c.Coverage.Level != "full" && c.Coverage.Level != "partial") {
		return fmt.Errorf("unit summary coverage differs from verified source coverage")
	}
	refs, evidence := map[string]bool{}, map[string]bool{}
	for _, s := range frozen.Sources {
		refs[s.Ref] = true
	}
	for _, s := range frozen.Evidence {
		refs[s.Ref] = true
		evidence[s.Ref] = true
	}
	checkRefs := func(items []string) bool {
		for _, ref := range items {
			if !refs[ref] {
				return false
			}
		}
		return true
	}
	goals, examples, checks, knowledge := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, g := range c.Goals {
		if g.ID == "" || goals[g.ID] || strings.TrimSpace(g.Text) == "" || len(g.SourceRefs) == 0 || !checkRefs(g.SourceRefs) {
			return fmt.Errorf("unit summary goal has invalid source references")
		}
		goals[g.ID] = true
	}
	for _, k := range c.Knowledge {
		validKind := false
		switch k.Kind {
		case "concept", "formula", "method", "vocabulary", "expression", "technique":
			validKind = true
		}
		if k.ID == "" || knowledge[k.ID] || !validKind || k.Title == "" || strings.TrimSpace(k.BodyMD) == "" || len(k.SourceRefs) == 0 || !checkRefs(k.SourceRefs) {
			return fmt.Errorf("unit summary knowledge has invalid source references")
		}
		knowledge[k.ID] = true
	}
	for _, e := range c.Examples {
		if e.ID == "" || examples[e.ID] || len(e.GoalIDs) == 0 || e.PromptMD == "" || e.DemonstrationMD == "" || e.ParentGuide.Ask == "" || e.ParentGuide.Explain == "" || e.ParentGuide.Hint == "" || (e.Origin != "source" && e.Origin != "ai_created") || !checkRefs(e.SourceRefs) || (e.Origin == "source" && len(e.SourceRefs) == 0) {
			return fmt.Errorf("unit summary example or parent guide is incomplete")
		}
		for _, g := range e.GoalIDs {
			if !goals[g] {
				return fmt.Errorf("unit summary example references an unknown goal")
			}
		}
		examples[e.ID] = true
	}
	for _, t := range c.TransferChecks {
		if t.ID == "" || checks[t.ID] || !examples[t.ExampleID] || t.QuestionMD == "" {
			return fmt.Errorf("unit summary transfer check has an invalid example")
		}
		checks[t.ID] = true
	}
	if len(checks) == 0 {
		return fmt.Errorf("unit summary transfer checks are required")
	}
	explained := map[string]bool{}
	for _, r := range c.ReferenceExplanations {
		if !checks[r.CheckID] || explained[r.CheckID] || r.ExplanationMD == "" {
			return fmt.Errorf("unit summary reference explanation is invalid")
		}
		explained[r.CheckID] = true
	}
	if len(explained) != len(checks) {
		return fmt.Errorf("unit summary reference explanations do not cover checks")
	}
	for _, p := range c.PersonalGuidance {
		if p.Text == "" || len(p.EvidenceRefs) == 0 {
			return fmt.Errorf("personal guidance requires student work evidence")
		}
		for _, ref := range p.EvidenceRefs {
			if !evidence[ref] {
				return fmt.Errorf("personal guidance references unavailable student evidence")
			}
		}
	}
	for _, s := range c.Sources {
		if !refs[s.Ref] {
			return fmt.Errorf("unit summary source was not read")
		}
	}
	return nil
}
