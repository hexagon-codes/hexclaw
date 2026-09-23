package apihttp

import (
	"database/sql"
	"errors"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"net/http"
)

func (h *handler) materialPreparation(w http.ResponseWriter, r *http.Request) {
	owner, err := h.ownerScope(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "Owner scope is unavailable")
		return
	}
	result, err := h.rt.Records.GetMaterialPreparationSummary(r.Context(), owner, r.PathValue("document_id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "Document not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Material preparation is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) materialPreparationList(w http.ResponseWriter, r *http.Request) {
	owner, err := h.ownerScope(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "Owner scope is unavailable")
		return
	}
	result := []k12storage.MaterialPreparationSummary{}
	seen := map[string]bool{}
	for _, document := range r.URL.Query()["document_id"] {
		if document == "" || seen[document] {
			continue
		}
		seen[document] = true
		item, err := h.rt.Records.GetMaterialPreparationOverview(r.Context(), owner, document)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Material preparation is unavailable")
			return
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"preparations": result})
}
