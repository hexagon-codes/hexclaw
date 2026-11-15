package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"testing"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/records"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func originalPhotoReportFixture(t *testing.T) (k12.GradingFinalArtifact, []byte) {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	source := encoded.Bytes()
	sum := sha256.Sum256(source)
	digest := hex.EncodeToString(sum[:])
	artifact, _ := finalArtifactFixture(source)
	artifact.AgentName = "child-tutor"
	artifact.AnnotatedAssetOwnerScope, artifact.AnnotatedAssetID, artifact.AnnotatedMIME, artifact.AnnotatedDigest, artifact.OriginalSourceDigest = "", "", "", "", ""
	origin := k12.TeachingReportContentOrigin{ProblemID: "q1", InputRevision: 1, InputDigest: "input-q1", SolutionDigest: "solution-q1"}
	report, err := k12.NewTeachingReport(k12.TeachingReport{
		SchemaVersion: k12.TeachingReportSchemaVersion, ContentVersion: k12.TeachingReportContentVersion,
		Source:         k12.TeachingReportSource{Kind: k12.TeachingReportSourceGrading, SourceID: artifact.JobID, Revision: 1, Digest: "controlled-source"},
		Metadata:       k12.TeachingReportMetadata{Title: "家长讲题指南", AgentDisplayName: "数学辅导", GradeTerm: "六年级上", Subject: "数学", ReportDate: "2026-10-23", Timezone: "Asia/Shanghai"},
		SourcePages:    []k12.TeachingReportSourcePage{{AssetID: "original-page", AssetDigest: digest, SourceDigest: digest, MIME: "image/png", Width: 2, Height: 2}},
		ProblemSources: []k12.TeachingReportProblemSource{{ProblemID: "q1", AttemptID: "attempt-q1", InputRevision: 1, InputDigest: "input-q1", SolutionDigest: "solution-q1", AssessmentDigest: "assessment-q1", AssessmentStatus: k12.GradingAssessmentUntrusted, AnswerState: k12.TeachingReportAnswerUnreadable}},
		Items: []k12.TeachingReportItem{{ProblemID: "q1", DisplayLabel: "1", ShortTitle: "笔的数量", Assessment: k12.TeachingReportAssessment{Status: k12.GradingAssessmentUntrusted},
			OriginalAnswer: k12.TeachingReportOriginalAnswer{State: k12.TeachingReportAnswerUnreadable}, AnswerAnalysis: k12.TeachingReportAnswerAnalysis{
				State: k12.TeachingReportReferenceAvailable, Answers: []k12.TeachingReportAnswer{{ID: "answer", Text: "4支", Origin: origin}},
				Steps:    []k12.TeachingReportCalculationStep{{ID: "calculation", Text: "2×2=4支", Origin: origin}},
				Evidence: k12.TeachingReportReferenceEvidence{Verdict: "agree", EvidenceType: "heuristic", SolutionDigest: "solution-q1"},
			}}},
		DeepSections: []k12.TeachingReportDeepSection{{ID: "reason", Title: "数量关系", ProblemIDs: []string{"q1"}, Blocks: []k12.TeachingReportExplanationBlock{{ID: "reason-block", Text: "每盒2支，2盒共有4支。", Origins: []k12.TeachingReportContentOrigin{origin}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact.CanonicalMarkdown, err = k12usecase.TeachingReportMarkdown(report)
	if err != nil {
		t.Fatal(err)
	}
	artifact.TeachingReport = &report
	raw, err := json.Marshal(k12usecase.TeachingReportFrozenSnapshot{Report: report})
	if err != nil {
		t.Fatal(err)
	}
	artifact.ReportSnapshotJSON = string(raw)
	artifact.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(artifact)
	return artifact, source
}

type originalPhotoReplyBatchFixture struct {
	finalReplyBatchFake
	sharedCalls int
	pdf         bool
}

func (*originalPhotoReplyBatchFixture) GetDeliveryBatchForMessageIdentity(context.Context, string, string, string, string, []k12usecase.DeliveryAttachmentIdentity) (k12.DeliveryBatch, error) {
	return k12.DeliveryBatch{}, records.ErrNotFound
}

func (fixture *originalPhotoReplyBatchFixture) PrepareAndSendK12FinalReplyBatch(_ context.Context, agent, kind, id, _, _ string, message k12usecase.DeliveryMessage, targets []k12usecase.ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	fixture.sharedCalls++
	fixture.lastMessage, fixture.lastTargets = message, targets
	mime := "image/png"
	if fixture.pdf {
		mime = "application/pdf"
	}
	target := targets[0]
	fixture.existing = k12.DeliveryBatch{BatchID: "original-report-batch", AgentName: agent, ObjectKind: kind, ObjectID: id, Status: k12.DeliveryBatchDelivered,
		Receipts: []k12.DeliveryReceipt{
			{DeliveryID: "original-report-text", PartKind: messagecontent.PartMarkdown, PartOrdinal: 1, BindingID: target.BindingID, Target: target.Target, Status: k12.DeliveryDelivered},
			{DeliveryID: "original-report-media", PartKind: messagecontent.PartArtifact, PartOrdinal: 2, PartMIME: mime, PartDigest: "controlled-part-digest", BindingID: target.BindingID, Target: target.Target, Status: k12.DeliveryDelivered},
		}}
	return fixture.existing, true, nil
}

func TestDingTalkPhotoOriginalTeachingReportUsesSharedImageAndPDFPartSets(t *testing.T) {
	for _, mode := range []string{"image", "pdf", "bad_original"} {
		t.Run(mode, func(t *testing.T) {
			artifact, source := originalPhotoReportFixture(t)
			bundle := inboundPhotoBundleFixture(source)
			bundle.Receipt.AgentName = "child-tutor"
			bundle.Dispatch.ProcessingStatus, bundle.Dispatch.ReplyStatus = k12usecase.InboundPhotoFinalArtifactReady, k12usecase.InboundPhotoReplyReady
			bundle.Dispatch.RoutingDecision, bundle.Dispatch.FinalArtifactID, bundle.Dispatch.ImageTaskID = k12usecase.InboundPhotoRouteNewSubmission, artifact.ArtifactID, "dispatch-1"
			if mode == "bad_original" {
				bundle.Asset.Bytes = []byte("broken original")
			}
			coordinator := &inboundPhotoCoordinatorFake{bundle: bundle}
			reader := &solveFinalArtifactReader{finalArtifactReaderFake: finalArtifactReaderFake{artifact: artifact}}
			images := &scopedSolveImageTasks{fakeK12ImageTaskFacade: &fakeK12ImageTaskFacade{}, action: "grade"}
			batches := &originalPhotoReplyBatchFixture{pdf: mode == "pdf"}
			runtime := newK12DingtalkPhotoInboundRuntime(k12DingtalkPhotoInboundRuntimeConfig{Inbound: coordinator, ImageTasks: images, Artifacts: reader, ReplyBatches: batches})
			done, err := runtime.advanceFinalReply(t.Context(), bundle)
			if mode == "bad_original" {
				if err == nil || done || batches.sharedCalls != 0 {
					t.Fatalf("bad original produced a successful send: done=%v sends=%d err=%v", done, batches.sharedCalls, err)
				}
				return
			}
			if err != nil || !done || batches.sharedCalls != 1 || reader.annotatedReads != 0 || len(batches.lastMessage.Attachments) != 1 {
				t.Fatalf("original report did not use the shared reply contract: done=%v shared=%d annotated=%d err=%v", done, batches.sharedCalls, reader.annotatedReads, err)
			}
			image := batches.lastMessage.Attachments[0]
			if image.Name != "原图.png" || image.MIME != "image/png" || !bytes.Equal(image.Data, source) {
				t.Fatal("original image was missing or mislabeled as annotated")
			}
		})
	}
}
