package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
)

// OAuthConfigForResource 复制认证配置，以实际服务端点补齐缺省资源身份。
// 显式资源身份允许同一授权资源迁移端点，不会自动增加 OAuth 请求参数。
func OAuthConfigForResource(config *OAuthConfig, endpoint string) *OAuthConfig {
	if config == nil {
		return nil
	}
	copy := *config
	copy.Scopes = append([]string(nil), config.Scopes...)
	if copy.Resource == "" {
		copy.Resource = endpoint
	}
	return &copy
}

func canonicalOAuthURL(value string) (string, error) {
	u, err := oauthEndpoint(value)
	if err != nil {
		return "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

func canonicalOAuthScopes(scopes []string) []string {
	set := make(map[string]struct{}, len(scopes))
	for _, value := range scopes {
		for _, scope := range strings.Fields(value) {
			set[scope] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	sort.Strings(result)
	return result
}

func canonicalOAuthIdentity(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" {
		return "", fmt.Errorf("OAuth identity must be an absolute URI")
	}
	if strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https") {
		return canonicalOAuthURL(value)
	}
	return u.String(), nil
}

// oauthSourceFingerprint 不包含临时回调端口，绑定实际授权来源和资源。
func oauthSourceFingerprint(server string, config *OAuthConfig) (string, error) {
	if server == "" || config == nil || config.ClientID == "" {
		return "", fmt.Errorf("%w: OAuth server and client configuration are required", ErrOAuthAuthorizationRequired)
	}
	authorize, err := canonicalOAuthURL(config.AuthURL)
	if err != nil {
		return "", fmt.Errorf("%w: OAuth authorization endpoint is invalid", ErrOAuthAuthorizationRequired)
	}
	token, err := canonicalOAuthURL(config.TokenURL)
	if err != nil {
		return "", fmt.Errorf("%w: OAuth token endpoint is invalid", ErrOAuthAuthorizationRequired)
	}
	issuer, resource := "", ""
	if config.Issuer != "" {
		if issuer, err = canonicalOAuthIdentity(config.Issuer); err != nil {
			return "", fmt.Errorf("%w: OAuth issuer is invalid", ErrOAuthAuthorizationRequired)
		}
	}
	if config.Resource != "" {
		if resource, err = canonicalOAuthIdentity(config.Resource); err != nil {
			return "", fmt.Errorf("%w: OAuth resource identity is invalid", ErrOAuthAuthorizationRequired)
		}
	}
	payload, err := json.Marshal(struct {
		Version  string   `json:"version"`
		Server   string   `json:"server"`
		Issuer   string   `json:"issuer"`
		AuthURL  string   `json:"auth_url"`
		TokenURL string   `json:"token_url"`
		ClientID string   `json:"client_id"`
		Scopes   []string `json:"scopes"`
		Resource string   `json:"resource"`
	}{"oauth-source-v1", server, issuer, authorize, token, config.ClientID, canonicalOAuthScopes(config.Scopes), resource})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func oauthTokenCacheKey(server, source string) string {
	return server + "\x00" + source
}

func (m *OAuthManager) tokenPath(server, source string) string {
	sum := sha256.Sum256([]byte(oauthTokenCacheKey(server, source)))
	return filepath.Join(m.credDir, "oauth-"+hex.EncodeToString(sum[:])+".json")
}

// MatchesConfiguration 只比较已有授权事实，不把当前配置补写成历史来源。
func (a *OAuthAuthorization) MatchesConfiguration(config *OAuthConfig) bool {
	source, err := oauthSourceFingerprint(a.ServerName, config)
	return err == nil && source == a.sourceFingerprint && config.RedirectURI == a.config.RedirectURI
}

func (m *OAuthManager) setCurrentSource(server, source string) {
	m.mu.Lock()
	m.sources[server] = source
	m.mu.Unlock()
}

func (m *OAuthManager) sourceIsCurrent(server, source string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sources[server] == source
}

func legacyOAuthTokenPath(directory, server string) string {
	name := filepath.Base(server)
	if name == "." || filepath.IsAbs(name) {
		return ""
	}
	return filepath.Join(directory, name+".json")
}
