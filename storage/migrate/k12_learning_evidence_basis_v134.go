package migrate

// 旧回传保持空依据；新回传在接纳事务内冻结来源时间，不回填未知历史。
var K12LearningEvidenceBasisV134 = Migration{Version: 134, Description: "K12 frozen practice evidence basis and result ownership", SQL: `
ALTER TABLE k12_practice_return_assets ADD COLUMN learning_evidence_basis_json TEXT NOT NULL DEFAULT '';
ALTER TABLE k12_practice_set_items ADD COLUMN result_return_id TEXT NOT NULL DEFAULT '';
`}
