package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ProblemAssetReuse 是历史实际采用的只读投影，不表示资产当前仍可用于新任务。
type ProblemAssetReuse struct {
	Stem         string `json:"stem"`
	Answer       string `json:"answer"`
	SourceName   string `json:"source_name"`
	DocumentID   string `json:"document_id,omitempty"`
	Page         int    `json:"page,omitempty"`
	SourceDigest string `json:"source_digest,omitempty"`
}

// GetProblemAssetReuse 绑定本次作答、不可变版本和采用回执；来源失效不改写历史采用事实。
func (s *Store) GetProblemAssetReuse(ctx context.Context, owner, job, problem string, source k12.ProblemAnswerSource) (ProblemAssetReuse, error) {
	var out ProblemAssetReuse
	if source.Kind != k12.ProblemAnswerAsset || source.Validate() != nil {
		return out, ErrProblemAssetEvidence
	}
	var factsRaw string
	err := s.db.QueryRowContext(ctx, `SELECT v.facts_json,v.answer FROM k12_problem_asset_adoptions a
 JOIN k12_problem_asset_versions v ON v.owner_id=a.owner_id AND v.asset_id=a.asset_id AND v.asset_version=a.asset_version
 WHERE a.owner_id=? AND a.adoption_id=? AND a.job_id=? AND a.problem_id=? AND a.asset_id=? AND a.asset_version=? AND a.asset_revision=? AND a.facts_digest=?`,
		owner, source.AdoptionID, job, problem, source.AssetID, source.AssetVersion, source.AssetRevision, source.FactsDigest).Scan(&factsRaw, &out.Answer)
	if err != nil {
		return out, err
	}
	var facts k12.ProblemAssetFacts
	if err = json.Unmarshal([]byte(factsRaw), &facts); err != nil {
		return out, err
	}
	out.Stem, out.SourceName = facts.Stem, "Previously verified answer"
	var candidateRaw, manifestRaw, document, sourceDigest string
	var current bool
	err = s.db.QueryRowContext(ctx, `SELECT p.document_id,src.original_name,src.blob_sha256,p.candidate_json,m.manifest_json,
 COALESCE(b.content_generation=p.source_revision AND b.lifecycle_state='active' AND d.deleted=0,0)
 FROM k12_material_preparations p
 JOIN k12_material_manifests m ON m.owner_id=p.owner_id AND m.document_id=p.document_id AND m.source_revision=p.source_revision
 JOIN kb_ingest_document_sources src ON src.owner_id=p.owner_id AND src.document_id=p.document_id AND src.content_generation=p.source_revision
 LEFT JOIN kb_semantic_document_bindings b ON b.owner_id=p.owner_id AND b.document_id=p.document_id
 LEFT JOIN kb_documents d ON d.id=p.document_id
 WHERE p.owner_id=? AND p.asset_id=? AND p.asset_version=? AND p.state='published' ORDER BY m.created_at,p.task_id LIMIT 1`,
		owner, source.AssetID, source.AssetVersion).Scan(&document, &out.SourceName, &sourceDigest, &candidateRaw, &manifestRaw, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var candidate MaterialCandidate
	var manifest MaterialManifest
	if err = json.Unmarshal([]byte(candidateRaw), &candidate); err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(manifestRaw), &manifest); err != nil {
		return out, err
	}
	// 新版本页码不可冒充当时来源；只有原修订仍可读时才提供预览链接。
	if current {
		out.DocumentID, out.SourceDigest = document, sourceDigest
		for _, block := range manifest.Blocks {
			if block.ID == candidate.BlockID {
				out.Page = block.Page
				break
			}
		}
	}
	return out, nil
}
