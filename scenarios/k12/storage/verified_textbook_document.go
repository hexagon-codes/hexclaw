package k12storage

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// VerifiedTextbookDocumentRequest 只定位本次明确材料的当前来源，不表达或创建孩子教材绑定。
type VerifiedTextbookDocumentRequest struct {
	OwnerID            string
	AgentName          string
	DocumentID         string
	DocumentGeneration int64
	SourceDigest       string
}

type VerifiedTextbookDocument struct {
	Catalog k12.CurriculumCatalog
	Pages   []k12.VerifiedTextbookPage
}

// ReadVerifiedTextbookDocument 按同归属文档与代次只读目录和原页证明；没有已验证目录时返回 NotFound。
// 已映射但正文不存在的页保持为空，调用方必须标明实际缺页；摘要冲突不是部分覆盖。
func (s *Store) ReadVerifiedTextbookDocument(ctx context.Context, requested VerifiedTextbookDocumentRequest) (VerifiedTextbookDocument, error) {
	return s.readVerifiedTextbookDocument(ctx, requested, true)
}

// GetVerifiedTextbookDocumentCatalog 只提供候选目录；正文证明在最终选定该材料时读取。
func (s *Store) GetVerifiedTextbookDocumentCatalog(ctx context.Context, requested VerifiedTextbookDocumentRequest) (k12.CurriculumCatalog, error) {
	source, err := s.readVerifiedTextbookDocument(ctx, requested, false)
	return source.Catalog, err
}

func (s *Store) readVerifiedTextbookDocument(ctx context.Context, requested VerifiedTextbookDocumentRequest, withContent bool) (VerifiedTextbookDocument, error) {
	principal, err := (TextbookScope{OwnerID: requested.OwnerID, AgentName: requested.AgentName, Subject: "math"}).normalized()
	if err != nil {
		return VerifiedTextbookDocument{}, err
	}
	if requested.DocumentID == "" || requested.DocumentID != strings.TrimSpace(requested.DocumentID) || requested.DocumentGeneration <= 0 || len(requested.SourceDigest) != 64 {
		return VerifiedTextbookDocument{}, fmt.Errorf("invalid verified textbook document identity")
	}
	if _, err := hex.DecodeString(requested.SourceDigest); err != nil {
		return VerifiedTextbookDocument{}, fmt.Errorf("invalid verified textbook document source digest")
	}
	if err := requireTextbookAgent(ctx, s.db, principal.AgentName); err != nil {
		return VerifiedTextbookDocument{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return VerifiedTextbookDocument{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT m.manifest_id,m.catalog_json,m.catalog_digest,j.ingest_job_id
		FROM k12_textbook_manifests m
		JOIN k12_textbook_catalog_jobs j ON j.manifest_id=m.manifest_id
		 AND j.owner_id=m.owner_id AND j.document_id=m.document_id
		 AND j.document_generation=m.document_generation AND j.source_digest=m.source_digest
		JOIN kb_documents d ON d.id=m.document_id AND d.deleted=0
		JOIN kb_semantic_document_bindings b ON b.document_id=m.document_id
		 AND b.owner_id=m.owner_id AND b.content_generation=m.document_generation
		 AND b.lifecycle_state='active' AND b.text_state='ready'
		JOIN kb_ingest_document_sources source ON source.document_id=m.document_id
		 AND source.owner_id=m.owner_id AND source.content_generation=m.document_generation
		 AND source.blob_sha256=m.source_digest
		WHERE m.owner_id=? AND m.subject='math' AND m.document_id=?
		 AND m.document_generation=? AND m.source_digest=?
		 AND m.state='ready_for_confirmation' AND j.state='succeeded'
		 AND (source.agent_id='' OR source.agent_id=?)
		ORDER BY m.manifest_id`, principal.OwnerID, requested.DocumentID, requested.DocumentGeneration, requested.SourceDigest, principal.AgentName)
	if err != nil {
		return VerifiedTextbookDocument{}, err
	}
	var manifestID, catalogJSON, catalogDigest, ingestJobID string
	if !rows.Next() {
		err := rows.Err()
		rows.Close()
		if err != nil {
			return VerifiedTextbookDocument{}, err
		}
		return VerifiedTextbookDocument{}, records.ErrNotFound
	}
	if err := rows.Scan(&manifestID, &catalogJSON, &catalogDigest, &ingestJobID); err != nil {
		rows.Close()
		return VerifiedTextbookDocument{}, err
	}
	if rows.Next() {
		rows.Close()
		return VerifiedTextbookDocument{}, fmt.Errorf("%w: verified textbook document catalog is ambiguous", records.ErrIllegalTransition)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return VerifiedTextbookDocument{}, err
	}
	rows.Close()
	catalog, pages, err := readVerifiedTextbookPageProof(ctx, tx, verifiedTextbookDocumentIdentity{ManifestID: manifestID, DocumentID: requested.DocumentID, Generation: requested.DocumentGeneration, SourceDigest: requested.SourceDigest}, catalogJSON, catalogDigest, ingestJobID, withContent)
	if err != nil {
		return VerifiedTextbookDocument{}, err
	}
	if catalog.Subject != "math" {
		return VerifiedTextbookDocument{}, fmt.Errorf("%w: verified textbook catalog subject changed", records.ErrIllegalTransition)
	}
	if err := tx.Commit(); err != nil {
		return VerifiedTextbookDocument{}, err
	}
	catalog.TextbookManifestID, catalog.AgentName = manifestID, principal.AgentName
	if catalog.GradeTerm == "" {
		catalog.GradeTerm, err = s.textbookCoverGradeTerm(ctx, principal.OwnerID, manifestID)
		if err != nil {
			return VerifiedTextbookDocument{}, err
		}
	}
	return VerifiedTextbookDocument{Catalog: catalog, Pages: pages}, nil
}
