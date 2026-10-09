package apihttp

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func (h *handler) registerUnitSummary(mux *http.ServeMux) {
	mux.HandleFunc("POST /unit-summary-jobs", h.startUnitSummary)
	mux.HandleFunc("GET /unit-summary-jobs/{id}", h.getUnitSummary)
	mux.HandleFunc("POST /unit-summary-jobs/{id}/resume", h.resumeUnitSummary)
	mux.HandleFunc("GET /unit-summary-documents", h.listUnitSummaryDocuments)
	mux.HandleFunc("GET /unit-summary-documents/{id}", h.getUnitSummaryDocument)
}
func (h *handler) unitSummaryOwner(w http.ResponseWriter, r *http.Request, agent string) (string, bool) {
	if h.rt.UnitSummary == nil {
		writeErr(w, http.StatusServiceUnavailable, "Unit summary service is unavailable")
		return "", false
	}
	if agent == "" {
		writeErr(w, http.StatusBadRequest, "agent is required")
		return "", false
	}
	owner, err := h.authorizedAgentOwnerScope(r.Context(), agent)
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusNotFound), "Unit summary scope is unavailable")
		return "", false
	}
	return owner, true
}
func unitSummaryError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, k12storage.ErrUnitSummaryCAS), errors.Is(err, k12storage.ErrUnitSummaryConflict):
		code = http.StatusConflict
	case errors.Is(err, records.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, usecase.ErrInvalidInput):
		code = http.StatusBadRequest
	}
	writeErr(w, code, err.Error())
}
func (h *handler) unitSummarySession(w http.ResponseWriter, r *http.Request, agent, session string) bool {
	if h.rt.AuthorizeSessionScope == nil {
		writeErr(w, http.StatusServiceUnavailable, "Session scope resolver is unavailable")
		return false
	}
	if err := h.rt.AuthorizeSessionScope(r.Context(), agent, session); err != nil {
		writeErr(w, http.StatusNotFound, "Session is unavailable in the current scope")
		return false
	}
	return true
}
func (h *handler) startUnitSummary(w http.ResponseWriter, r *http.Request) {
	var req usecase.UnitSummaryRequest
	if !decode(w, r, &req) {
		return
	}
	owner, ok := h.unitSummaryOwner(w, r, strings.TrimSpace(req.AgentName))
	if !ok {
		return
	}
	req.OwnerID = owner
	if !h.unitSummarySession(w, r, req.AgentName, req.SessionID) {
		return
	}
	// 独立HTTP命令必须引用同会话真实用户消息，不能凭请求体制造消息归属。
	text, err := h.rt.Records.ReadUnitSummaryFollowup(r.Context(), req.SessionID, req.SourceMessageID)
	if err != nil {
		unitSummaryError(w, err)
		return
	}
	if text != req.FinalUserText {
		writeErr(w, http.StatusConflict, "Final text differs from the persisted user message")
		return
	}
	view, replayed, err := h.rt.UnitSummary.Start(r.Context(), req)
	if err != nil {
		unitSummaryError(w, err)
		return
	}
	code := http.StatusAccepted
	if replayed && k12.UnitSummaryAttemptTerminal(view.Job.State) {
		code = http.StatusOK
	}
	writeJSON(w, code, view)
}
func (h *handler) getUnitSummary(w http.ResponseWriter, r *http.Request) {
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	owner, ok := h.unitSummaryOwner(w, r, agent)
	if !ok {
		return
	}
	view, err := h.rt.UnitSummary.Get(r.Context(), owner, agent, r.PathValue("id"))
	if err != nil {
		unitSummaryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
func (h *handler) resumeUnitSummary(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent string `json:"agent"`
		usecase.UnitSummaryResumeRequest
	}
	if !decode(w, r, &body) {
		return
	}
	owner, ok := h.unitSummaryOwner(w, r, strings.TrimSpace(body.Agent))
	if !ok {
		return
	}
	current, err := h.rt.UnitSummary.Get(r.Context(), owner, body.Agent, r.PathValue("id"))
	if err != nil {
		unitSummaryError(w, err)
		return
	}
	if !h.unitSummarySession(w, r, body.Agent, current.Job.SessionID) {
		return
	}
	view, replayed, err := h.rt.UnitSummary.Resume(r.Context(), owner, body.Agent, r.PathValue("id"), body.UnitSummaryResumeRequest)
	if err != nil {
		if errors.Is(err, k12storage.ErrUnitSummaryCAS) {
			latest, readErr := h.rt.UnitSummary.Get(r.Context(), owner, body.Agent, r.PathValue("id"))
			if readErr == nil {
				writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "current": latest})
				return
			}
		}
		unitSummaryError(w, err)
		return
	}
	code := http.StatusAccepted
	if replayed && k12.UnitSummaryAttemptTerminal(view.Job.State) {
		code = http.StatusOK
	}
	writeJSON(w, code, view)
}
func (h *handler) listUnitSummaryDocuments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := strings.TrimSpace(q.Get("agent"))
	owner, ok := h.unitSummaryOwner(w, r, agent)
	if !ok {
		return
	}
	if q.Get("session_id") != "" && !h.unitSummarySession(w, r, agent, q.Get("session_id")) {
		return
	}
	limit := 30
	if q.Get("limit") != "" {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			writeErr(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = n
	}
	view, err := h.rt.UnitSummary.ListDocuments(r.Context(), owner, agent, q.Get("session_id"), q.Get("cursor"), limit)
	if err != nil {
		unitSummaryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
func (h *handler) getUnitSummaryDocument(w http.ResponseWriter, r *http.Request) {
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	owner, ok := h.unitSummaryOwner(w, r, agent)
	if !ok {
		return
	}
	view, err := h.rt.UnitSummary.GetDocument(r.Context(), owner, agent, r.PathValue("id"), r.URL.Query().Get("revision_id"))
	if err != nil {
		unitSummaryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
