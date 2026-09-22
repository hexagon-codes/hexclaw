package knowledge

import (
	"context"
	"errors"
	"os"
)

// ErrDocumentSourceDeleted 表示当前归属下存在明确的删除记录。
var ErrDocumentSourceDeleted = errors.New("knowledge: document source was deleted")

// OpenDocumentSource 打开当前归属下的原始文件；读取不创建解析或模型任务。
func (s *SemanticIndexService) OpenDocumentSource(ctx context.Context, ownerID, corpusID, documentID string) (*os.File, PersistedIngestDocument, error) {
	if s.ingestRepo == nil {
		return nil, PersistedIngestDocument{}, ErrDocumentIngestUnavailable
	}
	doc, err := s.ingestRepo.GetIngestDocument(ctx, ownerID, documentID)
	if err != nil {
		// 仅当前归属的删除墓碑能证明删除；文件缺失或未知身份仍保持 not found。
		if errors.Is(err, ErrSemanticIndexNotFound) {
			if repo, ok := s.ingestRepo.(interface {
				DocumentSourceDeleted(context.Context, string, string, string) (bool, error)
			}); ok {
				deleted, lookupErr := repo.DocumentSourceDeleted(ctx, ownerID, corpusID, documentID)
				if lookupErr != nil {
					return nil, PersistedIngestDocument{}, lookupErr
				}
				if deleted {
					return nil, PersistedIngestDocument{}, ErrDocumentSourceDeleted
				}
			}
		}
		return nil, PersistedIngestDocument{}, err
	}
	if doc.OwnerID != ownerID || doc.CorpusAlias != corpusID {
		return nil, PersistedIngestDocument{}, ErrSemanticIndexNotFound
	}
	file, err := os.Open(doc.StoragePath)
	return file, doc, err
}

func (r *SQLiteSemanticIndexRepository) DocumentSourceDeleted(ctx context.Context, ownerID, corpusID, documentID string) (bool, error) {
	var deleted bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM kb_semantic_document_bindings b
 JOIN kb_semantic_corpora c ON c.corpus_uid=b.corpus_uid AND c.owner_id=b.owner_id
 JOIN kb_documents d ON d.id=b.document_id AND d.corpus_uid=b.corpus_uid
 WHERE b.owner_id=? AND c.corpus_alias=? AND b.document_id=?
 AND b.lifecycle_state='tombstoned' AND d.deleted=1)`, ownerID, corpusID, documentID).Scan(&deleted)
	return deleted, err
}
