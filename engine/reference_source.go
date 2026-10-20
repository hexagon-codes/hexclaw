package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

// ReferenceSourcePage 只携带实际读取的网页正文，不把搜索摘要当作来源事实。
type ReferenceSourcePage struct {
	URL     string
	Content string
	Digest  string
}

// ReadReferenceSourcePages 复用已启用的搜索、浏览器及权限钩子，不启用工具或写入会话记忆。
func (e *ReActEngine) ReadReferenceSourcePages(ctx context.Context, agentName, query string) ([]ReferenceSourcePage, error) {
	if strings.TrimSpace(query) == "" || e.toolCollector == nil || e.toolExecutor == nil {
		return nil, nil
	}
	e.mu.RLock()
	toolsConfig := e.cfg.LLM.Tools
	e.mu.RUnlock()
	metadata := map[string]string{"routed_agent": agentName, "pinned_agent": agentName}
	provider := ""
	if router := e.getAgentRouter(); router != nil {
		if agent, found := router.GetAgent(agentName); found {
			provider = agent.Provider
			for key, value := range agent.Metadata {
				metadata[key] = value
			}
		}
	}
	if !resolveToolsEnabledForMessage(toolsConfig, e.providerIsLocal(provider), metadata) {
		return nil, nil
	}
	tools := e.toolCollector.CollectFiltered(query, skill.Activation{Mode: string(ResolveMode("", query))})
	tools = applyInheritedToolPolicy(&adapter.Message{Metadata: metadata}, tools)
	tools = e.workflowTools(ctx, tools)
	available := map[string]bool{}
	for _, tool := range tools {
		available[tool.Function.Name] = true
	}
	if !available["search"] || !available["browser"] {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(skill.WithRoutedAgent(ctx, agentName), 30*time.Second)
	defer cancel()
	_, raw, err := e.toolExecutor.ExecuteResult(ctx, "search", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	result, ok := raw.(*skill.Result)
	if !ok || result == nil {
		return nil, nil
	}
	sources, _ := result.Data.([]map[string]string)
	var pages []ReferenceSourcePage
	for index, source := range sources {
		if index >= 3 {
			break
		}
		_, raw, readErr := e.toolExecutor.ExecuteResult(ctx, "browser", map[string]any{"url": source["url"], "action": "fetch"})
		if readErr != nil {
			continue
		}
		page, ok := raw.(*skill.Result)
		if !ok || page == nil || page.Metadata["status"] != "200" || !strings.Contains(strings.ToLower(page.Metadata["content_type"]), "text/") || strings.TrimSpace(page.Content) == "" {
			continue
		}
		digest := sha256.Sum256([]byte(page.Content))
		pages = append(pages, ReferenceSourcePage{URL: page.Metadata["url"], Content: page.Content, Digest: "sha256:" + hex.EncodeToString(digest[:])})
	}
	return pages, nil
}
