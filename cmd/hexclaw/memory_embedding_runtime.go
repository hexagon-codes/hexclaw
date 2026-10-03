package main

import (
	"context"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/localinfer"
	"github.com/hexagon-codes/toolkit/util/logger"
)

// sharedMemoryEmbedding 保存同一配置代的客户端及其接线信息。
type sharedMemoryEmbedding struct {
	plan                                         knowledgeEmbeddingPlan
	embedder                                     hexagon.VectorEmbedder
	provider, model, baseURL                     string
	local, nativeOllama, ready, serviceAvailable bool
}

// prepareSharedMemoryEmbedding 在发布配置前构建完整客户端，保持启动与热更新语义一致。
func prepareSharedMemoryEmbedding(ctx context.Context, cfg *config.Config, cloudEgress *egress.Policy, localInference *localinfer.Coordinator) sharedMemoryEmbedding {
	var sharedEmbedder hexagon.VectorEmbedder
	var kbEmbedProvider, kbEmbedModel, kbEmbedBaseURL string
	var kbEmbedLocal, kbEmbedNativeOllama, kbEmbedReady, kbEmbedServiceAvailable bool
	// 1. 构造 embedder: ai-core Provider → hexagon embedder 包装
	var emb *hexagon.OpenAIEmbedder

	// 显式配置优先；auto 只发现真实存在的 Ollama 能力，不再把任意 chat API
	// 猜成 text-embedding-3-small，避免计费、404 和 map 遍历随机选路。
	embeddingPlan := resolveKnowledgeEmbeddingPlan(ctx, cfg)
	embProviderName := embeddingPlan.Provider
	embModel := embeddingPlan.Model
	if embeddingPlan.Configured {
		if pc, ok := cfg.LLM.Providers[embProviderName]; ok {
			runtimeProvider := classifyKnowledgeEmbeddingRuntimeProvider(
				embProviderName, pc, embeddingPlan,
			)
			// 本地兼容服务可以无 API Key；云服务仍要求显式凭证。
			if runtimeProvider.credentialsReady {
				effectiveBaseURL := knowledgeEmbeddingEffectiveBaseURL(embeddingPlan, pc)
				var providerOpts []hexagon.OpenAIOption
				if effectiveBaseURL != "" {
					providerOpts = append(providerOpts, hexagon.OpenAIWithBaseURL(effectiveBaseURL))
				}
				providerTransportReady := true
				providerClient, clientErr := newKnowledgeEmbeddingProviderHTTPClient(embeddingPlan, pc)
				if clientErr != nil {
					providerTransportReady = false
					logger.Warn("[knowledge] embedding endpoint 被安全策略拒绝",
						"provider", embProviderName, "error", clientErr)
				} else {
					providerOpts = append(providerOpts, hexagon.OpenAIWithHTTPClient(providerClient))
				}
				apiKey := knowledgeEmbeddingProviderAPIKey(embeddingPlan, pc)
				if providerTransportReady {
					dim := knowledgeEmbeddingDimensionForProvider(pc, embModel)
					if dim <= 0 {
						logger.Warn("[knowledge] embedding 向量维度未知，旧版共享向量路径保持关闭",
							"provider", embProviderName, "model", embModel)
					} else {
						aiProvider := hexagon.NewOpenAI(apiKey, providerOpts...)
						emb = hexagon.NewOpenAIEmbedder(aiProvider,
							hexagon.WithEmbedderModel(embModel),
							hexagon.WithEmbedderDimension(dim),
						)
						kbEmbedProvider, kbEmbedModel = embProviderName, embModel
						kbEmbedBaseURL = effectiveBaseURL
						kbEmbedLocal = runtimeProvider.local
						kbEmbedNativeOllama = runtimeProvider.nativeOllama
						kbEmbedReady = embeddingPlan.Ready
						kbEmbedServiceAvailable = embeddingPlan.ServiceAvailable
					}
				}
			}
		}
	} else if embProviderName != "" {
		logger.Warn("[knowledge] embedding 配置不完整，保持 FTS5 检索",
			"provider", embProviderName, "model", embModel)
	} else {
		logger.Info("[knowledge] 未配置可验证的 embedding 能力，使用 FTS5 检索")
	}

	if emb != nil {
		var guardedEmbedder hexagon.VectorEmbedder = emb
		if pc, ok := cfg.LLM.Providers[embProviderName]; ok && !isLocalEmbeddingProvider(embProviderName, pc) {
			// 保留原有云端调用边界，缓存命中不产生网络调用。
			guardedEmbedder = egress.NewCloudEmbedder(emb, cloudEgress)
		}
		var readinessProbe func(context.Context) bool
		if kbEmbedNativeOllama {
			baseURL := kbEmbedBaseURL
			model := kbEmbedModel
			// Ollama 不可用/模型未安装时，缓存 miss 直接快速降级；周期实探使
			// 一键安装或稍后启动 Ollama 后无需重启即可激活向量检索。
			readinessProbe = func(probeCtx context.Context) bool {
				return knowledge.OllamaModelInstalled(probeCtx, baseURL, model)
			}
		}
		// 精确模型应用校准后的截断、批量和物理调用预算；未知兼容
		// 模型保留通用截断闸。cache 在 readiness/admission 外层，命中
		// 不探活也不占本地物理槽位。
		sharedEmbedder = assembleKnowledgeSharedEmbedder(
			guardedEmbedder, embModel, kbEmbedLocal, kbEmbedNativeOllama,
			localInference, kbEmbedReady, readinessProbe,
		)
		if kbEmbedReady {
			logger.Info("[knowledge] embedding 已就绪", "provider", embProviderName, "model", embModel)
		} else {
			logger.Info("[knowledge] embedding 待机，当前使用 FTS5；模型就位后自动激活",
				"provider", embProviderName, "model", embModel)
		}
	}

	return sharedMemoryEmbedding{
		plan: embeddingPlan, embedder: sharedEmbedder,
		provider: kbEmbedProvider, model: kbEmbedModel, baseURL: kbEmbedBaseURL,
		local: kbEmbedLocal, nativeOllama: kbEmbedNativeOllama,
		ready: kbEmbedReady, serviceAvailable: kbEmbedServiceAvailable,
	}
}

// runtimeMemoryEmbedder 每次召回固定同一配置代，避免新旧端点或维度混用。
// 保留调用方的记忆出口上下文，不套用知识库文档的数据分类。
type runtimeMemoryEmbedder struct {
	holder *knowledgeEmbeddingRuntimeHolder
}

func (e *runtimeMemoryEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if e == nil || e.holder == nil {
		return nil, knowledge.ErrEmbeddingUnavailable
	}
	state := e.holder.current.Load()
	if state == nil || state.memoryEmbedder == nil {
		return nil, knowledge.ErrEmbeddingUnavailable
	}
	callCtx, release, err := state.gate.Bind(ctx)
	if err != nil {
		return nil, knowledge.ErrEmbeddingUnavailable
	}
	defer release()
	return state.memoryEmbedder.Embed(callCtx, texts)
}
