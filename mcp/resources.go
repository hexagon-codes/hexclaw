package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Resource 保留资源 URI 和确切服务器归属，避免不同服务的同 URI 混用。
type Resource struct {
	Server      string `json:"server"`
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceTemplate 为服务公布的 URI 模板，使用时仍须指定所属服务器。
type ResourceTemplate struct {
	Server      string `json:"server"`
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ServerResourceResult 保留资源所有内容及 MIME/二进制元数据。
type ServerResourceResult struct {
	Server string `json:"server"`
	*sdkmcp.ReadResourceResult
}

// protocolServers 在锁内取得连接快照，网络操作不持有 Manager.mu。
// 连接替换后旧快照只能完成或报告原连接失败，不会重路由到新会话。
func (m *Manager) protocolServers(serverName string) ([]*connectedServer, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closedLocked() {
		return nil, fmt.Errorf("MCP manager is closed")
	}
	var servers []*connectedServer
	for name, server := range m.servers {
		if serverName != "" && name != serverName {
			continue
		}
		if server.connected && server.session != nil {
			servers = append(servers, server)
		}
	}
	if serverName != "" && len(servers) == 0 {
		return nil, fmt.Errorf("MCP server %q has no connected session", serverName)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].name < servers[j].name })
	return servers, nil
}

// DiscoverResources 聚合真实 resources/list，返回部分结果及所有协议错误。
func (m *Manager) DiscoverResources(ctx context.Context) ([]Resource, error) {
	return m.listResources(ctx, "")
}

// ListServerResources 只读取指定服务器的资源列表。
func (m *Manager) ListServerResources(ctx context.Context, serverName string) ([]Resource, error) {
	if serverName == "" {
		return nil, fmt.Errorf("MCP resource server is required")
	}
	return m.listResources(ctx, serverName)
}

func (m *Manager) listResources(ctx context.Context, serverName string) ([]Resource, error) {
	servers, err := m.protocolServers(serverName)
	if err != nil {
		return nil, err
	}
	var resources []Resource
	var failures []error
	for _, server := range servers {
		items, err := server.session.Resources(ctx)
		if err != nil {
			failures = append(failures, fmt.Errorf("MCP server %q resources: %w", server.name, err))
		}
		for _, item := range items {
			resources = append(resources, Resource{Server: server.name, URI: item.URI, Name: item.Name, Description: item.Description, MimeType: item.MIMEType})
		}
	}
	return resources, errors.Join(failures...)
}

// ListResources 保持旧列表签名；需要错误事实的调用方使用 DiscoverResources。
func (m *Manager) ListResources(ctx context.Context) []Resource {
	resources, _ := m.DiscoverResources(ctx)
	return resources
}

// ListResourceTemplates 读取指定服务或全部已连接服务的资源模板。
func (m *Manager) ListResourceTemplates(ctx context.Context, serverName string) ([]ResourceTemplate, error) {
	servers, err := m.protocolServers(serverName)
	if err != nil {
		return nil, err
	}
	var templates []ResourceTemplate
	var failures []error
	for _, server := range servers {
		items, err := server.session.ResourceTemplates(ctx)
		if err != nil {
			failures = append(failures, fmt.Errorf("MCP server %q resource templates: %w", server.name, err))
		}
		for _, item := range items {
			templates = append(templates, ResourceTemplate{Server: server.name, URITemplate: item.URITemplate, Name: item.Name, Description: item.Description, MimeType: item.MIMEType})
		}
	}
	return templates, errors.Join(failures...)
}

// ReadServerResource 按确切服务器读取 URI，支持未列举的模板实例。
// serverName 为空时仅按已发现且无歧义的 URI 解析归属。
func (m *Manager) ReadServerResource(ctx context.Context, serverName, uri string) (*ServerResourceResult, error) {
	servers, err := m.protocolServers(serverName)
	if err != nil {
		return nil, err
	}
	var owner *connectedServer
	if serverName != "" {
		owner = servers[0]
	} else {
		for _, server := range servers {
			resources, err := server.session.Resources(ctx)
			if err != nil {
				return nil, fmt.Errorf("MCP server %q resource discovery: %w", server.name, err)
			}
			for _, resource := range resources {
				if resource.URI != uri {
					continue
				}
				if owner != nil && owner != server {
					return nil, fmt.Errorf("MCP resource %q is ambiguous; specify server_name", uri)
				}
				owner = server
			}
		}
	}
	if owner == nil {
		return nil, fmt.Errorf("MCP resource %q not found; specify server_name for a resource template", uri)
	}
	result, err := owner.session.ReadResource(ctx, uri)
	if err != nil {
		if isMCPConnClosed(err) {
			m.markServerDisconnected(ctx, owner.name, owner, "connection closed during resources/read")
		}
		return nil, fmt.Errorf("MCP server %q resource read: %w", owner.name, err)
	}
	if result == nil {
		return nil, fmt.Errorf("MCP server %q returned no resource result", owner.name)
	}
	return &ServerResourceResult{Server: owner.name, ReadResourceResult: result}, nil
}

// ReadResource 保持文本读取兼容；包含二进制的结果以完整 JSON 返回。
func (m *Manager) ReadResource(ctx context.Context, uri string) (string, error) {
	result, err := m.ReadServerResource(ctx, "", uri)
	if err != nil {
		return "", err
	}
	var texts []string
	for _, content := range result.Contents {
		if content == nil {
			continue
		}
		if content.Blob != nil {
			data, err := json.Marshal(result)
			return string(data), err
		}
		texts = append(texts, content.Text)
	}
	return strings.Join(texts, "\n"), nil
}

// InjectResourceContext 提供服务器归属和 URI，不把连接失败伪装成空目录。
func (m *Manager) InjectResourceContext(ctx context.Context) string {
	resources, err := m.DiscoverResources(ctx)
	if len(resources) == 0 && err == nil {
		return ""
	}
	var result strings.Builder
	result.WriteString("\n[Available MCP Resources]\n")
	for _, resource := range resources {
		fmt.Fprintf(&result, "- server=%s; %s (%s): %s\n", resource.Server, resource.Name, resource.URI, resource.Description)
	}
	if err != nil {
		result.WriteString("Resource discovery is incomplete; query the selected server before reading.\n")
	}
	return result.String()
}
