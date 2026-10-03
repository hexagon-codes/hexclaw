package builtin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/knowledge"
)

// fakeKBSearcher 记录最近一次过滤参数，返回受控命中。
type fakeKBSearcher struct {
	lastQuery  string
	lastTopK   int
	lastFilter knowledge.Filter
	hits       []knowledge.SearchHit
}

func (f *fakeKBSearcher) SearchWithFilter(_ context.Context, query string, topK int, filter knowledge.Filter) ([]knowledge.SearchHit, error) {
	f.lastQuery, f.lastTopK, f.lastFilter = query, topK, filter
	return f.hits, nil
}

func TestKnowledgeSearchSkill_ParsesFiltersAndClampsTopK(t *testing.T) {
	fake := &fakeKBSearcher{hits: []knowledge.SearchHit{
		{DocTitle: "Doc", Source: "agent", Content: "body", Metadata: map[string]any{"source_type": "agent"}},
	}}
	sk := NewKnowledgeSearchSkill(fake)

	res, err := sk.Execute(context.Background(), map[string]any{
		"query":          "光合作用",
		"top_k":          float64(999), // JSON number; should clamp to max
		"source_types":   []any{"agent", "upload", ""},
		"sources":        []any{"https://x"},
		"created_after":  "2026-06-15",
		"created_before": "2026-06-25T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastQuery != "光合作用" {
		t.Errorf("query 透传错: %q", fake.lastQuery)
	}
	if fake.lastTopK != knowledgeSearchMaxTopK {
		t.Errorf("top_k 应钳到 %d，得 %d", knowledgeSearchMaxTopK, fake.lastTopK)
	}
	if len(fake.lastFilter.SourceTypes) != 2 { // 空串被丢弃
		t.Errorf("source_types 应规整为 2 项（去空串），得 %v", fake.lastFilter.SourceTypes)
	}
	if len(fake.lastFilter.Sources) != 1 || fake.lastFilter.Sources[0] != "https://x" {
		t.Errorf("sources 透传错: %v", fake.lastFilter.Sources)
	}
	if !fake.lastFilter.CreatedAfter.Equal(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("created_after 解析错: %v", fake.lastFilter.CreatedAfter)
	}
	if fake.lastFilter.CreatedBefore.IsZero() {
		t.Error("created_before 应被解析")
	}
	if res.Content == "" || res.Metadata["hits"] != "1" {
		t.Errorf("结果格式化/计数错: %+v", res)
	}
}

func TestKnowledgeSearchSkill_NoFilterAndEmptyQuery(t *testing.T) {
	fake := &fakeKBSearcher{}
	sk := NewKnowledgeSearchSkill(fake)

	// 空 query → 报错
	if _, err := sk.Execute(context.Background(), map[string]any{"query": "  "}); err == nil {
		t.Error("空 query 应报错")
	}

	// 无过滤 → 默认 topK + 零 Filter（IsZero）
	if _, err := sk.Execute(context.Background(), map[string]any{"query": "q"}); err != nil {
		t.Fatal(err)
	}
	if fake.lastTopK != knowledgeSearchDefaultTopK {
		t.Errorf("默认 topK 应为 %d，得 %d", knowledgeSearchDefaultTopK, fake.lastTopK)
	}
	if !fake.lastFilter.IsZero() {
		t.Errorf("无过滤参数时 Filter 应 IsZero，得 %+v", fake.lastFilter)
	}
}

func TestKnowledgeSearchSkill_BadDateAndNoResults(t *testing.T) {
	fake := &fakeKBSearcher{} // 返回空命中
	sk := NewKnowledgeSearchSkill(fake)

	if _, err := sk.Execute(context.Background(), map[string]any{"query": "q", "created_after": "not-a-date"}); err == nil {
		t.Error("非法 created_after 应报错")
	}

	res, err := sk.Execute(context.Background(), map[string]any{"query": "q"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Metadata["hits"] != "0" {
		t.Errorf("零命中应 hits=0，得 %v", res.Metadata)
	}
}

func TestKnowledgeSearchSkill_NilKB(t *testing.T) {
	sk := NewKnowledgeSearchSkill(nil)
	if _, err := sk.Execute(context.Background(), map[string]any{"query": "q"}); err == nil {
		t.Error("kb 为 nil 应报错而非 panic")
	}
}

func TestKnowledgeSearchSkillProjectsOnlyTrustedPDFPages(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end int
		want       string
	}{
		{"single-page", 7, 7, "PDF page: 7"},
		{"page-range", 7, 9, "PDF pages: 7–9"},
		{"missing-page", 0, 0, ""},
		{"invalid-range", 7, 4, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hit := knowledge.SearchHit{
				DocTitle: "六年级上册科学", Source: "upload:science.pdf", Content: "用四个小瓶进行对比实验。",
				PageStart: tc.start, PageEnd: tc.end, Metadata: map[string]any{"page": 4},
			}
			result, err := NewKnowledgeSearchSkill(&fakeKBSearcher{hits: []knowledge.SearchHit{hit}}).Execute(t.Context(), map[string]any{"query": "实验设计"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != "" && !strings.Contains(result.Content, tc.want) {
				t.Fatalf("trusted PDF location missing: %q", result.Content)
			}
			if tc.want == "" && strings.Contains(result.Content, "PDF page") {
				t.Fatalf("unverified PDF page projected: %q", result.Content)
			}
			if strings.Contains(result.Content, "page: 4") || !strings.Contains(result.Content, hit.Content) {
				t.Fatalf("printed page guessed or passage changed: %q", result.Content)
			}
		})
	}
}
