package render

// PDFBranding 保存各场景共用的产品事实，不包含题目、助手身份或投递行为。
type PDFBranding struct {
	Name         string `json:"name"`
	Tagline      string `json:"tagline"`
	WebsiteURL   string `json:"website_url"`
	WebsiteLabel string `json:"website_label"`
	WeChatID     string `json:"wechat_id"`
	GitHubURL    string `json:"github_url"`
	GitHubLabel  string `json:"github_label"`
}

// DefaultPDFBranding 返回值副本，场景无法改变后续文档的品牌资料。
func DefaultPDFBranding() PDFBranding {
	return PDFBranding{
		Name: "HexClaw 河蟹 AI", Tagline: "本地云端均可部署 · 数据自主掌控 · 软件开源免费",
		WebsiteURL: "https://hexclaw.net/", WebsiteLabel: "hexclaw.net", WeChatID: "hexclaw",
		GitHubURL: "https://github.com/hexagon-codes/hexclaw-desktop", GitHubLabel: "github.com/hexagon-codes/hexclaw-desktop",
	}
}
