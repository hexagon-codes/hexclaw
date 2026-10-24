package apihttp

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func (h *handler) registerPrintReplies(mux *http.ServeMux) {
	mux.HandleFunc("GET /print-replies/{message_id}", h.getPrintReply)
	mux.HandleFunc("POST /print-replies/{message_id}/resume", h.resumePrintReply)
}

func (h *handler) printReplyScope(w http.ResponseWriter, r *http.Request) (k12.ConversationPrintReply, bool) {
	agent, id := strings.TrimSpace(r.URL.Query().Get("agent")), strings.TrimSpace(r.PathValue("message_id"))
	if agent == "" || id == "" {
		writeErr(w, http.StatusBadRequest, "agent and message_id are required")
		return k12.ConversationPrintReply{}, false
	}
	if _, err := h.authorizedAgentOwnerScope(r.Context(), agent); err != nil {
		writeErr(w, http.StatusNotFound, "print reply not found")
		return k12.ConversationPrintReply{}, false
	}
	source, err := h.rt.Records.GetConversationPrintReply(r.Context(), agent, id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, records.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, "print reply source unavailable")
		return source, false
	}
	return source, true
}

func (h *handler) getPrintReply(w http.ResponseWriter, r *http.Request) {
	source, ok := h.printReplyScope(w, r)
	if !ok {
		return
	}
	view, err := h.rt.Deps.GetConversationPrintReply(r.Context(), source)
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusInternalServerError), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *handler) resumePrintReply(w http.ResponseWriter, r *http.Request) {
	source, ok := h.printReplyScope(w, r)
	if !ok {
		return
	}
	batch, err := h.rt.Deps.SendConversationPrintReply(r.Context(), source)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"batch": batch, "error": err.Error(), "model_invocations_added": 0})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batch": batch, "model_invocations_added": 0})
}
