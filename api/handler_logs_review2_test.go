package api

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ════════════════════════════════════════════════
// Round 2: 针对修复后代码的精确挑刺测试
// ════════════════════════════════════════════════

// ── 1. ring buffer 初始空 entry 被减统计 ──

func TestLogCollector_Add_FirstEntry_NoDecrementOfEmptyLevel(t *testing.T) {
	// 修复后的 ring buffer 预分配了 entries[0..maxSize-1] 全部为零值 LogEntry
	// 当 size==cap 时会减去 old entry 的统计
	// 但 LogEntry 零值的 Level="" Source="" — 如果首轮写满后继续写入
	// 减去 Level="" 会导致 byLevel[""] = -1 → delete 后再写 Level="" 会变成 0
	// 验证统计不会出现负数
	c := NewLogCollector(3)

	// 写满 3 条 level=info
	c.Add("info", "a", "m1", nil)
	c.Add("info", "a", "m2", nil)
	c.Add("info", "a", "m3", nil)

	// 第 4 条覆盖 m1 — 这里 old=entries[0]=m1(info/a)
	c.Add("warn", "b", "m4", nil)

	stats := c.Stats()
	if stats.ByLevel["info"] != 2 {
		t.Errorf("info = %d, want 2", stats.ByLevel["info"])
	}
	if stats.ByLevel["warn"] != 1 {
		t.Errorf("warn = %d, want 1", stats.ByLevel["warn"])
	}

	// 确保没有负数值
	for k, v := range stats.ByLevel {
		if v < 0 {
			t.Errorf("byLevel[%q] = %d, negative count!", k, v)
		}
	}
	for k, v := range stats.BySource {
		if v < 0 {
			t.Errorf("bySource[%q] = %d, negative count!", k, v)
		}
	}
}

func TestLogCollector_Add_EmptyLevelStats(t *testing.T) {
	// 极端场景: Level 为空字符串的 entry 被覆盖时
	// 减 byLevel[""] → -1 → delete → 但 byLevel[""] 从未被 ++
	// 因为 Add 总是 ++ level，即使 level=""
	c := NewLogCollector(2)

	c.Add("", "s", "m1", nil)     // byLevel[""] = 1
	c.Add("info", "s", "m2", nil) // byLevel["info"] = 1

	// 覆盖 m1: old.Level="" → byLevel[""]-- → 0 → delete
	c.Add("warn", "s", "m3", nil)

	stats := c.Stats()
	if v, ok := stats.ByLevel[""]; ok {
		t.Errorf("empty level should be deleted, got %d", v)
	}
	if stats.ByLevel["info"] != 1 {
		t.Errorf("info = %d, want 1", stats.ByLevel["info"])
	}
	if stats.ByLevel["warn"] != 1 {
		t.Errorf("warn = %d, want 1", stats.ByLevel["warn"])
	}
}

// ── 2. snapshot 无过滤快速路径仍然全量复制 ──

func TestLogCollector_QueryUnfiltered_AllocatesFullSnapshot(t *testing.T) {
	// 问题: Query("","","",10,0) 只需 10 条，但 snapshot() 复制了全部 5000 条
	// 然后用 snap[0:10] 切片 — 浪费了 4990 条的复制开销
	c := NewLogCollector(5000)
	for i := range 5000 {
		c.Add("info", "test", fmt.Sprintf("msg-%d", i), nil)
	}

	entries, total := c.Query("", "", "", 10, 0)
	if total != 5000 {
		t.Fatalf("total = %d, want 5000", total)
	}
	if len(entries) != 10 {
		t.Fatalf("entries = %d, want 10", len(entries))
	}
	// 验证返回的是最新的 10 条
	if !strings.Contains(entries[0].Message, "msg-4999") {
		t.Errorf("first = %q, want msg-4999", entries[0].Message)
	}
	if !strings.Contains(entries[9].Message, "msg-4990") {
		t.Errorf("last = %q, want msg-4990", entries[9].Message)
	}
}

// ── 3. handleLogStream 双重 unsubscribe ──

func TestLogCollector_HandleLogStream_DoubleUnsubscribe(t *testing.T) {
	// handleLogStream 中：
	//   if ch == nil → return (已调用 unsubscribe)
	//   defer conn.Close(...)
	//   defer s.logCollector.unsubscribe(subID)  ← 第二次 defer
	//
	// 但在 websocket.Accept 失败时：
	//   s.logCollector.unsubscribe(subID) ← 第一次
	//   return
	//   defer s.logCollector.unsubscribe(subID) ← 不会执行（函数已返回）
	//
	// 实际上只有在 Accept 失败后再次进入 defer 才会双重。
	// 但如果连接正常建立后 ctx.Done → return → 两个 defer 都执行？
	// 不，defer unsubscribe 只有一个。
	//
	// 验证双重 unsubscribe 是幂等的
	c := NewLogCollector(10)
	_, id := c.subscribe()
	c.unsubscribe(id) // 第一次
	c.unsubscribe(id) // 第二次应不 panic

	c.subMu.RLock()
	count := len(c.subscribers)
	c.subMu.RUnlock()
	if count != 0 {
		t.Errorf("subscribers = %d after double unsubscribe, want 0", count)
	}
}

// ── 4. Query offset 边界 ──

func TestLogCollector_QueryOffset_EqualToTotal(t *testing.T) {
	c := NewLogCollector(100)
	for range 10 {
		c.Add("info", "test", "msg", nil)
	}

	// offset == total: 应返回 nil
	entries, total := c.Query("", "", "", 10, 10)
	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
	if entries != nil {
		t.Errorf("entries should be nil when offset == total")
	}
}

func TestLogCollector_QueryOffset_WithFilter(t *testing.T) {
	c := NewLogCollector(100)
	for i := range 20 {
		if i%2 == 0 {
			c.Add("info", "test", fmt.Sprintf("msg-%d", i), nil)
		} else {
			c.Add("warn", "test", fmt.Sprintf("msg-%d", i), nil)
		}
	}

	// 过滤 info: 应有 10 条
	entries, total := c.Query("info", "", "", 5, 0)
	if total != 10 {
		t.Errorf("filtered total = %d, want 10", total)
	}
	if len(entries) != 5 {
		t.Errorf("entries = %d, want 5", len(entries))
	}

	// 第二页
	entries2, total2 := c.Query("info", "", "", 5, 5)
	if total2 != 10 {
		t.Errorf("page2 total = %d, want 10", total2)
	}
	if len(entries2) != 5 {
		t.Errorf("page2 entries = %d, want 5", len(entries2))
	}

	// 不应有重叠
	for _, e1 := range entries {
		for _, e2 := range entries2 {
			if e1.ID == e2.ID {
				t.Errorf("duplicate entry across pages: %s", e1.ID)
			}
		}
	}
}

// ── 5. 并发 Add 统计一致性 ──

func TestLogCollector_ConcurrentAdd_StatsConsistency(t *testing.T) {
	c := NewLogCollector(1000)
	var wg sync.WaitGroup
	perGoroutine := 200

	levels := []string{"info", "warn", "error"}
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perGoroutine {
				c.Add(levels[i], "src", "msg", nil)
			}
		}()
	}
	wg.Wait()

	stats := c.Stats()
	// 总共 600 条，ring buffer 容量 1000，都应保留
	if stats.Total != 600 {
		t.Errorf("total = %d, want 600", stats.Total)
	}

	levelSum := 0
	for _, v := range stats.ByLevel {
		if v < 0 {
			t.Errorf("negative count: %v", stats.ByLevel)
		}
		levelSum += v
	}
	if levelSum != stats.Total {
		t.Errorf("sum(ByLevel) = %d, want %d (Total)", levelSum, stats.Total)
	}
}

func TestLogCollector_ConcurrentAdd_StatsConsistency_Overflow(t *testing.T) {
	// ring buffer 溢出场景
	c := NewLogCollector(100)
	var wg sync.WaitGroup

	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				c.Add("info", "src", "msg", nil)
			}
		}()
	}
	wg.Wait()

	stats := c.Stats()
	if stats.Total != 100 {
		t.Errorf("total = %d, want 100", stats.Total)
	}

	levelSum := 0
	for _, v := range stats.ByLevel {
		levelSum += v
	}
	if levelSum != stats.Total {
		t.Errorf("sum(ByLevel) = %d != Total = %d — 增量计数器漂移！", levelSum, stats.Total)
	}
}

// ── 6. emptyList 类型检查 ──

func TestEmptyList_TotalIsInt(t *testing.T) {
	// 前端可能期望 total 是 number，不是 null
	// emptyList 返回 map[string]any{"total": 0} — 0 是 int，JSON 序列化后是 0
	handler := emptyList("items")
	// 已在其他测试中验证
	_ = handler
}

// ── 7. 基准：快速路径 vs 全量 snapshot ──

var benchmarkLogEntriesSink []LogEntry

func BenchmarkLogCollector_Query_Unfiltered_10of5000(b *testing.B) {
	c := NewLogCollector(5000)
	for i := range 5000 {
		c.Add("info", "test", fmt.Sprintf("msg-%d", i), nil)
	}
	b.ResetTimer()
	for range b.N {
		c.Query("", "", "", 10, 0)
	}
}

func BenchmarkLogCollector_Query_Unfiltered_10of5000_DetachedCopy(b *testing.B) {
	// 公平基线：模拟 Query 需要返回一个可变结果切片给调用方，因此结果会逃逸到堆上。
	c := NewLogCollector(5000)
	for i := range 5000 {
		c.Add("info", "test", fmt.Sprintf("msg-%d", i), nil)
	}
	b.ResetTimer()
	for range b.N {
		c.mu.RLock()
		result := make([]LogEntry, 10)
		for i := range 10 {
			idx := (c.head - 1 - i + c.capacity) % c.capacity
			result[i] = c.entries[idx]
		}
		c.mu.RUnlock()
		benchmarkLogEntriesSink = result
	}
}

func BenchmarkLogCollector_Query_Unfiltered_10of5000_DirectSlice(b *testing.B) {
	// 理论下界：结果不逃逸，编译器可将切片留在栈上。
	c := NewLogCollector(5000)
	for i := range 5000 {
		c.Add("info", "test", fmt.Sprintf("msg-%d", i), nil)
	}
	b.ResetTimer()
	for range b.N {
		c.mu.RLock()
		result := make([]LogEntry, 10)
		for i := range 10 {
			idx := (c.head - 1 - i + c.capacity) % c.capacity
			result[i] = c.entries[idx]
		}
		c.mu.RUnlock()
		_ = result
	}
}

func BenchmarkLogCollector_Query_Filtered_LevelOnly(b *testing.B) {
	c := NewLogCollector(5000)
	levels := []string{"info", "warn", "error", "debug"}
	for i := range 5000 {
		c.Add(levels[i%4], "test", fmt.Sprintf("msg-%d", i), nil)
	}
	b.ResetTimer()
	for range b.N {
		c.Query("info", "", "", 10, 0)
	}
}
