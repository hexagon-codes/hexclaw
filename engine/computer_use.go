package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/ai-core/llm/openai"
	"github.com/hexagon-codes/hexagon/browserruntime"
	hruntime "github.com/hexagon-codes/hexagon/runtime"

	"github.com/hexagon-codes/hexclaw/internal/externaltools"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

// ComputerUseUsage 记录单次物理模型请求，Usage 为 nil 时不能计作零消耗。
// ObserveUsage 的实现只负责持久化和观测，共享预算由控制器计量。
type ComputerUseUsage struct {
	Provider, Model, ExecutionID, ModelExchangeID, ResponseID, RequestID string
	ExchangeOrdinal                                                      uint64
	Usage                                                                *llm.Usage
	Cost                                                                 float64
	Duration                                                             time.Duration
}

type ComputerUseOptions struct {
	Router                  *llmrouter.Selector
	Browser                 *browserruntime.Coordinator
	Executions              *externaltools.Service
	Budget                  func(context.Context) *BudgetController
	ObserveUsage            func(context.Context, ComputerUseUsage) error
	AcknowledgeSafetyChecks func(context.Context, []openai.ComputerSafetyCheck) error
	// AuthorizeModel 复用当前工具调用的完整权限钩子与带版本授权。
	AuthorizeModel func(context.Context, map[string]any) (context.Context, error)
	// BorrowBrowser 借用当前 run 持有的同一租约，归还由 run 完成器负责。
	BorrowBrowser     func(context.Context, browserruntime.TaskRef, browserruntime.Target) (*browserruntime.Lease, error)
	MaxModelExchanges int
}

type ComputerUseController struct{ opts ComputerUseOptions }

func NewComputerUseController(opts ComputerUseOptions) *ComputerUseController {
	if opts.MaxModelExchanges <= 0 {
		opts.MaxModelExchanges = 32
	}
	if opts.Budget == nil {
		opts.Budget = CurrentToolBudget
	}
	return &ComputerUseController{opts: opts}
}

// Execute 只消费当前调用的可信归属；工具参数不能指定用户、run 或执行身份。
// 已派发但未取得终态的原调用只返回原回执，不自动重发模型请求或页面动作。
func (c *ComputerUseController) Execute(ctx context.Context, args map[string]any) (result *skill.Result, err error) {
	if c == nil || c.opts.Router == nil || c.opts.Executions == nil || (c.opts.Browser == nil && c.opts.BorrowBrowser == nil) {
		return nil, errors.New("computer use runtime is unavailable")
	}
	text := func(key string) string { value, _ := args[key].(string); return strings.TrimSpace(value) }
	if text("task") == "" {
		return nil, errors.New("computer use task is required")
	}
	ctx, operation, err := c.opts.Executions.Begin(ctx, externaltools.Intent{Kind: "computer_use", ToolName: "computer_use", Input: args}, false)
	if err != nil {
		return nil, err
	}
	if operation.Record.Phase != storage.ExternalExecutionPrepared {
		if operation.Record.Phase == storage.ExternalExecutionObserved && operation.Record.Outcome == "success" {
			return &skill.Result{Content: "Computer use already completed; use the original task result and execution receipt.", Data: map[string]any{"execution_id": operation.Record.ID}, Detail: &skill.ToolExecutionDetail{Phase: "computer_use", Outcome: "success", Reason: "original_receipt"}}, nil
		}
		outcome := operation.Record.Outcome
		if outcome == "" || operation.Record.Phase != storage.ExternalExecutionObserved {
			outcome = "unknown"
		}
		return nil, computerUseError(outcome, "original_receipt", errors.New("computer use was already dispatched; reconcile its original receipt before retrying"))
	}
	if err = c.opts.Executions.Dispatch(ctx, operation); err != nil {
		return nil, err
	}
	defer func() {
		phase, outcome, reason := storage.ExternalExecutionObserved, "success", "completed"
		if err != nil {
			outcome, reason = "failure", "execution_failed"
			var executionErr *skill.ExecutionError
			if errors.As(err, &executionErr) {
				outcome, reason = executionErr.Detail.Outcome, executionErr.Detail.Reason
			}
			if outcome == "unknown" {
				phase = storage.ExternalExecutionUnknown
			}
			if errors.Is(err, context.Canceled) && outcome != "unknown" {
				outcome, reason = "cancelled", "cancelled" //nolint:misspell // 沿用持久化终态合同的拼写。
			}
		}
		finishErr := c.opts.Executions.Finish(operation, phase, outcome, reason, map[string]any{"execution_id": operation.Record.ID}, false)
		if finishErr != nil {
			err = computerUseError("unknown", "receipt_save_failed", errors.Join(err, finishErr))
		}
	}()
	inv := skill.CurrentInvocation(ctx)
	route, err := c.opts.Router.SelectComputerUse(llmrouter.ComputerUseSelection{CurrentProvider: inv.ScenarioInput["provider"], CurrentModel: inv.ScenarioInput["model"], Provider: text("provider"), Model: text("model")})
	if err != nil {
		return nil, computerUseError("failure", "model_unavailable", err)
	}
	budget := c.opts.Budget(ctx)
	if err = checkComputerUseBudget(ctx, budget); err != nil {
		return nil, err
	}
	existing, _ := args["existing_session"].(bool)
	target := browserruntime.Target{ServerName: text("server"), ProfileID: text("profile"), PageID: text("page"), RequireExistingSession: existing}
	task := browserruntime.TaskRef{ID: operation.Record.ID, OwnerID: inv.AuthenticatedUserID, SessionID: inv.SessionID, RequestID: inv.RequestID, ToolCallID: inv.ToolCallID}
	var lease *browserruntime.Lease
	if c.opts.BorrowBrowser != nil {
		lease, err = c.opts.BorrowBrowser(ctx, task, target)
	} else {
		lease, err = c.opts.Browser.Acquire(ctx, task, target)
	}
	if err != nil {
		outcome := "failure"
		var unknown *browserruntime.UnknownCallError
		if errors.As(err, &unknown) {
			outcome = "unknown"
		}
		return nil, computerUseError(outcome, "browser_acquire_failed", err)
	}
	if lease == nil {
		return nil, errors.New("computer use browser lease is unavailable")
	}
	if c.opts.BorrowBrowser == nil {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if releaseErr := lease.Release(cleanup, "computer_use_finished"); releaseErr != nil {
				err = computerUseError("unknown", "browser_not_quiescent", errors.Join(err, releaseErr))
			}
		}()
	}
	image, err := c.capture(ctx, lease, operation.Record.LogicalCallKey+"/initial", "", 0, "", budget)
	if err != nil {
		return nil, err
	}
	imageURL := computerUseImageURL(image)
	// 实际截图已取得后再标记媒体，复用普通工具图片的用途、分类与记忆继承规则。
	ctx = labelToolObservationEgress(ctx, llm.CompletionRequest{Messages: []llm.Message{{Name: hruntime.ToolMediaMessageName, MultiContent: []llm.ContentPart{{Type: "image_url", ImageURL: &llm.ImageURL{URL: imageURL}}}}}})
	input := []openai.ResponseInputItem{{Role: "user", Content: []openai.ResponseInputContent{{Type: "input_text", Text: text("task")}, {Type: "input_image", ImageURL: imageURL, Detail: "original"}}}}
	store := false
	var activeExchangeID, activeAttemptID string
	var activeModelCtx context.Context
	var lastResponse *openai.ResponsesResponse
	var pendingChecks map[string][]openai.ComputerSafetyCheck
	content, loopErr := browserruntime.RunComputerUse(ctx, image, browserruntime.ComputerUseLoopPorts{
		MaxExchanges: c.opts.MaxModelExchanges,
		Exchange: func(_ context.Context, ordinal uint64, observed browserruntime.Image) (browserruntime.ComputerUseTurn, error) {
			if err = checkComputerUseBudget(ctx, budget); err != nil {
				return browserruntime.ComputerUseTurn{}, err
			}
			digest := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", operation.Record.LogicalCallKey, ordinal)))
			exchangeID := hex.EncodeToString(digest[:])
			request := openai.ResponsesRequest{Model: route.Model, Input: input, Tools: []openai.ComputerTool{{Type: "computer"}}, Store: &store, Include: []string{"reasoning.encrypted_content"}}
			var modelCtx context.Context
			var attempt *externaltools.Attempt
			var response *openai.ResponsesResponse
			var modelErr error
			var started time.Time
			for {
				if c.opts.AuthorizeModel != nil {
					ctx, err = c.opts.AuthorizeModel(ctx, args)
					if err != nil {
						return browserruntime.ComputerUseTurn{}, err
					}
				}
				var beginErr error
				modelCtx, attempt, beginErr = c.opts.Executions.Begin(ctx, externaltools.Intent{Kind: "model_request", ToolName: "computer_use", LogicalSuffix: exchangeID, Input: request, ModelExchangeID: exchangeID, ModelExchangeOrdinal: ordinal, BrowserGeneration: lease.Ref().Generation}, false)
				if beginErr != nil {
					return browserruntime.ComputerUseTurn{}, beginErr
				}
				if attempt.Record.Phase != storage.ExternalExecutionPrepared {
					return browserruntime.ComputerUseTurn{}, computerUseError("unknown", "original_model_receipt", errors.New("original computer use model exchange cannot be resent"))
				}
				request.BeforeSend = func(sendCtx context.Context) error {
					if checkErr := checkComputerUseBudget(sendCtx, budget); checkErr != nil {
						return checkErr
					}
					if c.opts.AuthorizeModel != nil {
						var authorizeErr error
						sendCtx, authorizeErr = c.opts.AuthorizeModel(sendCtx, args)
						if authorizeErr != nil {
							return authorizeErr
						}
						modelCtx = sendCtx
						granted, ok := skill.ToolDispatchPolicy(sendCtx, "computer_use")
						if !ok {
							return errors.New("computer use model dispatch requires its approved policy version")
						}
						if granted.Profile != attempt.Record.PolicyProfile || granted.Revision != attempt.Record.PolicyRevision {
							finishErr := c.opts.Executions.Finish(attempt, storage.ExternalExecutionObserved, "failed", "policy_changed_before_dispatch", nil, false)
							return errors.Join(externaltools.ErrPolicyChanged, finishErr)
						}
					}
					return c.opts.Executions.Dispatch(sendCtx, attempt)
				}
				started = time.Now()
				response, modelErr = route.Client.CreateResponse(modelCtx, request)
				// 策略切换已确认阻止物理派发；保留原轮次与输入，只准备下一个尝试。
				// Dispatch 已收口该条回执，不能再次 Finish 或把它记成模型消耗。
				if errors.Is(modelErr, externaltools.ErrPolicyChanged) && response == nil && attempt.Record.Phase == storage.ExternalExecutionObserved && attempt.Record.Reason == "policy_changed_before_dispatch" {
					if err = checkComputerUseBudget(ctx, budget); err != nil {
						return browserruntime.ComputerUseTurn{}, err
					}
					continue
				}
				break
			}
			modelDispatched := attempt.Record.Phase == storage.ExternalExecutionDispatched || response != nil
			usage := ComputerUseUsage{Provider: route.ProviderName, Model: route.Model, ExecutionID: attempt.Record.ID, ModelExchangeID: exchangeID, ExchangeOrdinal: ordinal, Duration: time.Since(started)}
			metadata := map[string]any{"provider": route.ProviderName, "model": route.Model, "model_exchange_id": exchangeID, "model_exchange_ordinal": ordinal, "usage_known": false, "dispatched": modelDispatched}
			if response != nil {
				usage.ResponseID, usage.RequestID, usage.Usage = response.ID, response.RequestID, response.Usage
				metadata["response_id"], metadata["request_id"], metadata["status"] = response.ID, response.RequestID, response.Status
				metadata["requested_model"] = route.Model
				if response.Model != "" {
					usage.Model = response.Model
					metadata["model"] = response.Model
				}
				if response.Usage != nil {
					usage.Cost = EstimateCost(route.ProviderName, usage.Model, response.Usage.PromptTokens, response.Usage.CompletionTokens)
					metadata["usage_known"], metadata["usage"] = true, response.Usage
					if budget != nil {
						budget.RecordTokens(response.Usage.TotalTokens)
						budget.RecordCost(usage.Cost)
					}
				}
			}
			// 取消后的完整回包仍记录实际消耗，但不再把它接受为可执行动作。
			if modelErr == nil && ctx.Err() != nil {
				modelErr = ctx.Err()
			}
			if modelErr == nil {
				modelErr = validateComputerUseResponse(response)
			}
			phase, outcome, reason := storage.ExternalExecutionObserved, "success", "completed"
			if modelErr != nil {
				outcome, reason = "failure", "model_failed"
				var responseErr *openai.ResponsesError
				hasResponseError := errors.As(modelErr, &responseErr)
				if hasResponseError {
					// 失败响应只保存结构化关联字段，不复制上游正文或底层错误。
					if responseErr.StatusCode > 0 {
						metadata["http_status"] = responseErr.StatusCode
					}
					if responseErr.RequestID != "" {
						metadata["request_id"] = responseErr.RequestID
						usage.RequestID = responseErr.RequestID
					}
					if responseErr.Code != "" {
						metadata["error_code"] = responseErr.Code
					}
					metadata["outcome_unknown"] = responseErr.OutcomeUnknown
					if responseErr.OutcomeUnknown {
						phase, outcome, reason = storage.ExternalExecutionUnknown, "unknown", "model_outcome_unknown"
					}
				}
				if response == nil && attempt.Record.Phase == storage.ExternalExecutionDispatched && !hasResponseError {
					phase, outcome, reason = storage.ExternalExecutionUnknown, "unknown", "model_outcome_unknown"
				}
			}
			if response != nil {
				var calls []string
				for _, item := range response.Output {
					if item.Type == "computer_call" {
						calls = append(calls, item.CallID)
					}
				}
				metadata["call_ids"] = calls
			}
			if finishErr := c.opts.Executions.Finish(attempt, phase, outcome, reason, metadata, modelErr == nil); finishErr != nil {
				return browserruntime.ComputerUseTurn{}, computerUseError("unknown", "model_receipt_save_failed", finishErr)
			}
			if c.opts.ObserveUsage != nil && modelDispatched {
				observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				observeErr := c.opts.ObserveUsage(observeCtx, usage)
				cancel()
				if observeErr != nil {
					if outcome == "success" {
						outcome = "failure"
					}
					return browserruntime.ComputerUseTurn{}, computerUseError(outcome, "usage_save_failed", errors.Join(modelErr, observeErr))
				}
			}
			if modelErr != nil {
				return browserruntime.ComputerUseTurn{}, computerUseError(outcome, reason, modelErr)
			}
			for _, item := range response.Output {
				raw := item.RawJSON
				if len(raw) == 0 {
					raw, err = json.Marshal(item)
					if err != nil {
						return browserruntime.ComputerUseTurn{}, err
					}
				}
				input = append(input, openai.ResponseInputItem{RawJSON: raw})
			}
			activeExchangeID, activeAttemptID, activeModelCtx = exchangeID, attempt.Record.ID, modelCtx
			lastResponse = response
			pendingChecks = make(map[string][]openai.ComputerSafetyCheck)
			turn := browserruntime.ComputerUseTurn{Text: response.OutputText()}
			for _, item := range response.Output {
				if item.Type != "computer_call" {
					continue
				}
				pendingChecks[item.CallID] = item.PendingSafetyChecks
				intents := item.Actions
				if item.Action != nil {
					intents = []openai.ComputerAction{*item.Action}
				}
				call := browserruntime.ComputerUseCall{ID: item.CallID}
				for _, intent := range intents {
					call.Actions = append(call.Actions, computerUseAction(intent, observed))
				}
				turn.Calls = append(turn.Calls, call)
			}
			return turn, nil
		},
		BeforeCall: func(_ context.Context, _ uint64, call browserruntime.ComputerUseCall) error {
			checks := pendingChecks[call.ID]
			if len(checks) == 0 {
				return nil
			}
			if c.opts.AcknowledgeSafetyChecks == nil {
				return computerUseError("failure", "safety_check_required", errors.New("computer use safety checks require the current permission handler"))
			}
			return c.opts.AcknowledgeSafetyChecks(ctx, checks)
		},
		Execute: func(_ context.Context, ordinal uint64, call browserruntime.ComputerUseCall, index int, action browserruntime.Action) error {
			suffix := fmt.Sprintf("%s/%s/%s/%d", activeExchangeID, activeAttemptID, call.ID, index)
			_, _, actionErr := c.executeAction(activeModelCtx, lease, suffix, activeExchangeID, ordinal, call.ID, index, action, budget)
			return actionErr
		},
		Observe: func(_ context.Context, ordinal uint64, call browserruntime.ComputerUseCall) (browserruntime.Image, error) {
			image, captureErr := c.capture(activeModelCtx, lease, activeExchangeID+"/"+activeAttemptID+"/"+call.ID+"/observation", activeExchangeID, ordinal, call.ID, budget)
			if captureErr != nil {
				return browserruntime.Image{}, captureErr
			}
			input = append(input, openai.ResponseInputItem{Type: "computer_call_output", CallID: call.ID, Output: &openai.ComputerScreenshot{Type: "computer_screenshot", ImageURL: computerUseImageURL(image), Detail: "original"}, AcknowledgedSafetyChecks: pendingChecks[call.ID]})
			return image, nil
		},
	})
	if loopErr != nil {
		if errors.Is(loopErr, browserruntime.ErrComputerUseEmptyResult) {
			return nil, computerUseError("failure", "missing_final_result", loopErr)
		}
		if errors.Is(loopErr, browserruntime.ErrComputerUseExchangeLimit) {
			return nil, computerUseError("failure", "exchange_limit", loopErr)
		}
		return nil, loopErr
	}
	return &skill.Result{Content: content, Data: map[string]any{"execution_id": operation.Record.ID, "response_id": lastResponse.ID, "provider": route.ProviderName, "model": route.Model}, Detail: &skill.ToolExecutionDetail{Phase: "computer_use", Outcome: "success", Reason: "model_completed", ResponseReceived: true}}, nil
}

func checkComputerUseBudget(ctx context.Context, budget *BudgetController) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if budget != nil {
		return budget.Check()
	}
	return nil
}

func validateComputerUseResponse(response *openai.ResponsesResponse) error {
	if response == nil || response.ID == "" || response.Status != "completed" {
		return errors.New("computer use requires a complete model response")
	}
	seen := make(map[string]bool)
	for _, item := range response.Output {
		if item.Type != "computer_call" {
			continue
		}
		if item.CallID == "" || seen[item.CallID] || item.Status != "completed" || (len(item.Actions) == 0 && item.Action == nil) || (len(item.Actions) > 0 && item.Action != nil) {
			return errors.New("computer use response contains an incomplete or ambiguous action batch")
		}
		seen[item.CallID] = true
	}
	return nil
}

func computerUseAction(intent openai.ComputerAction, image browserruntime.Image) browserruntime.Action {
	action := browserruntime.Action{Type: intent.Type, X: float64(intent.X), Y: float64(intent.Y), Button: intent.Button, Text: intent.Text, Keys: intent.Keys, ScrollX: float64(intent.ScrollX), ScrollY: float64(intent.ScrollY)}
	switch intent.Type {
	case "click", "double_click", "move", "scroll", "drag":
		action.SourceWidth, action.SourceHeight = image.Width, image.Height
	}
	for _, point := range intent.Path {
		action.Path = append(action.Path, browserruntime.Point{X: float64(point.X), Y: float64(point.Y)})
	}
	if intent.Type == "wait" {
		action.Seconds = 1
	}
	return action
}

func computerUseImageURL(image browserruntime.Image) string {
	return "data:" + image.MIME + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
}

// executeAction 在派发前保存动作身份，且仅使用当前租约所绑定的原页面。
func (c *ComputerUseController) executeAction(ctx context.Context, lease *browserruntime.Lease, suffix, exchangeID string, ordinal uint64, callID string, index int, action browserruntime.Action, budget *BudgetController) (browserruntime.ActionResult, *externaltools.Attempt, error) {
	if err := checkComputerUseBudget(ctx, budget); err != nil {
		return browserruntime.ActionResult{}, nil, err
	}
	ref := lease.Ref()
	actionCtx, attempt, err := c.opts.Executions.Begin(ctx, externaltools.Intent{Kind: "browser_action", ToolName: "computer_use", ServerName: ref.ServerName, TargetID: ref.PageID, LogicalSuffix: suffix, Input: action, ModelExchangeID: exchangeID, ModelExchangeOrdinal: ordinal, UpstreamCallID: callID, ActionIndex: index, BrowserGeneration: ref.Generation}, false)
	if err != nil {
		return browserruntime.ActionResult{}, nil, err
	}
	if err = c.opts.Executions.Dispatch(actionCtx, attempt); err != nil {
		return browserruntime.ActionResult{}, attempt, computerUseError("unknown", "original_action_receipt", err)
	}
	result, actionErr := lease.Execute(actionCtx, action)
	if actionErr == nil && !result.Result.Success {
		actionErr = errors.New("browser returned a failed action result")
	}
	phase, outcome, reason := storage.ExternalExecutionObserved, "success", "completed"
	if actionErr != nil {
		outcome, reason = "failure", "browser_action_failed"
		var unknown *browserruntime.UnknownCallError
		if result.Outcome == "unknown" || errors.As(actionErr, &unknown) || errors.Is(actionErr, browserruntime.ErrNotQuiescent) {
			phase, outcome, reason = storage.ExternalExecutionUnknown, "unknown", "browser_outcome_unknown"
		}
	}
	metadata := map[string]any{"action": action.Type, "page_id": ref.PageID, "browser_generation": ref.Generation}
	// 截图字节在 observed 终态前登记，回执冻结后不再追加或替换观察。
	if actionErr == nil && action.Type == "screenshot" {
		if len(result.Images) != 1 || len(result.Images[0].Data) == 0 || result.Images[0].Width <= 0 || result.Images[0].Height <= 0 {
			actionErr = errors.New("browser screenshot is missing its actual image or dimensions")
			outcome, reason = "failure", "missing_screenshot"
		} else {
			image := result.Images[0]
			artifact, saveErr := c.opts.Executions.SaveObservation(actionCtx, "computer-observation.png", image.MIME, bytes.NewReader(image.Data))
			if saveErr != nil {
				actionErr = saveErr
				outcome, reason = "failure", "screenshot_save_failed"
			} else {
				metadata["observation"] = artifact
				metadata["width"], metadata["height"] = image.Width, image.Height
			}
		}
	}
	if finishErr := c.opts.Executions.Finish(attempt, phase, outcome, reason, metadata, false); finishErr != nil {
		return result, attempt, computerUseError("unknown", "action_receipt_save_failed", finishErr)
	}
	if actionErr != nil {
		return result, attempt, computerUseError(outcome, reason, actionErr)
	}
	return result, attempt, nil
}

func (c *ComputerUseController) capture(ctx context.Context, lease *browserruntime.Lease, suffix, exchangeID string, ordinal uint64, callID string, budget *BudgetController) (browserruntime.Image, error) {
	result, _, err := c.executeAction(ctx, lease, suffix, exchangeID, ordinal, callID, -1, browserruntime.Action{Type: "screenshot"}, budget)
	if err != nil {
		return browserruntime.Image{}, err
	}
	return result.Images[0], nil
}

// 公共错误只暴露阶段与结果，保留原始错误链供内部分类而不输出页面内容或参数。
type computerUsePublicError struct {
	reason string
	cause  error
}

func (e *computerUsePublicError) Error() string {
	return "computer use failed: " + strings.ReplaceAll(e.reason, "_", " ")
}
func (e *computerUsePublicError) Unwrap() error { return e.cause }

func computerUseError(outcome, reason string, cause error) *skill.ExecutionError {
	return &skill.ExecutionError{Detail: skill.ToolExecutionDetail{Phase: "computer_use", Outcome: outcome, Reason: reason}, Err: &computerUsePublicError{reason: reason, cause: cause}}
}
