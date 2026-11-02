package engine

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
)

func TestKnowledgeRetrievalQueryPreservesUserQuestion(t *testing.T) {
	document := "[附件文档内容 - 请基于以下文档回答用户问题]\n" + strings.Repeat("附件原文。", 2000) + "\n\n[用户问题]\n这文件显示的什么？"
	reference := "[用户引用的参考上下文 — 请结合以下内容回答用户问题]\n引用资料\n\n[用户问题]\n请查相关知识库"
	plain := "请解释这个文本标记：\n\n[用户问题]\n后面的内容也是我的问题。"
	tests := []struct {
		name     string
		content  string
		metadata map[string]string
		want     string
	}{
		{name: "structured question", content: document, metadata: map[string]string{"retrieval_query": "查询指定问题"}, want: "查询指定问题"},
		{name: "structured question retains marker", content: document, metadata: map[string]string{"retrieval_query": plain}, want: plain},
		{name: "explicit empty question", content: document, metadata: map[string]string{"retrieval_query": ""}, want: ""},
		{name: "legacy document wrapper", content: document, want: "这文件显示的什么？"},
		{name: "legacy reference wrapper", content: reference, want: "请查相关知识库"},
		{name: "plain text marker", content: plain, want: plain},
		{name: "plain text unchanged", content: "  保留原始问题空格  ", want: "  保留原始问题空格  "},
		{name: "incomplete wrapper unchanged", content: "[附件文档内容 - 请基于以下文档回答用户问题]\n资料", want: "[附件文档内容 - 请基于以下文档回答用户问题]\n资料"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg := &adapter.Message{Content: test.content, Metadata: test.metadata}
			if got := knowledgeRetrievalQuery(msg); got != test.want {
				t.Fatalf("retrieval query = %q, want %q", got, test.want)
			}
			if msg.Content != test.content {
				t.Fatal("retrieval projection changed the complete model input")
			}
		})
	}
}
