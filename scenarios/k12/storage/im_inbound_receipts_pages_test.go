package k12storage_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestInboundPhotoPagesAtomicReplayAndOrdering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pages.sqlite")
	db, store := newInboundPhotoStore(t, path)
	ctx := t.Context()
	seedInboundAgent(t, db, "mingming")
	pages := []k12storage.InboundPhotoAdmission{
		inboundAdmission("msg-group", []byte("first")),
		inboundAdmission("msg-group", []byte("second")),
		inboundAdmission("msg-group", []byte("third")),
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_last_page BEFORE INSERT ON k12_im_inbound_dispatches
		WHEN (SELECT page_index FROM k12_im_inbound_pages WHERE receipt_id=NEW.receipt_id)=2
		BEGIN SELECT RAISE(ABORT,'simulated crash at last dispatch'); END`); err != nil {
		t.Fatal(err)
	}
	if partial, created, err := store.AdmitInboundPhotoPages(ctx, pages); err == nil || partial != nil || created {
		t.Fatalf("interrupted batch: pages=%d created=%v err=%v", len(partial), created, err)
	}
	for _, table := range []string{"k12_im_inbound_receipts", "k12_im_inbound_pages", "k12_im_inbound_assets", "k12_im_inbound_dispatches"} {
		if n := tableCount(t, db, table); n != 0 {
			t.Fatalf("partial %s rows after rollback: %d", table, n)
		}
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_last_page"); err != nil {
		t.Fatal(err)
	}
	first, created, err := store.AdmitInboundPhotoPages(ctx, pages)
	if err != nil || !created || len(first) != len(pages) {
		t.Fatalf("batch: created=%v count=%d err=%v", created, len(first), err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, store = openInboundPhotoStore(t, path)
	defer db.Close()
	replayed, created, err := store.AdmitInboundPhotoPages(ctx, pages)
	if err != nil || created || len(replayed) != len(pages) {
		t.Fatalf("replay: created=%v count=%d err=%v", created, len(replayed), err)
	}
	for i, input := range pages {
		identity := input.Identity
		identity.PageIndex = i
		got, readErr := store.GetInboundPhotoByIdentity(ctx, identity)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got.Receipt.Identity != identity || got.Receipt.PageCount != len(pages) || !bytes.Equal(got.Asset.Bytes, input.AssetBytes) ||
			got.Receipt != first[i].Receipt || got.Dispatch != replayed[i].Dispatch || got.Asset.AssetID != first[i].Asset.AssetID {
			t.Fatalf("restored page %d changed its identity, content or dispatch", i)
		}
	}
	for _, group := range [][]k12storage.InboundPhotoAdmission{
		pages[:1], pages[:2], {pages[0], pages[2], pages[1]}, {pages[0], pages[1], pages[2], pages[0]},
	} {
		if rows, newly, conflictErr := store.AdmitInboundPhotoPages(ctx, group); !errors.Is(conflictErr, k12storage.ErrInboundPhotoConflict) || newly || rows != nil {
			t.Fatalf("changed replay accepted: count=%d result=%d created=%v err=%v", len(group), len(rows), newly, conflictErr)
		}
	}
	if _, _, err = store.AdmitInboundPhoto(ctx, pages[0]); !errors.Is(err, k12storage.ErrInboundPhotoConflict) {
		t.Fatalf("single-page replay of a multi-page message: %v", err)
	}
	legacy := inboundAdmission("original-single", []byte("first"))
	if _, created, err = store.AdmitInboundPhoto(ctx, legacy); err != nil || !created {
		t.Fatalf("single page: created=%v err=%v", created, err)
	}
	if _, _, err = store.AdmitInboundPhotoPages(ctx, []k12storage.InboundPhotoAdmission{legacy, legacy}); !errors.Is(err, k12storage.ErrInboundPhotoConflict) {
		t.Fatalf("multi-page replay of a single message: %v", err)
	}
	if got := tableCount(t, db, "k12_im_inbound_pages"); got != 4 {
		t.Fatalf("receipts after conflicting replays: %d", got)
	}
	if got := tableCount(t, db, "k12_im_inbound_receipts"); got != 2 {
		t.Fatalf("physical messages after conflicting replays: %d", got)
	}
	// 原单图程序按根 ID 关联原图和调度。它仅能看到真正的单图，不能接续任一多图子页。
	var visibleAsSingle int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM k12_im_inbound_receipts r JOIN k12_im_inbound_assets a USING(receipt_id)
		JOIN k12_im_inbound_dispatches d USING(receipt_id)`).Scan(&visibleAsSingle); err != nil || visibleAsSingle != 1 {
		t.Fatalf("legacy single-photo recovery sees %d receipts, err=%v", visibleAsSingle, err)
	}

	third := first[2].Receipt.Identity
	before, err := store.PendingEarlierInboundPhotoPages(ctx, third)
	if err != nil || len(before) != 2 || before[0].ReceiptID != first[0].Receipt.ReceiptID || before[1].ReceiptID != first[1].Receipt.ReceiptID {
		t.Fatalf("ordered earlier pages: %+v, %v", before, err)
	}
	// 使用真实派发状态转换结束第一页，第二页仍须排在第三页之前。
	open := first[0].Dispatch
	next := open.State()
	next.ProcessingStatus = k12storage.InboundPhotoImageTaskSubmitted
	next.RoutingDecision = k12storage.InboundPhotoRouteNewSubmission
	next.ImageTaskID = "task-first"
	open, err = store.CompareAndSwapInboundPhotoDispatch(ctx, "mingming", open.ReceiptID, open.Version, next)
	if err != nil {
		t.Fatal(err)
	}
	next = open.State()
	next.TerminalStatus = k12storage.InboundPhotoTerminalFailed
	next.TerminalStage = k12storage.InboundPhotoTerminalStageImageTask
	next.FailureKind = "fixture_failure"
	if _, err = store.CompareAndSwapInboundPhotoDispatch(ctx, "mingming", open.ReceiptID, open.Version, next); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.PendingEarlierInboundPhotoPages(ctx, third)
	if err != nil || len(remaining) != 1 || remaining[0].ReceiptID != first[1].Receipt.ReceiptID {
		t.Fatalf("remaining earlier pages: %+v, %v", remaining, err)
	}
	outside := third
	outside.PageIndex++
	if _, err = store.GetInboundPhotoByIdentity(ctx, outside); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("unadmitted page: %v", err)
	}
}

func TestInboundPhotoPagesCompetingSingleAndGroupKeepOneShape(t *testing.T) {
	db, store := newInboundPhotoStore(t, filepath.Join(t.TempDir(), "inbound-shape.sqlite"))
	defer db.Close()
	seedInboundAgent(t, db, "mingming")
	db.SetMaxOpenConns(2)
	first := inboundAdmission("one-provider-message", []byte("A"))
	second := inboundAdmission("one-provider-message", []byte("B"))
	type outcome struct {
		pages   []k12storage.InboundPhotoBundle
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, inputs := range [][]k12storage.InboundPhotoAdmission{{first}, {first, second}} {
		go func() {
			<-start
			pages, created, err := store.AdmitInboundPhotoPages(t.Context(), inputs)
			results <- outcome{pages, created, err}
		}()
	}
	close(start)
	winner, conflicts := 0, 0
	for range 2 {
		out := <-results
		switch {
		case out.err == nil && out.created:
			winner = len(out.pages)
		case errors.Is(out.err, k12storage.ErrInboundPhotoConflict):
			conflicts++
		default:
			t.Fatalf("unexpected competing admission: %+v", out)
		}
	}
	if winner == 0 || conflicts != 1 || tableCount(t, db, "k12_im_inbound_dispatches") != winner {
		t.Fatalf("persisted shape=%d conflicts=%d", winner, conflicts)
	}
}
