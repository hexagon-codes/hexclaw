package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 已保存正文而回执确认未完成时，冷启动先确认落盘；随后删除也不能被重放复活。
func TestMemoryEvent_RecoverWrittenEntryBeforeDeletion(t *testing.T) {
	dir := t.TempDir()
	fm, err := New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	write := func(fm *FileMemory) error {
		return fm.SaveStructuredEvent("attempt-1", "小数乘法需要复习", "fact", "学情", "learner-a", EntryMeta{Subject: "小数乘法"})
	}
	if err := write(fm); err != nil {
		t.Fatal(err)
	}
	id := fm.ParseEntriesForRole("learner-a")[0].ID
	receipts, err := filepath.Glob(filepath.Join(dir, ".event-receipts", "*.json"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("expected one durable receipt: %v, %v", receipts, err)
	}
	data, err := os.ReadFile(receipts[0])
	if err != nil {
		t.Fatal(err)
	}
	var receipt eventEntryReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	// 保留实际写入的正文及准备信息，模拟确认文件尚未替换的中断现场。
	receipt.Applied = false
	if err := saveEventEntryReceipt(receipts[0], receipt); err != nil {
		t.Fatal(err)
	}
	fm, err = New(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := fm.DeleteEntry(id); err != nil {
		t.Fatal(err)
	}
	if err := write(fm); err != nil {
		t.Fatal(err)
	}
	if got := fm.ParseEntriesForRole("learner-a"); len(got) != 0 {
		t.Fatalf("replayed event restored deleted memory: %+v", got)
	}
}

func TestMemoryEvent_IdentityConflictPreservesOriginal(t *testing.T) {
	fm, err := New(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := fm.SaveStructuredEvent("attempt-1", "原始内容", "fact", "学情", "learner-a", EntryMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := fm.SaveStructuredEvent("attempt-1", "改变了来源的内容", "fact", "学情", "learner-b", EntryMeta{}); err == nil {
		t.Fatal("conflicting event must not write another memory")
	}
	got := fm.ParseEntriesForRole("learner-a")
	if len(got) != 1 || got[0].Content != "原始内容" {
		t.Fatalf("original memory changed: %+v", got)
	}
	if got := fm.ParseEntriesForRole("learner-b"); len(got) != 0 {
		t.Fatalf("conflicting replay wrote another role: %+v", got)
	}
}
