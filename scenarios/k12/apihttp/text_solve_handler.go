package apihttp

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func (h *handler) registerTextSolve(mux *http.ServeMux) {
	mux.HandleFunc("GET /text-solves/{solve_run_id}", h.getTextSolve)
	mux.HandleFunc("POST /text-solves/{solve_run_id}/resume-publication", h.resumeTextSolvePublication)
}

func (h *handler) textSolveScope(w http.ResponseWriter, r *http.Request) (owner, agent, id string, ok bool) {
	agent = strings.TrimSpace(r.URL.Query().Get("agent"))
	id = strings.TrimSpace(r.PathValue("solve_run_id"))
	if agent == "" || id == "" {
		writeErr(w, http.StatusBadRequest, "agent and solve_run_id are required")
		return
	}
	var err error
	owner, err = h.authorizedTextSolveOwnerScope(r.Context(), agent)
	if err != nil {
		writeErr(w, http.StatusNotFound, "text solve not found")
		return
	}
	if h.rt.Deps.Records == nil {
		writeErr(w, http.StatusServiceUnavailable, "text solve records unavailable")
		return
	}
	return owner, agent, id, true
}

func textSolveHTTPStatus(err error) int {
	if errors.Is(err, k12storage.ErrTextSolveConflict) || errors.Is(err, k12storage.ErrTextSolveUnknown) || errors.Is(err, k12storage.ErrProblemAssetConflict) {
		return http.StatusConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	return httpStatusForK12Error(err, http.StatusInternalServerError)
}

func (h *handler) getTextSolve(w http.ResponseWriter, r *http.Request) {
	owner, agent, id, ok := h.textSolveScope(w, r)
	if !ok {
		return
	}
	f, err := h.rt.Deps.GetTextSolveFinalization(r.Context(), owner, agent, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, textSolveHTTPStatus(err), err.Error())
		return
	}
	finalized := err == nil
	run, err := h.rt.Deps.Records.GetTextSolveRun(r.Context(), owner, agent, id)
	if err != nil {
		writeErr(w, textSolveHTTPStatus(err), err.Error())
		return
	}
	calls, err := h.rt.Deps.Records.ListTextSolveInvocations(r.Context(), owner, agent, id)
	if err != nil {
		writeErr(w, textSolveHTTPStatus(err), err.Error())
		return
	}
	result := map[string]any{"run": run, "invocations": calls, "finalized": finalized, "publication_eligible": finalized && f.Publication != nil}
	if finalized {
		result["finalization"] = f
	}
	if f.AnswerSource != nil && f.AnswerSource.AdoptionID != "" {
		result["adoption_currently_valid"] = h.rt.Deps.Records.ValidateProblemAssetAdoption(r.Context(), owner, f.AnswerSource.AdoptionID) == nil
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) resumeTextSolvePublication(w http.ResponseWriter, r *http.Request) {
	owner, agent, id, ok := h.textSolveScope(w, r)
	if !ok {
		return
	}
	v, err := h.rt.Deps.ResumeTextSolvePublication(r.Context(), owner, agent, id)
	if err != nil {
		writeErr(w, textSolveHTTPStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"published": v != nil, "asset": v, "model_invocations_added": 0})
}
