package apihttp

import (
	"errors"
	"net/http"

	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func (h *handler) recordReviewSourceGroup(w http.ResponseWriter, r *http.Request) {
	var request usecase.RecordReviewSourceGroupRequest
	if !decodeStrict(w, r, &request) {
		return
	}
	group, created, err := h.rt.Deps.RecordReviewSourceGroup(r.Context(), request)
	if err != nil {
		status := httpStatusForK12Error(err, http.StatusInternalServerError)
		if errors.Is(err, k12storage.ErrReviewSourceGroupStale) || errors.Is(err, k12storage.ErrReviewSourceGroupConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, usecase.ErrInvalidInput) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err.Error())
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"group": group, "created": created, "replayed": !created})
}
