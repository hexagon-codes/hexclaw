package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
)

var llmConfigMutationRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var (
	errLLMConfigMutationIDRequired = errors.New("typed credential mutation requires Idempotency-Key")
	errLLMConfigMutationIDInvalid  = errors.New("invalid LLM config Idempotency-Key")
	errLLMConfigMutationConflict   = errors.New("LLM config idempotency conflict")
)

// Provider credential changes are rare administrative operations. Keep a
// bounded fail-closed ledger in the durable config instead of evicting old
// request IDs and silently making a previously committed mutation executable
// again. Operators can compact the ledger deliberately during maintenance.
const maxLLMConfigMutationReceipts = 1024

type llmConfigMutationProof struct {
	operationKind    string
	targetRevision   uint64
	targetDigest     string
	preserveRevision bool
	requestID        string
	requestDigest    string
}

type llmConfigMutationResponse struct {
	OperationKind  string `json:"operation_kind,omitempty"`
	TargetRevision uint64 `json:"target_revision,omitempty"`
	TargetDigest   string `json:"target_digest,omitempty"`
	Status         string `json:"status"`
	RequestID      string `json:"request_id,omitempty"`
	ConfigRevision uint64 `json:"config_revision"`
	ConfigDigest   string `json:"config_digest"`
	CommittedAt    int64  `json:"committed_at,omitempty"`
	Replayed       bool   `json:"replayed"`
}

func newLLMConfigMutationProof(req LLMConfigUpdateRequest, requestID string) (llmConfigMutationProof, error) {
	typedCredentialMutation := false
	for _, provider := range req.Providers {
		if provider.APIKeyMutation != nil {
			typedCredentialMutation = true
			break
		}
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		if typedCredentialMutation {
			return llmConfigMutationProof{}, errLLMConfigMutationIDRequired
		}
		return llmConfigMutationProof{}, nil
	}
	if !llmConfigMutationRequestIDPattern.MatchString(requestID) {
		return llmConfigMutationProof{}, errLLMConfigMutationIDInvalid
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return llmConfigMutationProof{}, fmt.Errorf("canonicalize LLM config mutation: %w", err)
	}
	return llmConfigMutationProof{requestID: requestID, requestDigest: sha256Digest(raw)}, nil
}

func replayLLMConfigMutation(old config.LLMConfig, proof llmConfigMutationProof) (*llmConfigMutationResponse, error) {
	if proof.requestID == "" {
		return nil, nil
	}
	receipt, found := old.MutationReceipts[proof.requestID]
	if found {
		if receipt.RequestID != proof.requestID || receipt.RequestDigest != proof.requestDigest || receipt.OperationKind != proof.operationKind || (receipt.Revision == 0 && receipt.OperationKind != "ollama_target") || receipt.ConfigDigest == "" {
			return nil, errLLMConfigMutationConflict
		}
		return &llmConfigMutationResponse{
			Status: "ok", RequestID: receipt.RequestID, ConfigRevision: receipt.Revision,
			OperationKind: receipt.OperationKind, TargetRevision: receipt.TargetRevision, TargetDigest: receipt.TargetDigest,
			ConfigDigest: receipt.ConfigDigest, CommittedAt: receipt.CommittedAt, Replayed: true,
		}, nil
	}
	// Backward compatibility for configs created before the ledger existed.
	// A legacy last-only receipt is accepted only while it still describes the
	// current config; once a newer commit exists it cannot prove an older result.
	if old.LastMutationReceipt == nil || old.LastMutationReceipt.RequestID != proof.requestID {
		return nil, nil
	}
	receipt = *old.LastMutationReceipt
	if receipt.RequestDigest != proof.requestDigest || receipt.OperationKind != proof.operationKind || receipt.Revision != old.ConfigRevision {
		return nil, errLLMConfigMutationConflict
	}
	currentDigest, err := digestLLMConfig(old)
	if err != nil {
		return nil, err
	}
	if currentDigest != receipt.ConfigDigest {
		return nil, errLLMConfigMutationConflict
	}
	return &llmConfigMutationResponse{
		Status: "ok", RequestID: receipt.RequestID, ConfigRevision: receipt.Revision,
		OperationKind: receipt.OperationKind, TargetRevision: receipt.TargetRevision, TargetDigest: receipt.TargetDigest,
		ConfigDigest: receipt.ConfigDigest, CommittedAt: receipt.CommittedAt, Replayed: true,
	}, nil
}

func finalizeLLMConfigMutation(old config.LLMConfig, next *config.LLMConfig, proof llmConfigMutationProof) (llmConfigMutationResponse, error) {
	if next == nil {
		return llmConfigMutationResponse{}, errors.New("next LLM config is nil")
	}
	if old.ConfigRevision == math.MaxUint64 {
		return llmConfigMutationResponse{}, errors.New("LLM config revision exhausted")
	}
	receipts := make(map[string]config.LLMConfigMutationReceipt, len(old.MutationReceipts)+1)
	for requestID, receipt := range old.MutationReceipts {
		receipts[requestID] = receipt
	}
	next.MutationReceipts = receipts
	next.ConfigRevision = old.ConfigRevision + 1
	if proof.preserveRevision {
		next.ConfigRevision = old.ConfigRevision
	}
	configDigest, err := digestLLMConfig(*next)
	if err != nil {
		return llmConfigMutationResponse{}, err
	}
	response := llmConfigMutationResponse{
		Status: "ok", RequestID: proof.requestID, ConfigRevision: next.ConfigRevision,
		OperationKind: proof.operationKind, TargetRevision: proof.targetRevision, TargetDigest: proof.targetDigest,
		ConfigDigest: configDigest,
	}
	if proof.requestID == "" {
		return response, nil
	}
	if _, exists := receipts[proof.requestID]; !exists && len(receipts) >= maxLLMConfigMutationReceipts {
		return llmConfigMutationResponse{}, errors.New("LLM config idempotency ledger capacity exhausted")
	}
	committedAt := time.Now().UTC().UnixMilli()
	receipt := config.LLMConfigMutationReceipt{
		RequestID: proof.requestID, RequestDigest: proof.requestDigest,
		OperationKind: proof.operationKind, TargetRevision: proof.targetRevision, TargetDigest: proof.targetDigest,
		ConfigDigest: configDigest, Revision: next.ConfigRevision, CommittedAt: committedAt,
	}
	next.MutationReceipts[proof.requestID] = receipt
	next.LastMutationReceipt = &receipt
	response.CommittedAt = committedAt
	return response, nil
}

func digestLLMConfig(value config.LLMConfig) (string, error) {
	// Runtime API keys are deliberately excluded: persisted configs retain only
	// credential_ref and a restart therefore produces the same digest without
	// ever writing or returning secret material.
	value.LastMutationReceipt = nil
	value.MutationReceipts = nil
	providers := make(map[string]config.LLMProviderConfig, len(value.Providers))
	for name, provider := range value.Providers {
		provider.APIKey = ""
		providers[name] = provider
	}
	value.Providers = providers
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize LLM config: %w", err)
	}
	return sha256Digest(raw), nil
}

func sha256Digest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}
