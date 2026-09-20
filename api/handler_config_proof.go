package api

import (
	"net/http"
	"strings"

	"github.com/hexagon-codes/hexclaw/config"
)

// handleRevealProviderKey 仅在显式显示动作中返回当前实例的凭据。
func (s *Server) handleRevealProviderKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider_instance_id")
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	w.Header().Set("Cache-Control", "no-store")
	for name, provider := range s.cfg.LLM.Providers {
		if config.EffectiveProviderInstanceID(name, provider) == id {
			writeJSON(w, http.StatusOK, map[string]string{"api_key": provider.APIKey})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "Provider not found"})
}

// handleGetConfigMutation 只查询持久提交证明，不能据未找到结果自动重发。
func (s *Server) handleGetConfigMutation(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("operation_kind"))
	if kind != "llm" && kind != "ollama_target" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Unsupported operation kind"})
		return
	}
	id := r.PathValue("request_id")
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	receipt, ok := s.cfg.LLM.MutationReceipts[id]
	if !ok && s.cfg.LLM.LastMutationReceipt != nil && s.cfg.LLM.LastMutationReceipt.RequestID == id {
		last := *s.cfg.LLM.LastMutationReceipt
		digest, err := digestLLMConfig(s.cfg.LLM)
		if err == nil && last.Revision == s.cfg.LLM.ConfigRevision && last.ConfigDigest == digest {
			receipt, ok = last, true
		}
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Committed mutation not found"})
		return
	}
	receiptKind := receipt.OperationKind
	if receiptKind == "" {
		receiptKind = "llm"
	}
	if receiptKind != kind {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Mutation operation kind conflict"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"target_revision": receipt.TargetRevision, "target_digest": receipt.TargetDigest,
		"status": "ok", "operation_kind": receiptKind, "request_id": receipt.RequestID,
		"config_revision": receipt.Revision, "config_digest": receipt.ConfigDigest,
		"committed_at": receipt.CommittedAt, "replayed": true,
	})
}
