package k12storage

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const problemAssetCandidateLimit = 8
const problemAssetCandidateBudget = 100 * time.Millisecond

// FindEquivalentProblemAsset 只供精确查找未命中的短算式使用；索引只召回，权威事实决定采用。
func (s *Store) FindEquivalentProblemAsset(ctx context.Context, owner string, facts k12.ProblemAssetFacts) (k12.ProblemAssetVersion, error) {
	terms, ok := k12.ProblemAssetExpressionTerms(facts)
	if !ok || strings.TrimSpace(owner) == "" {
		return k12.ProblemAssetVersion{}, ErrProblemAssetUnavailable
	}
	queryTerms := make([]string, 0, len(terms))
	seen := make(map[string]bool, len(terms))
	for _, term := range terms {
		if !seen[term] {
			queryTerms = append(queryTerms, `"`+term+`"`)
			seen[term] = true
		}
	}
	queryCtx, cancel := context.WithTimeout(ctx, problemAssetCandidateBudget)
	defer cancel()
	rows, err := s.db.QueryContext(queryCtx, `SELECT v.owner_id,v.asset_id,v.asset_version,v.published_revision,v.facts_json,v.facts_digest,v.answer,v.answer_result_json,v.created_at
        FROM k12_problem_asset_candidates_fts f
        JOIN k12_problem_assets a ON a.owner_id=f.owner_id AND a.asset_id=f.asset_id AND a.current_version=f.asset_version
        JOIN k12_problem_asset_versions v ON v.owner_id=a.owner_id AND v.asset_id=a.asset_id AND v.asset_version=a.current_version
        WHERE k12_problem_asset_candidates_fts MATCH ? AND a.owner_id=? AND a.status='active'
        ORDER BY bm25(k12_problem_asset_candidates_fts),a.asset_id LIMIT ?`, strings.Join(queryTerms, " AND "), owner, problemAssetCandidateLimit)
	if err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate k12.ProblemAssetVersion
		var rawFacts string
		if err := rows.Scan(&candidate.OwnerID, &candidate.AssetID, &candidate.Version, &candidate.Revision, &rawFacts, &candidate.FactsDigest,
			&candidate.Answer, &candidate.AnswerResultJSON, &candidate.CreatedAt); err != nil {
			return k12.ProblemAssetVersion{}, err
		}
		if err := json.Unmarshal([]byte(rawFacts), &candidate.Facts); err != nil {
			return k12.ProblemAssetVersion{}, err
		}
		if k12.EquivalentProblemAssetExpression(facts, candidate.Facts) {
			return candidate, nil
		}
	}
	if err := rows.Err(); err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	return k12.ProblemAssetVersion{}, ErrProblemAssetUnavailable
}
