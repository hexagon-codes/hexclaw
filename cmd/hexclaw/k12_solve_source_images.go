package main

import (
	"context"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/skill"
)

// k12SolveSourceAttachments 只桥接可信持久求解本轮的来源图，不向普通派生继承媒体。
func k12SolveSourceAttachments(ctx context.Context, spec engine.SubAgentSpec) []adapter.Attachment {
	if _, ok := k12.GradingModelSnapshotFromContext(ctx); !ok || spec.ProgramOnly || spec.Source != "solve" ||
		(spec.Agent != "solver" && spec.Agent != "verifier") {
		return nil
	}
	images := skill.InputImages(ctx)
	attachments := make([]adapter.Attachment, 0, len(images))
	for _, image := range images {
		attachments = append(attachments, adapter.Attachment{ID: image.ID, Type: "image", Name: image.Name,
			Mime: image.MIME, Data: image.Data, URL: image.URL})
	}
	return attachments
}
