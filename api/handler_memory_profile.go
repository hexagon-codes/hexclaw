package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/hexagon-codes/hexclaw/memory"
)

type memoryProfileEngine interface {
	RefreshMemoryProfile(context.Context) (string, error)
	EditMemoryProfile(context.Context, string, string) error
}

func profileError(w http.ResponseWriter, err error) {
	code := http.StatusUnprocessableEntity
	if errors.Is(err, memory.ErrProfileStale) || errors.Is(err, memory.ErrProfileOutcomeUnknown) {
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) handleRefreshMemoryProfile(w http.ResponseWriter, r *http.Request) {
	engine, ok := s.engine.(memoryProfileEngine)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Profile maintenance is unavailable"})
		return
	}
	action, err := engine.RefreshMemoryProfile(r.Context())
	if err != nil {
		profileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"action": action})
}

func (s *Server) handleEditMemoryProfile(w http.ResponseWriter, r *http.Request) {
	engine, ok := s.engine.(memoryProfileEngine)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Profile maintenance is unavailable"})
		return
	}
	var request struct {
		Revision string `json:"revision"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid profile request"})
		return
	}
	if err := engine.EditMemoryProfile(r.Context(), request.Revision, request.Content); err != nil {
		profileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Profile and source memories updated"})
}
