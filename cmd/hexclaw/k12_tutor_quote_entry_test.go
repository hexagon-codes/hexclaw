package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

func tutorQuoteEntryFixture(t *testing.T, s *k12storage.Store) {
	t.Helper()
	ctx := context.Background()
	const agent = "child-tutor"
	if _, err := s.DB().Exec(`INSERT INTO agents(name) VALUES(?)`, agent); err != nil {
		t.Fatal(err)
	}
	attempt := k12.Attempt{AttemptID: "quote-attempt", AgentName: agent, SubmissionID: "quote-submission", ProblemID: "quote-question", AnswerState: "present", AnswerRaw: "4", AnswerMarkdown: "4", ConfirmedVersion: 1, InputDigest: "sha256:input", CreatedAt: 100, UpdatedAt: 100}
	if err := s.PutProblemAttemptSnapshot(ctx, k12.ProblemAttemptSnapshot{Problems: []k12.Problem{{ProblemID: attempt.ProblemID, AgentName: agent, SubmissionID: attempt.SubmissionID, PageAssetID: "quote-page", Ordinal: 1, ProblemKind: k12.ProblemKindStandalone, Subject: "数学", StemRaw: "2+2=?", StemMarkdown: "2+2=?", CanonicalVersion: 1, CreatedAt: 100, UpdatedAt: 100}}, Attempts: []k12.Attempt{attempt}}); err != nil {
		t.Fatal(err)
	}
	job, err := k12.NewGradingJobRecord(agent, "quote-session", k12.GradingJobFields{SubmissionID: attempt.SubmissionID, SourceKind: "test", IdempotencyKey: "quote-job", ConfirmationState: k12.GradingConfirmationPending, AnchorState: k12.GradingAnchorPending, ModelSnapshot: k12.GradingModelSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture/fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, job); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 2)
	for _, operation := range []k12.GradingItemOperation{k12.GradingItemOperationSolve, k12.GradingItemOperationGrade} {
		id := "quote-" + string(operation)
		in := k12.GradingItemInvocation{InvocationID: id, AgentName: agent, JobID: job.RecordID, ProblemID: attempt.ProblemID, AttemptID: attempt.AttemptID, Operation: operation, OperationAttempt: 1, RequestDigest: "sha256:request-" + id, InputRevision: 1, InputDigest: attempt.InputDigest, RouteSnapshot: k12.GradingModelSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture/fixture"}, CreatedAt: 100}
		if _, _, err := s.PrepareGradingItemInvocation(ctx, in); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkGradingItemInvocationSent(ctx, agent, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MarkGradingItemInvocationSucceeded(ctx, agent, id, "sha256:"+id, `{"ok":true}`); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	result, err := json.Marshal(usecase.PhotoGradeItem{Status: usecase.PhotoCorrect})
	if err != nil {
		t.Fatal(err)
	}
	item, _, err := s.CommitGradingAssessmentItem(ctx, k12.GradingAssessmentItem{AgentName: agent, JobID: job.RecordID, ProblemID: attempt.ProblemID, AttemptID: attempt.AttemptID, ConfirmedVersion: 1, InputDigest: attempt.InputDigest, Status: k12.GradingAssessmentCorrect, ResultJSON: string(result), ResultDigest: "sha256:quote-result", SolveInvocationID: ids[0], GradeInvocationID: ids[1], ProjectionStatus: k12.GradingProjectionCommitted, CreatedAt: 200}, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	question, err := json.Marshal(usecase.RecognizedQuestion{ProblemID: attempt.ProblemID, ConfirmedVersion: 1, SourceNumberPath: []string{"3"}, Question: "2+2=?", StudentAnswer: "4"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTutorSourceRefs(ctx, []k12storage.TutorContextRef{{OwnerScope: usecase.DefaultLocalOwnerScope, AgentName: agent, ConversationKey: k12storage.TutorConversationKey("dingtalk", "bot-1", "family-group"), MessageID: "quote-photo", Kind: "source", JobID: job.RecordID, ProblemID: attempt.ProblemID, InputRevision: 1, ResultDigest: item.ResultDigest, PrintedNumber: "3", QuestionJSON: string(question)}}); err != nil {
		t.Fatal(err)
	}
	route := k12.ImageTaskRouteSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture/fixture", Capability: "vision", SelectionSource: "explicit", PolicyVersion: "image-task-routing-v1", PromptVersion: "image-task-classifier-v1"}
	dispatch := k12.ImageTaskDispatch{DispatchID: "quote-task", AgentName: agent, LearnerID: "child", SourceKind: k12.ImageTaskSourceDesktop, SourceRef: "quote-photo", SourceSessionID: "quote-session", SourceAssetRefs: []string{"asset://child-tutor/" + strings.Repeat("a", 64) + ".png"}, SourceDigest: "sha256:source", TaskIntent: k12.ImageTaskIntentUnknown, Status: k12.ImageTaskStatusRouting, ClassificationRouteSnapshot: route, RoutePolicySnapshot: route, ClassificationInvocationID: "quote-classify", IdempotencyKey: "quote-dispatch", RequestDigest: "sha256:dispatch", AttemptGeneration: 1, CreatedAt: 100, UpdatedAt: 100}
	if _, _, err := s.PrepareImageTaskDispatch(ctx, dispatch, k12.ImageTaskInvocation{InvocationID: "quote-classify", AgentName: agent, DispatchID: dispatch.DispatchID, Operation: k12.ImageTaskOperationClassification, OperationKey: "quote-classification", RequestDigest: "sha256:classification", RouteSnapshot: route, Status: k12.ImageTaskInvocationPrepared, Attempt: 1, CreatedAt: 100, UpdatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO k12_homework_submissions (submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,task_intent,status,grading_job_id,idempotency_key,created_at,updated_at) VALUES('quote-submission','quote-task',?,'child','desktop','quote-photo','[]','completed_homework','completed',?,'quote-homework',100,100)`, agent, job.RecordID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE k12_image_task_invocations SET status='succeeded',result_digest='sha256:title',result_json=? WHERE invocation_id='quote-classify'`, `{"WorkTitleCandidate":{"value":"五升六数学《暑假作业》每日一练 Day5","source":"image_vision","confidence":0.99,"evidence_ref":"asset_index:0#试卷顶部标题区域"}}`); err != nil {
		t.Fatal(err)
	}
}

func TestK12TutorQuoteEntryStopsUnmatchedAndIdentityBeforeProvider(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "quote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	registry := scenario.NewRegistry()
	if err := registry.Assemble(k12.Pack(k12.NewCurriculumStub())); err != nil {
		t.Fatal(err)
	}
	records := k12storage.NewStore(store.DB(), registry.Records)
	tutorQuoteEntryFixture(t, records)
	router := k12PhotoTestRouter(t, true, k12TutorScenario)
	agent, _ := router.GetAgent("child-tutor")
	agent.Metadata[k12.MetaKeyChildName] = "小明"
	agent.Metadata[k12.MetaKeyPromptContractVersion] = k12.TutorIdentityPromptContractVersion
	router.LoadAll([]agentrouter.AgentConfig{*agent}, "child-tutor", []agentrouter.Rule{
		{Platform: "dingtalk", InstanceID: "bot-1", ChatID: "family-group", AgentName: "child-tutor"},
		{Platform: "dingtalk", InstanceID: "bot-1", ChatID: "new-family", AgentName: "child-tutor"},
	})
	policy := newK12TutorIdentityPolicy(router, nil)
	policy.followup = &usecase.Deps{Records: records}
	policy.resolveInstanceID = func(platform, instanceRef string) (string, error) {
		if platform != "dingtalk" || instanceRef != "Family Tutor" {
			t.Fatalf("unexpected physical instance reference: %q / %q", platform, instanceRef)
		}
		return "bot-1", nil
	}
	provider := &k12VisionRequestCaptureProvider{}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled = false
	cfg.FileMemory.Enabled = false
	cfg.Memory.LongTerm.Enabled = false
	cfg.LLM.Cache.Enabled = false
	cfg.LLM.Default = "capture"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"capture": {Model: "fixture"}}
	selector := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"capture": provider})
	eng := engine.NewReActEngine(cfg, selector, store, skill.NewRegistry())
	eng.SetAgentRouter(router)
	eng.SetAgentSystemPromptPolicy(policy)
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(ctx) })
	now := time.Now()
	if err := store.CreateSession(ctx, &storage.Session{ID: "quote-history", UserID: "parent-1", Platform: "dingtalk", InstanceID: "bot-1", ChatID: "family-group", Title: "旧卷 P-2638-01", Status: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: "old-result", SessionID: "quote-history", Role: "assistant", Content: "这是旧卷 P-2638-01。", ContentType: "text", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	message := func(id, query string) *adapter.Message {
		return &adapter.Message{ID: id, Platform: adapter.PlatformDingtalk, InstanceID: "Family Tutor", ChatID: "family-group", UserID: "parent-1", SessionID: "quote-history", Content: query, ReplyTo: "native-result", Metadata: map[string]string{"user_locale": "zh-CN"}}
	}
	for _, tc := range []struct {
		name, query, quoted, locale, want string
		ambiguous                         bool
	}{
		{"missing", "这条批改对应哪次作业？", "", "zh-CN", "暂时无法将这条引用关联", false},
		{"identity", "这条批改对应哪次作业？", "quote-task", "zh-CN", "五升六数学《暑假作业》每日一练 Day5", false},
		{"summary", "这份作业批改怎么样？", "quote-task", "zh-CN", "1 正确", false},
		{"conflict", "HW-another 是哪份作业？", "quote-task", "zh-CN", "不同的作业编号", false},
		{"ambiguous", "这是哪份作业？", "", "zh-CN", "不同的作业编号", true},
		{"english", "Which homework is this?", "", "en", "I couldn't link this reference", false},
		{"uyghur", "HW-missing", "", "ug", "بۇ نەقىلنى", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := message(tc.name, tc.query)
			msg.Metadata["user_locale"] = tc.locale
			msg.Metadata["quoted_homework_id"] = tc.quoted
			if tc.ambiguous {
				msg.Metadata["quoted_homework_ambiguous"] = "true"
			}
			reply, err := policy.ProcessDingtalkFollowup(ctx, msg, eng.Process)
			if err != nil || reply == nil || !strings.Contains(reply.Content, tc.want) || strings.Contains(reply.Content, "P-2638-01") {
				t.Fatalf("reply=%+v err=%v", reply, err)
			}
			if provider.calls != 0 {
				t.Fatalf("deterministic query called provider %d times", provider.calls)
			}
		})
	}
	msg := message("question", "第三题再讲一下")
	msg.Metadata["quoted_homework_id"] = "quote-task"
	if reply, err := policy.ProcessDingtalkFollowup(ctx, msg, eng.Process); err != nil || reply == nil {
		t.Fatalf("question explanation: %+v %v", reply, err)
	}
	if provider.calls != 1 {
		t.Fatalf("question explanation provider calls=%d", provider.calls)
	}
	var sent strings.Builder
	for _, m := range provider.request.Messages {
		sent.WriteString(m.Content)
	}
	if !strings.Contains(sent.String(), "2+2=?") || !strings.Contains(sent.String(), "sha256:quote-result") || !strings.Contains(sent.String(), "Answer the parent's current question directly") {
		t.Fatalf("original question/current result missing: %s", sent.String())
	}
	msg = message("ordinary", "Explain why the sky is blue")
	msg.ReplyTo = ""
	if _, err := policy.ProcessDingtalkFollowup(ctx, msg, eng.Process); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("ordinary chat intercepted: provider calls=%d", provider.calls)
	}
	msg = message("new-question", "这题怎么做：1+1=？")
	msg.ReplyTo, msg.ChatID, msg.SessionID = "", "new-family", ""
	if _, err := policy.ProcessDingtalkFollowup(ctx, msg, eng.Process); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 3 {
		t.Fatalf("new question without an old reference intercepted: provider calls=%d", provider.calls)
	}
	for _, m := range provider.request.Messages {
		if strings.Contains(m.Content, "Homework follow-up context") {
			t.Fatal("new question received an unrelated stored assessment")
		}
	}
	var assessments, invocations, followups int
	if err := store.DB().QueryRow(`SELECT (SELECT count(*) FROM k12_grading_assessment_items),(SELECT count(*) FROM k12_grading_item_invocations),(SELECT count(*) FROM k12_tutor_context_refs WHERE kind='followup')`).Scan(&assessments, &invocations, &followups); err != nil || assessments != 1 || invocations != 2 || followups != 1 {
		t.Fatalf("explanation created assessment or duplicate association: %d/%d/%d %v", assessments, invocations, followups, err)
	}
}
