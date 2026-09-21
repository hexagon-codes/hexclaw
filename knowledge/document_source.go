package knowledge

import (
	"context"
	"os"
)

// OpenDocumentSource 打开当前归属下的原始文件；读取不创建解析或模型任务。
func (s *SemanticIndexService) OpenDocumentSource(ctx context.Context, ownerID, corpusID, documentID string) (*os.File, PersistedIngestDocument, error) {
	if s.ingestRepo == nil {
		return nil, PersistedIngestDocument{}, ErrDocumentIngestUnavailable
	}
	doc, err := s.ingestRepo.GetIngestDocument(ctx, ownerID, documentID)
	if err != nil {
		return nil, PersistedIngestDocument{}, err
	}
	if doc.OwnerID != ownerID || doc.CorpusAlias != corpusID {
		return nil, PersistedIngestDocument{}, ErrSemanticIndexNotFound
	}
	file, err := os.Open(doc.StoragePath)
	return file, doc, err
}
