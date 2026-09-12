package usecase_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type weeklyDeliveryRenderer struct {
	calls    int
	payload  []byte
	markdown string
}

func (r *weeklyDeliveryRenderer) Render(
	_ context.Context,
	markdown string,
	_ string,
) ([]byte, string, error) {
	r.calls++
	r.markdown = markdown
	return append([]byte(nil), r.payload...), "application/pdf", nil
}

type weeklyDeliveryPayload struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
	Name    string `json:"name"`
	MIME    string `json:"mime"`
	Data    []byte `json:"data"`
}

func TestWeeklyPracticeSendUsesFrozenArtifactMarkdownAndPDFForEveryDirectTarget(t *testing.T) {
	d := newDataDeps(t, "xiaoming")
	clock := &mutableWeeklyClock{now: 1785081600}
	d.Now = func() int64 { return clock.now }
	configureWeeklyBundle(t, &d, false)
	seedInvariantDueMistake(t, d, clock.now-1)

	plan, _, err := d.EnsureWeeklyPracticePlan(context.Background(),
		usecase.EnsureWeeklyPracticePlanRequest{
			AgentName: "xiaoming", IdempotencyKey: "weekly-delivery-plan",
		})
	if err != nil {
		t.Fatal(err)
	}
	renderer := &weeklyDeliveryRenderer{payload: []byte("%PDF-1.7\nweekly-delivery-frozen")}
	d.Renderer = renderer
	prepared, err := d.PrepareWeeklyPracticeOutput(
		context.Background(), "xiaoming", plan.PlanID, plan.Revision, "weekly-delivery-output",
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Snapshot.ArtifactID == "" || renderer.calls != 1 {
		t.Fatalf("weekly output did not freeze one PDF artifact: result=%+v render_calls=%d",
			prepared, renderer.calls)
	}

	transport := newMessagePartTransport()
	d.Delivery = transport
	batch, err := d.SendWeeklyPracticeSnapshot(
		context.Background(), "xiaoming", prepared.Snapshot.SnapshotID, "weekly-delivery-send",
	)
	if err != nil {
		t.Fatal(err)
	}
	wantReceiptCount := len(transport.targets) * 2
	if len(batch.Receipts) != wantReceiptCount {
		t.Fatalf("weekly send receipts=%d want %d direct targets × (Markdown + frozen PDF)",
			len(batch.Receipts), wantReceiptCount)
	}
	if renderer.calls != 1 {
		t.Fatalf("weekly send rerendered the frozen artifact: render_calls=%d want 1", renderer.calls)
	}
	if transport.prepareCalls != 1 {
		t.Fatalf("weekly send prepared %d message projections want 1", transport.prepareCalls)
	}
	if len(transport.preflightCalls) != len(transport.targets) {
		t.Fatalf("weekly PDF media preflights=%d want one per direct target",
			len(transport.preflightCalls))
	}

	for i, receipt := range batch.Receipts {
		wantOrdinal := i%2 + 1
		if receipt.BatchOrdinal != i+1 || receipt.PartOrdinal != wantOrdinal ||
			receipt.Status != k12.DeliveryDelivered || receipt.ExternalMessageID == "" {
			t.Fatalf("weekly target×part receipt %d identity/status drifted: %+v", i, receipt)
		}
		var payload weeklyDeliveryPayload
		if err := json.Unmarshal([]byte(receipt.PayloadJSON), &payload); err != nil {
			t.Fatalf("decode weekly delivery payload %d: %v", i, err)
		}
		switch wantOrdinal {
		case 1:
			if receipt.PartKind != messagecontent.PartMarkdown || receipt.PartMIME != "" ||
				payload.Content != prepared.Artifact.Artifact.Title {
				t.Fatalf("weekly message must contain only the existing title: got=%q want=%q",
					payload.Content, prepared.Artifact.Artifact.Title)
			}
		case 2:
			if receipt.PartKind != messagecontent.PartArtifact ||
				receipt.PartMIME != "application/pdf" ||
				receipt.PartDigest != partTestDigestBytes(renderer.payload) ||
				payload.Name != prepared.Artifact.Artifact.Title+".pdf" ||
				payload.MIME != "application/pdf" || !bytes.Equal(payload.Data, renderer.payload) {
				t.Fatalf("weekly PDF did not reuse the frozen artifact bytes: receipt=%+v payload=%+v",
					receipt, payload)
			}
		}
	}

	prepareCalls, sends, renderCalls := transport.prepareCalls, len(transport.sends), renderer.calls
	replayed, err := d.SendWeeklyPracticeSnapshot(
		context.Background(), "xiaoming", prepared.Snapshot.SnapshotID, "weekly-delivery-send",
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.BatchID != batch.BatchID || transport.prepareCalls != prepareCalls ||
		len(transport.sends) != sends || renderer.calls != renderCalls {
		t.Fatalf("weekly send replay crossed preparation/render/provider boundaries: first=%+v replay=%+v",
			batch, replayed)
	}

	const sheet = "# 本周错题卷\n\n1. 12 ÷ 4 = ?"
	if err := d.DeliverCronResult(context.Background(), "xiaoming", usecase.KindWeeklySheet, sheet, nil); err != nil {
		t.Fatal(err)
	}
	if renderer.calls != renderCalls+1 || renderer.markdown != sheet || len(transport.sends) != sends+wantReceiptCount {
		t.Fatalf("weekly cron must render the complete sheet once and send title/PDF: renders=%d sends=%d", renderer.calls, len(transport.sends))
	}
	var title weeklyDeliveryPayload
	if err := json.Unmarshal([]byte(transport.sends[sends].PayloadJSON), &title); err != nil || title.Content != "本周错题卷" {
		t.Fatalf("weekly cron title=%q err=%v", title.Content, err)
	}
	if err := d.DeliverCronResult(context.Background(), "xiaoming", usecase.KindWeeklySheet, sheet+"\n\n2. 1 + 1 = ?", nil); err != nil {
		t.Fatal(err)
	}
	if renderer.calls != renderCalls+1 || len(transport.sends) != sends+wantReceiptCount {
		t.Fatalf("same-week replay rendered or resent: renders=%d sends=%d", renderer.calls, len(transport.sends))
	}
}
