package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/hexagon-codes/ai-core/llm"
	hexmcp "github.com/hexagon-codes/hexagon/mcp"
	"github.com/hexagon-codes/hexclaw/adapter"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func binaryAttachment(content hexmcp.BinaryContent) adapter.Attachment {
	descriptor := content.Descriptor()
	return adapter.Attachment{Type: "file", Name: descriptor.Name, Mime: descriptor.MIMEType, Size: descriptor.Size, ContentDigest: descriptor.Digest,
		Source: &adapter.AttachmentSource{OpenReader: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(content.Data)), nil
		}},
	}
}

// toolContentResult 仅装配应用附件来源；协议解析与模型观察由 Hexagon 负责。
type toolContentResult struct{ *hexmcp.PreservedToolResult }

type promptBinaryDescriptor struct {
	hexmcp.BinaryDescriptor
	MessageIndex int `json:"message_index"`
}

func (r *toolContentResult) ToolAttachments() []adapter.Attachment {
	var attachments []adapter.Attachment
	for _, content := range r.ToolBinaryContents() {
		// 普通工具图片继续作为过程观察，音频及显式资源文件进入用户交付链。
		if content.Type != "image" {
			attachments = append(attachments, binaryAttachment(content))
		}
	}
	return attachments
}

func (r *ServerResourceResult) projections() ([]hexmcp.ContentProjection, error) {
	if r == nil || r.ReadResourceResult == nil {
		return nil, nil
	}
	projections := make([]hexmcp.ContentProjection, len(r.Contents))
	for index, content := range r.Contents {
		if content == nil {
			continue
		}
		projection, err := hexmcp.ProjectResource(content, fmt.Sprintf("resource-%d", index+1))
		if err != nil {
			return nil, err
		}
		projections[index] = projection
	}
	return projections, nil
}

// ToolBinaryContents 只透传已返回的资源字节，不据 URI 授权或抓取。
func (r *ServerResourceResult) ToolBinaryContents() []hexmcp.BinaryContent {
	projections, err := r.projections()
	if err != nil {
		return nil
	}
	var binaries []hexmcp.BinaryContent
	for _, projection := range projections {
		binaries = append(binaries, projection.Binaries...)
	}
	return binaries
}

// ToolAttachments 将资源原字节交给已有附件归属与持久化入口，空文件也保留。
func (r *ServerResourceResult) ToolAttachments() []adapter.Attachment {
	var files []adapter.Attachment
	for _, content := range r.ToolBinaryContents() {
		files = append(files, binaryAttachment(content))
	}
	return files
}

// ToolMessageContent 只观察实际图片，其它内容保持为文件引用。
func (r *ServerResourceResult) ToolMessageContent() []llm.ContentPart {
	return binaryImageParts(r.ToolBinaryContents())
}

// ToolText 将资源所属服务与共享协议投影组装为模型文本。
func (r *ServerResourceResult) ToolText() string {
	if r == nil || r.ReadResourceResult == nil {
		return "null"
	}
	projections, err := r.projections()
	if err != nil {
		return fmt.Sprintf("MCP resource projection failed: %v", err)
	}
	result := *r.ReadResourceResult
	result.Contents = make([]*sdkmcp.ResourceContents, len(r.Contents))
	var descriptors []hexmcp.BinaryDescriptor
	for index, projection := range projections {
		if len(projection.Content) == 0 {
			continue
		}
		var content sdkmcp.ResourceContents
		if err := json.Unmarshal(projection.Content, &content); err != nil {
			return fmt.Sprintf("MCP resource projection failed: %v", err)
		}
		result.Contents[index] = &content
		descriptors = append(descriptors, projection.BinaryFiles()...)
	}
	if len(descriptors) == 0 {
		type resourceJSON ServerResourceResult
		return projectionText((*resourceJSON)(r))
	}
	return projectionText(struct {
		Server string `json:"server"`
		*sdkmcp.ReadResourceResult
		BinaryFiles []hexmcp.BinaryDescriptor `json:"binary_files"`
	}{Server: r.Server, ReadResourceResult: &result, BinaryFiles: descriptors})
}

// MarshalJSON 与模型文本使用同一去编码投影，原始字节继续由附件来源持有。
func (r *ServerResourceResult) MarshalJSON() ([]byte, error) {
	return projectedJSON(r.ToolText())
}

// promptProjection 只转换应用兼容 DTO 的字段形状，内容解析复用同一协议投影。
func promptProjection(content PromptContent, index int) (hexmcp.ContentProjection, error) {
	raw := content.Raw
	if len(raw) == 0 {
		encoded, err := json.Marshal(struct {
			Type     string                   `json:"type"`
			Text     string                   `json:"text,omitempty"`
			URI      string                   `json:"uri,omitempty"`
			MIMEType string                   `json:"mimeType,omitempty"`
			Data     []byte                   `json:"data,omitempty"`
			Resource *sdkmcp.ResourceContents `json:"resource,omitempty"`
		}{Type: content.Type, Text: content.Text, URI: content.ResourceURI, MIMEType: content.MimeType, Data: content.Data, Resource: content.Resource})
		if err != nil {
			return hexmcp.ContentProjection{}, err
		}
		raw = encoded
	}
	return hexmcp.ProjectContentJSON(raw, fmt.Sprintf("prompt-message-%d", index+1))
}

func (r *GetPromptResult) projections() ([]hexmcp.ContentProjection, error) {
	if r == nil {
		return nil, nil
	}
	projections := make([]hexmcp.ContentProjection, len(r.Messages))
	for index, message := range r.Messages {
		projection, err := promptProjection(message.Content, index)
		if err != nil {
			return nil, err
		}
		projections[index] = projection
	}
	return projections, nil
}

// ToolBinaryContents 不执行模板指令，只提供协议实际返回的媒体和嵌入资源。
func (r *GetPromptResult) ToolBinaryContents() []hexmcp.BinaryContent {
	projections, err := r.projections()
	if err != nil {
		return nil
	}
	var binaries []hexmcp.BinaryContent
	for _, projection := range projections {
		binaries = append(binaries, projection.Binaries...)
	}
	return binaries
}

// ToolAttachments 将模板实际字节装配为应用附件来源，不执行模板指令。
func (r *GetPromptResult) ToolAttachments() []adapter.Attachment {
	var files []adapter.Attachment
	for _, content := range r.ToolBinaryContents() {
		files = append(files, binaryAttachment(content))
	}
	return files
}

// ToolMessageContent 复用共享图片投影，模板角色仍是工具数据。
func (r *GetPromptResult) ToolMessageContent() []llm.ContentPart {
	return binaryImageParts(r.ToolBinaryContents())
}

// ToolText 保留模板消息位置与服务归属，不重复输出媒体编码。
func (r *GetPromptResult) ToolText() string {
	if r == nil {
		return "null"
	}
	projections, err := r.projections()
	if err != nil {
		return fmt.Sprintf("MCP prompt projection failed: %v", err)
	}
	result := *r
	result.Messages = append([]PromptMessage(nil), r.Messages...)
	var descriptors []promptBinaryDescriptor
	for index, projection := range projections {
		if len(projection.Binaries) == 0 {
			continue
		}
		var content PromptContent
		if err := json.Unmarshal(projection.Content, &content); err != nil {
			return fmt.Sprintf("MCP prompt projection failed: %v", err)
		}
		content.ResourceURI = r.Messages[index].Content.ResourceURI
		content.Raw = projection.Content
		result.Messages[index].Content = content
		for _, descriptor := range projection.BinaryFiles() {
			descriptors = append(descriptors, promptBinaryDescriptor{BinaryDescriptor: descriptor, MessageIndex: index})
		}
	}
	if len(descriptors) == 0 {
		type promptJSON GetPromptResult
		return projectionText((*promptJSON)(r))
	}
	return projectionText(struct {
		Server      string                   `json:"server"`
		Description string                   `json:"description,omitempty"`
		Messages    []PromptMessage          `json:"messages"`
		BinaryFiles []promptBinaryDescriptor `json:"binary_files"`
	}{Server: result.Server, Description: result.Description, Messages: result.Messages, BinaryFiles: descriptors})
}

// MarshalJSON 不把模板内媒体编码重复送入文本或公开结果。
func (r *GetPromptResult) MarshalJSON() ([]byte, error) {
	return projectedJSON(r.ToolText())
}

func binaryImageParts(contents []hexmcp.BinaryContent) []llm.ContentPart {
	var parts []llm.ContentPart
	for _, content := range contents {
		if part, ok := content.ImagePart(); ok {
			parts = append(parts, part)
		}
	}
	return parts
}

func projectionText(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("MCP content projection failed: %v", err)
	}
	return string(data)
}

func projectedJSON(text string) ([]byte, error) {
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("%s", text)
	}
	return []byte(text), nil
}
