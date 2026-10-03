package migrate

// K12ProblemAssetCandidatesV114 保存可由不可变题目事实重建的候选索引。
var K12ProblemAssetCandidatesV114 = Migration{
	Version: 114, Description: "K12 同题资产 FTS 候选索引",
	SQL: `
CREATE VIRTUAL TABLE k12_problem_asset_candidates_fts USING fts5(
    stem, owner_id UNINDEXED, asset_id UNINDEXED, asset_version UNINDEXED
);
INSERT INTO k12_problem_asset_candidates_fts(stem,owner_id,asset_id,asset_version)
SELECT json_extract(facts_json,'$.stem'),owner_id,asset_id,asset_version FROM k12_problem_asset_versions;
CREATE TRIGGER k12_problem_asset_candidates_insert AFTER INSERT ON k12_problem_asset_versions BEGIN
    INSERT INTO k12_problem_asset_candidates_fts(stem,owner_id,asset_id,asset_version)
    VALUES(json_extract(NEW.facts_json,'$.stem'),NEW.owner_id,NEW.asset_id,NEW.asset_version);
END;
CREATE TRIGGER k12_problem_asset_candidates_delete AFTER DELETE ON k12_problem_asset_versions BEGIN
    DELETE FROM k12_problem_asset_candidates_fts WHERE owner_id=OLD.owner_id AND asset_id=OLD.asset_id AND asset_version=OLD.asset_version;
END;
`,
}
