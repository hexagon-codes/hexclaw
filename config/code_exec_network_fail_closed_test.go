package config

import "testing"

func TestValidateAcceptsCodeExecHostNetwork(t *testing.T) {
	cfg := DefaultConfig()
	enabled := true
	cfg.Skill.Builtin.CodeExecPolicy.Network = &enabled

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate host-network configuration: %v", err)
	}
	if !cfg.Skill.Builtin.CodeExecPolicy.CodeExecNetworkAllowed() {
		t.Fatal("validation changed the enabled network policy")
	}
}
