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
	client *http.Client
}

// NewBrowserSkill 创建浏览器技能
func NewBrowserSkill() *BrowserSkill {
	return &BrowserSkill{
		client: httpx.MustNewRawClient(httpx.WithRawTimeout(30 * time.Second)),
	}
}

func (s *BrowserSkill) Name() string        { return "browser" }
func (s *BrowserSkill) Description() string { return "网页获取、内容提取和表单提交" }

// ToolDefinition 返回浏览器工具的 LLM 定义
func (s *BrowserSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("browser", "网页获取、内容提取和表单提交", &llm.Schema{
		Type: "object",
		Properties: map[string]*llm.Schema{
			"url":    {Type: "string", Description: "目标网页 URL"},
			"action": {Type: "string", Description: "操作类型", Enum: []any{"fetch", "extract", "post"}},
			"data":   {Type: "object", Description: "Form fields required for action=post. Values are encoded as strings. Omit for read actions."},
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
		return nil, fmt.Errorf("无效 URL: %s", targetURL)
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

func (s *BrowserSkill) doFetch(ctx context.Context, targetURL string) (*skill.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HexClaw/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, httpExecutionError(httpExecutionDetail(targetURL, "fetch", resp), "request", err)
	}
	defer resp.Body.Close()
	detail := httpExecutionDetail(targetURL, "fetch", resp)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return partialHTTPResult(body, detail, err)
	}

	text := truncateRunes(stripHTML(string(body)), maxFetchTextLen)

	return completeHTTPResult(&skill.Result{
		Content: text,
		Metadata: map[string]string{
			"url":          targetURL,
			"status":       fmt.Sprintf("%d", resp.StatusCode),
			"content_type": resp.Header.Get("Content-Type"),
		},
	}, detail)
}

func (s *BrowserSkill) doExtract(ctx context.Context, targetURL string, args map[string]any) (*skill.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "HexClaw/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, httpExecutionError(httpExecutionDetail(targetURL, "extract", resp), "request", err)
	}
	defer resp.Body.Close()
	detail := httpExecutionDetail(targetURL, "extract", resp)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
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
		links := extractLinks(html, targetURL)
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
			"link_count":  len(extractLinks(html, targetURL)),
		},
		Metadata: map[string]string{
			"url":    targetURL,
			"action": "extract",
		},
	}, detail)
}

func (s *BrowserSkill) doPost(ctx context.Context, targetURL string, args map[string]any) (*skill.Result, error) {
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

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, httpExecutionError(httpExecutionDetail(targetURL, "post", resp), "request", err)
	}
	defer resp.Body.Close()
	detail := httpExecutionDetail(targetURL, "post", resp)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return partialHTTPResult(body, detail, err)
	}

	return completeHTTPResult(&skill.Result{
		Content: stripHTML(string(body)),
		Metadata: map[string]string{
			"url":    targetURL,
			"status": fmt.Sprintf("%d", resp.StatusCode),
		},
	}, detail)
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
