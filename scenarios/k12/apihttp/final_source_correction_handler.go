package apihttp

import (
	"net/http"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func (h *handler) correctCompletedSource(w http.ResponseWriter, r *http.Request) {
	if h.rt.Grading == nil {
		writeErr(w, http.StatusServiceUnavailable, "grading runtime unavailable")
		return
	}
	var req usecase.FinalSourceCorrectionInput
	if !decodeStrict(w, r, &req) {
		return
	}
	owner, ok := h.authorizeImageTaskDispatch(w, r, req.Agent, r.PathValue("id"))
	if !ok {
		return
	}
	result, err := h.rt.Grading.CorrectCompletedSource(r.Context(), owner, r.PathValue("id"), r.PathValue("problem_id"), req)
	if err != nil {
		writeJSON(w, httpStatusForK12Error(err, http.StatusConflict), struct {
			Error    string                                 `json:"error"`
			Recovery *usecase.FinalSourceCorrectionRecovery `json:"recovery,omitempty"`
		}{Error: err.Error(), Recovery: result.Recovery})
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}
