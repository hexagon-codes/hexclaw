package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	sessionpkg "github.com/hexagon-codes/hexclaw/session"
	"github.com/hexagon-codes/hexclaw/storage"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

const FollowupReferenceText = "请保留原范围，补充代回检验。"

type FollowupReferenceGenerator struct {
	resolveCalls  atomic.Int32
	generateCalls atomic.Int32
	unknown       atomic.Bool
	resolved      chan UnitSummaryResolveInput
	generated     chan UnitSummaryGenerationInput
}

func (g *FollowupReferenceGenerator) ResolveUnitSummary(_ context.Context, input UnitSummaryResolveInput) (UnitSummaryResolution, error) {
	g.resolved <- input
	if g.resolveCalls.Add(1) == 1 {
		return UnitSummaryResolution{Subject: "math", Intent: "revise", Format: "standard", MissingFields: []string{"material"}, Clarification: "Please identify the material to revise."}, nil
	}
	if input.Request.ActiveRevisionID != "" {
		return UnitSummaryResolution{Subject: "math", Intent: "revise", Format: "standard", UnitTitle: "分配律与方程", UseActiveRevision: true, Requirements: FollowupReferenceText}, nil
	}
	return UnitSummaryResolution{Subject: "math", Intent: "generate", Format: "standard", UnitTitle: "分配律与方程", ProvidedMaterialText: StageRecoverySourceText, Requirements: FollowupReferenceText}, nil
}

func (g *FollowupReferenceGenerator) GenerateUnitSummary(_ context.Context, input UnitSummaryGenerationInput) (k12.UnitSummaryContentV1, error) {
	g.generateCalls.Add(1)
	g.generated <- input
	if g.unknown.Load() {
		return k12.UnitSummaryContentV1{}, context.DeadlineExceeded
	}
	// 来源已冻结后停止；此组不把可控模型边界当正文或PDF质量证据。
	return k12.UnitSummaryContentV1{}, errors.Join(egress.ErrProviderResponseProcessed, errors.New("controlled stop after followup source freeze"))
}

type FollowupReferenceFixture struct {
	base    *StageRecoveryFixture
	c       *UnitSummaryCoordinator
	g       *FollowupReferenceGenerator
	pending UnitSummaryView
	request UnitSummaryRequest
	head    k12.UnitSummaryRevision
}

func FollowupReferenceNewFixture(t *testing.T, originalActive, advanceHead bool) *FollowupReferenceFixture {
	t.Helper()
	base := StageRecoveryNewFixture(t)
	f := &FollowupReferenceFixture{base: base, head: base.old}
	if advanceHead {
		published := StageRecoveryStartTarget(t, base)
		if published.Material == nil || published.Material.Version != 2 || published.Material.DocumentID != base.old.DocumentID {
			t.Fatalf("fixture did not publish an independent newer head: %+v", published.Job)
		}
		f.head = published.Material.UnitSummaryRevision
	}
	f.g = &FollowupReferenceGenerator{resolved: make(chan UnitSummaryResolveInput, 4), generated: make(chan UnitSummaryGenerationInput, 4)}
	f.c = NewUnitSummaryCoordinator(Deps{Records: base.store, Renderer: base.renderer}, f.g,
		func(_ context.Context, route k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) {
			return route, nil
		})
	f.c.SetBaseContext(base.ctx)
	f.request = StageRecoveryRequest("followup-reference-original", "请更新当前数学资料，尚待确认本次材料。")
	if originalActive {
		f.request.ActiveRevisionID = base.old.RevisionID
	}
	StageRecoveryInsertMessage(t, base, f.request)
	var replayed bool
	var err error
	f.pending, replayed, err = f.c.StartAndWait(base.ctx, f.request)
	StageRecoveryWaitIdle(t, f.c)
	if err != nil || replayed || f.pending.Job.State != "needs_input" || f.pending.Material != nil || f.g.resolveCalls.Load() != 1 || f.g.generateCalls.Load() != 0 {
		t.Fatalf("actual coordinator did not stop at the intended clarification: error=%v replayed=%v job=%+v", err, replayed, f.pending.Job)
	}
	<-f.g.resolved
	return f
}

func FollowupReferenceMetadata(t *testing.T, document, revision string, version int) map[string]any {
	t.Helper()
	return map[string]any{"k12_active_material": map[string]any{"version": version, "document_id": document, "revision_id": revision}}
}

func FollowupReferenceInsert(t *testing.T, f *FollowupReferenceFixture, session, message, text string, metadata map[string]any) {
	t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var dbPath string
	if err := f.base.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	producer, err := sqlitestore.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	if _, legacy := metadata["k12_active_material"].(string); legacy {
		if err := producer.SaveMessage(f.base.ctx, &storage.MessageRecord{ID: message, SessionID: session, Role: "user", Content: text, Metadata: string(raw)}); err != nil {
			t.Fatal(err)
		}
	} else {
		invocation := map[string]string{"request_id": message}
		for key, value := range metadata {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			invocation[key] = string(encoded)
		}
		manager := sessionpkg.NewManager(producer, config.MemoryConfig{})
		if err := manager.SaveUserMessage(f.base.ctx, session, &adapter.Message{ID: message, SessionID: session, UserID: f.request.OwnerID, Content: text, Metadata: invocation}); err != nil {
			t.Fatal(err)
		}
	}
	public, err := producer.GetMessage(f.base.ctx, message)
	if err != nil || public.SessionID != session || public.Role != "user" || public.Content != text {
		t.Fatalf("public producer/readback lost the exact followup: %v", err)
	}
	var got, want any
	if err := json.Unmarshal([]byte(public.Metadata), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("public message projection lost canonical scenario metadata")
	}
	var metadataColumn, attachmentColumn string
	if err := f.base.db.QueryRow(`SELECT COALESCE(metadata,''),COALESCE(attachments,'') FROM messages WHERE id=?`, message).Scan(&metadataColumn, &attachmentColumn); err != nil {
		t.Fatal(err)
	}
	t.Logf("public followup producer: metadata_bytes=%d attachments_bytes=%d", len(metadataColumn), len(attachmentColumn))
}

func FollowupReferenceSnapshot(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range []string{"agents", "sessions", "messages", "k12_unit_summary_attempts", "k12_unit_summary_documents", "k12_unit_summary_revisions", "k12_unit_summary_resume_commands", "k12_unit_summary_model_invocations", "k12_unit_summary_received_orders", "k12_print_artifacts", "k12_print_artifact_renders"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		out[table] = []string{}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			out[table] = append(out[table], string(raw))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	return out
}

func FollowupReferenceAssertChangedMessage(t *testing.T, f *FollowupReferenceFixture, message, text string) {
	t.Helper()
	var dbPath string
	if err := f.base.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	reader, err := sqlitestore.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	public, err := reader.GetMessage(f.base.ctx, message)
	if err != nil || public.SessionID != f.request.SessionID || public.Content != text {
		t.Fatalf("changed followup was not visible through the public message reader: %v", err)
	}
	var metadata map[string]json.RawMessage
	var active string
	if json.Unmarshal([]byte(public.Metadata), &metadata) != nil || json.Unmarshal(metadata["k12_active_material"], &active) != nil || active != "invalid later metadata" {
		t.Fatal("old attachment envelope hid the changed public metadata")
	}
}

func FollowupReferenceResume(t *testing.T, f *FollowupReferenceFixture, command UnitSummaryResumeRequest) UnitSummaryView {
	t.Helper()
	view, replayed, err := f.c.ResumeAndWait(f.base.ctx, f.request.OwnerID, f.request.AgentName, f.pending.Job.AttemptID, command)
	StageRecoveryWaitIdle(t, f.c)
	if err != nil || replayed || view.Job.AttemptID != f.pending.Job.AttemptID || view.Job.State != "failed" || view.Job.FailureKind != "generation_failed" || f.g.resolveCalls.Load() != 2 || f.g.generateCalls.Load() != 1 {
		t.Fatalf("followup did not reach the one controlled generation boundary: error=%v replayed=%v job=%+v resolve=%d generate=%d", err, replayed, view.Job, f.g.resolveCalls.Load(), f.g.generateCalls.Load())
	}
	return view
}

func TestUnitSummaryFollowupReferenceAdoptsExactHistorySQLite(t *testing.T) {
	for _, mode := range []string{"historical_reference", "serialized_active_compatibility", "ordinary_text_without_reference"} {
		t.Run(mode, func(t *testing.T) {
			f := FollowupReferenceNewFixture(t, false, mode != "ordinary_text_without_reference")
			text := FollowupReferenceText
			var metadata map[string]any
			if mode != "ordinary_text_without_reference" {
				metadata = FollowupReferenceMetadata(t, f.base.old.DocumentID, f.base.old.RevisionID, 1)
				if mode == "serialized_active_compatibility" {
					raw, err := json.Marshal(metadata["k12_active_material"])
					if err != nil {
						t.Fatal(err)
					}
					metadata["k12_active_material"] = string(raw)
				}
			} else {
				text = "本次数学材料：" + StageRecoverySourceText + "\n" + FollowupReferenceText
				metadata = map[string]any{"prompt_invocation": map[string]any{"version": 1, "command_id": "k12-unit-math", "edited": true}}
			}
			FollowupReferenceInsert(t, f, f.request.SessionID, "followup-message", text, metadata)
			command := UnitSummaryResumeRequest{ExpectedRevision: f.pending.Job.Revision, IdempotencyKey: "followup-adopt", FollowupMessageID: "followup-message"}
			view := FollowupReferenceResume(t, f, command)
			var frozen UnitSummaryRequest
			if err := json.Unmarshal([]byte(view.Job.RequestJSON), &frozen); err != nil {
				t.Fatal(err)
			}
			if frozen.FinalUserText != f.request.FinalUserText+"\n\n"+text || view.Job.RequestDigest != f.pending.Job.RequestDigest {
				t.Fatal("followup changed the original request identity or lost exact persisted text")
			}
			resolved, generated := <-f.g.resolved, <-f.g.generated
			if mode != "ordinary_text_without_reference" {
				if frozen.ActiveRevisionID != f.base.old.RevisionID || resolved.ActiveRevision == nil || !reflect.DeepEqual(*resolved.ActiveRevision, f.base.old) || generated.BaseRevisionID != f.base.old.RevisionID || generated.Context.BaseRevisionID != f.base.old.RevisionID {
					t.Fatalf("the exact historical reference was replaced by a current head: frozen=%q active=%+v base=%q", frozen.ActiveRevisionID, resolved.ActiveRevision, generated.BaseRevisionID)
				}
				if !reflect.DeepEqual(generated.Context.Sources, f.base.old.Context.Sources) || !reflect.DeepEqual(generated.Context.Evidence, f.base.old.Context.Evidence) || generated.Context.UnitTitle != f.base.old.Context.UnitTitle || generated.Context.GradeTerm != f.base.old.Context.GradeTerm {
					t.Fatal("historical source, evidence or grade/unit snapshot changed")
				}
			} else if frozen.ActiveRevisionID != "" || resolved.ActiveRevision != nil || len(generated.Context.Sources) != 1 || generated.Context.Sources[0].Text != StageRecoverySourceText || generated.Context.Sources[0].Origin != "provided_material" {
				t.Fatal("ordinary text followup acquired an invented active reference or lost its actual material")
			}
			StageRecoveryOldUnchanged(t, f.base, mode == "ordinary_text_without_reference")
			head, err := f.base.store.GetUnitSummaryDocument(f.base.ctx, f.request.OwnerID, f.request.AgentName, f.base.old.DocumentID)
			if err != nil || head.CurrentRevisionID != f.head.RevisionID || head.HeadVersion != f.head.Version {
				t.Fatalf("source capture displaced the existing published head: %+v error=%v", head, err)
			}
		})
	}
}

func FollowupReferenceForeignMaterial(t *testing.T, f *FollowupReferenceFixture, foreignOwner, foreignChild bool) k12.UnitSummaryRevision {
	t.Helper()
	req := StageRecoveryRequest("followup-foreign-material", "整理数学材料："+StageRecoverySourceText)
	req.SessionID = "followup-foreign-session"
	if foreignOwner {
		req.OwnerID = "followup-other-owner"
	}
	if foreignChild {
		req.AgentName = "followup-other-child"
		if _, err := f.base.db.Exec(`INSERT INTO agents(name,display_name) VALUES(?,'Other child')`, req.AgentName); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.base.db.Exec(`INSERT INTO sessions(id,user_id,created_at,updated_at) VALUES(?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, req.SessionID, req.OwnerID); err != nil {
		t.Fatal(err)
	}
	StageRecoveryInsertMessage(t, f.base, req)
	view, _, err := f.base.coordinator.StartAndWait(f.base.ctx, req)
	StageRecoveryWaitIdle(t, f.base.coordinator)
	if err != nil || view.Material == nil || !view.Delivery.Complete {
		t.Fatalf("foreign fixture material was not published through the original coordinator: %v", err)
	}
	return view.Material.UnitSummaryRevision
}

func TestUnitSummaryFollowupReferenceRejectsWithoutWritesSQLite(t *testing.T) {
	for _, mode := range []string{"owner", "child", "session", "document", "revision", "schema", "incomplete", "original_reference_overwrite"} {
		t.Run(mode, func(t *testing.T) {
			f := FollowupReferenceNewFixture(t, mode == "original_reference_overwrite", mode == "original_reference_overwrite")
			document, revision, version := f.base.old.DocumentID, f.base.old.RevisionID, 1
			session := f.request.SessionID
			wantErr := records.ErrNotFound
			switch mode {
			case "owner", "child":
				foreign := FollowupReferenceForeignMaterial(t, f, mode == "owner", mode == "child")
				document, revision = foreign.DocumentID, foreign.RevisionID
			case "session":
				session = "followup-other-session"
				if _, err := f.base.db.Exec(`INSERT INTO sessions(id,user_id,created_at,updated_at) VALUES(?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, session, f.request.OwnerID); err != nil {
					t.Fatal(err)
				}
			case "document":
				document = "document-not-matching-the-existing-revision"
			case "revision":
				revision = "revision-that-does-not-exist"
			case "schema":
				version, wantErr = 2, ErrInvalidInput
			case "incomplete":
				document, wantErr = "", ErrInvalidInput
			case "original_reference_overwrite":
				revision, wantErr = f.head.RevisionID, k12storage.ErrUnitSummaryConflict
			}
			FollowupReferenceInsert(t, f, session, "rejected-followup", FollowupReferenceText, FollowupReferenceMetadata(t, document, revision, version))
			before := FollowupReferenceSnapshot(t, f.base.db)
			_, replayed, err := f.c.Resume(f.base.ctx, f.request.OwnerID, f.request.AgentName, f.pending.Job.AttemptID,
				UnitSummaryResumeRequest{ExpectedRevision: f.pending.Job.Revision, IdempotencyKey: "followup-rejected", FollowupMessageID: "rejected-followup"})
			StageRecoveryWaitIdle(t, f.c)
			if !errors.Is(err, wantErr) || replayed {
				t.Fatalf("invalid followup was accepted or misclassified: error=%v want=%v replayed=%v", err, wantErr, replayed)
			}
			if !reflect.DeepEqual(before, FollowupReferenceSnapshot(t, f.base.db)) || f.g.resolveCalls.Load() != 1 || f.g.generateCalls.Load() != 0 {
				t.Fatal("rejected followup wrote task/command/source/artifact rows or called a model boundary")
			}
		})
	}
}

func TestUnitSummaryFollowupReferenceKeepsReplayCASAndUnknownSQLite(t *testing.T) {
	t.Run("stale_and_frozen_command_replay", func(t *testing.T) {
		f := FollowupReferenceNewFixture(t, false, false)
		FollowupReferenceInsert(t, f, f.request.SessionID, "replay-followup", FollowupReferenceText, FollowupReferenceMetadata(t, f.base.old.DocumentID, f.base.old.RevisionID, 1))
		before := FollowupReferenceSnapshot(t, f.base.db)
		command := UnitSummaryResumeRequest{ExpectedRevision: f.pending.Job.Revision - 1, IdempotencyKey: "followup-command", FollowupMessageID: "replay-followup"}
		if _, _, err := f.c.Resume(f.base.ctx, f.request.OwnerID, f.request.AgentName, f.pending.Job.AttemptID, command); !errors.Is(err, k12storage.ErrUnitSummaryCAS) {
			t.Fatalf("stale revision was accepted: %v", err)
		}
		if !reflect.DeepEqual(before, FollowupReferenceSnapshot(t, f.base.db)) {
			t.Fatal("stale resume mutated durable rows")
		}
		command.ExpectedRevision = f.pending.Job.Revision
		frozen := FollowupReferenceResume(t, f, command)
		if _, err := f.base.db.Exec(`UPDATE messages SET content='changed after freeze',attachments='{"k12_active_material":"invalid later metadata"}' WHERE id='replay-followup'`); err != nil {
			t.Fatal(err)
		}
		FollowupReferenceAssertChangedMessage(t, f, "replay-followup", "changed after freeze")
		before = FollowupReferenceSnapshot(t, f.base.db)
		replayed, replay, err := f.c.ResumeAndWait(f.base.ctx, f.request.OwnerID, f.request.AgentName, f.pending.Job.AttemptID, command)
		StageRecoveryWaitIdle(t, f.c)
		if err != nil || !replay || replayed.Job.RequestJSON != frozen.Job.RequestJSON || !reflect.DeepEqual(replayed.Job.Context, frozen.Job.Context) || f.g.resolveCalls.Load() != 2 || f.g.generateCalls.Load() != 1 || !reflect.DeepEqual(before, FollowupReferenceSnapshot(t, f.base.db)) {
			t.Fatalf("replayed command consumed changed metadata or repeated work: error=%v replayed=%v", err, replay)
		}
	})
	t.Run("unknown_does_not_consume_new_reference_or_resend", func(t *testing.T) {
		f := FollowupReferenceNewFixture(t, false, false)
		f.g.unknown.Store(true)
		FollowupReferenceInsert(t, f, f.request.SessionID, "unknown-followup", FollowupReferenceText, FollowupReferenceMetadata(t, f.base.old.DocumentID, f.base.old.RevisionID, 1))
		command := UnitSummaryResumeRequest{ExpectedRevision: f.pending.Job.Revision, IdempotencyKey: "followup-to-unknown", FollowupMessageID: "unknown-followup"}
		unknown, _, err := f.c.ResumeAndWait(f.base.ctx, f.request.OwnerID, f.request.AgentName, f.pending.Job.AttemptID, command)
		StageRecoveryWaitIdle(t, f.c)
		if err != nil || unknown.Job.State != "reconciling" || f.g.resolveCalls.Load() != 2 || f.g.generateCalls.Load() != 1 {
			t.Fatalf("controlled invocation did not retain unknown: error=%v state=%s", err, unknown.Job.State)
		}
		before, err := f.base.store.ListUnitSummaryModelInvocations(f.base.ctx, f.request.AgentName, unknown.Job.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.base.db.Exec(`UPDATE messages SET attachments='{"k12_active_material":"invalid later metadata"}' WHERE id='unknown-followup'`); err != nil {
			t.Fatal(err)
		}
		FollowupReferenceAssertChangedMessage(t, f, "unknown-followup", FollowupReferenceText)
		view, _, err := f.c.ResumeAndWait(f.base.ctx, f.request.OwnerID, f.request.AgentName, unknown.Job.AttemptID, UnitSummaryResumeRequest{ExpectedRevision: unknown.Job.Revision, IdempotencyKey: "observe-original-unknown", FollowupMessageID: "unknown-followup"})
		StageRecoveryWaitIdle(t, f.c)
		after, readErr := f.base.store.ListUnitSummaryModelInvocations(f.base.ctx, f.request.AgentName, unknown.Job.AttemptID)
		if err != nil || readErr != nil || view.Job.State != "reconciling" || view.Job.RequestJSON != unknown.Job.RequestJSON || !reflect.DeepEqual(before, after) || f.g.resolveCalls.Load() != 2 || f.g.generateCalls.Load() != 1 {
			t.Fatalf("unknown observation consumed new metadata or resent a model: error=%v read=%v", err, readErr)
		}
		StageRecoveryOldUnchanged(t, f.base, true)
	})
	t.Run("published_attempt_refuses_new_followup", func(t *testing.T) {
		f := FollowupReferenceNewFixture(t, false, false)
		FollowupReferenceInsert(t, f, f.request.SessionID, "published-followup", FollowupReferenceText, FollowupReferenceMetadata(t, f.base.old.DocumentID, f.base.old.RevisionID, 1))
		before := FollowupReferenceSnapshot(t, f.base.db)
		_, _, err := f.c.Resume(f.base.ctx, f.request.OwnerID, f.request.AgentName, f.base.old.AttemptID, UnitSummaryResumeRequest{ExpectedRevision: 0, IdempotencyKey: "published-no-resume", FollowupMessageID: "published-followup"})
		if !errors.Is(err, k12storage.ErrUnitSummaryCAS) || !reflect.DeepEqual(before, FollowupReferenceSnapshot(t, f.base.db)) || f.g.resolveCalls.Load() != 1 || f.g.generateCalls.Load() != 0 {
			t.Fatalf("published attempt accepted followup or wrote new work: %v", err)
		}
		StageRecoveryOldUnchanged(t, f.base, true)
	})
}
