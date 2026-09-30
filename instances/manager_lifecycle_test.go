package instances

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/gateway"
	"github.com/hexagon-codes/hexclaw/skill"
)

type lifecycleAdapter struct {
	stubAdapter
	ctx     context.Context
	cancel  context.CancelFunc
	handler adapter.MessageHandler
}

func (a *lifecycleAdapter) Start(ctx context.Context, handler adapter.MessageHandler) error {
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.handler = handler
	return nil
}

func (a *lifecycleAdapter) Stop(context.Context) error {
	a.cancel()
	return nil
}

func TestManagerStartSeparatesRequestIdentityAndConnectionLifetime(t *testing.T) {
	mgr, cleanup := newTestManager(t)
	defer cleanup()
	inst := &Instance{Provider: "dingtalk", Name: "phone", Enabled: true, Config: []byte(`{}`)}
	if err := mgr.Upsert(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	adp := &lifecycleAdapter{}
	mgr.buildAdapter = func(*Instance) (adapter.Adapter, error) { return adp, nil }
	auth := gateway.NewAuthLayer(config.AuthConfig{})
	received := ""
	mgr.SetHandler(func(ctx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		if err := auth.Check(ctx, msg); err != nil {
			return nil, err
		}
		received = msg.UserID
		return &adapter.Reply{Content: "received"}, nil
	})
	type requestKey struct{}
	requestCtx, cancelRequest := context.WithCancel(context.WithValue(skill.WithAuthenticatedUser(context.Background(), "administrator"), requestKey{}, "request-only"))
	defer cancelRequest()
	if err := mgr.Start(requestCtx, inst.Name); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop(context.Background(), inst.Name)
	cancelRequest()
	if err := adp.ctx.Err(); err != nil {
		t.Fatalf("启动请求结束不应取消连接: %v", err)
	}
	reply, err := adp.handler(adp.ctx, &adapter.Message{Platform: adapter.PlatformDingtalk, UserID: "phone-parent", ChatID: "parent-chat", Content: "who are you"})
	if err != nil {
		t.Fatalf("手机身份应独立通过原网关校验: %v", err)
	}
	if received != "phone-parent" || reply == nil || reply.Content != "received" {
		t.Fatalf("消息归属或回复错误: user=%q reply=%+v", received, reply)
	}
	if principal := skill.AuthenticatedUserID(adp.ctx); principal != "" {
		t.Fatalf("连接继承了管理员身份: %q", principal)
	}
	if adp.ctx.Value(requestKey{}) != nil {
		t.Fatal("连接不应继承请求级值")
	}
	if err := mgr.Stop(context.Background(), inst.Name); err != nil {
		t.Fatal(err)
	}
	if adp.ctx.Err() != context.Canceled {
		t.Fatal("主动停止应取消连接")
	}
	stored, err := mgr.Get(context.Background(), inst.Name)
	if err != nil || stored.Status != StatusStopped {
		t.Fatalf("停止状态未保存: instance=%+v err=%v", stored, err)
	}
}
