package apihttp

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func (h *handler) materialRecovery(w http.ResponseWriter, r *http.Request) {
	owner, err := h.ownerScope(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "Owner scope is unavailable")
		return
	}
	document, task := r.PathValue("document_id"), r.PathValue("task_id")
	if r.Method == http.MethodGet {
		plan, e := h.rt.Records.MaterialRecoveryPlan(r.Context(), owner, document, task)
		if e != nil {
			writeMaterialRecoveryError(w, e)
			return
		}
		writeJSON(w, http.StatusOK, plan)
		return
	}
	var body struct {
		Fingerprint string `json:"fingerprint"`
		Allow       bool   `json:"allow_possible_duplicate_charge"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		writeMaterialRecoveryError(w, k12storage.ErrMaterialRecoveryInvalid)
		return
	}
	result, err := h.rt.Records.RecoverMaterialPreparation(r.Context(), owner, document, task, r.Header.Get("Idempotency-Key"), body.Fingerprint, body.Allow)
	if err != nil {
		writeMaterialRecoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}
func writeMaterialRecoveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, http.StatusNotFound, "Material task not found")
	case errors.Is(err, k12storage.ErrMaterialRecoveryInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, k12storage.ErrMaterialRecoveryConflict), errors.Is(err, k12storage.ErrMaterialPreparationFenced), errors.Is(err, k12storage.ErrMaterialPreparationUnknown), errors.Is(err, k12storage.ErrProblemAssetEvidence):
		writeErr(w, http.StatusConflict, "Material recovery plan is no longer current")
	default:
		writeErr(w, http.StatusInternalServerError, "Material recovery is unavailable")
	}
}
