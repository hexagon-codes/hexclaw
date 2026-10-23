package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type imFinalReplyTestDelivery struct {
	requests []k12usecase.FinalReplyDeliveryRequest
	targets  [][]k12usecase.ResolvedDeliveryTarget
	batch    k12.DeliveryBatch
	accepted bool
	err      error
	pdf      k12usecase.PrintableArtifactView
	pdfErr   error
	pdfReads int
}

func (f *imFinalReplyTestDelivery) PrepareAndSendFinalReply(_ context.Context, req k12usecase.FinalReplyDeliveryRequest, targets []k12usecase.ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	f.requests = append(f.requests, req)
	f.targets = append(f.targets, append([]k12usecase.ResolvedDeliveryTarget(nil), targets...))
	return f.batch, f.accepted, f.err
}

func (f *imFinalReplyTestDelivery) GetPrintableArtifactPDF(_ context.Context, agent, artifact string) (k12usecase.PrintableArtifactView, error) {
	f.pdfReads++
	if agent != f.pdf.Artifact.AgentName || artifact != f.pdf.Artifact.ArtifactID {
		return k12usecase.PrintableArtifactView{}, errors.New("unexpected artifact identity")
	}
	return f.pdf, f.pdfErr
}

func imFinalReplyTestInput(t *testing.T, text string) (*adapter.Message, *adapter.Reply, imFinalReplyDependencies, *imFinalReplyTestDelivery) {
	t.Helper()
	canonical, err := messagecontent.New(messagecontent.ProducerSkill, "ug", text, nil)
	if err != nil {
		t.Fatal(err)
	}
	msg := &adapter.Message{ID: "provider-event-1", Platform: adapter.PlatformDingtalk, InstanceID: "renamed-robot", UserID: "actual-sender", ChatID: "actual-sender",
		Metadata: map[string]string{"conversation_type": "1", "user_locale": "ug", "locale": "ug"}}
	reply := &adapter.Reply{Content: text, MessageContent: &canonical, AssistantMessageID: "assistant-actual-1", Metadata: map[string]string{"routed_agent": "actual-routed-agent"}}
	delivery := &imFinalReplyTestDelivery{batch: k12.DeliveryBatch{BatchID: "frozen-batch", Status: k12.DeliveryBatchDelivered}}
	deps := imFinalReplyDependencies{delivery: delivery, ownerID: "actual-owner", resolveInstanceID: func(platform, instance string) (string, error) {
		if platform != "dingtalk" || instance != "renamed-robot" {
			t.Fatalf("unexpected instance resolution: %q %q", platform, instance)
		}
		return "stable-instance-id", nil
	}}
	return msg, reply, deps, delivery
}

func TestIMFinalReplyUsesActualPrivateIdentityProducerAndFrozenLocale(t *testing.T) {
	msg, reply, deps, delivery := imFinalReplyTestInput(t, strings.Repeat("正文", 500))
	beforeMsg, beforeReply := *msg, *reply
	got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
	if err != nil || got != nil || len(delivery.requests) != 1 || len(delivery.targets) != 1 {
		t.Fatalf("final reply did not use one durable batch: reply=%+v error=%v", got, err)
	}
	req := delivery.requests[0]
	wantProof := k12.AcceptedPrivateReplySource{AgentName: "actual-routed-agent", Platform: "dingtalk", InstanceID: "stable-instance-id", SenderID: "actual-sender", MessageID: "provider-event-1", AssistantMessageID: "assistant-actual-1"}
	wantID, err := k12.PrivateReplyObjectID(wantProof)
	if err != nil || req.PrivateEvent == nil || *req.PrivateEvent != wantProof || req.ObjectID != wantID || req.ObjectKind != "im_final_reply" || req.AgentName != wantProof.AgentName {
		t.Fatalf("source identity was synthesized or changed: %+v %v", req, err)
	}
	if req.Producer != messagecontent.ProducerSkill || req.Locale != "ug" || req.Message.Content != reply.Content || req.SourceRef != reply.AssistantMessageID {
		t.Fatalf("canonical producer, source or frozen locale changed: %+v", req)
	}
	targets := delivery.targets[0]
	authorityID, err := k12.PrivateReplyAuthorityID(wantProof)
	if err != nil || len(targets) != 1 || targets[0].BindingID != authorityID || !strings.HasPrefix(authorityID, "accepted-event:") || targets[0].Target.Platform != "dingtalk" || targets[0].Target.InstanceID != wantProof.InstanceID || targets[0].Target.ChatID != wantProof.SenderID {
		t.Fatalf("private reply invented a binding or another target: %+v", targets)
	}
	if !reflect.DeepEqual(beforeMsg, *msg) || !reflect.DeepEqual(beforeReply, *reply) {
		t.Fatal("batch projection changed the original inbound message or reply")
	}
}

func TestIMFinalReplyExistingBatchSuppressesInlineReplyAndHandlerError(t *testing.T) {
	for _, status := range []k12.DeliveryBatchStatus{k12.DeliveryBatchPending, k12.DeliveryBatchSending, k12.DeliveryBatchDelivered, k12.DeliveryBatchFailed, k12.DeliveryBatchPartialFailed, k12.DeliveryBatchOutcomeUnknown} {
		t.Run(string(status), func(t *testing.T) {
			msg, reply, deps, delivery := imFinalReplyTestInput(t, strings.Repeat("正文", 500))
			delivery.batch.Status = status
			delivery.err = context.DeadlineExceeded
			got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
			if got != nil || err != nil || len(delivery.requests) != 1 {
				t.Fatalf("existing batch could trigger inline resend or release inbound dedupe: reply=%+v error=%v", got, err)
			}
		})
	}
}

func TestIMFinalReplyAcceptedWithoutReadbackSuppressesInlineReply(t *testing.T) {
	msg, reply, deps, delivery := imFinalReplyTestInput(t, strings.Repeat("正文", 500))
	delivery.batch = k12.DeliveryBatch{}
	delivery.accepted = true
	delivery.err = errors.New("accepted batch readback failed")
	got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
	if got != nil || err != nil || len(delivery.requests) != 1 {
		t.Fatalf("accepted delivery fell back to inline send after readback failure: reply=%+v error=%v", got, err)
	}
}

func TestIMFinalReplyReadableThresholdExcludesActualToolDigest(t *testing.T) {
	body := strings.Repeat("甲", 999)
	msg, reply, deps, delivery := imFinalReplyTestInput(t, body)
	reply.ToolCalls = []adapter.ToolCall{{Name: "code_exec", Status: "success", Result: strings.Repeat("实际工具结果", 20)}}
	tail := adapter.DingTalkReplyFooter(reply, "ug")
	if tail == "" || k12usecase.K12FinalReplyReadableCharacters(body+tail) < 1000 {
		t.Fatal("tool digest fixture did not cross the full-string threshold")
	}
	if err := appendIMToolDigestAndRebuildCanonical(reply, "dingtalk"); err != nil {
		t.Fatal(err)
	}
	if reply.Content != body || reply.MessageContent.Validate() != nil {
		t.Fatal("IM projection changed the canonical body")
	}
	got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
	if got != reply || err != nil || len(delivery.requests) != 0 || reply.Content != body {
		t.Fatalf("tool digest changed the short body into a PDF or was removed: reply=%+v error=%v", got, err)
	}
}

func TestIMFinalReplyModelSuffixStaysOutsidePDFThresholdAndBody(t *testing.T) {
	for _, size := range []int{999, 1000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := "### 标题\n\n" + strings.Repeat("甲", size-3)
			msg, reply, deps, delivery := imFinalReplyTestInput(t, body)
			reply.Metadata["provider"], reply.Metadata["provider_display_name"], reply.Metadata["model"] = "stable-id", "مودېل مۇلازىمىتى", "gpt-6.1-sol"
			reply.ToolCalls = []adapter.ToolCall{{Name: "solve", Result: "### 第一步\n\n原长40米"}}
			if err := appendIMToolDigestAndRebuildCanonical(reply, "dingtalk"); err != nil {
				t.Fatal(err)
			}
			got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
			if err != nil {
				t.Fatal(err)
			}
			if size == 999 {
				if got != reply || len(delivery.requests) != 0 {
					t.Fatal("model or tool suffix pushed a 999-character body into PDF")
				}
				return
			}
			if got != nil || len(delivery.requests) != 1 {
				t.Fatal("1000-character body did not create one final PDF request")
			}
			req := delivery.requests[0]
			if req.Message.Content != body || strings.Contains(req.Message.Content, "gpt-6.1-sol") || !strings.Contains(req.DingTalkSuffix, "مودېل مۇلازىمىتى") || !strings.Contains(req.DingTalkSuffix, "gpt-6.1-sol") {
				t.Fatalf("PDF source changed or caption lost actual model: %+v", req)
			}
		})
	}
}

func TestIMFinalReplyKnownPreparationFailurePreservesCompleteReply(t *testing.T) {
	msg, reply, deps, delivery := imFinalReplyTestInput(t, strings.Repeat("正文", 500))
	delivery.batch = k12.DeliveryBatch{}
	delivery.err = errors.New("PDF preparation failed before delivery")
	before, _ := json.Marshal(reply)
	got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
	after, _ := json.Marshal(reply)
	if got != reply || err != nil || string(before) != string(after) || len(delivery.requests) != 1 {
		t.Fatalf("pre-send failure lost or changed complete fallback: %+v %v", got, err)
	}
}

func TestIMFinalReplyLeavesNonFinalAndIneligibleInputsOnOriginalPath(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*adapter.Message, *adapter.Reply, *imFinalReplyDependencies)
	}{
		{"short reply", func(_ *adapter.Message, reply *adapter.Reply, _ *imFinalReplyDependencies) {
			canonical, _ := messagecontent.New(messagecontent.ProducerChat, "ug", "short", nil)
			reply.Content, reply.MessageContent = "short", &canonical
		}},
		{"group", func(msg *adapter.Message, _ *adapter.Reply, _ *imFinalReplyDependencies) {
			msg.Metadata["conversation_type"] = "2"
		}},
		{"other recipient", func(msg *adapter.Message, _ *adapter.Reply, _ *imFinalReplyDependencies) { msg.ChatID = "other-person" }},
		{"missing event", func(msg *adapter.Message, _ *adapter.Reply, _ *imFinalReplyDependencies) { msg.ID = "" }},
		{"missing routed agent", func(_ *adapter.Message, reply *adapter.Reply, _ *imFinalReplyDependencies) {
			delete(reply.Metadata, "routed_agent")
		}},
		{"missing saved assistant", func(_ *adapter.Message, reply *adapter.Reply, _ *imFinalReplyDependencies) {
			reply.AssistantMessageID = ""
		}},
		{"failed persistence", func(_ *adapter.Message, reply *adapter.Reply, _ *imFinalReplyDependencies) {
			reply.Metadata["persist_error"] = "assistant_reply failed"
		}},
		{"invalid canonical", func(_ *adapter.Message, reply *adapter.Reply, _ *imFinalReplyDependencies) {
			reply.MessageContent.SourceDigest = "invalid"
		}},
		{"frozen locale mismatch", func(msg *adapter.Message, _ *adapter.Reply, _ *imFinalReplyDependencies) {
			msg.Metadata["user_locale"] = "en"
		}},
		{"interactive control", func(_ *adapter.Message, reply *adapter.Reply, _ *imFinalReplyDependencies) {
			reply.Interactive = &adapter.InteractivePayload{}
		}},
		{"inbound image", func(msg *adapter.Message, _ *adapter.Reply, _ *imFinalReplyDependencies) {
			msg.Attachments = []adapter.Attachment{{Type: "image"}}
		}},
		{"outbound image", func(_ *adapter.Message, reply *adapter.Reply, _ *imFinalReplyDependencies) {
			reply.Attachments = []adapter.Attachment{{Type: "image"}}
		}},
		{"instance unavailable", func(_ *adapter.Message, _ *adapter.Reply, deps *imFinalReplyDependencies) {
			deps.resolveInstanceID = func(string, string) (string, error) { return "", errors.New("instance is unavailable") }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, reply, deps, delivery := imFinalReplyTestInput(t, strings.Repeat("正文", 500))
			tc.change(msg, reply, &deps)
			got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
			if err != nil || got != reply || len(delivery.requests) != 0 {
				t.Fatalf("existing non-final path was replaced: %+v %v calls=%d", got, err, len(delivery.requests))
			}
		})
	}
	msg, reply, deps, delivery := imFinalReplyTestInput(t, strings.Repeat("正文", 500))
	handlerErr := errors.New("original processing failed")
	if got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, handlerErr, deps); got != reply || err != handlerErr || len(delivery.requests) != 0 {
		t.Fatal("original handler error was changed")
	}
}

func TestIMFinalReplyReusesExactUnitMaterialPDF(t *testing.T) {
	msg, reply, deps, delivery := imFinalReplyTestInput(t, "Saved material is ready.")
	ref := imFinalReplyUnitArtifact{Kind: "unit_summary", State: "succeeded", AttemptID: "attempt-current", DocumentID: "doc-1", RevisionID: "rev-2", ArtifactID: "pdf-2", ContentDigest: "content-digest"}
	raw, _ := json.Marshal([]imFinalReplyUnitArtifact{ref})
	reply.Metadata["artifacts"] = string(raw)
	payload := []byte("%PDF-1.7 controlled artifact")
	sum := sha256.Sum256(payload)
	byteDigest := hex.EncodeToString(sum[:])
	material := &k12usecase.UnitSummaryMaterialView{UnitSummaryRevision: k12.UnitSummaryRevision{AgentName: "actual-routed-agent", DocumentID: ref.DocumentID, RevisionID: ref.RevisionID, ArtifactID: ref.ArtifactID, ContentDigest: ref.ContentDigest, CanonicalMarkdown: "Complete saved learning material", ByteDigest: byteDigest, ByteSize: int64(len(payload))}, Title: "Existing material title"}
	view := k12usecase.UnitSummaryView{Document: &k12.UnitSummaryDocument{AgentName: "actual-routed-agent", DocumentID: ref.DocumentID}, Material: material}
	view.Delivery.Complete = true
	attempt := k12usecase.UnitSummaryView{Job: k12.UnitSummaryAttempt{AttemptID: ref.AttemptID, OwnerID: "actual-owner", AgentName: "actual-routed-agent", Intent: "generate", State: ref.State, DocumentID: ref.DocumentID, ResultRevisionID: ref.RevisionID, ArtifactID: ref.ArtifactID}}
	attempt.Delivery.Complete = true
	deps.getUnitAttempt = func(_ context.Context, owner, agent, id string) (k12usecase.UnitSummaryView, error) {
		if owner != "actual-owner" || agent != "actual-routed-agent" || id != ref.AttemptID {
			t.Fatal("unit intent came from a different attempt")
		}
		return attempt, nil
	}
	documentReads := 0
	deps.getUnitDocument = func(_ context.Context, owner, agent, document, revision string) (k12usecase.UnitSummaryView, error) {
		documentReads++
		if owner != "actual-owner" || agent != "actual-routed-agent" || document != ref.DocumentID || revision != ref.RevisionID {
			t.Fatal("unit reader used another source identity")
		}
		return view, nil
	}
	delivery.pdf = k12usecase.PrintableArtifactView{Artifact: k12.PrintArtifact{AgentName: "actual-routed-agent", ArtifactID: ref.ArtifactID, SourceKind: k12.PrintSourceUnitSummary, CanonicalMarkdown: material.CanonicalMarkdown}, Render: k12.PrintArtifactRender{ArtifactID: ref.ArtifactID, ByteDigest: byteDigest, ByteSize: int64(len(payload)), Payload: payload}}
	got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
	if got != nil || err != nil || documentReads != 1 || delivery.pdfReads != 1 || len(delivery.requests) != 1 {
		t.Fatalf("complete short unit reply did not reuse its PDF: %+v %v", got, err)
	}
	req := delivery.requests[0]
	if !req.Document || req.PDF == nil || !reflect.DeepEqual(*req.PDF, delivery.pdf) || req.Message.Content != reply.Content || req.Summary != reply.Content || req.SourceKind != k12.PrintSourceUnitSummary || req.Title != material.Title {
		t.Fatal("unit path changed assistant text or regenerated material")
	}
	var source map[string]string
	if json.Unmarshal([]byte(req.SourceRef), &source) != nil || source["document_id"] != ref.DocumentID || source["revision_id"] != ref.RevisionID || source["artifact_id"] != ref.ArtifactID || source["content_digest"] != ref.ContentDigest {
		t.Fatal("exact unit version reference was not retained")
	}
	attempt.Job.Intent = "follow_up"
	delivery.requests = nil
	if got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps); got != reply || err != nil || len(delivery.requests) != 0 || documentReads != 1 || delivery.pdfReads != 1 {
		t.Fatal("ordinary material follow-up appended a file or reread the PDF")
	}
	attempt.Job.Intent = "generate"
	material.RevisionID = "other-revision"
	delivery.requests = nil
	if got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps); got != reply || err != nil || len(delivery.requests) != 0 {
		t.Fatal("mismatched revision was delivered")
	}
}
