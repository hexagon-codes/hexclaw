package usecase_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/llm/compatible"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/curriculum"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const accumulationValidRaw = `{"subject":"英语","entry_type":"词汇积累","source":""}`

func accumulationHTTPDeriver(t *testing.T, url string, route *string, afterRaw ...func() error) *engineadapter.AccumulationMetadataAdapter {
	t.Helper()
	provider := compatible.New("accumulation-controlled", "test-key", compatible.WithBaseURL(url))
	return engineadapter.NewAccumulationMetadataAdapter(func(ctx context.Context, content string) (string, error) {
		snapshot, raw, frozen, err := usecase.AccumulationMetadataFrozenRequestFromContext(ctx)
		if err != nil {
			return "", err
		}
		request := llm.CompletionRequest{Model: *route, Messages: []llm.Message{{Role: llm.RoleUser, Content: content}}, MaxTokens: 128}
		if frozen {
			if err := json.Unmarshal([]byte(raw), &request); err != nil {
				return "", err
			}
		} else {
			snapshot = k12.GradingModelSnapshot{Provider: "accumulation-controlled", Model: *route, Route: "accumulation-controlled/" + *route, Capability: "text"}
			raw, err := json.Marshal(request)
			if err != nil {
				return "", err
			}
			if err := usecase.FreezeAccumulationMetadataRequest(ctx, snapshot, string(raw)); err != nil {
				return "", err
			}
		}
		sendCtx := llm.WithBeforeSendHookForAction(llm.WithOperationSafety(ctx, llm.OperationSafetyNonIdempotent), "complete", usecase.BeforeAccumulationMetadataSend)
		response, err := provider.Complete(sendCtx, request)
		if err != nil {
			return "", err
		}
		if err := usecase.CaptureAccumulationMetadataRaw(ctx, response.Content); err != nil {
			return "", err
		}
		if len(afterRaw) > 0 {
			if err := afterRaw[0](); err != nil {
				return "", err
			}
		}
		return response.Content, nil
	}).WithBeforeSendHook()
}

func TestAccumulationMetadataOrdinaryAdapterUnknownHTTPDoesNotResend(t *testing.T) {
	d := newDataDeps(t)
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1); <-release }))
	t.Cleanup(server.Close)
	d.AccumulationMetadata = engineadapter.NewAccumulationMetadataAdapter(func(ctx context.Context, _ string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
		if err != nil {
			return "", err
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		return accumulationValidRaw, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, _, firstErr := d.CreateCurrentAccumulation(ctx, "xiaoming", "apple", "ordinary-adapter")
	cancel()
	_, _, replayErr := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "ordinary-adapter")
	if !errors.Is(firstErr, usecase.ErrModelInvocationRequiresReconciliation) || !errors.Is(replayErr, usecase.ErrModelInvocationRequiresReconciliation) || calls.Load() != 1 {
		t.Fatalf("ordinary callback retried unknown HTTP: calls=%d first=%v replay=%v", calls.Load(), firstErr, replayErr)
	}
}

func TestAccumulationMetadataStoredRawResumesPureDecodeWithoutProvider(t *testing.T) {
	d := newDataDeps(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		accumulationCompletion(w, accumulationValidRaw)
	}))
	defer server.Close()
	route := "first-model"
	d.AccumulationMetadata = accumulationHTTPDeriver(t, server.URL, &route, func() error { return errors.New("controlled interruption after raw persistence") })
	if _, _, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "raw-resume"); err == nil {
		t.Fatal("missing interruption")
	}
	command, err := d.Records.GetAccumulationMetadataCommand(t.Context(), "xiaoming", "raw-resume")
	if err != nil || command.State != "raw_received" || command.RawResponse != accumulationValidRaw {
		t.Fatalf("raw receipt=%+v err=%v", command, err)
	}
	reopenAccumulationStore(t, &d)
	d.AccumulationMetadata = engineadapter.NewAccumulationMetadataAdapter(nil)
	if _, created, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "raw-resume"); err != nil || !created || calls.Load() != 1 {
		t.Fatalf("pure decode: created=%v calls=%d err=%v", created, calls.Load(), err)
	}
}

func TestAccumulationMetadataExplicitSDKHookKeepsDefinitelyUnsentAttemptRetryable(t *testing.T) {
	d := newDataDeps(t)
	calls := 0
	d.AccumulationMetadata = engineadapter.NewAccumulationMetadataAdapter(func(ctx context.Context, _ string) (string, error) {
		calls++
		if calls == 1 {
			return "", egress.ErrProviderNotSent
		}
		if err := usecase.BeforeAccumulationMetadataSend(ctx); err != nil {
			return "", err
		}
		return accumulationValidRaw, nil
	}).WithBeforeSendHook()
	if _, _, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "sdk-not-sent"); err == nil || errors.Is(err, usecase.ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("not-sent err=%v", err)
	}
	command, err := d.Records.GetAccumulationMetadataCommand(t.Context(), "xiaoming", "sdk-not-sent")
	if err != nil || command.State != "failed" || command.FailureKind != "provider_not_sent" {
		t.Fatalf("unsent=%+v err=%v", command, err)
	}
	if _, created, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "sdk-not-sent"); err != nil || !created || calls != 2 {
		t.Fatalf("retry created=%v calls=%d err=%v", created, calls, err)
	}
}

func accumulationCompletion(w http.ResponseWriter, raw string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "controlled-completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": raw}, "finish_reason": "stop"}}})
}

func reopenAccumulationStore(t *testing.T, d *usecase.Deps) {
	t.Helper()
	var seq int
	var name, path string
	if err := d.Records.DB().QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := d.Records.DB().Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	registry := scenario.NewRegistry()
	if err := registry.Assemble(k12.Pack(curriculum.New())); err != nil {
		t.Fatal(err)
	}
	d.Records = k12storage.NewStore(db, registry.Records)
}

func TestAccumulationMetadataUnknownPersistsAcrossSQLiteReopenAndNeverResends(t *testing.T) {
	d := newDataDeps(t)
	var calls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-release }))
	defer server.CloseClientConnections()
	// 取消客户端后再释放受控 HTTP；避免测试服务器把未知请求误当成成功回执。
	t.Cleanup(server.Close)
	route := "frozen-model"
	d.AccumulationMetadata = accumulationHTTPDeriver(t, server.URL, &route)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, _, err := d.CreateCurrentAccumulation(ctx, "xiaoming", "apple", "unknown-command")
	cancel()
	if !errors.Is(err, usecase.ErrModelInvocationRequiresReconciliation) || calls.Load() != 1 {
		t.Fatalf("unknown: calls=%d err=%v", calls.Load(), err)
	}
	command, err := d.Records.GetAccumulationMetadataCommand(t.Context(), "xiaoming", "unknown-command")
	if err != nil {
		t.Fatal(err)
	}
	if command.State != "outcome_unknown" || command.RouteSnapshotJSON == "" || command.RequestPolicyJSON == "" {
		t.Fatalf("command=%+v", command)
	}
	reopenAccumulationStore(t, &d)
	if _, _, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "unknown-command"); !errors.Is(err, usecase.ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("reopen err=%v", err)
	}
	if _, _, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "pear", "unknown-command"); !errors.Is(err, k12storage.ErrCurrentCommandConflict) {
		t.Fatalf("changed content err=%v", err)
	}
	var roots int
	if err := d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_accumulations`).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || roots != 0 {
		t.Fatalf("calls=%d roots=%d", calls.Load(), roots)
	}
}

func TestAccumulationMetadataKnownResultSurvivesRootFailureAndConfigurationDrift(t *testing.T) {
	d := newDataDeps(t)
	if _, err := d.Records.DB().Exec(`UPDATE agents SET metadata='{"k12.grade_term":"六年级上"}' WHERE name='xiaoming'`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		accumulationCompletion(w, accumulationValidRaw)
	}))
	defer server.Close()
	route := "first-model"
	d.AccumulationMetadata = accumulationHTTPDeriver(t, server.URL, &route)
	if _, err := d.Records.DB().Exec(`CREATE TRIGGER reject_accumulation_root BEFORE INSERT ON k12_accumulations BEGIN SELECT RAISE(ABORT,'controlled root failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "root-failure"); err == nil {
		t.Fatal("root failure missing")
	}
	command, err := d.Records.GetAccumulationMetadataCommand(t.Context(), "xiaoming", "root-failure")
	if err != nil {
		t.Fatal(err)
	}
	if command.State != "metadata_ready" || command.RawResponse != accumulationValidRaw || command.MetadataJSON == "" {
		t.Fatalf("known result lost: %+v", command)
	}
	reopenAccumulationStore(t, &d)
	if _, err := d.Records.DB().Exec(`DROP TRIGGER reject_accumulation_root`); err != nil {
		t.Fatal(err)
	}
	route = "changed-model"
	d.AccumulationMetadata = nil
	if _, err := d.Records.DB().Exec(`UPDATE agents SET metadata='{"k12.grade_term":"五年级上"}' WHERE name='xiaoming'`); err != nil {
		t.Fatal(err)
	}
	id, created, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "root-failure")
	if err != nil || !created || id == "" || calls.Load() != 1 {
		t.Fatalf("commit-only resume: id=%s created=%v calls=%d err=%v", id, created, calls.Load(), err)
	}
	item, err := d.GetAccumulation(t.Context(), "xiaoming", id)
	if err != nil || item.Fields.GradeTerm != "六年级上" {
		t.Fatalf("frozen grade=%+v err=%v", item, err)
	}
	replay, created, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "root-failure")
	if err != nil || created || replay != id {
		t.Fatalf("old final receipt: id=%s created=%v err=%v", replay, created, err)
	}
}

func TestAccumulationMetadataInvalidRawRetriesOnlyOnExplicitCommandAndPreservesReceipt(t *testing.T) {
	for _, invalidRaw := range []string{`{"subject":"英语","entry_type":"grammar","source":""}`, `{malformed`} {
		t.Run(invalidRaw, func(t *testing.T) {
			d := newDataDeps(t)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					accumulationCompletion(w, invalidRaw)
				} else {
					accumulationCompletion(w, accumulationValidRaw)
				}
			}))
			defer server.Close()
			route := "first-model"
			d.AccumulationMetadata = accumulationHTTPDeriver(t, server.URL, &route)
			if _, _, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "invalid-response"); err == nil || errors.Is(err, usecase.ErrModelInvocationRequiresReconciliation) {
				t.Fatalf("known parse error=%v", err)
			}
			first, err := d.Records.GetAccumulationMetadataCommand(t.Context(), "xiaoming", "invalid-response")
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || first.State != "failed" || first.RawResponse != invalidRaw || first.RawDigest == "" || first.FailureKind != "invalid_response" {
				t.Fatalf("calls=%d command=%+v", calls.Load(), first)
			}
			reopenAccumulationStore(t, &d)
			if _, created, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "invalid-response"); err != nil || !created {
				t.Fatalf("explicit retry created=%v err=%v", created, err)
			}
			next, err := d.Records.GetAccumulationMetadataCommand(t.Context(), "xiaoming", "invalid-response")
			if err != nil {
				t.Fatal(err)
			}
			var receipts []struct {
				RawResponse string `json:"raw_response"`
				RawDigest   string `json:"raw_digest"`
				FailureKind string `json:"failure_kind"`
			}
			if err := json.Unmarshal([]byte(next.ReceiptsJSON), &receipts); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 || next.Attempt != 2 || len(receipts) != 1 || receipts[0].RawResponse != invalidRaw || receipts[0].RawDigest != first.RawDigest || receipts[0].FailureKind != "invalid_response" {
				t.Fatalf("calls=%d command=%+v receipts=%+v", calls.Load(), next, receipts)
			}
		})
	}
}

func TestAccumulationMetadataDefiniteHTTPFailureRetriesFrozenRequestOnlyOnExplicitCommand(t *testing.T) {
	d := newDataDeps(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "first-model" {
			t.Errorf("request drifted to model %q", body.Model)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"controlled known failure","type":"invalid_request_error"}}`))
			return
		}
		accumulationCompletion(w, accumulationValidRaw)
	}))
	defer server.Close()
	route := "first-model"
	d.AccumulationMetadata = accumulationHTTPDeriver(t, server.URL, &route)
	if _, _, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "http-failure"); err == nil || errors.Is(err, usecase.ErrModelInvocationRequiresReconciliation) {
		t.Fatalf("definite error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("hidden retry calls=%d", calls.Load())
	}
	route = "changed-model"
	if _, created, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "http-failure"); err != nil || !created {
		t.Fatalf("explicit retry created=%v err=%v", created, err)
	}
	command, err := d.Records.GetAccumulationMetadataCommand(t.Context(), "xiaoming", "http-failure")
	if err != nil {
		t.Fatal(err)
	}
	var receipts []json.RawMessage
	if err := json.Unmarshal([]byte(command.ReceiptsJSON), &receipts); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || command.Attempt != 2 || len(receipts) != 1 {
		t.Fatalf("calls=%d command=%+v receipts=%d", calls.Load(), command, len(receipts))
	}
}

type accumulationNotSentDeriver struct{ calls int }

func (d *accumulationNotSentDeriver) DeriveAccumulationMetadata(context.Context, string) (k12.AccumulationDerivedMetadata, error) {
	d.calls++
	if d.calls == 1 {
		return k12.AccumulationDerivedMetadata{}, egress.ErrProviderNotSent
	}
	return validAccumulationMetadata(), nil
}

func TestAccumulationMetadataKnownNotSentAndExpiredPreparedRemainRetryableWithFence(t *testing.T) {
	d := newDataDeps(t)
	deriver := &accumulationNotSentDeriver{}
	d.AccumulationMetadata = deriver
	if _, _, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "not-sent"); err == nil {
		t.Fatal("missing failure")
	}
	if _, created, err := d.CreateCurrentAccumulation(t.Context(), "xiaoming", "apple", "not-sent"); err != nil || !created || deriver.calls != 2 {
		t.Fatalf("retry created=%v calls=%d err=%v", created, deriver.calls, err)
	}
	// 模拟旧执行在发送前失去租约，新执行接管后旧执行不能跨过物理发送边界。
	digest := "sha256:" + fmt.Sprintf("%064d", 1)
	prepared, err := d.Records.PrepareAccumulationMetadataCommand(t.Context(), k12storage.AccumulationMetadataCommand{AgentName: "xiaoming", CommandKey: "fence", RequestDigest: digest, PolicyVersion: "test", CreatedAt: 1000})
	if err != nil {
		t.Fatal(err)
	}
	old, claimed, err := d.Records.ClaimAccumulationMetadataCommand(t.Context(), prepared, "old", 1000, 2000)
	if err != nil || !claimed {
		t.Fatalf("old claim=%v %v", claimed, err)
	}
	if _, claimed, err := d.Records.ClaimAccumulationMetadataCommand(t.Context(), old, "too-early", 1500, 2500); err != nil || claimed {
		t.Fatalf("live claim=%v %v", claimed, err)
	}
	next, claimed, err := d.Records.ClaimAccumulationMetadataCommand(t.Context(), old, "next", 2001, 3001)
	if err != nil || !claimed {
		t.Fatalf("new claim=%v %v", claimed, err)
	}
	if err := d.Records.MarkAccumulationMetadataSent(t.Context(), old, 2002); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("stale sender err=%v", err)
	}
	if err := d.Records.MarkAccumulationMetadataSent(t.Context(), next, 2002); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := d.Records.ClaimAccumulationMetadataCommand(t.Context(), next, "after-sent", 9000, 10000); err != nil || claimed {
		t.Fatalf("sent claim=%v %v", claimed, err)
	}
	if _, err := d.Records.DB().Exec(`DELETE FROM agents WHERE name='xiaoming'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Records.GetAccumulationMetadataCommand(t.Context(), "xiaoming", "fence"); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("cascade err=%v", err)
	}
}
