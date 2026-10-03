package egress

import (
	"net/http"
	"time"
)

// NewConfiguredOllamaClient 只用于已保存的 Ollama 地址；管理与推理共用相同传输。
// 重定向不继承此配置，其他 Provider、附件和下载器保持原有合同。
func NewConfiguredOllamaClient(headerTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = headerTimeout
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
