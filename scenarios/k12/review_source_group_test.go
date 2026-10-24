package k12

import (
	"errors"
	"strings"
	"testing"
)

func TestReviewSourceGroupIdentityBindsExactMembersAndStatement(t *testing.T) {
	group := ReviewSourceGroup{
		AgentName: "learner", CanonicalRecordID: "first", CanonicalStatement: "已用4.8 TB，占总量2/5。", CreatedAt: 100,
		Evidence: ReviewSourceGroupEvidence{Kind: ReviewSourceEvidenceReviewed, Note: "已核对两份来源为同一问题。"},
		Members: []ReviewSourceGroupMember{
			{RecordID: "second", QuestionDigest: ReviewSourceTextDigest("已用4.8 TB。"), JobID: "job-2", ProblemID: "problem-2", AttemptID: "attempt-2", ConfirmedVersion: 1, InputRevision: 1, InputDigest: "input-2", ResultDigest: "result-2", PageDigest: strings.Repeat("b", 64), SourceNumberPath: []string{"3", "1"}},
			{RecordID: "first", QuestionDigest: ReviewSourceTextDigest("已用4.8 TB。"), JobID: "job-1", ProblemID: "problem-1", AttemptID: "attempt-1", ConfirmedVersion: 1, InputRevision: 1, InputDigest: "input-1", ResultDigest: "result-1", PageDigest: strings.Repeat("a", 64), SourceNumberPath: []string{"3", "1"}},
		},
	}
	first, err := group.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	group.Members[0], group.Members[1] = group.Members[1], group.Members[0]
	group.CreatedAt, group.Evidence.Note = 200, "补充说明不重建相同关联。"
	replay, err := group.Normalized()
	if err != nil || replay.GroupID != first.GroupID {
		t.Fatalf("ordering, review note or time changed identity: %v", err)
	}
	group.Members[0].SourceNumberPath[1] = "2"
	if first.Members[0].SourceNumberPath[1] != "1" {
		t.Fatal("normalized identity shares mutable numbering with input")
	}
	changed, err := group.Normalized()
	if err != nil || changed.GroupID == first.GroupID {
		t.Fatalf("different subproblem reused the reviewed identity: %v", err)
	}
	changed = first
	changed.CanonicalStatement = "已用4.8 GB，占总量2/5。"
	if _, err := changed.Normalized(); !errors.Is(err, ErrInvalidReviewSourceGroup) {
		t.Fatalf("changed unit reused frozen statement identity: %v", err)
	}
	changed = first
	changed.Evidence.Kind = "similarity_candidate"
	if _, err := changed.Normalized(); !errors.Is(err, ErrInvalidReviewSourceGroup) {
		t.Fatalf("unreviewed similarity became a reviewed source group: %v", err)
	}
}
