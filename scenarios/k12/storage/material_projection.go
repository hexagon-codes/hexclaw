package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

// MaterialPreparationSummary 以当前来源修订统一生成摘要和题目明细。
type MaterialPreparationSummary struct {
	DocumentID         string                    `json:"document_id"`
	SourceRevision     int64                     `json:"source_revision"`
	State              string                    `json:"state"`
	ExtractionComplete bool                      `json:"extraction_complete"`
	Counts             map[string]int            `json:"counts"`
	Items              []MaterialPreparationItem `json:"items"`
}
type MaterialPreparationItem struct {
	CandidateID     string `json:"candidate_id"`
	State           string `json:"state"`
	Stem            string `json:"stem"`
	ReferenceAnswer string `json:"reference_answer,omitempty"`
	Answer          string `json:"answer,omitempty"`
	BlockID         string `json:"block_id"`
	Page            int    `json:"page,omitempty"`
	Line            int    `json:"line"`
	Reason          string `json:"reason,omitempty"`
	AssetID         string `json:"asset_id,omitempty"`
	AssetVersion    int    `json:"asset_version,omitempty"`
}

func (s *Store) GetMaterialPreparationSummary(ctx context.Context, owner, document string) (MaterialPreparationSummary, error) {
	return s.materialPreparationSummary(ctx, owner, document, true)
}

func (s *Store) GetMaterialPreparationOverview(ctx context.Context, owner, document string) (MaterialPreparationSummary, error) {
	return s.materialPreparationSummary(ctx, owner, document, false)
}

func (s *Store) materialPreparationSummary(ctx context.Context, owner, document string, details bool) (MaterialPreparationSummary, error) {
	out := MaterialPreparationSummary{DocumentID: document, State: "not_prepared", Counts: map[string]int{"ready": 0, "preparing": 0, "needs_review": 0, "failed": 0, "outcome_unknown": 0, "stopped": 0}, Items: []MaterialPreparationItem{}}
	if err := s.db.QueryRowContext(ctx, `SELECT b.content_generation FROM kb_semantic_document_bindings b JOIN kb_documents d ON d.id=b.document_id WHERE b.owner_id=? AND b.document_id=? AND b.lifecycle_state='active' AND d.deleted=0`, owner, document).Scan(&out.SourceRevision); err != nil {
		return out, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT manifest_json,state FROM k12_material_manifests WHERE owner_id=? AND document_id=? AND source_revision=?`, owner, document, out.SourceRevision).Scan(&raw, &out.State)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var manifest MaterialManifest
	if err = json.Unmarshal([]byte(raw), &manifest); err != nil {
		return out, err
	}
	out.ExtractionComplete = manifest.ExtractionComplete
	if !details {
		rows, err := s.db.QueryContext(ctx, `SELECT CASE WHEN p.state='published' AND a.status='active' THEN 'ready' WHEN p.state='published' THEN 'needs_review' WHEN p.state IN ('queued','running','verified') THEN 'preparing' ELSE p.state END,COUNT(*) FROM k12_material_preparations p LEFT JOIN k12_problem_assets a ON a.asset_id=p.asset_id AND a.owner_id=p.owner_id WHERE p.owner_id=? AND p.document_id=? AND p.source_revision=? GROUP BY 1`, owner, document, out.SourceRevision)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var state string
			var count int
			if err = rows.Scan(&state, &count); err != nil {
				return out, err
			}
			out.Counts[state] = count
		}
		if err = rows.Err(); err != nil {
			return out, err
		}
		materialSummaryState(&out)
		return out, nil
	}
	pages := map[string]int{}
	for _, block := range manifest.Blocks {
		pages[block.ID] = block.Page
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.candidate_json,p.state,p.reason,p.result_json,p.asset_id,p.asset_version,COALESCE(a.status,'') FROM k12_material_preparations p LEFT JOIN k12_problem_assets a ON a.asset_id=p.asset_id AND a.owner_id=p.owner_id WHERE p.owner_id=? AND p.document_id=? AND p.source_revision=? ORDER BY p.candidate_id`, owner, document, out.SourceRevision)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item MaterialPreparationItem
		var raw, result, assetState string
		if err = rows.Scan(&raw, &item.State, &item.Reason, &result, &item.AssetID, &item.AssetVersion, &assetState); err != nil {
			return out, err
		}
		var c MaterialCandidate
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return out, err
		}
		item.CandidateID, item.Stem, item.ReferenceAnswer, item.BlockID, item.Line, item.Page = c.ID, c.Facts.Stem, c.ReferenceAnswer, c.BlockID, c.Line, pages[c.BlockID]
		var answer struct{ Solution string }
		if strings.TrimSpace(result) != "" {
			if err = json.Unmarshal([]byte(result), &answer); err != nil {
				return out, err
			}
		}
		item.Answer = answer.Solution
		switch item.State {
		case "published":
			if assetState == "active" {
				item.State = "ready"
			} else {
				item.State = "needs_review"
				item.Reason = "Answer asset is no longer available"
				item.Answer = ""
			}
		case "queued", "running", "verified":
			item.State = "preparing"
		}
		out.Counts[item.State]++
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	order := map[string]int{}
	for i, block := range manifest.Blocks {
		order[block.ID] = i
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if order[a.BlockID] != order[b.BlockID] {
			return order[a.BlockID] < order[b.BlockID]
		}
		return a.Line < b.Line
	})
	materialSummaryState(&out)
	return out, nil
}

func materialSummaryState(out *MaterialPreparationSummary) {
	if out.Counts["preparing"] > 0 {
		out.State = "preparing"
	} else if out.Counts["outcome_unknown"] > 0 {
		out.State = "outcome_unknown"
	} else if !out.ExtractionComplete || out.Counts["needs_review"] > 0 {
		out.State = "needs_review"
	} else if out.Counts["failed"] > 0 {
		out.State = "failed"
	} else if out.Counts["stopped"] > 0 {
		out.State = "stopped"
	} else {
		out.State = "ready"
	}
}
