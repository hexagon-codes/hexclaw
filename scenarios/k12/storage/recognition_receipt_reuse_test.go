package k12storage_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

type recognitionClosureReceiptFixture struct {
	Manifest  string                         `json:"manifest"`
	Batches   []k12.RecognitionLayoutBatchV2 `json:"batches"`
	Responses map[string]string              `json:"responses"`
	Targets   []k12.RecognitionLayoutManifestTargetV2
	Page      []byte
}

// 真实正文配合合成页面，仅验证持久化和发送认领边界，不推断原图识别质量。
func TestRecognitionReceiptClosureReusesSixSuccessesAcrossAttempts(t *testing.T) {
	f := loadRecognitionClosureReceiptFixture(t)
	store, db, path, prior, priorPlan, sources := prepareRecognitionClosureReceiptSource(t, f, "")
	defer func() { _ = db.Close() }()
	before := recognitionClosureSourceRows(t, db, prior.InvocationID)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, db = openPhysicalLedgerFileStore(t, path)
	parent := unpreparedPhysicalInvocationParent(prior.JobID)
	parent.InvocationID, parent.Attempt = "closure-retry-parent", 2
	parent.RouteSnapshot, parent.RequestPolicySnapshot = prior.RouteSnapshot, prior.RequestPolicySnapshot
	parent, manifest, plan := prepareRecognitionClosureReceiptAttempt(t, store, f, parent)
	newCalls := 0
	assertReused := func(child, source k12.ModelPhysicalInvocation, want string) k12.ModelPhysicalInvocation {
		t.Helper()
		got, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(t.Context(), parent.AgentName, child.PhysicalInvocationID)
		if err != nil || !reused || got.Status != k12.ModelInvocationSucceeded ||
			got.ReusedFromPhysicalInvocationID != source.PhysicalInvocationID || got.ResultDigest != source.ResultDigest || got.ExternalRequestID != "" {
			t.Fatalf("reuse %s: reused=%v result=%+v err=%v", child.PhysicalUnit, reused, got, err)
		}
		body, err := store.LoadSucceededModelPhysicalInvocationResultContent(t.Context(), parent.AgentName, got.PhysicalInvocationID, source.ResultDigest)
		if err != nil || body != want {
			t.Fatalf("reuse changed raw bytes for %s: %v", child.PhysicalUnit, err)
		}
		if _, claimed, err := store.ClaimModelPhysicalInvocationSent(t.Context(), parent.AgentName, got.PhysicalInvocationID); claimed {
			newCalls++
			t.Errorf("reused %s entered the send boundary: %v", got.PhysicalUnit, err)
		}
		if replay, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(t.Context(), parent.AgentName, got.PhysicalInvocationID); err != nil || !reused || !reflect.DeepEqual(replay, got) {
			t.Fatalf("reuse replay changed %s: reused=%v err=%v", got.PhysicalUnit, reused, err)
		}
		return got
	}
	manifest = assertReused(manifest, sources[0], f.Manifest)
	authorizeRecognitionClosureReceiptPlan(t, store, parent, manifest, plan)
	var settlements []k12.RecognitionLayoutPrimaryBatchSettlementV2
	var receipts []k12.RecognitionLayoutPrimaryBatchSettlementResultV2
	for i, batch := range plan.Batches {
		child := prepareRecognitionClosureReceiptBatch(t, store, parent, plan, i)
		child = assertReused(child, sources[i+1], f.Responses[string(batch.Unit)])
		settlement := recognitionClosureReceiptSettlement(t, f, plan, child, false)
		receipt, created, err := store.SettleRecognitionLayoutPrimaryBatchV2(t.Context(), parent.AgentName, parent.InvocationID, settlement)
		if err != nil || !created || receipt.Classification != k12.RecognitionLayoutBatchClassifiedV2 || len(receipt.FrozenResults) != len(batch.TargetIDs) {
			t.Fatalf("settle reused %s: created=%v receipt=%+v err=%v", batch.Unit, created, receipt, err)
		}
		settlements, receipts = append(settlements, settlement), append(receipts, receipt)
	}
	if newCalls != 0 {
		t.Fatalf("successful source units entered send boundary %d times", newCalls)
	}
	if got := recognitionClosureSourceRows(t, db, prior.InvocationID); got != before {
		t.Fatal("reuse changed source physical body/digest, failed parent, or terminal settlement")
	}
	// 新 attempt 的分类不得覆盖旧 terminal 结算。
	changedOld := recognitionClosureReceiptSettlement(t, f, priorPlan, sources[4], false)
	if _, _, err := store.SettleRecognitionLayoutPrimaryBatchV2(t.Context(), prior.AgentName, prior.InvocationID, changedOld); err == nil {
		t.Fatal("old terminal settlement accepted a later classified result")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, db = openPhysicalLedgerFileStore(t, path)
	for i, settlement := range settlements {
		got, created, err := store.SettleRecognitionLayoutPrimaryBatchV2(t.Context(), parent.AgentName, parent.InvocationID, settlement)
		if err != nil || created || !reflect.DeepEqual(got, receipts[i]) {
			t.Fatalf("new settlement changed after reopen for %s: created=%v err=%v", settlement.SourcePhysicalUnit, created, err)
		}
	}
	var physicalCount, settlementCount, resultCount int
	for _, q := range []struct {
		sql  string
		want int
		got  *int
	}{
		{`SELECT count(*) FROM k12_model_physical_invocations WHERE parent_invocation_id=?`, 6, &physicalCount},
		{`SELECT count(*) FROM k12_recognition_layout_batch_settlements WHERE parent_invocation_id=?`, 5, &settlementCount},
		{`SELECT count(*) FROM k12_recognition_layout_candidate_results WHERE parent_invocation_id=?`, 16, &resultCount},
	} {
		if err := db.QueryRowContext(t.Context(), q.sql, parent.InvocationID).Scan(q.got); err != nil || *q.got != q.want {
			t.Fatalf("new attempt row count: got=%d want=%d err=%v", *q.got, q.want, err)
		}
	}
	if got := recognitionClosureSourceRows(t, db, prior.InvocationID); got != before {
		t.Fatal("settlement replay changed immutable source rows")
	}
	t.Log("reused whole_page and five batches byte-for-byte; send claims=0; source rows unchanged; five new settlements replayed after SQLite reopen")
}

func TestRecognitionReceiptClosureRejectsIdentityDriftAndTrueAmbiguity(t *testing.T) {
	for _, scenario := range []string{"model", "job", "page", "batch_target", "unattributable", "extra_candidate", "source_digest"} {
		t.Run(scenario, func(t *testing.T) {
			f := loadRecognitionClosureReceiptFixture(t)
			store, db, _, prior, _, sources := prepareRecognitionClosureReceiptSource(t, f, scenario)
			defer db.Close()
			before := recognitionClosureSourceRows(t, db, prior.InvocationID)
			parent := unpreparedPhysicalInvocationParent(prior.JobID)
			parent.InvocationID, parent.Attempt = "closure-retry-parent", 2
			parent.RouteSnapshot, parent.RequestPolicySnapshot = prior.RouteSnapshot, prior.RequestPolicySnapshot
			switch scenario {
			case "model":
				parent.RouteSnapshot.Model, parent.RouteSnapshot.Route = "closure-model-b", "local/closure-model-b"
			case "job":
				job := newGradingJobRecord(t, prior.AgentName, "closure-other-job")
				if _, err := store.Put(t.Context(), job); err != nil {
					t.Fatal(err)
				}
				parent.JobID = job.RecordID
			case "page":
				f.Page = recognitionClosureReceiptPage(t, true)
			case "batch_target":
				f.Targets[11].SourceSectionLabel += " changed"
			}
			parent, manifest, plan := prepareRecognitionClosureReceiptAttempt(t, store, f, parent)
			manifest, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(t.Context(), parent.AgentName, manifest.PhysicalInvocationID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "model" || scenario == "job" || scenario == "page" {
				if reused {
					t.Fatal("manifest reused across a changed model, job, or page")
				}
				manifest = succeedRecognitionClosureReceipt(t, store, parent, manifest, f.Manifest)
			} else if !reused {
				t.Fatal("unchanged manifest was not reusable")
			}
			authorizeRecognitionClosureReceiptPlan(t, store, parent, manifest, plan)
			child := prepareRecognitionClosureReceiptBatch(t, store, parent, plan, 3)
			got, reused, err := store.ReuseSucceededRecognitionPhysicalInvocation(t.Context(), parent.AgentName, child.PhysicalInvocationID)
			if err != nil || reused || got.Status != k12.ModelInvocationPrepared || got.ReusedFromPhysicalInvocationID != "" || got.ResultDigest != "" {
				t.Fatalf("ineligible source %s reused: source=%s reused=%v result=%+v err=%v", scenario, sources[4].PhysicalInvocationID, reused, got, err)
			}
			if got := recognitionClosureSourceRows(t, db, prior.InvocationID); got != before {
				t.Fatal("rejected reuse changed source rows")
			}
		})
	}
}

func loadRecognitionClosureReceiptFixture(t *testing.T) recognitionClosureReceiptFixture {
	t.Helper()
	raw, err := os.ReadFile("../testdata/recognition-batch-closure.json")
	if err != nil {
		t.Fatal(err)
	}
	var f recognitionClosureReceiptFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Targets []k12.RecognitionLayoutManifestTargetV2 `json:"targets"`
	}
	if err := json.Unmarshal([]byte(f.Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	f.Targets, f.Page = manifest.Targets, recognitionClosureReceiptPage(t, false)
	if len(f.Targets) != 16 || len(f.Batches) != 5 || len(f.Responses) != 5 {
		t.Fatal("receipt fixture does not contain the sixteen-target five-batch source")
	}
	if got := recognitionLayoutRuntimeTestDigest(f.Responses["layout_batch_0004"]); got != "sha256:cba0ae4fcb38529b294627e6de4dd85713045fc28f77a394cd44ff7b16398158" {
		t.Fatalf("raw fourth batch changed: %s", got)
	}
	return f
}

func recognitionClosureReceiptPage(t *testing.T, changed bool) []byte {
	t.Helper()
	page := image.NewRGBA(image.Rect(0, 0, 1086, 1448))
	if changed {
		page.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	}
	var out bytes.Buffer
	if err := png.Encode(&out, page); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func prepareRecognitionClosureReceiptSource(t *testing.T, f recognitionClosureReceiptFixture, scenario string) (*k12storage.Store, *sql.DB, string, k12.ModelInvocation, k12.RecognitionLayoutPlanV2, []k12.ModelPhysicalInvocation) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recognition-receipt-closure.db")
	store, db := openPhysicalLedgerFileStore(t, path)
	if err := migrate.Run(t.Context(), db, migrate.All); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO agents(name) VALUES(?)`, "mingming"); err != nil {
		t.Fatal(err)
	}
	job := newGradingJobRecord(t, "mingming", "closure-source-job")
	if _, err := store.Put(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	parent := unpreparedPhysicalInvocationParent(job.RecordID)
	parent.RouteSnapshot.Model, parent.RouteSnapshot.Route = "closure-model-a", "local/closure-model-a"
	parent.RequestPolicySnapshot = k12.ConfiguredRecognizingRequestPolicy()
	parent.RouteSnapshot.RecognizingRequestPolicy = parent.RequestPolicySnapshot
	parent, manifest, plan := prepareRecognitionClosureReceiptAttempt(t, store, f, parent)
	manifest = succeedRecognitionClosureReceipt(t, store, parent, manifest, f.Manifest)
	authorizeRecognitionClosureReceiptPlan(t, store, parent, manifest, plan)
	sources := []k12.ModelPhysicalInvocation{manifest}
	for i, batch := range plan.Batches {
		child := prepareRecognitionClosureReceiptBatch(t, store, parent, plan, i)
		content := f.Responses[string(batch.Unit)]
		if i == 3 && scenario == "unattributable" {
			content = `{"items":[{"target_id":"unmapped","kind":"question","recognition":{}}]}`
		}
		child = succeedRecognitionClosureReceipt(t, store, parent, child, content)
		settlement := recognitionClosureReceiptSettlement(t, f, plan, child, i == 3)
		if i == 3 && scenario == "extra_candidate" {
			settlement.AmbiguityKind = k12.RecognitionLayoutAmbiguityExtraCandidateV2
		}
		if _, created, err := store.SettleRecognitionLayoutPrimaryBatchV2(t.Context(), parent.AgentName, parent.InvocationID, settlement); err != nil || !created {
			t.Fatalf("source settlement %s: created=%v err=%v", batch.Unit, created, err)
		}
		sources = append(sources, child)
	}
	if _, err := store.MarkModelInvocationFailed(t.Context(), parent.AgentName, parent.InvocationID, "recognize_failed"); err != nil {
		t.Fatal(err)
	}
	if scenario == "source_digest" {
		if _, err := db.ExecContext(t.Context(), `UPDATE k12_model_physical_invocations SET result_content=result_content || ' ' WHERE physical_invocation_id=?`, sources[4].PhysicalInvocationID); err != nil {
			t.Fatal(err)
		}
	}
	return store, db, path, parent, plan, sources
}

func prepareRecognitionClosureReceiptAttempt(t *testing.T, store *k12storage.Store, f recognitionClosureReceiptFixture, parent k12.ModelInvocation) (k12.ModelInvocation, k12.ModelPhysicalInvocation, k12.RecognitionLayoutPlanV2) {
	t.Helper()
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{
		PagePNG: f.Page, Targets: f.Targets, RecognitionFormat: k12.RecognitionLayoutCompactV4,
		EnableSourceAdjudication: true,
		Manifest:                 k12.RecognitionLayoutManifestSuccessV2{InvocationID: parent.InvocationID + "-manifest", ResultDigest: recognitionLayoutRuntimeTestDigest(f.Manifest)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 5 {
		t.Fatalf("fixture plan has %d batches, want 5", len(plan.Batches))
	}
	for i, batch := range plan.Batches {
		if batch.Unit != f.Batches[i].Unit || len(batch.TargetIDs) != len(f.Batches[i].TargetIDs) {
			t.Fatalf("frozen batch %d no longer matches the real source grouping", i+1)
		}
	}
	header := k12.RecognitionLayoutPlanHeaderV2{
		PlanID: parent.InvocationID + "-plan", ParentInvocationID: parent.InvocationID,
		AgentName: parent.AgentName, JobID: parent.JobID, PageDigest: plan.PageDigest,
		ParentRequestDigest: parent.RequestDigest, RouteSnapshot: parent.RouteSnapshot, RequestPolicySnapshot: parent.RequestPolicySnapshot,
		StageStartedAtUnixMillis: time.Now().UnixMilli(), PhysicalCallCapMillis: 120000,
		BudgetBuckets:        k12.RecognitionLayoutBudgetBucketsV2{UpTo1ProblemMillis: 600000, UpTo8ProblemsMillis: 600000, UpTo16ProblemsMillis: 600000, UpTo32ProblemsMillis: 600000},
		AdapterWorkerHardCap: 2, EffectiveConcurrency: 1,
	}
	headerDigest, err := k12.RecognitionLayoutPlanHeaderDigestV2(header)
	if err != nil {
		t.Fatal(err)
	}
	manifest := newPhysicalInvocation(parent, plan.ManifestInvocationID, k12.RecognitionPhysicalUnitWholePage)
	manifest.RecognitionPlanVersion, manifest.PlanDigest = k12.RecognitionPlanVersionV2, headerDigest
	parent, manifest, created, err := store.PrepareRecognizingInvocationWithInitialLayoutPlanV2(t.Context(), parent, manifest, header)
	if err != nil || !created {
		t.Fatalf("prepare attempt: created=%v err=%v", created, err)
	}
	return parent, manifest, plan
}

func authorizeRecognitionClosureReceiptPlan(t *testing.T, store *k12storage.Store, parent k12.ModelInvocation, manifest k12.ModelPhysicalInvocation, plan k12.RecognitionLayoutPlanV2) {
	t.Helper()
	if err := store.AuthorizeRecognitionLayoutPlanV2(t.Context(), parent.AgentName, parent.InvocationID, k12.RecognitionLayoutManifestSuccessV2{InvocationID: manifest.PhysicalInvocationID, ResultDigest: manifest.ResultDigest}, plan); err != nil {
		t.Fatal(err)
	}
}

func prepareRecognitionClosureReceiptBatch(t *testing.T, store *k12storage.Store, parent k12.ModelInvocation, plan k12.RecognitionLayoutPlanV2, index int) k12.ModelPhysicalInvocation {
	t.Helper()
	child := recognitionLayoutRuntimeBatchInvocation(t, parent, plan, index)
	child.PhysicalInvocationID = parent.InvocationID + "-" + string(child.PhysicalUnit)
	child, created, err := store.PrepareModelPhysicalInvocation(t.Context(), child)
	if err != nil || !created {
		t.Fatalf("prepare batch: created=%v err=%v", created, err)
	}
	return child
}

func succeedRecognitionClosureReceipt(t *testing.T, store *k12storage.Store, parent k12.ModelInvocation, child k12.ModelPhysicalInvocation, content string) k12.ModelPhysicalInvocation {
	t.Helper()
	if _, claimed, err := store.ClaimModelPhysicalInvocationSent(t.Context(), parent.AgentName, child.PhysicalInvocationID); err != nil || !claimed {
		t.Fatalf("claim fixture source: claimed=%v err=%v", claimed, err)
	}
	child, err := store.MarkModelPhysicalInvocationSucceededWithContent(t.Context(), parent.AgentName, child.PhysicalInvocationID, content, "fixture-provider-receipt")
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func recognitionClosureReceiptSettlement(t *testing.T, f recognitionClosureReceiptFixture, plan k12.RecognitionLayoutPlanV2, source k12.ModelPhysicalInvocation, terminal bool) k12.RecognitionLayoutPrimaryBatchSettlementV2 {
	t.Helper()
	settlement := k12.RecognitionLayoutPrimaryBatchSettlementV2{
		PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: source.PhysicalInvocationID,
		SourcePhysicalUnit: source.PhysicalUnit, SourcePhysicalResultDigest: source.ResultDigest,
		Classification: k12.RecognitionLayoutBatchClassifiedV2,
	}
	if terminal {
		settlement.Classification, settlement.AmbiguityKind = k12.RecognitionLayoutBatchTerminalAmbiguousV2, k12.RecognitionLayoutAmbiguityUnattributableV2
		return settlement
	}
	content := f.Responses[string(source.PhysicalUnit)]
	// 第四批固定证据只缺最终 item 的闭合符，独立构造期望解析视图。
	if source.PhysicalUnit == "layout_batch_0004" {
		if !strings.HasSuffix(content, "}}]}") {
			t.Fatal("fourth batch is not the recorded single-closure fixture")
		}
		content = strings.TrimSuffix(content, "]}") + "}]}"
	}
	var envelope struct {
		Items []struct {
			TargetID    string          `json:"target_id"`
			Recognition json.RawMessage `json:"recognition"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, batch := range plan.Batches {
		if batch.Unit != source.PhysicalUnit {
			continue
		}
		if len(envelope.Items) != len(batch.TargetIDs) {
			t.Fatal("source items do not match the frozen batch")
		}
		for i, candidateID := range batch.TargetIDs {
			if envelope.Items[i].TargetID != fmt.Sprintf("t%d", i+1) {
				t.Fatal("source target order changed")
			}
			var candidate map[string]any
			if err := json.Unmarshal(envelope.Items[i].Recognition, &candidate); err != nil {
				t.Fatal(err)
			}
			canonical, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			settlement.Candidates = append(settlement.Candidates, k12.RecognitionLayoutCandidateSettlementV2{
				CandidateID: candidateID, Classification: k12.RecognitionLayoutCandidateValidV2,
				ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: canonical,
			})
		}
	}
	return settlement
}

func recognitionClosureSourceRows(t *testing.T, db *sql.DB, parentID string) string {
	t.Helper()
	var snapshot [][][]any
	for _, query := range []string{
		`SELECT * FROM k12_model_invocations WHERE invocation_id=?`,
		`SELECT * FROM k12_model_physical_invocations WHERE parent_invocation_id=? ORDER BY physical_invocation_id`,
		`SELECT * FROM k12_recognition_layout_batch_settlements WHERE parent_invocation_id=? ORDER BY source_physical_invocation_id`,
	} {
		rows, err := db.QueryContext(t.Context(), query, parentID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var table [][]any
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			table = append(table, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, table)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
