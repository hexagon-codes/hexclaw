package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/config"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

func mcpOAuthConfig(auth *config.MCPAuthConfig) *hexmcp.OAuthConfig {
	if auth == nil || !strings.EqualFold(auth.Type, "oauth") {
		return nil
	}
	return &hexmcp.OAuthConfig{ClientID: auth.ClientID, Issuer: auth.Issuer, AuthURL: auth.AuthURL, TokenURL: auth.TokenURL, Scopes: append([]string(nil), auth.Scopes...), Resource: auth.Resource}
}

// configureExternalOAuth 共用原MCP账号存储与连接管理，不复制模型供应商凭据。
func configureExternalOAuth(ctx context.Context, _ *config.Config, manager *hexmcp.Manager, registry *skill.DefaultRegistry) error {
	if manager == nil || registry == nil {
		return errors.New("external OAuth runtime is unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	runtime := &externalOAuthRuntime{ctx: ctx, manager: manager,
		oauth: hexmcp.NewOAuthManager(filepath.Join(home, ".hexclaw", "credentials")), flows: map[string]ownedOAuthFlow{}}
	manager.SetHTTPClientFactory(func(requestCtx context.Context, server hexmcp.ServerConfig) (*http.Client, error) {
		if server.Auth == nil {
			return nil, nil
		}
		return runtime.oauth.HTTPClient(requestCtx, server.Name, server.Auth, server.Endpoint, nil)
	})
	if current, ok := registry.Get("browser"); ok {
		if browser, ok := builtin.UnwrapConfigured(current).(*builtin.BrowserSkill); ok {
			browser.SetAuthResolver(runtime.resolveHTTPAuth)
		}
	}
	return registry.Register(&mcpAuthorizeSkill{runtime: runtime})
}

type ownedOAuthFlow struct {
	owner string
	flow  *hexmcp.OAuthAuthorization
}

type externalOAuthRuntime struct {
	ctx     context.Context
	manager *hexmcp.Manager
	oauth   *hexmcp.OAuthManager
	mu      sync.Mutex
	flows   map[string]ownedOAuthFlow
}

func externalAuthOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("HTTP authentication target is invalid")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}

// resolveHTTPAuth 只按明确引用或唯一目标服务匹配，不把一个账号借给其他服务。
func (r *externalOAuthRuntime) resolveHTTPAuth(ctx context.Context, target, reference string) (builtin.HTTPRequestAuth, error) {
	origin, err := externalAuthOrigin(target)
	if err != nil {
		return builtin.HTTPRequestAuth{}, err
	}
	names := r.manager.ConfiguredServerNames()
	if reference != "" {
		names = []string{strings.TrimPrefix(reference, "mcp:")}
	}
	var matches []hexmcp.ServerConfig
	for _, name := range names {
		server, found := r.manager.ConfiguredServerConfig(name)
		if !found || !server.Enabled || server.Auth == nil {
			continue
		}
		serverOrigin, err := externalAuthOrigin(server.Endpoint)
		if err == nil && serverOrigin == origin {
			matches = append(matches, server)
		}
	}
	if len(matches) == 0 {
		if reference != "" {
			return builtin.HTTPRequestAuth{}, errors.New("authentication reference does not belong to an enabled service at this target")
		}
		return builtin.HTTPRequestAuth{Type: "none", Source: "none"}, nil
	}
	if len(matches) != 1 {
		return builtin.HTTPRequestAuth{}, errors.New("multiple configured accounts match this target; specify auth_ref")
	}
	server := matches[0]
	server.Auth = hexmcp.OAuthConfigForResource(server.Auth, server.Endpoint)
	token, err := r.oauth.GetToken(ctx, server.Name, server.Auth)
	if err != nil {
		return builtin.HTTPRequestAuth{}, err
	}
	if token.TokenType != "" && !strings.EqualFold(token.TokenType, "Bearer") {
		return builtin.HTTPRequestAuth{}, errors.New("configured OAuth token requires its service-specific transport")
	}
	return builtin.HTTPRequestAuth{Type: "oauth", Token: token.AccessToken, Source: "mcp:" + server.Name}, nil
}

type mcpAuthorizeSkill struct{ runtime *externalOAuthRuntime }

func (*mcpAuthorizeSkill) Name() string { return "mcp_authorize" }
func (*mcpAuthorizeSkill) Description() string {
	return "Authorize a configured MCP service and inspect its actual login and connection status"
}
func (*mcpAuthorizeSkill) Match(string) bool { return false }
func (*mcpAuthorizeSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("mcp_authorize", "Start or resume OAuth for an existing configured MCP service. Return its authorization link through the current conversation; the user completes the external service login. Use status or wait to resume, then inspect actual connection readiness. Never return access or refresh tokens. HTTP auth_ref can use mcp:<server_name> only for that service target.", &llm.Schema{
		Type: "object", Properties: map[string]*llm.Schema{
			"server_name": {Type: "string", Description: "Exact existing MCP server name"},
			"action":      {Type: "string", Description: "start (default), status, or wait"},
			"state":       {Type: "string", Description: "Previously returned authorization state when resuming this flow"},
		}, Required: []string{"server_name"},
	})
}

func (s *mcpAuthorizeSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	name, _ := args["server_name"].(string)
	action, _ := args["action"].(string)
	if action == "" {
		action = "start"
	}
	server, found := s.runtime.manager.ConfiguredServerConfig(name)
	if !found {
		return nil, errors.New("MCP server is not configured")
	}
	if !server.Enabled {
		return nil, errors.New("MCP server is disabled")
	}
	if server.Auth == nil {
		return nil, errors.New("MCP server does not use OAuth")
	}
	server.Auth = hexmcp.OAuthConfigForResource(server.Auth, server.Endpoint)
	owner := skill.AuthenticatedUserID(ctx)
	if owner == "" {
		return nil, errors.New("OAuth authorization requires an authenticated owner")
	}
	r := s.runtime
	r.mu.Lock()
	owned, hasFlow := r.flows[name]
	if hasFlow && !owned.flow.MatchesConfiguration(server.Auth) {
		owned.flow.Cancel()
		hasFlow = false
	}
	if action == "start" {
		if hasFlow && owned.owner != owner && (owned.flow.Status() == "pending" || owned.flow.Status() == "completing") {
			r.mu.Unlock()
			return nil, errors.New("OAuth authorization already belongs to another active request")
		}
		flow, err := r.oauth.StartAuthorization(r.ctx, name, server.Auth)
		if err != nil {
			r.mu.Unlock()
			return nil, err
		}
		owned = ownedOAuthFlow{owner: owner, flow: flow}
		r.flows[name] = owned
		hasFlow = true
	}
	r.mu.Unlock()
	state, _ := args["state"].(string)
	if hasFlow && owned.owner == owner && state != "" && state != owned.flow.State {
		return nil, errors.New("OAuth authorization state does not match this request")
	}
	data := map[string]any{"server_name": name, "authorized": false}
	switch action {
	case "start":
		data["status"] = owned.flow.Status()
		data["authorized"] = data["status"] == "authorized"
		data["authorization_url"], data["state"], data["expires_at"] = owned.flow.AuthorizationURL, owned.flow.State, owned.flow.ExpiresAt
	case "wait":
		if !hasFlow || owned.owner != owner {
			return nil, errors.New("no OAuth authorization flow belongs to this request")
		}
		if _, err := owned.flow.Wait(ctx); err != nil {
			return nil, err
		}
		data["authorized"], data["status"] = true, "authorized"
	case "status":
		if _, err := r.oauth.GetToken(ctx, name, server.Auth); err == nil {
			data["authorized"], data["status"] = true, "authorized"
		} else if errors.Is(err, hexmcp.ErrOAuthAuthorizationRequired) {
			data["status"] = "authorization_required"
			if hasFlow && owned.owner == owner {
				data["status"], data["state"], data["authorization_url"] = owned.flow.Status(), owned.flow.State, owned.flow.AuthorizationURL
			}
		} else {
			return nil, err
		}
	default:
		return nil, errors.New("OAuth action must be start, status, or wait")
	}
	data["connected"] = false
	for _, current := range r.manager.ServerStatuses() {
		if current.Name == name {
			data["connected"], data["tool_count"] = current.Connected, current.ToolCount
		}
	}
	if data["authorized"] == true && data["connected"] == false {
		if err := r.manager.RestartServer(ctx, name); err != nil {
			data["connection_error"] = "MCP connection could not be established after authorization"
		}
		for _, current := range r.manager.ServerStatuses() {
			if current.Name == name {
				data["connected"], data["tool_count"] = current.Connected, current.ToolCount
			}
		}
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encode OAuth status: %w", err)
	}
	return &skill.Result{Content: string(encoded), Data: data}, nil
}
