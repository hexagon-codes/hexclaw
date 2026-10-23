package engine

import (
	"context"
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/adapter"
)

// instanceCostMetadata 保留成本发生时的稳定账号身份，未知历史不按名称补猜。
func instanceCostMetadata(ctx context.Context) string {
	identity, ok := adapter.PlatformInstanceIdentityFromContext(ctx)
	if !ok {
		return ""
	}
	raw, _ := json.Marshal(identity)
	return string(raw)
}
