package migrate

// K12TeachingReportV138 在原终稿事务内保存关联报告快照，不另建任务或求解账本。
var K12TeachingReportV138 = Migration{Version: 138, Description: "K12 immutable teaching report snapshots", SQL: `
CREATE TABLE IF NOT EXISTS k12_grading_final_artifact_reports (
 artifact_id TEXT PRIMARY KEY REFERENCES k12_grading_final_artifacts(artifact_id) ON DELETE CASCADE,
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json))
);
CREATE TRIGGER IF NOT EXISTS k12_grading_report_immutable BEFORE UPDATE ON k12_grading_final_artifact_reports
 BEGIN SELECT RAISE(ABORT,'teaching report snapshot is immutable'); END;
`}
