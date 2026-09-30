package k12storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ReadVerifiedTextbookPages 读取原目录审核时的正文；文档换代不会改读新正文。
func (s *Store) ReadVerifiedTextbookPages(ctx context.Context, requested k12.VerifiedTextbookReadRequest) ([]k12.VerifiedTextbookPage, error) {
	principal, err := (TextbookScope{OwnerID: requested.OwnerID, AgentName: requested.AgentName, Subject: requested.Subject}).normalized()
	if err != nil {
		return nil, err
	}
	scope := requested.Scope
	if err := k12.ValidateTextbookGroundingScope(scope); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var catalogJSON, catalogDigest, ingestJobID string
	err = tx.QueryRowContext(ctx, `SELECT m.catalog_json,m.catalog_digest,j.ingest_job_id
		FROM k12_textbook_bindings b
		JOIN k12_textbook_manifests m ON m.manifest_id=b.textbook_manifest_id
		 AND m.owner_id=b.owner_id AND m.subject=b.subject
		 AND m.document_id=b.document_id AND m.document_generation=b.document_generation
		JOIN k12_textbook_catalog_jobs j ON j.manifest_id=m.manifest_id
		 AND j.owner_id=m.owner_id AND j.document_id=m.document_id
		 AND j.document_generation=m.document_generation AND j.source_digest=m.source_digest
		JOIN kb_documents d ON d.id=m.document_id AND d.deleted=0
		JOIN kb_semantic_document_bindings kb ON kb.document_id=d.id
		 AND kb.owner_id=m.owner_id AND kb.lifecycle_state='active'
		WHERE b.textbook_binding_id=? AND b.owner_id=? AND b.agent_name=? AND b.subject=?
		 AND m.manifest_id=? AND m.document_id=? AND m.document_generation=?
		 AND m.source_digest=? AND j.state='succeeded'`,
		scope.TextbookBindingID, principal.OwnerID, principal.AgentName, principal.Subject,
		scope.TextbookManifestID, scope.DocumentID, scope.DocumentGeneration, scope.SourceDigest,
	).Scan(&catalogJSON, &catalogDigest, &ingestJobID)
	if err != nil {
		return nil, fmt.Errorf("k12storage: verified textbook source unavailable: %w", err)
	}
	if ingestJobID == "" {
		return nil, fmt.Errorf("%w: verified textbook ingest source missing", records.ErrIllegalTransition)
	}
	if sha256Hex([]byte(catalogJSON)) != catalogDigest {
		return nil, fmt.Errorf("%w: verified textbook catalog changed", records.ErrIllegalTransition)
	}
	catalog, err := decodeTextbookCatalog(catalogJSON)
	if err != nil {
		return nil, err
	}
	expectedPages, expectedSegments, err := decodeGroundingCatalogExactSet(catalogJSON, catalog)
	if err != nil {
		return nil, err
	}
	if catalog.TextbookEdition != scope.Edition || catalog.Volume != scope.Volume ||
		!sameGroundingExactSet(expectedPages, expectedSegments, scope.PageRefs, scope.SegmentRefs) {
		return nil, fmt.Errorf("%w: verified textbook scope differs from frozen catalog", records.ErrIllegalTransition)
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.logical_page,m.pdf_page,m.evidence_page,
		m.evidence_offset_start,m.evidence_offset_end,m.evidence_digest,m.method,
		m.verification_state,m.document_id,m.document_generation,m.source_digest,
		p.content,p.content_digest
		FROM k12_textbook_page_mappings m
		LEFT JOIN kb_ingest_page_checkpoints p ON p.job_id=?
		 AND p.source_digest=m.source_digest AND p.page_number=m.pdf_page
		WHERE m.manifest_id=? ORDER BY m.logical_page,m.pdf_page`, ingestJobID, scope.TextbookManifestID)
	if err != nil {
		return nil, err
	}
	pages := make([]k12.VerifiedTextbookPage, 0, len(scope.PageRefs))
	pageIndex := make(map[[2]int]int, len(scope.PageRefs))
	for rows.Next() {
		var page k12.VerifiedTextbookPage
		var evidencePage, offsetFrom, offsetTo int
		var generation int64
		var evidenceDigest, method, state, documentID, sourceDigest string
		var content, contentDigest sql.NullString
		if err := rows.Scan(&page.LogicalPage, &page.PDFPage, &evidencePage,
			&offsetFrom, &offsetTo, &evidenceDigest, &method, &state,
			&documentID, &generation, &sourceDigest, &content, &contentDigest); err != nil {
			rows.Close()
			return nil, err
		}
		if page.LogicalPage < 1 || page.PDFPage < 1 || evidencePage != page.PDFPage ||
			offsetFrom < 0 || offsetTo <= offsetFrom || !content.Valid || !contentDigest.Valid ||
			strings.TrimSpace(content.String) == "" || offsetTo > len(content.String) ||
			evidenceDigest != contentDigest.String || sha256Hex([]byte(content.String)) != contentDigest.String ||
			(method != "printed_anchor" && method != "adjacent_printed_anchors") || state != "verified" ||
			documentID != scope.DocumentID || generation != scope.DocumentGeneration || sourceDigest != scope.SourceDigest {
			rows.Close()
			return nil, fmt.Errorf("%w: verified textbook page evidence changed", records.ErrIllegalTransition)
		}
		page.Content, page.ContentDigest = content.String, contentDigest.String
		for _, unit := range catalog.Units {
			for _, lesson := range unit.Lessons {
				if lesson.PageFrom <= page.LogicalPage && page.LogicalPage <= lesson.PageTo && strings.TrimSpace(lesson.Title) != "" {
					page.LessonTitles = append(page.LessonTitles, lesson.Title)
				}
			}
		}
		pageIndex[[2]int{page.LogicalPage, page.PDFPage}] = len(pages)
		pages = append(pages, page)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT logical_page,pdf_page,segment_ref,document_id,document_generation,source_digest
		FROM k12_textbook_manifest_segments WHERE manifest_id=? ORDER BY logical_page,pdf_page,segment_ref`, scope.TextbookManifestID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var logicalPage, pdfPage int
		var generation int64
		var segmentRef, documentID, sourceDigest string
		if err := rows.Scan(&logicalPage, &pdfPage, &segmentRef, &documentID, &generation, &sourceDigest); err != nil {
			rows.Close()
			return nil, err
		}
		index, found := pageIndex[[2]int{logicalPage, pdfPage}]
		if !found || documentID != scope.DocumentID || generation != scope.DocumentGeneration || sourceDigest != scope.SourceDigest {
			rows.Close()
			return nil, fmt.Errorf("%w: verified textbook segment evidence changed", records.ErrIllegalTransition)
		}
		pages[index].SegmentRefs = append(pages[index].SegmentRefs, segmentRef)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	actualPages := make([]k12.TextbookGroundingPageRef, 0, len(pages))
	for _, page := range pages {
		actualPages = append(actualPages, k12.TextbookGroundingPageRef{LogicalPage: page.LogicalPage, PDFPage: page.PDFPage, SegmentRefs: page.SegmentRefs})
	}
	actualScope := scope
	actualScope.PageRefs = actualPages
	if k12.ValidateTextbookGroundingScope(actualScope) != nil || !sameGroundingExactSet(scope.PageRefs, scope.SegmentRefs, actualPages, expectedSegments) {
		return nil, fmt.Errorf("%w: verified textbook page set changed", records.ErrIllegalTransition)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return pages, nil
}
