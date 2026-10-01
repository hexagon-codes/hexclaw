package k12storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/internal/sqliteutil"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type recognitionInitialReadStoredV1 struct {
	Settlement k12.RecognitionLayoutInitialReadSettlementV1       `json:"settlement"`
	Result     k12.RecognitionLayoutInitialReadSettlementResultV1 `json:"result"`
}

type recognitionReviewStoredV1 struct {
	Settlement k12.RecognitionLayoutReviewBatchSettlementV1       `json:"settlement"`
	Result     k12.RecognitionLayoutReviewBatchSettlementResultV1 `json:"result"`
}

func recognitionReadDigestV1(contract, parent string, value any) (string, error) {
	raw, err := json.Marshal(struct {
		Contract string `json:"contract"`
		Parent   string `json:"parent"`
		Value    any    `json:"value"`
	}{contract, parent, value})
	if err != nil {
		return "", err
	}
	return physicalInvocationResultDigest(string(raw)), nil
}

// 首读的原始条目与规范结果分开保存；复核前只发布无需复核的有效成员。
func initialReadProjectionV1(parent string, in k12.RecognitionLayoutInitialReadSettlementV1) (out k12.RecognitionLayoutInitialReadSettlementResultV1, err error) {
	if !validPrefixedSHA256DigestV2(in.PlanDigest) || in.SourcePhysicalUnit != k12.RecognitionPhysicalUnitWholePage || in.SourcePhysicalInvocationID == "" || !validPrefixedSHA256DigestV2(in.SourcePhysicalResultDigest) {
		return out, ErrModelPhysicalInvocationConflict
	}
	out.Classification, out.AmbiguityKind = in.Classification, in.AmbiguityKind
	out.SettlementDigest, err = recognitionReadDigestV1("recognition_layout_initial_read_settlement_v1", parent, in)
	if err != nil {
		return out, err
	}
	if in.Classification == k12.RecognitionLayoutBatchTerminalAmbiguousV2 {
		if len(in.Candidates) != 0 || !validRecognitionLayoutAmbiguityKindV2(in.AmbiguityKind) {
			return out, ErrModelPhysicalInvocationConflict
		}
		return out, nil
	}
	if in.Classification != k12.RecognitionLayoutBatchClassifiedV2 || in.AmbiguityKind != "" || len(in.Candidates) < 1 || len(in.Candidates) > 32 {
		return out, ErrModelPhysicalInvocationConflict
	}
	seen := map[string]bool{}
	for _, candidate := range in.Candidates {
		if candidate.CandidateID == "" || seen[candidate.CandidateID] {
			return out, ErrModelPhysicalInvocationConflict
		}
		seen[candidate.CandidateID] = true
		if _, err = canonicalRecognitionLayoutResultJSONV2(candidate.FirstReadJSON); err != nil {
			return out, err
		}
		receipt := k12.RecognitionLayoutInitialReadReceiptV1{RecognitionLayoutInitialCandidateV1: candidate}
		receipt.FirstReadDigest, err = k12.RecognitionLayoutFirstReadDigestV1(parent, in, candidate)
		if err != nil {
			return out, err
		}
		switch candidate.Classification {
		case k12.RecognitionLayoutCandidateValidV2, k12.RecognitionLayoutCandidateReviewRequiredV2:
			if candidate.ResultKind != k12.RecognitionLayoutCandidateQuestionV2 && candidate.ResultKind != k12.RecognitionLayoutCandidateNonQuestionV2 {
				return out, ErrModelPhysicalInvocationConflict
			}
			receipt.ResultDigest, err = recognitionLayoutCandidateResultDigestV2(parent, k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: in.PlanDigest, SourcePhysicalInvocationID: in.SourcePhysicalInvocationID, SourcePhysicalUnit: in.SourcePhysicalUnit, SourcePhysicalResultDigest: in.SourcePhysicalResultDigest}, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: candidate.CandidateID, ResultKind: candidate.ResultKind, ResultJSON: candidate.ResultJSON})
			if err != nil {
				return out, err
			}
		case k12.RecognitionLayoutCandidateInvalidV2, k12.RecognitionLayoutCandidateMissingV2:
			if candidate.ResultKind != "" || len(candidate.ResultJSON) != 0 {
				return out, ErrModelPhysicalInvocationConflict
			}
		default:
			return out, ErrModelPhysicalInvocationConflict
		}
		out.FirstReads = append(out.FirstReads, receipt)
		if candidate.Classification == k12.RecognitionLayoutCandidateValidV2 {
			out.FrozenResults = append(out.FrozenResults, k12.RecognitionLayoutCandidateResultReceiptV2{CandidateID: candidate.CandidateID, ResultKind: candidate.ResultKind, ResultDigest: receipt.ResultDigest})
		} else {
			digest, e := recognitionReadDigestV1("recognition_layout_review_member_v1", parent, struct {
				Plan, Settlement, Source, SourceDigest, Candidate string
				Classification                                    k12.RecognitionLayoutCandidateClassificationV2
				Round                                             int
			}{in.PlanDigest, out.SettlementDigest, in.SourcePhysicalInvocationID, in.SourcePhysicalResultDigest, candidate.CandidateID, candidate.Classification, 1})
			if e != nil {
				return out, e
			}
			out.ReviewAuthorizations = append(out.ReviewAuthorizations, k12.RecognitionLayoutReviewMemberAuthorizationV1{AuthorizationID: "review-member-v1-" + strings.TrimPrefix(digest, "sha256:"), AuthorizationDigest: digest, CandidateID: candidate.CandidateID, ReviewRound: 1})
		}
	}
	return out, nil
}

func loadInitialReadV1(ctx context.Context, q dbQueryer, owner, parent string) (stored recognitionInitialReadStoredV1, found bool, err error) {
	var raw sql.NullString
	err = q.QueryRowContext(ctx, `SELECT initial_read_settlement_json FROM k12_recognition_layout_plans WHERE agent_name=? AND parent_invocation_id=?`, owner, parent).Scan(&raw)
	if err != nil {
		return stored, false, err
	}
	if !raw.Valid {
		return stored, false, nil
	}
	if err = json.Unmarshal([]byte(raw.String), &stored); err != nil {
		return stored, false, err
	}
	want, e := initialReadProjectionV1(parent, stored.Settlement)
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(stored.Result)
	canonical, _ := json.Marshal(stored)
	if e != nil || !bytes.Equal(a, b) || string(canonical) != raw.String {
		return stored, false, ErrModelPhysicalInvocationConflict
	}
	return stored, true, nil
}

func validateInitialReadSourceV1(ctx context.Context, q dbQueryer, owner, parent string, plan k12.RecognitionLayoutPlanV2, in k12.RecognitionLayoutInitialReadSettlementV1) error {
	child, err := getModelPhysicalInvocationByIDVia(ctx, q, owner, in.SourcePhysicalInvocationID)
	if err != nil {
		return err
	}
	if child.ParentInvocationID != parent || child.PhysicalUnit != in.SourcePhysicalUnit || child.Status != k12.ModelInvocationSucceeded || child.ResultDigest != in.SourcePhysicalResultDigest || plan.ManifestInvocationID != child.PhysicalInvocationID || plan.ManifestResultDigest != child.ResultDigest || plan.AuthorizedPlanDigest != in.PlanDigest {
		return ErrModelPhysicalInvocationConflict
	}
	var content string
	if err = q.QueryRowContext(ctx, `SELECT result_content FROM k12_model_physical_invocations WHERE physical_invocation_id=?`, child.PhysicalInvocationID).Scan(&content); err != nil {
		return err
	}
	if physicalInvocationResultDigest(content) != child.ResultDigest {
		return ErrModelPhysicalInvocationConflict
	}
	if in.Classification == k12.RecognitionLayoutBatchTerminalAmbiguousV2 {
		return nil
	}
	if len(in.Candidates) != len(plan.Targets) {
		return ErrModelPhysicalInvocationConflict
	}
	entries, err := k12.CanonicalRecognitionLayoutInitialReadEntriesV1(content)
	if err != nil || len(entries) != len(in.Candidates) {
		return ErrModelPhysicalInvocationConflict
	}
	rawEntries := map[string]bool{}
	for _, entry := range entries {
		if rawEntries[string(entry)] {
			return ErrModelPhysicalInvocationConflict
		}
		rawEntries[string(entry)] = true
	}
	for i, candidate := range in.Candidates {
		if candidate.CandidateID != plan.Targets[i].TargetID || !rawEntries[string(candidate.FirstReadJSON)] {
			return ErrModelPhysicalInvocationConflict
		}
		var entry struct {
			SourceNumberPath   []string              `json:"source_number_path"`
			DisplayLabel       string                `json:"display_label"`
			SourceSectionPath  []string              `json:"source_section_path"`
			SourceSectionLabel string                `json:"source_section_label"`
			Region             k12.SourcePixelRegion `json:"region"`
		}
		if json.Unmarshal(candidate.FirstReadJSON, &entry) != nil {
			return ErrModelPhysicalInvocationConflict
		}
		target := plan.Targets[i]
		region := target.Region
		if target.OriginalRegion != nil {
			region = *target.OriginalRegion
		}
		numbers, _ := json.Marshal(entry.SourceNumberPath)
		wantNumbers, _ := json.Marshal(target.SourceNumberPath)
		sections := strings.Join(entry.SourceSectionPath, "\x00")
		wantSections := strings.Join(target.SourceSectionPath, "\x00")
		if entry.Region != region || entry.DisplayLabel != target.DisplayLabel || !bytes.Equal(numbers, wantNumbers) || sections != wantSections || entry.SourceSectionLabel != target.SourceSectionLabel {
			return ErrModelPhysicalInvocationConflict
		}
		delete(rawEntries, string(candidate.FirstReadJSON))
	}
	return nil
}

func (s *Store) AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx context.Context, owner, parent string, plan k12.RecognitionLayoutPlanV2, in k12.RecognitionLayoutInitialReadSettlementV1) (out k12.RecognitionLayoutInitialReadSettlementResultV1, created bool, err error) {
	if plan.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 || k12.ValidateRecognitionLayoutPlanV2(plan) != nil {
		return out, false, ErrModelPhysicalInvocationConflict
	}
	err = sqliteutil.RetryOnBusy(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `UPDATE k12_recognition_layout_plans SET updated_at=updated_at WHERE agent_name=? AND parent_invocation_id=?`, owner, parent); e != nil {
			return e
		}
		stored, found, e := loadInitialReadV1(ctx, tx, owner, parent)
		if e != nil {
			return e
		}
		if found {
			if in.PlanDigest != stored.Settlement.PlanDigest || in.SourcePhysicalInvocationID != stored.Settlement.SourcePhysicalInvocationID || in.SourcePhysicalUnit != stored.Settlement.SourcePhysicalUnit || in.SourcePhysicalResultDigest != stored.Settlement.SourcePhysicalResultDigest {
				return ErrModelPhysicalInvocationConflict
			}
			if in.Classification != "" {
				a, _ := json.Marshal(in)
				b, _ := json.Marshal(stored.Settlement)
				if !bytes.Equal(a, b) {
					return ErrModelPhysicalInvocationConflict
				}
			}
			if e = validateInitialReadSourceV1(ctx, tx, owner, parent, plan, stored.Settlement); e != nil {
				return e
			}
			if e = authorizeRecognitionLayoutPlanV2Via(ctx, tx, owner, parent, k12.RecognitionLayoutManifestSuccessV2{InvocationID: plan.ManifestInvocationID, ResultDigest: plan.ManifestResultDigest}, plan); e != nil {
				return e
			}
			out, created = stored.Result, false
			return tx.Commit()
		}
		out, e = initialReadProjectionV1(parent, in)
		if e != nil {
			return e
		}
		if e = validateInitialReadSourceV1(ctx, tx, owner, parent, plan, in); e != nil {
			return e
		}
		if e = authorizeRecognitionLayoutPlanV2Via(ctx, tx, owner, parent, k12.RecognitionLayoutManifestSuccessV2{InvocationID: plan.ManifestInvocationID, ResultDigest: plan.ManifestResultDigest}, plan); e != nil {
			return e
		}
		var planID string
		if e = tx.QueryRowContext(ctx, `SELECT plan_id FROM k12_recognition_layout_plans WHERE parent_invocation_id=? AND agent_name=?`, parent, owner).Scan(&planID); e != nil {
			return e
		}
		raw, e := json.Marshal(recognitionInitialReadStoredV1{Settlement: in, Result: out})
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE k12_recognition_layout_plans SET initial_read_settlement_json=?,status='running',updated_at=? WHERE plan_id=? AND initial_read_settlement_json IS NULL AND status='authorized'`, string(raw), nowUnix(), planID); e != nil {
			return e
		}
		for _, receipt := range out.FirstReads {
			if receipt.Classification == k12.RecognitionLayoutCandidateValidV2 {
				if e = insertInitialCandidateV1(ctx, tx, planID, parent, in.SourcePhysicalInvocationID, in.SourcePhysicalResultDigest, receipt.CandidateID, receipt.ResultKind, receipt.ResultDigest, receipt.ResultJSON); e != nil {
					return e
				}
			}
		}
		created = true
		return tx.Commit()
	})
	return
}

func insertInitialCandidateV1(ctx context.Context, tx *sql.Tx, planID, parent, source, digest, candidate string, kind k12.RecognitionLayoutCandidateResultKindV2, resultDigest string, raw json.RawMessage) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO k12_recognition_layout_candidate_results(plan_id,candidate_id,parent_invocation_id,source_physical_invocation_id,source_physical_result_digest,result_kind,result_digest,result_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, planID, candidate, parent, source, digest, kind, resultDigest, string(raw), nowUnix())
	return err
}

func reviewAuthorizationV1(parent string, in k12.RecognitionLayoutReviewBatchAuthorizationRequestV1) (out k12.RecognitionLayoutReviewBatchAuthorizationV1, err error) {
	want, e := k12.RecognitionLayoutReviewBatchInputDigestV1(in)
	if e != nil || in.InputDigest != want || !in.PhysicalUnit.Valid() || !strings.HasPrefix(string(in.PhysicalUnit), "layout_review_batch_") {
		return out, ErrModelPhysicalInvocationConflict
	}
	ids := make([]string, len(in.Members))
	for i, m := range in.Members {
		if m.ReviewRound != 1 || m.AuthorizationID == "" || !validPrefixedSHA256DigestV2(m.AuthorizationDigest) {
			return out, ErrModelPhysicalInvocationConflict
		}
		ids[i] = m.CandidateID
	}
	exact, e := k12.RecognitionLayoutTargetExactSetDigestV2(ids)
	if e != nil {
		return out, e
	}
	digest, e := recognitionReadDigestV1("recognition_layout_review_batch_authorization_v1", parent, in)
	if e != nil {
		return out, e
	}
	return k12.RecognitionLayoutReviewBatchAuthorizationV1{RecognitionLayoutReviewBatchAuthorizationRequestV1: in, AuthorizationID: "review-batch-v1-" + strings.TrimPrefix(digest, "sha256:"), AuthorizationDigest: digest, OrderedTargetIDs: ids, ExactSetDigest: exact, ReviewRound: 1}, nil
}

func loadReviewBatchesV1(ctx context.Context, q dbQueryer, planID, parent string) ([]k12.RecognitionLayoutReviewBatchAuthorizationV1, error) {
	rows, err := q.QueryContext(ctx, `SELECT review_authorization_json,batch_digest,input_digest,member_count,physical_unit FROM k12_recognition_layout_batches WHERE plan_id=? AND review_authorization_json IS NOT NULL ORDER BY ordinal`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []k12.RecognitionLayoutReviewBatchAuthorizationV1
	for rows.Next() {
		var raw, digest, input string
		var count int
		var unit k12.RecognitionPhysicalUnit
		if err := rows.Scan(&raw, &digest, &input, &count, &unit); err != nil {
			return nil, err
		}
		var auth k12.RecognitionLayoutReviewBatchAuthorizationV1
		if json.Unmarshal([]byte(raw), &auth) != nil {
			return nil, ErrModelPhysicalInvocationConflict
		}
		want, e := reviewAuthorizationV1(parent, auth.RecognitionLayoutReviewBatchAuthorizationRequestV1)
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(auth)
		if e != nil || string(b) != raw || !bytes.Equal(a, b) || digest != auth.AuthorizationDigest || input != auth.InputDigest || count != len(auth.Members) || unit != auth.PhysicalUnit {
			return nil, ErrModelPhysicalInvocationConflict
		}
		result = append(result, auth)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, auth := range result {
		members, err := q.QueryContext(ctx, `SELECT candidate_id FROM k12_recognition_layout_batch_members WHERE plan_id=? AND batch_id=? ORDER BY slot`, planID, auth.PhysicalUnit)
		if err != nil {
			return nil, err
		}
		var ids []string
		for members.Next() {
			var id string
			if err := members.Scan(&id); err != nil {
				members.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		err = members.Err()
		closeErr := members.Close()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		actual, _ := json.Marshal(ids)
		expected, _ := json.Marshal(auth.OrderedTargetIDs)
		if !bytes.Equal(actual, expected) {
			return nil, ErrModelPhysicalInvocationConflict
		}
	}
	return result, nil
}

func (s *Store) AuthorizeRecognitionLayoutReviewBatchV1(ctx context.Context, owner, parent string, in k12.RecognitionLayoutReviewBatchAuthorizationRequestV1) (out k12.RecognitionLayoutReviewBatchAuthorizationV1, created bool, err error) {
	out, err = reviewAuthorizationV1(parent, in)
	if err != nil {
		return
	}
	err = sqliteutil.RetryOnBusy(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `UPDATE k12_recognition_layout_plans SET updated_at=updated_at WHERE agent_name=? AND parent_invocation_id=?`, owner, parent); e != nil {
			return e
		}
		authority, e := loadRecognitionLayoutFinalizationAuthorityV2(ctx, tx, owner, parent)
		if e != nil {
			return e
		}
		if authority.Plan.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 || authority.Plan.AuthorizedPlanDigest != in.PlanDigest {
			return ErrModelPhysicalInvocationConflict
		}
		initial, found, e := loadInitialReadV1(ctx, tx, owner, parent)
		if e != nil {
			return e
		}
		if !found || initial.Result.Classification != k12.RecognitionLayoutBatchClassifiedV2 {
			return ErrModelPhysicalInvocationConflict
		}
		batches, e := loadReviewBatchesV1(ctx, tx, authority.PlanID, parent)
		if e != nil {
			return e
		}
		for _, batch := range batches {
			if batch.PhysicalUnit == in.PhysicalUnit {
				a, _ := json.Marshal(batch)
				b, _ := json.Marshal(out)
				if !bytes.Equal(a, b) {
					return ErrModelPhysicalInvocationConflict
				}
				created = false
				return tx.Commit()
			}
		}
		if authority.Parent.Status != k12.ModelInvocationSent || authority.Status != "running" {
			return records.ErrIllegalTransition
		}
		unit, e := k12.RecognitionLayoutReviewUnitV1(len(batches) + 1)
		if e != nil || unit != in.PhysicalUnit {
			return ErrModelPhysicalInvocationConflict
		}
		used := map[string]bool{}
		for _, batch := range batches {
			for _, id := range batch.OrderedTargetIDs {
				used[id] = true
			}
		}
		remaining := []k12.RecognitionLayoutReviewMemberAuthorizationV1{}
		for _, member := range initial.Result.ReviewAuthorizations {
			if !used[member.CandidateID] {
				remaining = append(remaining, member)
			}
		}
		if len(in.Members) > len(remaining) {
			return ErrModelPhysicalInvocationConflict
		}
		width, height := 0, 0
		for i, member := range in.Members {
			if member != remaining[i] {
				return ErrModelPhysicalInvocationConflict
			}
			found := false
			for _, target := range authority.Plan.Targets {
				if target.TargetID == member.CandidateID {
					width = max(width, target.Region.Width)
					height += target.Region.Height
					found = true
					break
				}
			}
			if !found {
				return ErrModelPhysicalInvocationConflict
			}
		}
		if len(in.Members) > 1 {
			width += 16
			height += 16 + 8*(len(in.Members)-1)
			if height > 768 {
				return ErrModelPhysicalInvocationConflict
			}
		}
		if in.ImageWidth != width || in.ImageHeight != height {
			return ErrModelPhysicalInvocationConflict
		}
		var deadline int64
		if e = tx.QueryRowContext(ctx, `SELECT stage_deadline_at FROM k12_recognition_layout_plans WHERE plan_id=?`, authority.PlanID).Scan(&deadline); e != nil {
			return e
		}
		if deadline <= time.Now().UnixMilli() {
			return records.ErrIllegalTransition
		}
		raw, e := json.Marshal(out)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO k12_recognition_layout_batches(plan_id,batch_id,ordinal,physical_unit,member_count,batch_digest,input_digest,created_at,review_authorization_json) VALUES(?,?,?,?,?,?,?,?,?)`, authority.PlanID, in.PhysicalUnit, len(batches)+1, in.PhysicalUnit, len(in.Members), out.AuthorizationDigest, in.InputDigest, nowUnix(), string(raw)); e != nil {
			return e
		}
		for slot, id := range out.OrderedTargetIDs {
			if _, e = tx.ExecContext(ctx, `INSERT INTO k12_recognition_layout_batch_members(plan_id,batch_id,slot,candidate_id,created_at) VALUES(?,?,?,?,?)`, authority.PlanID, in.PhysicalUnit, slot, id, nowUnix()); e != nil {
				return e
			}
		}
		created = true
		return tx.Commit()
	})
	return
}

func validateReviewPhysicalAuthorizationV1(ctx context.Context, q dbQueryer, parent k12.ModelInvocation, child k12.ModelPhysicalInvocation, planID string) error {
	batches, err := loadReviewBatchesV1(ctx, q, planID, parent.InvocationID)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if batch.PhysicalUnit == child.PhysicalUnit {
			if batch.PlanDigest != child.PlanDigest || batch.ExactSetDigest != child.CandidateExactSetDigest {
				return ErrModelPhysicalInvocationConflict
			}
			return nil
		}
	}
	return ErrModelPhysicalInvocationConflict
}

func reviewSettlementProjectionV1(parent string, auth k12.RecognitionLayoutReviewBatchAuthorizationV1, in k12.RecognitionLayoutReviewBatchSettlementV1) (out k12.RecognitionLayoutReviewBatchSettlementResultV1, err error) {
	if in.PlanDigest != auth.PlanDigest || in.AuthorizationID != auth.AuthorizationID || in.AuthorizationDigest != auth.AuthorizationDigest || in.SourcePhysicalUnit != auth.PhysicalUnit || in.SourcePhysicalInvocationID == "" || !validPrefixedSHA256DigestV2(in.SourcePhysicalResultDigest) {
		return out, ErrModelPhysicalInvocationConflict
	}
	out.Classification = in.Classification
	out.SettlementDigest, err = recognitionReadDigestV1("recognition_layout_review_batch_settlement_v1", parent, in)
	if err != nil {
		return
	}
	if in.Classification == k12.RecognitionLayoutBatchTerminalAmbiguousV2 {
		if len(in.Candidates) != 0 || !validRecognitionLayoutAmbiguityKindV2(in.AmbiguityKind) {
			return out, ErrModelPhysicalInvocationConflict
		}
		out.UnresolvedCandidateIDs = append([]string(nil), auth.OrderedTargetIDs...)
		return out, nil
	}
	if in.Classification != k12.RecognitionLayoutBatchClassifiedV2 || in.AmbiguityKind != "" || len(in.Candidates) != len(auth.OrderedTargetIDs) {
		return out, ErrModelPhysicalInvocationConflict
	}
	for i, candidate := range in.Candidates {
		if candidate.CandidateID != auth.OrderedTargetIDs[i] {
			return out, ErrModelPhysicalInvocationConflict
		}
		switch candidate.Classification {
		case k12.RecognitionLayoutCandidateValidV2:
			if candidate.ResultKind != k12.RecognitionLayoutCandidateQuestionV2 && candidate.ResultKind != k12.RecognitionLayoutCandidateNonQuestionV2 {
				return out, ErrModelPhysicalInvocationConflict
			}
			digest, e := recognitionLayoutCandidateResultDigestV2(parent, k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: in.PlanDigest, SourcePhysicalInvocationID: in.SourcePhysicalInvocationID, SourcePhysicalUnit: in.SourcePhysicalUnit, SourcePhysicalResultDigest: in.SourcePhysicalResultDigest}, candidate)
			if e != nil {
				return out, e
			}
			out.FrozenResults = append(out.FrozenResults, k12.RecognitionLayoutCandidateResultReceiptV2{CandidateID: candidate.CandidateID, ResultKind: candidate.ResultKind, ResultDigest: digest})
		case k12.RecognitionLayoutCandidateInvalidV2, k12.RecognitionLayoutCandidateMissingV2:
			if candidate.ResultKind != "" || len(candidate.ResultJSON) != 0 {
				return out, ErrModelPhysicalInvocationConflict
			}
			out.UnresolvedCandidateIDs = append(out.UnresolvedCandidateIDs, candidate.CandidateID)
		default:
			return out, ErrModelPhysicalInvocationConflict
		}
	}
	return out, nil
}

func loadReviewSettlementV1(ctx context.Context, q dbQueryer, planID, parent string, auth k12.RecognitionLayoutReviewBatchAuthorizationV1) (stored recognitionReviewStoredV1, found bool, err error) {
	var raw sql.NullString
	var digest string
	err = q.QueryRowContext(ctx, `SELECT review_settlement_json,settlement_digest FROM k12_recognition_layout_batch_settlements WHERE plan_id=? AND batch_id=?`, planID, auth.PhysicalUnit).Scan(&raw, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return stored, false, nil
	}
	if err != nil {
		return stored, false, err
	}
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &stored) != nil {
		return stored, false, ErrModelPhysicalInvocationConflict
	}
	want, e := reviewSettlementProjectionV1(parent, auth, stored.Settlement)
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(stored.Result)
	canonical, _ := json.Marshal(stored)
	if e != nil || !bytes.Equal(a, b) || digest != want.SettlementDigest || string(canonical) != raw.String {
		return stored, false, ErrModelPhysicalInvocationConflict
	}
	return stored, true, nil
}

func (s *Store) SettleRecognitionLayoutReviewBatchV1(ctx context.Context, owner, parent string, in k12.RecognitionLayoutReviewBatchSettlementV1) (out k12.RecognitionLayoutReviewBatchSettlementResultV1, created bool, err error) {
	err = sqliteutil.RetryOnBusy(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `UPDATE k12_recognition_layout_plans SET updated_at=updated_at WHERE agent_name=? AND parent_invocation_id=?`, owner, parent); e != nil {
			return e
		}
		authority, e := loadRecognitionLayoutFinalizationAuthorityV2(ctx, tx, owner, parent)
		if e != nil {
			return e
		}
		batches, e := loadReviewBatchesV1(ctx, tx, authority.PlanID, parent)
		if e != nil {
			return e
		}
		var auth k12.RecognitionLayoutReviewBatchAuthorizationV1
		for _, batch := range batches {
			if batch.PhysicalUnit == in.SourcePhysicalUnit {
				auth = batch
				break
			}
		}
		stored, found, e := loadReviewSettlementV1(ctx, tx, authority.PlanID, parent, auth)
		if e != nil {
			return e
		}
		if found {
			if in.PlanDigest != stored.Settlement.PlanDigest || in.AuthorizationID != stored.Settlement.AuthorizationID || in.AuthorizationDigest != stored.Settlement.AuthorizationDigest || in.SourcePhysicalInvocationID != stored.Settlement.SourcePhysicalInvocationID || in.SourcePhysicalResultDigest != stored.Settlement.SourcePhysicalResultDigest {
				return ErrModelPhysicalInvocationConflict
			}
			if in.Classification != "" {
				a, _ := json.Marshal(in)
				b, _ := json.Marshal(stored.Settlement)
				if !bytes.Equal(a, b) {
					return ErrModelPhysicalInvocationConflict
				}
			}
			in = stored.Settlement
		}
		out, e = reviewSettlementProjectionV1(parent, auth, in)
		if e != nil {
			return e
		}
		if _, e = loadRecognitionLayoutFinalPhysicalEvidenceV2(ctx, tx, authority.Parent, in.SourcePhysicalInvocationID, in.SourcePhysicalUnit, in.PlanDigest, auth.ExactSetDigest, in.SourcePhysicalResultDigest); e != nil {
			return e
		}
		if found {
			created = false
			return tx.Commit()
		}
		if authority.Parent.Status != k12.ModelInvocationSent || authority.Status != "running" {
			return records.ErrIllegalTransition
		}
		raw, e := json.Marshal(recognitionReviewStoredV1{Settlement: in, Result: out})
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO k12_recognition_layout_batch_settlements(plan_id,batch_id,parent_invocation_id,source_physical_invocation_id,source_physical_unit,source_physical_result_digest,classification,ambiguity_kind,settlement_digest,created_at,review_settlement_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, authority.PlanID, in.SourcePhysicalUnit, parent, in.SourcePhysicalInvocationID, in.SourcePhysicalUnit, in.SourcePhysicalResultDigest, in.Classification, in.AmbiguityKind, out.SettlementDigest, nowUnix(), string(raw)); e != nil {
			return e
		}
		for _, candidate := range in.Candidates {
			if candidate.Classification == k12.RecognitionLayoutCandidateValidV2 {
				receipt := nextRecognitionLayoutCandidateReceiptV2(out.FrozenResults, candidate.CandidateID)
				if e = insertInitialCandidateV1(ctx, tx, authority.PlanID, parent, in.SourcePhysicalInvocationID, in.SourcePhysicalResultDigest, candidate.CandidateID, candidate.ResultKind, receipt.ResultDigest, candidate.ResultJSON); e != nil {
					return e
				}
			}
		}
		created = true
		return tx.Commit()
	})
	return
}
