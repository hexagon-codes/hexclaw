package main

import (
	"context"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/curriculum"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type tutorIdentitySavingStore struct {
	saves []agentrouter.AgentConfig
}

func (s *tutorIdentitySavingStore) SaveAgent(_ context.Context, cfg *agentrouter.AgentConfig) error {
	s.saves = append(s.saves, *cfg)
	return nil
}

func TestK12TutorIdentityPolicy_StampsVersionWithoutRewritingSystemPrompt(t *testing.T) {
	dispatcher := agentrouter.New()
	store := &tutorIdentitySavingStore{}
	const (
		agentName    = "k12-tutor-xiaoming"
		customPrompt = "我的自定义教学法；保留老师布置作业的现实语义。"
	)
	if err := dispatcher.Register(agentrouter.AgentConfig{
		Name: agentName, SystemPrompt: customPrompt,
		Metadata: map[string]string{
			"scenario": k12TutorScenario, k12.MetaKeyChildName: "小明",
		},
	}); err != nil {
		t.Fatalf("注册 agent: %v", err)
	}
	policy := newK12TutorIdentityPolicy(dispatcher, store)

	cfg, _ := dispatcher.GetAgent(agentName)
	directive, err := policy.CompileTerminalDirective(context.Background(),
		engine.AgentSystemPromptPolicyInput{Agent: *cfg, UserQuery: "你是谁？"})
	if err != nil {
		t.Fatalf("CompileTerminalDirective: %v", err)
	}
	if !strings.HasPrefix(directive.Key, k12.TutorIdentityPromptContractVersion+":") || directive.Content == "" {
		t.Fatalf("directive 不完整：%+v", directive)
	}
	updated, _ := dispatcher.GetAgent(agentName)
	if updated.Metadata[k12.MetaKeyPromptContractVersion] != k12.TutorIdentityPromptContractVersion {
		t.Fatalf("未写入 contract version：%#v", updated.Metadata)
	}
	if updated.SystemPrompt != customPrompt {
		t.Fatalf("metadata stamp 不得改写 SystemPrompt：%q", updated.SystemPrompt)
	}
	if len(store.saves) != 1 || store.saves[0].SystemPrompt != customPrompt {
		t.Fatalf("持久化必须一次且保留 SystemPrompt：%#v", store.saves)
	}

	fresh, _ := dispatcher.GetAgent(agentName)
	if _, err := policy.CompileTerminalDirective(context.Background(),
		engine.AgentSystemPromptPolicyInput{Agent: *fresh, UserQuery: "介绍下你"}); err != nil {
		t.Fatalf("重复编译: %v", err)
	}
	if len(store.saves) != 1 {
		t.Fatalf("版本 stamp 必须幂等，保存次数=%d", len(store.saves))
	}
}

func TestK12TutorIdentityPolicy_MissingChildDoesNotStamp(t *testing.T) {
	dispatcher := agentrouter.New()
	store := &tutorIdentitySavingStore{}
	const agentName = "k12-tutor-missing-child"
	if err := dispatcher.Register(agentrouter.AgentConfig{
		Name: agentName, SystemPrompt: "custom",
		Metadata: map[string]string{"scenario": k12TutorScenario},
	}); err != nil {
		t.Fatalf("注册 agent: %v", err)
	}
	policy := newK12TutorIdentityPolicy(dispatcher, store)
	cfg, _ := dispatcher.GetAgent(agentName)
	if _, err := policy.CompileTerminalDirective(context.Background(),
		engine.AgentSystemPromptPolicyInput{Agent: *cfg}); err == nil {
		t.Fatal("缺少 child_name 必须失败")
	}
	if len(store.saves) != 0 {
		t.Fatalf("失败时不得 stamp，保存次数=%d", len(store.saves))
	}
}

func TestK12TutorIdentityPolicyMathScopeUsesRegisteredProfileAndCurrentCurriculum(t *testing.T) {
	dispatcher := agentrouter.New()
	const agentName = "registered-math-child"
	if err := dispatcher.Register(agentrouter.AgentConfig{
		Name: agentName, Metadata: map[string]string{
			k12.MetaKeyScenario: k12TutorScenario, k12.MetaKeyChildName: "学生",
			k12.MetaKeyGradeTerm: "六年级上", k12.MetaKeyTextbookMath: "人教版", k12.MetaKeyTextbook: "other edition",
		},
	}); err != nil {
		t.Fatal(err)
	}
	policy := newK12TutorIdentityPolicy(dispatcher, nil)
	policy.followup = &usecase.Deps{Constraint: curriculum.New()}
	registered, _ := dispatcher.GetAgent(agentName)
	input := engine.AgentSystemPromptPolicyInput{Agent: *registered, UserQuery: "解方程：0.25x+0.5x=18"}
	input.Message.Metadata = map[string]string{k12.MetaKeyGradeTerm: "初一下", k12.MetaKeyTextbookMath: "untrusted edition"}
	directive, err := policy.CompileTerminalDirective(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	points := strings.Split(directive.FrozenMathConstraint, "、")
	if !engine.ElementaryEquationWithinCurriculum(input.UserQuery, points) || strings.Contains(directive.FrozenMathConstraint, "解方程组") {
		t.Fatalf("registered elementary scope was replaced: %q", directive.FrozenMathConstraint)
	}
	if registered.Metadata[k12.MetaKeyGradeTerm] != "六年级上" || registered.Metadata[k12.MetaKeyTextbookMath] != "人教版" {
		t.Fatal("curriculum read changed the registered profile")
	}
	policy.followup.Constraint = curriculum.NewFromEntries("人教版", []curriculum.Entry{
		{KnowledgePoint: "解方程", GradeTerm: "五年级上"},
	})
	changed, err := policy.CompileTerminalDirective(t.Context(), input)
	if err != nil || changed.Content != directive.Content || changed.Key == directive.Key || changed.FrozenMathConstraint != "解方程" {
		t.Fatalf("curriculum-only change did not change cache identity: changed=%+v err=%v", changed, err)
	}
}

func TestK12TutorIdentityPolicyMathScopeDoesNotInventMissingCourseBasis(t *testing.T) {
	policy := newK12TutorIdentityPolicy(agentrouter.New(), nil)
	policy.followup = &usecase.Deps{Constraint: curriculum.New()}
	for _, test := range []struct {
		name, scenario, grade, edition string
	}{
		{name: "non_k12", scenario: "other", grade: "六年级上", edition: "人教版"},
		{name: "missing_grade", scenario: k12TutorScenario, edition: "人教版"},
		{name: "secondary_grade", scenario: k12TutorScenario, grade: "初一下", edition: "人教版"},
		{name: "missing_edition", scenario: k12TutorScenario, grade: "六年级上"},
		{name: "different_edition", scenario: k12TutorScenario, grade: "六年级上", edition: "苏教版"},
		{name: "similar_edition", scenario: k12TutorScenario, grade: "六年级上", edition: "人教版（实验版）"},
	} {
		t.Run(test.name, func(t *testing.T) {
			constraint, err := policy.frozenMathConstraint(t.Context(), agentrouter.AgentConfig{Metadata: map[string]string{
				k12.MetaKeyScenario: test.scenario, k12.MetaKeyGradeTerm: test.grade, k12.MetaKeyTextbookMath: test.edition,
			}})
			if err != nil || constraint != "" {
				t.Fatalf("missing course basis was inferred: scope=%q err=%v", constraint, err)
			}
		})
	}
	constraint, err := policy.frozenMathConstraint(t.Context(), agentrouter.AgentConfig{Metadata: map[string]string{
		k12.MetaKeyScenario: k12TutorScenario, k12.MetaKeyGradeTerm: "四年级下", k12.MetaKeyTextbookMath: "人教版",
	}})
	if err != nil || engine.ElementaryEquationWithinCurriculum("解方程：0.25x+0.5x=18", strings.Split(constraint, "、")) {
		t.Fatalf("lower registered grade received equation scope: scope=%q err=%v", constraint, err)
	}
}
