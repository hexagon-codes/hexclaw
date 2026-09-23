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
	ID   string `json:"block_id"`
	Text string `json:"text"`
	Page int    `json:"page,omitempty"`
}
type MaterialManifest struct {
	SchemaVersion      int             `json:"schema_version"`
	SourceDigest       string          `json:"source_digest"`
	ParserVersion      string          `json:"parser_version"`
	Blocks             []MaterialBlock `json:"blocks"`
	ExtractionComplete bool            `json:"extraction_complete"`
}
type MaterialCandidate struct {
	ID              string                `json:"candidate_id"`
	BlockID         string                `json:"block_id"`
	Line            int                   `json:"line"`
	Facts           k12.ProblemAssetFacts `json:"facts"`
	ReferenceAnswer string                `json:"reference_answer,omitempty"`
}
type MaterialPreparation struct {
	TaskID, OwnerID, DocumentID                                  string
	SourceRevision                                               int64
	InputDigest, Policy, State, Reason, ResultJSON, ResultDigest string
	Candidate                                                    MaterialCandidate
}

// ReconcileMaterialPreparation 在知识发布原事务内保存原解析快照与独立候选队列。
// 通用知识流程不调用模型；不能完整识别的范围保留为待复核。
func (s *Store) ReconcileMaterialPreparation(ctx context.Context, tx *sql.Tx, ev TextbookManifestLifecycleEvent) error {
	var content, digest, ext, grade, subject, agent, lifecycle, textState string
	var generation int64
	var deleted bool
	err := tx.QueryRowContext(ctx, `SELECT d.content,s.blob_sha256,s.extension,s.grade,s.subject,s.agent_id,b.lifecycle_state,b.text_state,b.content_generation,d.deleted
 FROM kb_documents d JOIN kb_semantic_document_bindings b ON b.document_id=d.id
 JOIN kb_ingest_document_sources s ON s.document_id=d.id AND s.content_generation=b.content_generation
 WHERE b.owner_id=? AND b.document_id=?`, ev.OwnerID, ev.DocumentID).
		Scan(&content, &digest, &ext, &grade, &subject, &agent, &lifecycle, &textState, &generation, &deleted)
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
	if deleted || lifecycle != "active" || textState != "ready" || generation != ev.DocumentGeneration || strings.TrimSpace(content) == "" {
		return nil
	}
	switch strings.ToLower(ext) {
	case ".md", ".markdown", ".txt", ".pdf", ".docx":
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
	if strings.EqualFold(ext, ".pdf") {
		rows, e := tx.QueryContext(ctx, `SELECT p.page_number,p.content FROM kb_ingest_page_checkpoints p JOIN kb_knowledge_jobs j ON j.job_id=p.job_id
   WHERE j.owner_id=? AND j.document_id=? AND j.document_generation=? AND p.source_digest=? ORDER BY p.page_number`, ev.OwnerID, ev.DocumentID, generation, digest)
		if e != nil {
			return e
		}
		for rows.Next() {
			var b MaterialBlock
			if e = rows.Scan(&b.Page, &b.Text); e != nil {
				rows.Close()
				return e
			}
			b.ID = fmt.Sprintf("page:%d", b.Page)
			manifest.Blocks = append(manifest.Blocks, b)
		}
		if e = rows.Close(); e != nil {
			return e
		}
	}
	if len(manifest.Blocks) == 0 {
		manifest.Blocks = []MaterialBlock{{ID: "body", Text: content}}
	}
	candidates, complete := extractMaterialQuestions(manifest.Blocks, subject, grade)
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
		raw, _ := json.Marshal(candidate)
		input := problemAssetRequestDigest(raw)
		task := problemAssetRequestDigest([]byte(fmt.Sprintf("%s/%s/%d/%s", ev.OwnerID, ev.DocumentID, generation, candidate.ID)))
		if _, err = tx.ExecContext(ctx, `INSERT INTO k12_material_preparations(task_id,owner_id,document_id,source_revision,candidate_id,input_digest,candidate_json,policy,state,updated_at) VALUES(?,?,?,?,?,?,?,'independent-solve-v1','queued',?)`,
			task, ev.OwnerID, ev.DocumentID, generation, candidate.ID, input, string(raw), nowUnix()); err != nil {
			return err
		}
	}
	return nil
}

var materialNumberPrefix = regexp.MustCompile(`^\s*(?:[-*]\s+)?(?:[0-9]+[.、)]\s+)?(.+?)\s*$`)
var materialQuestionNumber = regexp.MustCompile(`^\s*(?:[-*]\s+)?[0-9]+[.、)]\s+(.+)$`)
var materialArabicDigit = regexp.MustCompile(`[0-9]`)
var materialArithmetic = regexp.MustCompile(`^[0-9\s.+\-*/×÷()（）=？?]+$`)

// extractMaterialQuestions 只认独立算式和明确编号的单行完整数学题；引用图表或公共材料时保留待复核。
func extractMaterialQuestions(blocks []MaterialBlock, subject, grade string) ([]MaterialCandidate, bool) {
	var out []MaterialCandidate
	complete := true
	for _, block := range blocks {
		for i, line := range strings.Split(block.Text, "\n") {
			text := strings.TrimSpace(line)
			if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "<!-- source_page_span=") {
				continue
			}
			matches := materialNumberPrefix.FindStringSubmatch(text)
			if len(matches) != 2 {
				complete = false
				continue
			}
			stem := matches[1]
			if !materialArithmetic.MatchString(stem) || !strings.ContainsAny(stem, "+-*/×÷") || !strings.ContainsAny(stem, "=+*/×÷?？") {
				if (subject == "数学" || subject == "math") && materialQuestionNumber.MatchString(text) && materialArabicDigit.MatchString(stem) && strings.ContainsAny(stem, "?？") && !materialHasExternalDependency(stem) {
					reference := ""
					for _, marker := range []string{"参考答案：", "答案：", "参考答案:", "答案:"} {
						if at := strings.Index(stem, marker); at >= 0 {
							reference, stem = strings.TrimSpace(stem[at+len(marker):]), strings.TrimSpace(stem[:at])
							break
						}
					}
					facts := k12.ProblemAssetFacts{Subject: "数学", Stem: stem, AnswerContext: map[string]string{"grade_term": grade}}
					out = append(out, MaterialCandidate{ID: fmt.Sprintf("%s:line:%d", block.ID, i+1), BlockID: block.ID, Line: i + 1, Facts: facts, ReferenceAnswer: reference})
				} else {
					complete = false
				}
				continue
			}
			if strings.Count(stem, "=") > 1 {
				complete = false
				continue
			}
			reference := ""
			if at := strings.Index(stem, "="); at >= 0 {
				reference = strings.TrimSpace(stem[at+1:])
				stem = strings.TrimSpace(stem[:at]) + "="
			}
			stem = strings.TrimRight(stem, "?？")
			if strings.TrimSpace(stem) == "" {
				complete = false
				continue
			}
			reference = strings.Trim(reference, "?？ ")
			facts := k12.ProblemAssetFacts{Subject: "数学", Stem: stem, AnswerContext: map[string]string{"grade_term": grade}}
			out = append(out, MaterialCandidate{ID: fmt.Sprintf("%s:line:%d", block.ID, i+1), BlockID: block.ID, Line: i + 1, Facts: facts, ReferenceAnswer: reference})
		}
	}
	return out, complete
}

func materialHasExternalDependency(stem string) bool {
	for _, ref := range []string{"如图", "下图", "上图", "表格", "下表", "上表", "图中", "根据材料", "上述", "上题", "下列", "选项", "![", "<img", "书后", "见答案", "同上"} {
		if strings.Contains(stem, ref) {
			return true
		}
	}
	return false
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
func materialSourceCurrent(ctx context.Context, db dbHandle, p MaterialPreparation) error {
	var active bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kb_semantic_document_bindings b JOIN kb_documents d ON d.id=b.document_id WHERE b.owner_id=? AND b.document_id=? AND b.content_generation=? AND b.lifecycle_state='active' AND b.text_state='ready' AND d.deleted=0)`, p.OwnerID, p.DocumentID, p.SourceRevision).Scan(&active)
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
	_, err := s.db.ExecContext(ctx, `UPDATE k12_material_preparations SET state='outcome_unknown',reason='Previous execution requires reconciliation',updated_at=? WHERE state='running'`, nowUnix())
	return err
}
func (s *Store) MaterialForegroundBusy(ctx context.Context) (bool, error) {
	var busy bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_grading_jobs WHERE status IN ('queued','normalizing','recognizing','assessing','finalizing'))`).Scan(&busy)
	return busy, err
}
