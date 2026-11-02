package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hexagon-codes/toolkit/net/httpx"
	// randx 为 toolkit 的加密安全随机串生成包；本文件同时直接使用标准库
	// crypto/rand（PKCE verifier），故以别名 randx 区分，避免与标准库 rand 冲突。
	randx "github.com/hexagon-codes/toolkit/util/rand"
)

// oauthHTTPClient 复用 toolkit httpx：带 ResponseHeaderTimeout 防"连上但不发数据"挂死，
// 取代裸 http.DefaultClient（无任何超时）。OAuth token 交换是短请求，30s 首字节超时足够。
var oauthHTTPClient = httpx.MustNewRawClient(httpx.WithResponseHeaderTimeout(30 * time.Second))

// OAuthConfig holds OAuth 2.0 configuration for an MCP server.
type OAuthConfig struct {
	ClientID string `yaml:"client_id" json:"client_id"`
	// Issuer 仅使用服务明确提供的发行者身份，不根据域名猜测。
	Issuer   string   `yaml:"issuer,omitempty" json:"issuer,omitempty"`
	AuthURL  string   `yaml:"auth_url" json:"auth_url"`
	TokenURL string   `yaml:"token_url" json:"token_url"`
	Scopes   []string `yaml:"scopes" json:"scopes"`
	// Resource 只用于凭据来源绑定，缺省由实际 MCP 端点补齐。
	Resource    string `yaml:"resource,omitempty" json:"resource,omitempty"`
	RedirectURI string `yaml:"redirect_uri,omitempty" json:"redirect_uri,omitempty"`
}

// OAuthToken stored credentials.
type OAuthToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes,omitempty"`
	// SourceFingerprint 缺失的旧凭据不能被当前配置自动认领。
	SourceFingerprint string `json:"source_fingerprint,omitempty"`
}

// IsExpired checks if the token needs refresh.
func (t *OAuthToken) IsExpired() bool {
	return !t.ExpiresAt.IsZero() && time.Now().After(t.ExpiresAt.Add(-5*time.Minute))
}

var ErrOAuthAuthorizationRequired = errors.New("OAuth re-authorization required")

// OAuthManager handles OAuth flows for MCP servers.
type OAuthManager struct {
	mu      sync.Mutex
	credDir string // ~/.hexclaw/credentials/
	tokens  map[string]*OAuthToken
	dirty   map[string]bool
	locks   map[string]*sync.Mutex
	flows   map[string]*OAuthAuthorization
	sources map[string]string
	initErr error
}

// NewOAuthManager creates an OAuth manager.
func NewOAuthManager(credDir string) *OAuthManager {
	err := os.MkdirAll(credDir, 0700)
	return &OAuthManager{
		credDir: credDir,
		tokens:  make(map[string]*OAuthToken),
		dirty:   make(map[string]bool),
		locks:   make(map[string]*sync.Mutex),
		flows:   make(map[string]*OAuthAuthorization),
		sources: make(map[string]string),
		initErr: err,
	}
}

// GetToken returns a valid token for the server, refreshing if needed.
func (m *OAuthManager) GetToken(ctx context.Context, serverName string, cfg *OAuthConfig) (*OAuthToken, error) {
	return m.getToken(ctx, serverName, cfg, true)
}

func (m *OAuthManager) getToken(ctx context.Context, serverName string, cfg *OAuthConfig, currentConfiguration bool) (*OAuthToken, error) {
	if m.initErr != nil {
		return nil, fmt.Errorf("initialize OAuth credential storage: %w", m.initErr)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg = OAuthConfigForResource(cfg, "")
	source, err := oauthSourceFingerprint(serverName, cfg)
	if err != nil {
		return nil, err
	}
	lock := m.serverLock(serverName)
	lock.Lock()
	defer lock.Unlock()
	if currentConfiguration {
		m.setCurrentSource(serverName, source)
	}
	key := oauthTokenCacheKey(serverName, source)
	m.mu.Lock()
	token := cloneOAuthToken(m.tokens[key])
	dirty := m.dirty[key]
	m.mu.Unlock()
	if token != nil && token.AccessToken != "" && !token.IsExpired() {
		if dirty {
			if err := m.persistToken(serverName, token); err != nil {
				return nil, err
			}
		}
		return token, nil
	}
	if token == nil {
		token, err = m.loadToken(serverName, source)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("load OAuth token: %w", err)
		}
	}
	if token != nil && token.AccessToken != "" && !token.IsExpired() {
		m.mu.Lock()
		m.tokens[key] = token
		m.mu.Unlock()
		return cloneOAuthToken(token), nil
	}
	if token != nil && token.RefreshToken != "" && cfg != nil && cfg.TokenURL != "" {
		refreshed, err := m.refreshToken(ctx, cfg, token.RefreshToken)
		if err != nil {
			return nil, err
		}
		if len(refreshed.Scopes) == 0 {
			refreshed.Scopes = append([]string(nil), token.Scopes...)
		}
		refreshed.SourceFingerprint = source
		if err := m.persistToken(serverName, refreshed); err != nil {
			return nil, err
		}
		return cloneOAuthToken(refreshed), nil
	}
	return nil, fmt.Errorf("%w for server %q", ErrOAuthAuthorizationRequired, serverName)
}

// Authorize 兼容原阻塞入口；需要先展示链接的调用方使用StartAuthorization和Wait。
func (m *OAuthManager) Authorize(ctx context.Context, serverName string, cfg *OAuthConfig) (*OAuthToken, error) {
	flow, err := m.StartAuthorization(ctx, serverName, cfg)
	if err != nil {
		return nil, err
	}
	return flow.Wait(ctx)
}

// GetAuthURL 兼容旧链接入口，返回真实持有回调与PKCE状态的授权链接。
func (m *OAuthManager) GetAuthURL(serverName string, cfg *OAuthConfig) string {
	flow, err := m.StartAuthorization(context.Background(), serverName, cfg)
	if err != nil {
		return ""
	}
	return flow.AuthorizationURL
}

func (m *OAuthManager) exchangeCode(ctx context.Context, cfg *OAuthConfig, code, verifier, redirectURI string) (*OAuthToken, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {verifier},
	}

	return m.tokenRequest(ctx, cfg.TokenURL, data)
}

func (m *OAuthManager) refreshToken(ctx context.Context, cfg *OAuthConfig, refreshToken string) (*OAuthToken, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {cfg.ClientID},
	}

	token, err := m.tokenRequest(ctx, cfg.TokenURL, data)
	if err == nil && token.RefreshToken == "" {
		token.RefreshToken = refreshToken
	}
	return token, err
}

func (m *OAuthManager) tokenRequest(ctx context.Context, tokenURL string, data url.Values) (*OAuthToken, error) {
	endpoint, err := oauthEndpoint(tokenURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint.String(), strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := oauthHTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("OAuth token request transport failed")
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if readErr != nil {
		return nil, fmt.Errorf("OAuth token response body could not be read completely")
	}
	if len(body) > 1<<20 {
		return nil, fmt.Errorf("OAuth token response body is too large")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && failure.Error == "invalid_grant" {
			return nil, fmt.Errorf("%w: OAuth grant is no longer valid", ErrOAuthAuthorizationRequired)
		}
		return nil, fmt.Errorf("OAuth token request returned HTTP %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    *int64 `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("OAuth token response is invalid")
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("OAuth token response has no access token")
	}
	if tokenResp.TokenType == "" {
		tokenResp.TokenType = "Bearer"
	}
	token := &OAuthToken{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		Scopes:       strings.Fields(tokenResp.Scope),
	}
	if tokenResp.ExpiresIn != nil {
		seconds := *tokenResp.ExpiresIn
		if seconds > int64((time.Duration(1<<63-1))/time.Second) || seconds < 0 {
			return nil, fmt.Errorf("OAuth token expiry is invalid")
		}
		token.ExpiresAt = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	return token, nil
}

func (m *OAuthManager) loadToken(serverName string, sources ...string) (*OAuthToken, error) {
	source := ""
	if len(sources) != 0 {
		source = sources[0]
	}
	canonical := m.tokenPath(serverName, source)
	paths := []string{canonical}
	if source != "" {
		paths = append(paths, m.tokenPath(serverName, ""))
		if legacy := legacyOAuthTokenPath(m.credDir, serverName); legacy != "" {
			paths = append(paths, legacy)
		}
	}
	found := false
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = true
		var token OAuthToken
		if err := json.Unmarshal(data, &token); err != nil {
			return nil, err
		}
		if source != "" && token.SourceFingerprint != source {
			continue
		}
		if source != "" && path != canonical {
			// 只有现有来源证明完全匹配才迁移；旧文件保留，不认领未绑定凭据。
			if err := m.saveToken(serverName, &token); err != nil {
				return nil, err
			}
		}
		return &token, nil
	}
	if found {
		return nil, fmt.Errorf("%w: stored OAuth credentials have no matching source binding", ErrOAuthAuthorizationRequired)
	}
	return nil, os.ErrNotExist
}

func (m *OAuthManager) saveToken(serverName string, token *OAuthToken) error {
	data, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	path := m.tokenPath(serverName, token.SourceFingerprint)
	file, err := os.CreateTemp(m.credDir, ".oauth-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (m *OAuthManager) serverLock(server string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.locks[server]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[server] = lock
	}
	return lock
}

// persistToken 保留已取得的新token；落盘失败必须显式返回，并在下次使用前补登记。
func (m *OAuthManager) persistToken(server string, token *OAuthToken) error {
	key := oauthTokenCacheKey(server, token.SourceFingerprint)
	m.mu.Lock()
	m.tokens[key] = cloneOAuthToken(token)
	m.dirty[key] = true
	m.mu.Unlock()
	if err := m.saveToken(server, token); err != nil {
		return fmt.Errorf("persist OAuth token: %w", err)
	}
	m.mu.Lock()
	m.dirty[key] = false
	m.mu.Unlock()
	return nil
}

func cloneOAuthToken(token *OAuthToken) *OAuthToken {
	if token == nil {
		return nil
	}
	copy := *token
	copy.Scopes = append([]string(nil), token.Scopes...)
	return &copy
}

// PKCE helpers

func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func codeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// truncateForErr caps an upstream body included in an error message so a verbose
// OAuth-server response cannot flood logs or surface large internal payloads.
func truncateForErr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…(truncated)"
}

// oauthStateValid reports whether the OAuth callback's state parameter matches
// the one we issued. An empty expected state never validates: if state
// generation failed and left it empty, a callback carrying no state must not be
// able to pass the CSRF guard via "" == "".
func oauthStateValid(expected, got string) bool {
	return expected != "" && got == expected
}

// generateRandomString 生成长度为 n 的加密安全随机字符串，用于 OAuth CSRF state
// 等不可信任场景下的一次性随机 nonce。
//
// 该 state 仅作为不透明随机值参与 oauthStateValid 的精确相等比较，对字符集无要求，
// 只要求加密安全且具备足够熵。因此底层实现下沉到 toolkit 的 rand.TryToken：
//   - 错误传播：熵源（crypto/rand）失败时返回 error 而非 panic，保留本调用点
//     "生成失败即中止授权流程"的语义（避免 state="" 时 "" == "" 击穿 CSRF 守卫）；
//   - 字符集：rand.TryToken 输出 [0-9A-Za-z] 的 AlphaNumeric 串（n 字符约 5.95n 比特
//     熵），对 CSRF nonce 而言安全裕度充足，与历史 base64url 串等价可换。
//
// PKCE code verifier 因 RFC 7636 对字符集与长度有强约束，仍保留本地 generateCodeVerifier。
func generateRandomString(n int) (string, error) {
	return randx.TryToken(n)
}

func joinScopes(scopes []string) string {
	result := ""
	for i, s := range scopes {
		if i > 0 {
			result += " "
		}
		result += s
	}
	return result
}
