package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/engine"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	k12 "github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type tutorIdentityAgentStore interface {
	SaveAgent(context.Context, *agentrouter.AgentConfig) error
}

type k12TutorIdentityPolicy struct {
	router   *agentrouter.Dispatcher
	store    tutorIdentityAgentStore
	followup *usecase.Deps
}

func newK12TutorIdentityPolicy(
	router *agentrouter.Dispatcher,
	store tutorIdentityAgentStore,
) *k12TutorIdentityPolicy {
	return &k12TutorIdentityPolicy{router: router, store: store}
}

func (p *k12TutorIdentityPolicy) CompileTerminalDirective(
	ctx context.Context,
	input engine.AgentSystemPromptPolicyInput,
) (engine.AgentSystemPromptDirective, error) {
	directive, err := p.compileIdentity(ctx, input)
	if err != nil || directive.Content == "" || p.followup == nil {
		return directive, err
	}
	msg := input.Message
	conversation := k12storage.TutorConversationKey("desktop", "", msg.SessionID)
	if msg.Platform == adapter.PlatformDingtalk {
		conversation = k12storage.TutorConversationKey(string(msg.Platform), msg.InstanceID, msg.ChatID)
	}
	contextText, err := p.followup.TutorFollowupDirective(ctx, usecase.TutorFollowupInput{
		OwnerScope: usecase.DefaultLocalOwnerScope, AgentName: input.Agent.Name,
		ConversationKey: conversation, MessageID: msg.ID, ReplyTo: msg.ReplyTo,
		Query: input.UserQuery, HasAttachments: len(msg.Attachments) > 0,
	})
	if err != nil {
		return engine.AgentSystemPromptDirective{}, err
	}
	if strings.TrimSpace(contextText) != "" {
		return k12TutorDirective(directive.Content + "\n\n" + contextText), nil
	}
	return directive, nil
}

func (p *k12TutorIdentityPolicy) compileIdentity(
	ctx context.Context,
	input engine.AgentSystemPromptPolicyInput,
) (engine.AgentSystemPromptDirective, error) {
	if input.Agent.Metadata["scenario"] != k12TutorScenario {
		return engine.AgentSystemPromptDirective{}, nil
	}
	if input.Agent.Metadata[k12.MetaKeyPromptContractVersion] == k12.TutorIdentityPromptContractVersion {
		content, err := k12.CompileTutorIdentityDirective(input.Agent.Metadata)
		return k12TutorDirective(content), err
	}

	var content string
	shouldPersist := false
	err := p.router.UpdateAgentPersisted(input.Agent.Name,
		func(current agentrouter.AgentConfig) (agentrouter.AgentConfig, error) {
			if current.Metadata["scenario"] != k12TutorScenario {
				content = ""
				return current, nil
			}
			compiled, err := k12.CompileTutorIdentityDirective(current.Metadata)
			if err != nil {
				return current, err
			}
			content = compiled
			if current.Metadata[k12.MetaKeyPromptContractVersion] == k12.TutorIdentityPromptContractVersion {
				return current, nil
			}
			meta := make(map[string]string, len(current.Metadata)+1)
			for key, value := range current.Metadata {
				meta[key] = value
			}
			meta[k12.MetaKeyPromptContractVersion] = k12.TutorIdentityPromptContractVersion
			current.Metadata = meta
			shouldPersist = true
			return current, nil
		},
		func(updated *agentrouter.AgentConfig) error {
			if !shouldPersist || p.store == nil {
				return nil
			}
			return p.store.SaveAgent(ctx, updated)
		},
	)
	if err != nil {
		return engine.AgentSystemPromptDirective{}, err
	}
	if content == "" {
		return engine.AgentSystemPromptDirective{}, nil
	}
	return k12TutorDirective(content), nil
}

// 档案变化即改变缓存身份，避免同一句追问沿用旧孩子或旧课程的回答。
func k12TutorDirective(content string) engine.AgentSystemPromptDirective {
	sum := sha256.Sum256([]byte(content))
	return engine.AgentSystemPromptDirective{
		Key: k12.TutorIdentityPromptContractVersion + ":" + hex.EncodeToString(sum[:]), Content: content,
	}
}
