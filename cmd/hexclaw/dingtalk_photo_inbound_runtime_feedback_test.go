package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/records"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	_ "modernc.org/sqlite"
)

type statusNoticeTransport struct {
	prepared       int
	sends, queries []k12.DeliveryReceipt
	status         k12.DeliveryReceiptStatus
	prepareErr     error
}

func TestDingTalkPhotoFinalReplyRecallsOnlyItsAcceptedNotice(t *testing.T) {
	for _, scenario := range []string{"delivered", "still_sending", "recall_failure_restart", "wrong_notice_target"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			bundle := inboundPhotoBundleFixture([]byte("source-photo"))
			bundle.Dispatch.ProcessingStatus = k12usecase.InboundPhotoFinalArtifactReady
			bundle.Dispatch.ReplyStatus = k12usecase.InboundPhotoReplyBound
			bundle.Dispatch.DeliveryBatchID = "final-batch"
			target := inboundPhotoFrozenTarget(bundle)
			part := func(id, kind, mime string, ordinal int) k12.DeliveryReceipt {
				return k12.DeliveryReceipt{DeliveryID: id, BindingID: target.BindingID, Target: target.Target,
					PartKind: messagecontent.PartKind(kind), PartMIME: mime, PartOrdinal: ordinal,
					Status: k12.DeliveryDelivered, ExternalMessageID: id + "-provider"}
			}
			final := k12.DeliveryBatch{BatchID: "final-batch", Status: k12.DeliveryBatchDelivered,
				Receipts: []k12.DeliveryReceipt{part("final-text", "markdown", "", 1), part("final-image", "artifact", "image/png", 2)}}
			objectID := bundle.Receipt.ReceiptID + ":accepted"
			notice := k12.DeliveryBatch{BatchID: "notice-batch", AgentName: bundle.Receipt.AgentName,
				ObjectKind: k12DingtalkPhotoStatusNoticeKind, ObjectID: objectID,
				Receipts: []k12.DeliveryReceipt{part("accepted", "markdown", "", 1)}}
			if scenario == "still_sending" {
				final.Status = k12.DeliveryBatchOutcomeUnknown
				final.Receipts[1].Status = k12.DeliveryOutcomeUnknown
			}
			if scenario == "wrong_notice_target" {
				notice.Receipts[0].Target.InstanceID = "another-bot"
			}
			batches := &finalReplyBatchFake{existing: final, statusNotices: map[string]k12.DeliveryBatch{
				bundle.Receipt.AgentName + "/" + k12DingtalkPhotoStatusNoticeKind + "/" + objectID: notice,
			}}
			inbound := &inboundPhotoCoordinatorFake{bundle: bundle}
			recalls := 0
			failure := errors.New("provider recall failed")
			config := k12DingtalkPhotoInboundRuntimeConfig{Inbound: inbound, ReplyBatches: batches,
				RecallStatusNotice: func(ctx context.Context, instance, external string) error {
					recalls++
					if ctx.Err() != nil || instance != "family-bot" || external != "accepted-provider" {
						t.Fatalf("recall lost original notice identity: %s/%s, %v", instance, external, ctx.Err())
					}
					if scenario == "recall_failure_restart" && recalls == 1 {
						return failure
					}
					return nil
				}}
			r := newK12DingtalkPhotoInboundRuntime(config)
			done, err := r.completeBoundReply(ctx, bundle)
			switch scenario {
			case "delivered":
				if err != nil || !done || !inbound.completed || recalls != 1 {
					t.Fatalf("completed reply did not clean its notice: done=%v recall=%d err=%v", done, recalls, err)
				}
			case "still_sending":
				if err != nil || done || inbound.completed || recalls != 0 {
					t.Fatal("unfinished reply removed progress or completed the task")
				}
			case "wrong_notice_target":
				if err == nil || done || recalls != 0 || inbound.completed {
					t.Fatal("notice target drift allowed recall")
				}
			case "recall_failure_restart":
				if !errors.Is(err, failure) || done || inbound.completed {
					t.Fatalf("failed cleanup lost pending state: %v", err)
				}
				restarted := newK12DingtalkPhotoInboundRuntime(config)
				done, err = restarted.completeBoundReply(ctx, bundle)
				if err != nil || !done || !inbound.completed || recalls != 2 {
					t.Fatalf("restart did not resume original cleanup: %v", err)
				}
			}
			if batches.prepareCalls != 0 || batches.existing.BatchID != "final-batch" {
				t.Fatal("cleanup prepared or replaced the already delivered final reply")
			}
		})
	}
}

func (p *statusNoticeTransport) PrepareText(context.Context, string, string) (k12usecase.PreparedTextDelivery, error) {
	return k12usecase.PreparedTextDelivery{}, errors.New("unexpected mutable target resolution")
}
func (p *statusNoticeTransport) ResolveTextTargets(context.Context, string) ([]k12usecase.ResolvedDeliveryTarget, error) {
	return nil, errors.New("unexpected mutable target resolution")
}
func (p *statusNoticeTransport) PrepareTextForTargets(_ context.Context, content string, targets []k12usecase.ResolvedDeliveryTarget) ([]k12usecase.PreparedTextDelivery, error) {
	p.prepared++
	if p.prepareErr != nil {
		return nil, p.prepareErr
	}
	var out []k12usecase.PreparedTextDelivery
	for _, target := range targets {
		payload, _ := json.Marshal(map[string]string{"content": content})
		out = append(out, k12usecase.PreparedTextDelivery{BindingID: target.BindingID, Target: target.Target, PayloadJSON: string(payload), RenderJSON: `{"surface":"channel"}`})
	}
	return out, nil
}
func (p *statusNoticeTransport) SendPrepared(_ context.Context, receipt k12.DeliveryReceipt) (k12usecase.DeliveryTransportAck, error) {
	p.sends = append(p.sends, receipt)
	status := p.status
	if status == "" {
		status = k12.DeliveryDelivered
	}
	return k12usecase.DeliveryTransportAck{Status: status, ExternalMessageID: "notice-provider-id"}, nil
}
func (p *statusNoticeTransport) QueryPrepared(_ context.Context, receipt k12.DeliveryReceipt) (k12usecase.DeliveryTransportAck, error) {
	p.queries = append(p.queries, receipt)
	return k12usecase.DeliveryTransportAck{Status: k12.DeliveryDelivered, ExternalMessageID: receipt.ExternalMessageID}, nil
}

func statusNoticeDeps(t *testing.T) (k12usecase.Deps, *statusNoticeTransport) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "notices.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migrate.Run(context.Background(), db, migrate.All); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agents(name,display_name) VALUES('child-tutor','Child Tutor')`); err != nil {
		t.Fatal(err)
	}
	transport := &statusNoticeTransport{}
	return k12usecase.Deps{Records: k12storage.NewStore(db, nil), Delivery: transport}, transport
}

func TestDingTalkPhotoStatusNoticeFreezesLocaleTargetAndSurvivesRestart(t *testing.T) {
	d, transport := statusNoticeDeps(t)
	bundle := terminalImageTaskBundle()
	for state, expected := range map[string]string{
		"accepted":        "已收到图片，正在识别和处理。",
		"failed":          "这张图片处理失败，暂未生成完整结果。",
		"outcome_unknown": "本次图片处理结果暂未确认，任务已保留，请勿重复发送。",
	} {
		runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{ReplyBatches: d, MessageLocale: func() string { return "en" }})
		if err := runtime.sendStatusNotice(context.Background(), bundle, state); err != nil {
			t.Fatal(err)
		}
		before, err := d.GetDeliveryBatchForObject(context.Background(), bundle.Receipt.AgentName, k12DingtalkPhotoStatusNoticeKind, bundle.Receipt.ReceiptID+":"+state)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(before.Receipts[0].PayloadJSON, expected) || before.Receipts[0].Target != inboundPhotoFrozenTarget(bundle).Target {
			t.Fatalf("notice lost frozen locale/target: %+v", before)
		}
		restarted := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{ReplyBatches: d, MessageLocale: func() string { return "ug" }, ResolveInstanceID: func(string, string) (string, error) { return "changed-bot", nil }})
		if err := restarted.sendStatusNotice(context.Background(), bundle, state); err != nil {
			t.Fatal(err)
		}
		after, err := d.GetDeliveryBatchForObject(context.Background(), bundle.Receipt.AgentName, k12DingtalkPhotoStatusNoticeKind, bundle.Receipt.ReceiptID+":"+state)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("restart changed notice: before=%+v after=%+v err=%v", before, after, err)
		}
	}
	if transport.prepared != 3 || len(transport.sends) != 3 || len(transport.queries) != 0 {
		t.Fatalf("duplicate notice send: %+v", transport)
	}
	if bundle.Dispatch.DeliveryBatchID != "" || bundle.Dispatch.ReplyStatus != k12usecase.InboundPhotoReplyPending {
		t.Fatal("notice occupied final delivery")
	}
}

func TestDingTalkPhotoStatusNoticeResumesOnlyPendingAndQueriesUnknown(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		d, transport := statusNoticeDeps(t)
		transport.status = k12.DeliveryOutcomeUnknown
		bundle := terminalImageTaskBundle()
		r := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{ReplyBatches: d})
		for range 3 {
			if err := r.sendStatusNotice(context.Background(), bundle, "outcome_unknown"); err != nil {
				t.Fatal(err)
			}
		}
		if len(transport.sends) != 1 || len(transport.queries) != 1 || transport.prepared != 1 {
			t.Fatalf("unknown was resent: %+v", transport)
		}
	})
	t.Run("pending original payload", func(t *testing.T) {
		d, transport := statusNoticeDeps(t)
		bundle := terminalImageTaskBundle()
		target := inboundPhotoFrozenTarget(bundle)
		payload := `{"content":"frozen original notice"}`
		root := k12.DeliveryBatch{BatchID: "prepared-notice", AgentName: bundle.Receipt.AgentName, ObjectKind: k12DingtalkPhotoStatusNoticeKind, ObjectID: bundle.Receipt.ReceiptID + ":accepted", DedupeKey: "prepared-notice", ContentDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("original"))), Receipts: []k12.DeliveryReceipt{{DeliveryID: "prepared-child", BindingID: target.BindingID, Target: target.Target, DedupeKey: "prepared-child", PayloadDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(payload))), PayloadJSON: payload, RenderJSON: `{"surface":"channel"}`, PartKind: messagecontent.PartMarkdown, PartOrdinal: 1}}}
		if _, _, err := d.Records.PrepareDeliveryBatch(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		r := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{ReplyBatches: d})
		for range 2 {
			if err := r.sendStatusNotice(context.Background(), bundle, "accepted"); err != nil {
				t.Fatal(err)
			}
		}
		if transport.prepared != 0 || len(transport.sends) != 1 || transport.sends[0].PayloadJSON != payload {
			t.Fatalf("pending notice recreated: %+v", transport)
		}
	})
}

func TestDingTalkPhotoStatusFailureWaitsWithoutRetryOrTerminalizing(t *testing.T) {
	for _, stage := range []string{k12.GradingStageFailedRetryable, k12.GradingStageFailedTerminal, k12.GradingStageOutcomeUnknown, "classification_unknown"} {
		t.Run(stage, func(t *testing.T) {
			d, transport := statusNoticeDeps(t)
			bundle := terminalImageTaskBundle()
			inbound := &inboundPhotoCoordinatorFake{bundle: bundle}
			images := &pollingImageTaskFacade{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{}, view: k12usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{Status: k12.ImageTaskStatusRouted, TaskIntent: k12.ImageTaskIntentCompletedHomework}, HomeworkProjection: &k12usecase.ImageTaskHomeworkProjection{Stage: stage}}}
			if stage == "classification_unknown" {
				images.view.HomeworkProjection = nil
				images.view.ClassificationInvocationStatus = k12.ImageTaskInvocationOutcomeUnknown
			}
			r := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: inbound, ImageTasks: images, ReplyBatches: d})
			for range 2 {
				if done, err := r.advance(context.Background(), inbound.bundle); done || !errors.Is(err, errK12DingtalkPhotoWaiting) {
					t.Fatalf("stopped state not parked: done=%v err=%v", done, err)
				}
			}
			if len(transport.sends) != 1 || inbound.terminalCalls != 0 || images.retryCalls != 0 || images.startCalls != 0 || inbound.boundBatchID != "" || inbound.completed {
				t.Fatalf("failure crossed recovery boundary: inbound=%+v images=%+v sends=%d", inbound, images, len(transport.sends))
			}
			images.view.ClassificationInvocationStatus = ""
			images.view.HomeworkProjection = &k12usecase.ImageTaskHomeworkProjection{Stage: k12.GradingStageAssessing}
			if done, err := r.advance(context.Background(), inbound.bundle); done || err != nil {
				t.Fatalf("recovered task not observed: done=%v err=%v", done, err)
			}
			if inbound.terminalCalls != 0 || images.retryCalls != 0 || images.startCalls != 0 {
				t.Fatal("observer retried a recovered task")
			}
		})
	}
}

func TestDingTalkPhotoStatusNoticeFailureDoesNotBlockTaskOrStealConfirmation(t *testing.T) {
	d, transport := statusNoticeDeps(t)
	transport.prepareErr = errors.New("controlled preparation failure")
	bundle := terminalImageTaskBundle()
	images := &pollingImageTaskFacade{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{}, view: k12usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{Status: k12.ImageTaskStatusRouted}, HomeworkProjection: &k12usecase.ImageTaskHomeworkProjection{Stage: k12.GradingStageAwaitingConfirmation}}}
	inbound := &inboundPhotoCoordinatorFake{bundle: bundle}
	r := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: inbound, ImageTasks: images, ReplyBatches: d})
	if done, err := r.advance(context.Background(), bundle); done || err != nil {
		t.Fatalf("notice failure blocked business: done=%v err=%v", done, err)
	}
	if len(images.events) != 0 || images.startCalls != 0 || images.retryCalls != 0 || inbound.terminalCalls != 0 || len(transport.sends) != 0 {
		t.Fatal("notice failure advanced shared confirmation or model")
	}
}

func TestDingTalkPhotoStatusNotificationPreservesDurableRecoveryEligibility(t *testing.T) {
	d, transport := statusNoticeDeps(t)
	inbound := k12usecase.NewInboundPhotoCoordinator(d.Records)
	fixture := terminalImageTaskBundle()
	ctx := context.Background()
	bundle, _, err := inbound.Admit(ctx, k12usecase.InboundPhotoAdmission{OwnerScope: fixture.Receipt.OwnerScope, AgentName: fixture.Receipt.AgentName, BindingID: fixture.Receipt.BindingID, Identity: fixture.Receipt.Identity, AssetName: fixture.Asset.Name, AssetMIME: fixture.Asset.MIME, AssetBytes: fixture.Asset.Bytes, CommandJSON: fixture.Receipt.CommandJSON})
	if err != nil {
		t.Fatal(err)
	}
	bundle.Dispatch, err = inbound.RecordImageTask(ctx, bundle.Receipt.AgentName, bundle.Receipt.ReceiptID, bundle.Dispatch.Version, "same-image-task")
	if err != nil {
		t.Fatal(err)
	}
	bundle.Dispatch, err = inbound.RecordRoutingDecision(ctx, bundle.Receipt.AgentName, bundle.Receipt.ReceiptID, bundle.Dispatch.Version, k12usecase.InboundPhotoRouteNewSubmission)
	if err != nil {
		t.Fatal(err)
	}
	before := bundle.Dispatch
	images := &pollingImageTaskFacade{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{}, view: k12usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{Status: k12.ImageTaskStatusRouted}, HomeworkProjection: &k12usecase.ImageTaskHomeworkProjection{Stage: k12.GradingStageFailedTerminal}}}
	for range 2 {
		restarted := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: inbound, ImageTasks: images, ReplyBatches: d})
		if done, err := restarted.advance(ctx, bundle); done || !errors.Is(err, errK12DingtalkPhotoWaiting) {
			t.Fatalf("recovery wait: done=%v err=%v", done, err)
		}
	}
	after, err := inbound.Resume(ctx, bundle.Receipt.AgentName, bundle.Receipt.ReceiptID)
	if err != nil || !reflect.DeepEqual(before, after.Dispatch) {
		t.Fatalf("notice changed durable recovery state: before=%+v after=%+v err=%v", before, after.Dispatch, err)
	}
	recoverable, err := inbound.Recoverable(ctx, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].Receipt.ReceiptID != bundle.Receipt.ReceiptID || len(transport.sends) != 1 {
		t.Fatalf("restart lost original receipt or resent: recoverable=%+v sends=%d err=%v", recoverable, len(transport.sends), err)
	}
}

func TestDingTalkPhotoStatusNoticeDoesNotBackfillLegacyReceipts(t *testing.T) {
	d, transport := statusNoticeDeps(t)
	bundle := terminalImageTaskBundle()
	bundle.Receipt.CommandJSON = strings.Replace(bundle.Receipt.CommandJSON, `"status_notice_version":"v1",`, "", 1)
	original := bundle.Receipt.CommandJSON
	r := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{ReplyBatches: d})
	for _, state := range []string{"accepted", "failed", "outcome_unknown"} {
		if err := r.sendStatusNotice(context.Background(), bundle, state); err != nil {
			t.Fatal(err)
		}
		_, err := d.GetDeliveryBatchForObject(context.Background(), bundle.Receipt.AgentName, k12DingtalkPhotoStatusNoticeKind, bundle.Receipt.ReceiptID+":"+state)
		if !errors.Is(err, records.ErrNotFound) {
			t.Fatalf("legacy notice was created: state=%s err=%v", state, err)
		}
	}
	if bundle.Receipt.CommandJSON != original || transport.prepared != 0 || len(transport.sends) != 0 || len(transport.queries) != 0 {
		t.Fatal("legacy command or delivery was changed")
	}
}

type statusNoticeReadError struct {
	k12usecase.Deps
	err error
}

func (p statusNoticeReadError) GetDeliveryBatchForObject(context.Context, string, string, string) (k12.DeliveryBatch, error) {
	return k12.DeliveryBatch{}, p.err
}

func TestDingTalkPhotoStatusNoticeReadErrorDoesNotCreateReplacement(t *testing.T) {
	d, transport := statusNoticeDeps(t)
	failure := errors.New("controlled batch read failure")
	r := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{ReplyBatches: statusNoticeReadError{Deps: d, err: failure}})
	if err := r.sendStatusNotice(context.Background(), terminalImageTaskBundle(), "failed"); !errors.Is(err, failure) {
		t.Fatalf("read error was hidden: %v", err)
	}
	if transport.prepared != 0 || len(transport.sends) != 0 {
		t.Fatal("read failure created a replacement notice")
	}
}

func TestDingTalkPhotoStatusLegacyUnknownStopsObservationWithoutMutation(t *testing.T) {
	d, transport := statusNoticeDeps(t)
	for _, source := range []string{"classification", "grading"} {
		t.Run(source, func(t *testing.T) {
			bundle := terminalImageTaskBundle()
			bundle.Receipt.CommandJSON = strings.Replace(bundle.Receipt.CommandJSON, `"status_notice_version":"v1",`, "", 1)
			before := bundle
			images := &pollingImageTaskFacade{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{}, view: k12usecase.ImageTaskView{Dispatch: k12.ImageTaskDispatch{Status: k12.ImageTaskStatusRouted}}}
			if source == "classification" {
				images.view.ClassificationInvocationStatus = k12.ImageTaskInvocationOutcomeUnknown
			} else {
				images.view.HomeworkProjection = &k12usecase.ImageTaskHomeworkProjection{Stage: k12.GradingStageOutcomeUnknown}
			}
			inbound := &inboundPhotoCoordinatorFake{bundle: bundle}
			r := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: inbound, ImageTasks: images, ReplyBatches: d})
			if done, err := r.advance(context.Background(), bundle); !done || err != nil {
				t.Fatalf("legacy unknown was rescheduled: done=%v err=%v", done, err)
			}
			if !reflect.DeepEqual(inbound.bundle, before) || inbound.terminalCalls != 0 || images.retryCalls != 0 || images.startCalls != 0 || images.resultCalls != 0 || transport.prepared != 0 || len(transport.sends) != 0 || len(transport.queries) != 0 {
				t.Fatal("legacy unknown observation changed records or crossed a side-effect boundary")
			}
			bundle.Receipt.CommandJSON = `{invalid`
			if done, err := r.advance(context.Background(), bundle); done || err == nil {
				t.Fatalf("invalid frozen command was silently skipped: done=%v err=%v", done, err)
			}
		})
	}
}
