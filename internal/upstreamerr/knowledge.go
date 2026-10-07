package upstreamerr

import (
	"errors"
	"regexp"
	"strings"
)

var (
	knowledgeRequestPreview = regexp.MustCompile(`(?i)\brequest[ _-]?preview\s*[:=]`)
	knowledgeURL            = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	knowledgeSensitiveField = regexp.MustCompile(`(?i)(\b(?:authorization|api[ _-]?key|token|access_token|refresh_token|secret|client_secret|password)\b["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|(?:bearer|basic)\s+[^\s,;]+|[^\s,;]+)`)
	knowledgeBearer         = regexp.MustCompile(`(?i)\bbearer\s+[^\s,"';]+`)
	knowledgeSecretKey      = regexp.MustCompile(`\bsk-[a-zA-Z0-9_-]+`)
)

// KnowledgeFailureMessage 投影知识库失败原因；保留空值和诊断信息，不返回请求正文或已标识的凭据。
func KnowledgeFailureMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if location := knowledgeRequestPreview.FindStringIndex(message); location != nil {
		message = strings.TrimRight(strings.TrimSpace(message[:location[0]]), ",; ")
	}
	message = PublicMessage(errors.New(message), "")
	message = knowledgeURL.ReplaceAllStringFunc(message, func(value string) string {
		// URL 仅保留协议、目标与路径，查询参数名并不能可靠判断是否携带凭据。
		authorityStart := strings.Index(value, "://") + 3
		authorityEnd := len(value)
		if end := strings.IndexAny(value[authorityStart:], "/?#"); end >= 0 {
			authorityEnd = authorityStart + end
		}
		if at := strings.LastIndex(value[authorityStart:authorityEnd], "@"); at >= 0 {
			value = value[:authorityStart] + value[authorityStart+at+1:]
		}
		if end := strings.IndexAny(value, "?#"); end >= 0 {
			value = value[:end]
		}
		return value
	})
	message = knowledgeSensitiveField.ReplaceAllString(message, "${1}[redacted]")
	message = knowledgeBearer.ReplaceAllString(message, "Bearer [redacted]")
	message = knowledgeSecretKey.ReplaceAllString(message, "[redacted]")
	return strings.TrimSpace(message)
}
