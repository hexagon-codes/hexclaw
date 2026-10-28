package apihttp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func (h *handler) registerDocumentRecovery(mux *http.ServeMux) {
	mux.HandleFunc("GET /final-replies/{message_id}", h.getFinalReplyPreparation)
	mux.HandleFunc("POST /final-replies/{message_id}/resume", h.resumeFinalReplyPreparation)
	mux.HandleFunc("POST /final-replies/{message_id}/reexport", h.reexportFinalReply)
	mux.HandleFunc("POST /print-artifacts/{id}/reexport", h.reexportPrintableArtifact)
}

func (h *handler) finalReplyPreparationScope(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	agent, id := strings.TrimSpace(r.URL.Query().Get("agent")), strings.TrimSpace(r.PathValue("message_id"))
	if agent == "" || id == "" {
		writeErr(w, http.StatusBadRequest, "agent and message_id are required")
		return "", "", false
	}
	owner, err := h.authorizedTextSolveOwnerScope(r.Context(), agent)
	if err != nil {
		writeErr(w, http.StatusNotFound, "final reply not found")
		return "", "", false
	}
	event, err := h.rt.Records.GetFinalReplyPreparation(r.Context(), agent, id, "")
	if err != nil {
		writeErr(w, http.StatusNotFound, "final reply not found")
		return "", "", false
	}
	var payload struct {
		Request usecase.FinalReplyDeliveryRequest `json:"request"`
	}
	if json.Unmarshal([]byte(event.Payload), &payload) != nil || payload.Request.PrivateEvent == nil ||
		(strings.TrimSpace(h.rt.PrincipalMode) == "remote" && owner != h.rt.Deps.TextbookOwnerID && owner != payload.Request.PrivateEvent.SenderID) {
		writeErr(w, http.StatusNotFound, "final reply not found")
		return "", "", false
	}
	return agent, id, true
}

func (h *handler) getFinalReplyPreparation(w http.ResponseWriter, r *http.Request) {
	agent, id, ok := h.finalReplyPreparationScope(w, r)
	if !ok {
		return
	}
	event, err := h.rt.Records.GetFinalReplyPreparation(r.Context(), agent, id, "")
	if err != nil {
		writeErr(w, http.StatusNotFound, "final reply not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preparation_id": event.EventID, "state": event.Status, "failure": event.LastError, "model_invocations_added": 0})
}

func (h *handler) resumeFinalReplyPreparation(w http.ResponseWriter, r *http.Request) {
	agent, id, ok := h.finalReplyPreparationScope(w, r)
	if !ok {
		return
	}
	batch, _, err := h.rt.Deps.ResumeFinalReplyPreparation(r.Context(), agent, id)
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	out := map[string]any{"batch": batch, "model_invocations_added": 0}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, status, out)
}

func (h *handler) reexportFinalReply(w http.ResponseWriter, r *http.Request) {
	agent, id, ok := h.finalReplyPreparationScope(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeErr(w, http.StatusBadRequest, "Idempotency-Key is required")
		return
	}
	batch, _, err := h.rt.Deps.ReexportFinalReply(r.Context(), agent, id, key)
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	out := map[string]any{"batch": batch, "model_invocations_added": 0}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, status, out)
}

func (h *handler) reexportPrintableArtifact(w http.ResponseWriter, r *http.Request) {
	agent, id, key := strings.TrimSpace(r.URL.Query().Get("agent")), strings.TrimSpace(r.PathValue("id")), strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if agent == "" || id == "" || key == "" {
		writeErr(w, http.StatusBadRequest, "agent, artifact id and Idempotency-Key are required")
		return
	}
	original, err := h.rt.Deps.GetPrintableArtifactPDF(r.Context(), agent, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "print artifact not found")
		return
	}
	if original.Artifact.SourceKind == k12.PrintSourceTextSolveFinalization {
		owner, scopeErr := h.authorizedTextSolveOwnerScope(r.Context(), agent)
		source, sourceErr := h.rt.Records.TextSolvePrintArtifactSource(r.Context(), original.Artifact)
		if scopeErr != nil || sourceErr != nil || source.OwnerID != owner {
			writeErr(w, http.StatusNotFound, "print artifact not found")
			return
		}
	} else if _, err = h.authorizedAgentOwnerScope(r.Context(), agent); err != nil {
		writeErr(w, http.StatusNotFound, "print artifact not found")
		return
	}
	view, replay, err := h.rt.Deps.ReexportPrintableArtifact(r.Context(), agent, id, key)
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusConflict), err.Error())
		return
	}
	out := map[string]any{"artifact": printableArtifactDTO(view), "replayed": replay, "model_invocations_added": 0}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	if r.URL.Query().Get("send") == "true" {
		batch, _, sendErr := h.rt.Deps.PrepareAndSendMessageBatch(r.Context(), agent, "print_artifact_revision", view.Artifact.ArtifactID, usecase.DeliveryMessage{Content: view.Artifact.Title, Attachments: []usecase.DeliveryAttachment{{Name: view.Artifact.Title + ".pdf", MIME: "application/pdf", Data: view.Render.Payload}}})
		out["batch"] = batch
		if sendErr != nil {
			status = http.StatusBadGateway
			out["error"] = sendErr.Error()
		}
	}
	writeJSON(w, status, out)
}
