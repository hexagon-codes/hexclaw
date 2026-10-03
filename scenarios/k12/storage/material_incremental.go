package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// MaterialIngestProgress 只描述已经持久化的连续页，不推断尚未收到的页。
type MaterialIngestProgress struct {
	PagesReady     int    `json:"pages_ready"`
	PagesTotal     int    `json:"pages_total"`
	SourceComplete bool   `json:"source_complete"`
	Subject        string `json:"subject,omitempty"`
	GradeTerm      string `json:"grade_term,omitempty"`
}

// MaterialReferenceObservation 保留后到答案及真实位置，不修改已发送的求解输入。
type MaterialReferenceObservation struct {
	CandidateID    string                   `json:"candidate_id,omitempty"`
	QuestionNumber string                   `json:"question_number"`
	Text           string                   `json:"text"`
	Locations      []MaterialSourceLocation `json:"locations"`
	Reason         string                   `json:"reason,omitempty"`
	ReviewRequired bool                     `json:"review_required,omitempty"`
}

type MaterialSourceIssue struct {
	CandidateID string `json:"candidate_id"`
	Reason      string `json:"reason"`
}

// reconcileMaterialPDF 从不可变页事实重建进度投影；候选身份与调用事实只追加。
func (s *Store) reconcileMaterialPDF(ctx context.Context, tx *sql.Tx, ev TextbookManifestLifecycleEvent, title, digest, grade, subject, agent string, sourceComplete bool) error {
	var priorRaw string
	err := tx.QueryRowContext(ctx, `SELECT manifest_json FROM k12_material_manifests WHERE owner_id=? AND document_id=? AND source_revision=?`, ev.OwnerID, ev.DocumentID, ev.DocumentGeneration).Scan(&priorRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var prior MaterialManifest
	if priorRaw != "" {
		if err = json.Unmarshal([]byte(priorRaw), &prior); err != nil {
			return err
		}
		if prior.Progress == nil || prior.Progress.SourceComplete {
			return nil
		}
	}
	if prior.Progress != nil {
		grade, subject = prior.Progress.GradeTerm, prior.Progress.Subject
	}
	if prior.Progress == nil && grade == "" {
		grade = materialTitleGrade(title)
	}
	if prior.Progress == nil && subject == "" && strings.Contains(title, "数学") {
		subject = "数学"
	}
	if prior.Progress == nil && grade == "" && agent != "" && ev.OwnerID == "desktop-user" {
		var raw string
		if err = tx.QueryRowContext(ctx, `SELECT metadata FROM agents WHERE name=?`, agent).Scan(&raw); err == nil {
			var meta map[string]string
			if json.Unmarshal([]byte(raw), &meta) == nil {
				grade = k12.ProfileFromMeta(meta).GradeTerm
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	manifest := MaterialManifest{SchemaVersion: 1, SourceDigest: digest, ParserVersion: "pdf-closed-groups-v2", Progress: &MaterialIngestProgress{Subject: subject, GradeTerm: grade}, SourceIssues: prior.SourceIssues}
	rows, err := tx.QueryContext(ctx, `SELECT p.page_number,p.pages_total,p.content FROM kb_ingest_page_checkpoints p JOIN kb_knowledge_jobs j ON j.job_id=p.job_id WHERE j.owner_id=? AND j.document_id=? AND j.document_generation=? AND p.source_digest=? ORDER BY p.page_number,j.created_at DESC,j.job_id DESC`, ev.OwnerID, ev.DocumentID, ev.DocumentGeneration, digest)
	if err != nil {
		return err
	}
	for rows.Next() {
		var block MaterialBlock
		var total int
		if err = rows.Scan(&block.Page, &total, &block.Text); err != nil {
			rows.Close()
			return err
		}
		if block.Page <= len(manifest.Blocks) {
			continue
		}
		if block.Page != len(manifest.Blocks)+1 {
			break
		}
		block.ID = fmt.Sprintf("page:%d", block.Page)
		manifest.Blocks = append(manifest.Blocks, block)
		manifest.Progress.PagesTotal = total
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil || len(manifest.Blocks) == 0 {
		return err
	}
	manifest.Progress.PagesReady = len(manifest.Blocks)
	manifest.Progress.SourceComplete = sourceComplete && len(manifest.Blocks) == manifest.Progress.PagesTotal
	candidates, complete := extractMaterialClosedGroups(manifest.Blocks, subject, grade, manifest.Progress.SourceComplete)
	manifest.ExtractionComplete = manifest.Progress.SourceComplete && complete
	stored := map[string]MaterialCandidate{}
	existing := map[string]bool{}
	rows, err = tx.QueryContext(ctx, `SELECT candidate_json FROM k12_material_preparations WHERE owner_id=? AND document_id=? AND source_revision=?`, ev.OwnerID, ev.DocumentID, ev.DocumentGeneration)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw string
		var c MaterialCandidate
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			rows.Close()
			return err
		}
		stored[c.ID] = c
		existing[c.ID] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, c := range candidates {
		if old, ok := stored[c.ID]; ok {
			oldFacts, _ := old.Facts.ExactIdentity(ev.OwnerID)
			newFacts, _ := c.Facts.ExactIdentity(ev.OwnerID)
			if oldFacts.FactsDigest != newFacts.FactsDigest {
				found := false
				for _, issue := range manifest.SourceIssues {
					found = found || issue.CandidateID == c.ID
				}
				if !found {
					manifest.SourceIssues = append(manifest.SourceIssues, MaterialSourceIssue{CandidateID: c.ID, Reason: "Source question changed after preparation"})
				}
				manifest.ExtractionComplete = false
			}
			continue
		}
		stored[c.ID] = c
	}
	manifest.ReferenceObservations = materialReferenceObservations(manifest.Blocks, stored, manifest.Progress.SourceComplete)
	if len(manifest.SourceIssues) > 0 {
		manifest.ExtractionComplete = false
	}
	for i := range manifest.ReferenceObservations {
		o := &manifest.ReferenceObservations[i]
		if o.Reason == "" && o.CandidateID != "" && existing[o.CandidateID] && stored[o.CandidateID].ReferenceAnswer == "" {
			o.Reason = "Reference answer arrived after preparation; independent answer is unchanged"
		}
		if o.ReviewRequired {
			manifest.ExtractionComplete = false
		}
	}
	for _, c := range stored {
		for _, from := range c.SourceBlockIDs {
			manifest.Relations = append(manifest.Relations, MaterialSourceRelation{Kind: "question_source", From: from, To: c.ID})
		}
	}
	for _, o := range manifest.ReferenceObservations {
		if o.CandidateID != "" {
			for _, loc := range o.Locations {
				manifest.Relations = append(manifest.Relations, MaterialSourceRelation{Kind: "answer_for", From: loc.BlockID, To: o.CandidateID})
			}
		}
	}
	state := "preparing"
	if manifest.Progress.SourceComplete {
		state = "ready"
		if !manifest.ExtractionComplete {
			state = "needs_review"
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO k12_material_manifests(owner_id,corpus_uid,document_id,source_revision,source_digest,parser_version,manifest_json,state,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_id,document_id,source_revision) DO UPDATE SET manifest_json=excluded.manifest_json,state=excluded.state WHERE k12_material_manifests.source_digest=excluded.source_digest`, ev.OwnerID, ev.CorpusUID, ev.DocumentID, ev.DocumentGeneration, digest, manifest.ParserVersion, string(raw), state, nowUnix()); err != nil {
		return err
	}
	for _, c := range candidates {
		candidateState, reason := "queued", ""
		if len(c.Issues) > 0 {
			candidateState, reason = "needs_review", strings.Join(c.Issues, "; ")
		}
		raw, _ := json.Marshal(c)
		task := problemAssetRequestDigest([]byte(fmt.Sprintf("%s/%s/%d/%s", ev.OwnerID, ev.DocumentID, ev.DocumentGeneration, c.ID)))
		if _, err = tx.ExecContext(ctx, `INSERT INTO k12_material_preparations(task_id,owner_id,document_id,source_revision,candidate_id,input_digest,candidate_json,policy,state,reason,updated_at) VALUES(?,?,?,?,?,?,?,'independent-solve-v1',?,?,?) ON CONFLICT(task_id) DO NOTHING`, task, ev.OwnerID, ev.DocumentID, ev.DocumentGeneration, c.ID, problemAssetRequestDigest(raw), string(raw), candidateState, reason, nowUnix()); err != nil {
			return err
		}
	}
	return nil
}

func materialReferenceObservations(blocks []MaterialBlock, candidates map[string]MaterialCandidate, sourceComplete bool) []MaterialReferenceObservation {
	var out []MaterialReferenceObservation
	var current *MaterialReferenceObservation
	inAnswers := false
	flush := func() {
		if current != nil {
			out = append(out, *current)
			current = nil
		}
	}
	for _, block := range blocks {
		for i, raw := range strings.Split(block.Text, "\n") {
			text := strings.TrimSpace(raw)
			if text == "" || strings.HasPrefix(text, "<!-- source_page_span=") {
				continue
			}
			heading := strings.TrimSpace(strings.TrimLeft(text, "#"))
			if materialAnswerHeading(heading) {
				flush()
				inAnswers = true
				continue
			}
			if strings.HasPrefix(text, "#") || block.Kind == "heading" {
				flush()
				inAnswers = false
				continue
			}
			if !inAnswers {
				continue
			}
			number, body, numbered := materialNumberedLine(text)
			if numbered {
				flush()
				current = &MaterialReferenceObservation{QuestionNumber: number, Text: body}
			} else if current != nil {
				current.Text += "\n" + text
			}
			if current != nil {
				current.Locations = append(current.Locations, MaterialSourceLocation{BlockID: block.ID, Line: i + 1})
			}
		}
	}
	if sourceComplete {
		flush()
	}
	for i := range out {
		o := &out[i]
		var matches []MaterialCandidate
		for _, c := range candidates {
			if c.QuestionNumber == o.QuestionNumber {
				matches = append(matches, c)
			}
		}
		if len(matches) != 1 {
			o.Reason = "Reference answer has no unique question"
			o.ReviewRequired = true
			continue
		}
		o.CandidateID = matches[0].ID
		if old := matches[0].ReferenceAnswer; old != "" && old != strings.TrimSpace(o.Text) {
			o.Reason = "Reference answers conflict; verified answer is unchanged"
			o.ReviewRequired = true
		}
		for j, other := range out {
			if i != j && other.QuestionNumber == o.QuestionNumber {
				o.Reason = "Reference answer number is ambiguous"
				o.CandidateID = ""
				o.ReviewRequired = true
			}
		}
	}
	return out
}
