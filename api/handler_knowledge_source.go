package api

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/hexagon-codes/hexclaw/knowledge"
)

type knowledgeDocumentSourceAPI interface {
	OpenDocumentSource(context.Context, string, string, string) (*os.File, knowledge.PersistedIngestDocument, error)
}

type knowledgeDocumentRecoveryAPI interface {
	DocumentRecoveryPlan(context.Context, string, string, string) (knowledge.DocumentRecoveryPlan, error)
	RecoverDocument(context.Context, string, string, string, string, string) (knowledge.CreateDocumentResult, error)
}

func (s *Server) handleKnowledgeDocumentRecovery(w http.ResponseWriter, r *http.Request) {
	service, ok := s.semanticIndex.(knowledgeDocumentRecoveryAPI)
	if !ok {
		writeDocumentIngestError(w, knowledge.ErrDocumentIngestUnavailable)
		return
	}
	owner, id := knowledgePrincipalID(r), strings.TrimSpace(r.PathValue("id"))
	if r.Method == http.MethodGet {
		plan, err := service.DocumentRecoveryPlan(r.Context(), owner, knowledgeDefaultCorpusID, id)
		if err != nil {
			writeDocumentRetryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plan)
		return
	}
	var body struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := decodeSemanticIndexRequest(r, &body); err != nil {
		writeDocumentRetryError(w, knowledge.ErrInvalidDocumentRetry)
		return
	}
	result, err := service.RecoverDocument(r.Context(), owner, knowledgeDefaultCorpusID, id, r.Header.Get("Idempotency-Key"), body.Fingerprint)
	if err != nil {
		writeDocumentRetryError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

// handleKnowledgeDocumentSource 沿文档归属读取原字节，并核对历史引用的来源摘要。
func (s *Server) handleKnowledgeDocumentSource(w http.ResponseWriter, r *http.Request) {
	service, ok := s.semanticIndex.(knowledgeDocumentSourceAPI)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Document source unavailable"})
		return
	}
	file, doc, err := service.OpenDocumentSource(r.Context(), knowledgePrincipalID(r), knowledgeDefaultCorpusID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, knowledge.ErrDocumentSourceDeleted) {
			writeJSON(w, http.StatusGone, map[string]string{"code": "knowledge_document_deleted", "error": "File deleted. The original is no longer available."})
		} else if errors.Is(err, os.ErrNotExist) || errors.Is(err, knowledge.ErrSemanticIndexNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Document source not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to read document source"})
		}
		return
	}
	defer file.Close()
	if expected := strings.TrimSpace(r.URL.Query().Get("source_digest")); expected != "" && expected != doc.SHA256 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Document source has changed"})
		return
	}
	info, err := file.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to read document source"})
		return
	}
	w.Header().Set("Content-Type", doc.MediaType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": doc.Filename}))
	w.Header().Set("ETag", `"`+doc.SHA256+`"`)
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, doc.Filename, info.ModTime(), file)
}
