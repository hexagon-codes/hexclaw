package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
	"github.com/hexagon-codes/hexclaw/storage"
)

func registerSessionPersona(t *testing.T, reg *skill.DefaultRegistry, name, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: "+name+"\ntags: [persona]\n---\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	md, err := marketplace.LoadSkillFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(marketplace.WrapAsSkill(md)); err != nil {
		t.Fatal(err)
	}
}

func TestSessionPersonaOrdinaryProcessAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "process", true: "stream"}[stream], func(t *testing.T) {
			eng, provider, store, _, reg, _ := newInheritanceFixture(t, false)
			registerSessionPersona(t, reg, "role-one", "ROLE_ONE_BODY: you are the fictional character River.")
			registerSessionPersona(t, reg, "role-two", "ROLE_TWO_BODY: you are the fictional character Sky.")
			send := func(id, mounts, want string) {
				t.Helper()
				provider.mu.Lock()
				provider.seen = nil
				provider.mu.Unlock()
				msg := &adapter.Message{ID: id, UserID: "owner", Platform: adapter.PlatformAPI, SessionID: "persona-session", Content: "介绍下你", Metadata: map[string]string{"provider": "test", "model": "mock-model", "memory": "off", "request_id": id}}
				if mounts != "" {
					msg.Metadata["skills"] = mounts
				}
				if stream {
					ch, err := eng.ProcessStream(t.Context(), msg)
					if err != nil {
						t.Fatal(err)
					}
					for c := range ch {
						if c.Error != nil {
							t.Fatal(c.Error)
						}
					}
				} else {
					if _, err := eng.Process(t.Context(), msg); err != nil {
						t.Fatal(err)
					}
				}
				if !provider.sawText(want) {
					t.Fatalf("missing expected context %q", want)
				}
				if msg.Metadata["memory"] != "off" {
					t.Fatal("memory=off changed")
				}
			}
			send("first", "role-one", "ROLE_ONE_BODY")
			session, err := store.GetSession(t.Context(), "persona-session")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(session.Meta, `"persona_skill":"role-one"`) {
				t.Fatalf("binding=%s", session.Meta)
			}
			send("followup", "", "ROLE_ONE_BODY")
			send("task", "configured-method", "CONFIGURED_METHOD_BODY")
			if !provider.sawText("ROLE_ONE_BODY") {
				t.Fatal("task removed bound persona")
			}
			send("switch", "role-two", "ROLE_TWO_BODY")
			if err := store.SetSessionPersona(t.Context(), "persona-session", ""); err != nil {
				t.Fatal(err)
			}
			send("cancel", "", defaultSoul)
		})
	}
}

func TestSessionPersonaPromptIdentityAndExecutionBoundaries(t *testing.T) {
	eng, _, store, _, reg, _ := newInheritanceFixture(t, false)
	registerSessionPersona(t, reg, "role-one", "ROLE_IDENTITY_BODY: you are the fictional character River.")
	meta := map[string]string{"skills": "role-one", "model": "mock-model", "provider": "test", "user_locale": "zh-CN", "memory": "off"}
	ctx := eng.prepareSkillPromptContext(context.Background(), &adapter.Message{Metadata: meta})
	messages := eng.buildStreamMessages(ctx, "", []hexagon.Message{{Role: "assistant", Content: "Old default identity"}}, "", "你真实使用的模型是什么？", meta, nil)
	system := messages[0].Content
	if strings.Contains(system, defaultSoul) || !strings.Contains(system, "ROLE_IDENTITY_BODY") || !strings.Contains(system, "mock-model") || !strings.Contains(system, "只有用户明确询问真实模型") || !strings.Contains(system, "用户语言设置为中文") {
		t.Fatalf("identity context=%s", system)
	}
	var receipts []skill.ContextReceipt
	if err := json.Unmarshal([]byte(meta[skillContextReceiptsMetadataKey]), &receipts); err != nil || len(receipts) != 1 || receipts[0].Status != "loaded" {
		t.Fatalf("receipts=%v err=%v", receipts, err)
	}
	// 旧会话默认身份保留为转录，当前系统角色有明确的独立身份。
	if messages[1].Content != "Old default identity" {
		t.Fatal("history was rewritten")
	}
	for _, route := range []map[string]string{{"role": "configured-agent"}, {"pinned_agent": "configured-agent"}, {"source": cronDispatchSource}, {}} {
		m := &adapter.Message{SessionID: "does-not-exist", Metadata: route}
		c := t.Context()
		if len(route) == 0 {
			c = WithWorkflowStepOptions(c, &WorkflowStepOptions{})
		}
		if err := eng.applySessionPersona(c, m); err != nil {
			t.Fatalf("non-chat route read binding: %v", err)
		}
	}
	if _, err := store.GetSession(t.Context(), "does-not-exist"); err == nil {
		t.Fatal("boundary fixture unexpectedly exists")
	}
}

func TestSessionPersonaFirstBoundTurnDoesNotUseKeywordFastPath(t *testing.T) {
	eng, provider, store, _, reg, _ := newInheritanceFixture(t, false)
	registerSessionPersona(t, reg, "role-one", "FIRST_BOUND_PERSONA_BODY")
	if err := store.CreateSession(t.Context(), &storage.Session{ID: "first-bound", UserID: "owner", Title: "bound"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionPersona(t.Context(), "first-bound", "role-one"); err != nil {
		t.Fatal(err)
	}
	msg := &adapter.Message{ID: "first-bound-request", UserID: "owner", Platform: adapter.PlatformAPI, SessionID: "first-bound", Content: "diagnostic", Metadata: map[string]string{"provider": "test", "model": "mock-model", "pinned_agent": "default"}}
	if _, err := eng.Process(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	fast, _ := reg.Get("diagnostic")
	if fast.(*inheritanceFastPathSkill).calls != 0 || provider.calls != 1 || !provider.sawText("FIRST_BOUND_PERSONA_BODY") {
		t.Fatal("bound first turn bypassed persona")
	}
}

func TestSessionPersonaLegacyMetadataWithoutBindingRemainsUsable(t *testing.T) {
	eng, _, store, _, _, _ := newInheritanceFixture(t, false)
	for _, raw := range []string{"legacy non-JSON metadata", "[]", "null"} {
		if err := store.CreateSession(t.Context(), &storage.Session{ID: raw, UserID: "owner", Title: "legacy", Meta: raw}); err != nil {
			t.Fatal(err)
		}
		msg := &adapter.Message{SessionID: raw, Metadata: map[string]string{}}
		if err := eng.applySessionPersona(t.Context(), msg); err != nil {
			t.Fatal(err)
		}
		if len(msg.Metadata) != 0 {
			t.Fatalf("legacy metadata generated a persona: %v", msg.Metadata)
		}
		if err := store.SetSessionPersona(t.Context(), raw, "role-one"); err == nil {
			t.Fatal("non-object metadata was overwritten or falsely saved")
		}
		sess, err := store.GetSession(t.Context(), raw)
		if err != nil || sess.Meta != raw {
			t.Fatalf("legacy session=%+v err=%v", sess, err)
		}
	}
}
