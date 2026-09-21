package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	hexagon "github.com/hexagon-codes/hexagon"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/memory"
)

// 画像蒸馏只综合当前源事实，单独维护常驻摘要；显式编辑通过关联修正同步源事实。

// profileSynthMaxTokens 给推理模型留足额度：reasoning 模型把思考放 reasoning_content，
// 额度太低会在思考阶段耗尽 → content 空（见 AP-098）。2000 留出思考 + 画像正文空间。
const profileSynthMaxTokens = 2000

const profileSynthSystemPrompt = `你是一个用户画像蒸馏器。把零碎的事实合成为一段稳定、紧凑的中文画像，**只描述当前软件使用者本人**。

严格规则：
- 只综合下面列出的【已知事实】，绝不杜撰、不推测、不添加任何未列出的信息。
- **只刻画当前使用者本人**。事实里若提到**其他具名人物**（第三方，如某人姓名 + 其头衔/简介/特质——
  往往是使用者让你记住的资料或彩蛋），**忽略这些人物事实**，绝不把他们的身份、头衔、特质安到使用者
  头上（如"某某是公司创始人"、"某某偏爱X"这类描述别人的句子，不得写进画像）。
  分不清某条事实说的是使用者还是第三方时，宁可略去该条。
- 用户明确事实或人工修正优先于自动推断；冲突的旧自动说法不得作为当前事实输出。
- 若与【现有画像】有时序矛盾（如"要去X"已变为"已去X"、住址/状态变更），以最新事实为准更新表述。
- 输出一段或几条短句，紧凑可读；不要解释说明、不要前后缀、不要 markdown 标题。
- 若已知事实不足以形成有意义的画像，直接输出空。`

// llmProfileSynthesizer 用 ReActEngine 的无状态 completion 合成画像。
type llmProfileSynthesizer struct {
	eng *ReActEngine
}

// NewProfileSynthesizer 返回基于引擎当前路由 LLM 的画像蒸馏器（供 main.go 接入 StartProfileDistillation）。
func NewProfileSynthesizer(eng *ReActEngine) memory.ProfileSynthesizer {
	return &llmProfileSynthesizer{eng: eng}
}

func (s *llmProfileSynthesizer) Synthesize(ctx context.Context, facts []string, prevProfile string) (string, error) {
	if len(facts) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("【已知事实】\n")
	for _, f := range facts {
		b.WriteString("- ")
		b.WriteString(f)
		b.WriteByte('\n')
	}
	if p := strings.TrimSpace(prevProfile); p != "" {
		b.WriteString("\n【现有画像】\n")
		b.WriteString(p)
		b.WriteByte('\n')
	}
	b.WriteString("\n请输出更新后的用户画像：")
	return s.complete(ctx, profileSynthSystemPrompt, b.String())
}

// 画像维护使用持久回执；明确拒绝可以切换既有候选，结果未知不能重发。
func (s *llmProfileSynthesizer) complete(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, localMemoryExtractTimeout)
	defer cancel()
	ctx = egress.WithRequest(ctx, egress.PurposeMemoryProfile, "profile-maintenance", egress.ClassGeneral, egress.ClassMemory)
	provider, name, err := s.eng.selectLLMForMemory(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", memory.ErrProfileRejected, err)
	}
	temperature := 0.3
	request := hexagon.CompletionRequest{Messages: []hexagon.Message{{Role: "system", Content: system}, {Role: "user", Content: user}}, MaxTokens: profileSynthMaxTokens, Temperature: &temperature}
	var tried []string
	for {
		tried = append(tried, name)
		response, callErr := provider.Complete(ctx, request)
		if callErr == nil {
			return strings.TrimSpace(response.Content), nil
		}
		var providerErr *llm.ProviderError
		if !errors.As(callErr, &providerErr) {
			return "", callErr
		}
		switch providerErr.StatusCode {
		case 400, 401, 403, 404, 422, 429:
			// 明确拒绝没有生成结果；只使用既有候选路由，不修改用户默认模型。
			provider, name, err = s.eng.router.Fallback(tried...)
			if err != nil {
				return "", fmt.Errorf("%w: %v", memory.ErrProfileRejected, callErr)
			}
		default:
			return "", callErr
		}
	}
}

func (s *llmProfileSynthesizer) PlanProfileEdit(ctx context.Context, facts []memory.MemoryEntry, before, after string) (memory.ProfileEditPlan, error) {
	input, err := json.Marshal(map[string]any{"sources": facts, "before_profile": before, "edited_profile": after})
	if err != nil {
		return memory.ProfileEditPlan{}, err
	}
	output, err := s.complete(ctx, `把用户对画像的修改映射为原始记忆中的明确修正。原始记忆是事实来源，不得猜测删除或添加用户没写的事实。
只输出 JSON: {"ambiguous":false,"changes":[{"id":"源条目ID","before":"完整原条目正文","old_span":"原画像和源条目共同包含的待修正逐字片段","new_span":"编辑画像中的逐字替换片段"}]}。
只改明确变化的片段，保留未涉及的其他源内容。删除明确片段使用空 new_span；新增明确事实使用空 id/before/old_span，new_span 为编辑画像新增的逐字事实，并给 type=fact/preference/identity/context。
纯措辞修改且事实不变可以 changes=[]。任何事实变化无法可靠关联、涉及第三方或需要猜测时返回 ambiguous=true，不输出删除建议。
必须覆盖用户的所有明确事实变更；遗漏或来源互相矛盾也返回 ambiguous=true。JSON 字符串需要正确转义。`, string(input))
	if err != nil {
		return memory.ProfileEditPlan{}, err
	}
	output = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(output, "```json"), "```"), "```"))
	var plan memory.ProfileEditPlan
	if err := json.Unmarshal([]byte(output), &plan); err != nil {
		return plan, errors.New("Profile correction returned an invalid result")
	}
	return plan, nil
}

func (e *ReActEngine) RefreshMemoryProfile(ctx context.Context) (string, error) {
	if e.fileMem == nil {
		return "", errors.New("Memory is unavailable")
	}
	return e.fileMem.DistillProfileForRole(ctx, "", &llmProfileSynthesizer{eng: e}, memory.DistillProfileConfig{RetryRejected: true}, time.Now())
}

func (e *ReActEngine) EditMemoryProfile(ctx context.Context, revision, content string) error {
	if e.fileMem == nil {
		return errors.New("Memory is unavailable")
	}
	return e.fileMem.EditProfile(ctx, revision, content, &llmProfileSynthesizer{eng: e})
}
