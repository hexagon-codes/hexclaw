package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/channel"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

const standaloneIMProblem = "一条公路甲乙合作6天完成，甲先做3天，乙再做10.5天，甲单独需要几天？"

type standaloneIMModel struct{ printReplyAPIModel }

func (m *standaloneIMModel) Complete(_ context.Context, request llm.CompletionRequest) (*llm.CompletionResponse, error) {
	m.calls.Add(1)
	for _, message := range request.Messages {
		if message.Role == llm.RoleTool {
			return &llm.CompletionResponse{Content: "答案是10天，完整讲解见附件。"}, nil
		}
	}
	args, _ := json.Marshal(map[string]string{"problem": standaloneIMProblem, "subject": "数学", "grade": "六年级上"})
	return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "standalone-grade", Name: "k12_grade", Arguments: string(args)}}}, nil
}

type standaloneIMSolver struct{ calls atomic.Int32 }

func (*standaloneIMSolver) FreezeReportSolveTeachingContext(context.Context, string, string) (string, []skill.ContextReceipt, error) {
	return "Explain the current verified problem completely.", nil, nil
}

func (s *standaloneIMSolver) Solve(ctx context.Context, _, _, _ string) (k12usecase.SolveResult, error) {
	s.calls.Add(1)
	const solution = "甲乙合作3天完成一半，乙再做7.5天完成另一半，因此乙每天完成1/15，甲每天完成1/6−1/15=1/10。\n答案：10天"
	digest := sha256.Sum256([]byte(solution))
	result := k12usecase.SolveResult{Solution: solution, Evidence: k12usecase.SolveEvidence{Verdict: k12usecase.VerdictAgree, EvidenceType: k12usecase.EvidenceNone, SolverOutputDigest: hex.EncodeToString(digest[:])}}
	version, _ := k12usecase.SolveTeachingRequest(ctx)
	if version != k12usecase.SolveOutputWithTeachingReportV2 {
		return result, fmt.Errorf("teaching report contract was not selected")
	}
	guide := k12usecase.ParentTeachingGuide{Answer: "10天", FullSolutionSteps: []string{solution}, GradeLevelMethod: "用工作量和效率求时间。", LikelyMistakes: []string{"不要把合作时间当成甲的单独时间。"}, ParentTeachingSequence: []string{"先解释工作量，再列式。"}, FollowUpQuestions: []string{"怎样代回两个条件检查？"}, CheckingMethod: "把甲乙每天完成的工作量代回原条件。",
		ReportContent: &k12usecase.TeachingReportContentDraft{Version: k12usecase.TeachingReportContentContractVersion,
			Items:    []k12usecase.TeachingReportDraftItem{{ProblemID: "current", ShortTitle: "修公路", Answers: []k12usecase.TeachingReportDraftAnswer{{ID: "answer", Text: "10天"}}, Steps: []k12usecase.TeachingReportDraftStep{{ID: "work-rate", Text: solution}}}},
			Sections: []k12usecase.TeachingReportDraftSection{{ID: "explanation", Title: "工作量与效率", ProblemIDs: []string{"current"}, Blocks: []k12usecase.TeachingReportDraftBlock{{ID: "explanation-body", ProblemIDs: []string{"current"}, Text: strings.Repeat("合作3天相当于两队各工作3天，剩余工作量由乙队继续完成；把工作量相加后再求甲队效率。", 160)}}}},
		},
	}
	result.Generation = &k12usecase.SolveGeneration{OutputVersion: version, ParentGuideCandidate: &guide, GuideAudit: "VALID", SourceSolutionDigest: result.Evidence.SolverOutputDigest}
	return result, nil
}

type standaloneIMReportResources struct{}

func (standaloneIMReportResources) ReadResource(name, path string) (marketplace.Resource, error) {
	fs := k12.BundledSkillsFS()
	raw, err := fs.ReadFile("skill-bundles/" + name + "/bundle.json")
	if err != nil {
		return marketplace.Resource{}, err
	}
	var manifest marketplace.BundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return marketplace.Resource{}, err
	}
	content, err := fs.ReadFile("skill-bundles/" + name + "/" + path)
	if err != nil {
		return marketplace.Resource{}, err
	}
	digest := sha256.Sum256(content)
	return marketplace.Resource{SkillID: name, Version: manifest.Version, Path: path, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:]), Content: content}, nil
}

type standaloneIMRenderer struct{ printReplyAPIRenderer }

func (r *standaloneIMRenderer) RenderTeachingReport(_ context.Context, _ render.ReportBundle, input render.ReportRenderInput) ([]byte, string, error) {
	r.calls.Add(1)
	content, err := json.Marshal(input.Document)
	if err != nil || !bytes.Contains(content, []byte("10天")) || len(content) < 4000 {
		return nil, "", fmt.Errorf("complete teaching report did not reach renderer: %w", err)
	}
	return bytes.Clone(r.pdf), "application/pdf", nil
}

// 模型、PDF渲染和平台网络是可控边界；真实Skill、SQLite、出口策略、原回合及Outbox共用正式实现。
func TestDefaultK12TextReplyReturnsFrozenPDFToOriginalPrivateEvent(t *testing.T) {
	ctx := t.Context()
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "standalone-im.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	delivery, dispatcher, channels := newDelivererFixture(t)
	recorded := &recordChannel{name: "dingtalk", queryAck: channel.DeliveryAck{Status: channel.DeliveryAccepted}}
	channels.Register(recorded)
	delivery.SetInstanceResolver(func(platform, ref string) (string, error) {
		if platform != "dingtalk" || ref != "original-robot" {
			return "", fmt.Errorf("unexpected physical instance")
		}
		return ref, nil
	})
	delivery.MarkReady()
	solver := &standaloneIMSolver{}
	renderer := &standaloneIMRenderer{printReplyAPIRenderer{pdf: printReplyValidPDF()}}
	runtime, err := assembly.Wire(store.DB(), &printReplyAPITool{}, assembly.WithDeliveryTransport(delivery), assembly.WithRenderer(renderer), assembly.WithTeachingReportResources(standaloneIMReportResources{}), func(d *k12usecase.Deps) {
		d.Solver, d.TextbookOwnerID = solver, "application-owner"
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := skill.NewRegistry()
	if err := registry.Register(skilladapter.NewGradeSkill(runtime.Deps)); err != nil {
		t.Fatal(err)
	}
	model := &standaloneIMModel{}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled, cfg.FileMemory.Enabled, cfg.LLM.Cache.Enabled = false, false, false
	cfg.LLM.Default, cfg.LLM.Tools.Enabled = "fixture", "on"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"fixture": {Model: "fixture-chat"}}
	providers := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"fixture": model})
	providers.SetEgressPolicy(&egress.Policy{})
	eng := engine.NewReActEngine(cfg, providers, store, registry)
	eng.SetAgentRouter(dispatcher)
	eng.SetToolCollector(engine.NewToolCollector(registry, nil, 20))
	eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
	eng.SetReplyArtifactResolver(engineadapter.NewPrintReplyResolver(runtime.Deps))
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	message := func(chat string) *adapter.Message {
		return &adapter.Message{ID: "original-platform-message", UserID: "dingtalk-staff", SessionID: "standalone-im-session", Platform: adapter.PlatformDingtalk, InstanceID: "original-robot", ChatID: chat, Content: standaloneIMProblem, Metadata: map[string]string{"request_id": "standalone-im-request", "user_locale": "zh-CN", "conversation_type": "1"}}
	}
	ctx = adapter.WithPlatformInstanceIdentity(ctx, "original-robot", "dingtalk")
	reply, err := eng.Process(ctx, message("dingtalk-staff"))
	if err != nil || reply == nil {
		t.Fatalf("default K12 reply failed: %v", err)
	}
	if reply.Metadata["routed_agent"] != "default" || reply.Metadata["persist_error"] != "" || reply.Metadata["print_artifacts"] == "" {
		t.Fatalf("default reply failed to persist its frozen PDF: %+v", reply.Metadata)
	}
	source, err := runtime.Records.GetConversationPrintReply(ctx, "default", reply.AssistantMessageID)
	if err != nil || source.Owner != "dingtalk-staff" || source.ChatID != "dingtalk-staff" || source.InstanceID != "original-robot" || source.BindingID != "" || len(source.Artifacts) != 1 {
		t.Fatalf("original private event was not preserved: %+v %v", source, err)
	}
	if err := runtime.Outbox.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := runtime.Deps.GetConversationPrintReply(ctx, source)
	if err != nil || view.Batch == nil || len(recorded.sentParts) != 2 {
		var failure string
		_ = store.DB().QueryRow(`SELECT last_error FROM outbox_events WHERE event_id=?`, "print-reply:"+reply.AssistantMessageID).Scan(&failure)
		t.Fatalf("default reply did not reach its durable delivery batch: batch=%+v sent=%d err=%v outbox=%s", view.Batch, len(recorded.sentParts), err, failure)
	}
	for _, receipt := range view.Batch.Receipts {
		if receipt.AgentName != "default" || receipt.Target.Platform != "dingtalk" || receipt.Target.InstanceID != "original-robot" || receipt.Target.ChatID != "dingtalk-staff" || !strings.HasPrefix(receipt.BindingID, "accepted-event:") {
			t.Fatalf("delivery used a tutor binding or another target: %+v", receipt)
		}
	}
	if recorded.sentParts[1].Attachment == nil || !bytes.Equal(recorded.sentParts[1].Attachment.Data, renderer.pdf) {
		t.Fatal("delivery did not retain the frozen PDF bytes")
	}
	beforeCalls, beforeSolve, beforeRender, beforeSend := model.calls.Load(), solver.calls.Load(), renderer.calls.Load(), len(recorded.sentParts)
	replayed, err := eng.Process(ctx, message("dingtalk-staff"))
	if err != nil || replayed.AssistantMessageID != reply.AssistantMessageID || model.calls.Load() != beforeCalls || solver.calls.Load() != beforeSolve || renderer.calls.Load() != beforeRender {
		t.Fatalf("original request was regenerated: model=%d/%d solve=%d/%d render=%d/%d err=%v", model.calls.Load(), beforeCalls, solver.calls.Load(), beforeSolve, renderer.calls.Load(), beforeRender, err)
	}
	recorded.queryAck = channel.DeliveryAck{Status: channel.DeliveryOutcomeUnknown}
	unknown, err := runtime.Deps.SendConversationPrintReply(ctx, source)
	if err != nil || unknown.Status != k12.DeliveryBatchOutcomeUnknown || len(recorded.sentParts) != beforeSend {
		t.Fatalf("unknown receipt was resent: status=%s sent=%d/%d err=%v", unknown.Status, len(recorded.sentParts), beforeSend, err)
	}
	changed := message("another-chat")
	changed.Metadata["routed_agent"] = "default"
	if _, err := engineadapter.NewPrintReplyResolver(runtime.Deps)(ctx, changed, reply.Metadata); err == nil {
		t.Fatal("replay was retargeted to another chat")
	}
	changed.ID = "another-platform-message"
	changed.Metadata["request_id"] = "unbound-group-request"
	if _, err := engineadapter.NewPrintReplyResolver(runtime.Deps)(ctx, changed, reply.Metadata); err == nil || !strings.Contains(err.Error(), "original group delivery binding is unavailable") {
		t.Fatalf("missing tutor binding allowed a new non-private target: %v", err)
	}
	for _, query := range []string{"SELECT COUNT(*) FROM agents", "SELECT COUNT(*) FROM k12_mistakes", "SELECT COUNT(*) FROM k12_grading_jobs"} {
		var count int
		if err := store.DB().QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("standalone tutoring created a student or wrote learning records: %s count=%d err=%v", query, count, err)
		}
	}
}
