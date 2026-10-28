package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type observedFailurePhysicalCall struct {
	payload  string
	calls    int
	requests []string
}

func (p *observedFailurePhysicalCall) ExecuteGradingPhysicalCall(ctx context.Context, spec usecase.GradingPhysicalCallSpec, send func(context.Context) (string, error)) (usecase.GradingPhysicalCallResult, error) {
	p.calls++
	p.requests = append(p.requests, spec.RequestDigest)
	payload, err := send(ctx)
	p.payload = payload
	return usecase.GradingPhysicalCallResult{Payload: payload, InvocationID: "observed-failure"}, err
}

func TestSolveAdapterPreservesObservedResponseOnLocalFailure(t *testing.T) {
	physical := &observedFailurePhysicalCall{}
	ctx := usecase.WithGradingPhysicalCallExecutor(t.Context(), physical)
	localFailure := errors.New("local verification failed")
	solver := engine.NewSolveSkill(func(ctx context.Context, _ engine.SubAgentSpec) (engine.SubAgentResult, error) {
		if _, bounded := ctx.Deadline(); bounded {
			t.Error("durable default solve received an unrelated engine deadline")
		}
		return engine.SubAgentResult{Output: "Observed but unverified solution."}, errors.Join(egress.ErrProviderResponseProcessed, localFailure)
	}, engine.NewSubAgentRegistry(""))
	_, err := NewSolveAdapter(solver).Solve(ctx, "根据图中条件解方程并说明依据。", "", "")
	var observed engine.SubAgentResult
	if !errors.Is(err, localFailure) || physical.calls != 2 || physical.requests[0] == physical.requests[1] || json.Unmarshal([]byte(physical.payload), &observed) != nil || observed.Output != "Observed but unverified solution." {
		t.Fatalf("adapter lost observed failed response or retried: calls=%d payload=%q err=%v", physical.calls, physical.payload, err)
	}
}
