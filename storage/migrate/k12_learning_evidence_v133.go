package migrate

// 新字段为空时保留历史未知；不改变已有掌握、回传和复批结论。
var K12LearningEvidenceV133 = Migration{Version: 133, Description: "K12 observed answer and learning evidence provenance", SQL: `
ALTER TABLE k12_mistakes ADD COLUMN observed_student_answer TEXT NOT NULL DEFAULT '';
ALTER TABLE k12_practice_set_items ADD COLUMN learning_evidence_json TEXT NOT NULL DEFAULT '';
ALTER TABLE k12_practice_return_assets ADD COLUMN learning_evidence_json TEXT NOT NULL DEFAULT '';
`}
