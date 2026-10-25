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
