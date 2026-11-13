package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hexagon-codes/ai-core/llm"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

func TestToolDiscoveryKeepsIndexWithinExistingResultBudget(t *testing.T) {
	for _, limit := range []int{8000, 700} {
		t.Run(fmt.Sprintf("budget_%d", limit), func(t *testing.T) {
			registry := skill.NewRegistry()
			builtin.RegisterAll(registry, config.BuiltinConfig{}, func() skill.ExecutionPolicy { return skill.ExecutionPolicy{Profile: "full_access"} })
			executor := NewToolExecutor(registry, nil)
			executor.AddHook(&TruncateHook{MaxChars: limit})
			RegisterExternalToolAccess(registry, NewToolCollector(registry, nil, 1), executor, nil)
			content, _, err := executor.ExecuteResult(t.Context(), discoverToolsName, map[string]any{})
			if err != nil || !json.Valid([]byte(content)) || utf8.RuneCountInString(content) > limit {
				t.Fatalf("discovery exceeded its existing result budget: chars=%d valid_json=%t err=%v", utf8.RuneCountInString(content), json.Valid([]byte(content)), err)
			}
			var result struct {
				Mode                     string
				Total, Returned, Omitted int
				Hint                     string
				Tools                    []map[string]json.RawMessage
				Origins                  map[string]json.RawMessage
			}
			if err := json.Unmarshal([]byte(content), &result); err != nil {
				t.Fatal(err)
			}
			if result.Mode != "index" || result.Returned == 0 || result.Returned != len(result.Tools) || result.Returned+result.Omitted != result.Total || result.Hint == "" {
				t.Fatalf("tool index lost discovery or omission facts: %s", content)
			}
			for _, entry := range result.Tools {
				var name string
				if err := json.Unmarshal(entry["name"], &name); err != nil || name == "" || len(result.Origins[name]) == 0 || len(entry["function"]) != 0 {
					t.Fatalf("index entry lost its exact tool name or origin, or copied a complete schema: %s", content)
				}
			}
			if limit == 700 && result.Omitted == 0 {
				t.Fatal("small result budget silently lost the omitted tool count")
			}
		})
	}
}

func TestToolDiscoveryExactQueryKeepsOriginalSchemaAndWrapper(t *testing.T) {
	registry := skill.NewRegistry()
	if err := registry.Register(builtin.NewBrowserSkill()); err != nil {
		t.Fatal(err)
	}
	executor := NewToolExecutor(registry, nil)
	budget := &TruncateHook{MaxChars: 8000}
	executor.AddHook(budget)
	RegisterExternalToolAccess(registry, NewToolCollector(registry, nil, 1), executor, nil)
	content, _, err := executor.ExecuteResult(t.Context(), discoverToolsName, map[string]any{"query": "HTTP"})
	var index struct {
		Mode  string
		Tools []struct{ Name string }
	}
	if err != nil || json.Unmarshal([]byte(content), &index) != nil || index.Mode != "index" || len(index.Tools) != 1 || index.Tools[0].Name != "browser" {
		t.Fatalf("a broad query with one match did not return an index: err=%v content=%s", err, content)
	}
	for _, name := range []string{discoverToolsName, invokeToolName} {
		args := map[string]any{"query": "browser"}
		if name == invokeToolName {
			args = map[string]any{"name": discoverToolsName, "arguments": args}
		}
		content, _, err := executor.ExecuteResult(t.Context(), name, args)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Mode  string
			Tools []llm.ToolDefinition
		}
		if err := json.Unmarshal([]byte(content), &result); err != nil || result.Mode != "schema" || len(result.Tools) != 1 {
			t.Fatalf("exact lookup through %s lost its schema: %s err=%v", name, content, err)
		}
		want, _ := json.Marshal(builtin.NewBrowserSkill().ToolDefinition())
		got, _ := json.Marshal(result.Tools[0])
		if string(got) != string(want) {
			t.Fatal("exact lookup changed the original declared tool schema")
		}
	}
	executor.AddHook(NewToolPermissionHook(NewToolPermissions(nil, []string{"browser"})))
	if _, _, err := executor.ExecuteResult(t.Context(), invokeToolName, map[string]any{"name": "browser", "arguments": map[string]any{"url": "https://fixture.invalid"}}); err == nil || !strings.Contains(err.Error(), "denied by rule") {
		t.Fatalf("discovery wrapper bypassed the target executor permission: %v", err)
	}
	budget.MaxChars = 700
	if _, _, err := executor.ExecuteResult(t.Context(), discoverToolsName, map[string]any{"query": "browser"}); err == nil || !strings.Contains(err.Error(), "schema exceeds") {
		t.Fatalf("an oversized exact schema must report the existing budget instead of claiming completeness: %v", err)
	}
}
