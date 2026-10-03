package apihttp

import (
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"net/http"
)

func (h *handler) authorizeGroundingSourceRecovery(w http.ResponseWriter, r *http.Request) {
	if h.rt.Grading == nil {
		writeErr(w, http.StatusServiceUnavailable, "grading runtime unavailable")
		return
	}
	var req usecase.GroundingSourceRecoveryInput
	if !decodeStrict(w, r, &req) {
		return
	}
	if _, ok := h.authorizeImageTaskDispatch(w, r, req.Agent, r.PathValue("id")); !ok {
		return
	}
	result, err := h.rt.Grading.AuthorizeGroundingSourceRecovery(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusConflict), err.Error())
		return
	}
	if !result.Replayed {
		h.rt.Grading.StartAsync(result.JobID)
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}
