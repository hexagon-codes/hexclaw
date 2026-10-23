// Package requestcancel 区分用户主动停止与请求传输中断。
package requestcancel

import (
	"context"
	"errors"
	"sync/atomic"
)

type userCancellation struct{}

func (userCancellation) Error() string { return "request cancelled by user" }
func (userCancellation) Unwrap() error { return context.Canceled }

// ErrUserCancelled 只由可信会话入口用于用户主动停止，保留通用取消错误语义。
var ErrUserCancelled error = userCancellation{}

type signalKey struct{}

type requestSignal struct {
	context.Context
	retained atomic.Bool
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
	if signal, ok := ctx.Value(signalKey{}).(*requestSignal); ok {
		signal.retained.Store(true)
	}
}
func IsRetained(ctx context.Context) bool {
	signal, ok := ctx.Value(signalKey{}).(*requestSignal)
	return ok && signal.retained.Load()
}

// IsExplicit 保留首次取消原因，并从独立信号识别断线后的主动停止。
func IsExplicit(ctx context.Context) bool {
	if errors.Is(context.Cause(ctx), ErrUserCancelled) {
		return true
	}
	signal := Signal(ctx)
	return signal != nil && errors.Is(context.Cause(signal), ErrUserCancelled)
}
