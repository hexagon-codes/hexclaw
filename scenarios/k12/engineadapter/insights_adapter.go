package engineadapter

import (
	"context"
	"errors"

	"github.com/hexagon-codes/hexclaw/memory"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// memoryWriter 是 Insights adapter 依赖的最小写入接口（*memory.FileMemory 满足）。
type memoryWriter interface {
	SaveStructuredEvent(eventID, content, memType, source, role string, meta memory.EntryMeta) error
}

// ReviseWeakness 不追加第二条同源薄弱信号，也不把撤回解释为掌握证据。
func (a *InsightsAdapter) ReviseWeakness(_ context.Context, eventID string, revision int, agentName, knowledgePoint, note string) error {
	if a.mem == nil || agentName == "" {
		return nil
	}
	writer, ok := a.mem.(interface {
		ReviseStructuredEvent(string, int, string, string, string, string, memory.EntryMeta) error
	})
	if !ok {
		return errors.New("memory writer does not support insight corrections")
	}
	if note != "" {
		note = "[学情] " + note
	}
	return writer.ReviseStructuredEvent(eventID, revision, note, "fact", "学情", agentName, memory.EntryMeta{Subject: knowledgePoint})
}

// InsightsAdapter 把学情薄弱信号写进 hexclaw 的 FileMemory。
type InsightsAdapter struct{ mem memoryWriter }

// NewInsightsAdapter 创建 adapter。mem 通常是 main.go 的 fileMem。
func NewInsightsAdapter(mem memoryWriter) *InsightsAdapter { return &InsightsAdapter{mem: mem} }

var _ usecase.Insights = (*InsightsAdapter)(nil)

// WriteWeakness 写一条学情画像信号。
//
//   - role = agentName → 多孩隔离（memory 按 role 分目录）；
//   - memType = "fact" → 留在 role 目录（避免被 global memType 路由到 _global，丢隔离）；
//   - "[学情] " 前缀 + source="学情" → 携带学情标签；
//   - Subject = knowledgePoint → 结构化召回槽。
//
// 错题本身不入记忆（AP-3）；这里只写"薄弱点"画像。
func (a *InsightsAdapter) WriteWeakness(_ context.Context, eventID, agentName, knowledgePoint, note string) error {
	if a.mem == nil || agentName == "" {
		return nil
	}
	return a.mem.SaveStructuredEvent(eventID, "[学情] "+note, "fact", "学情", agentName, memory.EntryMeta{Subject: knowledgePoint})
}
