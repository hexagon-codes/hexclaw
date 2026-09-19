package engineadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

// 真实 SQLite 保存物理回执；仅模型返回值可控，结算失败由数据库触发器产生。
type partialRecognitionFixture struct {
	t                   *testing.T
	db                  *sql.DB
	path, runDir, jobID string
	store               *k12storage.Store
	deps                usecase.Deps
	o                   *usecase.GradingOrchestrator
	mu                  sync.Mutex
	calls               map[string]int
	unknown             bool
}

func newPartialRecognitionFixture(t *testing.T, unknown bool) *partialRecognitionFixture {
	t.Helper()
	f := &partialRecognitionFixture{t: t, path: filepath.Join(t.TempDir(), "receipts.db"), runDir: t.TempDir(), calls: map[string]int{}, unknown: unknown}
	f.open()
	if _, err := f.db.Exec(`INSERT INTO agents(name,metadata) VALUES('mingming','{"k12.grade_term":"五年级上"}')`); err != nil {
		t.Fatal(err)
	}
	// 复核响应已持久化后模拟本地结算失败，尚未调度的复核不产生回执。
	if _, err := f.db.Exec(`CREATE TRIGGER partial_recovery_stop BEFORE INSERT ON k12_recognition_layout_repair_settlements BEGIN SELECT RAISE(ABORT,'local settlement unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	job, created, err := f.o.StartPhotoGradingJob(context.Background(), usecase.StartPhotoGradingInput{
		Photo:      usecase.PhotoGradeRequest{AgentName: "mingming", Grade: "五年级上", SourceSession: "partial-recovery", Image: recognitionLayoutV2DensePagePNG(t, 1000, 1800)},
		SourceKind: "desktop", SourceKey: "partial-recovery",
	})
	if err != nil || !created {
		t.Fatalf("start: created=%v err=%v", created, err)
	}
	f.jobID = job.Record.RecordID
	view, err := f.o.RunGradingJob(context.Background(), f.jobID)
	if err == nil || view.Record.Status != k12.GradingStageOutcomeUnknown {
		t.Fatalf("local failure: stage=%s failure=%s err=%v", view.Record.Status, view.Fields.FailureKind, err)
	}
	t.Cleanup(f.close)
	return f
}

func (f *partialRecognitionFixture) open() {
	f.t.Helper()
	var err error
	f.db, err = sql.Open("sqlite", f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.db.SetMaxOpenConns(1)
	if err = migrate.Run(context.Background(), f.db, migrate.All); err != nil {
		f.t.Fatal(err)
	}
	registry := scenario.NewRegistry()
	constraint := k12.NewCurriculumStub()
	if err = registry.Assemble(k12.Pack(constraint)); err != nil {
		f.t.Fatal(err)
	}
	f.store = k12storage.NewStore(f.db, registry.Records)
	f.deps = usecase.Deps{Records: f.store, Constraint: constraint, Recognizer: NewRecognizerAdapter(f.vision)}
	f.deps.GradingBudgetSnapshot = k12.GradingBudgetSnapshot{
		PolicyVersion: 20260808, RecognitionPlanVersion: k12.RecognitionPlanVersionV2,
		StageSeconds:     k12.GradingStageBudgets{Queued: 60, Normalizing: 60, Recognizing: 900, Locating: 60, Rendering: 60, Projecting: 60},
		AssessingBuckets: []k12.GradingAssessingBudgetBucket{{MaxProblems: 1, Seconds: 90}, {MaxProblems: 8, Seconds: 180}, {MaxProblems: 16, Seconds: 300}, {MaxProblems: 32, Seconds: 540}},
		ItemConcurrency:  2, PhysicalCallCapMillis: 120000, WorkerHardCap: 2, EffectiveConcurrency: 1,
		RecognizingBuckets: k12.RecognitionLayoutBudgetBucketsV2{UpTo1ProblemMillis: 120000, UpTo8ProblemsMillis: 300000, UpTo16ProblemsMillis: 600000, UpTo32ProblemsMillis: 900000},
	}
	snapshot := k12.GradingModelSnapshot{Provider: "hexclaw-gpt", Model: k12.RecognizingPolicyModel, Route: "hexclaw-gpt/" + k12.RecognizingPolicyModel, Capability: "vision", TimeoutMS: 120000, RecognizingRequestPolicy: k12.ApprovedRecognizingRequestPolicy()}
	f.o = usecase.NewGradingOrchestrator(f.deps, func(k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return snapshot, nil }, usecase.WithGradingRunDir(f.runDir))
}

func (f *partialRecognitionFixture) close() {
	if f.o != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.o.Shutdown(ctx); err != nil {
			f.t.Error(err)
		}
		f.o = nil
	}
	if f.db != nil {
		if err := f.db.Close(); err != nil {
			f.t.Error(err)
		}
		f.db = nil
	}
}

func (f *partialRecognitionFixture) vision(_ context.Context, _ []byte, prompt string) (string, error) {
	kind := "batch"
	if strings.Contains(prompt, "manifest_ref") {
		kind = "manifest"
	} else if strings.HasPrefix(prompt, "Independently transcribe") {
		kind = "repair"
	}
	f.mu.Lock()
	f.calls[kind]++
	call := f.calls[kind]
	unknown := f.unknown
	f.mu.Unlock()
	if kind == "manifest" {
		return recognitionLayoutV2ManifestPayload(f.t, 4), nil
	}
	if unknown && kind == "repair" && call == 1 {
		return "", io.ErrUnexpectedEOF
	}
	var targets []struct {
		TargetID string `json:"target_id"`
	}
	if err := json.Unmarshal([]byte(prompt[strings.LastIndex(prompt, "["):]), &targets); err != nil {
		return "", err
	}
	count := len(targets)
	items := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, map[string]any{"target_id": fmt.Sprintf("t%d", i+1), "kind": "question", "recognition": map[string]any{"question": "1+1=", "subject": "数学", "answer_state": "blank", "student_answer": "", "recognition_confidence": 0.99}})
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	return string(raw), err
}

func (f *partialRecognitionFixture) counts() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := map[string]int{}
	for k, v := range f.calls {
		result[k] = v
	}
	return result
}

func (f *partialRecognitionFixture) receipts() []k12.ModelPhysicalInvocation {
	f.t.Helper()
	rows, err := f.store.ListModelPhysicalInvocations(context.Background(), "mingming", f.jobID)
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func (f *partialRecognitionFixture) assertOriginalUnchanged(old []k12.ModelPhysicalInvocation) {
	f.t.Helper()
	for _, row := range old {
		current, err := f.store.GetModelPhysicalInvocation(context.Background(), "mingming", row.PhysicalInvocationID)
		if err != nil || !reflect.DeepEqual(current, row) {
			f.t.Fatalf("original receipt changed: %s err=%v", row.PhysicalUnit, err)
		}
		if row.Status == k12.ModelInvocationSucceeded {
			if err := f.store.ValidateModelPhysicalInvocationResultContent(context.Background(), "mingming", row.PhysicalInvocationID); err != nil {
				f.t.Fatal(err)
			}
		}
	}
}

func TestRecognitionPartialRecoveryRestartReusesSuccessfulRepairs(t *testing.T) {
	f := newPartialRecognitionFixture(t, false)
	old := f.receipts()
	before := f.counts()
	if before["manifest"] != 1 || before["batch"] != 2 || before["repair"] < 1 || before["repair"] >= 4 {
		t.Fatalf("fixture must leave unsent repairs: %v", before)
	}
	for _, row := range old {
		if row.Status != k12.ModelInvocationSucceeded {
			t.Fatalf("expected conclusive physical receipt: %+v", row)
		}
	}
	f.close()
	f.open()
	if _, err := f.db.Exec(`DROP TRIGGER partial_recovery_stop`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.o.RecoverGradingJobs(context.Background(), []string{"mingming"}); err != nil {
		t.Fatal(err)
	}
	view, err := f.deps.GetGradingJob(context.Background(), "mingming", f.jobID)
	if err != nil || view.Record.Status != k12.GradingStageFailedRetryable || view.Fields.FailureKind != "reconciled_partial_succeeded" {
		t.Fatalf("partial reconciliation: %+v err=%v", view, err)
	}
	// 重复启动扫描只复核本地证据，不能自动产生第二次模型发送。
	if _, err := f.o.RecoverGradingJobs(context.Background(), []string{"mingming"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.counts()) {
		t.Fatalf("restart sent model calls: before=%v after=%v", before, f.counts())
	}
	if _, handled, err := f.o.RetryPhotoGradingJob(context.Background(), f.jobID); err != nil || !handled {
		t.Fatalf("public retry: handled=%v err=%v", handled, err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		view, err = f.deps.GetGradingJob(context.Background(), "mingming", f.jobID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Record.Status == k12.GradingStageAwaitingConfirmation {
			break
		}
		if time.Now().After(deadline) || view.Record.Status == k12.GradingStageOutcomeUnknown || view.Record.Status == k12.GradingStageFailedTerminal {
			t.Fatalf("resume stage=%s failure=%s calls=%v", view.Record.Status, view.Fields.FailureKind, f.counts())
		}
		time.Sleep(10 * time.Millisecond)
	}
	questions, ok := f.o.RecognizedQuestionsForOwner(context.Background(), "mingming", f.jobID)
	if !ok || len(questions) != 4 {
		t.Fatalf("same Job recognition artifact: ok=%v questions=%+v", ok, questions)
	}
	for _, q := range questions {
		if q.Question != "1+1=" {
			t.Fatalf("unexpected question: %+v", q)
		}
	}
	if got := f.counts(); !reflect.DeepEqual(got, map[string]int{"manifest": 1, "batch": 2, "repair": 4}) {
		t.Fatalf("successful calls were resent: %v", got)
	}
	reused := 0
	for _, row := range f.receipts() {
		if row.ReusedFromPhysicalInvocationID != "" {
			reused++
		}
	}
	if reused != len(old) {
		t.Fatalf("reused=%d original=%d", reused, len(old))
	}
	f.assertOriginalUnchanged(old)
	f.close()
	f.open()
	if _, err := f.o.RecoverGradingJobs(context.Background(), []string{"mingming"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.counts(), map[string]int{"manifest": 1, "batch": 2, "repair": 4}) {
		t.Fatal("completed recognition was sent again")
	}
	f.assertOriginalUnchanged(old)
}

func TestRecognitionPartialRecoveryUnknownOrInputMismatchDoesNotSend(t *testing.T) {
	for _, mode := range []string{"unknown", "input_mismatch"} {
		t.Run(mode, func(t *testing.T) {
			f := newPartialRecognitionFixture(t, mode == "unknown")
			old, before := f.receipts(), f.counts()
			f.close()
			if mode == "input_mismatch" {
				// 运行时原图损坏属于身份不符；恢复不得用新像素覆盖成功回执。
				if err := os.WriteFile(filepath.Join(f.runDir, f.jobID, "image.bin"), recognitionLayoutV2DensePagePNG(t, 900, 1600), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.open()
			if _, err := f.o.RecoverGradingJobs(context.Background(), []string{"mingming"}); err != nil {
				t.Fatal(err)
			}
			view, err := f.deps.GetGradingJob(context.Background(), "mingming", f.jobID)
			if err != nil || view.Record.Status != k12.GradingStageOutcomeUnknown {
				t.Fatalf("unsafe recovery: stage=%s err=%v", view.Record.Status, err)
			}
			if _, _, err := f.o.RetryPhotoGradingJob(context.Background(), f.jobID); err == nil && mode == "unknown" {
				t.Fatal("unknown public retry accepted")
			}
			if !reflect.DeepEqual(before, f.counts()) {
				t.Fatalf("unexpected model send: before=%v after=%v", before, f.counts())
			}
			f.assertOriginalUnchanged(old)
		})
	}
}
