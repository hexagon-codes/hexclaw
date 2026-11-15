package engineadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

const (
	accumulationMetadataPolicy  = "accumulation-metadata"
	accumulationMetadataVersion = "1"
)

// AccumulationMetadataGenerateFunc 是生产模型调用与受控结果解析之间的唯一装配缝。
type AccumulationMetadataGenerateFunc func(
	ctx context.Context,
	content string,
) (string, error)

// AccumulationMetadataAdapter 把模型 JSON 收敛为领域层允许的封闭分类与 provenance。
type AccumulationMetadataAdapter struct {
	generate       AccumulationMetadataGenerateFunc
	beforeSendHook bool
}

func NewAccumulationMetadataAdapter(
	generate AccumulationMetadataGenerateFunc,
) *AccumulationMetadataAdapter {
	return &AccumulationMetadataAdapter{generate: generate}
}

func (a *AccumulationMetadataAdapter) DeriveAccumulationMetadata(
	ctx context.Context,
	content string,
) (k12.AccumulationDerivedMetadata, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return k12.AccumulationDerivedMetadata{}, fmt.Errorf(
			"accumulation metadata adapter: content is required",
		)
	}
	if a == nil || a.generate == nil {
		return k12.AccumulationDerivedMetadata{}, fmt.Errorf(
			"accumulation metadata adapter: generator is not configured",
		)
	}
	raw, err := a.generate(ctx, content)
	if err != nil {
		return k12.AccumulationDerivedMetadata{}, fmt.Errorf(
			"accumulation metadata adapter: generation failed: %w",
			providerResponseError(err),
		)
	}
	if err := usecase.CaptureAccumulationMetadataRaw(ctx, raw); err != nil {
		return k12.AccumulationDerivedMetadata{}, err
	}
	return a.DecodeAccumulationMetadata(raw)
}

// WithBeforeSendHook 仅供已连接 SDK complete 发送前钩子的生产生成器显式声明能力。
func (a *AccumulationMetadataAdapter) WithBeforeSendHook() *AccumulationMetadataAdapter {
	a.beforeSendHook = true
	return a
}

// UsesAccumulationMetadataSendHook 普通生成器保持保守模式，不推断任意回调的发送边界。
func (a *AccumulationMetadataAdapter) UsesAccumulationMetadataSendHook() bool {
	return a != nil && a.beforeSendHook
}

// DecodeAccumulationMetadata 仅解释已有响应，恢复时不再次生成或调用模型。
func (a *AccumulationMetadataAdapter) DecodeAccumulationMetadata(raw string) (k12.AccumulationDerivedMetadata, error) {
	var response struct {
		Subject   *string `json:"subject"`
		EntryType *string `json:"entry_type"`
		Source    *string `json:"source"`
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return k12.AccumulationDerivedMetadata{}, fmt.Errorf(
			"accumulation metadata adapter: invalid response: %w", err,
		)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return k12.AccumulationDerivedMetadata{}, fmt.Errorf(
			"accumulation metadata adapter: response contains trailing content",
		)
	}
	if response.Subject == nil || response.EntryType == nil || response.Source == nil {
		return k12.AccumulationDerivedMetadata{}, fmt.Errorf(
			"accumulation metadata adapter: response fields are incomplete",
		)
	}
	provenance := k12.DerivationProvenance{
		Method: "model", Policy: accumulationMetadataPolicy,
		Version: accumulationMetadataVersion,
	}
	metadata := k12.AccumulationDerivedMetadata{
		Subject:             strings.TrimSpace(*response.Subject),
		EntryType:           strings.TrimSpace(*response.EntryType),
		Source:              strings.TrimSpace(*response.Source),
		SubjectProvenance:   provenance,
		EntryTypeProvenance: provenance,
	}
	if metadata.Source != "" {
		sourceProvenance := provenance
		metadata.SourceProvenance = &sourceProvenance
	}
	if err := metadata.Validate(); err != nil {
		return k12.AccumulationDerivedMetadata{}, fmt.Errorf(
			"accumulation metadata adapter: invalid derivation: %w", err,
		)
	}
	return metadata, nil
}

var _ usecase.AccumulationMetadataDeriver = (*AccumulationMetadataAdapter)(nil)
