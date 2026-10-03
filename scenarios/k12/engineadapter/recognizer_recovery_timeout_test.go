package engineadapter

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestRecognitionRecoveryTimeoutAdapterClock(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		index                 int
		parent, elapsed, want time.Duration
		expired               bool
	}{
		{"authorized_target_accepts_129_seconds", 4, 300 * time.Second, 129466 * time.Millisecond, 180 * time.Second, false},
		{"other_batch_remains_120_seconds", 0, 300 * time.Second, 129466 * time.Millisecond, 120 * time.Second, true},
		{"target_stops_at_180_seconds", 4, 300 * time.Second, 181 * time.Second, 180 * time.Second, true},
		{"parent_cancellation_still_wins", 4, 90 * time.Second, 91 * time.Second, 90 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				page := recognitionLayoutV2DensePagePNG(t, 200, 1200)
				targets := make([]k12.RecognitionLayoutManifestTargetV2, 24)
				for i := range targets {
					targets[i] = k12.RecognitionLayoutManifestTargetV2{ManifestRef: fmt.Sprintf("manifest_%04d", i+1), ManifestOrder: i + 1, DisplayLabel: fmt.Sprint(i + 1), SourceNumberPath: []string{fmt.Sprint(i + 1)}, Region: k12.SourcePixelRegion{X: 0, Y: i * 50, Width: 200, Height: 40}}
				}
				plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: "clock-manifest", ResultDigest: recognitionLayoutV2TestDigest("clock-manifest")}, Targets: targets, RecognitionFormat: k12.RecognitionLayoutCompactV4})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), tc.parent)
				defer cancel()
				ctx = k12.WithRecognitionPhysicalCallExecutor(ctx, &recognitionLayoutV2DeadlineExecutor{})
				runtime := k12.RecognitionLayoutPlanRuntimeV2{Header: k12.RecognitionLayoutPlanHeaderV2{PhysicalCallCapMillis: 120000}, StageDeadlineAtUnixMillis: time.Now().Add(5 * time.Minute).UnixMilli(), RecoveryPhysicalUnit: "layout_batch_0005", RecoveryTimeoutOverrideMS: 180000}
				calls := 0
				adapter := NewRecognizerAdapter(func(sendCtx context.Context, _ []byte, _ string) (string, error) {
					calls++
					deadline, ok := sendCtx.Deadline()
					if !ok || time.Until(deadline) != tc.want {
						t.Fatalf("effective deadline=%v want=%v", time.Until(deadline), tc.want)
					}
					time.Sleep(tc.elapsed)
					if (sendCtx.Err() != nil) != tc.expired {
						t.Fatalf("elapsed=%v err=%v expired=%v", tc.elapsed, sendCtx.Err(), tc.expired)
					}
					return "", errRecognitionLayoutV2DeadlineObserved
				})
				result := adapter.recognizeLayoutPrimaryBatchV2(ctx, page, plan, runtime, tc.index)
				if calls != 1 || !errors.Is(result.err, errRecognitionLayoutV2DeadlineObserved) {
					t.Fatalf("provider boundary calls=%d err=%v", calls, result.err)
				}
			})
		})
	}
}
