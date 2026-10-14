package library

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

type remainingUnitPromptInstallation struct {
	PromptID     string
	Version      int
	BodyDigest   string
	UserModified int
	DeletedAt    sql.NullString
}

func remainingUnitPromptInstallations(t *testing.T, db *sql.DB) map[string]remainingUnitPromptInstallation {
	t.Helper()
	rows, err := db.Query(`SELECT builtin_key,prompt_id,installed_version,installed_body_digest,user_modified,deleted_at
	 FROM prompt_builtin_installations ORDER BY builtin_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	installed := make(map[string]remainingUnitPromptInstallation)
	for rows.Next() {
		var key string
		var row remainingUnitPromptInstallation
		if err := rows.Scan(&key, &row.PromptID, &row.Version, &row.BodyDigest, &row.UserModified, &row.DeletedAt); err != nil {
			t.Fatal(err)
		}
		installed[key] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return installed
}

func remainingUnitPrompts(t *testing.T, store *PromptStore) map[string]Prompt {
	t.Helper()
	prompts, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bySubject := make(map[string]Prompt)
	for _, prompt := range prompts {
		if _, exists := bySubject[prompt.Subject]; exists {
			t.Fatalf("duplicate subject prompt: %s", prompt.Subject)
		}
		bySubject[prompt.Subject] = prompt
	}
	return bySubject
}

func TestK12UnitPromptInitializationSixSubjectsSQLite(t *testing.T) {
	ctx := context.Background()
	db := setupDB(t)
	store := NewPromptStore(db)
	if err := store.EnsureK12UnitPrompts(ctx); err != nil {
		t.Fatal(err)
	}
	prompts := remainingUnitPrompts(t, store)
	installed := remainingUnitPromptInstallations(t, db)
	// 固定业务名单独立于安装函数输出，避免漏科、错科或重复条目被动态预期掩盖。
	wants := []struct {
		subject string
		command string
		title   string
	}{
		{"math", "/unit-math", "梳理数学单元知识"},
		{"chinese", "/unit-chinese", "梳理语文单元知识"},
		{"english", "/unit-english", "梳理英语单元知识"},
		{"science", "/unit-science", "梳理科学单元知识"},
		{"information_technology", "/unit-it", "梳理信息科技单元知识"},
		{"art", "/unit-art", "梳理美术单元知识"},
	}
	if len(prompts) != 6 || len(installed) != 6 {
		t.Fatalf("initialization counts: prompts=%d installations=%d, want six each", len(prompts), len(installed))
	}
	ids := make(map[string]bool)
	for _, want := range wants {
		prompt, ok := prompts[want.subject]
		key := "k12.unit_summary." + want.subject
		if !ok || prompt.ID == "" || ids[prompt.ID] || prompt.BuiltinKey != key ||
			prompt.Command != want.command || prompt.Title != want.title || prompt.Type != "command" ||
			prompt.Scenario != "k12" || prompt.TaskKind != "unit_summary" || !prompt.Enabled ||
			strings.TrimSpace(prompt.BodyMD) == "" {
			t.Fatalf("incorrect default for %s: %+v", want.subject, prompt)
		}
		ids[prompt.ID] = true
		row, ok := installed[key]
		if !ok || row.PromptID != prompt.ID || row.Version != 1 || row.UserModified != 0 ||
			row.DeletedAt.Valid || len(row.BodyDigest) != 64 {
			t.Fatalf("incorrect installation for %s: %+v", want.subject, row)
		}
	}
	enabled, err := store.ListEnabled(ctx)
	if err != nil || len(enabled) != 6 {
		t.Fatalf("enabled defaults=%d, want six: %v", len(enabled), err)
	}
}

func TestK12UnitPromptReinstallPreservesChoicesAndTombstonesSQLite(t *testing.T) {
	ctx := context.Background()
	db := setupDB(t)
	store := NewPromptStore(db)
	if err := store.EnsureK12UnitPrompts(ctx); err != nil {
		t.Fatal(err)
	}
	initial := remainingUnitPrompts(t, store)
	// 当前安装版本没有合法更低版本，只验证重复安装对用户内容和选择的保留。
	custom := initial["chinese"]
	custom.Title = "家长自定义语文梳理"
	custom.BodyMD = "保留我的语文教学顺序。\n只整理我明确指定的内容。"
	if _, err := store.Upsert(ctx, &custom); err != nil {
		t.Fatal(err)
	}
	disabled := initial["english"]
	disabled.Enabled = false
	if _, err := store.Upsert(ctx, &disabled); err != nil {
		t.Fatal(err)
	}
	reenabled := initial["science"]
	reenabled.Enabled = false
	if _, err := store.Upsert(ctx, &reenabled); err != nil {
		t.Fatal(err)
	}
	reenabled.Enabled = true
	if _, err := store.Upsert(ctx, &reenabled); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, initial["information_technology"].ID); err != nil {
		t.Fatal(err)
	}
	before := remainingUnitPrompts(t, store)
	beforeInstalled := remainingUnitPromptInstallations(t, db)
	if beforeInstalled["k12.unit_summary.english"].UserModified != 0 ||
		beforeInstalled["k12.unit_summary.science"].UserModified != 0 {
		t.Fatal("enable-only changes acquired a content-edit marker")
	}
	if !beforeInstalled["k12.unit_summary.information_technology"].DeletedAt.Valid {
		t.Fatal("delete did not persist a tombstone")
	}
	if err := store.EnsureK12UnitPrompts(ctx); err != nil {
		t.Fatal(err)
	}
	after := remainingUnitPrompts(t, store)
	afterInstalled := remainingUnitPromptInstallations(t, db)
	if len(after) != 5 || len(afterInstalled) != 6 {
		t.Fatalf("reinstall duplicated or revived defaults: prompts=%d installations=%d", len(after), len(afterInstalled))
	}
	for subject, prompt := range after {
		if prompt.ID != initial[subject].ID {
			t.Fatalf("reinstall replaced the existing %s prompt ID", subject)
		}
	}
	if !reflect.DeepEqual(after["math"], before["math"]) || !after["math"].Enabled {
		t.Fatal("reinstall changed an unmodified current default")
	}
	if !reflect.DeepEqual(after["chinese"], before["chinese"]) ||
		after["chinese"].Title != "家长自定义语文梳理" || after["chinese"].BodyMD != custom.BodyMD ||
		!reflect.DeepEqual(afterInstalled["k12.unit_summary.chinese"], beforeInstalled["k12.unit_summary.chinese"]) {
		t.Fatal("reinstall overwrote a user-edited prompt or its installation marker")
	}
	if after["english"].Enabled || !after["science"].Enabled {
		t.Fatal("reinstall overwrote disabled or re-enabled choices")
	}
	for _, subject := range []string{"math", "english", "science", "art"} {
		row := afterInstalled["k12.unit_summary."+subject]
		if row.Version != 1 || row.UserModified != 0 || row.DeletedAt.Valid {
			t.Fatalf("reinstall changed the current %s installation: %+v", subject, row)
		}
	}
	if _, ok := after["information_technology"]; ok {
		t.Fatal("reinstall revived a deleted default")
	}
	if !reflect.DeepEqual(afterInstalled["k12.unit_summary.information_technology"], beforeInstalled["k12.unit_summary.information_technology"]) {
		t.Fatal("reinstall changed the deleted default's tombstone or identity")
	}
	if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(afterInstalled, beforeInstalled) {
		t.Fatal("reinstall changed saved prompts or installation records")
	}
	if _, exists, err := store.Get(ctx, initial["information_technology"].ID); err != nil || exists {
		t.Fatalf("deleted default returned by ID: exists=%v error=%v", exists, err)
	}
}

func TestK12UnitPromptInstallationSharedAcrossChildrenSQLite(t *testing.T) {
	ctx := context.Background()
	db := setupDB(t)
	first := NewPromptStore(db)
	if err := first.EnsureK12UnitPrompts(ctx); err != nil {
		t.Fatal(err)
	}
	before := remainingUnitPrompts(t, first)
	beforeInstalled := remainingUnitPromptInstallations(t, db)
	if len(before) != 6 || len(beforeInstalled) != 6 {
		t.Fatalf("first initialization counts: prompts=%d installations=%d, want six each", len(before), len(beforeInstalled))
	}
	// 安装入口不接收孩子身份；两个句柄模拟不同孩子触发同一服务库的安装与恢复。
	second := NewPromptStore(db)
	for _, step := range []struct {
		name  string
		store *PromptStore
	}{
		{"second_child_initialization", second},
		{"first_child_restore", first},
		{"second_child_restore", second},
	} {
		t.Run(step.name, func(t *testing.T) {
			if err := step.store.EnsureK12UnitPrompts(ctx); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(remainingUnitPrompts(t, step.store), before) ||
				!reflect.DeepEqual(remainingUnitPromptInstallations(t, db), beforeInstalled) {
				t.Fatal("shared initialization or restore duplicated or changed installed prompts")
			}
		})
	}
}
