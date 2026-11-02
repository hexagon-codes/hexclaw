package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/storage"
	"github.com/hexagon-codes/hexclaw/streamstate"
)

type restStreamEngine interface {
	ProcessStream(context.Context, *adapter.Message) (<-chan *adapter.ReplyChunk, error)
}

// restChatRun 只连接传输、现有源流和显式停止信号，不拥有第二套任务状态。
type restChatRun struct {
	ctx                context.Context
	transport          context.Context
	registry           *streamstate.Registry
	control            *streamstate.RunControl
	requestID          string
	closeSignals       func()
	finishOnce         sync.Once
	streamOwnsLifetime bool
}

func (s *Server) beginRESTChat(ctx context.Context, msg *adapter.Message) (*restChatRun, error) {
	s.chatAdmissionMu.Lock()
	defer s.chatAdmissionMu.Unlock()
	requestID := msg.Metadata["request_id"]
	if requestID == "" {
		requestID = msg.ID
		msg.Metadata["request_id"] = requestID
	}
	if s.streamStates != nil {
		if _, exists := s.streamStates.GetStreamSnapshot(msg.UserID, requestID); exists {
			return nil, &originalChatRequest{reference: map[string]string{"source": "websocket", "snapshot_url": "/api/v1/streams/" + url.PathEscape(requestID) + "?source=websocket"}}
		}
	}
	if _, exists := s.restStreams.GetStreamSnapshot(msg.UserID, requestID); exists {
		return nil, &originalChatRequest{reference: map[string]string{"source": "rest", "snapshot_url": "/api/v1/streams/" + url.PathEscape(requestID) + "?source=rest"}}
	}
	if err := s.checkDurableChatRequest(ctx, msg.UserID, requestID); err != nil {
		return nil, err
	}
	signalCtx, cancelSignal := requestcancel.WithSignal(ctx)
	outputCtx, closeOutput := requestcancel.WithRetainedOutput(signalCtx, s.serviceLifecycleCtx)
	runCtx, cancelRun := context.WithCancelCause(outputCtx)
	control, err := s.restStreams.StartCancellable(msg.UserID, msg.SessionID, requestID, func(cause error) {
		// 独立信号先写入，使传输已断开的保留任务仍能识别主动停止。
		cancelSignal(cause)
		cancelRun(cause)
	})
	if err != nil {
		closeOutput()
		cancelSignal(nil)
		cancelRun(nil)
		return nil, err
	}
	return &restChatRun{ctx: runCtx, transport: ctx, registry: s.restStreams, control: control, requestID: requestID, closeSignals: func() { cancelSignal(nil); cancelRun(nil); closeOutput() }}, nil
}

func (r *restChatRun) finish(err error) {
	r.finishWithChunk(err, nil)
}

func (r *restChatRun) finishWithChunk(err error, terminal *adapter.ReplyChunk) {
	r.finishOnce.Do(func() {
		status := streamstate.StatusCompleted
		if requestcancel.IsExplicit(r.ctx) || errors.Is(err, context.Canceled) {
			status = streamstate.StatusCancelled
		} else if err != nil {
			status = streamstate.StatusErrored
		}
		r.control.FinishWithChunk(status, err, terminal)
		r.closeSignals()
	})
}

func (r *restChatRun) finishUnstarted() {
	if !r.streamOwnsLifetime {
		r.finish(errors.New("chat request ended before execution completed"))
	}
}

func (r *restChatRun) process(eng engine.Engine, msg *adapter.Message) (*adapter.Reply, error) {
	reply, err := eng.Process(r.ctx, msg)
	r.control.SetSession(msg.SessionID)
	if reply != nil {
		r.registry.Append(r.requestID, &adapter.ReplyChunk{Content: reply.Content, Metadata: reply.Metadata, Usage: reply.Usage, ToolCalls: reply.ToolCalls, Blocks: reply.Blocks, AssistantMessageID: reply.AssistantMessageID, BackendMessageID: reply.BackendMessageID, MessageID: reply.MessageID, ReasoningDisclosure: reply.ReasoningDisclosure})
	}
	r.finish(err)
	return reply, err
}

type restObservedStream struct {
	source restStreamEngine
	run    *restChatRun
}

type completedChatStream struct{ chunks <-chan *adapter.ReplyChunk }

func (s completedChatStream) ProcessStream(context.Context, *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
	return s.chunks, nil
}

func (s restObservedStream) ProcessStream(_ context.Context, msg *adapter.Message) (<-chan *adapter.ReplyChunk, error) {
	source, err := s.source.ProcessStream(s.run.ctx, msg)
	if err != nil {
		s.run.finish(err)
		return nil, err
	}
	s.run.control.SetSession(msg.SessionID)
	s.run.streamOwnsLifetime = true
	out := make(chan *adapter.ReplyChunk, 16)
	go func() {
		defer close(out)
		var terminal *adapter.ReplyChunk
		var runErr error
		// 即使 HTTP 消费方先退出也排空原源流；它关闭时引擎已执行运行完成器。
		for chunk := range source {
			if chunk == nil {
				continue
			}
			if chunk.Error != nil {
				runErr = chunk.Error
			}
			if chunk.Done {
				terminal = chunk
			} else {
				s.run.registry.Append(s.run.requestID, chunk)
			}
			select {
			case out <- chunk:
			case <-s.run.transport.Done():
			}
		}
		if terminal == nil && runErr == nil {
			if s.run.ctx.Err() != nil {
				runErr = s.run.ctx.Err()
			} else {
				runErr = errors.New("chat stream closed without a terminal frame")
			}
		}
		s.run.finishWithChunk(runErr, terminal)
	}()
	return out, nil
}

// checkDurableChatRequest 只按既有可信请求身份查证，不按消息或工具参数合并独立任务。
func (s *Server) checkDurableChatRequest(ctx context.Context, owner, requestID string) error {
	if provider, ok := s.store.(interface {
		ExternalToolExecutionRepository() storage.ExternalToolExecutionRepository
	}); ok {
		rows, err := provider.ExternalToolExecutionRepository().List(ctx, storage.ExternalToolExecutionFilter{OwnerID: owner, RequestID: requestID, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			record := rows[0]
			return &originalChatRequest{reference: map[string]string{"execution_id": record.ID, "request_id": record.RequestID, "session_id": record.SessionID, "phase": record.Phase, "outcome": record.Outcome}}
		}
	}
	if s.store != nil {
		message, err := s.store.GetMessage(ctx, requestID+":assistant")
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		if err == nil {
			session, sessionErr := s.store.GetSession(ctx, message.SessionID)
			if sessionErr != nil {
				return streamstate.ErrRequestConflict
			}
			if session.UserID == owner {
				return streamstate.ErrRequestConflict
			}
		}
	}
	return nil
}

type originalChatRequest struct{ reference map[string]string }

func (*originalChatRequest) Error() string {
	return "request identity is already in use; read its original receipt"
}
func (*originalChatRequest) Unwrap() error { return streamstate.ErrRequestConflict }

func writeChatRequestConflict(w http.ResponseWriter, msg *adapter.Message, err error) {
	reference := map[string]string{"request_id": msg.Metadata["request_id"], "session_id": msg.SessionID}
	var previous *originalChatRequest
	if errors.As(err, &previous) {
		for key, value := range previous.reference {
			reference[key] = value
		}
	}
	if reference["session_id"] != "" {
		reference["history_url"] = "/api/v1/sessions/" + url.PathEscape(reference["session_id"]) + "/messages"
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": "request identity is already in use or its original result is incomplete; read its original receipt", "receipt_ref": reference})
}
