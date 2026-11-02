package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractLegacyDOCRealText(t *testing.T) {
	if findTool("textutil", "/usr/bin/textutil") == "" && findTool("antiword", "/usr/bin/antiword", "/usr/local/bin/antiword") == "" {
		t.Skip("legacy DOC extraction requires textutil or antiword; real tool boundary not verified")
	}
	data, err := os.ReadFile(filepath.Join("testdata", "legacy-word.doc"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := extractDOCText(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "LEGACY_DOC_REFERENCE") || !strings.Contains(text, "甲乙丙") {
		t.Fatalf("legacy DOC body was not extracted: %q", text)
	}
}

func TestExtractLegacyDOCAntiwordFallback(t *testing.T) {
	if findTool("antiword", "/usr/bin/antiword", "/usr/local/bin/antiword") == "" {
		t.Skip("antiword is unavailable; its real extraction boundary not verified")
	}
	// 模拟优先工具不可执行，仍由真实antiword读取真实OLE文件。
	t.Setenv("HEXCLAW_TEXTUTIL", filepath.Join(t.TempDir(), "unavailable-textutil"))
	data, err := os.ReadFile(filepath.Join("testdata", "legacy-word.doc"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := extractDOCText(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "LEGACY_DOC_REFERENCE") || !strings.Contains(text, "甲乙丙") {
		t.Fatalf("antiword fallback did not preserve the DOC body: %q", text)
	}
}
