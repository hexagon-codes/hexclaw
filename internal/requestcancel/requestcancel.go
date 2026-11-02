// Package requestcancel 区分用户主动停止与请求传输中断。
package requestcancel

import (
	"context"
	"errors"
	"sync"
)

type userCancellation struct{}

func (userCancellation) Error() string { return "request cancelled by user" }
func (userCancellation) Unwrap() error { return context.Canceled }

// ErrUserCancelled 只由可信会话入口用于用户主动停止，保留通用取消错误语义。
var ErrUserCancelled error = userCancellation{}

type signalKey struct{}

type requestSignal struct {
	context.Context
	mu           sync.Mutex
	retained     bool
	transport    context.Context
	outputCancel context.CancelCauseFunc
}

// WithSignal 建立独立于传输生命周期的停止信号；调用方在处理结束时以nil关闭。
func WithSignal(parent context.Context) (context.Context, context.CancelCauseFunc) {
	signal, cancel := context.WithCancelCause(context.Background())
	return context.WithValue(parent, signalKey{}, &requestSignal{Context: signal}), cancel
}

// Signal 返回同一请求的主动停止信号；未接入的入口返回nil。
func Signal(ctx context.Context) context.Context {
	signal, _ := ctx.Value(signalKey{}).(*requestSignal)
	if signal == nil {
		return nil
	}
	return signal.Context
}

// Retain 标记已持久接纳的工作，传输断线不能把业务快照改为取消终态。
func Retain(ctx context.Context) {
	_ = RetainWith(ctx, nil)
}
func IsRetained(ctx context.Context) bool {
	signal, ok := ctx.Value(signalKey{}).(*requestSignal)
	if !ok {
		return false
	}
	signal.mu.Lock()
	defer signal.mu.Unlock()
	return signal.retained
}

// RetainWith 把实际接纳与传输断连串行交接；接纳失败不能延长原请求。
// accept 仅承担已有的持久接纳，不在这里启动外部执行或新任务状态机。
func RetainWith(ctx context.Context, accept func() error) error {
	signal, ok := ctx.Value(signalKey{}).(*requestSignal)
	if !ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		if accept != nil {
			return accept()
		}
		return nil
	}
	signal.mu.Lock()
	defer signal.mu.Unlock()
	if signal.Err() != nil {
		return context.Cause(signal)
	}
	if !signal.retained && signal.transport != nil && signal.transport.Err() != nil {
		cause := context.Cause(signal.transport)
		signal.outputCancel(cause)
		return cause
	}
	if accept != nil {
		if err := accept(); err != nil {
			return err
		}
	}
	signal.retained = true
	return nil
}

// WithRetainedOutput 保留已接纳运行的输出通道；接纳前仍受原HTTP断连取消。
// 同一 RetainWith 临界区决定交接，不能因异步取消回调尚未运行而误接纳。
func WithRetainedOutput(parent, host context.Context) (context.Context, func()) {
	signal, ok := parent.Value(signalKey{}).(*requestSignal)
	if !ok {
		return parent, func() {}
	}
	output, cancel := context.WithCancelCause(context.WithoutCancel(parent))
	signal.mu.Lock()
	signal.transport, signal.outputCancel = parent, cancel
	signal.mu.Unlock()
	stopTransport := context.AfterFunc(parent, func() {
		signal.mu.Lock()
		defer signal.mu.Unlock()
		if !signal.retained {
			cancel(context.Cause(parent))
		}
	})
	stopHost := func() bool { return true }
	if host != nil {
		stopHost = context.AfterFunc(host, func() { cancel(context.Cause(host)) })
	}
	return output, func() { stopTransport(); stopHost(); cancel(nil) }
}

// IsExplicit 保留首次取消原因，并从独立信号识别断线后的主动停止。
func IsExplicit(ctx context.Context) bool {
	if errors.Is(context.Cause(ctx), ErrUserCancelled) {
		return true
	}
	signal := Signal(ctx)
	return signal != nil && errors.Is(context.Cause(signal), ErrUserCancelled)
}
