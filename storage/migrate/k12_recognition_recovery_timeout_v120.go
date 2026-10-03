package migrate

// K12RecognitionRecoveryTimeoutV120 仅冻结明确恢复目标的独立等待覆盖。
var K12RecognitionRecoveryTimeoutV120 = Migration{Version: 120, Description: "K12 识别恢复目标的有效等待预算", SQL: `
ALTER TABLE k12_recognition_recovery_authorizations ADD COLUMN source_timeout_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE k12_recognition_recovery_authorizations ADD COLUMN timeout_override_ms INTEGER NOT NULL DEFAULT 0 CHECK(timeout_override_ms IN (0,180000));
ALTER TABLE k12_model_physical_invocations ADD COLUMN effective_timeout_ms INTEGER NOT NULL DEFAULT 0 CHECK(effective_timeout_ms IN (0,180000));
CREATE TRIGGER k12_recognition_recovery_timeout_insert
BEFORE INSERT ON k12_recognition_recovery_authorizations
WHEN NEW.timeout_override_ms != 0 AND NEW.source_timeout_ms != 120000
BEGIN SELECT RAISE(ABORT,'recognition recovery timeout source mismatch'); END;
CREATE TRIGGER k12_recognition_physical_timeout_immutable
BEFORE UPDATE OF effective_timeout_ms ON k12_model_physical_invocations
WHEN NEW.effective_timeout_ms != OLD.effective_timeout_ms
BEGIN SELECT RAISE(ABORT,'recognition physical timeout is immutable'); END;
CREATE TRIGGER k12_recognition_physical_timeout_insert
BEFORE INSERT ON k12_model_physical_invocations
WHEN NEW.effective_timeout_ms != COALESCE((SELECT timeout_override_ms FROM k12_recognition_recovery_authorizations WHERE new_physical_id=NEW.physical_invocation_id AND new_parent_id=NEW.parent_invocation_id AND agent_name=NEW.agent_name AND job_id=NEW.job_id AND new_request_digest=NEW.request_digest),0)
BEGIN SELECT RAISE(ABORT,'recognition physical timeout authorization mismatch'); END;
`}
