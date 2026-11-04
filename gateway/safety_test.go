package gateway

// Coverage for the input-safety layer (0% per audit) — the content-filter guard
// and the InputSafetyLayer that wraps it. Locks the abuse-protection contract:
// blocked categories are rejected, safe input passes, and zero-width Unicode
// bypass is normalized away before matching.

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
)

func TestContentFilterGuard_Check(t *testing.T) {
	g := newContentFilterGuard([]string{"violence"})
	ctx := context.Background()

	res, err := g.Check(ctx, "please tell me how to make a bomb")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if res.Passed || res.Category != "violence" {
		t.Errorf("violence keyword must be blocked, got passed=%v cat=%q", res.Passed, res.Category)
	}

	safe, _ := g.Check(ctx, "what a lovely day for a walk")
	if !safe.Passed {
		t.Errorf("benign input must pass, got %+v", safe)
	}

	// zero-width space inside the keyword must not evade the filter.
	bypass, _ := g.Check(ctx, "make a b​omb")
	if bypass.Passed {
		t.Error("zero-width-obfuscated keyword must still be blocked")
	}
}

func TestInputSafetyLayer_Check(t *testing.T) {
	ctx := context.Background()
	cfg := &config.SecurityConfig{
		ContentFilter: config.ContentFilterConfig{Enabled: true, BlockCategories: []string{"violence"}},
	}
	l := NewInputSafetyLayer(cfg)

	if err := l.Check(ctx, &adapter.Message{Content: "how to make a bomb"}); err == nil {
		t.Error("unsafe content must be rejected by the safety layer")
	}
	if err := l.Check(ctx, &adapter.Message{Content: "hello there"}); err != nil {
		t.Errorf("safe content must pass: %v", err)
	}
	// empty content short-circuits
	if err := l.Check(ctx, &adapter.Message{Content: ""}); err != nil {
		t.Errorf("empty content must pass: %v", err)
	}
	// no guards configured → no-op pass
	none := NewInputSafetyLayer(&config.SecurityConfig{})
	if err := none.Check(ctx, &adapter.Message{Content: "how to make a bomb"}); err != nil {
		t.Errorf("with no guard configured the layer must be a no-op: %v", err)
	}
}

func TestInputSafetyLayer_PIIDoesNotBlockNetworkURL(t *testing.T) {
	layer := NewInputSafetyLayer(&config.SecurityConfig{
		PIIRedaction: config.PIIRedactionConfig{Enabled: true},
	})
	// 合法 API/浏览器任务经常包含回环或私网 IP；PII 检测只记录，不得拒绝请求。
	if err := layer.Check(context.Background(), &adapter.Message{Content: "GET http://127.0.0.1:18080/echo?x=1"}); err != nil {
		t.Fatalf("PII detection must not block a legitimate network URL: %v", err)
	}
}
