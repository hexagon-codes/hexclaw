package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/toolkit/util/idgen"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

const accumulationMetadataCommandPolicy = "accumulation-metadata-v1"

type accumulationMetadataHooksKey struct{}

type accumulationMetadataHooks struct {
	frozen func() (k12.GradingModelSnapshot, string, bool, error)
	freeze func(k12.GradingModelSnapshot, string) error
	sent   func(context.Context) error
	raw    func(string) error
}

// AccumulationMetadataFrozenRequestFromContext 读取本命令已冻结的真实路由和请求，避免恢复时跟随默认配置。
func AccumulationMetadataFrozenRequestFromContext(ctx context.Context) (k12.GradingModelSnapshot, string, bool, error) {
	hooks, _ := ctx.Value(accumulationMetadataHooksKey{}).(*accumulationMetadataHooks)
	if hooks == nil {
		return k12.GradingModelSnapshot{}, "", false, nil
	}
	return hooks.frozen()
}

// FreezeAccumulationMetadataRequest 在发送前冻结实际使用的路由和完整请求，不保存凭据。
func FreezeAccumulationMetadataRequest(ctx context.Context, route k12.GradingModelSnapshot, requestJSON string) error {
	hooks, _ := ctx.Value(accumulationMetadataHooksKey{}).(*accumulationMetadataHooks)
	if hooks == nil {
		return nil
	}
	return hooks.freeze(route, requestJSON)
}

// BeforeAccumulationMetadataSend 由 SDK 的 complete 发送前钩子调用，持久回执失败便不发送。
func BeforeAccumulationMetadataSend(ctx context.Context) error {
	hooks, _ := ctx.Value(accumulationMetadataHooksKey{}).(*accumulationMetadataHooks)
	if hooks == nil {
		return nil
	}
	if err := hooks.sent(ctx); err != nil {
		return errors.Join(egress.ErrProviderNotSent, err)
	}
	return nil
}

// CaptureAccumulationMetadataRaw 先保留完整模型响应，再由原有封闭解析器解释。
func CaptureAccumulationMetadataRaw(ctx context.Context, raw string) error {
	hooks, _ := ctx.Value(accumulationMetadataHooksKey{}).(*accumulationMetadataHooks)
	if hooks == nil {
		return nil
	}
	return hooks.raw(raw)
}

func (d Deps) deriveCurrentAccumulationMetadata(ctx context.Context, agent, content, key, digest string) (k12.AccumulationDerivedMetadata, string, error) {
	var zero k12.AccumulationDerivedMetadata
	command, err := d.Records.PrepareAccumulationMetadataCommand(ctx, k12storage.AccumulationMetadataCommand{
		AgentName: agent, CommandKey: key, RequestDigest: digest,
		GradeTerm: d.creationGradeTerm(ctx, agent, ""), PolicyVersion: accumulationMetadataCommandPolicy,
		CreatedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return zero, "", err
	}
	if command.PolicyVersion != accumulationMetadataCommandPolicy {
		return zero, "", fmt.Errorf("%w: accumulation metadata policy is unavailable", ErrModelInvocationRequiresReconciliation)
	}
	if command.State == "metadata_ready" {
		var metadata k12.AccumulationDerivedMetadata
		if err := json.Unmarshal([]byte(command.MetadataJSON), &metadata); err != nil {
			return zero, "", err
		}
		return metadata, command.GradeTerm, metadata.Validate()
	}
	if command.State == "raw_received" {
		decoder, ok := d.AccumulationMetadata.(interface {
			DecodeAccumulationMetadata(string) (k12.AccumulationDerivedMetadata, error)
		})
		if !ok {
			return zero, "", fmt.Errorf("%w: accumulation metadata decoder is unavailable", ErrModelInvocationRequiresReconciliation)
		}
		metadata, err := decoder.DecodeAccumulationMetadata(command.RawResponse)
		if err != nil {
			writeErr := d.Records.FailAccumulationMetadataCommand(ctx, command, false, "invalid_response", time.Now().UnixMilli())
			return zero, "", errors.Join(ErrSolveFailed, err, writeErr)
		}
		return d.saveAccumulationMetadataResult(ctx, command, metadata)
	}
	if d.AccumulationMetadata == nil {
		return zero, "", fmt.Errorf("%w: accumulation metadata deriver is not configured", ErrSolveFailed)
	}
	command, claimed, err := d.Records.ClaimAccumulationMetadataCommand(ctx, command, idgen.NanoID(), time.Now().UnixMilli(), time.Now().Add(30*time.Second).UnixMilli())
	if err != nil {
		return zero, "", err
	}
	if !claimed {
		return zero, "", ErrModelInvocationRequiresReconciliation
	}
	cctx, cancel := context.WithDeadline(ctx, time.UnixMilli(command.LeaseExpiresAt))
	defer cancel()
	hooks := &accumulationMetadataHooks{
		frozen: func() (k12.GradingModelSnapshot, string, bool, error) {
			if command.RouteSnapshotJSON == "" && command.RequestPolicyJSON == "" {
				return k12.GradingModelSnapshot{}, "", false, nil
			}
			var route k12.GradingModelSnapshot
			if err := json.Unmarshal([]byte(command.RouteSnapshotJSON), &route); err != nil {
				return route, "", false, err
			}
			return route, command.RequestPolicyJSON, true, nil
		},
		freeze: func(route k12.GradingModelSnapshot, request string) error {
			raw, err := json.Marshal(route)
			if err != nil {
				return err
			}
			return d.Records.FreezeAccumulationMetadataRequest(cctx, command, string(raw), request, time.Now().UnixMilli())
		},
		sent: func(sendCtx context.Context) error {
			if err := sendCtx.Err(); err != nil {
				return err
			}
			return d.Records.MarkAccumulationMetadataSent(sendCtx, command, time.Now().UnixMilli())
		},
		raw: func(raw string) error {
			persistCtx, persistCancel := accumulationMetadataPersistContext(ctx)
			defer persistCancel()
			digest := sha256.Sum256([]byte(raw))
			return d.Records.SaveAccumulationMetadataRaw(persistCtx, command, raw, hex.EncodeToString(digest[:]), time.Now().UnixMilli())
		},
	}
	cctx = context.WithValue(cctx, accumulationMetadataHooksKey{}, hooks)
	// 旧派生端口没有 SDK 发送边界时保守登记；生产适配器始终由实际 HTTP 钩子登记。
	if sender, ok := d.AccumulationMetadata.(interface{ UsesAccumulationMetadataSendHook() bool }); !ok || !sender.UsesAccumulationMetadataSendHook() {
		if err := hooks.sent(cctx); err != nil {
			persistCtx, persistCancel := accumulationMetadataPersistContext(ctx)
			defer persistCancel()
			writeErr := d.Records.FailAccumulationMetadataCommand(persistCtx, command, false, "provider_not_sent", time.Now().UnixMilli())
			return zero, "", errors.Join(egress.ErrProviderNotSent, err, writeErr)
		}
	}
	metadata, callErr := d.AccumulationMetadata.DeriveAccumulationMetadata(cctx, content)
	persistCtx, persistCancel := accumulationMetadataPersistContext(ctx)
	defer persistCancel()
	if callErr == nil {
		return d.saveAccumulationMetadataResult(persistCtx, command, metadata)
	}
	latest, readErr := d.Records.GetAccumulationMetadataCommand(persistCtx, agent, key)
	if readErr != nil {
		return zero, "", errors.Join(ErrModelInvocationRequiresReconciliation, callErr, readErr)
	}
	if latest.State == "raw_received" {
		if decoder, ok := d.AccumulationMetadata.(interface {
			DecodeAccumulationMetadata(string) (k12.AccumulationDerivedMetadata, error)
		}); ok {
			if _, decodeErr := decoder.DecodeAccumulationMetadata(latest.RawResponse); decodeErr != nil {
				writeErr := d.Records.FailAccumulationMetadataCommand(persistCtx, command, false, "invalid_response", time.Now().UnixMilli())
				return zero, "", errors.Join(ErrSolveFailed, callErr, writeErr)
			}
		}
		return zero, "", fmt.Errorf("%w: accumulation metadata response: %v", ErrSolveFailed, callErr)
	}
	unknown := latest.State == "sent" && sentProviderOutcomeUnknown(callErr, cctx.Err())
	failure := "provider_failed"
	if latest.State == "prepared" {
		failure = "provider_not_sent"
	} else if status, ok := definitiveProviderResponseStatus(callErr); ok {
		failure = fmt.Sprintf("provider_http_%d", status)
	}
	writeErr := d.Records.FailAccumulationMetadataCommand(persistCtx, command, unknown, failure, time.Now().UnixMilli())
	if unknown || writeErr != nil {
		return zero, "", errors.Join(ErrModelInvocationRequiresReconciliation, callErr, writeErr)
	}
	return zero, "", fmt.Errorf("%w: accumulation metadata generation: %v", ErrSolveFailed, callErr)
}

func accumulationMetadataPersistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (d Deps) saveAccumulationMetadataResult(ctx context.Context, command k12storage.AccumulationMetadataCommand, metadata k12.AccumulationDerivedMetadata) (k12.AccumulationDerivedMetadata, string, error) {
	metadata.Subject = strings.TrimSpace(metadata.Subject)
	metadata.EntryType = strings.TrimSpace(metadata.EntryType)
	metadata.Source = strings.TrimSpace(metadata.Source)
	if err := metadata.Validate(); err != nil {
		writeErr := d.Records.FailAccumulationMetadataCommand(ctx, command, false, "invalid_metadata", time.Now().UnixMilli())
		return metadata, "", errors.Join(ErrSolveFailed, err, writeErr)
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return metadata, "", err
	}
	if err := d.Records.SaveAccumulationMetadataResult(ctx, command, string(raw), time.Now().UnixMilli()); err != nil {
		return metadata, "", errors.Join(ErrModelInvocationRequiresReconciliation, err)
	}
	return metadata, command.GradeTerm, nil
}
