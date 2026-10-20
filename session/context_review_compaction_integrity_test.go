package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/storage"
)

func TestContextReviewCompactionPreservesHistoryOnFailedPreparation(t *testing.T) {
	tests := []struct {
		name           string
		summary        string
		archiveFailure bool
	}{
		{
			name:    "empty_summary_response",
			summary: "",
		},
		{
			name:           "archive_failure_with_valid_summary",
			summary:        "The student asked about exercise 3 and received an explanation.",
			archiveFailure: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, store := newTestManager(t)
			const sessionID = "compaction-integrity"
			if err := store.CreateSession(ctx, &storage.Session{
				ID:       sessionID,
				UserID:   "test-parent",
				Platform: "web",
			}); err != nil {
				t.Fatalf("create session: %v", err)
			}

			base := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
			originals := []*storage.MessageRecord{
				{
					ID:        "old-question",
					SessionID: sessionID,
					Role:      "user",
					Content:   "Please check exercise 3 on this worksheet.",
					Metadata:  `{"attachments":[{"type":"image","url":"asset://test-worksheet-page-1"}]}`,
					CreatedAt: base,
				},
				{
					ID:        "old-answer",
					SessionID: sessionID,
					Role:      "assistant",
					Content:   "Exercise 3 uses the diagram and the shared condition on page 1.",
					CreatedAt: base.Add(time.Second),
				},
				{
					ID:        "recent-question",
					SessionID: sessionID,
					Role:      "user",
					Content:   "Why is the second step necessary?",
					CreatedAt: base.Add(2 * time.Second),
				},
				{
					ID:        "recent-answer",
					SessionID: sessionID,
					Role:      "assistant",
					Content:   "The second step applies the shared condition.",
					CreatedAt: base.Add(3 * time.Second),
				},
			}
			for _, msg := range originals {
				if err := store.SaveMessage(ctx, msg); err != nil {
					t.Fatalf("save original message %q: %v", msg.ID, err)
				}
			}
			before, err := store.ListMessages(ctx, sessionID, len(originals)+1, 0)
			if err != nil {
				t.Fatalf("read original history: %v", err)
			}
			if len(before) != len(originals) {
				t.Fatalf("fixture has %d messages, want %d", len(before), len(originals))
			}

			archiveDir := t.TempDir()
			if tt.archiveFailure {
				// 用普通文件占据归档目录位置，稳定触发失败，不依赖文件权限或宿主用户。
				archiveDir = filepath.Join(archiveDir, "not-a-directory")
				if err := os.WriteFile(archiveDir, []byte("archive path blocker"), 0600); err != nil {
					t.Fatalf("create archive failure fixture: %v", err)
				}
			}
			// 显式指定临时归档器，所有存储副作用都留在测试目录内。
			compactor := &Compactor{
				store:    store,
				config:   CompactionConfig{MaxMessages: 3, KeepRecent: 2},
				archiver: NewArchiver(archiveDir),
			}
			compacted, compactErr := compactor.Compact(ctx, sessionID, &titleSuggestProvider{
				response: tt.summary,
			})
			if compacted != 0 {
				t.Errorf("failed preparation must not delete history: compacted=%d, error=%v", compacted, compactErr)
			}

			// 摘要无有效内容或原文归档失败时，逐条独立回读原消息及附件元数据。
			for _, want := range before {
				got, err := store.GetMessage(ctx, want.ID)
				if err != nil {
					t.Errorf("original message %q must remain readable after failed preparation: %v", want.ID, err)
					continue
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("original message %q changed after failed preparation:\ngot: %#v\nwant: %#v", want.ID, got, want)
				}
			}
			after, err := store.ListMessages(ctx, sessionID, len(originals)+1, 0)
			if err != nil {
				t.Fatalf("read history after compaction: %v", err)
			}
			for _, msg := range after {
				if strings.Contains(msg.Content, "摘要生成失败") {
					t.Errorf("failed summary placeholder must not be persisted: message=%q content=%q", msg.ID, msg.Content)
				}
			}
		})
	}
}
