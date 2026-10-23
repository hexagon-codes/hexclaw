package k12

import "errors"

const (
	TeachingReportSchemaVersion  = 1
	TeachingReportContentVersion = "answer-first-v3"
)

var ErrTeachingReportInvalid = errors.New("teaching report is invalid")

type TeachingReportSourceKind string

const (
	TeachingReportSourceGrading   TeachingReportSourceKind = "grading"
	TeachingReportSourceTextSolve TeachingReportSourceKind = "text_solve"
)

// TeachingReportSource 只保存真实来源的精确引用，不创建任务或执行回执。
// 图片使用当前作业来源；文字使用实际已交付的最终结算引用，不伪造批改任务。
type TeachingReportSource struct {
	Kind     TeachingReportSourceKind `json:"kind"`
	SourceID string                   `json:"source_id"`
	Revision int                      `json:"revision"`
	Digest   string                   `json:"digest"`
}

// TeachingReportMetadata 来自本次任务快照，领域对象不读取当前配置或时钟。
type TeachingReportMetadata struct {
	Title            string `json:"title"`
	AgentDisplayName string `json:"agent_display_name"`
	GradeTerm        string `json:"grade_term"`
	Subject          string `json:"subject"`
	ReportDate       string `json:"report_date"`
	Timezone         string `json:"timezone"`
}

type TeachingReportAnswerState string

const (
	TeachingReportAnswerPresent     TeachingReportAnswerState = "present"
	TeachingReportAnswerBlank       TeachingReportAnswerState = "blank"
	TeachingReportAnswerUnreadable  TeachingReportAnswerState = "unreadable"
	TeachingReportAnswerUnknown     TeachingReportAnswerState = "unknown"
	TeachingReportAnswerNotProvided TeachingReportAnswerState = "not_provided"
)

// TeachingReportProblemSource 是调用方读回的题目快照，用于校对报告条目。
// 数组顺序就是冻结题序；共享父材料不作为可作答题放入该数组。
type TeachingReportProblemSource struct {
	ProblemID        string                    `json:"problem_id"`
	ParentProblemID  string                    `json:"parent_problem_id,omitempty"`
	AttemptID        string                    `json:"attempt_id,omitempty"`
	InputRevision    int                       `json:"input_revision"`
	InputDigest      string                    `json:"input_digest"`
	AssessmentDigest string                    `json:"assessment_digest,omitempty"`
	SolutionDigest   string                    `json:"solution_digest,omitempty"`
	AssessmentStatus GradingAssessmentStatus   `json:"assessment_status,omitempty"`
	AnswerState      TeachingReportAnswerState `json:"answer_state"`
	AnswerRaw        string                    `json:"answer_raw"`
	AnswerCanonical  string                    `json:"answer_canonical"`
	AnswerSource     *ProblemAnswerSource      `json:"answer_source,omitempty"`
}

// TeachingReportContentOrigin 将答案、步骤或解释绑定到同题的输入和解法。
// 引用一致只证明内部关系，不能替代用例从真实存储读取和核验来源。
type TeachingReportContentOrigin struct {
	ProblemID      string `json:"problem_id"`
	InputRevision  int    `json:"input_revision"`
	InputDigest    string `json:"input_digest"`
	SolutionDigest string `json:"solution_digest"`
}

// TeachingReportSourcePage 引用完整源图或同源批注图，不携带裁切后的替代图。
type TeachingReportSourcePage struct {
	AssetID      string `json:"asset_id"`
	AssetDigest  string `json:"asset_digest"`
	SourceDigest string `json:"source_digest"`
	MIME         string `json:"mime"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

// TeachingReportAssessment 保留原评估状态；缺少评价时状态为空且说明原因。
type TeachingReportAssessment struct {
	Status GradingAssessmentStatus `json:"status"`
	Reason string                  `json:"reason,omitempty"`
}

// TeachingReportAnswerSlot 仅由具有位置证据的作答投影构造；空白槽位没有答案内容。
// Text 与 LaTeX 同时存在时必须表达相同内容，投影可优先使用 LaTeX，不放独有解释。
type TeachingReportAnswerSlot struct {
	ID          string                    `json:"id"`
	Label       string                    `json:"label"`
	State       TeachingReportAnswerState `json:"state"`
	Text        string                    `json:"text"`
	LaTeX       string                    `json:"latex,omitempty"`
	EvidenceRef string                    `json:"evidence_ref"`
}

// TeachingReportOriginalAnswer 常驻于每题，原始和规范转写不能被答案或排版改写。
type TeachingReportOriginalAnswer struct {
	State         TeachingReportAnswerState  `json:"state"`
	RawText       string                     `json:"raw_text"`
	CanonicalText string                     `json:"canonical_text"`
	Slots         []TeachingReportAnswerSlot `json:"slots"`
}

type TeachingReportReferenceState string

const (
	TeachingReportReferenceAvailable   TeachingReportReferenceState = "available"
	TeachingReportReferenceConditional TeachingReportReferenceState = "conditional"
	TeachingReportReferenceUnavailable TeachingReportReferenceState = "unavailable"
)

// TeachingReportAnswer 表达一个所求量或填空；条件直接随该答案保留。
// Text 与 LaTeX 是相同内容的不同表现，至少提供一种，不在此推导数学真值。
type TeachingReportAnswer struct {
	ID         string                      `json:"id"`
	Label      string                      `json:"label,omitempty"`
	Text       string                      `json:"text"`
	LaTeX      string                      `json:"latex,omitempty"`
	Conditions []string                    `json:"conditions,omitempty"`
	Origin     TeachingReportContentOrigin `json:"origin"`
}

// TeachingReportCalculationStep 只承载已核对的必要列式，原因解释放在深度讲解中。
// Text 与 LaTeX 是同一内容的替代表现，投影可优先使用 LaTeX，不拼接独有文字。
type TeachingReportCalculationStep struct {
	ID     string                      `json:"id"`
	Text   string                      `json:"text"`
	LaTeX  string                      `json:"latex,omitempty"`
	Origin TeachingReportContentOrigin `json:"origin"`
}

// TeachingReportReferenceEvidence 记录既有核验事实，不提供模型可自填的 verified 开关。
type TeachingReportReferenceEvidence struct {
	Verdict            string `json:"verdict"`
	EvidenceType       string `json:"evidence_type"`
	ReceiptRef         string `json:"receipt_ref,omitempty"`
	SolutionDigest     string `json:"solution_digest,omitempty"`
	VerificationRunID  string `json:"verification_run_id,omitempty"`
	VerificationDigest string `json:"verification_digest,omitempty"`
}

// TeachingReportAnswerAnalysis 的答案数组先于必要步骤；内容缺失时保留明确原因。
type TeachingReportAnswerAnalysis struct {
	State             TeachingReportReferenceState    `json:"state"`
	UnavailableReason string                          `json:"unavailable_reason,omitempty"`
	Answers           []TeachingReportAnswer          `json:"answers"`
	Steps             []TeachingReportCalculationStep `json:"steps"`
	Evidence          TeachingReportReferenceEvidence `json:"evidence"`
}

// TeachingReportItem 的三个常驻对象不会因缺少内容而被序列化省略。
type TeachingReportItem struct {
	ProblemID      string                       `json:"problem_id"`
	DisplayLabel   string                       `json:"display_label"`
	ShortTitle     string                       `json:"short_title"`
	Assessment     TeachingReportAssessment     `json:"assessment"`
	AnswerAnalysis TeachingReportAnswerAnalysis `json:"answer_analysis"`
	OriginalAnswer TeachingReportOriginalAnswer `json:"original_answer"`
}

// TeachingReportExplanationPurpose 表达教学内容的用途，不定义字号、颜色或版式。
type TeachingReportExplanationPurpose string

const (
	TeachingReportExplanationSupplement TeachingReportExplanationPurpose = "supplement"
	TeachingReportExplanationPitfall    TeachingReportExplanationPurpose = "pitfall"
)

// TeachingReportExplanationBlock 可引用同一讲解组内多个题目的同源解法。
// Purpose 只适用于文字块，空值表示普通解释。
type TeachingReportExplanationBlock struct {
	ID      string                           `json:"id"`
	Text    string                           `json:"text"`
	LaTeX   string                           `json:"latex,omitempty"`
	Table   *TeachingReportTable             `json:"table,omitempty"`
	Purpose TeachingReportExplanationPurpose `json:"purpose,omitempty"`
	Origins []TeachingReportContentOrigin    `json:"origins"`
}

// TeachingReportTableCell 仅承载文字或公式，不接收渲染器源码。
type TeachingReportTableCell struct {
	Text  string `json:"text"`
	LaTeX string `json:"latex,omitempty"`
}

// TeachingReportTable 保存教学比较表的列和行，整表来源由所属讲解块绑定。
type TeachingReportTable struct {
	Headers []TeachingReportTableCell   `json:"headers"`
	Rows    [][]TeachingReportTableCell `json:"rows"`
}

// TeachingReportMethodNote 只说明实际引用的讲解块，不把方法名当作应用证据。
type TeachingReportMethodNote struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	BlockIDs    []string `json:"block_ids"`
	EvidenceRef string   `json:"evidence_ref"`
}

type TeachingReportDeepSection struct {
	ID          string                           `json:"id"`
	Title       string                           `json:"title"`
	ProblemIDs  []string                         `json:"problem_ids"`
	Blocks      []TeachingReportExplanationBlock `json:"blocks"`
	Diagrams    []TeachingDiagramAsset           `json:"diagrams"`
	MethodNotes []TeachingReportMethodNote       `json:"method_notes"`
}

// TeachingReport 是已核对事实的报告快照，不含数据库、模型、渲染器或投递依赖。
// 对外共享前通过 NewTeachingReport 构造；需要独立修改时先 Clone。
type TeachingReport struct {
	SchemaVersion  int                           `json:"schema_version"`
	ContentVersion string                        `json:"content_version"`
	Source         TeachingReportSource          `json:"source"`
	Metadata       TeachingReportMetadata        `json:"metadata"`
	ProblemSources []TeachingReportProblemSource `json:"problem_sources"`
	SourcePages    []TeachingReportSourcePage    `json:"source_pages"`
	Items          []TeachingReportItem          `json:"items"`
	DeepSections   []TeachingReportDeepSection   `json:"deep_sections"`
}

// NewTeachingReport 校验引用和完整性并隔离输入切片，不验证模型答案或持久来源真伪。
func NewTeachingReport(input TeachingReport) (TeachingReport, error) {
	report := input.Clone()
	if err := report.Validate(); err != nil {
		return TeachingReport{}, err
	}
	return report, nil
}

// Clone 深拷贝所有可变成员，调用方修改原输入不会改变返回快照。
func (r TeachingReport) Clone() TeachingReport {
	out := r
	out.ProblemSources = append([]TeachingReportProblemSource(nil), r.ProblemSources...)
	for i := range out.ProblemSources {
		if r.ProblemSources[i].AnswerSource != nil {
			source := *r.ProblemSources[i].AnswerSource
			out.ProblemSources[i].AnswerSource = &source
		}
	}
	out.SourcePages = append([]TeachingReportSourcePage(nil), r.SourcePages...)
	out.Items = append([]TeachingReportItem(nil), r.Items...)
	for i := range out.Items {
		out.Items[i].OriginalAnswer.Slots = append([]TeachingReportAnswerSlot(nil), r.Items[i].OriginalAnswer.Slots...)
		out.Items[i].AnswerAnalysis.Answers = append([]TeachingReportAnswer(nil), r.Items[i].AnswerAnalysis.Answers...)
		for j := range out.Items[i].AnswerAnalysis.Answers {
			out.Items[i].AnswerAnalysis.Answers[j].Conditions = append([]string(nil), r.Items[i].AnswerAnalysis.Answers[j].Conditions...)
		}
		out.Items[i].AnswerAnalysis.Steps = append([]TeachingReportCalculationStep(nil), r.Items[i].AnswerAnalysis.Steps...)
	}
	out.DeepSections = append([]TeachingReportDeepSection(nil), r.DeepSections...)
	for i := range out.DeepSections {
		out.DeepSections[i].ProblemIDs = append([]string(nil), r.DeepSections[i].ProblemIDs...)
		out.DeepSections[i].Blocks = append([]TeachingReportExplanationBlock(nil), r.DeepSections[i].Blocks...)
		for j := range out.DeepSections[i].Blocks {
			out.DeepSections[i].Blocks[j].Origins = append([]TeachingReportContentOrigin(nil), r.DeepSections[i].Blocks[j].Origins...)
			if table := r.DeepSections[i].Blocks[j].Table; table != nil {
				cloned := *table
				cloned.Headers = append([]TeachingReportTableCell(nil), table.Headers...)
				cloned.Rows = make([][]TeachingReportTableCell, len(table.Rows))
				for row := range table.Rows {
					cloned.Rows[row] = append([]TeachingReportTableCell(nil), table.Rows[row]...)
				}
				out.DeepSections[i].Blocks[j].Table = &cloned
			}
		}
		out.DeepSections[i].Diagrams = append([]TeachingDiagramAsset(nil), r.DeepSections[i].Diagrams...)
		out.DeepSections[i].MethodNotes = append([]TeachingReportMethodNote(nil), r.DeepSections[i].MethodNotes...)
		for j := range out.DeepSections[i].MethodNotes {
			out.DeepSections[i].MethodNotes[j].BlockIDs = append([]string(nil), r.DeepSections[i].MethodNotes[j].BlockIDs...)
		}
	}
	return out
}
