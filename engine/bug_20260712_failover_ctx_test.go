package engine

// 调用者取消与上游局部失败必须区分：前者终止派发，后者才允许在原期限内回退。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
)

// ctxPoisonLocalProvider 在首次模型调用期间触发调用者取消。
type ctxPoisonLocalProvider struct {
	name   string
	cancel context.CancelFunc
	calls  int32
}

func (p *ctxPoisonLocalProvider) Name() string { return p.name }

func (p *ctxPoisonLocalProvider) poison() error {
	atomic.AddInt32(&p.calls, 1)
	if p.cancel != nil {
		p.cancel()
	}
	return errors.New(`Post "http://localhost:11434/api/chat": context canceled`)
}

func (p *ctxPoisonLocalProvider) Complete(_ context.Context, _ hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
	return nil, p.poison()
}

func (p *ctxPoisonLocalProvider) Stream(_ context.Context, _ hexagon.CompletionRequest) (*hexagon.LLMStream, error) {
	return nil, p.poison()
}

func (p *ctxPoisonLocalProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "qwen3.5:9b", Name: "Qwen"}}
}

func (p *ctxPoisonLocalProvider) CountTokens(messages []hexagon.Message) (int, error) {
	return len(messages), nil
}

func (p *ctxPoisonLocalProvider) callCount() int32 { return atomic.LoadInt32(&p.calls) }

// ctxHealthCloudProvider 是回退目标：像真实 HTTP 客户端一样，收到**已取消的 ctx** 就立刻失败
// （记 sawCanceled），只有拿到干净 ctx 才 capture egress 信封并正常回答 "ok"。
type ctxHealthCloudProvider struct {
	egressCaptureProvider
	sawCanceled int32
	calls       int32
	receivedCtx context.Context
}

func (p *ctxHealthCloudProvider) Complete(ctx context.Context, _ hexagon.CompletionRequest) (*hexagon.CompletionResponse, error) {
	atomic.AddInt32(&p.calls, 1)
	p.receivedCtx = ctx
	if err := ctx.Err(); err != nil {
		atomic.StoreInt32(&p.sawCanceled, 1)
		return nil, fmt.Errorf(`Post "https://open.bigmodel.cn": %w`, err)
	}
	p.capture(ctx)
	return &hexagon.CompletionResponse{Content: "ok"}, nil
}

func (p *ctxHealthCloudProvider) Stream(ctx context.Context, _ hexagon.CompletionRequest) (*hexagon.LLMStream, error) {
	atomic.AddInt32(&p.calls, 1)
	p.receivedCtx = ctx
	if err := ctx.Err(); err != nil {
		atomic.StoreInt32(&p.sawCanceled, 1)
		return nil, fmt.Errorf(`Post "https://open.bigmodel.cn": %w`, err)
	}
	p.capture(ctx)
	body := strings.Join([]string{
		`data: {"id":"c1","model":"mock-model","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")
	return llm.NewStream(strings.NewReader(body), llm.StreamOpenAIFormat), nil
}

func (p *ctxHealthCloudProvider) canceledSeen() bool { return atomic.LoadInt32(&p.sawCanceled) == 1 }

// 非流式调用者取消后不得启动备用模型或污染当前模型健康。
func TestFailover_NonStreaming_PreservesCanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := &ctxPoisonLocalProvider{name: "Ollama (本地)", cancel: cancel}
	cloud := &ctxHealthCloudProvider{}
	eng := newFailoverEgressEngine(t, local, cloud, local.name, "openrouter")

	reply, err := eng.Process(ctx, &adapter.Message{
		ID: "foe-ctx-nonstream", Platform: adapter.PlatformAPI,
		UserID: "u-ctx-1", ChatID: "c-ctx-1",
		Content: "hello",
	})
	if !errors.Is(err, context.Canceled) || reply != nil {
		t.Fatalf("caller cancellation must stop the request: reply=%+v err=%v", reply, err)
	}
	if local.callCount() == 0 {
		t.Fatalf("本地 provider 应先被调用一次（是回退+取消的起点）")
	}
	if calls := atomic.LoadInt32(&cloud.calls); calls != 0 {
		t.Fatalf("caller cancellation dispatched %d fallback calls", calls)
	}
	if _, name, routeErr := eng.router.Route(context.Background()); routeErr != nil || name != local.name {
		t.Fatalf("caller cancellation must not trip provider health: name=%q err=%v", name, routeErr)
	}
}

// 流式取消保持同一停止语义，不把取消包装成新的模型请求。
func TestFailover_Streaming_PreservesCanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := &ctxPoisonLocalProvider{name: "Ollama (本地)", cancel: cancel}
	cloud := &ctxHealthCloudProvider{}
	eng := newFailoverEgressEngine(t, local, cloud, local.name, "openrouter")

	ch, err := eng.ProcessStream(ctx, &adapter.Message{
		ID: "foe-ctx-stream", Platform: adapter.PlatformAPI,
		UserID: "u-ctx-2", ChatID: "c-ctx-2",
		Content: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessStream 建流失败: %v", err)
	}
	out, derr := drainStream(t, ch)
	if out != "" {
		t.Fatalf("caller cancellation must not produce a fallback answer: out=%q err=%v", out, derr)
	}
	if local.callCount() == 0 {
		t.Fatalf("本地 provider 应先被调用一次（是回退+取消的起点）")
	}
	if calls := atomic.LoadInt32(&cloud.calls); calls != 0 {
		t.Fatalf("caller cancellation dispatched %d fallback calls", calls)
	}
	if _, name, routeErr := eng.router.Route(context.Background()); routeErr != nil || name != local.name {
		t.Fatalf("caller cancellation must not trip provider health: name=%q err=%v", name, routeErr)
	}
}
