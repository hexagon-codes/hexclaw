package instances

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hexagon-codes/hexclaw/storage/migrate"
)

func openPhotoTestStore(t *testing.T, path string) *PhotoIntakeStore {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
	if err = migrate.Run(t.Context(), db, []migrate.Migration{migrate.IMPhotoIntakesV152}); err != nil {
		t.Fatal(err)
	}
	return NewPhotoIntakeStore(db)
}

func photoTestInput(id string) PhotoIntakeInput {
	return PhotoIntakeInput{Key: PhotoIntakeKey{Platform: "dingtalk", InstanceID: "bot", ChatID: "family", ProviderMessageID: id}, SenderID: "parent", Content: "两页作业",
		Route: PhotoIntakeRoute{Kind: "k12", AgentName: "tutor", BindingID: "bound-family", Provider: "fixture", Model: "vision",
			DomainData: []byte(`{"schema_version":2,"source_session_id":"family-session","provider":"fixture","model":"vision"}`)},
		ReceivedAtMS: time.Now().UnixMilli(), PageSignatures: []string{fmt.Sprintf("%x", sha256.Sum256([]byte("first media handle"))), fmt.Sprintf("%x", sha256.Sum256([]byte("second media handle")))}}
}

func photoTestPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	im := image.NewGray(image.Rect(0, 0, 1, 1))
	im.SetGray(0, 0, color.Gray{Y: shade})
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func changePhotoTestState(t *testing.T, s *PhotoIntakeStore, k PhotoIntakeKey, lane, from string, state PhotoIntakeState, at int64) {
	t.Helper()
	ok, err := s.Transition(t.Context(), k, lane, from, state, at)
	if err != nil || !ok {
		t.Fatalf("%s %s to %s: claimed=%v err=%v", lane, from, state.Status, ok, err)
	}
}

func TestPhotoIntakePersistsOrderedPartialGroupAndReleasesOnlyAfterHandoff(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "intake.sqlite")
	s := openPhotoTestStore(t, path)
	in := photoTestInput("group")
	k := in.Key
	r, created, err := s.Register(ctx, in)
	if err != nil || !created || r.Notice.Status != "pending" || r.Work.Status != "pending" || len(r.Pages) != 2 {
		t.Fatalf("initial receipt: %+v created=%v err=%v", r, created, err)
	}
	now := time.Now().UnixMilli()
	a, b := photoTestPNG(t, 50), photoTestPNG(t, 180)
	mime := "image/png"
	if err = s.StartPage(ctx, k, 0, now); err != nil {
		t.Fatal(err)
	}
	if err = s.SavePage(ctx, k, 0, in.PageSignatures[0], "page.png", mime, a, now); err != nil {
		t.Fatal(err)
	}
	if err = s.StartPage(ctx, k, 1, now+1); err != nil {
		t.Fatal(err)
	}
	if err = s.FailPage(ctx, k, 1, "download_unavailable", now+2); err != nil {
		t.Fatal(err)
	}
	if ok, e := s.Transition(ctx, k, "work", "pending", PhotoIntakeState{Status: "running"}, now+2); ok || e != nil {
		t.Fatalf("partial group went to business: %v %v", ok, e)
	}
	if e := s.DropPages(ctx, k); !errors.Is(e, ErrPhotoIntakeConflict) {
		t.Fatalf("early content release: %v", e)
	}

	restarted := openPhotoTestStore(t, path)
	originalID := r.ID
	replay := in
	replay.Route = PhotoIntakeRoute{Kind: "ordinary", AgentName: "changed-after-registration"}
	r, created, err = restarted.Register(ctx, replay)
	if err != nil || created || r.ID != originalID || r.Route.AgentName != "tutor" || r.AssetsReadyAtMS != 0 || r.Pages[0].Data != nil ||
		r.Pages[1].FailureKind != "download_unavailable" || r.Pages[1].FailedAtMS == 0 {
		t.Fatalf("restart changed receipt: %+v %v", r, err)
	}
	p, err := restarted.GetPage(ctx, k, 0)
	if err != nil || !bytes.Equal(p.Data, a) {
		t.Fatalf("lost first page: %v", err)
	}
	wrong := in
	wrong.PageSignatures = []string{in.PageSignatures[1], in.PageSignatures[0]}
	if _, _, e := restarted.Register(ctx, wrong); !errors.Is(e, ErrPhotoIntakeConflict) {
		t.Fatalf("swapped incomplete group: %v", e)
	}
	wrong = in
	wrong.PageSignatures = wrong.PageSignatures[:1]
	if _, _, e := restarted.Register(ctx, wrong); !errors.Is(e, ErrPhotoIntakeConflict) {
		t.Fatalf("changed count: %v", e)
	}
	wrong = in
	wrong.SenderID = "different-parent"
	if _, _, e := restarted.Register(ctx, wrong); !errors.Is(e, ErrPhotoIntakeConflict) {
		t.Fatalf("changed sender: %v", e)
	}
	newSig := fmt.Sprintf("%x", sha256.Sum256([]byte("refreshed handle")))
	if e := restarted.SavePage(ctx, k, 0, newSig, "page.png", mime, b, now+3); !errors.Is(e, ErrPhotoIntakeConflict) {
		t.Fatalf("changed byte set: %v", e)
	}
	if e := restarted.SavePage(ctx, k, 0, newSig, "page.png", mime, a, now+3); e != nil {
		t.Fatal(e)
	}
	if e := restarted.SavePage(ctx, k, 1, in.PageSignatures[1], "page.png", mime, b, now+3); e != nil {
		t.Fatal(e)
	}
	r, err = restarted.Get(ctx, k)
	if err != nil || r.AssetsReadyAtMS <= 0 || r.Pages[0].StoredAtMS != p.StoredAtMS || r.Pages[0].DownloadedAtMS != now {
		t.Fatalf("bad ready state: %+v %v", r, err)
	}
	changePhotoTestState(t, restarted, k, "work", "pending", PhotoIntakeState{Status: "running"}, now+4)
	changePhotoTestState(t, restarted, k, "work", "running", PhotoIntakeState{Status: "completed", ResultRef: "k12-receipt"}, now+5)
	if err = restarted.DropPages(ctx, k); err != nil {
		t.Fatal(err)
	}
	for i, want := range [][]byte{a, b} {
		page, e := restarted.GetPage(ctx, k, i)
		if e != nil || page.Data != nil || page.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256(want)) {
			t.Fatalf("release lost identity at %d: %+v %v", i, page, e)
		}
	}
	if e := restarted.SavePage(ctx, k, 0, newSig, "page.png", mime, a, now+6); e != nil {
		t.Fatal(e)
	}
	if e := restarted.SavePage(ctx, k, 0, newSig, "page.png", mime, b, now+6); !errors.Is(e, ErrPhotoIntakeConflict) {
		t.Fatalf("released original accepted different bytes: %v", e)
	}
	page, e := restarted.GetPage(ctx, k, 0)
	if e != nil || page.Data != nil {
		t.Fatal("replay restaged released data", e)
	}
}

func TestPhotoIntakeOnePhysicalNoticeAndScopedRecovery(t *testing.T) {
	ctx := t.Context()
	s := openPhotoTestStore(t, filepath.Join(t.TempDir(), "claims.sqlite"))
	in := photoTestInput("same")
	in.PageSignatures = in.PageSignatures[:1]
	bad := in
	bad.Route.DomainData = []byte(`{"downloadCode":"test-transient-token","downloadUrl":"https://example.invalid/private"}`)
	if _, _, err := s.Register(ctx, bad); err == nil {
		t.Fatal("persisted transport credentials in a routing command")
	}
	if _, e := s.Get(ctx, in.Key); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal("invalid registration left data", e)
	}
	other := in
	other.Key.InstanceID = "second-stable-id"
	other.InstanceName = in.InstanceName
	if _, _, e := s.Register(ctx, other); e != nil {
		t.Fatal(e)
	}
	registration := make(chan bool, 2)
	claim := make(chan bool, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, fresh, err := s.Register(ctx, in)
			registration <- fresh
			if err != nil {
				claim <- false
				errs <- err
				return
			}
			won, err := s.Transition(ctx, in.Key, "notice", "pending", PhotoIntakeState{Status: "sending"}, 10)
			claim <- won
			errs <- err
		}()
	}
	firstRegistration, secondRegistration := <-registration, <-registration
	firstClaim, secondClaim := <-claim, <-claim
	if firstRegistration == secondRegistration || firstClaim == secondClaim {
		t.Fatal("two callbacks did not share one registration and one sender")
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	changePhotoTestState(t, s, in.Key, "notice", "sending", PhotoIntakeState{Status: "accepted", ExternalID: "original-notice"}, 20)
	changePhotoTestState(t, s, in.Key, "notice", "accepted", PhotoIntakeState{Status: "unknown", FailureKind: "query_unavailable"}, 21)
	if ok, e := s.Transition(ctx, in.Key, "notice", "unknown", PhotoIntakeState{Status: "sending"}, 22); e == nil || ok {
		t.Fatal("blindly resent an unknown notice")
	}
	if ok, e := s.Transition(ctx, in.Key, "notice", "unknown", PhotoIntakeState{Status: "delivered", ExternalID: "other-notice"}, 23); !errors.Is(e, ErrPhotoIntakeConflict) || ok {
		t.Fatal("reconciled an unrelated handle")
	}
	changePhotoTestState(t, s, in.Key, "notice", "unknown", PhotoIntakeState{Status: "delivered"}, 24)
	first, _ := s.Get(ctx, in.Key)
	if first.Notice.ExternalID != "original-notice" || first.Notice.AcceptedAtMS != 20 || first.Notice.DeliveredAtMS != 24 || first.Notice.StartedAtMS != 10 {
		t.Fatalf("lost distinct notice facts: %+v", first.Notice)
	}
	if rows, e := s.Recoverable(ctx, in.Key.InstanceID, "", 10); e != nil || len(rows) != 0 {
		t.Fatalf("recovery cannot download a missing image after restart: %v %v", rows, e)
	}
	second, e := s.Recoverable(ctx, other.Key.InstanceID, "", 1)
	if e != nil || len(second) != 1 || second[0].Key != other.Key {
		t.Fatalf("recovery crossed stable instances: %+v %v", second, e)
	}
	if later, nextErr := s.Recoverable(ctx, other.Key.InstanceID, second[0].ID, 1); nextErr != nil || len(later) != 0 {
		t.Fatal("cursor repeated the same receipt", nextErr)
	}
	if e = s.SavePage(ctx, in.Key, 0, in.PageSignatures[0], "sheet", "image/png", photoTestPNG(t, 90), 25); e != nil {
		t.Fatal(e)
	}
	changePhotoTestState(t, s, in.Key, "work", "pending", PhotoIntakeState{Status: "running"}, 30)
	changePhotoTestState(t, s, in.Key, "work", "running", PhotoIntakeState{Status: "completed", ResultRef: "assistant-message"}, 31)
	changePhotoTestState(t, s, in.Key, "final", "pending", PhotoIntakeState{Status: "delegated"}, 32)
	changePhotoTestState(t, s, in.Key, "final", "delegated", PhotoIntakeState{Status: "delivered"}, 33)
	changePhotoTestState(t, s, in.Key, "notice", "delivered", PhotoIntakeState{Status: "recalling"}, 33)
	changePhotoTestState(t, s, in.Key, "notice", "recalling", PhotoIntakeState{Status: "recalled"}, 34)
	closed, e := s.Get(ctx, in.Key)
	if e != nil || closed.Notice.DeliveredAtMS != 24 || closed.Notice.RecalledAtMS != 34 || closed.Notice.ExternalID != "original-notice" {
		t.Fatalf("recall erased prior facts %+v %v", closed.Notice, e)
	}
	if rows, e := s.Recoverable(ctx, in.Key.InstanceID, "", 10); e != nil || len(rows) != 0 {
		t.Fatal("closed receipt remained in recovery", e)
	}
}

func TestPhotoIntakeRecoveryUsesOnlyOutstandingIndex(t *testing.T) {
	s := openPhotoTestStore(t, filepath.Join(t.TempDir(), "recovery.sqlite"))
	var id, parent, unused int
	var plan string
	if err := s.db.QueryRowContext(context.Background(), `EXPLAIN QUERY PLAN SELECT id FROM im_photo_intakes WHERE instance_id=? AND recoverable=1 AND id>? ORDER BY id LIMIT 10`, "bot", "").Scan(&id, &parent, &unused, &plan); err != nil || !strings.Contains(plan, "idx_im_photo_intakes_recovery") {
		t.Fatalf("recovery plan: %s, %v", plan, err)
	}
}
