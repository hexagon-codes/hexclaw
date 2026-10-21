package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
)

type responseCacheContextKey struct{}

type responseCacheState struct {
	mu                   sync.Mutex
	generation           uint64
	key, provider, model string
	hit, finished        bool
}

func (e *ReActEngine) beginResponseCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, responseCacheContextKey{}, &responseCacheState{generation: e.responseCacheGeneration.Load()})
}

type responseCacheProvider struct {
	hexagon.Provider
	engine                  *ReActEngine
	message                 *adapter.Message
	providerName, modelName string
}

func (e *ReActEngine) wrapResponseCache(ctx context.Context, msg *adapter.Message, provider hexagon.Provider, name, model string) hexagon.Provider {
	if _, ok := ctx.Value(responseCacheContextKey{}).(*responseCacheState); !ok {
		return provider
	}
	return preserveContextTokenCounter(&responseCacheProvider{Provider: provider, engine: e, message: msg, providerName: name, modelName: model}, provider)
}

// lookup 只在全部依赖已确定的实际模型请求上查缓存；工具环境仍需执行本轮取数。
func (p *responseCacheProvider) lookup(ctx context.Context, req llm.CompletionRequest) (string, bool) {
	state, ok := ctx.Value(responseCacheContextKey{}).(*responseCacheState)
	if !ok {
		return "", false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.finished {
		return "", false
	}
	state.key = ""
	if state.generation != p.engine.responseCacheGeneration.Load() || shouldBypassSemanticCache(p.message) || len(req.Tools) != 0 {
		return "", false
	}
	identity := struct {
		Version                                        string
		Generation                                     uint64
		User, Session, Platform, Instance, Chat, Agent string
		Provider, Model                                string
		ReasoningScope                                 llm.ReasoningPolicyScope
		Request                                        llm.CompletionRequest
	}{Version: "effective-request-v2", Generation: state.generation,
		User: p.message.UserID, Session: p.message.SessionID, Platform: string(p.message.Platform), Instance: p.message.InstanceID, Chat: p.message.ChatID,
		Agent:    p.message.Metadata["role"] + "\x00" + p.message.Metadata["routed_agent"] + "\x00" + p.message.Metadata["pinned_agent"],
		Provider: p.providerName, Model: p.modelName, ReasoningScope: req.ReasoningPolicyScope, Request: req}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(raw)
	state.key = "effective-request-v2:" + hex.EncodeToString(sum[:])
	state.provider, state.model = p.providerName, p.modelName
	content, hit := p.engine.cache.Get(state.key, state.provider, state.model)
	state.hit = hit
	return content, hit
}

func (p *responseCacheProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if content, hit := p.lookup(ctx, req); hit {
		return &llm.CompletionResponse{Content: content, Model: p.modelName, FinishReason: "stop"}, nil
	}
	return p.Provider.Complete(ctx, req)
}

func (p *responseCacheProvider) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	if content, hit := p.lookup(ctx, req); hit {
		payload, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]string{"content": content}}}})
		return llm.NewStream(strings.NewReader("data: "+string(payload)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
	}
	return p.Provider.Stream(ctx, req)
}

// writeResponseCache 在业务输出完成后提交同一请求键，避免缓存未经处理的模型结果。
func (e *ReActEngine) writeResponseCache(ctx context.Context, content, provider, model string) {
	state, ok := ctx.Value(responseCacheContextKey{}).(*responseCacheState)
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.finished {
		return
	}
	state.finished = true
	if state.hit || state.key == "" || state.provider != provider || state.model != model || state.generation != e.responseCacheGeneration.Load() {
		return
	}
	e.cache.Put(state.key, content, provider, model)
}

func responseReplyMetadata(ctx context.Context, metadata map[string]string, provider, model, messageID string) map[string]string {
	result := buildReplyMetadata(metadata, provider, model, messageID)
	if receipt := runtimeSnapshotReceipt(ctx); receipt != "" {
		result[selfContextReceiptKey] = receipt
	}
	if state, ok := ctx.Value(responseCacheContextKey{}).(*responseCacheState); ok {
		state.mu.Lock()
		if state.hit {
			result["source"] = "cache"
		}
		state.mu.Unlock()
	}
	return result
}
