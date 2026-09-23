package engineadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hexagon-codes/hexclaw/memory"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/storage/migrate"
	_ "modernc.org/sqlite"
)

// 真实文件写入后尚无 SQLite 消费标记，重新装配消费者必须复用同一条记忆。
func TestInsightsReplay_SQLiteAndFileMemory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "events.db")
	openDB := func() *sql.DB {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := openDB()
	if err := migrate.Run(ctx, db, migrate.All); err != nil {
		t.Fatal(err)
	}
	newMemory := func() *memory.FileMemory {
		fm, err := memory.New(memory.Options{Dir: filepath.Join(dir, "memory")})
		if err != nil {
			t.Fatal(err)
		}
		return fm
	}
	fm := newMemory()
	payload, err := json.Marshal(k12storage.MistakeRecordedPayload{
		RecordID: "problem-1", AgentName: "learner-a", KnowledgePoint: "小数乘法", ErrorCause: "计算失误",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := k12storage.OutboxEvent{EventID: "attempt-1", AgentName: "learner-a", AggregateID: "problem-1",
		EventType: k12storage.EventMistakeRecorded, PayloadVersion: 1, Payload: string(payload)}
	if _, err := db.Exec(`INSERT INTO outbox_events
		(event_id, agent_name, aggregate_id, event_type, payload_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, 1)`, event.EventID, event.AgentName, event.AggregateID, event.EventType, event.Payload); err != nil {
		t.Fatal(err)
	}
	consumer := usecase.InsightsConsumer{Insights: NewInsightsAdapter(fm)}
	if err := consumer.Handle(ctx, event); err != nil {
		t.Fatal(err)
	}
	first := fm.ParseEntriesForRole("learner-a")
	if len(first) != 1 {
		t.Fatalf("first delivery: want one entry, got %d", len(first))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openDB()
	fm = newMemory()
	consumer = usecase.InsightsConsumer{Insights: NewInsightsAdapter(fm)}
	dispatcher := k12storage.NewDispatcher(k12storage.NewStore(db, nil), consumer)
	if err := dispatcher.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	got := fm.ParseEntriesForRole("learner-a")
	if len(got) != 1 || got[0].ID != first[0].ID {
		t.Fatalf("replay created or replaced memory: %+v", got)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM outbox_events WHERE event_id='attempt-1'`).Scan(&status); err != nil || status != k12storage.OutboxDelivered {
		t.Fatalf("event not delivered: %q, %v", status, err)
	}
	var receipts int
	if err := db.QueryRow(`SELECT count(*) FROM outbox_consumptions WHERE event_id='attempt-1'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("consumption receipt count=%d: %v", receipts, err)
	}
	// 同题再次作答是新事件，不能按薄弱点正文去重。
	event.EventID = "attempt-2"
	if err := consumer.Handle(ctx, event); err != nil {
		t.Fatal(err)
	}
	got = fm.ParseEntriesForRole("learner-a")
	if len(got) != 2 || got[0].ID == got[1].ID {
		t.Fatalf("independent attempts must stay distinct: %+v", got)
	}
	if got := fm.ParseEntriesForRole("learner-b"); len(got) != 0 {
		t.Fatalf("memory leaked to another role: %+v", got)
	}
}

func TestInsightsReplay_PreservesMemoryChanges(t *testing.T) {
	for _, action := range []string{"edit", "archive", "delete", "clear"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			fm, err := memory.New(memory.Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			write := func(fm *memory.FileMemory) error {
				return NewInsightsAdapter(fm).WriteWeakness(context.Background(), "attempt-1", "learner-a", "小数乘法", "原始薄弱点")
			}
			if err := write(fm); err != nil {
				t.Fatal(err)
			}
			id := fm.ParseEntriesForRole("learner-a")[0].ID
			switch action {
			case "edit":
				err = fm.UpdateEntry(id, "家长修正后的内容")
			case "archive":
				err = fm.ArchiveEntry(id)
			case "delete":
				err = fm.DeleteEntry(id)
			case "clear":
				err = fm.ClearAll()
			}
			if err != nil {
				t.Fatal(err)
			}
			fm, err = memory.New(memory.Options{Dir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if err := write(fm); err != nil {
				t.Fatal(err)
			}
			entries := fm.ParseEntriesForRole("learner-a")
			if action == "edit" {
				if len(entries) != 1 || entries[0].ID != id || entries[0].Content != "家长修正后的内容" {
					t.Fatalf("replay overwrote edit: %+v", entries)
				}
			} else if len(entries) != 0 {
				t.Fatalf("replay restored removed memory: %+v", entries)
			}
			capacity := fm.CapacityForRole("learner-a")
			if action == "archive" && capacity.Archived != 1 {
				t.Fatalf("replay changed archived memory: %+v", capacity)
			}
		})
	}
}
