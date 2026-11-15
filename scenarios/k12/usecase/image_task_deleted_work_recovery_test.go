package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestImageTaskRecoverySkipsDeletedCreativeButPreservesMissingIdentityError(t *testing.T) {
	for _, deleted := range []bool{true, false} {
		t.Run(fmt.Sprintf("deleted=%v", deleted), func(t *testing.T) {
			classifier := &imageTaskClassifierStub{result: ImageTaskClassification{
				Intent: k12.ImageTaskIntentArtwork, IntentEvidence: []string{"visible art"}, Confidence: .99,
			}}
			c, _ := newImageTaskCoordinatorForTest(t, classifier)
			var sequence atomic.Int64
			c.NewID = func(kind string) string { return fmt.Sprintf("recovery-%s-%d", kind, sequence.Add(1)) }
			ctx := t.Context()
			first := testCreateImageTaskInput()
			first.OwnerScope = "recovery-owner"
			first.SourceRef = "original-art"
			v, _, err := createAndRunImageTask(t, c, first)
			if err != nil {
				t.Fatal(err)
			}
			if v.Creative == nil || v.Creative.PromotedWorkID == "" {
				t.Fatal("original work was not promoted")
			}
			if deleted {
				state, err := c.Records.GetCreativeWorkGenerationState(ctx, "mingming", v.Creative.PromotedWorkID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Records.TombstoneCurrentObject(ctx, "mingming", "creative_work", v.Creative.PromotedWorkID, state.RowVersion, "delete-original-work"); err != nil {
					t.Fatal(err)
				}
			} else {
				// 损坏来源不是合法删除，恢复不能静默掩盖缺失身份。
				if _, err := c.Records.DB().ExecContext(ctx, `UPDATE k12_creative_work_intakes SET promoted_work_id='missing-work' WHERE intake_id=?`, v.Creative.IntakeID); err != nil {
					t.Fatal(err)
				}
			}
			second := testCreateImageTaskInput()
			second.OwnerScope = "recovery-owner"
			second.SourceRef = "later-photo"
			queued, _, err := c.Create(ctx, second)
			if err != nil {
				t.Fatal(err)
			}
			for _, ownerScoped := range []bool{false, true} {
				var rows []k12.ImageTaskDispatch
				if ownerScoped {
					rows, err = c.Records.ListImageTaskDispatchesForOwnerSession(ctx, "recovery-owner", "mingming", first.SourceSessionID)
				} else {
					rows, err = c.Records.ListImageTaskDispatchesForSession(ctx, "mingming", first.SourceSessionID)
				}
				want := 2
				if deleted {
					want = 1
				}
				if err != nil || len(rows) != want {
					t.Fatalf("session recovery excluded the wrong source: owner_scoped=%v rows=%d want=%d err=%v", ownerScoped, len(rows), want, err)
				}
				if deleted && rows[0].DispatchID != queued.Dispatch.DispatchID {
					t.Fatal("session recovery lost the live message")
				}
			}
			if _, err := c.Get(ctx, "mingming", v.Dispatch.DispatchID); !errors.Is(err, records.ErrNotFound) {
				t.Fatalf("single deleted/missing source read changed its error: %v", err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = c.Shutdown(ctx)
			}()
			recovered, recoverErr := c.Recover(ctx, []string{"mingming"})
			if deleted {
				if recoverErr != nil || recovered != 1 {
					t.Fatalf("deleted old work blocked later dispatch: recovered=%d err=%v", recovered, recoverErr)
				}
			} else {
				if !errors.Is(recoverErr, records.ErrNotFound) || recovered != 0 {
					t.Fatalf("missing source identity was hidden: recovered=%d err=%v", recovered, recoverErr)
				}
				inv, err := c.Records.GetImageTaskInvocation(ctx, "mingming", queued.Dispatch.ClassificationInvocationID)
				if err != nil || inv.Status != k12.ImageTaskInvocationPrepared {
					t.Fatalf("broken recovery started later work: status=%s err=%v", inv.Status, err)
				}
			}
		})
	}
}
