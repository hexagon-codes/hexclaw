package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/streamx"
	"github.com/hexagon-codes/hexagon/testing/mock"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

type terminalErrorParser struct{}

func (terminalErrorParser) Parse([]byte) (*streamx.Chunk, error) {
	return nil, errors.New("terminal stream failure")
}
func (terminalErrorParser) IsDone([]byte) bool { return false }

func TestPipeStreamSurfacesTerminalErrorBeforePersist(t *testing.T) {
	eng := newEngineWithProvider(t, mock.NewLLMProvider("test"))
	ctx := context.Background()
	sessionID := "sess-pipe-stream-error"
	if err := eng.store.CreateSession(ctx, &storage.Session{ID: sessionID, UserID: "user", Platform: "web"}); err != nil {
		t.Fatal(err)
	}

	stream := llm.NewStream(strings.NewReader("data: x\n\n"), llm.StreamOpenAIFormat)
	stream.SetParser(terminalErrorParser{})
	out := make(chan *adapter.ReplyChunk, 8)
	go eng.pipeStream(ctx, out, stream, sessionID, &adapter.Message{
		ID: "req-pipe-stream-error", SessionID: sessionID,
	}, nil, llm.CompletionRequest{}, "test", "mock-model", "")

	var gotError bool
	for chunk := range out {
		if chunk.Error != nil {
			gotError = true
		}
	}
	if !gotError {
		t.Fatal("terminal stream error was not surfaced")
	}
	records, err := eng.store.ListMessages(ctx, sessionID, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Role == "assistant" {
			t.Fatalf("failed stream persisted assistant message: %+v", record)
		}
	}
}
