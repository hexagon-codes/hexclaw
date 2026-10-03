package apihttp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type answerFeedbackRequest struct {
	Kind          string `json:"kind"`
	JobID         string `json:"job_id"`
	InputRevision int    `json:"input_revision"`
	ResultDigest  string `json:"result_digest"`
	Reason        string `json:"reason"`
}

// answerFeedbackScope 沿现有逐题资源解析归属，不接受请求体自报孩子或 owner。
func (h *handler) answerFeedbackScope(w http.ResponseWriter, r *http.Request) (string, k12storage.ProblemSourceActionAssetScope, bool) {
	owner, err := h.ownerScope(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "authenticated image task principal required")
		return "", k12storage.ProblemSourceActionAssetScope{}, false
	}
	if h.rt.Records == nil {
		writeErr(w, http.StatusServiceUnavailable, "image task records unavailable")
		return "", k12storage.ProblemSourceActionAssetScope{}, false
	}
	scope, err := h.rt.Records.GetProblemSourceActionAssetScope(r.Context(), r.PathValue("dispatch_id"), r.PathValue("problem_id"))
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusNotFound), "problem assessment not found")
		return "", scope, false
	}
	durableOwner, err := h.rt.Records.GetImageTaskOwnerScope(r.Context(), scope.AgentName, r.PathValue("dispatch_id"))
	if err != nil || durableOwner != owner {
		writeErr(w, http.StatusNotFound, "problem assessment not found")
		return "", scope, false
	}
	if strings.TrimSpace(h.rt.PrincipalMode) == "remote" && (h.rt.AuthorizeAgentScope == nil || h.rt.AuthorizeAgentScope(r.Context(), owner, scope.AgentName) != nil) {
		writeErr(w, http.StatusForbidden, "answer feedback forbidden")
		return "", scope, false
	}
	return owner, scope, true
}

func (h *handler) createProblemAnswerFeedback(w http.ResponseWriter, r *http.Request) {
	var request answerFeedbackRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	requestID := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if requestID == "" || request.Kind != "answer_error" || request.JobID == "" || request.InputRevision < 1 || request.ResultDigest == "" || strings.TrimSpace(request.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "explicit answer_error, original assessment, reason and Idempotency-Key required")
		return
	}
	owner, scope, ok := h.answerFeedbackScope(w, r)
	if !ok {
		return
	}
	if request.JobID != scope.JobID {
		writeErr(w, http.StatusConflict, "answer feedback job mismatch")
		return
	}
	feedback, created, err := h.rt.Records.AcceptProblemAssetFeedback(r.Context(), k12storage.ProblemAssetFeedbackCommand{
		RequestID: requestID, OwnerID: owner, AgentName: scope.AgentName, DispatchID: r.PathValue("dispatch_id"), JobID: request.JobID, ProblemID: r.PathValue("problem_id"),
		InputRevision: request.InputRevision, ResultDigest: request.ResultDigest, Kind: request.Kind, Reason: request.Reason,
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, k12storage.ErrProblemAssetFeedbackConflict) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"created": created, "feedback": feedback})
}

// getProblemAnswerFeedbackContext 返回原评估身份与独立的有效纠正，供命令调用者冻结请求前提。
func (h *handler) getProblemAnswerFeedbackContext(w http.ResponseWriter, r *http.Request) {
	_, scope, ok := h.answerFeedbackScope(w, r)
	if !ok {
		return
	}
	view, err := h.rt.Records.GetEffectiveGradingAssessment(r.Context(), scope.AgentName, scope.JobID, r.PathValue("problem_id"))
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusNotFound), "problem assessment not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job_id": scope.JobID, "input_revision": view.Original.InputRevision,
		"result_digest": view.Original.ResultDigest, "assessment": view,
	})
}

func (h *handler) getProblemAnswerFeedback(w http.ResponseWriter, r *http.Request) {
	owner, scope, ok := h.answerFeedbackScope(w, r)
	if !ok {
		return
	}
	feedback, err := h.rt.Records.GetProblemAssetFeedback(r.Context(), owner, r.PathValue("feedback_id"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, records.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	if feedback.Command.AgentName != scope.AgentName || feedback.Command.DispatchID != r.PathValue("dispatch_id") || feedback.Command.ProblemID != r.PathValue("problem_id") {
		writeErr(w, http.StatusNotFound, "answer feedback not found")
		return
	}
	writeJSON(w, http.StatusOK, feedback)
}
