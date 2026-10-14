package skilladapter

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	_ "modernc.org/sqlite"
)

type unitReplayGenerator struct{ calls atomic.Int32 }

func (g *unitReplayGenerator) ResolveUnitSummary(context.Context, usecase.UnitSummaryResolveInput) (usecase.UnitSummaryResolution, error) {
	g.calls.Add(1)
	return usecase.UnitSummaryResolution{}, errors.New("unexpected model resolution")
}

func (g *unitReplayGenerator) GenerateUnitSummary(context.Context, usecase.UnitSummaryGenerationInput) (k12.UnitSummaryContentV1, error) {
	g.calls.Add(1)
	return k12.UnitSummaryContentV1{}, errors.New("unexpected model generation")
}

type unitReplayFixture struct {
	db        *sql.DB
	store     *k12storage.Store
	request   usecase.UnitSummaryRequest
	inv       skill.InvocationContext
	attempt   k12.UnitSummaryAttempt
	revision  k12.UnitSummaryRevision
	generator *unitReplayGenerator
	adapter   *UnitSummarySkill
}

func newUnitReplayFixture(t *testing.T, unknown bool) *unitReplayFixture {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "unit-replay.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err := migrate.Run(ctx, db, migrate.All); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO agents(name,display_name) VALUES('replay-child','Replay child')`,
		`INSERT INTO sessions(id,user_id,created_at,updated_at) VALUES('replay-session','replay-owner',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
		`INSERT INTO messages(id,session_id,role,content,created_at) VALUES('replay-message','replay-session','user','整理本次数学材料',CURRENT_TIMESTAMP)`,
		`INSERT INTO messages(id,session_id,role,content,created_at) VALUES('other-message','replay-session','user','整理本次数学材料',CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	f := &unitReplayFixture{db: db, store: k12storage.NewStore(db, nil), generator: &unitReplayGenerator{}}
	f.request = usecase.UnitSummaryRequest{
		OwnerID: "replay-owner", AgentName: "replay-child", SessionID: "replay-session", SourceMessageID: "replay-message",
		IdempotencyKey: "replay-turn:unit-summary", FinalUserText: "整理本次数学材料", Subject: "math", Intent: "generate",
		PromptInvocation: json.RawMessage(`{"version":1,"kind":"unit_summary","subject":"math"}`),
		ModelSnapshot:    k12.GradingModelSnapshot{Provider: "local-fixture", Model: "fixture-model"},
	}
	f.inv = skill.InvocationContext{RoutedAgentName: f.request.AgentName, RequestID: "replay-turn", SessionID: f.request.SessionID, SourceMessageID: f.request.SourceMessageID,
		ScenarioInput: map[string]string{"k12_task_intent": string(f.request.PromptInvocation), "provider": "local-fixture", "model": "fixture-model"}}
	raw, err := json.Marshal(f.request)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := f.store.CreateUnitSummaryAttempt(ctx, k12.UnitSummaryAttempt{
		OwnerID: f.request.OwnerID, AgentName: f.request.AgentName, SessionID: f.request.SessionID, SourceMessageID: f.request.SourceMessageID,
		IdempotencyKey: f.request.IdempotencyKey, RequestDigest: k12.UnitSummaryDigest(f.request), FinalUserText: f.request.FinalUserText, RequestJSON: string(raw),
		Context: k12.UnitSummaryContext{Subject: "math", UnitTitle: "本次数学材料"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, claimed, err := f.store.ClaimUnitSummaryAttempt(ctx, a.OwnerID, a.AgentName, a.AttemptID, "original-worker", time.Now().Unix()+600)
	if err != nil || !claimed {
		t.Fatalf("claim original attempt: %v, claimed=%v", err, claimed)
	}
	if unknown {
		inv, _, err := f.store.PrepareUnitSummaryModelInvocation(ctx, k12.ModelInvocation{InvocationID: "modelinv-original", AgentName: a.AgentName, JobID: a.AttemptID, Stage: "unit_summary_generate", RequestDigest: k12.UnitSummaryDigest("original generation"), Attempt: 1, RouteSnapshot: f.request.ModelSnapshot})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MarkUnitSummaryModelInvocationSent(ctx, a.AgentName, inv.InvocationID, "original-provider-key"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MarkUnitSummaryModelInvocationOutcomeUnknown(ctx, a.AgentName, inv.InvocationID, "provider_outcome_unknown"); err != nil {
			t.Fatal(err)
		}
		next := a
		next.State, next.Stage, next.InvocationRef = "reconciling", "generate", inv.InvocationID
		a, err = f.store.CheckpointUnitSummaryAttempt(ctx, a, next)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		a, _, err = f.store.BindUnitSummaryDocument(ctx, a, "original-scope")
		if err != nil {
			t.Fatal(err)
		}
		const markdown = "# 冻结数学资料\n\n0.25x+0.5x=18，x=24。\n\n家长讲法保持本版内容。\n"
		payload := []byte("%PDF-1.4\nlocal byte fixture\n%%EOF\n")
		digest := sha256.Sum256(payload)
		byteDigest := hex.EncodeToString(digest[:])
		now := time.Now()
		_, _, _, err = f.store.FreezePrintArtifact(ctx, k12.PrintArtifact{ArtifactID: "part-original", AgentName: a.AgentName, SourceKind: k12.PrintSourceUnitSummary, SourceRef: "usr-original", Title: "冻结数学资料", CanonicalMarkdown: markdown, SourceDigest: k12.UnitSummaryDigest(markdown), CreatedAt: now.Unix()},
			k12.PrintArtifactRender{ArtifactID: "part-original", Format: "pdf", RenderContractVersion: "local-unit-fixture", ContentType: "application/pdf", ByteDigest: byteDigest, ByteSize: int64(len(payload)), Payload: payload, CreatedAt: now.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		date := now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
		next := a
		next.ContentDigest = k12.UnitSummaryDigest("frozen mathematics content")
		next.Candidate = &k12.UnitSummaryCandidate{ExpectedHeadVersion: 0, CandidateVersion: 1, ArtifactID: "part-original", ByteDigest: byteDigest}
		a, err = f.store.CheckpointUnitSummaryAttempt(ctx, a, next)
		if err != nil {
			t.Fatal(err)
		}
		a, f.revision, err = f.store.PublishUnitSummary(ctx, a, &k12.UnitSummaryRevision{RevisionID: "usr-original", DocumentID: a.DocumentID, AgentName: a.AgentName, Version: 1, AttemptID: a.AttemptID,
			Content: k12.UnitSummaryContentV1{SchemaVersion: 1, Subject: "math", Title: "冻结数学资料"}, CanonicalMarkdown: markdown, ContentDigest: a.ContentDigest, Context: a.Context,
			GeneratedDate: date, GeneratedTimezone: "Asia/Shanghai", Filename: "frozen-math-v01.pdf", ArtifactID: "part-original", ByteDigest: byteDigest, ByteSize: int64(len(payload))})
		if err != nil {
			t.Fatal(err)
		}
	}
	f.attempt = a
	coordinator := usecase.NewUnitSummaryCoordinator(usecase.Deps{Records: f.store, TextbookOwnerID: f.request.OwnerID}, f.generator,
		func(_ context.Context, route k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
			return route, nil
		})
	f.adapter = NewUnitSummarySkill(coordinator)
	return f
}

func (f *unitReplayFixture) snapshot(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range []string{"k12_unit_summary_attempts", "k12_unit_summary_documents", "k12_unit_summary_revisions", "k12_unit_summary_model_invocations", "k12_print_artifacts", "k12_print_artifact_renders"} {
		rows, err := f.db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		out[table] = []string{}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], string(raw))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out
}

func TestUnitSummarySkillReplayReadsFrozenMaterialSQLite(t *testing.T) {
	f := newUnitReplayFixture(t, false)
	before := f.snapshot(t)
	for _, action := range []struct{ subject, intent string }{{"math", "generate"}, {"chinese", "open"}, {"science", "follow_up"}} {
		result, err := f.adapter.Execute(skill.WithInvocationContext(context.Background(), f.inv), map[string]any{"subject": action.subject, "intent": action.intent})
		if err != nil {
			t.Fatalf("%s: %v", action.intent, err)
		}
		var receipt struct {
			State             string            `json:"state"`
			Complete          bool              `json:"delivery_complete"`
			CanonicalMarkdown string            `json:"canonical_markdown"`
			Reference         map[string]string `json:"material_reference"`
		}
		if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.State != "succeeded" || !receipt.Complete || receipt.CanonicalMarkdown != f.revision.CanonicalMarkdown || receipt.Reference["attempt_id"] != f.attempt.AttemptID || receipt.Reference["revision_id"] != f.revision.RevisionID || receipt.Reference["artifact_id"] != f.revision.ArtifactID {
			t.Fatalf("%s did not read the original frozen material: %+v", action.intent, receipt)
		}
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) || f.generator.calls.Load() != 0 {
		t.Fatalf("read replay changed frozen records or called a model: calls=%d", f.generator.calls.Load())
	}
}

func TestUnitSummarySkillReplayReturnsOriginalUnknownSQLite(t *testing.T) {
	f := newUnitReplayFixture(t, true)
	before := f.snapshot(t)
	for _, intent := range []string{"generate", "open", "follow_up"} {
		ctx, cancel := context.WithTimeout(skill.WithInvocationContext(context.Background(), f.inv), time.Second)
		result, err := f.adapter.Execute(ctx, map[string]any{"subject": "english", "intent": intent})
		contextErr := ctx.Err()
		cancel()
		if err != nil || contextErr != nil {
			t.Fatalf("%s did not promptly return unknown: %v, context=%v", intent, err, contextErr)
		}
		var receipt struct {
			State     string            `json:"state"`
			Complete  bool              `json:"delivery_complete"`
			Reference map[string]string `json:"material_reference"`
		}
		if err := json.Unmarshal([]byte(result.Content), &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.State != "reconciling" || receipt.Complete || receipt.Reference["attempt_id"] != f.attempt.AttemptID {
			t.Fatalf("%s changed unknown task identity or state: %+v", intent, receipt)
		}
	}
	// 原 worker 的有效租约让观察调度只读退出；不把此 fixture 当成过期租约发送验证。
	time.Sleep(20 * time.Millisecond)
	if !reflect.DeepEqual(before, f.snapshot(t)) || f.generator.calls.Load() != 0 {
		t.Fatalf("unknown replay changed the ledger or called a model: calls=%d", f.generator.calls.Load())
	}
}

func TestUnitSummarySkillReplayRejectsChangedTrustedInputsSQLite(t *testing.T) {
	for _, change := range []string{"owner", "session", "source_message", "final_text", "prompt_invocation", "active_revision", "provider", "model"} {
		t.Run(change, func(t *testing.T) {
			f := newUnitReplayFixture(t, false)
			inv := f.inv
			inv.ScenarioInput = map[string]string{}
			for key, value := range f.inv.ScenarioInput {
				inv.ScenarioInput[key] = value
			}
			want := k12storage.ErrUnitSummaryConflict
			switch change {
			case "owner":
				f.adapter.coordinator.Deps.TextbookOwnerID = "other-owner"
			case "session":
				inv.SessionID = "other-session"
				want = records.ErrNotFound
			case "source_message":
				inv.SourceMessageID = "other-message"
			case "final_text":
				if _, err := f.db.Exec(`UPDATE messages SET content='改为语文学习材料' WHERE id='replay-message'`); err != nil {
					t.Fatal(err)
				}
			case "prompt_invocation":
				inv.ScenarioInput["k12_task_intent"] = `{"version":1,"kind":"unit_summary","subject":"art"}`
			case "active_revision":
				inv.ScenarioInput["k12_active_material"] = `{"revision_id":"usr-other"}`
			case "provider", "model":
				inv.ScenarioInput[change] = "other-" + change
			}
			before := f.snapshot(t)
			result, err := f.adapter.Execute(skill.WithInvocationContext(context.Background(), inv), map[string]any{"subject": "chinese", "intent": "open"})
			if !errors.Is(err, want) || result != nil {
				t.Fatalf("changed %s accepted or returned another error: result=%+v, err=%v, want=%v", change, result, err, want)
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) || f.generator.calls.Load() != 0 {
				t.Fatalf("changed %s caused new domain writes or a model call: calls=%d", change, f.generator.calls.Load())
			}
		})
	}
}
