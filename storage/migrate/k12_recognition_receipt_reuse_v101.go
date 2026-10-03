package migrate

// K12RecognitionReceiptReuseV101 区分真实模型请求与跨重试的本地成功回执复用。
var K12RecognitionReceiptReuseV101 = Migration{
	Version:     101,
	Description: "Preserve recognition receipt reuse provenance",
	SQL: `ALTER TABLE k12_model_physical_invocations
ADD COLUMN reused_from_physical_invocation_id TEXT NOT NULL DEFAULT '';

CREATE TRIGGER k12_model_physical_invocation_reuse_once
BEFORE UPDATE OF reused_from_physical_invocation_id ON k12_model_physical_invocations
WHEN NOT (
    NEW.reused_from_physical_invocation_id=OLD.reused_from_physical_invocation_id
    OR (OLD.status='prepared' AND OLD.reused_from_physical_invocation_id=''
        AND NEW.status='succeeded' AND NEW.external_request_id=''
        AND EXISTS (
            SELECT 1 FROM k12_model_physical_invocations source
            WHERE source.physical_invocation_id=NEW.reused_from_physical_invocation_id
              AND source.agent_name=NEW.agent_name AND source.job_id=NEW.job_id
              AND source.parent_invocation_id!=NEW.parent_invocation_id
              AND source.physical_unit=NEW.physical_unit AND source.status='succeeded'
              AND source.result_digest=NEW.result_digest
              AND source.result_content=NEW.result_content
        ))
)
BEGIN
    SELECT RAISE(ABORT, 'recognition reuse provenance is immutable');
END;

DROP TRIGGER k12_recognition_layout_plan_status_guard;
CREATE TRIGGER k12_recognition_layout_plan_status_guard
BEFORE UPDATE OF status ON k12_recognition_layout_plans
WHEN NEW.status!=OLD.status AND NOT (
    (OLD.status='prepared_manifest' AND NEW.status IN ('manifest_sent','failed','cancelled'))
    OR (OLD.status='prepared_manifest' AND NEW.status='manifest_succeeded'
        AND EXISTS (
            SELECT 1 FROM k12_model_physical_invocations child
            WHERE child.physical_invocation_id=NEW.manifest_physical_invocation_id
              AND child.parent_invocation_id=NEW.parent_invocation_id
              AND child.agent_name=NEW.agent_name AND child.status='succeeded'
              AND child.reused_from_physical_invocation_id!=''
              AND child.result_digest=NEW.manifest_result_digest
        ))
    OR (OLD.status='manifest_sent' AND NEW.status IN (
        'manifest_succeeded','failed','outcome_unknown','cancelled'
    ))
    OR (OLD.status='manifest_succeeded' AND NEW.status IN ('authorized','failed','cancelled'))
    OR (OLD.status='authorized' AND NEW.status IN ('running','failed','cancelled'))
    OR (OLD.status='running' AND NEW.status IN (
        'succeeded','failed','outcome_unknown','cancelled'
    ))
)
BEGIN
    SELECT RAISE(ABORT, 'invalid recognition layout plan status transition');
END;`,
}
