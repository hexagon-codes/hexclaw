package main

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
)

func TestIMPDFBodyExcludesOnlyActualAppendedDigest(t *testing.T) {
	body := "解释 knowledge_search 和 solve 的含义。\n\n" + strings.Repeat("正文", 500)
	msg, reply, deps, delivery := imFinalReplyTestInput(t, body)
	reply.ToolCalls = []adapter.ToolCall{{Name: "knowledge_search", Status: "success", Result: "Found 3 passages"}, {Name: "solve", Status: "success", Result: "第一步：原长40米"}}
	if err := appendIMToolDigestAndRebuildCanonical(reply, "dingtalk"); err != nil {
		t.Fatal(err)
	}
	original := reply.Content
	got, err := maybeDeliverIMFinalReply(t.Context(), msg, reply, nil, deps)
	if err != nil || got != nil || len(delivery.requests) != 1 {
		t.Fatalf("PDF preparation: %v", err)
	}
	if delivery.requests[0].Message.Content != body {
		t.Fatal("PDF source lost teaching text or retained the appended execution digest")
	}
	if reply.Content != original || len(reply.ToolCalls) != 2 {
		t.Fatal("PDF projection modified original execution facts")
	}
}
