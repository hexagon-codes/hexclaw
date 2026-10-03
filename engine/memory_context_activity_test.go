package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/memory"
)

func TestMemoryActivityMatchesInjectedResidentAndRecalledEntries(t *testing.T) {
	t.Run("relevant resident is visible and injected once", func(t *testing.T) {
		fm := newFileMem(t, 200)
		const fact = "人生的意义是陪伴家人。"
		if err := fm.SaveStructuredEntry(fact, "fact", "manual", "", memory.EntryMeta{Pinned: true}); err != nil {
			t.Fatal(err)
		}
		mustSave(t, fm, "用户偏好数学讲解逐步解释为什么，不使用超纲方法", "preference")
		eng := engineWithFileMem(t, fm)
		ctx := withRetrievalHitsSink(context.Background())
		messages := eng.buildStreamMessages(ctx, "", nil, "", "人生的意义是什么？", map[string]string{"memory": "on"}, nil)
		activities := retrievalProcessSnapshot(ctx)
		if len(activities) != 1 || activities[0].Retrieval == nil {
			t.Fatalf("memory activity missing: %+v", activities)
		}
		activity := activities[0].Retrieval
		if len(activity.ResidentHits) != 2 || len(activity.MemoryHits) != 1 || activity.MemoryHits[0].Content != fact {
			t.Fatalf("expected only the related resident in retrieval details: %+v", activity)
		}
		if strings.Count(messages[len(messages)-1].Content, fact) != 1 {
			t.Fatal("related resident was not injected exactly once")
		}
		for _, entry := range fm.ParseEntries() {
			if entry.Content == fact && (entry.ID != activity.MemoryHits[0].ID || entry.HitCount != 1) {
				t.Fatalf("related resident identity or hit count changed: %+v", entry)
			}
			if entry.Type == "preference" && entry.HitCount != 0 {
				t.Fatal("unrelated resident counted as a retrieval hit")
			}
		}
	})
	fm := newFileMem(t, 200)
	mustSave(t, fm, "用户喜欢生活中的数学例子", "preference")
	mustSave(t, fm, "周末数学课程安排在周六上午", "fact")
	eng := engineWithFileMem(t, fm)
	ctx := withRetrievalHitsSink(context.Background())
	messages := eng.buildStreamMessages(ctx, "", nil, "", "周末数学课程安排", map[string]string{"memory": "on"}, nil)
	activities := retrievalProcessSnapshot(ctx)
	if len(activities) != 1 || activities[0].Retrieval == nil {
		t.Fatalf("memory activity missing: %+v", activities)
	}
	activity := activities[0].Retrieval
	if len(activity.ResidentHits) != 1 || len(activity.MemoryHits) != 1 {
		t.Fatalf("expected one resident and one recalled: %+v", activity)
	}
	turn := messages[len(messages)-1].Content
	if !strings.Contains(turn, activity.ResidentHits[0].Content) || !strings.Contains(turn, activity.MemoryHits[0].Content) {
		t.Fatal("activity differs from model input")
	}
	for _, entry := range fm.ParseEntries() {
		if entry.Type == "preference" && entry.HitCount != 0 {
			t.Fatal("resident activity inflated recall frequency")
		}
	}
	off := withRetrievalHitsSink(context.Background())
	eng.buildStreamMessages(off, "", nil, "", "周末数学课程安排", map[string]string{"memory": "off"}, nil)
	if len(retrievalProcessSnapshot(off)) != 0 {
		t.Fatal("memory=off reported injected memory")
	}
}

func TestKnowledgeActivityRetainsExactDocumentPageIdentity(t *testing.T) {
	ctx := withRetrievalHitsSink(context.Background())
	recordKnowledgeHits(ctx, []knowledge.SearchHit{{DocID: "doc-a", DocumentGeneration: 3, SourceDigest: "source-a", PageStart: 11, PageEnd: 12, Content: "原页内容"}})
	hits, _ := retrievalHitsSnapshot(ctx)
	if len(hits) != 1 || hits[0].DocID != "doc-a" || hits[0].DocumentGeneration != 3 || hits[0].SourceDigest != "source-a" || hits[0].PageStart != 11 || hits[0].PageEnd != 12 {
		t.Fatalf("citation identity lost: %+v", hits)
	}
}
