package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	agentrouter "github.com/hexagon-codes/hexclaw/router"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/storage/migrate"

	_ "modernc.org/sqlite"
)

// 通过终端策略和真实 SQLite 核对课程更新、缺失、多孩及缓存隔离，不调用模型。
func TestK12TutorCurriculumContextUsesCurrentFacts(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "context.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err = migrate.Run(ctx, db, migrate.All); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO agents(name) VALUES('child-a'),('child-b')`); err != nil {
		t.Fatal(err)
	}
	deps := usecase.Deps{Records: k12storage.NewStore(db, nil)}
	policy := newK12TutorIdentityPolicy(agentrouter.New(), nil)
	policy.followup = &deps
	const preference = "历史偏好：只使用五年级方法"
	agent := agentrouter.AgentConfig{Name: "child-a", SystemPrompt: preference, Metadata: map[string]string{
		"scenario": k12TutorScenario, k12.MetaKeyChildName: "甲", k12.MetaKeyGradeTerm: "六年级上",
		k12.MetaKeyTextbookMath: "人教版", k12.MetaKeyPromptContractVersion: k12.TutorIdentityPromptContractVersion,
	}}
	compile := func(config agentrouter.AgentConfig) engine.AgentSystemPromptDirective {
		t.Helper()
		directive, err := policy.CompileTerminalDirective(ctx, engine.AgentSystemPromptPolicyInput{Agent: config, UserQuery: "请按这次课程讲清分数乘法"})
		if err != nil {
			t.Fatal(err)
		}
		return directive
	}
	empty := compile(agent)
	if !strings.Contains(empty.Content, `"grade_term":"六年级上"`) || !strings.Contains(empty.Content, "An old grade restriction must not override the current confirmed course") || strings.Contains(empty.Content, "Confirmed course context") {
		t.Fatalf("current profile or absent course contract lost: %s", empty.Content)
	}
	if _, err = db.Exec(`INSERT INTO k12_curriculum_progress(progress_id,agent_name,subject,revision,textbook_binding_id,textbook_edition,textbook_version,title,volume,unit_id,unit_title,lesson_id,lesson_title,requested_page_from,requested_page_to,verified_page_from,verified_page_to,page_verification_status,segment_refs_json,evidence_source,confirmed_at,created_at,updated_at)
		VALUES('progress-a','child-a','math',1,'binding-a','人教版','v1','六年级数学','上册','unit-1','分数乘法','lesson-1','分数乘整数',12,99,12,13,'partially_verified','["unrelated-source-body"]','parent_confirmed',1000,1000,1000)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO k12_curriculum_progress_revisions(agent_name,subject,revision,updated_at) VALUES('child-a','math',1,1000)`); err != nil {
		t.Fatal(err)
	}
	current := compile(agent)
	if current.Key == empty.Key || !strings.Contains(current.Content, `"unit":"分数乘法"`) || !strings.Contains(current.Content, `"verified_page_from":12`) || strings.Contains(current.Content, "99") || strings.Contains(current.Content, "unrelated-source-body") || !strings.Contains(current.Content, "Explicit course constraints for the current task take precedence") {
		t.Fatalf("confirmed course projection is incomplete or contains unrelated data: %s", current.Content)
	}
	if stable := compile(agent); stable.Key != current.Key {
		t.Fatal("unchanged facts changed cache identity")
	}
	if _, err = db.Exec(`UPDATE k12_curriculum_progress SET revision=2,unit_id='unit-5',unit_title='圆',lesson_title='',updated_at=2000 WHERE agent_name='child-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE k12_curriculum_progress_revisions SET revision=2,updated_at=2000 WHERE agent_name='child-a'`); err != nil {
		t.Fatal(err)
	}
	updated := compile(agent)
	if updated.Key == current.Key || !strings.Contains(updated.Content, `"unit":"圆"`) || strings.Contains(updated.Content, `"unit":"分数乘法"`) {
		t.Fatal("updated progress reused old course/cache identity")
	}
	other := agent
	other.Name, other.Metadata = "child-b", map[string]string{"scenario": k12TutorScenario, k12.MetaKeyChildName: "乙", k12.MetaKeyGradeTerm: "五年级上", k12.MetaKeyPromptContractVersion: k12.TutorIdentityPromptContractVersion}
	otherContext := compile(other)
	if otherContext.Key == updated.Key || strings.Contains(otherContext.Content, "Confirmed course context") || strings.Contains(otherContext.Content, `"child_name":"甲"`) {
		t.Fatal("another child inherited the course")
	}
	agent.Metadata[k12.MetaKeyGradeTerm] = "六年级下"
	if changed := compile(agent); changed.Key == updated.Key || !strings.Contains(changed.Content, `"grade_term":"六年级下"`) {
		t.Fatal("profile update reused the old cache identity")
	}
	if agent.SystemPrompt != preference {
		t.Fatal("historical preference was rewritten")
	}
	if _, err = db.Exec(`DELETE FROM k12_curriculum_progress WHERE agent_name='child-a'`); err != nil {
		t.Fatal(err)
	}
	if cleared := compile(agent); strings.Contains(cleared.Content, "Confirmed course context") {
		t.Fatal("cleared course remained in context")
	}
}
