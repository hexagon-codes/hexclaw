package builtin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/toolkit/net/httpx"
	"github.com/hexagon-codes/toolkit/util/retry"
)

// BrowserSkill 网页浏览技能
//
// 支持:
//   - fetch: 获取网页文本内容
//   - extract: 提取标题、链接、元描述
//   - post: 提交表单数据
type BrowserSkill struct {
	client            *http.Client
	initializationErr error
	authResolver      BrowserAuthResolver
	fileOpener        BrowserFileOpener
	artifactWriter    BrowserArtifactWriter
	attemptObserver   BrowserAttemptObserver
}

// NewBrowserSkill 创建浏览器技能
func NewBrowserSkill(opts ...BrowserSkillOption) *BrowserSkill {
	s := &BrowserSkill{
		client: httpx.MustNewRawClient(httpx.WithRawTimeout(30 * time.Second)),
	}
	s.initializationErr = observeBrowserHTTPTransport(s.client)
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *BrowserSkill) Name() string { return "browser" }
func (s *BrowserSkill) Description() string {
	return "Fetch web pages, extract content, submit forms, or call HTTP APIs"
}

// ToolDefinition 返回浏览器工具的 LLM 定义
func (s *BrowserSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("browser", "Fetch pages, extract content, submit forms, or make HTTP API requests. API results preserve transport status; verify business success separately.", &llm.Schema{
		Type: "object",
		Properties: map[string]*llm.Schema{
			"url":           {Type: "string", Description: "目标网页 URL"},
			"action":        {Type: "string", Description: "Operation; defaults to fetch", Enum: []any{"fetch", "extract", "post", "request"}},
			"data":          {Type: "object", Description: "Form fields required for action=post. Values are encoded as strings. Omit for read actions."},
			"selector":      {Type: "string", Description: "For extract: title, links, meta, or text"},
			"method":        {Type: "string", Description: "HTTP method for request", Enum: []any{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}},
			"query":         {Type: "array", Description: "Additional query parameters, preserving repeated names", Items: &llm.Schema{Type: "object", Properties: map[string]*llm.Schema{"name": {Type: "string"}, "value": {Type: "string"}}, Required: []string{"name", "value"}}},
			"headers":       {Type: "object", Description: "Custom HTTP headers; prefer auth_ref for credentials"},
			"body_kind":     {Type: "string", Description: "Request body encoding", Enum: []any{"none", "json", "text", "form", "multipart", "file"}},
			"json":          {Description: "JSON request value, including objects, arrays, and scalar values"},
			"text":          {Type: "string", Description: "UTF-8 request body for body_kind=text"},
			"form":          {Type: "object", Description: "Form fields; array values preserve repeated fields"},
			"files":         {Type: "array", Description: "Multipart files: name, path, optional filename and content_type", Items: &llm.Schema{Type: "object", Properties: map[string]*llm.Schema{"name": {Type: "string"}, "path": {Type: "string"}, "filename": {Type: "string"}, "content_type": {Type: "string"}}, Required: []string{"name", "path"}}},
			"file":          {Type: "string", Description: "Authorized input file reference or path for a raw file body"},
			"content_type":  {Type: "string", Description: "Request body MIME type; defaults according to body_kind"},
			"auth_ref":      {Type: "string", Description: "Existing service authentication reference; unrelated Provider credentials are never borrowed"},
			"auth":          {Type: "object", Description: "Task-provided temporary auth: type=none|bearer|basic|api_key, token or username/password or in/name/value"},
			"auth_override": {Type: "string", Description: "Only when explicitly instructed, resolve conflicting auth in favor of explicit or reference", Enum: []any{"explicit", "reference"}},
		},
		Required: []string{"url"},
	})
}

func (s *BrowserSkill) Match(content string) bool {
	lower := strings.ToLower(content)
	keywords := []string{"fetch url", "打开网页", "获取网页", "browse ", "extract from"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func (s *BrowserSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	if s.initializationErr != nil {
		return nil, fmt.Errorf("HTTP tool transport initialization failed: %w", s.initializationErr)
	}
	action, _ := args["action"].(string)
	targetURL, _ := args["url"].(string)

	if targetURL == "" {
		// 尝试从 query 中提取 URL
		if query, ok := args["query"].(string); ok {
			targetURL = extractURL(query)
		}
	}

	if targetURL == "" {
		return nil, fmt.Errorf("缺少 url 参数")
	}

	// 验证 URL
	parsed, err := url.Parse(targetURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid HTTP URL")
	}
	if action == "request" {
		return s.doRequest(ctx, targetURL, args)
	}

	if action == "post" {
		return s.doPost(ctx, targetURL, args)
	}

	// 只重试读取；每次请求仍使用客户端超时，取消与总截止由原上下文决定。
	const maxReadRetries = 3
	var result *skill.Result
	err = retry.DoWithContext(ctx, func() error {
		var readErr error
		if action == "extract" {
			result, readErr = s.doExtract(ctx, targetURL, args)
		} else {
			result, readErr = s.doFetch(ctx, targetURL)
		}
		return readErr
	}, retry.Attempts(maxReadRetries+1), retry.Delay(time.Second), retry.MaxDelay(4*time.Second),
		retry.Multiplier(2), retry.DelayType(retry.ExponentialBackoff),
		retry.If(func(readErr error) bool {
			var failure *skill.ExecutionError
			if errors.As(readErr, &failure) && failure.Detail.Reason == "receipt_store" {
				return false
			}
			if errors.As(readErr, &failure) && failure.Detail.Reason == "http_status" {
				return retry.IsRetryableHTTPError(&retry.HTTPError{StatusCode: failure.Detail.HTTPStatus})
			}
			return retry.IsRetryableHTTPError(readErr)
		}), retry.OnRetry(func(n int, _ error) {
			slog.InfoContext(ctx, "Retrying browser read", "action", action, "retry", n, "max_retries", maxReadRetries)
		}))
	return result, err
}

const maxBodySize = 64 << 20 // 64MB（桌面端按宿主机语义放宽，覆盖任意正常网页/接口响应，仅防 OOM）

// maxFetchTextLen 限制 fetch 返回给 LLM 的正文长度（rune 计）。整页可见文字
// 常含大量导航/重复噪声，无界回灌会淹没（尤其是弱模型）。这是卫生上界，
// 正常页面不触发；要结构化内容应优先用 extract。
const maxFetchTextLen = 12000

// truncateRunes 按 rune 边界截断，避免切碎 UTF-8（中文）字符。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n…[内容过长已截断]"
}

func (s *BrowserSkill) doFetch(ctx context.Context, targetURL string) (result *skill.Result, resultErr error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HexClaw/1.0")

	resp, req, done, err := s.dispatchHTTPRequest(req)
	defer func() { result, resultErr = completeHTTPAttempt(result, resultErr, done) }()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	detail := httpExecutionDetail(req.URL.String(), "fetch", resp)

	body, err := readBufferedHTTPBody(resp.Body)
	if err != nil {
		return partialHTTPResult(body, detail, err)
	}

	text := truncateRunes(stripHTML(string(body)), maxFetchTextLen)

	return completeHTTPResult(&skill.Result{
		Content: text,
		Metadata: map[string]string{
			"url":          safeHTTPRequestURL(req.URL),
			"status":       fmt.Sprintf("%d", resp.StatusCode),
			"content_type": resp.Header.Get("Content-Type"),
		},
	}, detail)
}

func (s *BrowserSkill) doExtract(ctx context.Context, targetURL string, args map[string]any) (result *skill.Result, resultErr error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HexClaw/1.0")

	resp, req, done, err := s.dispatchHTTPRequest(req)
	defer func() { result, resultErr = completeHTTPAttempt(result, resultErr, done) }()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	detail := httpExecutionDetail(req.URL.String(), "extract", resp)

	body, err := readBufferedHTTPBody(resp.Body)
	if err != nil {
		return partialHTTPResult(body, detail, err)
	}
	html := string(body)

	selector, _ := args["selector"].(string)
	var content string

	switch selector {
	case "title":
		content = extractTitle(html)
	case "links":
		links := extractLinks(html, req.URL.String())
		content = strings.Join(links, "\n")
	case "meta":
		content = extractMetaDescription(html)
	default: // "text"
		content = stripHTML(html)
	}

	return completeHTTPResult(&skill.Result{
		Content: content,
		Data: map[string]any{
			"title":       extractTitle(html),
			"description": extractMetaDescription(html),
			"link_count":  len(extractLinks(html, req.URL.String())),
		},
		Metadata: map[string]string{
			"url":    safeHTTPRequestURL(req.URL),
			"action": "extract",
		},
	}, detail)
}

func (s *BrowserSkill) doPost(ctx context.Context, targetURL string, args map[string]any) (result *skill.Result, resultErr error) {
	// JSON unmarshals to map[string]any, so accept that and convert values to strings
	rawData, _ := args["data"].(map[string]any)
	if rawData == nil {
		return nil, fmt.Errorf("post 请求缺少 data 参数")
	}

	form := url.Values{}
	for k, v := range rawData {
		form.Set(k, fmt.Sprint(v))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "HexClaw/1.0")

	resp, req, done, err := s.dispatchHTTPRequest(req)
	defer func() { result, resultErr = completeHTTPAttempt(result, resultErr, done) }()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	detail := httpExecutionDetail(req.URL.String(), strings.ToLower(req.Method), resp)

	body, err := readBufferedHTTPBody(resp.Body)
	if err != nil {
		return partialHTTPResult(body, detail, err)
	}

	return completeHTTPResult(&skill.Result{
		Content: stripHTML(string(body)),
		Metadata: map[string]string{
			"url":    safeHTTPRequestURL(req.URL),
			"status": fmt.Sprintf("%d", resp.StatusCode),
		},
	}, detail)
}

// readBufferedHTTPBody 保留旧提取路径的内存边界，超限明确失败，不能把限长EOF当完整响应。
func readBufferedHTTPBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxBodySize+1))
	if err == nil && len(body) > maxBodySize {
		return body[:maxBodySize], fmt.Errorf("response exceeds in-memory extraction size; use request with response artifact storage")
	}
	return body, err
}

// HTML 处理辅助函数

var (
	reTag     = regexp.MustCompile(`<[^>]*>`)
	reSpace   = regexp.MustCompile(`\s{2,}`)
	reScript  = regexp.MustCompile(`(?is)<script.*?</script>`)
	reStyle   = regexp.MustCompile(`(?is)<style.*?</style>`)
	reTitle   = regexp.MustCompile(`(?i)<title[^>]*>(.*?)</title>`)
	reLink    = regexp.MustCompile(`(?i)<a[^>]+href=["']([^"']+)["']`)
	reMeta    = regexp.MustCompile(`(?i)<meta[^>]+name=["']description["'][^>]+content=["']([^"']*)["']`)
	reMetaAlt = regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']*)["'][^>]+name=["']description["']`)
	reURL     = regexp.MustCompile(`https?://[^\s<>"']+`)
)

func stripHTML(html string) string {
	// 移除 script 和 style（使用包级预编译正则）
	html = reScript.ReplaceAllString(html, "")
	html = reStyle.ReplaceAllString(html, "")

	text := reTag.ReplaceAllString(html, " ")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = reSpace.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func extractTitle(html string) string {
	matches := reTitle.FindStringSubmatch(html)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

func extractLinks(html, baseURL string) []string {
	matches := reLink.FindAllStringSubmatch(html, -1)
	var links []string
	seen := make(map[string]bool)
	base, _ := url.Parse(baseURL)

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		href := m[1]
		if strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") {
			continue
		}
		// 解析相对 URL
		if parsed, err := url.Parse(href); err == nil && base != nil {
			href = base.ResolveReference(parsed).String()
		}
		if !seen[href] {
			seen[href] = true
			links = append(links, href)
		}
	}
	return links
}

func extractMetaDescription(html string) string {
	matches := reMeta.FindStringSubmatch(html)
	if len(matches) > 1 {
		return matches[1]
	}
	matches = reMetaAlt.FindStringSubmatch(html)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}

func extractURL(text string) string {
	match := reURL.FindString(text)
	return match
}
