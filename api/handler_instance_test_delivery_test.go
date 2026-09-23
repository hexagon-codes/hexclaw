package api

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/instances"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
)

func TestBoundInstanceTestTargetsUseCurrentValidBindings(t *testing.T) {
	mgr, cleanup := newTestInstanceManager(t)
	defer cleanup()
	router := agentrouter.New()
	rules := []agentrouter.Rule{
		{Platform: "slack", InstanceID: "current", ChatID: "chat", AgentName: "tutor"},
		{Platform: "slack", InstanceID: "main", ChatID: "chat", AgentName: "tutor"},
		{Platform: "slack", InstanceID: "current", ChatID: "group", AgentName: "tutor"},
		{Platform: "slack", InstanceID: "other", ChatID: "wrong-instance", AgentName: "tutor"},
		{Platform: "feishu", InstanceID: "current", ChatID: "wrong-platform", AgentName: "tutor"},
		{Platform: "slack", InstanceID: "current", ChatID: "deleted-agent", AgentName: "deleted"},
		{Platform: "slack", InstanceID: "current", UserID: "user-only", AgentName: "tutor"},
		{Platform: "slack", ChatID: "ambiguous-instance", AgentName: "tutor"},
	}
	router.LoadAll([]agentrouter.AgentConfig{{Name: "tutor"}}, "tutor", rules)
	server := &Server{instanceMgr: mgr, agentRouter: router}
	targets := server.boundInstanceTestTargets(&instances.Instance{ID: "current", Name: "main", Provider: "slack", Enabled: true})
	if len(targets) != 2 || targets[0] != "chat" || targets[1] != "group" {
		t.Fatalf("unexpected targets: %v", targets)
	}
}
