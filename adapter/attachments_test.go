package adapter

import "testing"

func TestHasMessageInput(t *testing.T) {
	if HasMessageInput("", nil) {
		t.Fatal("空文本且无附件时不应视为有效输入")
	}
	if !HasMessageInput("  ", []Attachment{{Type: "image", URL: "https://example.com/a.png"}}) {
		t.Fatal("仅附件输入应视为有效输入")
	}
	if !HasMessageInput("你好", nil) {
		t.Fatal("纯文本输入应视为有效输入")
	}
}

func TestValidateAttachments(t *testing.T) {
	tests := []struct {
		name        string
		attachments []Attachment
		wantErr     bool
	}{
		{
			name:        "图片URL合法",
			attachments: []Attachment{{Type: "image", Mime: "image/png", URL: "https://example.com/a.png"}},
		},
		{
			name:        "图片Data合法",
			attachments: []Attachment{{Mime: "image/jpeg", Data: "abc123"}},
		},
		{
			name:        "缺少载荷",
			attachments: []Attachment{{Type: "image", Mime: "image/png"}},
			wantErr:     true,
		},
		{
			name:        "同时提供URL和Data",
			attachments: []Attachment{{Type: "image", Mime: "image/png", URL: "https://example.com/a.png", Data: "abc123"}},
			wantErr:     true,
		},
		{
			name:        "不支持PDF",
			attachments: []Attachment{{Type: "file", Mime: "application/pdf", URL: "https://example.com/a.pdf"}},
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAttachments(tt.attachments)
			if tt.wantErr && err == nil {
				t.Fatal("期望返回错误，实际为 nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("期望无错误，实际为 %v", err)
			}
		})
	}
}

func TestBuildUserMessage(t *testing.T) {
	msg := BuildUserMessage("描述图片", []Attachment{
		{Type: "image", Mime: "image/png", Data: "abc123"},
	})
	if !msg.HasMultiContent() {
		t.Fatal("图片消息应构建为 MultiContent")
	}
	if len(msg.MultiContent) != 2 {
		t.Fatalf("期望 2 个 ContentPart，实际为 %d", len(msg.MultiContent))
	}

	plain := BuildUserMessage("纯文本", nil)
	if plain.HasMultiContent() {
		t.Fatal("纯文本消息不应包含 MultiContent")
	}
	if plain.Content != "纯文本" {
		t.Fatalf("纯文本内容不匹配: %q", plain.Content)
	}
}

func TestAttachmentCacheKeyIncludesAttachments(t *testing.T) {
	keyA := AttachmentCacheKey("这是什么", []Attachment{{Type: "image", Mime: "image/png", Data: "image-a"}})
	keyB := AttachmentCacheKey("这是什么", []Attachment{{Type: "image", Mime: "image/png", Data: "image-b"}})
	keyARepeat := AttachmentCacheKey("这是什么", []Attachment{{Type: "image", Mime: "image/png", Data: "image-a"}})

	if keyA == keyB {
		t.Fatal("不同图片不应生成相同缓存键")
	}
	if keyA != keyARepeat {
		t.Fatal("相同输入应生成稳定缓存键")
	}
}
