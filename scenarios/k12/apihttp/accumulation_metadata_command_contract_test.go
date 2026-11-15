package apihttp_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
)

type unknownAccumulationMetadataDeriver struct{ calls int }

func (d *unknownAccumulationMetadataDeriver) DeriveAccumulationMetadata(context.Context, string) (k12.AccumulationDerivedMetadata, error) {
	d.calls++
	return k12.AccumulationDerivedMetadata{}, context.DeadlineExceeded
}

func TestCurrentAccumulationUnknownCommandReturnsConflictWithoutReinvocation(t *testing.T) {
	deriver := &unknownAccumulationMetadataDeriver{}
	h := newServerWithSolver(t, fakeSolveExec{}, assembly.WithAccumulationMetadataDeriver(deriver))
	for i := 0; i < 2; i++ {
		response, _ := doCurrent(t, h, http.MethodPost, "/accumulation?agent=mingming", `{"content":"apple"}`, map[string]string{"Idempotency-Key": "unknown-command"})
		if response.Code != http.StatusConflict {
			t.Fatalf("attempt %d status=%d body=%s", i, response.Code, response.Body.String())
		}
	}
	if deriver.calls != 1 {
		t.Fatalf("same command invoked provider %d times", deriver.calls)
	}
}
