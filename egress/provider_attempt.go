package egress

import (
	"context"
	"errors"
	"sync/atomic"
)

var ErrDenied = errors.New("egress request blocked before provider dispatch")
var ErrProviderNotSent = errors.New("provider request was not sent")

type providerAttemptKey struct{}

// ProviderAttempt 仅证明已进入 Provider，不把客户端失败解释为未发送。
type ProviderAttempt struct{ entered atomic.Bool }

// WithProviderAttempt 为一次业务操作记录实际出口，不继承先前操作的状态。
func WithProviderAttempt(ctx context.Context) (context.Context, *ProviderAttempt) {
	a := &ProviderAttempt{}
	return context.WithValue(ctx, providerAttemptKey{}, a), a
}

// MarkProviderAttempt 在出口检查通过且调用 Provider 前记录；后续拒绝不能清除此状态。
func MarkProviderAttempt(ctx context.Context) {
	if a, ok := ctx.Value(providerAttemptKey{}).(*ProviderAttempt); ok {
		a.entered.Store(true)
	}
}

// Reconcile 只将明确出口拒绝且未进入 Provider 的操作归为未发送。
func (a *ProviderAttempt) Reconcile(err error) error {
	if err != nil && a != nil && !a.entered.Load() && errors.Is(err, ErrDenied) {
		return errors.Join(ErrProviderNotSent, err)
	}
	return err
}
