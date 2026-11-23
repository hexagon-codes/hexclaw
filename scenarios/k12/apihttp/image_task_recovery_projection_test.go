package apihttp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestPublicImageTaskProjectsAmbiguousReceiptsAsRecovering(t *testing.T) {
	for _, view := range []usecase.ImageTaskView{
		{
			Dispatch: k12.ImageTaskDispatch{
				DispatchID: "dispatch-homework", TaskIntent: k12.ImageTaskIntentCompletedHomework,
				Status: k12.ImageTaskStatusRouted,
			},
			Homework: &k12.HomeworkSubmission{},
			HomeworkProjection: &usecase.ImageTaskHomeworkProjection{
				Stage: "outcome_unknown",
			},
		},
		{
			Dispatch: k12.ImageTaskDispatch{
				DispatchID: "dispatch-creative", TaskIntent: k12.ImageTaskIntentArtwork,
				Status: k12.ImageTaskStatusRouted,
			},
			Creative: &k12.CreativeWorkIntake{
				Status:   k12.CreativeWorkIntakePromoted,
				WorkType: k12.WorkTypeArt,
			},
			CreativeFeedback: "feedback_outcome_unknown",
		},
	} {
		raw, err := json.Marshal(publicImageTask(view))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "outcome_unknown") ||
			!strings.Contains(string(raw), `"state":"recovering"`) {
			t.Fatalf("internal ambiguity leaked on public wire: %s", raw)
		}
	}
}

func TestImageTaskRecoverySeparatesUnreadableWritingAndPendingWork(t *testing.T) {
	for _, tc := range []struct {
		intake   k12.CreativeWorkIntakeStatus
		terminal bool
	}{
		{k12.CreativeWorkIntakeUnreadable, true},
		{k12.CreativeWorkIntakePreparing, false},
		{k12.CreativeWorkIntakeAwaitingConfirmation, false},
		{k12.CreativeWorkIntakeReady, false},
	} {
		t.Run(string(tc.intake), func(t *testing.T) {
			view := usecase.ImageTaskView{
				Dispatch: k12.ImageTaskDispatch{Status: k12.ImageTaskStatusRouted, TaskIntent: k12.ImageTaskIntentWriting},
				Creative: &k12.CreativeWorkIntake{Status: tc.intake, WorkType: k12.WorkTypeWriting},
			}
			if got := imageTaskProjectionTerminal(publicImageTask(view)); got != tc.terminal {
				t.Fatalf("intake %s: terminal=%t, want %t", tc.intake, got, tc.terminal)
			}
		})
	}
}

func TestImageTaskRecoveryUsesReceiptFinalityForFailures(t *testing.T) {
	for _, tc := range []struct {
		failure             string
		creative, retryable bool
		stage               string
		terminal            bool
	}{
		{"classification_outcome_unknown", false, false, "recovering", false},
		{"writing_ocr_outcome_unknown", true, false, "recovering", false},
		{"writing_ocr_provider_failed", true, true, "failed_retryable", false},
		{"model_capability_unverified", true, false, "failed_terminal", true},
	} {
		t.Run(tc.failure, func(t *testing.T) {
			view := usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{
				Status: k12.ImageTaskStatusFailed, FailureKind: tc.failure, RetrySafe: tc.retryable,
			}}
			if tc.creative {
				view.Dispatch.TaskIntent = k12.ImageTaskIntentWriting
				view.Creative = &k12.CreativeWorkIntake{Status: k12.CreativeWorkIntakeFailed, WorkType: k12.WorkTypeWriting}
			}
			projection := publicImageTask(view)
			if projection.Progress.State != tc.stage || imageTaskProjectionTerminal(projection) != tc.terminal {
				t.Fatalf("stage=%q terminal=%t, want %q and %t", projection.Progress.State, imageTaskProjectionTerminal(projection), tc.stage, tc.terminal)
			}
		})
	}
}
