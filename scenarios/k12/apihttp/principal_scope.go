package apihttp

import (
	"context"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

var errAgentScopeNotFound = errors.New("agent scope not found")

func (h *handler) ownerScope(ctx context.Context) (string, error) {
	switch strings.TrimSpace(h.rt.PrincipalMode) {
	case "local_loopback":
		ownerID := strings.TrimSpace(h.rt.OwnerScope)
		if ownerID == "" {
			return "", errors.New("local owner scope missing")
		}
		return ownerID, nil
	case "remote":
		if h.rt.AuthenticatedOwnerScope == nil {
			return "", errors.New("remote principal resolver missing")
		}
		ownerID, err := h.rt.AuthenticatedOwnerScope(ctx)
		if err != nil || strings.TrimSpace(ownerID) == "" {
			return "", errors.New("authenticated owner scope missing")
		}
		return strings.TrimSpace(ownerID), nil
	default:
		return "", errors.New("unsupported principal mode")
	}
}

// authorizedAgentOwnerScope resolves the server-owned principal and enforces
// the owner -> agent binding in remote mode. Authorization failures are
// deliberately collapsed to one sentinel so callers can return 404 without
// revealing whether either side of the binding exists.
func (h *handler) authorizedAgentOwnerScope(
	ctx context.Context,
	agentName string,
) (string, error) {
	ownerID, err := h.ownerScope(ctx)
	if err != nil {
		return "", err
	}
	agentName = strings.TrimSpace(agentName)
	if strings.TrimSpace(h.rt.PrincipalMode) != "remote" {
		return ownerID, nil
	}
	if h.rt.AuthorizeAgentScope == nil {
		return "", errAgentScopeNotFound
	}
	if err := h.rt.AuthorizeAgentScope(ctx, ownerID, agentName); err != nil {
		return "", errAgentScopeNotFound
	}
	return ownerID, nil
}

// 文字执行沿当前调用者的实际档案决定作用域；独立助手不借用其他学生，也不创建档案。
func (h *handler) authorizedTextSolveOwnerScope(ctx context.Context, agentName string) (string, error) {
	owner, err := h.ownerScope(ctx)
	if err != nil {
		return "", err
	}
	if h.rt.Records == nil {
		return "", errAgentScopeNotFound
	}
	learner, err := h.rt.Records.HasLearnerProfile(ctx, strings.TrimSpace(agentName))
	if err != nil {
		return "", err
	}
	if learner {
		return h.authorizedAgentOwnerScope(ctx, agentName)
	}
	return owner, nil
}

// 未选择孩子的辅导仅绑定当前调用者及请求，由工具执行器为每次实际派发登记自己的回执。
func (h *handler) tutorSolveContext(ctx context.Context, agent string) (context.Context, error) {
	if agent != "" {
		owner, err := h.authorizedTextSolveOwnerScope(ctx, agent)
		if err != nil {
			return ctx, err
		}
		return usecase.WithAuthorizedTextSolveOrigin(ctx, k12.TextSolveOrigin{OwnerID: owner}, agent), nil
	}
	owner, err := h.ownerScope(ctx)
	if err != nil {
		return ctx, err
	}
	ctx = skill.WithAuthenticatedUser(ctx, owner)
	identity := skill.CurrentInvocation(ctx)
	if identity.RequestID == "" {
		identity.RequestID = "tutor-" + idgen.NanoID()
		ctx = skill.WithInvocationContext(ctx, identity)
	}
	return ctx, nil
}

func (h *handler) textbookScope(
	ctx context.Context,
	agentName, subject string,
) (k12storage.TextbookScope, error) {
	ownerID, err := h.authorizedAgentOwnerScope(ctx, agentName)
	if err != nil {
		return k12storage.TextbookScope{}, err
	}
	agentName = strings.TrimSpace(agentName)
	subject = strings.TrimSpace(subject)
	return k12storage.TextbookScope{
		OwnerID: ownerID, AgentName: agentName, Subject: subject,
	}, nil
}
