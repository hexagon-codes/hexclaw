package engine

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

//go:embed product_facts.json
var productFactsSource []byte

var productFactsOnce sync.Once
var productFactsPublicJSON string
var productFactsLoadError error

var hostFactsSubject = regexp.MustCompile(`(?i)hexclaw|河蟹|小蟹|本应用|本软件|你|您|\byou(?:r|rself)?\b`)
var hostFactsTopic = regexp.MustCompile(`(?i)官网|官方网站|公众号|github|仓库|官方入口|二维码|介绍|是谁|名字|名称|功能|能力|能做|会做|website|official account|introduc|who are`)
var hostFactsQueryWords = regexp.MustCompile(`(?i)不要图片|不用图片|不带图|不附图|只用文字|不要推广|一句话|回答|叫什么|能做什么|会做什么|微信公众号|官方github|github地址|官方仓库|官方网站|官方入口|公众号id|公众号名称|公众号|二维码|官网|github|仓库|地址|链接|名称|名字|id|完整介绍|详细介绍|自我介绍|介绍一下|介绍|主要功能|主要能力|功能|能力|你自己|你们|你|您|hexclaw|河蟹ai|河蟹|小蟹|本应用|本软件|是什么|是多少|是什么样的|是谁|怎么关注|如何关注|有哪些|有没有|多少|是否|可以|能不能|知道|告诉我|给出|提供|请问|一下|完整|详细|简要|简单|有|的|和|与|及|吗|呢|吧|请|用|但|也|yourself|your|you|what is|what are|who are|website|official account|official|github|repository|repositories|qr code|introduce|introduction|full|detailed|please|and|have|do|a|an|the`)
var hostFactsQueryPunctuation = regexp.MustCompile(`[\s\p{P}\p{S}]+`)
var hostFactsGreeting = regexp.MustCompile(`(?i)^\s*(?:你好|您好|hello|hi)(?:[，,！!：:\s]+|$)`)
var hostFactsRequestModifiers = regexp.MustCompile(`(?i)发送给我|分享给我|发给我|提供给我|给我发|帮我|麻烦|以及|还有|怎么搜索|如何搜索|在哪里搜|在哪搜|怎么搜|怎么找|如何查找|怎样关注|在哪里|在哪|发一下|分享一下|给我|把`)

// isStandaloneHostFactsQuery 仅识别能完全由宿主事实回答的独立问询。
// 未消费的题目、第三方名称和资料指令保留原检索路径，避免混合请求被短路。
func isStandaloneHostFactsQuery(content string) bool {
	content = hostFactsGreeting.ReplaceAllString(strings.TrimSpace(content), "")
	if !hostFactsSubject.MatchString(content) || !hostFactsTopic.MatchString(content) {
		return false
	}
	// 先处理完整修饰短语，保留未识别的实体、题目和资料指令。
	remaining := hostFactsRequestModifiers.ReplaceAllString(strings.ToLower(content), "")
	remaining = hostFactsQueryWords.ReplaceAllString(remaining, "")
	return hostFactsQueryPunctuation.ReplaceAllString(remaining, "") == ""
}

// hostWechatReplySuffix 补齐明确请求中的已确认二维码，避免模型漏图。
// 只追加本次缺失的区块；短介绍、无图约束及已有二维码不重复处理。
func hostWechatReplySuffix(query, content string) string {
	if strings.TrimSpace(content) == "" || !isStandaloneHostFactsQuery(query) {
		return ""
	}
	lower := strings.ToLower(query)
	for _, exclusion := range []string{"不要图片", "不用图片", "不带图", "不附图", "只用文字", "不要推广", "no image", "no promotion"} {
		if strings.Contains(lower, exclusion) {
			return ""
		}
	}
	accountQuestion := strings.Contains(lower, "公众号") || strings.Contains(lower, "official account")
	fullIntroduction := (strings.Contains(lower, "完整") || strings.Contains(lower, "详细") || strings.Contains(lower, "full") || strings.Contains(lower, "detailed")) &&
		(strings.Contains(lower, "介绍") || strings.Contains(lower, "introduc"))
	if !accountQuestion && !fullIntroduction {
		return ""
	}
	facts, err := PublicProductFactsJSON()
	if err != nil {
		return ""
	}
	var document struct {
		Accounts struct {
			Wechat struct {
				Status        string `json:"status"`
				ID            string `json:"id"`
				IDLabel       string `json:"id_label"`
				QRCode        string `json:"qr_code"`
				FollowMessage string `json:"follow_message"`
			} `json:"wechat"`
		} `json:"official_accounts"`
	}
	if json.Unmarshal([]byte(facts), &document) != nil {
		return ""
	}
	account := document.Accounts.Wechat
	if account.Status != "confirmed_present" || account.QRCode == "" || account.ID == "" {
		return ""
	}
	image := regexp.MustCompile(`!\[[^\]\n]*\]\(` + regexp.QuoteMeta(account.QRCode) + `\)`)
	if image.MatchString(content) {
		return ""
	}
	return "\n\n![微信公众号二维码](" + account.QRCode + ")\n\n" + account.IDLabel + "：" + account.ID + "  \n" + account.FollowMessage
}

// PublicProductFactsJSON 返回与客户端公开副本同源的产品事实，不包含当前用户配置。
func PublicProductFactsJSON() (string, error) {
	productFactsOnce.Do(func() {
		productFactsPublicJSON, productFactsLoadError = buildPublicProductFacts(productFactsSource)
	})
	return productFactsPublicJSON, productFactsLoadError
}

func buildPublicProductFacts(source []byte) (string, error) {
	var document map[string]any
	if err := json.Unmarshal(source, &document); err != nil {
		return "", fmt.Errorf("decode product facts: %w", err)
	}
	public, ok := publicProductFactValue(document)
	if !ok {
		return "", fmt.Errorf("product facts have no public projection")
	}
	projection, ok := public.(map[string]any)
	if !ok || projection["product_id"] != "hexclaw" {
		return "", fmt.Errorf("invalid product facts identity")
	}
	payload, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("encode public product facts: %w", err)
	}
	digest := sha256.Sum256(payload)
	projection["facts_digest"] = "sha256:" + hex.EncodeToString(digest[:])
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", fmt.Errorf("encode product facts envelope: %w", err)
	}
	return string(encoded), nil
}

// publicProductFactValue 在每层去掉内部项，公开摘要只覆盖消费者实际获得的内容。
func publicProductFactValue(value any) (any, bool) {
	switch item := value.(type) {
	case map[string]any:
		if item["visibility"] == "internal" {
			return nil, false
		}
		out := make(map[string]any, len(item))
		for key, child := range item {
			if strings.HasPrefix(key, "_") || key == "visibility" || key == "facts_digest" || key == "source_digest" {
				continue
			}
			if public, ok := publicProductFactValue(child); ok {
				out[key] = public
			}
		}
		return out, true
	case []any:
		out := make([]any, 0, len(item))
		for _, child := range item {
			if public, ok := publicProductFactValue(child); ok {
				out = append(out, public)
			}
		}
		return out, true
	default:
		return value, true
	}
}

func productFactsContext() string {
	facts, err := PublicProductFactsJSON()
	if err != nil {
		return "\n[Host product facts]\nProduct facts are currently unavailable; do not invent missing facts.\n"
	}
	return "\n[Host product facts]\n" +
		"These are versioned facts about the HexClaw host, not a replacement for this agent's name or persona. " +
		"Answer standalone host identity and official-entry questions directly from these facts without web search or knowledge-base retrieval. Honor explicit requests to consult documents and retrieve information needed by other parts of a mixed question. Unknown official account details must remain unknown. " +
		"When the user asks about the HexClaw WeChat official account or how to follow it, or requests a full/detailed introduction of this assistant and its host product, show the confirmed account's qr_code exactly once as an inline Markdown image, not as a normal link. " +
		"Below the image, use exactly two caption lines: id_label and id, then follow_message, using the supplied values exactly. Do not add a separate account-name line above the ID or repeat the name in this caption. Use the label 公众号 ID rather than 微信号 for this identifier. " +
		"Keep the website and GitHub as ordinary links. Do not output a decoded QR destination or a follow/login-page hyperlink, and do not promise automatic following or cross-app conversation continuity. " +
		"Do not append the QR code to brief identity answers, one-sentence introductions, unrelated tasks or introductions of third parties. Respect explicit requests for no images or no promotion. The brand caption does not replace the current agent's identity. " +
		"Configured endpoints do not prove a direct connection or a complete forwarding chain. " +
		"Product capabilities do not imply that all tools are enabled for this request.\n" + facts + "\n"
}
