package mediaflow

import (
	"context"
	"sync"
	"time"
)

type ReplyRecoveryRepository interface {
	UnfinishedReplies(context.Context, int) ([]*Operation, error)
}
type ReplyRecoveryPageRepository interface {
	UnfinishedRepliesAfter(context.Context, string, int) ([]*Operation, error)
}
type ReplyResumer func(context.Context, *Operation) error
type replyRecovery struct {
	mu     sync.RWMutex
	resume ReplyResumer
}

func (s *Service) SetReplyResumer(resume ReplyResumer) {
	s.replies.mu.Lock()
	s.replies.resume = resume
	s.replies.mu.Unlock()
	s.signal()
}

func (s *Service) recoverReplies() {
	s.replies.mu.RLock()
	resume := s.replies.resume
	s.replies.mu.RUnlock()
	if resume == nil {
		return
	}
	repo, ok := s.repo.(ReplyRecoveryRepository)
	if !ok {
		return
	}
	afterID := ""
	for {
		var ops []*Operation
		var err error
		pages, paged := s.repo.(ReplyRecoveryPageRepository)
		if paged {
			ops, err = pages.UnfinishedRepliesAfter(s.ctx, afterID, 16)
		} else {
			ops, err = repo.UnfinishedReplies(s.ctx, 16)
		}
		if err != nil || len(ops) == 0 {
			return
		}
		for _, op := range ops {
			if s.ctx.Err() != nil {
				return
			}
			_ = resume(s.ctx, op)
			afterID = op.ID
		}
		if !paged || len(ops) < 16 {
			return
		}
	}
}

// 回复恢复可能等待会话锁，不能占用仍需完成媒体生成的推进器。
func (s *Service) runReplyRecovery() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		s.recoverReplies()
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
