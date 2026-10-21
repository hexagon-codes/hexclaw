package engineadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestTeachingDiagramArchiveRestoresExactBytesAndMigratesOwner(t *testing.T) {
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	f := newArchiveRestoreFixture(t)
	ctx := context.Background()
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 20"><text x="0" y="15">12 TB</text></svg>`)
	id, err := f.records.PutTeachingDiagramAsset(ctx, "guardian", "mingming", svg, 100)
	if err != nil {
		t.Fatal(err)
	}
	set, err := k12.NewPracticeSetRecord("mingming", "diagram-archive", k12.PracticeSetFields{SourceKind: k12.PracticeSourceManual, Title: "容量", Items: []k12.PracticeItem{{ItemID: "diagram-item", Subject: "数学", QuestionMarkdown: "总容量是多少？", ExpectedAnswerMarkdown: "12 TB\n\n![容量关系](" + id + ")"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.records.Put(ctx, set); err != nil {
		t.Fatal(err)
	}
	d := usecase.Deps{Records: f.records, ArchiveRestorer: f.restore, ArchiveMigrator: f.restore}
	bak, err := d.Backup(ctx, "mingming")
	if err != nil || len(bak.TeachingDiagrams) != 1 {
		t.Fatalf("archive has no diagram: %v", err)
	}
	before, _ := json.Marshal(bak)
	if _, err := f.db.ExecContext(ctx, `DELETE FROM k12_teaching_diagram_assets WHERE agent_name=?`, "mingming"); err != nil {
		t.Fatal(err)
	}
	if err := f.restore.RestoreHexbak(ctx, bak); err != nil {
		t.Fatal(err)
	}
	actual, err := f.records.GetTeachingDiagramAsset(ctx, "guardian", "mingming", id)
	if err != nil || !bytes.Equal(actual, svg) {
		t.Fatalf("restored SVG bytes changed: %v", err)
	}
	target := newArchiveRestoreFixture(t)
	registerRestoreTarget(t, target)
	targetDeps := usecase.Deps{Records: target.records, ArchiveMigrator: target.restore}
	result, err := targetDeps.RestoreAs(ctx, usecase.RestoreAsRequest{Archive: bak, SourceAgent: "mingming", TargetAgent: "target-child", GuardianConfirmed: true, IdempotencyKey: "diagram-owner-once"})
	if err != nil {
		t.Fatal(err)
	}
	newID := strings.Replace(id, "asset://mingming/", "asset://target-child/", 1)
	actual, err = target.records.GetTeachingDiagramAsset(ctx, "guardian", "target-child", newID)
	if err != nil || !bytes.Equal(actual, svg) {
		t.Fatalf("migrated SVG bytes or owner changed: %v", err)
	}
	items, err := target.records.ExportAgent(ctx, "target-child")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if strings.Contains(item.Fields, id) {
			t.Fatal("target retains source SVG reference")
		}
		found = found || strings.Contains(item.Fields, newID)
	}
	if !found {
		t.Fatal("target lost diagram reference")
	}
	after, _ := json.Marshal(bak)
	if !bytes.Equal(before, after) {
		t.Fatal("restore-as changed the source archive")
	}
	if _, err := targetDeps.RollbackRestoreAs(ctx, usecase.RestoreAsRollbackRequest{MigrationID: result.MigrationID, TargetAgent: "target-child", GuardianConfirmed: true}); err != nil {
		t.Fatal(err)
	}
	var corrupt usecase.Hexbak
	if err := json.Unmarshal(before, &corrupt); err != nil {
		t.Fatal(err)
	}
	corrupt.TeachingDiagrams[0].SVG = []byte("changed")
	if err := f.restore.RestoreHexbak(ctx, &corrupt); err == nil {
		t.Fatal("corrupt diagram archive was accepted")
	}
	actual, err = f.records.GetTeachingDiagramAsset(ctx, "guardian", "mingming", id)
	if err != nil || !bytes.Equal(actual, svg) {
		t.Fatal("invalid archive damaged the existing asset")
	}
}
