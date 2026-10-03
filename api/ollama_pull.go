package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hexagon-codes/toolkit/net/sse"
)

type ollamaPullSnapshot struct {
	OperationID    string `json:"operation_id"`
	TargetID       string `json:"target_id"`
	TargetRevision uint64 `json:"target_revision"`
	Model          string `json:"model"`
	State          string `json:"state"`
	Status         string `json:"status"`
	Digest         string `json:"digest,omitempty"`
	Completed      int64  `json:"completed,omitempty"`
	Total          int64  `json:"total,omitempty"`
	Error          string `json:"error,omitempty"`
	Durable        bool   `json:"durable"`
}
type ollamaPullOperation struct {
	mu            sync.Mutex
	snapshot      ollamaPullSnapshot
	requestDigest string
	changed       chan struct{}
	target        ollamaTargetSnapshot
}

func (op *ollamaPullOperation) read() (ollamaPullSnapshot, <-chan struct{}) {
	op.mu.Lock()
	defer op.mu.Unlock()
	return op.snapshot, op.changed
}
func (op *ollamaPullOperation) update(update func(*ollamaPullSnapshot)) {
	op.mu.Lock()
	defer op.mu.Unlock()
	update(&op.snapshot)
	close(op.changed)
	op.changed = make(chan struct{})
}
func (s *Server) handleOllamaPull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model                  string  `json:"model"`
		ExpectedTargetID       string  `json:"expected_target_id,omitempty"`
		ExpectedTargetRevision *uint64 `json:"expected_target_revision,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
		return
	}
	id := r.Header.Get("Idempotency-Key")
	if id == "" {
		id = rand.Text()
	}
	if !llmConfigMutationRequestIDPattern.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid operation identity"})
		return
	}
	raw, _ := json.Marshal(req)
	digest := sha256Digest(raw)
	target := s.ollamaTarget()
	s.ollamaPullMu.Lock()
	if s.ollamaPulls == nil {
		s.ollamaPulls = make(map[string]*ollamaPullOperation)
	}
	op := s.ollamaPulls[id]
	if op != nil {
		s.ollamaPullMu.Unlock()
		if op.requestDigest != digest {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Ollama pull idempotency conflict"})
			return
		}
		serveOllamaPullEvents(w, r, op)
		return
	}
	if (req.ExpectedTargetID == "") != (req.ExpectedTargetRevision == nil) || (req.ExpectedTargetID != "" && (req.ExpectedTargetID != target.TargetID || *req.ExpectedTargetRevision != target.TargetRevision)) {
		s.ollamaPullMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Ollama target changed"})
		return
	}
	op = &ollamaPullOperation{snapshot: ollamaPullSnapshot{OperationID: id, TargetID: target.TargetID, TargetRevision: target.TargetRevision, Model: req.Model, State: "running", Status: "requesting"}, requestDigest: digest, changed: make(chan struct{}), target: target}
	s.ollamaPulls[id] = op
	s.ollamaPullMu.Unlock()
	// 物理请求由服务生命周期拥有；页面断开只退出订阅。
	go s.runOllamaPull(op)
	serveOllamaPullEvents(w, r, op)
}
func serveOllamaPullEvents(w http.ResponseWriter, r *http.Request, op *ollamaPullOperation) {
	if _, ok := w.(http.Flusher); !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Streaming not supported"})
		return
	}
	writer := sse.MustNewWriter(w)
	for {
		snapshot, changed := op.read()
		raw, _ := json.Marshal(snapshot)
		if writer.WriteData(string(raw)) != nil {
			return
		}
		if snapshot.State != "running" {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		}
	}
}
func (s *Server) pullOperation(id string) *ollamaPullOperation {
	s.ollamaPullMu.Lock()
	defer s.ollamaPullMu.Unlock()
	return s.ollamaPulls[id]
}
func (s *Server) handleGetOllamaPull(w http.ResponseWriter, r *http.Request) {
	op := s.pullOperation(r.PathValue("operation_id"))
	if op == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Ollama pull operation is unknown"})
		return
	}
	snapshot, _ := op.read()
	writeJSON(w, http.StatusOK, snapshot)
}
func (s *Server) handleOllamaPullEvents(w http.ResponseWriter, r *http.Request) {
	op := s.pullOperation(r.PathValue("operation_id"))
	if op == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Ollama pull operation is unknown"})
		return
	}
	serveOllamaPullEvents(w, r, op)
}
func (s *Server) runOllamaPull(op *ollamaPullOperation) {
	snapshot, _ := op.read()
	ctx, cancel := context.WithTimeout(s.ollamaLifecycleContext(), 4*time.Hour)
	defer cancel()
	finish := func(state, message string) {
		op.update(func(p *ollamaPullSnapshot) {
			p.State = state
			p.Error = message
			if state == "succeeded" {
				p.Status = "success"
			} else {
				p.Status = state
			}
		})
	}
	raw, _ := json.Marshal(map[string]any{"model": snapshot.Model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, op.target.endpoint("/api/pull"), bytes.NewReader(raw))
	if err != nil {
		finish("failed", "Invalid Ollama target")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := op.target.client(0, 30*time.Second).Do(req)
	if err != nil {
		finish("outcome_unknown", "Ollama pull response is unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		finish("failed", fmt.Sprintf("Ollama pull returned HTTP %d", response.StatusCode))
		return
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	succeeded := false
	explicitError := ""
	for scanner.Scan() {
		var event struct {
			Status    string `json:"status"`
			Digest    string `json:"digest"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			finish("outcome_unknown", "Invalid Ollama progress response")
			return
		}
		if event.Status != "" {
			succeeded = event.Status == "success"
		}
		if event.Error != "" {
			explicitError = event.Error
			succeeded = false
		}
		op.update(func(p *ollamaPullSnapshot) {
			p.Status = event.Status
			p.Digest = event.Digest
			if event.Total > 0 {
				p.Total = event.Total
			}
			if event.Completed > 0 {
				p.Completed = event.Completed
			}
		})
	}
	if explicitError != "" {
		finish("failed", explicitError)
		return
	}
	if scanner.Err() != nil || !succeeded {
		finish("outcome_unknown", "Ollama pull ended without a reliable terminal receipt")
		return
	}
	// 成功事件与原目标目录共同证明当前模型可用，不向新目标探测。
	tagsReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, op.target.endpoint("/api/tags"), nil)
	tags, err := op.target.client(10*time.Second, 10*time.Second).Do(tagsReq)
	if err != nil {
		finish("outcome_unknown", "Ollama model availability could not be verified")
		return
	}
	var catalog ollamaTagsResponse
	err = json.NewDecoder(tags.Body).Decode(&catalog)
	tags.Body.Close()
	found := false
	if tags.StatusCode == 200 && err == nil {
		for _, model := range catalog.Models {
			if canonicalOllamaModelTag(model.Name) == canonicalOllamaModelTag(snapshot.Model) {
				found = true
				break
			}
		}
	}
	if !found {
		finish("outcome_unknown", "Ollama model is not confirmed in the original target catalog")
		return
	}
	if s.onOllamaModelInstalled != nil && op.target.ResolvedBaseURL == s.ollamaBaseURL {
		s.onOllamaModelInstalled(ctx, snapshot.Model)
	}
	finish("succeeded", "")
}
