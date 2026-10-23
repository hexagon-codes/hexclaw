package session

import (
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/storage"
)

// 历史只提供已保存的媒体身份用于续接，不重新注入文件字节或上游链接。
func historicalAssistantMediaReferences(record *storage.MessageRecord) string {
	if record.Role != "assistant" {
		return record.Content
	}
	var metadata struct {
		CompletionDigest string `json:"media_completion_digest"`
		Operations       string `json:"media_operations"`
		Assets           []struct {
			ID          string `json:"asset_id"`
			OperationID string `json:"operation_id"`
			Kind        string `json:"kind"`
			Role        string `json:"role"`
		} `json:"media_assets"`
	}
	if json.Unmarshal([]byte(record.Metadata), &metadata) != nil || metadata.CompletionDigest == "" {
		return record.Content
	}
	var operations []string
	_ = json.Unmarshal([]byte(metadata.Operations), &operations)
	if len(metadata.Assets) == 0 && len(operations) == 0 {
		return record.Content
	}
	references, err := json.Marshal(struct {
		Operations []string `json:"operation_ids,omitempty"`
		Assets     any      `json:"assets,omitempty"`
	}{Operations: operations, Assets: metadata.Assets})
	if err != nil {
		return record.Content
	}
	return record.Content + "\n\n[Persisted media references] " + string(references)
}
