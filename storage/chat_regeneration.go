package storage

import (
	"context"
	"database/sql"
	"errors"
)

var ErrChatRegenerationConflict = errors.New("original conversation changed; read its current history")

// ChatRegenerationSnapshot 冻结原用户、所属回复和模型可见的会话前缀。
type ChatRegenerationSnapshot struct {
	Source   *MessageRecord
	Previous *MessageRecord
	Prefix   []*MessageRecord
	// Replace 在新助手插入前锁定原会话、核对冻结段并移除旧尾段。
	Replace func(context.Context, *sql.Tx, *MessageRecord) error
	// Invalidate 在实际提交后清理受旧尾段影响的读取缓存。
	Invalidate func()
}

// ChatRegenerationReader 读取并冻结可在现有消息事务内替换的原轮。
type ChatRegenerationReader interface {
	FreezeChatRegeneration(ctx context.Context, owner, sessionID, sourceID string, prefixLimit int) (*ChatRegenerationSnapshot, error)
}

// SupersededChatRequestReader 从被替换的旧轮次读取原请求所属的会话。
type SupersededChatRequestReader interface {
	FindSupersededChatRequest(ctx context.Context, owner, requestID string) (sessionID string, err error)
}
