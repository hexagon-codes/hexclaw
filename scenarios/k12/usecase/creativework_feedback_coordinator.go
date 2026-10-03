package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// CreativeWorkFeedbackCoordinator owns direct-text feedback workers. Image
// works remain owned by ImageTaskCoordinator; storage recovery filters the two
// domains so a generation always has exactly one runner.
type CreativeWorkFeedbackCoordinator struct {
	Deps        *Deps
	Records     *k12storage.Store
	BaseContext context.Context

	workerMu     sync.Mutex
	runCtx       context.Context
	runCancel    context.CancelCauseFunc
	agentWorkers agentWorkerFenceRegistry
	workerCount  int
	workerIdle   chan struct{}
	sealed       bool
}

func (c *CreativeWorkFeedbackCoordinator) validate() error {
	if c == nil || c.Deps == nil || c.Records == nil {
		return fmt.Errorf("creative work feedback coordinator dependencies unavailable")
	}
	return nil
}

func (c *CreativeWorkFeedbackCoordinator) initWorkerRuntimeLocked() {
	if c.workerIdle == nil {
		c.workerIdle = make(chan struct{})
		close(c.workerIdle)
	}
	if c.runCtx == nil {
		base := c.BaseContext
		if base == nil {
			base = context.Background()
		}
		c.runCtx, c.runCancel = context.WithCancelCause(base)
	}
}

// StartAsync accepts only a persisted, non-terminal generation. Duplicate
// schedules in the same process collapse on the generation identity.
func (c *CreativeWorkFeedbackCoordinator) StartAsync(
	agentName, generationID string,
) bool {
	if c.validate() != nil {
		return false
	}
	agentName = strings.TrimSpace(agentName)
	generationID = strings.TrimSpace(generationID)
	if agentName == "" || generationID == "" {
		return false
	}
	generation, err := c.Records.GetWorkFeedbackGeneration(
		context.Background(), agentName, generationID,
	)
	if err != nil ||
		(generation.Status != k12.WorkFeedbackQueued &&
			generation.Status != k12.WorkFeedbackRunning) {
		return false
	}

	key := agentName + "\x00" + generationID
	c.workerMu.Lock()
	c.initWorkerRuntimeLocked()
	if c.sealed {
		c.workerMu.Unlock()
		return false
	}
	runCtx, finishWorker, accepted := c.agentWorkers.start(
		c.runCtx, agentName, key,
	)
	if !accepted {
		c.workerMu.Unlock()
		return false
	}
	if c.workerCount == 0 {
		c.workerIdle = make(chan struct{})
	}
	c.workerCount++
	c.workerMu.Unlock()

	go func() {
		defer func() {
			c.workerMu.Lock()
			c.workerCount--
			if c.workerCount == 0 {
				close(c.workerIdle)
			}
			c.workerMu.Unlock()
			finishWorker()
		}()
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error(
					"K12 CreativeWork feedback worker panic; durable checkpoint retained",
					"agent", agentName,
					"generation_id", generationID,
					"panic", recovered,
				)
			}
		}()
		if err := c.Run(runCtx, agentName, generationID); err != nil {
			slog.Warn(
				"K12 CreativeWork feedback worker stopped at durable checkpoint",
				"agent", agentName,
				"generation_id", generationID,
				"err", err,
			)
		}
	}()
	return true
}

// QuiesceAgent fences new local feedback workers for one Agent, cancels and
// drains workers already owned by this coordinator, and returns an idempotent
// resume callback for the enclosing Agent-deletion saga.
func (c *CreativeWorkFeedbackCoordinator) QuiesceAgent(
	ctx context.Context,
	agentName string,
) (func(), error) {
	if c == nil {
		return func() {}, nil
	}
	return c.agentWorkers.quiesceAgent(ctx, agentName)
}

func (c *CreativeWorkFeedbackCoordinator) Run(
	ctx context.Context,
	agentName, generationID string,
) error {
	if err := c.validate(); err != nil {
		return err
	}
	generation, err := c.Records.GetWorkFeedbackGeneration(
		ctx, strings.TrimSpace(agentName), strings.TrimSpace(generationID),
	)
	if err != nil {
		return err
	}
	switch generation.Status {
	case k12.WorkFeedbackSucceeded:
		return nil
	case k12.WorkFeedbackFailed:
		return k12storage.ErrImageTaskInvalidState
	case k12.WorkFeedbackQueued, k12.WorkFeedbackRunning:
	default:
		return k12storage.ErrImageTaskInvalidState
	}
	invocation, invocationErr := c.Records.GetLatestWorkFeedbackInvocation(ctx, agentName, generation.WorkID,
		"work:"+generation.WorkID+":version:"+generation.GenerationID+":feedback")
	if invocationErr != nil && !errors.Is(invocationErr, k12storage.ErrImageTaskNotFound) {
		return invocationErr
	}
	if invocationErr == nil && invocation.Status == k12.ImageTaskInvocationSent {
		// 重启后的在途调用只等原截止时间，不延长预算或再次发送。
		slog.Info("K12 work feedback recovery waiting for original deadline", "agent_id", agentName,
			"work_id", generation.WorkID, "generation_id", generationID,
			"invocation_id", invocation.InvocationID, "deadline_at", invocation.DeadlineAt)
		if invocation.DeadlineAt > 0 {
			timer := time.NewTimer(time.Until(time.Unix(invocation.DeadlineAt, 0)))
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		} else if err := c.Records.FailWorkFeedbackInvocation(ctx, agentName, invocation.InvocationID,
			"work_feedback_outcome_unknown", true, false); err != nil {
			return err
		}
	}
	count, err := c.Records.ExpirePreparedWorkFeedbackGenerations(ctx, agentName, generation.WorkID, time.Now().Unix())
	if err != nil {
		return err
	}
	if count > 0 {
		slog.Info("K12 work feedback recovery settled invocation", "agent_id", agentName,
			"work_id", generation.WorkID, "generation_id", generationID, "settled_generations", count)
	}
	generation, err = c.Records.GetWorkFeedbackGeneration(ctx, agentName, generationID)
	if err != nil || generation.Status == k12.WorkFeedbackFailed {
		return err
	}
	_, err = c.Deps.GenerateWorkFeedbackCommand(
		ctx, generation.AgentName, generation.WorkID, generation.CommandKey,
	)
	return err
}

func (c *CreativeWorkFeedbackCoordinator) recoverySafe(
	ctx context.Context,
	generation k12.WorkFeedbackGeneration,
) (bool, error) {
	operationKey := "work:" + generation.WorkID +
		":version:" + generation.GenerationID + ":feedback"
	invocation, err := c.Records.GetLatestWorkFeedbackInvocation(
		ctx, generation.AgentName, generation.WorkID, operationKey,
	)
	if errors.Is(err, k12storage.ErrImageTaskNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	switch invocation.Status {
	case k12.ImageTaskInvocationPrepared, k12.ImageTaskInvocationSucceeded, k12.ImageTaskInvocationSent:
		return true, nil
	default:
		// 已终结的调用由恢复结算为失败；未知结果不再次发送。
		return false, nil
	}
}

func (c *CreativeWorkFeedbackCoordinator) Recover(
	ctx context.Context,
	agents []string,
) (int, error) {
	if err := c.validate(); err != nil {
		return 0, err
	}
	recovered := 0
	for _, agentName := range agents {
		generations, err := c.Records.ListDirectWorkFeedbackGenerationsForRecovery(
			ctx, strings.TrimSpace(agentName),
		)
		if err != nil {
			return recovered, err
		}
		for _, generation := range generations {
			count, err := c.Records.ExpirePreparedWorkFeedbackGenerations(ctx, generation.AgentName, generation.WorkID, time.Now().Unix())
			if err != nil {
				return recovered, err
			}
			if count > 0 {
				slog.Info("K12 work feedback startup recovery settled invocation", "agent_id", generation.AgentName,
					"work_id", generation.WorkID, "generation_id", generation.GenerationID, "settled_generations", count)
			}
			safe, err := c.recoverySafe(ctx, generation)
			if err != nil {
				return recovered, err
			}
			if safe && c.StartAsync(generation.AgentName, generation.GenerationID) {
				recovered++
			}
		}
	}
	return recovered, nil
}

func (c *CreativeWorkFeedbackCoordinator) Wait(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.workerMu.Lock()
	c.initWorkerRuntimeLocked()
	done := c.workerIdle
	c.workerMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *CreativeWorkFeedbackCoordinator) Shutdown(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.workerMu.Lock()
	c.initWorkerRuntimeLocked()
	c.sealed = true
	done := c.workerIdle
	cancel := c.runCancel
	c.workerMu.Unlock()
	if cancel != nil {
		cancel(context.Canceled)
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
