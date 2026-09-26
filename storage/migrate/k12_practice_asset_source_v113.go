package migrate

// K12PracticeAssetSourceV113 保留每次练习采用来源，历史打印与学生作答不回写资产。
var K12PracticeAssetSourceV113 = Migration{
	Version: 113, Description: "K12 练习资产来源快照",
	SQL: `ALTER TABLE k12_practice_set_items ADD COLUMN asset_source_json TEXT
    CHECK(asset_source_json IS NULL OR (json_valid(asset_source_json) AND json_type(asset_source_json)='object'));`,
}
