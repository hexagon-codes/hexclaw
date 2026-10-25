package usecase

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill"
)

type imageHistorySolver struct{ calls int }

func (s *imageHistorySolver) Solve(context.Context, string, string, string) (SolveResult, error) {
	s.calls++
	return SolveResult{}, errors.New("history must not invoke the solver")
}

func imageReportHistoryFixture(t *testing.T, frozen bool, sources ...completedSourceImageFixture) (completedSourceFixture, skill.InvocationContext, PrintableArtifactView, *finalReplyPDFRenderer) {
	t.Helper()
	f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentCompletedHomework, sources...)
	db := f.store.DB()
	for _, statement := range []string{
		`INSERT INTO sessions(id,user_id,platform,created_at,updated_at) VALUES('session-1','report-user','desktop','2026-10-25 00:00:00','2026-10-25 00:00:00')`,
		`INSERT INTO messages(id,session_id,role,content,request_id,created_at) VALUES('old-text-report','session-1','assistant','older text PDF','old-text','2026-10-25 00:00:01')`,
		`INSERT INTO messages(id,session_id,role,content,request_id,created_at) VALUES('source-correction','session-1','user','original worksheet','source-correction','2026-10-25 00:00:02')`,
		`INSERT INTO messages(id,session_id,role,content,request_id,created_at) VALUES('newer-text-report','session-1','assistant','newer text PDF','newer-text','2026-10-25 00:00:03')`,
		`INSERT INTO messages(id,session_id,role,content,request_id,created_at) VALUES('resend-current','session-1','user','resend the PDF','resend-current','2026-10-25 00:00:04')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	renderer := &finalReplyPDFRenderer{}
	f.o.deps.Renderer = renderer
	var pdf PrintableArtifactView
	if frozen {
		var err error
		pdf, _, err = f.o.deps.PrepareGradingFinalArtifactPDFExact(context.Background(), "mingming", f.old.ArtifactID, f.old.ArtifactDigest, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	inv := skill.InvocationContext{AuthenticatedUserID: "report-user", RoutedAgentName: "mingming", SessionID: "session-1", SourceMessageID: "resend-current", RequestID: "resend-current", Platform: "desktop"}
	return f, inv, pdf, renderer
}

func TestConversationImageReportUsesOriginalIMPhysicalSource(t *testing.T) {
	image := validPNGFixture(t, "history-im-original")
	f, inv, original, renderer := imageReportHistoryFixture(t, true, completedSourceImageFixture{sourceKind: k12.ImageTaskSourceIM, data: image})
	ctx := context.Background()
	bundle, _, err := f.store.AdmitInboundPhoto(ctx, InboundPhotoAdmission{OwnerScope: "guardian-final", AgentName: "mingming", BindingID: "original-binding", Identity: InboundPhotoIdentity{Platform: "dingtalk", InstanceID: "original-bot", ChatID: "original-chat", ProviderMessageID: "source-correction"}, AssetName: "original.png", AssetMIME: "image/png", AssetBytes: image, CommandJSON: `{"schema_version":2,"source_session_id":"session-1"}`})
	if err != nil {
		t.Fatal(err)
	}
	state := bundle.Dispatch.State()
	state.ProcessingStatus, state.RoutingDecision, state.ImageTaskID = InboundPhotoImageTaskSubmitted, InboundPhotoRouteNewSubmission, f.dispatch
	submitted, err := f.store.CompareAndSwapInboundPhotoDispatch(ctx, "mingming", bundle.Receipt.ReceiptID, bundle.Dispatch.Version, state)
	if err != nil {
		t.Fatal(err)
	}
	state = submitted.State()
	state.ProcessingStatus, state.FinalArtifactID, state.ReplyStatus = InboundPhotoFinalArtifactReady, f.old.ArtifactID, InboundPhotoReplyReady
	ready, err := f.store.CompareAndSwapInboundPhotoDispatch(ctx, "mingming", bundle.Receipt.ReceiptID, submitted.Version, state)
	if err != nil {
		t.Fatal(err)
	}
	inv.Platform, inv.PlatformInstanceID, inv.ChatID = "dingtalk", "original-bot", "original-chat"
	solver := &imageHistorySolver{}
	f.o.deps.Solver = solver
	view, err := f.o.deps.ReadConversationImageReport(ctx, inv, "old-text-report")
	if err != nil || view.Artifact.ArtifactID != original.Artifact.ArtifactID || view.Source.SourceMessageID != bundle.Receipt.Identity.ProviderMessageID || view.Source.OwnerScope != bundle.Receipt.OwnerScope {
		t.Fatalf("IM history lost the accepted physical source: %+v err=%v", view, err)
	}
	for _, mutate := range []func(*skill.InvocationContext){
		func(i *skill.InvocationContext) { i.PlatformInstanceID = "different-bot" },
		func(i *skill.InvocationContext) { i.ChatID = "different-chat" },
		func(i *skill.InvocationContext) { i.Platform = "feishu" },
	} {
		other := inv
		mutate(&other)
		if _, err := f.o.deps.ReadConversationImageReport(ctx, other, ""); !errors.Is(err, records.ErrNotFound) {
			t.Fatalf("IM history crossed a physical conversation: %v", err)
		}
	}
	after, err := f.store.GetInboundPhoto(ctx, "mingming", bundle.Receipt.ReceiptID)
	if err != nil || after.Dispatch.Version != ready.Version || after.Dispatch.State() != ready.State() || renderer.calls != 1 || solver.calls != 0 || f.reviewer.calls != 0 {
		t.Fatalf("history changed the original delivery intent or executed work: version=%d/%d render=%d review=%d err=%v", after.Dispatch.Version, ready.Version, renderer.calls, f.reviewer.calls, err)
	}
}

func TestConversationImageReportReadsFrozenBytesWithoutExecution(t *testing.T) {
	f, inv, original, renderer := imageReportHistoryFixture(t, true)
	ctx := context.Background()
	before := renderer.calls
	solver := &imageHistorySolver{}
	f.o.deps.Solver = solver
	view, err := f.o.deps.ReadConversationImageReport(ctx, inv, "old-text-report")
	if err != nil {
		t.Fatal(err)
	}
	if view.Artifact.ArtifactID != original.Artifact.ArtifactID || view.Artifact.ByteDigest != original.Render.ByteDigest || view.Content != original.Artifact.CanonicalMarkdown || view.Source.OwnerScope != "guardian-final" || view.Source.SourceMessageID != "source-correction" {
		t.Fatalf("history must retain the real image source and frozen PDF: %+v", view)
	}
	_, attachment, err := f.o.deps.ResolvePrintReplyArtifact(ctx, "mingming", view.Artifact.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := attachment.OpenReader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	actual, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(actual, original.Render.Payload) || renderer.calls != before || solver.calls != 0 || f.reviewer.calls != 0 {
		t.Fatalf("history executed work or changed PDF bytes: render=%d/%d review=%d err=%v", renderer.calls, before, f.reviewer.calls, err)
	}
	var batches int
	if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM k12_delivery_batches`).Scan(&batches); err != nil || batches != 0 {
		t.Fatalf("read-only history created a delivery batch: count=%d err=%v", batches, err)
	}
}

func TestConversationImageReportHonorsMessageOrderAndScope(t *testing.T) {
	f, inv, _, renderer := imageReportHistoryFixture(t, true)
	if _, err := f.o.deps.ReadConversationImageReport(context.Background(), inv, "newer-text-report"); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("older photo must not replace newer conversation report: %v", err)
	}
	for _, mutate := range []func(*skill.InvocationContext){
		func(i *skill.InvocationContext) { i.AuthenticatedUserID = "other-user" },
		func(i *skill.InvocationContext) { i.SessionID = "other-session" },
		func(i *skill.InvocationContext) { i.RoutedAgentName = "other-agent" },
		func(i *skill.InvocationContext) {
			i.Platform, i.PlatformInstanceID, i.ChatID = "dingtalk", "other-bot", "other-chat"
		},
	} {
		candidate := inv
		mutate(&candidate)
		if _, err := f.o.deps.ReadConversationImageReport(context.Background(), candidate, ""); err == nil {
			t.Fatal("history crossed the actual user, session, agent or physical channel")
		}
	}
	if renderer.calls != 1 {
		t.Fatalf("scope rejection rendered another PDF: %d", renderer.calls)
	}
}

func TestConversationImageReportWithoutFrozenPDFDoesNotPrepare(t *testing.T) {
	f, inv, _, renderer := imageReportHistoryFixture(t, false)
	_, err := f.o.deps.ReadConversationImageReport(context.Background(), inv, "old-text-report")
	if !errors.Is(err, ErrFrozenImageReportUnavailable) || renderer.calls != 0 || f.reviewer.calls != 0 {
		t.Fatalf("missing PDF must remain missing without rendering or solving: render=%d review=%d err=%v", renderer.calls, f.reviewer.calls, err)
	}
	var count int
	if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM k12_print_artifacts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("read created a fake artifact: count=%d err=%v", count, err)
	}
}
