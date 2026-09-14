package migrate

// K12PracticeReturnAutoMatchV100 将冻结候选与照片实际覆盖分开保存，历史批次维持显式映射。
var K12PracticeReturnAutoMatchV100 = Migration{
	Version:     100,
	Description: "Persist automatic practice return matching candidates",
	SQL: `ALTER TABLE k12_practice_return_assets ADD COLUMN auto_match INTEGER NOT NULL DEFAULT 0;
ALTER TABLE k12_practice_return_assets ADD COLUMN candidate_item_ids_json TEXT NOT NULL DEFAULT '[]';`,
}
