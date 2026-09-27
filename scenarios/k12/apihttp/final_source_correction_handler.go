package apihttp

import (
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"net/http"
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
		writeErr(w, httpStatusForK12Error(err, http.StatusConflict), err.Error())
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}
