package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/hexagon-codes/hexclaw/api"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/storage"
)

type k12CapabilityReceiptEvidence struct {
	ProviderInstanceID      string
	ConfigFingerprint       string
	CapabilityReceiptDigest string
	ProbePolicyVersion      string
}

type k12SavedModelCapabilityProbe func(context.Context, string, string, string) error

var k12NewTaskCapabilityProbeMu sync.Mutex

// resolveK12GradingModelSnapshotWithCapabilityReceipt 为精确文本候选冻结视觉成功回执。
// 已冻结任务只验证原证据，不重新探测或替换模型、回执。
func resolveK12GradingModelSnapshotWithCapabilityReceipt(
	ctx context.Context,
	router *llmrouter.Selector,
	receipts storage.ModelCapabilityProbeReceiptStore,
	requested k12.GradingModelSnapshot,
	probes ...k12SavedModelCapabilityProbe,
) (k12.GradingModelSnapshot, error) {
	snapshot, err := resolveK12GradingModelSnapshot(router, requested)
	if err != nil {
		return k12.GradingModelSnapshot{}, err
	}
	requested = k12.NormalizeGradingModelSnapshot(requested)
	if requested.HasFrozenCapabilityProbeEvidence() {
		if requested.Provider != snapshot.Provider || requested.Model != snapshot.Model || requested.ProviderInstanceID != snapshot.ProviderInstanceID {
			return k12.GradingModelSnapshot{}, k12.ErrModelCapabilityUnverified
		}
		if err := validateK12FrozenModelCapabilityReceipt(ctx, router, receipts, requested, config.LLMModelCapabilityVision); err != nil {
			return k12.GradingModelSnapshot{}, err
		}
		return requested, nil
	}
	evidence, err := ensureK12NewTaskVisionCapabilityReceipt(
		ctx, router, receipts, snapshot.Provider, snapshot.Model, probes,
	)
	if err != nil {
		return k12.GradingModelSnapshot{}, err
	}
	snapshot.ProviderInstanceID = evidence.ProviderInstanceID
	snapshot.ConfigFingerprint = evidence.ConfigFingerprint
	snapshot.CapabilityReceiptDigest = evidence.CapabilityReceiptDigest
	snapshot.ProbePolicyVersion = evidence.ProbePolicyVersion
	return k12.NormalizeGradingModelSnapshot(snapshot), nil
}

// ensureK12NewTaskVisionCapabilityReceipt 只为新任务补齐缺失或过期的当前视觉回执。
// 当前配置的失败回执不自动重发；运行中冻结任务的校验仍只读，不能换模型或回执。
func ensureK12NewTaskVisionCapabilityReceipt(
	ctx context.Context,
	router *llmrouter.Selector,
	receipts storage.ModelCapabilityProbeReceiptStore,
	providerName, modelID string,
	probes []k12SavedModelCapabilityProbe,
) (k12CapabilityReceiptEvidence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	evidence, err := k12CapabilityReceiptEvidenceForRoute(ctx, router, receipts, providerName, modelID, config.LLMModelCapabilityVision)
	if err == nil || len(probes) == 0 || probes[0] == nil || router == nil || receipts == nil {
		return evidence, err
	}
	// 同时到达的首图在锁内重读，避免为同一模型发送多次前置探测。
	k12NewTaskCapabilityProbeMu.Lock()
	defer k12NewTaskCapabilityProbeMu.Unlock()
	evidence, err = k12CapabilityReceiptEvidenceForRoute(ctx, router, receipts, providerName, modelID, config.LLMModelCapabilityVision)
	if err == nil {
		return evidence, nil
	}
	provider, configured := router.ProviderConfig(providerName)
	if !configured {
		return evidence, err
	}
	providerInstanceID := config.EffectiveProviderInstanceID(providerName, provider)
	fingerprint := api.ModelCapabilityProbeConfigFingerprint(providerName, provider, modelID)
	receipt, readErr := receipts.GetModelCapabilityProbeReceipt(ctx, providerInstanceID, modelID, config.LLMModelCapabilityVision)
	if readErr != nil || (receipt != nil && receipt.ConfigFingerprint == fingerprint && receipt.ProbePolicyVersion == api.ModelCapabilityProbePolicyVersion) {
		return evidence, err
	}
	if probeErr := probes[0](ctx, providerInstanceID, modelID, config.LLMModelCapabilityVision); probeErr != nil {
		return k12CapabilityReceiptEvidence{}, fmt.Errorf("%w: %v", k12.ErrModelCapabilityUnverified, probeErr)
	}
	return k12CapabilityReceiptEvidenceForRoute(ctx, router, receipts, providerName, modelID, config.LLMModelCapabilityVision)
}

// resolveK12PracticeModelSnapshotWithCapabilityReceipt 冻结逐题生成实际发送前需要的
// text 探测回执；旧任务不会在运行时替换为当前默认模型。
func resolveK12PracticeModelSnapshotWithCapabilityReceipt(
	ctx context.Context,
	router *llmrouter.Selector,
	receipts storage.ModelCapabilityProbeReceiptStore,
	requested k12.GradingModelSnapshot,
) (k12.GradingModelSnapshot, error) {
	snapshot, err := resolveK12PracticeModelSnapshot(router, requested)
	if err != nil {
		return k12.GradingModelSnapshot{}, err
	}
	evidence, err := k12CapabilityReceiptEvidenceForRoute(
		ctx, router, receipts, snapshot.Provider, snapshot.Model, config.LLMModelCapabilityText,
	)
	if err != nil {
		return k12.GradingModelSnapshot{}, err
	}
	snapshot.ProviderInstanceID = evidence.ProviderInstanceID
	snapshot.ConfigFingerprint = evidence.ConfigFingerprint
	snapshot.CapabilityReceiptDigest = evidence.CapabilityReceiptDigest
	snapshot.ProbePolicyVersion = evidence.ProbePolicyVersion
	return k12.NormalizeGradingModelSnapshot(snapshot), nil
}

// resolveK12WorkFeedbackRouteWithCapabilityReceipt 冻结作品点评的真实发送能力：
// 写作使用 text，美术使用 vision。静态 text+vision 声明本身不能替代对应回执。
func resolveK12WorkFeedbackRouteWithCapabilityReceipt(
	ctx context.Context,
	router *llmrouter.Selector,
	receipts storage.ModelCapabilityProbeReceiptStore,
	workType string,
	probes ...k12SavedModelCapabilityProbe,
) (k12.ImageTaskRouteSnapshot, error) {
	route, err := resolveK12WorkFeedbackRoute(router, workType)
	if err != nil {
		return k12.ImageTaskRouteSnapshot{}, err
	}
	return freezeK12WorkFeedbackRouteCapabilityReceipt(
		ctx, router, receipts, workType, route, probes,
	)
}

func resolveK12RequestedWorkFeedbackRouteWithCapabilityReceipt(
	ctx context.Context,
	router *llmrouter.Selector,
	receipts storage.ModelCapabilityProbeReceiptStore,
	workType string,
	requested k12.ImageTaskRouteSnapshot,
	probes ...k12SavedModelCapabilityProbe,
) (k12.ImageTaskRouteSnapshot, error) {
	requested = k12.NormalizeImageTaskRouteSnapshot(requested)
	selection := requested
	if k12WorkFeedbackHasMatchingFrozenCapability(requested, workType) {
		selection.SelectionSource = "explicit"
	}
	route, err := resolveK12RequestedWorkFeedbackRoute(router, workType, selection)
	if err != nil {
		return k12.ImageTaskRouteSnapshot{}, err
	}
	if k12WorkFeedbackHasMatchingFrozenCapability(requested, workType) {
		route.ProviderInstanceID = requested.ProviderInstanceID
		route.ConfigFingerprint = requested.ConfigFingerprint
		route.CapabilityReceiptDigest = requested.CapabilityReceiptDigest
		route.ProbePolicyVersion = requested.ProbePolicyVersion
		route.Capability = requested.Capability
		route.SelectionSource = requested.SelectionSource
	}
	return freezeK12WorkFeedbackRouteCapabilityReceipt(
		ctx, router, receipts, workType, route, probes,
	)
}

// 不同阶段能力回执各自归属；视觉分类证据不能替换写作点评的文本回执。
func k12WorkFeedbackHasMatchingFrozenCapability(route k12.ImageTaskRouteSnapshot, workType string) bool {
	return route.HasFrozenCapabilityProbeEvidence() &&
		k12ProbeKindForSnapshot(k12.GradingModelSnapshot{Capability: route.Capability}) == k12WorkFeedbackProbeKind(workType)
}

func k12WorkFeedbackProbeKind(workType string) string {
	if strings.TrimSpace(workType) == k12.WorkTypeArt {
		return config.LLMModelCapabilityVision
	}
	return config.LLMModelCapabilityText
}

func freezeK12WorkFeedbackRouteCapabilityReceipt(
	ctx context.Context,
	router *llmrouter.Selector,
	receipts storage.ModelCapabilityProbeReceiptStore,
	workType string,
	route k12.ImageTaskRouteSnapshot,
	probes []k12SavedModelCapabilityProbe,
) (k12.ImageTaskRouteSnapshot, error) {
	probeKind := k12WorkFeedbackProbeKind(workType)
	if k12WorkFeedbackHasMatchingFrozenCapability(route, workType) {
		snapshot := k12.GradingModelSnapshot{
			Provider: route.Provider, Model: route.Model, Route: route.Route, Capability: route.Capability,
			ProviderInstanceID: route.ProviderInstanceID, ConfigFingerprint: route.ConfigFingerprint,
			CapabilityReceiptDigest: route.CapabilityReceiptDigest, ProbePolicyVersion: route.ProbePolicyVersion,
		}
		if err := validateK12FrozenModelCapabilityReceipt(ctx, router, receipts, snapshot, probeKind); err != nil {
			return k12.ImageTaskRouteSnapshot{}, err
		}
		return k12.NormalizeImageTaskRouteSnapshot(route), nil
	}
	var evidence k12CapabilityReceiptEvidence
	var err error
	if probeKind == config.LLMModelCapabilityVision {
		evidence, err = ensureK12NewTaskVisionCapabilityReceipt(ctx, router, receipts, route.Provider, route.Model, probes)
	} else {
		evidence, err = k12CapabilityReceiptEvidenceForRoute(ctx, router, receipts, route.Provider, route.Model, probeKind)
	}
	if err != nil {
		return k12.ImageTaskRouteSnapshot{}, err
	}
	route.ProviderInstanceID = evidence.ProviderInstanceID
	route.ConfigFingerprint = evidence.ConfigFingerprint
	route.CapabilityReceiptDigest = evidence.CapabilityReceiptDigest
	route.ProbePolicyVersion = evidence.ProbePolicyVersion
	return k12.NormalizeImageTaskRouteSnapshot(route), nil
}

func k12CapabilityReceiptEvidenceForRoute(
	ctx context.Context,
	router *llmrouter.Selector,
	receipts storage.ModelCapabilityProbeReceiptStore,
	providerName, modelID, probeKind string,
) (k12CapabilityReceiptEvidence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if router == nil || receipts == nil {
		return k12CapabilityReceiptEvidence{}, k12.ErrModelCapabilityUnverified
	}
	providerName = strings.TrimSpace(providerName)
	modelID = strings.TrimSpace(modelID)
	probeKind = strings.TrimSpace(probeKind)
	provider, configured := router.ProviderConfig(providerName)
	if !configured || providerName == "" || modelID == "" || probeKind == "" {
		return k12CapabilityReceiptEvidence{}, k12.ErrModelCapabilityUnverified
	}
	return k12CapabilityReceiptEvidenceForProviderConfig(ctx, receipts, providerName, modelID, probeKind, provider)
}

// k12CapabilityReceiptEvidenceForProviderConfig 复用路由捕获的配置，避免跨重载读取两份事实。
func k12CapabilityReceiptEvidenceForProviderConfig(ctx context.Context, receipts storage.ModelCapabilityProbeReceiptStore, providerName, modelID, probeKind string, provider config.LLMProviderConfig) (k12CapabilityReceiptEvidence, error) {
	if receipts == nil {
		return k12CapabilityReceiptEvidence{}, k12.ErrModelCapabilityUnverified
	}
	providerInstanceID := config.EffectiveProviderInstanceID(providerName, provider)
	fingerprint := api.ModelCapabilityProbeConfigFingerprint(providerName, provider, modelID)
	receipt, err := receipts.GetModelCapabilityProbeReceipt(
		ctx, providerInstanceID, modelID, probeKind,
	)
	if err != nil || receipt == nil ||
		strings.TrimSpace(receipt.Outcome) != "passed" ||
		strings.TrimSpace(receipt.ConfigFingerprint) != fingerprint ||
		strings.TrimSpace(receipt.ProbePolicyVersion) != api.ModelCapabilityProbePolicyVersion {
		return k12CapabilityReceiptEvidence{}, k12.ErrModelCapabilityUnverified
	}
	return k12CapabilityReceiptEvidence{
		ProviderInstanceID:      providerInstanceID,
		ConfigFingerprint:       fingerprint,
		CapabilityReceiptDigest: k12CapabilityReceiptDigest(receipt),
		ProbePolicyVersion:      strings.TrimSpace(receipt.ProbePolicyVersion),
	}, nil
}

// validateK12FrozenModelCapabilityReceipt 在模型调用前重读当前执行配置和持久回执。
// 任一字段漂移均以终态错误停止，绝不重选模型或自动重发。
func validateK12FrozenModelCapabilityReceipt(
	ctx context.Context,
	router *llmrouter.Selector,
	receipts storage.ModelCapabilityProbeReceiptStore,
	snapshot k12.GradingModelSnapshot,
	probeKind string,
) error {
	if router == nil {
		return k12.ErrModelCapabilityUnverified
	}
	provider, configured := router.ProviderConfig(snapshot.Provider)
	if !configured {
		return k12.ErrModelCapabilityUnverified
	}
	return validateK12FrozenModelCapabilityReceiptForProviderConfig(ctx, receipts, snapshot, probeKind, provider)
}

func validateK12FrozenModelCapabilityReceiptForProviderConfig(ctx context.Context, receipts storage.ModelCapabilityProbeReceiptStore, snapshot k12.GradingModelSnapshot, probeKind string, provider config.LLMProviderConfig) error {
	snapshot = k12.NormalizeGradingModelSnapshot(snapshot)
	if !snapshot.HasFrozenCapabilityProbeEvidence() {
		return k12.ErrModelCapabilityUnverified
	}
	evidence, err := k12CapabilityReceiptEvidenceForProviderConfig(ctx, receipts, snapshot.Provider, snapshot.Model, probeKind, provider)
	if err != nil {
		return k12.ErrModelCapabilityUnverified
	}
	if evidence.ProviderInstanceID != snapshot.ProviderInstanceID ||
		evidence.ConfigFingerprint != snapshot.ConfigFingerprint ||
		evidence.CapabilityReceiptDigest != snapshot.CapabilityReceiptDigest ||
		evidence.ProbePolicyVersion != snapshot.ProbePolicyVersion {
		return k12.ErrModelCapabilityUnverified
	}
	return nil
}

func k12ProbeKindForSnapshot(snapshot k12.GradingModelSnapshot) string {
	if strings.Contains(
		strings.ToLower(strings.TrimSpace(snapshot.Capability)),
		config.LLMModelCapabilityVision,
	) {
		// 视觉探测使用包含文本与图片的同一次 completion，足以覆盖该冻结视觉路由的
		// 文本输出协议；文本探测成功反过来不能推导视觉能力。
		return config.LLMModelCapabilityVision
	}
	return config.LLMModelCapabilityText
}

func k12CapabilityReceiptDigest(receipt *storage.ModelCapabilityProbeReceipt) string {
	if receipt == nil {
		return ""
	}
	// 只对不可逆配置摘要与探测元数据取摘要，不把请求、模型正文或上游原文纳入快照。
	payload, _ := json.Marshal(struct {
		ProviderInstanceID string `json:"provider_instance_id"`
		ModelID            string `json:"model_id"`
		ProbeKind          string `json:"probe_kind"`
		ConfigFingerprint  string `json:"config_fingerprint"`
		ProbePolicyVersion string `json:"probe_policy_version"`
		Outcome            string `json:"outcome"`
		FailureCode        string `json:"failure_code"`
		TestedAt           int64  `json:"tested_at"`
		ProbeStartedAt     int64  `json:"probe_started_at"`
		LatencyMS          int64  `json:"latency_ms"`
	}{
		ProviderInstanceID: strings.TrimSpace(receipt.ProviderInstanceID),
		ModelID:            strings.TrimSpace(receipt.ModelID),
		ProbeKind:          strings.TrimSpace(receipt.ProbeKind),
		ConfigFingerprint:  strings.TrimSpace(receipt.ConfigFingerprint),
		ProbePolicyVersion: strings.TrimSpace(receipt.ProbePolicyVersion),
		Outcome:            strings.TrimSpace(receipt.Outcome),
		FailureCode:        strings.TrimSpace(receipt.FailureCode),
		TestedAt:           receipt.TestedAt,
		ProbeStartedAt:     receipt.ProbeStartedAt,
		LatencyMS:          receipt.LatencyMS,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
