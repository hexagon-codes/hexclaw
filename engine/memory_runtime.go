package engine

import "context"

// memoryRuntimeEnabled 返回当前自动记忆行为的总开关。
// 文件对象保持挂接以保留手动管理和既有数据；启停共用 FileMemory 配置，不产生第二事实源。
func (e *ReActEngine) memoryRuntimeEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.fileMem != nil && (e.cfg == nil || e.cfg.FileMemory.Enabled)
}

// ingestEnabledMemoryFacts 在总开关的同一次运行快照下提交自动抽取结果。
// 关闭操作等待已开始的短写入完成，关闭回执返回后迟到的抽取结果不能继续落库。
func (e *ReActEngine) ingestEnabledMemoryFacts(ctx context.Context, facts []extractedFact, role string) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.fileMem == nil || (e.cfg != nil && !e.cfg.FileMemory.Enabled) {
		return 0
	}
	return e.ingestExtractedFacts(ctx, facts, role)
}
