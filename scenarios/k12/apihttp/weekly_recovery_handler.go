package apihttp

import (
	"net/http"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func (h *handler) recoverWeeklyTextbookTrack(w http.ResponseWriter, r *http.Request) {
	var req usecase.WeeklyTextbookRecoveryRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	agentName, err := h.resolveWeeklyPlanAgent(r)
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusNotFound), err.Error())
		return
	}
	plan, replay, err := h.rt.Deps.RecoverWeeklyTextbookTrack(r.Context(), agentName, r.PathValue("id"), req)
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusConflict), err.Error())
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"plan": plan, "replayed": replay})
}

func (h *handler) reinterpretWeeklyTextbookTrack(w http.ResponseWriter, r *http.Request) {
	var req usecase.WeeklyTextbookReinterpretationRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	agentName, err := h.resolveWeeklyPlanAgent(r)
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusNotFound), err.Error())
		return
	}
	plan, replay, err := h.rt.Deps.ReinterpretWeeklyTextbookTrack(r.Context(), agentName, r.PathValue("id"), req)
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusConflict), err.Error())
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"plan": plan, "replayed": replay})
}
