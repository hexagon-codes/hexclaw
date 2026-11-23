package usecase

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestImageTaskSourceCommandConcurrentAdmission(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_pages_%t", same), func(t *testing.T) {
			c, _ := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{})
			c.NewID = nil
			c.ReadAsset = func(_, ref string) ([]byte, error) { return []byte(ref), nil }
			first := testCreateImageTaskInput()
			first.SourceAssetRefs = append(first.SourceAssetRefs, fmt.Sprintf("asset://mingming/%064x.png", 2))
			second := first
			if !same {
				second.SourceAssetRefs = first.SourceAssetRefs[:1]
			}
			reached, release := make(chan struct{}, 2), make(chan struct{})
			var resolutions atomic.Int32
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			c.ResolveRoute = func(req k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
				if resolutions.Add(1) <= 2 {
					reached <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						return k12.ImageTaskRouteSnapshot{}, ctx.Err()
					}
				}
				return imageTaskRouteForTest(req)
			}
			type outcome struct {
				tasks []CreatedImageTask
				err   error
			}
			finished := make(chan outcome, 2)
			for _, input := range []CreateImageTaskInput{first, second} {
				go func() { rows, _, err := c.CreateMany(ctx, input); finished <- outcome{rows, err} }()
			}
			for range 2 {
				select {
				case <-reached:
				case <-ctx.Done():
					t.Fatal("both requests did not reach preparation")
				}
			}
			close(release)
			a, b := <-finished, <-finished
			if same {
				if a.err != nil || b.err != nil || len(a.tasks) != 2 || len(b.tasks) != 2 {
					t.Fatalf("same simultaneous source failed: %v, %v", a.err, b.err)
				}
				for i := range a.tasks {
					if a.tasks[i].View.Dispatch.DispatchID != b.tasks[i].View.Dispatch.DispatchID || a.tasks[i].Created == b.tasks[i].Created {
						t.Fatalf("page %d was not created exactly once", i+1)
					}
				}
			} else {
				if a.err != nil {
					a, b = b, a
				}
				if a.err != nil || !errors.Is(b.err, k12storage.ErrImageTaskConflict) || len(b.tasks) != 0 {
					t.Fatalf("different simultaneous shapes: winner=%v other=%v other_pages=%d", a.err, b.err, len(b.tasks))
				}
			}
		})
	}
}

func TestImageTaskSourceCommandFreezesOrderedPages(t *testing.T) {
	c, _ := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{})
	c.NewID = nil
	base := testCreateImageTaskInput()
	refs := []string{base.SourceAssetRefs[0], fmt.Sprintf("asset://mingming/%064x.png", 2), fmt.Sprintf("asset://mingming/%064x.png", 3)}
	c.ReadAsset = func(_, ref string) ([]byte, error) { return []byte(ref), nil }
	input := func(origin string, pages ...string) CreateImageTaskInput {
		in := base
		in.SourceRef, in.SourceAssetRefs = origin, pages
		return in
	}
	ctx := context.Background()
	original, _, err := c.CreateMany(ctx, input("family", refs[:2]...))
	if err != nil || len(original) != 2 {
		t.Fatalf("create: %v %v", original, err)
	}
	for _, changed := range [][]string{{refs[0]}, {refs[1], refs[0]}, refs, {refs[0], refs[2]}} {
		rows, _, conflictErr := c.CreateMany(ctx, input("family", changed...))
		if !errors.Is(conflictErr, k12storage.ErrImageTaskConflict) || len(rows) != 0 {
			t.Errorf("changed pages accepted: %v %v", rows, conflictErr)
		}
	}
	originalAgain, _, err := c.CreateMany(ctx, input("family", refs[:2]...))
	if err != nil || len(originalAgain) != 2 {
		t.Fatalf("replay: %v %v", originalAgain, err)
	}
	for i, row := range originalAgain {
		if row.Created || row.View.Dispatch.DispatchID != original[i].View.Dispatch.DispatchID {
			t.Fatal("replay replaced the page")
		}
	}
	// 外部消息本身允许带稳定页标后缀。新来源和另一条消息的派生页分别接纳。
	if _, created, createErr := c.Create(ctx, input("independent:image:1", refs[0])); createErr != nil || !created {
		t.Fatal(created, createErr)
	}
	nested, _, err := c.CreateMany(ctx, input("independent", refs[0], refs[1]))
	if err != nil || len(nested) != 2 || !nested[0].Created || !nested[1].Created {
		t.Fatalf("independent source names collided: %v %v", nested, err)
	}
	if _, created, err := c.Create(ctx, input("family:image:2", refs[1])); err != nil || !created {
		t.Fatal(created, err)
	}
}

func TestImageTaskSourceCommandContinuesOnlyItsIncompleteBatch(t *testing.T) {
	c, _ := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{})
	c.NewID = nil
	in := testCreateImageTaskInput()
	in.SourceAssetRefs = append(in.SourceAssetRefs, fmt.Sprintf("asset://mingming/%064x.png", 2))
	reads := 0
	failure := errors.New("second page temporarily unavailable")
	c.ReadAsset = func(_, ref string) ([]byte, error) {
		if ref == in.SourceAssetRefs[1] {
			reads++
			if reads == 2 {
				return nil, failure
			}
		}
		return []byte(ref), nil
	}
	first, failedPage, err := c.CreateMany(t.Context(), in)
	if !errors.Is(err, failure) || failedPage != 1 || len(first) != 1 || !first[0].Created {
		t.Fatalf("partial receipt = %v %d %v", first, failedPage, err)
	}
	changed := in
	changed.SourceAssetRefs = append([]string{}, in.SourceAssetRefs[0], fmt.Sprintf("asset://mingming/%064x.png", 3))
	if rows, _, conflictErr := c.CreateMany(t.Context(), changed); !errors.Is(conflictErr, k12storage.ErrImageTaskConflict) || len(rows) != 0 {
		t.Fatalf("substituted missing page: %v %v", rows, conflictErr)
	}
	next, _, err := c.CreateMany(t.Context(), in)
	if err != nil || len(next) != 2 || next[0].Created || !next[1].Created || next[0].View.Dispatch.DispatchID != first[0].View.Dispatch.DispatchID {
		t.Fatalf("resume original batch: %v %v", next, err)
	}
}
