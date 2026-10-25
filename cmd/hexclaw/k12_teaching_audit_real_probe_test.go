package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	k12engineadapter "github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type teachingAuditProbeReceipt struct {
	Case          string `json:"case"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Expected      string `json:"expected_verdict"`
	Verdict       string `json:"verdict,omitempty"`
	Calls         int    `json:"calls"`
	ElapsedMS     int64  `json:"elapsed_ms"`
	PromptSHA256  string `json:"prompt_sha256,omitempty"`
	OutputSHA256  string `json:"output_sha256,omitempty"`
	OutputBytes   int    `json:"output_bytes,omitempty"`
	OutputSummary string `json:"output_summary,omitempty"`
	ErrorType     string `json:"error_type,omitempty"`
}

// 默认不读取模型配置或外连；启用后两份固定候选各发一次请求，未知结果不重发。
func TestK12ParentTeachingAudit_RealConfiguredModel(t *testing.T) {
	if os.Getenv("HEXCLAW_K12_TEACHING_AUDIT_PROBE") != "1" {
		t.Skip("set HEXCLAW_K12_TEACHING_AUDIT_PROBE=1 to run the two-call real-model audit probe")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "scenarios", "k12", "engineadapter", "testdata", "parent_guide_road_work_audit.json"))
	if err != nil {
		t.Fatalf("load audit fixture: error_type=%T", err)
	}
	var fixture struct {
		Subject                string                         `json:"subject"`
		Grade                  string                         `json:"grade"`
		Problem                string                         `json:"problem"`
		VerifiedSolution       string                         `json:"verified_solution"`
		FrozenTeachingContract string                         `json:"frozen_teaching_contract"`
		Guide                  k12usecase.ParentTeachingGuide `json:"guide"`
		Cases                  []struct {
			Name                 string `json:"name"`
			ReplacementStepIndex int    `json:"replacement_step_index"`
			ReplacementStep      string `json:"replacement_step"`
			ExpectedVerdict      string `json:"expected_verdict"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode audit fixture: error_type=%T", err)
	}
	if len(fixture.Cases) != 2 || fixture.Cases[0].ExpectedVerdict != "VALID" || fixture.Cases[1].ExpectedVerdict != "INVALID" {
		t.Fatal("audit fixture must contain exactly one valid and one mathematically invalid candidate")
	}
	if err := k12usecase.ValidateSingleProblemReportDraft(fixture.Guide.ReportContent); err != nil {
		t.Fatalf("invalid report fixture: error_type=%T", err)
	}
	for _, candidate := range fixture.Cases {
		if candidate.ReplacementStep != "" && (candidate.ReplacementStepIndex < 0 || candidate.ReplacementStepIndex >= len(fixture.Guide.FullSolutionSteps)) {
			t.Fatal("audit fixture replacement step is outside the candidate")
		}
	}
	cfg, err := config.Load(strings.TrimSpace(os.Getenv("HEXCLAW_K12_TEACHING_AUDIT_CONFIG")))
	if err != nil {
		t.Fatalf("load model config: error_type=%T", err)
	}
	router, err := llmrouter.New(cfg.LLM)
	if err != nil {
		t.Fatalf("build configured router: error_type=%T", err)
	}
	if router == nil {
		t.Fatal("configured LLM router is unavailable")
	}
	router.SetEgressPolicy(&egress.Policy{})
	route, err := router.DefaultRouteForCapabilities(config.LLMModelCapabilityText)
	if err != nil {
		t.Fatalf("select configured default text route: error_type=%T", err)
	}

	fixtureDigest := sha256.Sum256(raw)
	solutionDigest := sha256.Sum256([]byte(fixture.VerifiedSolution))
	sourceDigest := hex.EncodeToString(solutionDigest[:])
	report := struct {
		FixtureSHA256 string                      `json:"fixture_sha256"`
		Receipts      []teachingAuditProbeReceipt `json:"receipts"`
	}{FixtureSHA256: hex.EncodeToString(fixtureDigest[:])}
	if resultPath := strings.TrimSpace(os.Getenv("HEXCLAW_K12_TEACHING_AUDIT_RESULT")); resultPath != "" {
		// 开始调用前确认落盘位置；不覆盖已有验收证据，也不记录配置或错误正文。
		file, err := os.OpenFile(resultPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatalf("create audit evidence file: error_type=%T", err)
		}
		t.Cleanup(func() {
			encoder := json.NewEncoder(file)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(report); err != nil {
				t.Errorf("write audit evidence: error_type=%T", err)
			}
			if err := file.Close(); err != nil {
				t.Errorf("close audit evidence: error_type=%T", err)
			}
		})
	}
	for _, candidate := range fixture.Cases {
		t.Run(candidate.Name, func(t *testing.T) {
			guide := fixture.Guide
			guide.FullSolutionSteps = append([]string(nil), guide.FullSolutionSteps...)
			if candidate.ReplacementStep != "" {
				guide.FullSolutionSteps[candidate.ReplacementStepIndex] = candidate.ReplacementStep
			}
			receipt := teachingAuditProbeReceipt{Case: candidate.Name, Provider: route.ProviderName, Model: route.Model, Expected: candidate.ExpectedVerdict}
			started := time.Now()
			defer func() {
				receipt.ElapsedMS = time.Since(started).Milliseconds()
				report.Receipts = append(report.Receipts, receipt)
				t.Logf("TEACHING_AUDIT provider=%q model=%q case=%s verdict=%s expected=%s calls=%d output_sha256=%s summary=%q error_type=%s",
					receipt.Provider, receipt.Model, receipt.Case, receipt.Verdict, receipt.Expected, receipt.Calls, receipt.OutputSHA256, receipt.OutputSummary, receipt.ErrorType)
			}()
			a := k12engineadapter.NewSolveAdapter(nil)
			a.SetParentTeachingGuideAudit(func(ctx context.Context, subject, prompt, grade string) (string, error) {
				if receipt.Calls != 0 {
					return "", fmt.Errorf("audit probe refuses a repeated model request")
				}
				if subject != "" {
					prompt = "Frozen subject: " + subject + "\n" + prompt
				}
				if grade != "" {
					prompt += "\nUse only the supplied methods allowed for " + grade + "."
				}
				cctx := k12ParentTeachingGuideRequestContext(ctx)
				temp := 0.2
				promptDigest := sha256.Sum256([]byte(prompt))
				receipt.PromptSHA256 = hex.EncodeToString(promptDigest[:])
				receipt.Calls++
				resp, err := route.Provider.Complete(k12NonIdempotentLLMContext(cctx), llm.CompletionRequest{
					Model: route.Model,
					Messages: []llm.Message{
						{Role: llm.RoleSystem, Content: "Independently audit the supplied parent tutoring guide against its frozen verified solution and curriculum. Check every field required by the supplied task contract, including report_content when required, and all mathematical examples, explanations and checks. Do not rewrite or generate a solution or guide. Output exactly one PARENT_GUIDE_AUDITS line containing the requested JSON array; incorrect, missing or out-of-scope content must not pass."},
						{Role: llm.RoleUser, Content: prompt},
					},
					Temperature: &temp,
				})
				if err != nil {
					return "", err
				}
				outputDigest := sha256.Sum256([]byte(resp.Content))
				receipt.OutputSHA256 = hex.EncodeToString(outputDigest[:])
				receipt.OutputBytes = len(resp.Content)
				receipt.OutputSummary = teachingAuditProbeSummary(resp.Content)
				return resp.Content, nil
			})
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			audit, err := a.AuditParentTeachingGuide(ctx, k12usecase.ParentTeachingGuideAuditRequest{
				Request: k12usecase.ParentTeachingGuideRequest{
					Subject: fixture.Subject, Grade: fixture.Grade, Problem: fixture.Problem,
					VerifiedSolution: fixture.VerifiedSolution, FrozenTeachingContract: fixture.FrozenTeachingContract,
					ReportRequired: true,
				},
				Guide: guide, SourceSolutionDigest: sourceDigest,
			})
			if err != nil {
				receipt.ErrorType = fmt.Sprintf("%T", err)
				t.Fatalf("configured audit request failed: error_type=%s", receipt.ErrorType)
			}
			receipt.Verdict = audit.Verdict
			if receipt.Calls != 1 || audit.SourceSolutionDigest != sourceDigest || audit.Verdict != candidate.ExpectedVerdict {
				t.Fatalf("audit contract mismatch: calls=%d verdict=%s expected=%s", receipt.Calls, audit.Verdict, candidate.ExpectedVerdict)
			}
		})
	}
}

// 摘要只保留审核字段结论与拒绝原因，不打印整段模型输出。
func teachingAuditProbeSummary(output string) string {
	for _, line := range strings.Split(output, "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "PARENT_GUIDE_AUDITS:")
		if !ok {
			continue
		}
		var audits []struct {
			Scope  string `json:"scope"`
			Fields []struct {
				Field  string `json:"field"`
				Valid  *bool  `json:"valid"`
				Reason string `json:"reason"`
			} `json:"fields"`
		}
		if json.Unmarshal([]byte(payload), &audits) != nil || len(audits) != 1 {
			return "audit response is not a single structured candidate"
		}
		parts := []string{"scope=" + audits[0].Scope}
		for _, field := range audits[0].Fields {
			status := "missing"
			if field.Valid != nil {
				status = fmt.Sprint(*field.Valid)
			}
			part := field.Field + "=" + status
			if field.Valid != nil && !*field.Valid {
				reason := []rune(strings.Join(strings.Fields(field.Reason), " "))
				if len(reason) > 240 {
					reason = reason[:240]
				}
				part += ": " + string(reason)
			}
			parts = append(parts, part)
		}
		return strings.Join(parts, "; ")
	}
	return "audit response line is missing"
}
