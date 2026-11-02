package main

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/externaltools"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

type externalShellOutput struct{ service *externaltools.Service }

func (s externalShellOutput) SaveShellOutput(ctx context.Context, _ skill.InvocationContext, stream string, input io.Reader) (map[string]any, error) {
	return s.service.SaveArtifact(ctx, stream+".txt", "text/plain; charset=utf-8", input)
}

// configureExternalToolIO 对原实例注入同一持久存储，不重新注册另一个工具。
func configureExternalToolIO(registry *skill.DefaultRegistry, deps *builtin.SkillDeps, service *externaltools.Service, executor *engine.ToolExecutor) {
	if s, ok := registry.Get("shell"); ok {
		if shell, ok := builtin.UnwrapConfigured(s).(*builtin.ShellSkill); ok {
			shell.SetOutputStore(externalShellOutput{service})
			if deps.CodeExecSkill != nil {
				deps.CodeExecSkill.SetHostExecutor(shell.Execute)
			}
		}
	}
	if implementation, ok := registry.Get("file_ops"); ok {
		if files, ok := builtin.UnwrapConfigured(implementation).(*builtin.FileOpsSkill); ok {
			files.SetArtifactWriter(service.SaveArtifact)
			files.SetFileAccessBroker(deps.FileAccess)
		}
	}
	if s, ok := registry.Get("browser"); ok {
		if browser, ok := builtin.UnwrapConfigured(s).(*builtin.BrowserSkill); ok {
			browser.SetArtifactWriter(service.SaveArtifact)
			if deps.FileAccess != nil {
				browser.SetFileOpener(func(ctx context.Context, path string) (io.ReadCloser, error) {
					if resolved, err := builtin.ResolveToolPath(ctx, deps.Workspace, path); err == nil {
						return os.Open(resolved)
					}
					return deps.FileAccess.OpenReadContext(ctx, path)
				})
			}
			browser.SetAttemptObserver(func(ctx context.Context, method, target string) (context.Context, func(*skill.Result, error) error, error) {
				u, parseErr := url.Parse(target)
				if parseErr != nil || u.Host == "" {
					return ctx, nil, errors.New("HTTP request target is invalid")
				}
				readOnly := method == "GET" || method == "HEAD" || method == "OPTIONS"
				inv := skill.CurrentInvocation(ctx)
				parent, parentErr := service.Receipt(ctx, inv.AuthenticatedUserID, skill.ExternalExecutionID(ctx))
				if parentErr != nil {
					return ctx, nil, parentErr
				}
				input := map[string]string{"method": method, "url": target, "request_arguments_digest": parent.InputDigest}
				var a *externaltools.Attempt
				var err error
				for dispatchAttempt := 0; dispatchAttempt < 2; dispatchAttempt++ {
					policy := skill.CurrentExecutionPolicy(ctx)
					if !skill.ToolDispatchAuthorized(ctx, "browser") {
						if executor == nil {
							return ctx, nil, errors.New("HTTP permission runtime is unavailable")
						}
						var gateErr error
						ctx, gateErr = executor.AuthorizeToolDispatch(ctx, &engine.ToolCallInfo{Name: "browser", Source: "skill", Arguments: map[string]any{"action": "request", "method": method, "url": target}})
						if gateErr != nil {
							return ctx, nil, gateErr
						}
						policy, _ = skill.ToolDispatchPolicy(ctx, "browser")
						if skill.CurrentExecutionPolicy(ctx) != policy {
							continue
						}
						ctx = skill.WithToolDispatchGrant(ctx, "browser", policy)
					}
					ctx, a, err = service.Begin(ctx, externaltools.Intent{Kind: "http_request", ToolName: "browser", TargetID: u.Hostname(), LogicalSuffix: method + ":" + target, Input: input, SafeRetry: readOnly}, readOnly)
					if err != nil {
						return ctx, nil, err
					}
					if a.Record.PolicyProfile != policy.Profile || a.Record.PolicyRevision != policy.Revision {
						err = errors.Join(externaltools.ErrPolicyChanged, service.Finish(a, "observed", "failed", "policy_changed_before_dispatch", nil, false))
					} else {
						err = service.Dispatch(ctx, a)
					}
					if !errors.Is(err, externaltools.ErrPolicyChanged) {
						break
					}
				}
				if err != nil {
					return ctx, nil, err
				}
				if a == nil {
					return ctx, nil, externaltools.ErrPolicyChanged
				}
				return ctx, func(result *skill.Result, requestErr error) error {
					phase, outcome, reason := "observed", "success", ""
					var detail *skill.ToolExecutionDetail
					if result != nil {
						detail = result.Detail
					}
					var executionErr *skill.ExecutionError
					if errors.As(requestErr, &executionErr) {
						detail = &executionErr.Detail
					}
					if requestErr != nil {
						outcome = "failed"
						reason = "http_request_failed"
					}
					if detail != nil {
						outcome = detail.Outcome
						reason = detail.Reason
					}
					if outcome == "unknown" {
						phase = "unknown"
					}
					facts := map[string]any{"method": method, "target_host": u.Hostname(), "detail": detail}
					if result != nil {
						if data, ok := result.Data.(map[string]any); ok {
							for _, key := range []string{"status", "bytes_read", "response_complete", "artifact", "pending_artifact", "transport_replay"} {
								if value, ok := data[key]; ok {
									facts[key] = value
								}
							}
						}
					}
					return service.Finish(a, phase, outcome, reason, facts, false)
				}, nil
			})
		}
	}
}
