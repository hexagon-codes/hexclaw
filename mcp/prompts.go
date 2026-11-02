package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Prompt 是所属服务公布的参数化模板，不视为已完成的用户任务。
type Prompt struct {
	Server      string           `json:"server"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument 保留服务定义的参数名、说明和必填标记。
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptMessage 保留解析后消息的角色与完整内容。
type PromptMessage struct {
	Role    string        `json:"role"`
	Content PromptContent `json:"content"`
}

// PromptContent 兼容文本/资源字段，并保留图片、音频及嵌入资源。
// Raw 为官方协议内容的完整投影，防止后续内容字段被摘要丢弃。
type PromptContent struct {
	Type        string                   `json:"type"`
	Text        string                   `json:"text,omitempty"`
	ResourceURI string                   `json:"resource_uri,omitempty"`
	MimeType    string                   `json:"mimeType,omitempty"`
	Data        []byte                   `json:"data,omitempty"`
	Resource    *sdkmcp.ResourceContents `json:"resource,omitempty"`
	Raw         json.RawMessage          `json:"raw,omitempty"`
}

// GetPromptResult 是 prompts/get 的实际响应，不触发模板内指令执行。
type GetPromptResult struct {
	Server      string          `json:"server"`
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

// PromptInfo 用于展示模板的真实服务器归属。
type PromptInfo struct {
	Server      string `json:"server"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	ArgCount    int    `json:"arg_count"`
}

// DiscoverPrompts 聚合实际 prompts/list，返回部分结果及协议错误。
func (m *Manager) DiscoverPrompts(ctx context.Context) ([]Prompt, error) {
	return m.listPrompts(ctx, "")
}

// ListServerPrompts 只发现指定服务器的模板。
func (m *Manager) ListServerPrompts(ctx context.Context, serverName string) ([]Prompt, error) {
	if serverName == "" {
		return nil, fmt.Errorf("MCP prompt server is required")
	}
	return m.listPrompts(ctx, serverName)
}

func (m *Manager) listPrompts(ctx context.Context, serverName string) ([]Prompt, error) {
	servers, err := m.protocolServers(serverName)
	if err != nil {
		return nil, err
	}
	var prompts []Prompt
	var failures []error
	for _, server := range servers {
		items, err := server.session.Prompts(ctx)
		if err != nil {
			failures = append(failures, fmt.Errorf("MCP server %q prompts: %w", server.name, err))
		}
		for _, item := range items {
			prompt := Prompt{Server: server.name, Name: item.Name, Description: item.Description}
			for _, arg := range item.Arguments {
				if arg == nil {
					return prompts, fmt.Errorf("MCP server %q returned a null prompt argument", server.name)
				}
				prompt.Arguments = append(prompt.Arguments, PromptArgument{Name: arg.Name, Description: arg.Description, Required: arg.Required})
			}
			prompts = append(prompts, prompt)
		}
	}
	return prompts, errors.Join(failures...)
}

// ListPrompts 保持旧列表签名；需要错误事实的调用方使用 DiscoverPrompts。
func (m *Manager) ListPrompts(ctx context.Context) []Prompt {
	prompts, _ := m.DiscoverPrompts(ctx)
	return prompts
}

// GetPrompt 仅在同名模板的服务器归属无歧义时解析。
func (m *Manager) GetPrompt(ctx context.Context, name string, args map[string]string) (*GetPromptResult, error) {
	return m.GetServerPrompt(ctx, "", name, args)
}

// GetServerPrompt 从确切连接解析模板，消息和服务错误不跨服务器兜底。
func (m *Manager) GetServerPrompt(ctx context.Context, serverName, name string, args map[string]string) (*GetPromptResult, error) {
	servers, err := m.protocolServers(serverName)
	if err != nil {
		return nil, err
	}
	var owner *connectedServer
	if serverName != "" {
		owner = servers[0]
	} else {
		for _, server := range servers {
			prompts, err := server.session.Prompts(ctx)
			if err != nil {
				return nil, fmt.Errorf("MCP server %q prompt discovery: %w", server.name, err)
			}
			for _, prompt := range prompts {
				if prompt.Name != name {
					continue
				}
				if owner != nil && owner != server {
					return nil, fmt.Errorf("MCP prompt %q is ambiguous; specify server_name", name)
				}
				owner = server
			}
		}
	}
	if owner == nil {
		return nil, fmt.Errorf("MCP prompt %q not found", name)
	}
	raw, err := owner.session.GetPrompt(ctx, name, args)
	if err != nil {
		if isMCPConnClosed(err) {
			m.markServerDisconnected(ctx, owner.name, owner, "connection closed during prompts/get")
		}
		return nil, fmt.Errorf("MCP server %q prompt get: %w", owner.name, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("MCP server %q returned no prompt result", owner.name)
	}
	result := &GetPromptResult{Server: owner.name, Description: raw.Description, Messages: make([]PromptMessage, 0, len(raw.Messages))}
	for _, message := range raw.Messages {
		if message == nil || message.Content == nil {
			return nil, fmt.Errorf("MCP server %q returned an empty prompt message", owner.name)
		}
		data, err := json.Marshal(message.Content)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q prompt content: %w", owner.name, err)
		}
		var content PromptContent
		if err := json.Unmarshal(data, &content); err != nil {
			return nil, fmt.Errorf("MCP server %q prompt content: %w", owner.name, err)
		}
		content.Raw = data
		if resource, ok := message.Content.(*sdkmcp.ResourceLink); ok {
			content.ResourceURI = resource.URI
		} else if content.Resource != nil {
			content.ResourceURI = content.Resource.URI
		}
		result.Messages = append(result.Messages, PromptMessage{Role: string(message.Role), Content: content})
	}
	return result, nil
}

// AllPromptInfos 返回兼容展示投影，实际错误由 DiscoverPrompts 保留。
func (m *Manager) AllPromptInfos(ctx context.Context) []PromptInfo {
	prompts, _ := m.DiscoverPrompts(ctx)
	infos := make([]PromptInfo, 0, len(prompts))
	for _, prompt := range prompts {
		infos = append(infos, PromptInfo{Server: prompt.Server, Name: prompt.Name, Description: prompt.Description, ArgCount: len(prompt.Arguments)})
	}
	return infos
}
