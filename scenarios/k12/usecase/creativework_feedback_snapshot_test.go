package usecase_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type feedbackSnapshotUnavailable struct{}

func (feedbackSnapshotUnavailable) Error() string                   { return "controlled service unavailable" }
func (feedbackSnapshotUnavailable) ProviderResponseStatusCode() int { return 503 }

func feedbackSnapshotAdapter(t *testing.T, marker string, generate engineadapter.WorkFeedbackGenerateFunc) *engineadapter.SolveAdapter {
	t.Helper()
	body, err := fs.ReadFile(k12.BundledSkillsFS(), "skills/writing-feedback.md")
	if err != nil {
		t.Fatal(err)
	}
	return engineadapter.NewSolveAdapter(nil,
		engineadapter.WithWorkFeedbackGen(generate),
		engineadapter.WithWorkFeedbackSkillLoader(func(string) (string, error) {
			return string(body) + "\n" + marker, nil
		}),
	)
}

func assertFeedbackSnapshot(t *testing.T, raw, marker string) usecase.WorkFeedbackRequest {
	t.Helper()
	var request usecase.WorkFeedbackRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	if request.FrozenContext == nil || !strings.Contains(request.FrozenContext.Prompt, marker) {
		t.Fatalf("complete frozen method missing: %s", raw)
	}
	sum := sha256.Sum256([]byte(request.FrozenContext.Prompt))
	if request.FrozenContext.ContentDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal("stored method digest does not describe the actual prompt")
	}
	return request
}

func TestWorkFeedbackFrozenContextPreparedAndNewGenerations(t *testing.T) {
	d := newDataDeps(t)
	ctx := context.Background()
	const content = "柳枝像绿色的丝带，随风轻轻摆动。"
	workID, generationID, _, err := d.CreateCurrentTextWork(ctx, "xiaoming", content, "snapshot-prepared")
	if err != nil {
		t.Fatal(err)
	}
	view, err := d.GetCreativeWork(ctx, "xiaoming", workID)
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	generate := func(_ context.Context, _, prompt, _ string) (string, error) {
		prompts = append(prompts, prompt)
		return conciseWritingResponse, nil
	}
	a := feedbackSnapshotAdapter(t, "METHOD_A", generate)
	frozen, err := a.FreezeWorkFeedbackContext(ctx, usecase.WorkFeedbackRequest{
		WorkType: k12.WorkTypeWriting, Title: view.Fields.Title, Task: view.Fields.Task,
		Intent: view.Fields.Intent, ContentMarkdown: content,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := d.Records.FreezeWorkFeedbackGenerationRequest(ctx, "xiaoming", generationID, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	assertFeedbackSnapshot(t, stored, "METHOD_A")
	// 新适配器只提供B；恢复必须从SQLite读取准备好的A，而不是重新解析目录。
	d.Solver = feedbackSnapshotAdapter(t, "METHOD_B", generate)
	if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", workID, generationID); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "METHOD_A") || strings.Contains(prompts[0], "METHOD_B") {
		t.Fatal("prepared request consumed the current method instead of its frozen content")
	}
	if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", workID, generationID); err != nil || len(prompts) != 1 {
		t.Fatalf("completed replay regenerated feedback: calls=%d err=%v", len(prompts), err)
	}
	if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", workID, "new-method-generation"); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "METHOD_B") || strings.Contains(prompts[1], "METHOD_A") {
		t.Fatal("new generation did not consume the current method")
	}
	original, err := d.Records.GetWorkFeedbackGeneration(ctx, "xiaoming", generationID)
	if err != nil || original.RequestSnapshotJSON != stored {
		t.Fatalf("new generation overwrote the original snapshot: %v", err)
	}
}

func TestWorkFeedbackFrozenContextSafeRetryAndUnknownNoResend(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		safe bool
	}{
		{"safe_retry", feedbackSnapshotUnavailable{}, true},
		{"unknown_no_resend", errors.New("controlled response lost after send"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDataDeps(t)
			ctx := context.Background()
			workID, generationID, _, err := d.CreateCurrentTextWork(ctx, "xiaoming", "柳枝像绿色的丝带，随风轻轻摆动。", "snapshot-"+tc.name)
			if err != nil {
				t.Fatal(err)
			}
			var prompts []string
			d.Solver = feedbackSnapshotAdapter(t, "METHOD_A", func(_ context.Context, _, prompt, _ string) (string, error) {
				prompts = append(prompts, prompt)
				return "", tc.err
			})
			if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", workID, generationID); err == nil {
				t.Fatal("controlled provider failure was hidden")
			}
			original, err := d.Records.GetWorkFeedbackGeneration(ctx, "xiaoming", generationID)
			if err != nil {
				t.Fatal(err)
			}
			assertFeedbackSnapshot(t, original.RequestSnapshotJSON, "METHOD_A")
			d.Solver = feedbackSnapshotAdapter(t, "METHOD_B", func(_ context.Context, _, prompt, _ string) (string, error) {
				prompts = append(prompts, prompt)
				return conciseWritingResponse, nil
			})
			_, err = d.GenerateWorkFeedbackCommand(ctx, "xiaoming", workID, generationID)
			if !tc.safe {
				if err == nil || len(prompts) != 1 {
					t.Fatalf("unknown request was resent: calls=%d err=%v", len(prompts), err)
				}
				return
			}
			if err != nil || len(prompts) != 2 || !strings.Contains(prompts[1], "METHOD_A") || strings.Contains(prompts[1], "METHOD_B") {
				t.Fatalf("safe retry lost the frozen method: calls=%d err=%v", len(prompts), err)
			}
			restored, err := d.Records.GetWorkFeedbackGeneration(ctx, "xiaoming", generationID)
			if err != nil || restored.RequestSnapshotJSON != original.RequestSnapshotJSON {
				t.Fatalf("safe retry changed the persisted snapshot: %v", err)
			}
		})
	}
}

type feedbackGenerationProfileStore struct{ profile k12.ChildProfile }

func (p *feedbackGenerationProfileStore) GetProfile(context.Context, string) (k12.ChildProfile, error) {
	return p.profile, nil
}

func (p *feedbackGenerationProfileStore) SaveProfile(_ context.Context, _ string, value k12.ChildProfile) error {
	p.profile = value
	return nil
}

func TestWorkFeedbackCoordinatorRestoresFrozenRegenerationAfterProfileChange(t *testing.T) {
	for _, changeProfile := range []bool{false, true} {
		t.Run(map[bool]string{false: "same profile", true: "changed profile"}[changeProfile], func(t *testing.T) {
			d := newDataDeps(t)
			ctx := t.Context()
			const agent, content = "xiaoming", "柳枝像绿色的丝带，随风轻轻摆动。"
			profiles := &feedbackGenerationProfileStore{profile: k12.ChildProfile{GradeTerm: "六年级上"}}
			d.Profiles = profiles
			calls, actualGrade := 0, ""
			solver := feedbackSnapshotAdapter(t, "FROZEN_METHOD", func(_ context.Context, _, _, grade string) (string, error) {
				calls++
				actualGrade = grade
				return conciseWritingResponse, nil
			})
			d.Solver = solver
			work, initial, _, err := d.CreateCurrentTextWork(ctx, agent, content, "initial-work")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.GenerateWorkFeedbackCommand(ctx, agent, work, initial); err != nil {
				t.Fatal(err)
			}
			view, err := d.GetCreativeWork(ctx, agent, work)
			if err != nil {
				t.Fatal(err)
			}
			req := usecase.WorkFeedbackRequest{WorkType: view.Fields.WorkType, Title: view.Fields.Title,
				Task: view.Fields.Task, Intent: view.Fields.Intent, ContentMarkdown: content, Grade: "六年级上"}
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			next, _, err := d.Records.PrepareWorkFeedbackGeneration(ctx, agent, work,
				"explicit-regeneration", "sha256:"+hex.EncodeToString(sum[:]), time.Now().Unix())
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := solver.FreezeWorkFeedbackContext(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = json.Marshal(frozen)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := d.Records.FreezeWorkFeedbackGenerationRequest(ctx, agent, next.GenerationID, string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if changeProfile {
				profiles.profile.GradeTerm = "六年级下"
			}
			runner := &usecase.CreativeWorkFeedbackCoordinator{Deps: &d, Records: d.Records}
			if err = runner.Run(ctx, agent, next.GenerationID); err != nil {
				t.Fatalf("prepared generation did not resume: %v", err)
			}
			current, err := d.Records.GetWorkFeedbackGeneration(ctx, agent, next.GenerationID)
			if err != nil || current.Status != k12.WorkFeedbackSucceeded || current.RequestSnapshotJSON != stored || calls != 2 || actualGrade != "六年级上" {
				t.Fatalf("recovery changed the frozen request or duplicated work: status=%s calls=%d grade=%s err=%v", current.Status, calls, actualGrade, err)
			}
		})
	}
}

func TestWorkFeedbackKnownResponseSurvivesCancellationAndRecoversWithoutResend(t *testing.T) {
	for _, cancelAtReturn := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "cancel_at_known_response"}[cancelAtReturn], func(t *testing.T) {
			d := newDataDeps(t)
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			work, generation, _, err := d.CreateCurrentTextWork(parent, "xiaoming", "柳枝像绿色的丝带，随风轻轻摆动。", "known-response")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			d.Solver = feedbackSnapshotAdapter(t, "KNOWN_RESPONSE", func(_ context.Context, _, _, _ string) (string, error) {
				calls++
				if cancelAtReturn {
					cancel()
				}
				return conciseWritingResponse, nil
			})
			_, callErr := d.GenerateWorkFeedbackCommand(parent, "xiaoming", work, generation)
			invocation, err := d.Records.GetLatestWorkFeedbackInvocation(t.Context(), "xiaoming", work, "work:"+work+":version:"+generation+":feedback")
			if err != nil {
				t.Fatal(err)
			}
			if invocation.Status != k12.ImageTaskInvocationSucceeded || !strings.Contains(invocation.ResultJSON, "具体肯定") {
				t.Fatalf("known response must remain durable: status=%s response_bytes=%d call_err=%v", invocation.Status, len(invocation.ResultJSON), callErr)
			}
			runner := &usecase.CreativeWorkFeedbackCoordinator{Deps: &d, Records: d.Records}
			if err := runner.Run(t.Context(), "xiaoming", generation); err != nil {
				t.Fatal(err)
			}
			view, err := d.GetCreativeWork(t.Context(), "xiaoming", work)
			if err != nil || view.GenerationState.Latest == nil || calls != 1 {
				t.Fatalf("known response recovery must not resend: state=%+v calls=%d err=%v", view.GenerationState, calls, err)
			}
		})
	}
}

func TestWorkFeedbackExplicitRetryAfterStructuredResponseFailure(t *testing.T) {
	d := newDataDeps(t)
	ctx := t.Context()
	work, generation, _, err := d.CreateCurrentTextWork(ctx, "xiaoming", "柳枝像绿色的丝带，随风轻轻摆动。", "format-retry")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	invalid := strings.Replace(conciseWritingResponse, "## 关键问题", "## 其他问题", 1)
	d.Solver = feedbackSnapshotAdapter(t, "FORMAT_RETRY", func(_ context.Context, _, _, _ string) (string, error) {
		calls++
		if calls == 1 {
			return invalid, nil
		}
		return conciseWritingResponse, nil
	})
	if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", work, generation); err == nil {
		t.Fatal("invalid initial section must fail")
	}
	view, err := d.GetCreativeWork(ctx, "xiaoming", work)
	if err != nil || view.GenerationState.Current == nil || !view.GenerationState.Current.RetrySafe {
		t.Fatalf("known invalid response must allow explicit retry: current=%+v err=%v", view.GenerationState.Current, err)
	}
	prior, err := d.Records.GetLatestWorkFeedbackInvocation(ctx, "xiaoming", work, "work:"+work+":version:"+generation+":feedback")
	if err != nil || prior.Status != k12.ImageTaskInvocationFailed || !prior.RetrySafe ||
		prior.StartedAt == 0 || prior.ProviderRequestKey == "" || prior.ResultDigest == "" ||
		!strings.Contains(prior.ResultJSON, "其他问题") {
		t.Fatalf("invalid original response must remain auditable: response=%s err=%v", prior.ResultJSON, err)
	}
	if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", work, generation); err != nil || calls != 2 {
		t.Fatalf("explicit retry must obtain a corrected attempt: calls=%d err=%v", calls, err)
	}
	if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", work, generation); err != nil || calls != 2 {
		t.Fatalf("successful replay must not generate again: calls=%d err=%v", calls, err)
	}
	stored, err := d.Records.GetImageTaskInvocation(ctx, "xiaoming", prior.InvocationID)
	if err != nil || stored.ResultJSON != prior.ResultJSON || stored.ResultDigest != prior.ResultDigest {
		t.Fatalf("retry changed the original response: err=%v", err)
	}
}

func TestWorkFeedbackInvalidResponseReceiptDoesNotRewriteTerminalOutcomes(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful", true: "outcome_unknown"}[unknown], func(t *testing.T) {
			d := newDataDeps(t)
			ctx := t.Context()
			work, generation, _, err := d.CreateCurrentTextWork(ctx, "xiaoming", "柳枝像绿色的丝带，随风轻轻摆动。", "terminal-receipt")
			if err != nil {
				t.Fatal(err)
			}
			d.Solver = feedbackSnapshotAdapter(t, "TERMINAL_RESPONSE", func(_ context.Context, _, _, _ string) (string, error) {
				if unknown {
					return "", errors.New("controlled response lost after send")
				}
				return conciseWritingResponse, nil
			})
			_, callErr := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", work, generation)
			if (callErr != nil) != unknown {
				t.Fatalf("unexpected provider result: unknown=%v err=%v", unknown, callErr)
			}
			prior, err := d.Records.GetLatestWorkFeedbackInvocation(ctx, "xiaoming", work, "work:"+work+":version:"+generation+":feedback")
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(`{}`))
			response, digest := prior.ResultJSON, prior.ResultDigest
			if unknown {
				response, digest = `{}`, "sha256:"+hex.EncodeToString(sum[:])
			}
			if err := d.Records.FailWorkFeedbackInvocationWithResult(ctx, "xiaoming", prior.InvocationID,
				"work_feedback_structure_invalid", digest, response); err == nil {
				t.Fatal("known failure reception must not rewrite terminal outcomes")
			}
			stored, err := d.Records.GetImageTaskInvocation(ctx, "xiaoming", prior.InvocationID)
			if err != nil || stored.Status != prior.Status || stored.RetrySafe != prior.RetrySafe ||
				stored.ResultJSON != prior.ResultJSON || stored.ResultDigest != prior.ResultDigest {
				t.Fatalf("terminal outcome changed: err=%v", err)
			}
		})
	}
}

func TestWorkFeedbackLegacyInvalidSucceededResponseRequiresExplicitRetry(t *testing.T) {
	for _, initialStatus := range []string{k12.WorkFeedbackFailed, k12.WorkFeedbackRunning} {
		t.Run(initialStatus, func(t *testing.T) {
			d := newDataDeps(t)
			ctx := t.Context()
			work, generation, _, err := d.CreateCurrentTextWork(ctx, "xiaoming", "柳枝像绿色的丝带，随风轻轻摆动。", "legacy-format")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			d.Solver = feedbackSnapshotAdapter(t, "LEGACY_FORMAT", func(_ context.Context, _, _, _ string) (string, error) {
				calls++
				if calls == 1 {
					return strings.Replace(conciseWritingResponse, "## 关键问题", "## 其他问题", 1), nil
				}
				return conciseWritingResponse, nil
			})
			if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", work, generation); err == nil {
				t.Fatal("malformed response must fail")
			}
			prior, err := d.Records.GetLatestWorkFeedbackInvocation(ctx, "xiaoming", work, "work:"+work+":version:"+generation+":feedback")
			if err != nil || prior.ResultJSON == "" || prior.ResultDigest == "" {
				t.Fatalf("original response fixture is missing: %v", err)
			}
			// 历史实现先保存成功回执再校验结构；响应、摘要和冻结请求仍由正式入口生成。
			if _, err := d.Records.DB().ExecContext(ctx, `UPDATE k12_image_task_invocations SET status='succeeded',retry_safe=0,error_kind='' WHERE invocation_id=?`, prior.InvocationID); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Records.DB().ExecContext(ctx, `UPDATE k12_work_feedback_generations SET status=?,failure_reason=CASE WHEN ?='running' THEN '' ELSE failure_reason END WHERE generation_id=?`, initialStatus, initialStatus, generation); err != nil {
				t.Fatal(err)
			}
			if initialStatus == k12.WorkFeedbackRunning {
				if _, err := d.Records.DB().ExecContext(ctx, `UPDATE k12_creative_works SET feedback_state='queued' WHERE record_id=? AND initial_feedback_generation_id=?`, work, generation); err != nil {
					t.Fatal(err)
				}
			}
			for _, invalid := range []struct{ kind, digest, response string }{
				{"work_feedback_empty", prior.ResultDigest, prior.ResultJSON},
				{"work_feedback_structure_invalid", "sha256:" + strings.Repeat("a", 64), prior.ResultJSON},
				{"work_feedback_structure_invalid", prior.ResultDigest, `{}`},
			} {
				if err := d.Records.FailWorkFeedbackInvocationWithResult(ctx, "xiaoming", prior.InvocationID, invalid.kind, invalid.digest, invalid.response); err == nil {
					t.Fatal("legacy reclassification must require the original response, digest and structural failure")
				}
			}
			if initialStatus == k12.WorkFeedbackRunning {
				runner := &usecase.CreativeWorkFeedbackCoordinator{Deps: &d, Records: d.Records}
				if err := runner.Run(ctx, "xiaoming", generation); err == nil {
					t.Fatal("background recovery must retain the known structural failure")
				}
				failed, err := d.Records.GetWorkFeedbackGeneration(ctx, "xiaoming", generation)
				if err != nil || failed.Status != k12.WorkFeedbackFailed || !failed.RetrySafe || calls != 1 {
					t.Fatalf("background recovery must not resend: generation=%+v calls=%d err=%v", failed, calls, err)
				}
			}
			if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", work, generation); err != nil || calls != 2 {
				t.Fatalf("explicit retry must obtain the corrected attempt: calls=%d err=%v", calls, err)
			}
			stored, err := d.Records.GetImageTaskInvocation(ctx, "xiaoming", prior.InvocationID)
			if err != nil || stored.Status != k12.ImageTaskInvocationFailed || !stored.RetrySafe ||
				stored.ResultJSON != prior.ResultJSON || stored.ResultDigest != prior.ResultDigest ||
				stored.StartedAt != prior.StartedAt || stored.FinishedAt != prior.FinishedAt {
				t.Fatalf("reclassification changed the original response: invocation=%+v err=%v", stored, err)
			}
			if _, err := d.GenerateWorkFeedbackCommand(ctx, "xiaoming", work, generation); err != nil || calls != 2 {
				t.Fatalf("successful replay must not send: calls=%d err=%v", calls, err)
			}
		})
	}
}
