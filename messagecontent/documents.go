package messagecontent

import (
	"mime"
	"strings"
)

// DocumentExtension 返回应用既有文档格式的标准扩展名，媒体参数不改变文件种类。
func DocumentExtension(mediaType string) string {
	base, _, err := mime.ParseMediaType(strings.TrimSpace(mediaType))
	if err != nil {
		return ""
	}
	switch strings.ToLower(base) {
	case "application/pdf":
		return "pdf"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx"
	case "application/vnd.oasis.opendocument.text":
		return "odt"
	case "application/epub+zip":
		return "epub"
	case "application/rtf", "text/rtf":
		return "rtf"
	case "text/markdown":
		return "md"
	case "text/html":
		return "html"
	case "text/plain":
		return "txt"
	default:
		return ""
	}
}

// IsDocumentMIME 让消息组装与渠道传输共用应用已支持的文档种类。
func IsDocumentMIME(mediaType string) bool { return DocumentExtension(mediaType) != "" }
