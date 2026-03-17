package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hexagon-codes/toolkit/lang/stringx"
)

// ════════════════════════════════════════════════
// 1. stringx.Truncate 边界测试（验证 toolkit 行为符合预期）
// ════════════════════════════════════════════════

func TestTruncate_EmptyString(t *testing.T) {
	if got := stringx.Truncate("", 10); got != "" {
		t.Errorf("Truncate empty = %q, want empty", got)
	}
}

func TestTruncate_ExactLength(t *testing.T) {
	if got := stringx.Truncate("hello", 5); got != "hello" {
		t.Errorf("Truncate exact = %q, want %q", got, "hello")
	}
}

func TestTruncate_ShorterThanMax(t *testing.T) {
	if got := stringx.Truncate("hi", 10); got != "hi" {
		t.Errorf("Truncate short = %q, want %q", got, "hi")
	}
}

func TestTruncate_LongerThanMax(t *testing.T) {
	got := stringx.Truncate("hello world", 8)
	if got != "hello..." {
		t.Errorf("Truncate long = %q, want %q", got, "hello...")
	}
}

func TestTruncate_ChineseUTF8(t *testing.T) {
	s := "你好世界这是一个测试"
	got := stringx.Truncate(s, 7)
	if !utf8.ValidString(got) {
		t.Fatalf("Truncate produced invalid UTF-8: %q", got)
	}
	if got != "你好世界..." {
		t.Errorf("Truncate chinese = %q, want %q", got, "你好世界...")
	}
}

func TestTruncate_ZeroMaxLen(t *testing.T) {
	got := stringx.Truncate("hello", 0)
	if got != "" {
		t.Errorf("Truncate(s, 0) = %q, should return empty string", got)
	}
}

func TestTruncate_NegativeMaxLen(t *testing.T) {
	got := stringx.Truncate("hello", -1)
	if got != "" {
		t.Errorf("Truncate(s, -1) = %q, should return empty string", got)
	}
}

func TestTruncate_MaxLen3(t *testing.T) {
	got := stringx.Truncate("hello world", 3)
	if got != "hel" {
		t.Errorf("Truncate(s, 3) = %q, want %q", got, "hel")
	}
}

func TestTruncate_MaxLen_1(t *testing.T) {
	got := stringx.Truncate("hello world", 1)
	if got != "h" {
		t.Errorf("Truncate(s, 1) = %q, want %q", got, "h")
	}
}

// ════════════════════════════════════════════════
// 2. LogCollector ring buffer 内存测试
// ════════════════════════════════════════════════

func TestLogCollector_RingBufferFixedCapacity(t *testing.T) {
	c := NewLogCollector(10)

	// 写入大量大字符串
	bigMsg := strings.Repeat("x", 1024*1024) // 1MB per entry
	for range 100 {
		c.Add("info", "test", bigMsg, nil)
	}

	runtime.GC()

	c.mu.RLock()
	if c.size != 10 {
		t.Errorf("size = %d, want 10", c.size)
	}
	// 固定数组 ring buffer: cap 始终等于初始容量
	if cap(c.entries) != 10 {
		t.Errorf("cap = %d, want 10 (fixed ring buffer)", cap(c.entries))
	}
	c.mu.RUnlock()
}

func TestLogCollector_RingBufferCapStable(t *testing.T) {
	c := NewLogCollector(100)

	for round := range 10 {
		for i := range 200 {
			c.Add("info", "test", fmt.Sprintf("msg-%d-%d", round, i), nil)
		}
	}

	c.mu.RLock()
	currentCap := cap(c.entries)
	c.mu.RUnlock()

	if currentCap != 100 {
		t.Errorf("cap = %d, want 100 (fixed ring buffer should never grow)", currentCap)
	}
}

// ════════════════════════════════════════════════
// 3. Query 逻辑测试
// ════════════════════════════════════════════════

func TestLogCollector_QueryKeywordCaseSensitive(t *testing.T) {
	c := NewLogCollector(100)
	c.Add("info", "test", "Hello World", nil)
	c.Add("info", "test", "hello world", nil)
	c.Add("info", "test", "HELLO WORLD", nil)

	entries, total := c.Query("", "", "hello", 100, 0)
	if total != 3 {
		t.Errorf("case-insensitive search total = %d, want 3", total)
	}
	if len(entries) != 3 {
		t.Errorf("entries len = %d, want 3", len(entries))
	}
}

func TestLogCollector_QueryPagination(t *testing.T) {
	c := NewLogCollector(100)
	for i := range 50 {
		c.Add("info", "test", fmt.Sprintf("msg-%02d", i), nil)
	}

	// 第一页
	entries, total := c.Query("", "", "", 10, 0)
	if total != 50 {
		t.Fatalf("total = %d, want 50", total)
	}
	if len(entries) != 10 {
		t.Fatalf("page1 len = %d, want 10", len(entries))
	}
	// 最新的在前面（逆序）
	if !strings.Contains(entries[0].Message, "msg-49") {
		t.Errorf("first entry = %q, want msg-49 (newest first)", entries[0].Message)
	}

	// offset 超出范围
	entries, total = c.Query("", "", "", 10, 100)
	if total != 50 {
		t.Errorf("total for over-offset = %d, want 50", total)
	}
	if entries != nil {
		t.Errorf("entries for over-offset should be nil, got %d items", len(entries))
	}
}

func TestLogCollector_QueryUnfiltered_FastPath(t *testing.T) {
	c := NewLogCollector(100)
	for i := range 50 {
		c.Add("info", "test", fmt.Sprintf("msg-%d", i), nil)
	}

	// 无过滤条件应走快速路径
	entries, total := c.Query("", "", "", 10, 0)
	if total != 50 {
		t.Errorf("total = %d, want 50", total)
	}
	if len(entries) != 10 {
		t.Errorf("entries = %d, want 10", len(entries))
	}
}

func TestLogCollector_QueryWithLevel(t *testing.T) {
	c := NewLogCollector(100)
	c.Add("info", "test", "msg1", nil)
	c.Add("warn", "test", "msg2", nil)
	c.Add("error", "test", "msg3", nil)

	entries, total := c.Query("warn", "", "", 100, 0)
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(entries) != 1 || entries[0].Level != "warn" {
		t.Errorf("unexpected entries: %+v", entries)
	}
}

// ════════════════════════════════════════════════
// 4. 并发竞态条件测试
// ════════════════════════════════════════════════

func TestLogCollector_ConcurrentAddAndQuery(t *testing.T) {
	c := NewLogCollector(1000)
	var wg sync.WaitGroup

	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 100 {
				c.Add("info", fmt.Sprintf("src-%d", i), fmt.Sprintf("msg-%d", j), nil)
			}
		}()
	}

	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				c.Query("info", "", "", 100, 0)
				c.Stats()
			}
		}()
	}

	wg.Wait()
}

func TestLogCollector_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	c := NewLogCollector(100)
	var wg sync.WaitGroup

	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, id := c.subscribe()
			if ch == nil {
				return
			}
			time.Sleep(time.Millisecond)
			c.unsubscribe(id)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			c.Add("info", "test", "msg", nil)
		}
	}()

	wg.Wait()
}

// ════════════════════════════════════════════════
// 5. Pub/Sub 问题测试
// ════════════════════════════════════════════════

func TestLogCollector_UnsubscribeRaceWithAdd(t *testing.T) {
	c := NewLogCollector(100)
	var wg sync.WaitGroup

	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, id := c.subscribe()
			if ch == nil {
				return
			}
			go c.unsubscribe(id)
			c.Add("info", "test", "msg", nil)
		}()
	}

	wg.Wait()
}

func TestLogCollector_MaxSubscribers(t *testing.T) {
	c := NewLogCollector(100)
	var ids []uint64

	// 应该最多允许 maxSubscribers 个
	for range c.maxSubscribers + 10 {
		ch, id := c.subscribe()
		if ch != nil {
			ids = append(ids, id)
		}
	}

	if len(ids) != c.maxSubscribers {
		t.Errorf("accepted %d subscribers, want %d (max limit)", len(ids), c.maxSubscribers)
	}

	// 清理
	for _, id := range ids {
		c.unsubscribe(id)
	}
}

func TestLogCollector_SlowSubscriberBlocksBroadcast(t *testing.T) {
	c := NewLogCollector(100)
	ch, id := c.subscribe()
	if ch == nil {
		t.Fatal("subscribe failed")
	}

	// 不从 ch 中消费任何数据
	start := time.Now()
	for range 200 { // 超过 channel 缓冲 64
		c.Add("info", "test", "msg", nil)
	}
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("Add blocked for %v with slow subscriber — should be non-blocking", elapsed)
	}

	c.unsubscribe(id)
}

func TestLogCollector_DoubleUnsubscribe(t *testing.T) {
	c := NewLogCollector(100)
	_, id := c.subscribe()

	// 第二次 unsubscribe 不应 panic
	c.unsubscribe(id)
	c.unsubscribe(id) // 不应 panic（幂等）
}

// ════════════════════════════════════════════════
// 6. Clear 后的行为测试
// ════════════════════════════════════════════════

func TestLogCollector_ClearThenQuery(t *testing.T) {
	c := NewLogCollector(100)
	for i := range 50 {
		c.Add("info", "test", fmt.Sprintf("msg-%d", i), nil)
	}

	c.Clear()

	entries, total := c.Query("", "", "", 100, 0)
	if total != 0 {
		t.Errorf("total after clear = %d, want 0", total)
	}
	if entries != nil {
		t.Errorf("entries after clear should be nil")
	}
}

func TestLogCollector_ClearReleasesReferences(t *testing.T) {
	c := NewLogCollector(100)
	for range 100 {
		c.Add("info", "test", strings.Repeat("x", 10240), nil)
	}

	c.Clear()

	c.mu.RLock()
	// 固定数组: cap 保持不变（这是预期的）
	if cap(c.entries) != 100 {
		t.Errorf("cap should stay 100, got %d", cap(c.entries))
	}
	// 但所有元素应被清零
	allEmpty := true
	for _, e := range c.entries {
		if e.Message != "" {
			allEmpty = false
			break
		}
	}
	c.mu.RUnlock()

	if !allEmpty {
		t.Error("Clear should zero all entry references")
	}
}

func TestLogCollector_ClearResetsStats(t *testing.T) {
	c := NewLogCollector(100)
	c.Add("info", "chat", "msg", nil)
	c.Add("warn", "system", "msg", nil)

	c.Clear()

	stats := c.Stats()
	if stats.Total != 0 {
		t.Errorf("total = %d, want 0", stats.Total)
	}
	if len(stats.ByLevel) != 0 {
		t.Errorf("ByLevel should be empty, got %v", stats.ByLevel)
	}
	if len(stats.BySource) != 0 {
		t.Errorf("BySource should be empty, got %v", stats.BySource)
	}
}

// ════════════════════════════════════════════════
// 7. Stats 测试
// ════════════════════════════════════════════════

func TestLogCollector_StatsEmpty(t *testing.T) {
	c := NewLogCollector(100)
	stats := c.Stats()
	if stats.Total != 0 {
		t.Errorf("total = %d, want 0", stats.Total)
	}
	if stats.RequestsPerMinute != 0 {
		t.Errorf("rpm = %f, want 0", stats.RequestsPerMinute)
	}
}

func TestLogCollector_StatsAccuracy(t *testing.T) {
	c := NewLogCollector(100)
	c.Add("info", "chat", "msg1", nil)
	c.Add("warn", "chat", "msg2", nil)
	c.Add("error", "system", "msg3", nil)
	c.Add("info", "", "msg4", nil)

	stats := c.Stats()
	if stats.Total != 4 {
		t.Fatalf("total = %d, want 4", stats.Total)
	}
	if stats.ByLevel["info"] != 2 {
		t.Errorf("info count = %d, want 2", stats.ByLevel["info"])
	}
	if stats.ByLevel["warn"] != 1 {
		t.Errorf("warn count = %d, want 1", stats.ByLevel["warn"])
	}
	if stats.BySource[""] != 0 {
		t.Errorf("empty source should not be counted, got %d", stats.BySource[""])
	}
}

func TestLogCollector_StatsAfterOverflow(t *testing.T) {
	// 验证 ring buffer 溢出后统计仍然正确（增量计数器减旧加新）
	c := NewLogCollector(5)

	c.Add("info", "a", "m1", nil)
	c.Add("warn", "b", "m2", nil)
	c.Add("info", "a", "m3", nil)
	c.Add("error", "c", "m4", nil)
	c.Add("info", "a", "m5", nil)

	// 此时满了，继续写入会覆盖旧的
	c.Add("debug", "d", "m6", nil) // 覆盖 m1 (info/a)
	c.Add("debug", "d", "m7", nil) // 覆盖 m2 (warn/b)

	stats := c.Stats()
	if stats.Total != 5 {
		t.Fatalf("total = %d, want 5", stats.Total)
	}
	// info: m3, m5 = 2 (m1 被覆盖了)
	if stats.ByLevel["info"] != 2 {
		t.Errorf("info = %d, want 2", stats.ByLevel["info"])
	}
	// warn: m2 被覆盖了 = 0
	if stats.ByLevel["warn"] != 0 {
		t.Errorf("warn = %d, want 0", stats.ByLevel["warn"])
	}
	// debug: m6, m7 = 2
	if stats.ByLevel["debug"] != 2 {
		t.Errorf("debug = %d, want 2", stats.ByLevel["debug"])
	}
	// source b: 被覆盖了 = 0
	if stats.BySource["b"] != 0 {
		t.Errorf("source b = %d, want 0", stats.BySource["b"])
	}
}

// ════════════════════════════════════════════════
// 8. HTTP Handler 测试
// ════════════════════════════════════════════════

func TestHandleGetLogs_DefaultParams(t *testing.T) {
	s := &Server{logCollector: NewLogCollector(100)}
	for i := range 5 {
		s.logCollector.Add("info", "test", fmt.Sprintf("msg-%d", i), nil)
	}

	req := httptest.NewRequest("GET", "/api/v1/logs", nil)
	w := httptest.NewRecorder()
	s.handleGetLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["total"].(float64) != 5 {
		t.Errorf("total = %v, want 5", resp["total"])
	}
	logs := resp["logs"].([]any)
	if len(logs) != 5 {
		t.Errorf("logs len = %d, want 5", len(logs))
	}
}

func TestHandleGetLogs_LimitCap(t *testing.T) {
	s := &Server{logCollector: NewLogCollector(100)}

	req := httptest.NewRequest("GET", "/api/v1/logs?limit=99999", nil)
	w := httptest.NewRecorder()
	s.handleGetLogs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHandleGetLogs_InvalidParams(t *testing.T) {
	s := &Server{logCollector: NewLogCollector(100)}

	req := httptest.NewRequest("GET", "/api/v1/logs?limit=-1", nil)
	w := httptest.NewRecorder()
	s.handleGetLogs(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("negative limit status = %d, want 200 (ignored)", w.Code)
	}

	req = httptest.NewRequest("GET", "/api/v1/logs?offset=abc", nil)
	w = httptest.NewRecorder()
	s.handleGetLogs(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("non-numeric offset status = %d, want 200 (ignored)", w.Code)
	}
}

func TestHandleGetLogStats(t *testing.T) {
	s := &Server{logCollector: NewLogCollector(100)}
	s.logCollector.Add("info", "chat", "msg", nil)

	req := httptest.NewRequest("GET", "/api/v1/logs/stats", nil)
	w := httptest.NewRecorder()
	s.handleGetLogStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var stats LogStats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 {
		t.Errorf("total = %d, want 1", stats.Total)
	}
}

// ════════════════════════════════════════════════
// 9. emptyList 测试
// ════════════════════════════════════════════════

func TestEmptyList(t *testing.T) {
	handler := emptyList("documents")

	req := httptest.NewRequest("GET", "/api/v1/knowledge/documents", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	docs := resp["documents"].([]any)
	if len(docs) != 0 {
		t.Errorf("documents len = %d, want 0", len(docs))
	}
	if resp["total"].(float64) != 0 {
		t.Errorf("total = %v, want 0", resp["total"])
	}
}

// ════════════════════════════════════════════════
// 10. 基准测试 — 验证性能改进
// ════════════════════════════════════════════════

func BenchmarkLogCollector_Add(b *testing.B) {
	c := NewLogCollector(5000)
	b.ResetTimer()
	for range b.N {
		c.Add("info", "bench", "benchmark message", nil)
	}
}

func BenchmarkLogCollector_Add_WithSubscribers(b *testing.B) {
	c := NewLogCollector(5000)
	for range 10 {
		ch, _ := c.subscribe()
		go func() {
			for range ch {
			}
		}()
	}
	b.ResetTimer()
	for range b.N {
		c.Add("info", "bench", "benchmark message", nil)
	}
}

func BenchmarkLogCollector_Add_Overflow(b *testing.B) {
	c := NewLogCollector(100)
	for range 100 {
		c.Add("info", "test", "fill", nil)
	}
	b.ResetTimer()
	for range b.N {
		c.Add("info", "bench", "overflow msg", nil)
	}
}

func BenchmarkLogCollector_Query_Unfiltered(b *testing.B) {
	c := NewLogCollector(5000)
	for i := range 5000 {
		c.Add("info", "test", fmt.Sprintf("message %d", i), nil)
	}
	b.ResetTimer()
	for range b.N {
		c.Query("", "", "", 100, 0)
	}
}

func BenchmarkLogCollector_Query_WithKeyword(b *testing.B) {
	c := NewLogCollector(5000)
	for i := range 5000 {
		c.Add("info", "test", fmt.Sprintf("Request #%d processed successfully", i), nil)
	}
	b.ResetTimer()
	for range b.N {
		c.Query("", "", "processed", 10, 0)
	}
}

func BenchmarkLogCollector_Stats(b *testing.B) {
	c := NewLogCollector(5000)
	levels := []string{"info", "warn", "error", "debug"}
	for i := range 5000 {
		c.Add(levels[i%4], fmt.Sprintf("src-%d", i%10), "msg", nil)
	}
	b.ResetTimer()
	for range b.N {
		c.Stats()
	}
}

func BenchmarkTruncate_Short(b *testing.B) {
	s := "hello"
	for range b.N {
		stringx.Truncate(s, 10)
	}
}

func BenchmarkTruncate_Long_ASCII(b *testing.B) {
	s := strings.Repeat("a", 1000)
	for range b.N {
		stringx.Truncate(s, 80)
	}
}

func BenchmarkTruncate_Long_Chinese(b *testing.B) {
	s := strings.Repeat("你好世界", 250)
	for range b.N {
		stringx.Truncate(s, 80)
	}
}

// ════════════════════════════════════════════════
// 11. 并发基准测试
// ════════════════════════════════════════════════

func BenchmarkLogCollector_ConcurrentAdd(b *testing.B) {
	c := NewLogCollector(5000)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Add("info", "bench", "concurrent message", nil)
		}
	})
}

func BenchmarkLogCollector_ConcurrentAddAndQuery(b *testing.B) {
	c := NewLogCollector(5000)
	for range 1000 {
		c.Add("info", "test", "prefill", nil)
	}

	var ops atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := ops.Add(1)
			if n%3 == 0 {
				c.Query("info", "", "", 10, 0)
			} else {
				c.Add("info", "bench", "msg", nil)
			}
		}
	})
}
