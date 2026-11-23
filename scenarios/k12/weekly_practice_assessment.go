package k12

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// WeeklyPracticeAttemptID identifies the original attempt both when it is
// submitted and when a weekly archive is restored to a different learner.
func WeeklyPracticeAttemptID(snapshotID, itemID, key string) string {
	sum := sha256.Sum256([]byte(snapshotID + "\x00" + itemID + "\x00" + key))
	return "wattempt-" + hex.EncodeToString(sum[:12])
}

// WeeklyAssessmentRequestDigest 按最初冻结的快照、题项与原答案绑定命令。
func WeeklyAssessmentRequestDigest(snapshot, item, answer string) string {
	raw, _ := json.Marshal(struct{ Snapshot, Item, Answer string }{snapshot, item, answer})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// WeeklyPracticeAssessmentCommandStatus is the crash-durable ownership and
// recovery state for one physical answer-assessment call.
type WeeklyPracticeAssessmentCommandStatus string

const (
	WeeklyAssessmentPrepared       WeeklyPracticeAssessmentCommandStatus = "prepared"
	WeeklyAssessmentSent           WeeklyPracticeAssessmentCommandStatus = "sent"
	WeeklyAssessmentSucceeded      WeeklyPracticeAssessmentCommandStatus = "succeeded"
	WeeklyAssessmentFailed         WeeklyPracticeAssessmentCommandStatus = "failed"
	WeeklyAssessmentOutcomeUnknown WeeklyPracticeAssessmentCommandStatus = "outcome_unknown"
	WeeklyAssessmentCommitted      WeeklyPracticeAssessmentCommandStatus = "committed"
)

// WeeklyPracticeAssessmentCommand persists both the immutable request binding
// and the provider receipt. A succeeded receipt can be committed locally after
// a restart without issuing another physical provider call.
type WeeklyPracticeAssessmentCommand struct {
	CommandID        string
	AgentName        string
	SnapshotID       string
	ItemID           string
	IdempotencyKey   string
	RequestDigest    string
	Status           WeeklyPracticeAssessmentCommandStatus
	AssessmentJSON   string
	AssessmentDigest string
	FailureKind      string
	AttemptID        string
	CreatedAt        int64
	UpdatedAt        int64
}
