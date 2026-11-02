package api

import (
	"net/http"
	"sort"

	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/streamstate"
)

func (s *Server) handleListActiveStreams(w http.ResponseWriter, r *http.Request) {
	userID := sessionUserIDFromRequest(r)
	streams := []streamstate.Snapshot{}
	if s.streamStates != nil {
		for _, snapshot := range s.streamStates.ListActiveStreams(userID) {
			snapshot.Source = "websocket"
			streams = append(streams, snapshot)
		}
	}
	if s.restStreams != nil {
		for _, snapshot := range s.restStreams.ListActiveStreams(userID) {
			snapshot.Source = "rest"
			streams = append(streams, snapshot)
		}
	}
	sort.SliceStable(streams, func(i, j int) bool { return streams[i].UpdatedAt.After(streams[j].UpdatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{
		"streams": streams,
		"total":   len(streams),
	})
}

func (s *Server) handleGetStreamSnapshot(w http.ResponseWriter, r *http.Request) {
	userID := sessionUserIDFromRequest(r)
	requestID := r.PathValue("request_id")
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request_id 不能为空"})
		return
	}
	source := r.URL.Query().Get("source")
	if source != "" && source != "rest" && source != "websocket" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported stream source"})
		return
	}
	var rest, websocket *streamstate.Snapshot
	if s.restStreams != nil {
		rest, _ = s.restStreams.GetStreamSnapshot(userID, requestID)
	}
	if s.streamStates != nil {
		websocket, _ = s.streamStates.GetStreamSnapshot(userID, requestID)
	}
	if source == "" && rest != nil && websocket != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stream source is ambiguous; specify rest or websocket"})
		return
	}
	var snapshot *streamstate.Snapshot
	if source == "rest" || source == "" && rest != nil {
		snapshot = rest
		source = "rest"
	} else {
		snapshot = websocket
		source = "websocket"
	}
	if snapshot == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found"})
		return
	}
	copy := *snapshot
	copy.Source = source
	writeJSON(w, http.StatusOK, copy)
}

func (s *Server) handleCancelStream(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("request_id")
	if requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request_id is required"})
		return
	}
	if s.restStreams == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found"})
		return
	}
	snapshot, requested, found := s.restStreams.RequestCancel(sessionUserIDFromRequest(r), requestID, requestcancel.ErrUserCancelled)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stream not found"})
		return
	}
	snapshot.Source = "rest"
	status := http.StatusOK
	if requested {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"request_id": requestID, "cancel_requested": requested, "snapshot": snapshot})
}
