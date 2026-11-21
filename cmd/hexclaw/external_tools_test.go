package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/toolkit/blobstore"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/externaltools"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

func TestProcessObservationsAndRequestedFilesHaveIndependentDelivery(t *testing.T) {
	root := t.TempDir()
	store, err := sqlitefixture.New(filepath.Join(root, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	blobs, err := blobstore.NewStore(filepath.Join(root, "generated"))
	if err != nil {
		t.Fatal(err)
	}
	service := externaltools.New(t.Context(), store.ExternalToolExecutionRepository(), blobs)
	inv := skill.InvocationContext{AuthenticatedUserID: "owner", RequestID: "request", SessionID: "session", ToolCallID: "shell"}
	req := skill.WithInvocationContext(t.Context(), inv)
	shell, _, err := service.Begin(req, externaltools.Intent{Kind: "tool_call", ToolName: "shell"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"stdout", "stderr"} {
		ref, saveErr := (externalShellOutput{service}).SaveShellOutput(shell, inv, stream, strings.NewReader("diagnostic"))
		if saveErr != nil || ref["artifact_id"] == nil {
			t.Fatalf("missing %s receipt: %v", stream, saveErr)
		}
	}
	// 用户可以交付同名文本；自动浏览器日志亦不能覆盖明确请求的文件。
	inv.ToolCallID = "export"
	export, _, err := service.Begin(skill.WithInvocationContext(t.Context(), inv), externaltools.Intent{Kind: "tool_call", ToolName: "file_ops"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveArtifact(export, "stdout.txt", "text/plain", strings.NewReader("requested text")); err != nil {
		t.Fatal(err)
	}
	profile := &browserToolProfile{outputDir: t.TempDir()}
	for _, filename := range []string{"console-report.log", "console-internal.log"} {
		if err = os.WriteFile(filepath.Join(profile.outputDir, filename), []byte(filename), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runtime := browserToolRuntime{ctx: t.Context(), executions: service}
	files, err := runtime.captureOutputs(export, profile, nil, false, nil, nil, filepath.Join(profile.outputDir, "console-report.log"))
	if err != nil || len(files) != 1 || files[0]["name"] != "console-report.log" {
		t.Fatalf("requested browser file: %v %v", files, err)
	}
	reply, err := service.ResolveReply(req, &adapter.Message{UserID: inv.AuthenticatedUserID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	completion, _ := storage.AssistantCompletionFromContext(reply)
	if len(completion.Attachments) != 2 || completion.Attachments[0].Name != "stdout.txt" || completion.Attachments[1].Name != "console-report.log" {
		t.Fatalf("unexpected reply attachments: %+v", completion.Attachments)
	}
}
