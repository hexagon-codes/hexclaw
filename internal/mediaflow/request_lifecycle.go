package mediaflow

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/toolkit/util/logger"
)

type requestLifetimeKey struct{}
type requestOperation struct{ owner, id string }
type requestLifetime struct {
	mu         sync.Mutex
	retained   bool
	cancelled  bool
	cancel     context.CancelCauseFunc
	parent     context.Context
	operations map[requestOperation]struct{}
}

// RequestContext 在接纳后忽略断连，显式停止仍取消本回合并登记操作取消意图。
func (s *Service) RequestContext(parent context.Context) (context.Context, func()) {
	owned, cancel := context.WithCancelCause(context.WithoutCancel(parent))
	state := &requestLifetime{cancel: cancel, parent: parent, operations: make(map[requestOperation]struct{})}
	transportFinished := make(chan struct{})
	stopTransport := context.AfterFunc(parent, func() {
		defer close(transportFinished)
		state.mu.Lock()
		defer state.mu.Unlock()
		state.cancelFromParent(s)
	})
	var stopExplicit func() bool
	var explicitFinished chan struct{}
	if signal := requestcancel.Signal(parent); signal != nil {
		explicitFinished = make(chan struct{})
		stopExplicit = context.AfterFunc(signal, func() {
			defer close(explicitFinished)
			state.mu.Lock()
			defer state.mu.Unlock()
			state.cancelFromParent(s)
		})
	}
	stopHost := context.AfterFunc(s.ctx, func() { cancel(context.Cause(s.ctx)) })
	ctx := context.WithValue(owned, requestLifetimeKey{}, state)
	var cleanupOnce sync.Once
	return ctx, func() {
		cleanupOnce.Do(func() {
			if !stopTransport() {
				<-transportFinished
			}
			if stopExplicit != nil && !stopExplicit() {
				<-explicitFinished
			}
			// 取消信号可能已写入，而异步回调尚未开始；收口时不能丢掉停止意图。
			state.mu.Lock()
			state.cancelFromParent(s)
			state.mu.Unlock()
			stopHost()
			cancel(nil)
		})
	}
}

// cancelFromParent 由生命周期锁串行调用，登记只使用接纳时冻结的身份。
func (state *requestLifetime) cancelFromParent(s *Service) {
	if requestcancel.IsExplicit(state.parent) {
		state.cancelled = true
		state.cancel(requestcancel.ErrUserCancelled)
		state.cancelOperations(s)
	} else if state.parent.Err() != nil && !state.retained {
		state.cancelled = true
		state.cancel(context.Cause(state.parent))
	}
}

func (state *requestLifetime) cancelOperations(s *Service) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
	defer cancel()
	for operation := range state.operations {
		for {
			_, err := s.Cancel(ctx, operation.owner, operation.id)
			if err == nil {
				delete(state.operations, operation)
				break
			}
			if !errors.Is(err, ErrConflict) || ctx.Err() != nil {
				logger.Warn("media explicit cancellation persistence failed", "operation_id", operation.id, "error", err)
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
}

// acceptRequestOperation 将持久接纳和取消登记串行，避免取消恰逢创建时留下未登记操作。
func (s *Service) acceptRequestOperation(ctx context.Context, accept func() (*Operation, error)) (*Operation, error) {
	state, _ := ctx.Value(requestLifetimeKey{}).(*requestLifetime)
	if state == nil {
		return accept()
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.cancelFromParent(s)
	if state.cancelled || ctx.Err() != nil {
		return nil, context.Canceled
	}
	op, err := accept()
	if err != nil {
		return op, err
	}
	state.operations[requestOperation{owner: op.Owner, id: op.ID}] = struct{}{}
	state.retained = true
	requestcancel.Retain(state.parent)
	state.cancelFromParent(s)
	if state.cancelled || ctx.Err() != nil {
		return op, context.Canceled
	}
	return op, nil
}

// RetainRequest 只由实际媒体调用入口接纳任务；与断连取消串行决定归属。
func RetainRequest(ctx context.Context) error {
	state, _ := ctx.Value(requestLifetimeKey{}).(*requestLifetime)
	if state == nil {
		return ctx.Err()
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.cancelled || ctx.Err() != nil || requestcancel.IsExplicit(state.parent) || !state.retained && state.parent.Err() != nil {
		state.cancelled = true
		cause := context.Cause(state.parent)
		if requestcancel.IsExplicit(state.parent) {
			cause = requestcancel.ErrUserCancelled
		}
		state.cancel(cause)
		return context.Canceled
	}
	state.retained = true
	return nil
}

func CancelRequest(ctx context.Context) {
	if state, ok := ctx.Value(requestLifetimeKey{}).(*requestLifetime); ok {
		state.mu.Lock()
		state.cancelled = true
		state.cancel(nil)
		state.mu.Unlock()
	}
}
