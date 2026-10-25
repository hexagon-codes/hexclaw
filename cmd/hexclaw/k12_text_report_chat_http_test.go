package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	hexapi "github.com/hexagon-codes/hexclaw/api"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/apihttp"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

// 仅控制工具选择和最终短话术；原题由真实本地数学链求解，正文由正式resolver读回。
type textReportChatModel struct {
	printReplyAPIModel
	studentAnswer string
}

func (m *textReportChatModel) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	m.calls.Add(1)
	for _, message := range req.Messages {
		if message.Role == llm.RoleTool {
			return &llm.CompletionResponse{Content: "已算好，答案见附件。"}, nil
		}
	}
	args := map[string]string{"problem": "1+1=", "subject": "数学", "grade": "六年级上"}
	if m.studentAnswer != "" {
		args["student_answer"] = m.studentAnswer
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "current-math", Name: "k12_grade", Arguments: string(raw)}}}, nil
}

func (m *textReportChatModel) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	response, err := m.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	delta := map[string]any{"content": response.Content}
	finish := "stop"
	if len(response.ToolCalls) > 0 {
		call := response.ToolCalls[0]
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments}}}}
		finish = "tool_calls"
	}
	frame, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(frame)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

type textReportChatRenderer struct {
	printReplyAPIRenderer
	fail bool
}

func (r *textReportChatRenderer) RenderTeachingReport(_ context.Context, _ render.ReportBundle, input render.ReportRenderInput) ([]byte, string, error) {
	r.calls.Add(1)
	if len(input.Document) == 0 {
		return nil, "", fmt.Errorf("report document is missing")
	}
	if r.fail {
		return nil, "", fmt.Errorf("fixture PDF compilation failed")
	}
	return bytes.Clone(r.pdf), "application/pdf", nil
}

func TestK12TextReportChatHTTPKeepsFrozenBodyAfterModelSummary(t *testing.T) {
	for _, tc := range []struct {
		failPDF bool
		answer  string
	}{{false, ""}, {true, ""}, {false, "3"}, {true, "3"}} {
		failPDF := tc.failPDF
		name := "pdf_ready"
		if failPDF {
			name = "pdf_failed"
		}
		if tc.answer != "" {
			name = "graded_" + name
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "text-report-chat.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			const sessionID, requestID = "text-report-chat-session", "text-report-chat-request"
			if err := store.CreateSession(ctx, &storage.Session{ID: sessionID, UserID: "api-user", Platform: "api", Title: "当前解题报告"}); err != nil {
				t.Fatal(err)
			}
			mathProviderCalls := 0
			mathSolver := engine.NewSolveSkill(func(context.Context, engine.SubAgentSpec) (engine.SubAgentResult, error) {
				mathProviderCalls++
				return engine.SubAgentResult{}, fmt.Errorf("unexpected mathematical model call")
			}, nil)
			renderer := &textReportChatRenderer{printReplyAPIRenderer: printReplyAPIRenderer{pdf: printReplyValidPDF()}, fail: failPDF}
			runtime, err := assembly.Wire(store.DB(), mathSolver, assembly.WithRenderer(renderer), assembly.WithTeachingReportResources(standaloneIMReportResources{}))
			if err != nil {
				t.Fatal(err)
			}
			registry := skill.NewRegistry()
			if err := registry.Register(skilladapter.NewGradeSkill(runtime.Deps)); err != nil {
				t.Fatal(err)
			}
			model := &textReportChatModel{studentAnswer: tc.answer}
			cfg := config.DefaultConfig()
			cfg.Compaction.Enabled, cfg.FileMemory.Enabled, cfg.LLM.Cache.Enabled = false, false, false
			cfg.LLM.Default, cfg.LLM.Tools.Enabled = "fixture", "on"
			cfg.LLM.Providers = map[string]config.LLMProviderConfig{"fixture": {Model: "fixture-chat"}}
			cfg.Server.APIToken = "text-report-chat-fixture"
			eng := engine.NewReActEngine(cfg, llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"fixture": model}), store, registry)
			eng.SetAgentRouter(router.New())
			eng.SetToolCollector(engine.NewToolCollector(registry, nil, 20))
			eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
			eng.SetReplyArtifactResolver(engineadapter.NewPrintReplyResolver(runtime.Deps))
			if err := eng.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Stop(context.Background()) })
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Server.Host, cfg.Server.Port = "127.0.0.1", listener.Addr().(*net.TCPAddr).Port
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			server := hexapi.NewServer(cfg, eng, nil, store)
			server.Mount("/api/k12", apihttp.NewHandler(apihttp.Runtime{Views: runtime.Registry.Views, Records: runtime.Records, Deps: runtime.Deps, PrincipalMode: "remote", AuthenticatedOwnerScope: func(ctx context.Context) (string, error) {
				if skill.AuthenticatedUserID(ctx) != "api-user" {
					return "", fmt.Errorf("authenticated principal required")
				}
				return k12usecase.DefaultLocalOwnerScope, nil
			}}))
			ready, done := make(chan struct{}), make(chan error, 1)
			go func() { done <- server.Start(ctx, func() { close(ready) }) }()
			select {
			case <-ready:
			case err := <-done:
				t.Fatalf("start HTTP: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			t.Cleanup(func() {
				stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := server.Stop(stop); err != nil {
					t.Error(err)
				}
			})
			base := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
			call := func(method, path string, body any) (int, []byte) {
				t.Helper()
				var raw []byte
				if body != nil {
					var err error
					raw, err = json.Marshal(body)
					if err != nil {
						t.Fatal(err)
					}
				}
				req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
				req.Header.Set("Content-Type", "application/json")
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				data, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				return res.StatusCode, data
			}
			body := map[string]string{"request_id": requestID, "session_id": sessionID, "message": "请解答1+1，给出完整快速解答、深度讲解和PDF。"}
			if tc.answer != "" {
				body["message"] = "请批改1+1，我的作答是3，给出完整快速解答、深度讲解和PDF。"
			}
			status, raw := call(http.MethodPost, "/api/v1/chat", body)
			if status != http.StatusOK {
				t.Fatalf("normal chat failed: %d %s", status, raw)
			}
			var reply hexapi.ChatResponse
			if err := json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			saved, err := store.GetMessage(ctx, requestID+":assistant")
			if err != nil {
				t.Fatal(err)
			}
			historical, err := runtime.Records.GetConversationReportMessage(ctx, "api-user", "default", sessionID, saved.ID, failPDF)
			if err != nil || historical.ID != saved.ID {
				t.Fatalf("the original default conversation cannot find its report for prepare/resend: %v metadata=%s", err, saved.Metadata)
			}
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal([]byte(saved.Metadata), &metadata); err != nil {
				t.Fatal(err)
			}
			metadataString := func(key string) string {
				var value string
				if raw := metadata[key]; len(raw) > 0 {
					if err := json.Unmarshal(raw, &value); err != nil {
						t.Fatal(err)
					}
				}
				return value
			}
			var refs []struct {
				RunID          string                        `json:"solve_run_id"`
				ID             string                        `json:"finalization_id"`
				Digest         string                        `json:"finalization_digest"`
				PDFStatus      string                        `json:"pdf_status"`
				GradeReportRef *k12.TextGradeReportReference `json:"grade_report_ref"`
			}
			if err := json.Unmarshal([]byte(metadataString("text_solve_refs")), &refs); err != nil || len(refs) != 1 {
				t.Fatalf("missing actual text finalization reference: %v %v", refs, err)
			}
			final, err := runtime.Records.GetTextSolveFinalization(ctx, k12usecase.DefaultLocalOwnerScope, "default", refs[0].RunID)
			if err != nil || final.FinalizationID != refs[0].ID || final.ResultDigest != refs[0].Digest || final.SourceSessionID != sessionID {
				t.Fatalf("text finalization lost original source: %+v %v", final, err)
			}
			var canonical string
			if tc.answer == "" {
				if refs[0].GradeReportRef != nil {
					t.Fatal("unscored report acquired a grading reference")
				}
				canonical, err = runtime.Deps.TextSolveTeachingReportContent(ctx, final.OwnerID, "default", final.RunID, final.FinalizationID, final.ResultDigest)
			} else {
				if refs[0].GradeReportRef == nil {
					t.Fatal("graded conversation lost its exact assessment reference")
				}
				inv, invErr := runtime.Records.ValidateTextGradeReportReference(ctx, final, *refs[0].GradeReportRef)
				if invErr != nil || inv.Status != k12.ModelInvocationSucceeded {
					t.Fatalf("graded conversation refers to a missing actual assessment: %v", invErr)
				}
				var assessment struct {
					Result k12usecase.GradeResult `json:"result"`
				}
				if err := json.Unmarshal([]byte(inv.ResultJSON), &assessment); err != nil || assessment.Result.Outcome.AssessmentStatus() != k12.GradingAssessmentWrong {
					t.Fatalf("graded conversation did not preserve the actual wrong verdict: %v", err)
				}
				canonical, err = runtime.Deps.TextSolveTeachingReportContentWithGrade(ctx, final.OwnerID, "default", final.RunID, final.FinalizationID, final.ResultDigest, *refs[0].GradeReportRef)
				if !strings.Contains(canonical, "原始作答：3") {
					t.Fatalf("actual original answer or wrong verdict missing: %q", canonical)
				}
			}
			if err != nil || len([]rune(canonical)) >= 1000 {
				t.Fatalf("short actual report unavailable: %v", err)
			}
			for label, value := range map[string]string{"HTTP": reply.Reply, "stored": saved.Content} {
				if strings.Count(value, canonical) != 1 || strings.Count(value, "一、快速解答") != 1 || strings.Count(value, "二、深度讲解") != 1 {
					t.Fatalf("%s body was summarized or duplicated: %q", label, value)
				}
			}
			if reply.MessageContent == nil || reply.MessageContent.Markdown != reply.Reply {
				t.Fatal("HTTP canonical body differs from the original assistant body")
			}
			var pdfRefs []k12.PrintReplyArtifact
			if encoded := metadataString("print_artifacts"); encoded != "" {
				if err := json.Unmarshal([]byte(encoded), &pdfRefs); err != nil {
					t.Fatal(err)
				}
			}
			if failPDF {
				if len(pdfRefs) != 0 || refs[0].PDFStatus != "failed" {
					t.Fatalf("failed PDF was advertised as an attachment: %v %+v", pdfRefs, refs)
				}
			} else {
				if len(pdfRefs) != 1 || len(reply.MessageContent.Attachments) != 1 {
					t.Fatalf("expected exactly one frozen PDF: refs=%v content=%+v", pdfRefs, reply.MessageContent)
				}
				status, downloaded := call(http.MethodGet, "/api/k12/print-artifacts/"+pdfRefs[0].ArtifactID+"/content?agent=default", nil)
				if status != http.StatusOK || !bytes.Equal(downloaded, renderer.pdf) {
					t.Fatalf("history download changed PDF bytes: %d", status)
				}
			}
			status, history := call(http.MethodGet, "/api/v1/sessions/"+sessionID+"/messages", nil)
			if status != http.StatusOK || !bytes.Contains(history, []byte("快速解答")) || !bytes.Contains(history, []byte("深度讲解")) {
				t.Fatalf("normal history lost full report: %d", status)
			}
			beforeModel, beforeRender := model.calls.Load(), renderer.calls.Load()
			var beforeInvocations int
			if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_text_solve_invocations`).Scan(&beforeInvocations); err != nil {
				t.Fatal(err)
			}
			status, replay := call(http.MethodPost, "/api/v1/chat", body)
			var afterInvocations int
			if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_text_solve_invocations`).Scan(&afterInvocations); err != nil {
				t.Fatal(err)
			}
			if status != http.StatusOK || model.calls.Load() != beforeModel || renderer.calls.Load() != beforeRender || beforeInvocations != afterInvocations || mathProviderCalls != 0 {
				t.Fatalf("same request solved/rendered again: status=%d body=%s model=%d/%d render=%d/%d invocations=%d/%d", status, replay, model.calls.Load(), beforeModel, renderer.calls.Load(), beforeRender, afterInvocations, beforeInvocations)
			}
		})
	}
}
