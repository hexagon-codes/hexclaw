package builtin

import (
	"context"
	"testing"

	"github.com/hexagon-codes/toolkit/os/sandbox"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/skill"
)

func TestRegisterAdvancedUsesConfiguredCodeExecHostNetwork(t *testing.T) {
	requireCodeExecSandbox(t)
	allowed := true
	registry := skill.NewRegistry()
	deps := &SkillDeps{Workspace: t.TempDir()}

	RegisterAdvanced(registry, config.BuiltinConfig{
		CodeExec: true,
		CodeExecPolicy: config.CodeExecPolicyConfig{
			Network: &allowed,
		},
	}, deps)

	if deps.CodeExecSkill == nil {
		t.Fatal("code_exec was not initialized with the enabled network policy")
	}
	if _, exists := registry.Get("code_exec"); !exists {
		t.Fatal("code_exec was not registered with the enabled network policy")
	}
	cfg := codeExecConfigForTest(deps.CodeExecSkill)
	if cfg.Network != sandbox.NetworkHost || !deps.CodeExecSkill.SandboxPolicy().NetworkEnabled {
		t.Fatalf("registered network=%s policy=%+v, want host network", cfg.Network, deps.CodeExecSkill.SandboxPolicy())
	}
	if cfg.ExecutionProfile != sandbox.ExecutionProfileUntrusted || cfg.RequiredCapabilities&sandbox.UntrustedCodeIsolationCapabilities != sandbox.UntrustedCodeIsolationCapabilities {
		t.Fatalf("registered host network changed the untrusted execution contract: %+v", cfg)
	}
}

func TestPrepareSandboxPolicyConstructsConfiguredHostNetwork(t *testing.T) {
	s := NewCodeExecSkill(nil, sandbox.Config{
		Workspace: t.TempDir(),
		Network:   sandbox.NetworkDisabled,
	})
	factoryCalled := false
	var constructed sandbox.Config
	s.sandboxFactory = func(cfg sandbox.Config) (sandbox.Sandbox, error) {
		factoryCalled = true
		constructed = cfg
		return &mockSandbox{}, nil
	}

	candidate, err := s.PrepareSandboxPolicy(context.Background(), SandboxPolicy{NetworkEnabled: true})
	if err != nil || candidate == nil {
		t.Fatalf("PrepareSandboxPolicy candidate=%v error=%v, want valid host-network candidate", candidate, err)
	}
	defer candidate.Discard()
	if !factoryCalled || constructed.Network != sandbox.NetworkHost {
		t.Fatalf("host-network construction called=%v network=%s", factoryCalled, constructed.Network)
	}
	if constructed.ExecutionProfile != sandbox.ExecutionProfileUntrusted || constructed.RequiredCapabilities&sandbox.UntrustedCodeIsolationCapabilities != sandbox.UntrustedCodeIsolationCapabilities {
		t.Fatalf("host-network candidate changed the untrusted execution contract: %+v", constructed)
	}
	if s.SandboxPolicy().NetworkEnabled {
		t.Fatal("uncommitted host-network candidate changed the active policy")
	}
	candidate.Commit()
	if !s.SandboxPolicy().NetworkEnabled {
		t.Fatal("committed host-network candidate did not update the active policy")
	}
}

func TestCodeExecExecuteUsesConfiguredHostNetwork(t *testing.T) {
	s := NewCodeExecSkill(nil, sandbox.Config{
		Workspace: t.TempDir(),
		Network:   sandbox.NetworkHost,
	})
	factoryCalled := false
	var constructed sandbox.Config
	s.sandboxFactory = func(cfg sandbox.Config) (sandbox.Sandbox, error) {
		factoryCalled = true
		constructed = cfg
		return &mockSandbox{}, nil
	}

	result, err := s.Execute(context.Background(), map[string]any{
		"language": "python",
		"code":     "print('network policy')",
	})
	if err != nil || result == nil {
		t.Fatalf("Execute result=%#v error=%v, want successful configured execution", result, err)
	}
	if !factoryCalled || constructed.Network != sandbox.NetworkHost {
		t.Fatalf("host-network execution construction called=%v network=%s", factoryCalled, constructed.Network)
	}
	if constructed.ExecutionProfile != sandbox.ExecutionProfileUntrusted || constructed.RequiredCapabilities&sandbox.UntrustedCodeIsolationCapabilities != sandbox.UntrustedCodeIsolationCapabilities {
		t.Fatalf("host-network execution changed the untrusted execution contract: %+v", constructed)
	}
}

func TestPrepareSandboxPolicyKeepsDefaultOfflineBehavior(t *testing.T) {
	s := NewCodeExecSkill(nil, sandbox.Config{
		Workspace: t.TempDir(),
		Network:   sandbox.NetworkDisabled,
	})
	var constructed sandbox.NetworkMode
	s.sandboxFactory = func(cfg sandbox.Config) (sandbox.Sandbox, error) {
		constructed = cfg.Network
		return &mockSandbox{}, nil
	}

	candidate, err := s.PrepareSandboxPolicy(context.Background(), SandboxPolicy{})
	if err != nil {
		t.Fatalf("PrepareSandboxPolicy offline error = %v", err)
	}
	candidate.Commit()
	if constructed != sandbox.NetworkDisabled || s.SandboxPolicy().NetworkEnabled {
		t.Fatalf("offline policy constructed=%s active=%+v", constructed, s.SandboxPolicy())
	}
}
