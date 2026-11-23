package migrate

// IMPhotoIntakesV152 keeps channel-level receipt, notice and staged input separate from K12 tasks.
var IMPhotoIntakesV152 = Migration{
	Version:     152,
	Description: "Persist grouped IM photo intake and first-notice delivery",
	SQL: `
CREATE TABLE im_photo_intakes (
    id TEXT PRIMARY KEY NOT NULL,
    platform TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    provider_message_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    message_json TEXT NOT NULL CHECK(json_valid(message_json)),
    route_json TEXT NOT NULL CHECK(json_valid(route_json)),
    page_count INTEGER NOT NULL CHECK(page_count > 0),
    platform_created_at_ms INTEGER NOT NULL DEFAULT 0,
    received_at_ms INTEGER NOT NULL,
    committed_at_ms INTEGER NOT NULL,
    assets_ready_at_ms INTEGER NOT NULL DEFAULT 0,
    notice_json TEXT NOT NULL DEFAULT '{"status":"pending"}' CHECK(json_valid(notice_json)),
    work_json TEXT NOT NULL DEFAULT '{"status":"pending"}' CHECK(json_valid(work_json)),
    final_json TEXT NOT NULL DEFAULT '{"status":"pending"}' CHECK(json_valid(final_json)),
    recoverable INTEGER GENERATED ALWAYS AS (
        json_extract(notice_json,'$.status') IN ('pending','sending','accepted','recalling')
        OR (json_extract(notice_json,'$.status')='unknown' AND coalesce(json_extract(notice_json,'$.external_id'),'')<>'')
        OR (assets_ready_at_ms>0 AND json_extract(work_json,'$.status') IN ('pending','running','unknown'))
        OR (json_extract(work_json,'$.status')='completed' AND json_extract(final_json,'$.status') IN ('pending','sending','accepted','delegated'))
        OR (json_extract(final_json,'$.status')='unknown' AND coalesce(json_extract(final_json,'$.external_id'),'')<>'')
        OR (json_extract(notice_json,'$.status')='delivered' AND json_extract(final_json,'$.status') IN ('delivered','failed'))
    ) VIRTUAL,
    UNIQUE(platform,instance_id,chat_id,provider_message_id)
);
CREATE INDEX idx_im_photo_intakes_recovery ON im_photo_intakes(instance_id,id) WHERE recoverable=1;
CREATE TABLE im_photo_intake_pages (
    intake_id TEXT NOT NULL REFERENCES im_photo_intakes(id) ON DELETE CASCADE,
    page_index INTEGER NOT NULL CHECK(page_index >= 0),
    signature TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    mime TEXT NOT NULL DEFAULT '',
    byte_size INTEGER NOT NULL DEFAULT 0,
    digest TEXT NOT NULL DEFAULT '',
    data BLOB,
    started_at_ms INTEGER NOT NULL DEFAULT 0,
    downloaded_at_ms INTEGER NOT NULL DEFAULT 0,
    stored_at_ms INTEGER NOT NULL DEFAULT 0,
    failure_kind TEXT NOT NULL DEFAULT '',
    failed_at_ms INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(intake_id,page_index),
    CHECK((digest='' AND byte_size=0 AND data IS NULL AND stored_at_ms=0)
        OR (digest<>'' AND byte_size>0 AND stored_at_ms>0 AND (data IS NULL OR length(data)=byte_size)))
);
CREATE TRIGGER im_photo_intakes_release_handed_off_pages AFTER UPDATE OF work_json ON im_photo_intakes
WHEN json_extract(NEW.work_json,'$.status')='completed' AND json_extract(OLD.work_json,'$.status')<>'completed'
BEGIN
    UPDATE im_photo_intake_pages SET data=NULL WHERE intake_id=NEW.id AND data IS NOT NULL;
END;`,
}
