package adapter

import "github.com/hexagon-codes/hexclaw/messagecontent"

// SandboxExecution 是已有沙箱执行报告的展示投影，不代表附件已交付。
type SandboxExecution struct {
	RunID     string            `json:"run_id"`
	Status    string            `json:"status"`
	Language  string            `json:"language,omitempty"`
	Command   []string          `json:"command,omitempty"`
	ExitCode  int               `json:"exit_code"`
	Timeout   bool              `json:"timeout"`
	Error     string            `json:"error,omitempty"`
	Artifacts []SandboxArtifact `json:"artifacts,omitempty"`
}

type SandboxArtifact struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	MIME string `json:"mime,omitempty"`
}

// RetrievalActivity 记录已实际执行的检索；命中不推导模型已采用。
type RetrievalActivity struct {
	Kind          string         `json:"kind"`
	Status        string         `json:"status"`
	Source        string         `json:"source,omitempty"`
	KnowledgeHits []KnowledgeHit `json:"knowledge_hits,omitempty"`
	MemoryHits    []MemoryHit    `json:"memory_hits,omitempty"`
	ResidentHits  []MemoryHit    `json:"resident_hits,omitempty"`
}

// ToolOrigin 保存本轮实际注册来源，不通过协议名称猜测服务归属。
type ToolOrigin struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	ServerName string `json:"server_name,omitempty"`
}

// MergeProcessCalls 更新同一调用的回执，保留开始顺序和已取得的详情。
func MergeProcessCalls(current, incoming []ToolCall) []ToolCall {
	out := append([]ToolCall(nil), current...)
	for _, call := range incoming {
		found := false
		for i := range out {
			if out[i].ID != call.ID {
				continue
			}
			if call.Arguments == "" {
				call.Arguments = out[i].Arguments
			}
			if call.Result == "" {
				call.Result = out[i].Result
			}
			if call.MessageContent == nil {
				call.MessageContent = out[i].MessageContent
			}
			if call.Execution == nil {
				call.Execution = out[i].Execution
			}
			if call.Origin == nil {
				call.Origin = out[i].Origin
			}
			out[i] = call
			found = true
			break
		}
		if !found {
			out = append(out, call)
		}
	}
	return out
}

// AppendProcessChunk 仅处理通过路由公开约束的内容；快照优先于同帧增量。
func AppendProcessChunk(current []Block, chunk *ReplyChunk) []Block {
	if len(chunk.Blocks) > 0 {
		return append([]Block(nil), chunk.Blocks...)
	}
	out := append([]Block(nil), current...)
	appendText := func(kind, text string) {
		if text == "" {
			return
		}
		if len(out) == 0 || out[len(out)-1].Type != kind {
			out = append(out, Block{Type: kind})
		}
		last := &out[len(out)-1]
		if kind == "thinking" {
			last.Thinking += text
		} else {
			last.Text += text
			if source, err := messagecontent.New(messagecontent.ProducerChat, "und", last.Text, nil); err == nil {
				last.MessageContent = &source
			}
		}
	}
	if chunk.ReasoningDisclosure.Visibility == ReasoningVisible {
		appendText("thinking", chunk.Reasoning)
	}
	appendText("text", chunk.Content)
	if event := chunk.RuntimeEvent; event != nil && event.Kind == RuntimeEventToolStarted {
		for _, b := range out {
			if b.Type == "tool_use" && b.ID == event.ToolCallID {
				return out
			}
		}
		b := Block{Type: "tool_use", ID: event.ToolCallID, Name: event.ToolName}
		for _, call := range chunk.ToolCalls {
			if call.ID == b.ID {
				b.Input = call.Arguments
				break
			}
		}
		out = append(out, b)
	}
	return out
}

func WithoutThinkingBlocks(blocks []Block) []Block {
	out := make([]Block, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "thinking" {
			out = append(out, b)
		}
	}
	return out
}

// mergeProcessRetrievals 补齐终态准备阶段回执，同 ID 更新，保留实时内容顺序。
func mergeProcessRetrievals(current, final []Block) []Block {
	out := append([]Block(nil), current...)
	positions := make(map[string]int)
	insertAt := 0
	for i, block := range out {
		if block.Type == "retrieval" {
			positions[block.ID] = i
			insertAt = i + 1
		}
	}
	var added []Block
	for _, block := range final {
		if block.Type != "retrieval" {
			continue
		}
		if i, exists := positions[block.ID]; exists {
			out[i] = block
		} else {
			positions[block.ID] = len(out)
			out = append(out, block)
			added = append(added, block)
		}
	}
	// 新回执同属准备阶段，位于已记录召回之后、模型公开输出之前。
	base := out[:len(out)-len(added)]
	result := append([]Block(nil), base[:insertAt]...)
	result = append(result, out[len(base):]...)
	return append(result, base[insertAt:]...)
}

func hasMissingProcessCalls(current, final []Block) bool {
	seen := make(map[string]bool)
	for _, block := range current {
		if block.Type == "tool_use" {
			seen[block.ID] = true
		}
	}
	for _, block := range final {
		if block.Type == "tool_use" && !seen[block.ID] {
			return true
		}
	}
	return false
}
