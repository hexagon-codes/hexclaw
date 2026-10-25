package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/skill"
)

var ErrFrozenImageReportUnavailable = errors.New("the latest image report has no frozen PDF; prepare its original report before resending")

type ConversationImageReport struct {
	Source   k12storage.ConversationImageReportSource
	Artifact k12.PrintReplyArtifact
	Content  string
}

// ReadConversationImageReport 读取实际图片来源已有的冻结报告，不执行识别、求解、渲染或投递。
// afterMessageID 非空时只返回比该已保存文字回复更新的图片来源。
func (d Deps) ReadConversationImageReport(ctx context.Context, invocation skill.InvocationContext, afterMessageID string) (ConversationImageReport, error) {
	var result ConversationImageReport
	if d.Records == nil || invocation.AuthenticatedUserID == "" || invocation.RoutedAgentName == "" {
		return result, records.ErrNotFound
	}
	if err := d.Records.ValidateTextSolveRequest(ctx, k12.TextSolveOrigin{SourceSessionID: invocation.SessionID, SourceMessageID: invocation.SourceMessageID}, invocation.AuthenticatedUserID, invocation.RequestID); err != nil {
		return result, err
	}
	source, err := d.Records.GetLatestConversationImageReportSource(ctx, k12storage.ConversationImageReportScope{UserID: invocation.AuthenticatedUserID, AgentName: invocation.RoutedAgentName, SessionID: invocation.SessionID, Platform: invocation.Platform, InstanceID: invocation.PlatformInstanceID, ChatID: invocation.ChatID, AfterMessageID: afterMessageID})
	if err != nil {
		return result, err
	}
	result.Source = source
	artifact, err := d.Records.GetFrozenGradingReportForJob(ctx, source.AgentName, source.JobID)
	if errors.Is(err, records.ErrNotFound) {
		return result, fmt.Errorf("%w: dispatch_id=%s", ErrFrozenImageReportUnavailable, source.DispatchID)
	}
	if err != nil {
		return result, err
	}
	ref, _, err := d.ResolvePrintReplyArtifact(ctx, source.AgentName, artifact.ArtifactID)
	if err != nil {
		return result, err
	}
	result.Artifact, result.Content = ref, artifact.CanonicalMarkdown
	return result, nil
}
