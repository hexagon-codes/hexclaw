package migrate

// K12TextbookCatalogExtractorRecoveryV125 保留旧目录恢复分支并接纳来源不变的 v5 恢复。
var K12TextbookCatalogExtractorRecoveryV125 = Migration{
	Version:     125,
	Description: "K12 教材目录 v5 合同受控恢复",
	SQL:         K12TextbookCatalogExtractorRecoveryV125DDL,
}

const K12TextbookCatalogExtractorRecoveryV125DDL = `
DROP TRIGGER IF EXISTS k12_textbook_catalog_source_snapshot_immutable;

CREATE TRIGGER k12_textbook_catalog_source_snapshot_immutable
BEFORE UPDATE OF ingest_job_id,source_plan_digest,extractor_contract,request_digest
ON k12_textbook_catalog_jobs
WHEN (length(OLD.ingest_job_id)>0 AND length(OLD.source_plan_digest)=64)
 AND (NEW.ingest_job_id<>OLD.ingest_job_id
      OR NEW.source_plan_digest<>OLD.source_plan_digest
      OR NEW.extractor_contract<>OLD.extractor_contract
      OR NEW.request_digest<>OLD.request_digest)
 AND NOT (
      OLD.state='failed_terminal'
      AND OLD.failure_code='catalog_evidence_incomplete'
      AND OLD.result_digest=''
      AND ((OLD.extractor_contract='checkpoint-toc-footer-v1'
            AND NEW.extractor_contract='checkpoint-toc-footer-v2')
        OR (OLD.extractor_contract='checkpoint-toc-footer-v2'
            AND NEW.extractor_contract='checkpoint-toc-footer-v3')
        OR (OLD.extractor_contract IN ('checkpoint-toc-footer-v1',
                                      'checkpoint-toc-footer-v2',
                                      'checkpoint-toc-footer-v3')
            AND NEW.extractor_contract='checkpoint-toc-footer-v4')
        OR (OLD.extractor_contract IN ('checkpoint-toc-footer-v1',
                                      'checkpoint-toc-footer-v2',
                                      'checkpoint-toc-footer-v3',
                                      'checkpoint-toc-footer-v4')
            AND NEW.extractor_contract='checkpoint-toc-footer-v5'))
      AND NEW.ingest_job_id=OLD.ingest_job_id
      AND NEW.source_plan_digest=OLD.source_plan_digest
      AND length(NEW.request_digest)=64
      AND NEW.request_digest<>OLD.request_digest
      AND NEW.state='queued'
      AND NEW.failure_code=''
      AND NEW.last_error=''
      AND NEW.result_digest=''
      AND NEW.attempt=0
      AND NEW.next_attempt_at=0
      AND NEW.lease_owner=''
      AND NEW.lease_expires_at=0
 )
BEGIN
  SELECT RAISE(ABORT, 'textbook catalog source snapshot is immutable');
END;
`
