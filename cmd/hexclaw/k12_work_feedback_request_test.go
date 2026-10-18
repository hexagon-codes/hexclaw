package main

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestK12WritingFeedbackCompletionRequestKeepsSystemAndTaskConcise(t *testing.T) {
	for _, tc := range []struct {
		name, content, task string
		partial, oldDisk    bool
	}{
		{name: "complete_embedded", content: "我的爸爸很忙，他每天晚上陪我读书。"},
		{name: "old_disk_request_contract", content: "我的爸爸很忙，他每天晚上陪我读书。", oldDisk: true},
		{name: "quoted_reference_and_negation", content: "老师说：“不要照抄家长参考稿。”", task: "不要整篇重写"},
		{name: "partial_same_contract", content: "春天到了。[无法识别]小鸟在唱歌。", partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var captured hexagon.CompletionRequest
			calls := 0
			options := []engineadapter.SolveAdapterOption{engineadapter.WithWorkFeedbackGen(func(_ context.Context, subject, prompt, grade string) (string, error) {
				if subject != "语文" {
					t.Fatalf("writing was routed as %q", subject)
				}
				captured = k12WritingFeedbackCompletionRequest("current-model", prompt, grade)
				calls++
				return "controlled feedback", nil
			})}
			if tc.oldDisk {
				options = append(options, engineadapter.WithWorkFeedbackSkillLoader(func(string) (string, error) {
					return "---\nname: writing-feedback\nversion: \"9.9.9\"\nschema_version: 1\n---\n# 方法\n家长参考与原稿分开，不打分。旧输出要求：提供完整参考稿。", nil
				}))
			}
			generator := engineadapter.NewSolveAdapter(nil, options...)
			out, err := generator.GenerateWorkFeedback(context.Background(), usecase.WorkFeedbackRequest{
				WorkType: k12.WorkTypeWriting, ContentMarkdown: tc.content, Task: tc.task,
				Grade: "六年级", PartialContent: tc.partial,
			})
			if err != nil || calls != 1 || out.ProjectionContract != k12.WorkFeedbackProjectionWritingConciseV1 {
				t.Fatalf("adapter/request assembly failed: calls=%d out=%+v err=%v", calls, out, err)
			}
			if captured.Model != "current-model" || captured.MaxTokens != 1536 || captured.Temperature == nil || *captured.Temperature != 0.3 ||
				len(captured.Messages) != 2 || captured.Messages[0].Role != hexagon.RoleSystem || captured.Messages[1].Role != hexagon.RoleUser {
				t.Fatalf("completion routing or budget drift: %+v", captured)
			}
			system, user := captured.Messages[0].Content, captured.Messages[1].Content
			for _, clause := range []string{"1～2处局部示范", "不输出完整参考稿", "不整篇重写", "否定句不能反向理解为请求", "不编造经历", "不成人化", "只点评可靠片段"} {
				if !strings.Contains(system, clause) {
					t.Fatalf("actual system contract missing %q: %s", clause, system)
				}
			}
			if strings.Contains(system, "提供家长参考改句与完整参考稿") || strings.Contains(system, "再给完整家长参考稿") {
				t.Fatal("higher-priority system still forces a full reference essay")
			}
			if !strings.Contains(user, tc.content) || !strings.HasSuffix(user, "（点评口径贴合六年级孩子的水平，用家长和孩子都能懂的话。）") ||
				!strings.Contains(user, "不要整篇重写或输出完整参考稿") || !strings.Contains(user, "## 局部示范") {
				t.Fatalf("source, grade or final user contract changed: %s", user)
			}
			if tc.partial && !strings.Contains(user, "只点评下文可辨认片段") {
				t.Fatal("partial evidence lost its limited scope")
			}
			if tc.oldDisk && (out.SkillStamp != "writing-feedback@9.9.9/disk" || !strings.Contains(user, "不采用技能中的输出信封或完整参考稿要求")) {
				t.Fatal("old disk methods replaced the automatic output contract")
			}
		})
	}
}
