package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var ErrMaterialPreparationFenced = errors.New("material preparation source is no longer current")
var ErrMaterialPreparationUnknown = errors.New("material preparation outcome requires reconciliation")

// MaterialBlock 来自原解析正文或完整页回执，位置不由知识切片推算。
type MaterialBlock struct {
	ID         string   `json:"block_id"`
	Kind       string   `json:"kind,omitempty"`
	ObjectIDs  []string `json:"object_ids,omitempty"`
	Incomplete bool     `json:"incomplete,omitempty"`
	Text       string   `json:"text"`
	Page       int      `json:"page,omitempty"`
}
type MaterialManifest struct {
	SchemaVersion         int                            `json:"schema_version"`
	SourceDigest          string                         `json:"source_digest"`
	ParserVersion         string                         `json:"parser_version"`
	Blocks                []MaterialBlock                `json:"blocks"`
	ExtractionComplete    bool                           `json:"extraction_complete"`
	Relations             []MaterialSourceRelation       `json:"relations,omitempty"`
	Objects               json.RawMessage                `json:"objects,omitempty"`
	Questions             []MaterialStructuredQuestion   `json:"questions,omitempty"`
	Progress              *MaterialIngestProgress        `json:"progress,omitempty"`
	ReferenceObservations []MaterialReferenceObservation `json:"reference_observations,omitempty"`
	SourceIssues          []MaterialSourceIssue          `json:"source_issues,omitempty"`
}
type MaterialSourceRelation struct {
	Kind string `json:"kind"`
	From string `json:"from"`
	To   string `json:"to"`
}
type MaterialSourceLocation struct {
	BlockID string `json:"block_id"`
	Line    int    `json:"line"`
}
type MaterialCandidate struct {
	VisualPDFPages     []int                    `json:"visual_pdf_pages,omitempty"`
	VisualObjectIDs    []string                 `json:"visual_object_ids,omitempty"`
	ID                 string                   `json:"candidate_id"`
	BlockID            string                   `json:"block_id"`
	Line               int                      `json:"line"`
	Facts              k12.ProblemAssetFacts    `json:"facts"`
	ReferenceAnswer    string                   `json:"reference_answer,omitempty"`
	SourceBlockIDs     []string                 `json:"source_block_ids,omitempty"`
	ReferenceBlockIDs  []string                 `json:"reference_block_ids,omitempty"`
	QuestionNumber     string                   `json:"question_number,omitempty"`
	SourceRecordID     string                   `json:"source_record_id,omitempty"`
	SourceLabel        string                   `json:"source_label,omitempty"`
	SourceLocation     string                   `json:"source_location,omitempty"`
	SourcePage         int                      `json:"source_page,omitempty"`
	Issues             []string                 `json:"issues,omitempty"`
	SourceLocations    []MaterialSourceLocation `json:"source_locations,omitempty"`
	ReferenceLocations []MaterialSourceLocation `json:"reference_locations,omitempty"`
}
type MaterialPreparation struct {
	VisualEvidence                                               *MaterialVisualEvidence
	TaskID, OwnerID, DocumentID                                  string
	SourceRevision                                               int64
	InputDigest, Policy, State, Reason, ResultJSON, ResultDigest string
	Candidate                                                    MaterialCandidate
}

// ReconcileMaterialPreparation 在知识发布原事务内保存原解析快照与独立候选队列。
// 通用知识流程不调用模型；不能完整识别的范围保留为待复核。
func (s *Store) ReconcileMaterialPreparation(ctx context.Context, tx *sql.Tx, ev TextbookManifestLifecycleEvent) error {
	var content, title, digest, ext, grade, subject, agent, lifecycle, textState string
	var generation int64
	var deleted bool
	err := tx.QueryRowContext(ctx, `SELECT d.content,d.title,s.blob_sha256,s.extension,s.grade,s.subject,s.agent_id,b.lifecycle_state,b.text_state,b.content_generation,d.deleted
 FROM kb_documents d JOIN kb_semantic_document_bindings b ON b.document_id=d.id
 JOIN kb_ingest_document_sources s ON s.document_id=d.id AND s.content_generation=b.content_generation
 WHERE b.owner_id=? AND b.document_id=?`, ev.OwnerID, ev.DocumentID).
		Scan(&content, &title, &digest, &ext, &grade, &subject, &agent, &lifecycle, &textState, &generation, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='stopped',reason='Source changed or removed',updated_at=?
 WHERE owner_id=? AND document_id=? AND state IN ('queued','running','verified') AND (source_revision<>? OR ? OR ?<>'active')`,
		nowUnix(), ev.OwnerID, ev.DocumentID, generation, deleted, lifecycle); err != nil {
		return err
	}
	if deleted || lifecycle != "active" || generation != ev.DocumentGeneration {
		return nil
	}
	if strings.EqualFold(ext, ".pdf") {
		return s.reconcileMaterialPDF(ctx, tx, ev, title, digest, grade, subject, agent, textState == "ready")
	}
	if textState != "ready" || strings.TrimSpace(content) == "" {
		return nil
	}
	switch strings.ToLower(ext) {
	case ".md", ".markdown", ".txt", ".pdf", ".docx", ".hexbank", ".jsonl":
	default:
		return nil
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_material_manifests WHERE owner_id=? AND document_id=? AND source_revision=?)`, ev.OwnerID, ev.DocumentID, generation).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	// 标题中只有一个明确年级学期时采用来源原文，不从用户偏好猜测。
	if grade == "" {
		grade = materialTitleGrade(title)
	}
	if subject == "" && strings.Contains(title, "数学") {
		subject = "数学"
	}
	// 只有来源明确绑定的本机孩子档案可补充课程范围，未知来源不猜年级。
	if grade == "" && agent != "" && ev.OwnerID == "desktop-user" {
		var raw string
		if e := tx.QueryRowContext(ctx, `SELECT metadata FROM agents WHERE name=?`, agent).Scan(&raw); e == nil {
			var meta map[string]string
			if json.Unmarshal([]byte(raw), &meta) == nil {
				grade = k12.ProfileFromMeta(meta).GradeTerm
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
	}
	manifest := MaterialManifest{SchemaVersion: 1, SourceDigest: digest, ParserVersion: "persisted-text-v1"}
	var sourceManifest string
	manifestErr := tx.QueryRowContext(ctx, `SELECT manifest_json FROM kb_ingest_source_manifests WHERE owner_id=? AND document_id=? AND content_generation=? AND source_digest=?`, ev.OwnerID, ev.DocumentID, generation, digest).Scan(&sourceManifest)
	if manifestErr == nil {
		if err = json.Unmarshal([]byte(sourceManifest), &manifest); err != nil {
			return err
		}
	} else if !errors.Is(manifestErr, sql.ErrNoRows) {
		return manifestErr
	}
	if len(manifest.Blocks) == 0 {
		manifest.Blocks = []MaterialBlock{{ID: "body", Text: content}}
	}
	var candidates []MaterialCandidate
	var complete bool
	if manifest.Questions != nil {
		candidates, complete = extractStructuredMaterialQuestions(manifest.Questions)
	} else {
		candidates, complete = extractMaterialQuestions(manifest.Blocks, subject, grade)
	}
	candidates = prepareMaterialVisualCandidates(manifest, candidates)
	for _, candidate := range candidates {
		if len(candidate.Issues) > 0 {
			complete = false
		}
	}
	for _, c := range candidates {
		for _, from := range c.SourceBlockIDs {
			manifest.Relations = append(manifest.Relations, MaterialSourceRelation{Kind: "question_source", From: from, To: c.ID})
		}
		for _, from := range c.ReferenceBlockIDs {
			manifest.Relations = append(manifest.Relations, MaterialSourceRelation{Kind: "answer_for", From: from, To: c.ID})
		}
	}
	manifest.ExtractionComplete = complete
	raw, _ := json.Marshal(manifest)
	state := "ready"
	if !complete {
		state = "needs_review"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO k12_material_manifests(owner_id,corpus_uid,document_id,source_revision,source_digest,parser_version,manifest_json,state,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		ev.OwnerID, ev.CorpusUID, ev.DocumentID, generation, digest, manifest.ParserVersion, string(raw), state, nowUnix()); err != nil {
		return err
	}
	for _, candidate := range candidates {
		state, reason := "queued", ""
		if len(candidate.Issues) > 0 {
			state = "needs_review"
			reason = strings.Join(candidate.Issues, "; ")
		}
		raw, _ := json.Marshal(candidate)
		input := problemAssetRequestDigest(raw)
		task := problemAssetRequestDigest([]byte(fmt.Sprintf("%s/%s/%d/%s", ev.OwnerID, ev.DocumentID, generation, candidate.ID)))
		if _, err = tx.ExecContext(ctx, `INSERT INTO k12_material_preparations(task_id,owner_id,document_id,source_revision,candidate_id,input_digest,candidate_json,policy,state,reason,updated_at) VALUES(?,?,?,?,?,?,?,'independent-solve-v1',?,?,?)`,
			task, ev.OwnerID, ev.DocumentID, generation, candidate.ID, input, string(raw), state, reason, nowUnix()); err != nil {
			return err
		}
	}
	return nil
}

var materialGradeInTitle = regexp.MustCompile(`([一二三四五六七八九1-9])年级[ ·\t]*([上下])(?:册|学期)?`)

func materialTitleGrade(title string) string {
	matches := materialGradeInTitle.FindAllStringSubmatch(title, -1)
	grade := ""
	for _, m := range matches {
		value := m[1] + "年级" + m[2]
		if strings.Contains("123456789", m[1]) {
			value = string([]rune("一二三四五六七八九")[int(m[1][0]-'1')]) + "年级" + m[2]
		}
		if grade != "" && grade != value {
			return ""
		}
		grade = value
	}
	return grade
}

const materialColumns = `task_id,owner_id,document_id,source_revision,input_digest,candidate_json,policy,state,reason,result_json,result_digest`

func scanMaterial(row interface{ Scan(...any) error }) (MaterialPreparation, error) {
	var p MaterialPreparation
	var raw string
	err := row.Scan(&p.TaskID, &p.OwnerID, &p.DocumentID, &p.SourceRevision, &p.InputDigest, &raw, &p.Policy, &p.State, &p.Reason, &p.ResultJSON, &p.ResultDigest)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &p.Candidate)
	}
	return p, err
}
func (s *Store) GetMaterialPreparation(ctx context.Context, task string) (MaterialPreparation, error) {
	return scanMaterial(s.db.QueryRowContext(ctx, `SELECT `+materialColumns+` FROM k12_material_preparations WHERE task_id=?`, task))
}
func (s *Store) NextMaterialPreparation(ctx context.Context) (MaterialPreparation, error) {
	return scanMaterial(s.db.QueryRowContext(ctx, `SELECT `+materialColumns+` FROM k12_material_preparations WHERE state IN ('queued','verified') ORDER BY updated_at,task_id LIMIT 1`))
}

const materialCurrentSourcePredicate = `b.owner_id=? AND b.document_id=? AND b.content_generation=? AND b.lifecycle_state='active' AND d.deleted=0 AND (b.text_state='ready' OR EXISTS(SELECT 1 FROM k12_material_manifests m JOIN kb_ingest_document_sources s ON s.owner_id=m.owner_id AND s.document_id=m.document_id AND s.content_generation=m.source_revision WHERE m.owner_id=b.owner_id AND m.document_id=b.document_id AND m.source_revision=b.content_generation AND m.source_digest=s.blob_sha256 AND json_extract(m.manifest_json,'$.progress.pages_ready')>0))`

func materialSourceCurrent(ctx context.Context, db dbHandle, p MaterialPreparation) error {
	var active bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kb_semantic_document_bindings b JOIN kb_documents d ON d.id=b.document_id WHERE `+materialCurrentSourcePredicate+`)`, p.OwnerID, p.DocumentID, p.SourceRevision).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return ErrMaterialPreparationFenced
	}
	return nil
}

// ClaimMaterialPreparation 在发送前再次核对来源并取得唯一执行权。
func (s *Store) ClaimMaterialPreparation(ctx context.Context, p MaterialPreparation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='running',updated_at=? WHERE task_id=? AND state='queued'`, nowUnix(), p.TaskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrMaterialPreparationUnknown
	}
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) FinishMaterialPreparation(ctx context.Context, p MaterialPreparation, state, reason, result string) error {
	if state != "verified" && state != "needs_review" && state != "outcome_unknown" && state != "failed" && state != "queued" {
		return errors.New("invalid material preparation outcome")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state=?,reason=?,result_json=?,result_digest=?,updated_at=? WHERE task_id=? AND state='running'`, state, reason, result, problemAssetRequestDigest([]byte(result)), nowUnix(), p.TaskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrMaterialPreparationFenced
	}
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverMaterialPreparations 不重发进程退出前已进入执行的任务。
func (s *Store) RecoverMaterialPreparations(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 未发送的明确恢复或已保存成功替代回执可继续；已提交 sent 而缺少结果仍停止。
	_, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='queued',reason='Explicit verification recovery queued',updated_at=? WHERE state='running' AND EXISTS(SELECT 1 FROM k12_material_recovery_decisions r WHERE r.task_id=k12_material_preparations.task_id AND (r.replacement_invocation_id IS NULL OR EXISTS(SELECT 1 FROM k12_material_invocations replacement WHERE replacement.invocation_id=r.replacement_invocation_id AND replacement.status='succeeded')) AND NOT EXISTS(SELECT 1 FROM k12_material_invocations i WHERE i.task_id=r.task_id AND i.invocation_id<>r.original_invocation_id AND `+materialUnresolvedPredicate+`))`, nowUnix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='outcome_unknown',reason='Previous execution requires reconciliation',updated_at=? WHERE state='running'`, nowUnix())
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) MaterialForegroundBusy(ctx context.Context) (bool, error) {
	var busy bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_grading_jobs WHERE status IN ('queued','normalizing','recognizing','assessing','finalizing'))`).Scan(&busy)
	return busy, err
}

// StopStaleMaterialPreparation 只终止已失效来源；原回执与已发布资产保留。
func (s *Store) StopStaleMaterialPreparation(ctx context.Context, p MaterialPreparation) error {
	_, err := s.db.ExecContext(ctx, `UPDATE k12_material_preparations SET state='stopped',reason='Source changed or removed',updated_at=? WHERE task_id=? AND state IN ('queued','running','verified') AND NOT EXISTS(SELECT 1 FROM kb_semantic_document_bindings b JOIN kb_documents d ON d.id=b.document_id WHERE `+materialCurrentSourcePredicate+`)`, nowUnix(), p.TaskID, p.OwnerID, p.DocumentID, p.SourceRevision)
	return err
}
