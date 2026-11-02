package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuthAuthorization 的公开字段只承载授权入口，token和PKCE verifier只保留在进程内。
type OAuthAuthorization struct {
	ServerName       string    `json:"server_name"`
	AuthorizationURL string    `json:"authorization_url"`
	State            string    `json:"state"`
	RedirectURI      string    `json:"redirect_uri"`
	ExpiresAt        time.Time `json:"expires_at"`

	mu                sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	config            OAuthConfig
	sourceFingerprint string
	verifier          string
	started           bool
	finished          bool
	token             *OAuthToken
	err               error
}

// Wait 只取消本次等待，不取消授权现场；显式取消通过Cancel执行。
func (a *OAuthAuthorization) Wait(ctx context.Context) (*OAuthToken, error) {
	select {
	case <-a.done:
		a.mu.Lock()
		defer a.mu.Unlock()
		return cloneOAuthToken(a.token), a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *OAuthAuthorization) Cancel() { a.finish(nil, context.Canceled) }

// Status 只返回流程事实，不暴露授权码、token或验证材料。
func (a *OAuthAuthorization) Status() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.finished {
		if a.started {
			return "completing"
		}
		return "pending"
	}
	if a.err == nil {
		return "authorized"
	}
	if errors.Is(a.err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(a.err, context.DeadlineExceeded) {
		return "expired"
	}
	return "failed"
}

func (a *OAuthAuthorization) finish(token *OAuthToken, err error) {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return
	}
	a.finished = true
	a.token = cloneOAuthToken(token)
	a.err = err
	close(a.done)
	a.mu.Unlock()
	a.cancel()
}

func oauthEndpoint(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return nil, fmt.Errorf("OAuth endpoint must be an absolute HTTP URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u, nil
}

// StartAuthorization 先建立可接续的PKCE流程再交付链接。ctx属于应用任务生命期，
// 不应绑定立即返回的HTTP请求；配置RedirectURI时由已有回调入口调用CompleteAuthorization。
func (m *OAuthManager) StartAuthorization(ctx context.Context, server string, config *OAuthConfig) (*OAuthAuthorization, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.initErr != nil {
		return nil, fmt.Errorf("initialize OAuth credential storage: %w", m.initErr)
	}
	if server == "" || config == nil || config.ClientID == "" {
		return nil, fmt.Errorf("OAuth server and client configuration are required")
	}
	config = OAuthConfigForResource(config, "")
	source, err := oauthSourceFingerprint(server, config)
	if err != nil {
		return nil, err
	}
	authURL, err := oauthEndpoint(config.AuthURL)
	if err != nil {
		return nil, err
	}
	if _, err := oauthEndpoint(config.TokenURL); err != nil {
		return nil, err
	}
	lock := m.serverLock(server)
	lock.Lock()
	defer lock.Unlock()
	m.setCurrentSource(server, source)
	m.mu.Lock()
	for state, flow := range m.flows {
		if time.Now().After(flow.ExpiresAt) {
			delete(m.flows, state)
			continue
		}
		flow.mu.Lock()
		active := !flow.finished && flow.ServerName == server
		flow.mu.Unlock()
		if active {
			if flow.MatchesConfiguration(config) {
				m.mu.Unlock()
				return flow, nil
			}
			flow.finish(nil, fmt.Errorf("%w: OAuth authorization configuration changed", ErrOAuthAuthorizationRequired))
		}
	}
	m.mu.Unlock()
	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	state, err := generateRandomString(32)
	if err != nil {
		return nil, fmt.Errorf("generate OAuth state: %w", err)
	}
	redirect := config.RedirectURI
	var listener net.Listener
	if redirect == "" {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("start OAuth callback listener: %w", err)
		}
		redirect = "http://" + listener.Addr().String() + "/callback"
	} else if _, err := oauthEndpoint(redirect); err != nil {
		return nil, err
	}
	flowCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	expires, _ := flowCtx.Deadline()
	cfg := *config
	cfg.Scopes = append([]string(nil), config.Scopes...)
	query := authURL.Query()
	query.Set("response_type", "code")
	query.Set("client_id", cfg.ClientID)
	query.Set("redirect_uri", redirect)
	query.Set("state", state)
	query.Set("code_challenge", codeChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	if len(cfg.Scopes) > 0 {
		query.Set("scope", joinScopes(cfg.Scopes))
	}
	authURL.RawQuery = query.Encode()
	flow := &OAuthAuthorization{ServerName: server, AuthorizationURL: authURL.String(), State: state,
		RedirectURI: redirect, ExpiresAt: expires, ctx: flowCtx, cancel: cancel, done: make(chan struct{}),
		config: cfg, sourceFingerprint: source, verifier: verifier}
	m.mu.Lock()
	m.flows[state] = flow
	m.mu.Unlock()
	var callbackServer *http.Server
	if listener != nil {
		mux := http.NewServeMux()
		mux.HandleFunc("/callback", func(w http.ResponseWriter, request *http.Request) {
			values := request.URL.Query()
			if !oauthStateValid(flow.State, values.Get("state")) {
				http.Error(w, "State mismatch", http.StatusBadRequest)
				return
			}
			if err := m.CompleteAuthorization(request.Context(), flow.State, values.Get("code"), values.Get("error")); err != nil {
				http.Error(w, "OAuth authorization could not be completed", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<html><body><h2>Authorization successful!</h2><p>You can close this window.</p><script>window.close()</script></body></html>`)
		})
		callbackServer = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := callbackServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				flow.finish(nil, fmt.Errorf("OAuth callback listener stopped"))
			}
		}()
	}
	go func() {
		<-flowCtx.Done()
		flow.finish(nil, flowCtx.Err())
		if callbackServer != nil {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := callbackServer.Shutdown(cleanupCtx); err != nil {
				callbackServer.Close()
			}
		}
	}()
	return flow, nil
}

// CompleteAuthorization 对一个已持有的state最多交换一次授权码；不会因重复回调重发。
// token请求使用原流程context，回调连接中断不会丢弃已取得的凭据结果。
func (m *OAuthManager) CompleteAuthorization(ctx context.Context, state, code, authorizationError string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	flow := m.flows[state]
	m.mu.Unlock()
	if flow == nil || !oauthStateValid(flow.State, state) {
		return fmt.Errorf("OAuth authorization state is invalid or expired")
	}
	flow.mu.Lock()
	if flow.finished || flow.started {
		flow.mu.Unlock()
		return fmt.Errorf("OAuth authorization callback was already handled")
	}
	if authorizationError != "" {
		flow.mu.Unlock()
		err := fmt.Errorf("OAuth authorization was denied")
		flow.finish(nil, err)
		return err
	}
	if code == "" {
		flow.mu.Unlock()
		return fmt.Errorf("OAuth authorization code is missing")
	}
	flow.started = true
	flow.mu.Unlock()
	lock := m.serverLock(flow.ServerName)
	lock.Lock()
	defer lock.Unlock()
	if !m.sourceIsCurrent(flow.ServerName, flow.sourceFingerprint) {
		err := fmt.Errorf("%w: OAuth authorization no longer matches the current service", ErrOAuthAuthorizationRequired)
		flow.finish(nil, err)
		return err
	}
	token, err := m.exchangeCode(flow.ctx, &flow.config, code, flow.verifier, flow.RedirectURI)
	if err == nil {
		if len(token.Scopes) == 0 {
			token.Scopes = append([]string(nil), flow.config.Scopes...)
		}
		token.SourceFingerprint = flow.sourceFingerprint
		err = m.persistToken(flow.ServerName, token)
	}
	flow.finish(token, err)
	return err
}

// HTTPClient 为确切服务复用已有HTTP配置；刷新发生在派发前，401不会自动重放原请求。
func (m *OAuthManager) HTTPClient(ctx context.Context, server string, config *OAuthConfig, endpoint string, base *http.Client) (*http.Client, error) {
	target, err := oauthEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	config = OAuthConfigForResource(config, endpoint)
	if _, err := m.GetToken(ctx, server, config); err != nil {
		return nil, err
	}
	if base == nil {
		base = oauthHTTPClient
	}
	client := *base
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	var cfg OAuthConfig
	if config != nil {
		cfg = *config
		cfg.Scopes = append([]string(nil), config.Scopes...)
	}
	client.Transport = &oauthTransport{base: transport, manager: m, server: server, config: cfg, origin: oauthOrigin(target)}
	return &client, nil
}

type oauthTransport struct {
	base    http.RoundTripper
	manager *OAuthManager
	server  string
	config  OAuthConfig
	origin  string
}

func oauthOrigin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

func (t *oauthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// 重定向到其他服务时沿标准HTTP行为，不把此账号token注入另一个目标。
	if oauthOrigin(request.URL) != t.origin || request.Header.Get("Authorization") != "" {
		return t.base.RoundTrip(request)
	}
	// 已建立的旧连接只使用自己的来源，不把后台清理反写为当前配置。
	token, err := t.manager.getToken(request.Context(), t.server, &t.config, false)
	if err != nil {
		return nil, err
	}
	copy := request.Clone(request.Context())
	copy.Header = request.Header.Clone()
	tokenType := token.TokenType
	// Bearer 类型大小写不敏感，发送标准拼写以兼容服务端认证解析。
	if strings.EqualFold(tokenType, "Bearer") {
		tokenType = "Bearer"
	}
	copy.Header.Set("Authorization", tokenType+" "+token.AccessToken)
	return t.base.RoundTrip(copy)
}
