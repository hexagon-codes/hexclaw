package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/render"
)

func (s *Server) SetDocumentFlow(flow *documentflow.Service) { s.documentFlow = flow }

func (s *Server) handleDocumentArtifactContent(w http.ResponseWriter, r *http.Request) {
	if s.documentFlow == nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.documentFlow.Get(r.Context(), httpPrincipalFromRequest(r).userID, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.MIME)
	w.Header().Set("Content-Disposition", render.ContentDispositionHeader(a.Name, a.Format.Extension()))
	w.Header().Set("ETag", `"`+a.ByteDigest+`"`)
	http.ServeContent(w, r, a.Name, a.CreatedAt, bytes.NewReader(a.Payload))
}

func (s *Server) handleResumeDocumentArtifact(w http.ResponseWriter, r *http.Request) {
	if s.documentFlow == nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.documentFlow.Resume(r.Context(), httpPrincipalFromRequest(r).userID, r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"artifact_id": a.ID, "state": a.State, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(documentflow.ReferenceFor(a))
}
