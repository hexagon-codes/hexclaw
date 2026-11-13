package builtin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/hexagon-codes/hexclaw/skill"
	"golang.org/x/net/http2"
)

type httpAuthScopeKey struct{}

// httpAuthScope 仅保存本次目标的内存认证范围，不进入回执或配置。
type httpAuthScope struct {
	headers map[string]struct{}
	queries map[string][]string
	secrets []string
}

func rememberHTTPRequestAuth(req *http.Request, auth HTTPRequestAuth) {
	scope, _ := req.Context().Value(httpAuthScopeKey{}).(*httpAuthScope)
	if scope == nil {
		scope = &httpAuthScope{headers: map[string]struct{}{}, queries: map[string][]string{}}
	}
	for _, value := range []string{auth.Token, auth.Password, auth.Value} {
		if value != "" {
			scope.secrets = append(scope.secrets, value)
		}
	}
	if strings.EqualFold(auth.Type, "api_key") {
		if auth.In == "header" {
			scope.headers[http.CanonicalHeaderKey(auth.Name)] = struct{}{}
		} else if auth.In == "query" {
			scope.queries[auth.Name] = append([]string(nil), req.URL.Query()[auth.Name]...)
		}
	}
	for name := range req.Header {
		switch strings.ToLower(name) {
		case "authorization", "proxy-authorization", "cookie", "x-api-key", "api-key", "x-auth-token", "x-access-token", "x-goog-api-key", "private-token":
			scope.headers[http.CanonicalHeaderKey(name)] = struct{}{}
		}
	}
	for name := range scope.headers {
		for _, value := range req.Header.Values(name) {
			if value != "" {
				scope.secrets = append(scope.secrets, value)
			}
		}
	}
	if value := req.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(value), "bearer ") {
		scope.secrets = append(scope.secrets, value[len("Bearer "):])
	}
	if _, password, ok := req.BasicAuth(); ok && password != "" {
		scope.secrets = append(scope.secrets, password)
	}
	if req.URL.User != nil {
		if password, ok := req.URL.User.Password(); ok && password != "" {
			scope.secrets = append(scope.secrets, password)
		}
	}
	for name, values := range req.URL.Query() {
		switch strings.ToLower(name) {
		case "api_key", "apikey", "access_token", "auth_token", "token", "signature":
			scope.queries[name] = append([]string(nil), values...)
		}
	}
	for _, values := range scope.queries {
		for _, value := range values {
			if value != "" {
				scope.secrets = append(scope.secrets, value)
			}
		}
	}
	*req = *req.WithContext(context.WithValue(req.Context(), httpAuthScopeKey{}, scope))
}

func httpOrigin(target *url.URL) string {
	port := target.Port()
	if port == "" {
		if target.Scheme == "https" {
			port = "443"
		} else if target.Scheme == "http" {
			port = "80"
		}
	}
	return strings.ToLower(target.Scheme) + "://" + strings.ToLower(target.Hostname()) + ":" + port
}

func stripRedirectAuth(previous, next *http.Request) {
	scope, _ := previous.Context().Value(httpAuthScopeKey{}).(*httpAuthScope)
	if scope == nil {
		return
	}
	if httpOrigin(previous.URL) != httpOrigin(next.URL) {
		for name := range scope.headers {
			next.Header.Del(name)
		}
		next.Header.Del("Authorization")
		next.Header.Del("Proxy-Authorization")
		next.Header.Del("Cookie")
		next.URL.User = nil
		stripAuthQueryValues(next.URL, scope)
	}
	if referer := next.Header.Get("Referer"); referer != "" {
		if parsed, err := url.Parse(referer); err == nil {
			parsed.User = nil
			stripAuthQueryValues(parsed, scope)
			next.Header.Set("Referer", parsed.String())
		}
	}
}

func stripAuthQueryValues(target *url.URL, scope *httpAuthScope) {
	query := target.Query()
	for name, values := range query {
		kept := values[:0]
		for _, value := range values {
			secret := false
			for _, candidate := range scope.secrets {
				if value == candidate {
					secret = true
					break
				}
			}
			if !secret {
				kept = append(kept, value)
			}
		}
		if len(kept) == 0 {
			query.Del(name)
		} else {
			query[name] = kept
		}
	}
	target.RawQuery = query.Encode()
}

// dispatchHTTPRequest 显式推进重定向，每次实际客户端请求均先登记并闭合独立回执。
// 最后一次响应由调用方读取和登记；中间响应必须收口成功才允许继续派发。
func (s *BrowserSkill) dispatchHTTPRequest(initial *http.Request) (*http.Response, *http.Request, func(*skill.Result, error) error, error) {
	rememberHTTPRequestAuth(initial, HTTPRequestAuth{})
	baseCtx := initial.Context()
	client := *s.client
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cancel := func() {}
	if client.Timeout > 0 {
		baseCtx, cancel = context.WithTimeout(baseCtx, client.Timeout)
		client.Timeout = 0
	}
	noop := func(*skill.Result, error) error { return nil }
	current := initial
	var via []*http.Request
	connectionRecoveries := 0
	for {
		attemptCtx, finish, err := s.beginHTTPAttempt(baseCtx, current.Method, current.URL.String())
		if err != nil {
			if current.Body != nil {
				_ = current.Body.Close()
			}
			cancel()
			return nil, current, noop, err
		}
		var connected atomic.Bool
		trace := &httptrace.ClientTrace{
			GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
		}
		current = current.WithContext(httptrace.WithClientTrace(attemptCtx, trace))
		response, err := client.Do(current)
		if err != nil {
			detail := httpExecutionDetail(current.URL.String(), strings.ToLower(current.Method), response)
			failure := httpExecutionError(detail, "request", err)
			if isHTTPWriteMethod(current.Method) && !connected.Load() && httpDefinitelyNotDispatched(err) {
				failure.Detail.Outcome, failure.Detail.Phase = "failure", "prepare"
			}
			facts := map[string]any{"method": current.Method, "url": safeHTTPRequestURL(current.URL), "response_complete": false}
			var replayErr *httpTransportReplayError
			blockedReplay := errors.As(err, &replayErr)
			if blockedReplay {
				failure.Detail.Reason = "transport_replay_blocked"
				facts["transport_replay"] = httpTransportReplayFacts(err)
			}
			_, failureErr := completeHTTPAttempt(&skill.Result{Detail: &failure.Detail, Data: facts}, failure, finish)
			var receiptFailure *skill.ExecutionError
			stored := !(errors.As(failureErr, &receiptFailure) && receiptFailure.Detail.Reason == "receipt_store")
			if recoverableHTTPReadFailure(err) && stored && baseCtx.Err() == nil && connectionRecoveries == 0 && isHTTPReadMethod(current.Method) {
				retry := current.Clone(baseCtx)
				if current.Body != nil && current.Body != http.NoBody {
					if current.GetBody == nil {
						cancel()
						return response, current, noop, failureErr
					}
					retry.Body, err = current.GetBody()
					if err != nil {
						cancel()
						return response, current, noop, errors.Join(failureErr, fmt.Errorf("HTTP read body could not be reopened"))
					}
				}
				// 安全读取的恢复由新一轮完整observer授权与持久记录负责，不能覆盖首尝试。
				connectionRecoveries++
				current = retry
				continue
			}
			cancel()
			return response, current, noop, failureErr
		}
		next, redirectErr := nextHTTPRequest(current, response)
		if redirectErr == nil && next != nil {
			via = append(via, current)
			if checkRedirect != nil {
				redirectErr = checkRedirect(next, via)
			} else if len(via) >= 10 {
				redirectErr = fmt.Errorf("stopped after 10 redirects")
			}
		}
		if next == nil || errors.Is(redirectErr, http.ErrUseLastResponse) {
			if next != nil && next.Body != nil {
				_ = next.Body.Close()
			}
			return response, current, func(result *skill.Result, err error) error { defer cancel(); return finish(result, err) }, nil
		}
		detail := httpExecutionDetail(current.URL.String(), strings.ToLower(current.Method), response)
		detail.Outcome, detail.Reason = "success", ""
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		intermediate := &skill.Result{Detail: &detail, Data: map[string]any{"method": current.Method, "url": safeHTTPRequestURL(current.URL), "status": response.StatusCode, "redirect": true, "response_complete": readErr == nil}}
		var attemptErr error
		if readErr != nil || closeErr != nil {
			failure := httpExecutionError(detail, "read_response", errors.Join(readErr, closeErr))
			intermediate.Detail, attemptErr = &failure.Detail, failure
		} else if redirectErr != nil {
			detail.Outcome, detail.Phase, detail.Reason = "failure", "redirect", "redirect_target"
			intermediate.Detail = &detail
			attemptErr = &skill.ExecutionError{Detail: detail, Err: &httpPublicError{phase: "redirect", reason: "redirect_target", cause: redirectErr}}
		}
		_, attemptErr = completeHTTPAttempt(intermediate, attemptErr, finish)
		if attemptErr != nil {
			if next.Body != nil {
				_ = next.Body.Close()
			}
			cancel()
			return response, current, noop, attemptErr
		}
		current = next.WithContext(baseCtx)
		connectionRecoveries = 0
	}
}

func isHTTPReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// recoverableHTTPReadFailure 仅接纳已确认可恢复的连接终止与拒绝流，不泛化协议或配置错误。
func recoverableHTTPReadFailure(err error) bool {
	var replay *httpTransportReplayError
	var stream http2.StreamError
	return errors.As(err, &replay) || errors.Is(err, io.EOF) ||
		errors.As(err, &stream) && stream.Code == http2.ErrCodeRefusedStream
}

func nextHTTPRequest(current *http.Request, response *http.Response) (*http.Request, error) {
	method := current.Method
	includeBody := false
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther:
		if method != http.MethodGet && method != http.MethodHead {
			method = http.MethodGet
		}
	case http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		includeBody = true
		if current.Body != nil && current.Body != http.NoBody && current.GetBody == nil {
			return nil, nil
		}
	default:
		return nil, nil
	}
	location := response.Header.Get("Location")
	if location == "" {
		return nil, nil
	}
	target, err := current.URL.Parse(location)
	if err != nil {
		return &http.Request{}, fmt.Errorf("invalid redirect location")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return &http.Request{}, fmt.Errorf("unsupported redirect URL scheme")
	}
	var body io.ReadCloser
	if includeBody && current.GetBody != nil {
		body, err = current.GetBody()
		if err != nil {
			return &http.Request{}, fmt.Errorf("redirect body could not be reopened")
		}
	}
	next, err := http.NewRequestWithContext(current.Context(), method, target.String(), body)
	if err != nil {
		if body != nil {
			_ = body.Close()
		}
		return &http.Request{}, fmt.Errorf("invalid redirect request")
	}
	next.Header = current.Header.Clone()
	if includeBody {
		next.GetBody, next.ContentLength = current.GetBody, current.ContentLength
	} else {
		next.Header.Del("Content-Length")
		next.Header.Del("Content-Type")
		next.Header.Del("Content-Encoding")
	}
	if !(current.URL.Scheme == "https" && target.Scheme == "http") {
		referer := *current.URL
		referer.User, referer.Fragment = nil, ""
		next.Header.Set("Referer", referer.String())
	} else {
		next.Header.Del("Referer")
	}
	stripRedirectAuth(current, next)
	return next, nil
}
