package k12storage_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func setTutorQuoteClassification(t *testing.T, s *k12storage.Store, dispatchID, title string) {
	t.Helper()
	raw, err := json.Marshal(k12storage.ImageTaskRoutingDecision{WorkTitleCandidate: &k12.FactCandidate{
		Value: title, Source: "image_vision", Confidence: .99, EvidenceRef: "asset_index:0#试卷顶部标题区域",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE k12_image_task_invocations SET status='succeeded',result_json=?,result_digest=? WHERE invocation_id=?`, string(raw), "sha256:title-"+dispatchID, "classify-"+dispatchID); err != nil {
		t.Fatal(err)
	}
}

func tutorQuoteSnapshot(t *testing.T, s *k12storage.Store) string {
	t.Helper()
	var snapshot []any
	for _, table := range []string{"k12_image_task_dispatches", "k12_image_task_invocations", "k12_homework_submissions", "k12_grading_jobs", "k12_grading_assessment_items", "k12_assessment_corrections", "k12_grading_item_invocations", "k12_grading_final_artifacts", "k12_tutor_context_refs", "k12_mistakes", "outbox_events"} {
		rows, err := s.DB().Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, table, columns)
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				t.Fatal(err)
			}
			snapshot = append(snapshot, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTutorQuoteResolutionUsesExactTitleCurrentResultAndReadOnlyReplay(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	first, original, attempt := tutorRefFixture(t, s, "quote-first", "photo-first")
	second, _, _ := tutorRefFixture(t, s, "quote-second", "photo-second")
	first.ConversationKey = k12storage.TutorConversationKey("dingtalk", "bot", "parent")
	second.ConversationKey = first.ConversationKey
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{first, second}); err != nil {
		t.Fatal(err)
	}
	tutorHomeworkFixture(t, s, first, "quote-first")
	tutorHomeworkFixture(t, s, second, "quote-second")
	const title = "五升六数学《暑假作业》每日一练 Day5"
	setTutorQuoteClassification(t, s, "quote-first", title)
	setTutorQuoteClassification(t, s, "quote-second", "旧卷 P-2638-01")
	artifact := k12.GradingFinalArtifact{AgentName: first.AgentName, JobID: first.JobID,
		StructureVersion: k12.GradingFinalArtifactStructureVersion, CoverageStatus: k12.GradingFinalArtifactCoverageComplete,
		TotalCount: 1, PublishedCount: 1, OrderedCurrentDigestsJSON: `["sha256:assessment-result"]`,
		CanonicalMarkdown: "# 已有批改\n第3题正确", SummaryInvocationID: "quote-summary"}
	artifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(artifact)
	if _, _, err := s.CommitGradingFinalArtifact(ctx, artifact, 0); err != nil {
		t.Fatal(err)
	}
	deps := &usecase.Deps{Records: s}
	input := usecase.TutorFollowupInput{OwnerScope: first.OwnerScope, AgentName: first.AgentName, ConversationKey: first.ConversationKey,
		MessageID: "query", Query: "这条批改对应哪次作业？", ReplyTo: "native-result", QuotedHomeworkID: "quote-first", Locale: "zh-CN"}
	before := tutorQuoteSnapshot(t, s)
	for range 2 {
		got, err := deps.ResolveTutorFollowup(ctx, input)
		if err != nil || got.Kind != usecase.TutorFollowupHomework || got.Reply != "这条结果对应“"+title+"”，作业编号：HW-quote-first。" || strings.Contains(got.Directive, second.JobID) || !strings.Contains(got.Directive, artifact.ArtifactDigest) {
			t.Fatalf("exact identity: %+v %v", got, err)
		}
	}
	if after := tutorQuoteSnapshot(t, s); before != after {
		t.Fatal("identity replay mutated saved source, results or learning state")
	}
	input.QuotedHomeworkID, input.ReplyTo = "", "photo-first"
	if got, err := deps.ResolveTutorFollowup(ctx, input); err != nil || got.Kind != usecase.TutorFollowupHomework || !strings.Contains(got.Reply, title) {
		t.Fatalf("original photo reply lost: %+v %v", got, err)
	}
	corrected := original
	corrected.GradeInvocationID = correctionProof(t, s, original.JobID, attempt, k12.GradingItemOperationGrade, 3, true)
	corrected.Status, corrected.ResultJSON, corrected.ResultDigest = k12.GradingAssessmentUntrusted, `{"Status":"untrusted"}`, "sha256:quote-corrected"
	if _, _, err := s.AppendGradingAssessmentCorrection(ctx, k12.GradingAssessmentCorrection{CorrectionID: "quote-correction", OriginalResultDigest: original.ResultDigest, Reason: k12.AssessmentCorrectionGrading, Assessment: corrected}, k12storage.GradingAssessmentEffects{}); err != nil {
		t.Fatal(err)
	}
	input.Query = "这份作业批改怎么样？"
	before = tutorQuoteSnapshot(t, s)
	got, err := deps.ResolveTutorFollowup(ctx, input)
	if err != nil || got.Kind != usecase.TutorFollowupHomework || !strings.Contains(got.Reply, "1 结果待核实") || strings.Contains(got.Reply, "1 正确") || !strings.Contains(got.Directive, corrected.ResultDigest) || strings.Contains(got.Directive, original.ResultDigest) {
		t.Fatalf("stale assessment used: %+v %v", got, err)
	}
	if after := tutorQuoteSnapshot(t, s); before != after {
		t.Fatal("summary mutated current or historical receipts")
	}
}

func TestTutorQuoteResolutionRejectsConflictsScopeAndStaleSources(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	ref, _, _ := tutorRefFixture(t, s, "quote-scope", "photo")
	ref.ConversationKey = k12storage.TutorConversationKey("dingtalk", "bot", "parent")
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{ref}); err != nil {
		t.Fatal(err)
	}
	tutorHomeworkFixture(t, s, ref, "quote-scope")
	deps := &usecase.Deps{Records: s}
	base := usecase.TutorFollowupInput{OwnerScope: ref.OwnerScope, AgentName: ref.AgentName, ConversationKey: ref.ConversationKey, MessageID: "query", Query: "这是哪份作业？", QuotedHomeworkID: "quote-scope"}
	// 缺标题的旧记录只报告编号，不从其他分类回执推断。
	got, err := deps.ResolveTutorFollowup(ctx, base)
	if err != nil || got.Kind != usecase.TutorFollowupHomework || got.Reply != "这条结果对应作业 HW-quote-scope。" {
		t.Fatalf("missing title: %+v %v", got, err)
	}
	before := tutorQuoteSnapshot(t, s)
	for _, tc := range []struct {
		name   string
		change func(*usecase.TutorFollowupInput)
	}{
		{"owner", func(in *usecase.TutorFollowupInput) { in.OwnerScope = "other" }},
		{"child", func(in *usecase.TutorFollowupInput) { in.AgentName = "other" }},
		{"instance", func(in *usecase.TutorFollowupInput) {
			in.ConversationKey = k12storage.TutorConversationKey("dingtalk", "other", "parent")
		}},
		{"chat", func(in *usecase.TutorFollowupInput) {
			in.ConversationKey = k12storage.TutorConversationKey("dingtalk", "bot", "other")
		}},
		{"query_conflict", func(in *usecase.TutorFollowupInput) { in.Query = "HW-other 是哪份作业？" }},
		{"quote_conflict", func(in *usecase.TutorFollowupInput) { in.QuotedHomeworkID = ""; in.QuotedHomeworkAmbiguous = true }},
		{"missing", func(in *usecase.TutorFollowupInput) { in.QuotedHomeworkID = "deleted" }},
		{"native_image", func(in *usecase.TutorFollowupInput) { in.QuotedHomeworkID = ""; in.ReplyTo = "unmapped-native-image" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.change(&in)
			got, err := deps.ResolveTutorFollowup(ctx, in)
			if err != nil || got.Kind != usecase.TutorFollowupUnmatched || strings.Contains(got.Reply, "对应作业 HW-") {
				t.Fatalf("reference guessed: %+v %v", got, err)
			}
		})
	}
	if after := tutorQuoteSnapshot(t, s); !reflect.DeepEqual(before, after) {
		t.Fatal("unmatched queries mutated saved records")
	}
	base.Query = "HW-quote-scope HW-quote-scope 对应哪次作业？"
	if got, err := deps.ResolveTutorFollowup(ctx, base); err != nil || got.Kind != usecase.TutorFollowupHomework {
		t.Fatalf("same ID treated as conflict: %+v %v", got, err)
	}
	if _, err := s.DB().Exec(`UPDATE k12_grading_assessment_items SET current_disposition='superseded' WHERE job_id=?`, ref.JobID); err != nil {
		t.Fatal(err)
	}
	if got, err := deps.ResolveTutorFollowup(ctx, base); err != nil || got.Kind != usecase.TutorFollowupUnmatched {
		t.Fatalf("stale source selected: %+v %v", got, err)
	}
}
