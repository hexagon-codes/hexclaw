package storage

import (
	"crypto/sha256"
	"encoding/hex"
)

// IMRequestIDForEvent 将原始平台事件固定到物理实例；实例名称仅用于旧实例没有 ID 的情况。
func IMRequestIDForEvent(provider, instanceID, eventID string) string {
	digest := sha256.Sum256([]byte(provider + "\x00" + instanceID + "\x00" + eventID))
	return "imreq-" + hex.EncodeToString(digest[:])
}
