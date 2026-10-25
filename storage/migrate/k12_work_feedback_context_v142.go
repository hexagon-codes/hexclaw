package migrate

// K12WorkFeedbackContextV142 允许未发送的作品点评首次填充请求快照，冻结后仍不可改写。
var K12WorkFeedbackContextV142 = Migration{Version: 142, Description: "Freeze work feedback request context before send", SQL: `
DROP TRIGGER IF EXISTS k12_work_feedback_generation_identity_immutable;
CREATE TRIGGER k12_work_feedback_generation_identity_immutable
BEFORE UPDATE OF generation_id, work_id, agent_name, generation_no, command_key,
                 request_digest, feedback_type, source_snapshot_json, route_snapshot_json
ON k12_work_feedback_generations
BEGIN
    SELECT RAISE(ABORT, 'work feedback generation identity is immutable');
END;

CREATE TRIGGER k12_work_feedback_request_snapshot_immutable
BEFORE UPDATE OF request_snapshot_json ON k12_work_feedback_generations
WHEN NEW.request_snapshot_json IS NOT OLD.request_snapshot_json
 AND NOT COALESCE((
    OLD.request_snapshot_json IN ('','{}')
    AND OLD.status IN ('queued','running')
    AND NEW.status=OLD.status
    AND json_valid(NEW.request_snapshot_json)
    AND json_extract(NEW.request_snapshot_json,'$.frozen_context.version')=1
    AND json_type(NEW.request_snapshot_json,'$.frozen_context.prompt')='text'
    AND length(json_extract(NEW.request_snapshot_json,'$.frozen_context.prompt'))>0
    AND NOT EXISTS (
        SELECT 1 FROM k12_image_task_invocations i
        WHERE i.agent_name=OLD.agent_name AND i.work_record_id=OLD.work_id
          AND i.operation='work_feedback'
          AND i.operation_key='work:'||OLD.work_id||':version:'||OLD.generation_id||':feedback'
    )
 ),0)
BEGIN
    SELECT RAISE(ABORT, 'work feedback request snapshot is immutable');
END;`}
