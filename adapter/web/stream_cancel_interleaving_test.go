package web

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
)

// 在指定检查读到 nil 后执行真实取消，固定不同检查点之间的交错。
type cancelAfterCheckContext struct {
	context.Context
	cancel context.CancelCauseFunc
	after  int32
	checks atomic.Int32
}

func (c *cancelAfterCheckContext) Err() error {
	err := c.Context.Err()
	if c.checks.Add(1) == c.after {
		c.cancel(requestcancel.ErrUserCancelled)
	}
	return err
}

func TestWebAdapter_CancelBetweenChunkChecksRetainsRealTerminal(t *testing.T) {
	for _, after := range []int32{1, 2, 3} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			a := New()
			a.streams.Start("owner", "session", "request")
			base, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			ctx := &cancelAfterCheckContext{Context: base, cancel: cancel, after: after}
			chunks := make(chan *adapter.ReplyChunk, 2)
			chunks <- &adapter.ReplyChunk{Content: "preview"}
			chunks <- &adapter.ReplyChunk{
				Content: "actual failure output", Done: true, Error: context.Canceled,
				MessageContent: canonicalReplyContent("actual failure output", nil),
				ToolCalls:      []adapter.ToolCall{{ID: "observed-call", Name: "shell", Status: "error", Result: "actual failure output"}},
			}
			close(chunks)
			_ = a.sendStreamWithIDs(ctx, "no-connection", "session", "request", chunks)
			a.streams.Cancel("request")
			snapshot, ok := a.streams.GetStreamSnapshot("owner", "request")
			if !ok || !snapshot.Done || snapshot.Content != "actual failure output" || len(snapshot.ToolCalls) != 1 || snapshot.ToolCalls[0].Status != "error" {
				t.Fatalf("real terminal lost at check %d: %#v", after, snapshot)
			}
			if len(chunks) != 0 {
				t.Fatal("terminal was left for the caller to discard")
			}
		})
	}
}
