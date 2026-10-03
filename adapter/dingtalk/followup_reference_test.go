package dingtalk

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	dtpayload "github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

func TestDingTalkFollowupReferenceUsesExplicitRepliedMessage(t *testing.T) {
	for _, tc := range []struct{ name, payload, want string }{
		{"quoted", `{"msgId":"new","text":{"content":"第三题再简单点","isReplyMsg":true,"repliedMsg":{"msgId":"source"}}}`, "source"},
		{"ordinary", `{"msgId":"new","originalMsgId":"transport-id","text":{"content":"第三题再简单点"}}`, ""},
		{"not_reply", `{"msgId":"new","text":{"content":"第三题再简单点","isReplyMsg":false,"repliedMsg":{"msgId":"unrelated"}}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var event dtEvent
			if err := json.Unmarshal([]byte(tc.payload), &event); err != nil {
				t.Fatal(err)
			}
			message := (&DingtalkAdapter{}).messageFromEvent(event, nil)
			if message.ID != "new" || message.ReplyTo != tc.want || message.Content != "第三题再简单点" {
				t.Fatalf("message=%+v", message)
			}
		})
	}
}

func TestDingTalkStreamFramePreservesExplicitFollowupReference(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"quoted", `{"content":"第三题再简单点","isReplyMsg":true,"repliedMsg":{"msgId":"source"}}`, "source"},
		{"ordinary", `{"content":"第三题再简单点"}`, ""},
		{"not_reply", `{"content":"第三题再简单点","isReplyMsg":false,"repliedMsg":{"msgId":"unrelated"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captured := make(chan *adapter.Message, 1)
			a := newTestAdapter()
			a.openAPI = newFakeDingtalkOpenAPI("tok")
			a.handler = func(_ context.Context, message *adapter.Message) (*adapter.Reply, error) {
				captured <- message
				return nil, nil
			}
			response, err := a.onChatBotFrame(context.Background(), &dtpayload.DataFrame{Data: `{"msgId":"next","originalMsgId":"transport-id","conversationType":"1","senderStaffId":"parent","msgtype":"text","text":` + tc.text + `}`})
			if err != nil || response == nil {
				t.Fatalf("stream callback: response=%+v err=%v", response, err)
			}
			select {
			case message := <-captured:
				if message.ID != "next" || message.ChatID != "parent" || message.ReplyTo != tc.want || message.Content != "第三题再简单点" {
					t.Fatalf("stream reference lost or invented: %+v", message)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("stream message did not reach the common handler")
			}
		})
	}
}

func TestDingTalkStreamPreservesHomeworkNumberInReceivedText(t *testing.T) {
	captured := make(chan *adapter.Message, 1)
	a := newTestAdapter()
	a.openAPI = newFakeDingtalkOpenAPI("tok")
	a.handler = func(_ context.Context, message *adapter.Message) (*adapter.Reply, error) {
		captured <- message
		return nil, nil
	}
	const content = "这次作业有哪些问题？作业编号：HW-existing-task"
	response, err := a.onChatBotFrame(context.Background(), &dtpayload.DataFrame{Data: `{"msgId":"number-followup","conversationType":"1","senderStaffId":"parent","msgtype":"text","text":{"content":"` + content + `","isReplyMsg":true,"repliedMsg":{"msgId":"native-result"}}}`})
	if err != nil || response == nil {
		t.Fatalf("callback: %+v %v", response, err)
	}
	select {
	case message := <-captured:
		if message.Content != content || message.ReplyTo != "native-result" || message.ChatID != "parent" {
			t.Fatalf("number or native identity changed: %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("numbered text did not reach the common handler")
	}
}

func TestDingTalkStreamExtractsHomeworkNumberFromQuotedCardText(t *testing.T) {
	const question = "这条批改结果对应哪次作业？只按已有结果说明。"
	longChildren := make([]any, 60)
	for i := range longChildren {
		longChildren[i] = map[string]any{"value": "已有批改正文"}
	}
	longChildren = append(longChildren,
		map[string]any{"value": "作业编号：HW-existing-task"},
		map[string]any{"value": "HW-existing-task"},
	)
	for _, tc := range []struct {
		name          string
		content       any
		isReply       bool
		wantID        string
		wantAmbiguous bool
	}{
		{
			name: "footer_beyond_diagnostic_limit_and_repeated_id",
			content: map[string]any{"cardContent": []any{
				map[string]any{"children": longChildren},
			}},
			isReply: true, wantID: "existing-task",
		},
		{
			name: "different_numbers_across_blocks_are_ambiguous",
			content: map[string]any{"cardContent": []any{
				map[string]any{"children": []any{map[string]any{"value": "HW-first-task"}}},
				map[string]any{"children": []any{map[string]any{"value": "HW-other-task"}}},
			}},
			isReply: true, wantAmbiguous: true,
		},
		{
			name: "download_code_is_not_quoted_text",
			content: map[string]any{"cardContent": []any{
				map[string]any{"children": []any{map[string]any{"downloadCode": "HW-media-code"}}},
			}},
			isReply: true,
		},
		{
			name: "nonstring_values_do_not_drop_current_message",
			content: map[string]any{"cardContent": []any{
				map[string]any{"children": []any{map[string]any{"value": 1}, map[string]any{"value": nil}}},
			}},
			isReply: true,
		},
		{
			name: "nonreply_does_not_adopt_quoted_number",
			content: map[string]any{"cardContent": []any{
				map[string]any{"children": []any{map[string]any{"value": "HW-unrelated-task"}}},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"msgId": "followup", "conversationType": "1", "senderStaffId": "parent", "msgtype": "text",
				"text": map[string]any{
					"content": question, "isReplyMsg": tc.isReply,
					"repliedMsg": map[string]any{"msgId": "native-result", "content": tc.content},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			captured := make(chan *adapter.Message, 1)
			a := newTestAdapter()
			a.openAPI = newFakeDingtalkOpenAPI("tok")
			a.handler = func(_ context.Context, message *adapter.Message) (*adapter.Reply, error) {
				captured <- message
				return nil, nil
			}
			response, err := a.onChatBotFrame(context.Background(), &dtpayload.DataFrame{Data: string(payload)})
			if err != nil || response == nil {
				t.Fatalf("stream callback: response=%+v err=%v", response, err)
			}
			select {
			case message := <-captured:
				wantReply := ""
				if tc.isReply {
					wantReply = "native-result"
				}
				if message.Content != question || message.ReplyTo != wantReply || len(message.Attachments) != 0 {
					t.Fatalf("current question, explicit reference or attachments changed: %+v", message)
				}
				if got := message.Metadata["quoted_homework_id"]; got != tc.wantID {
					t.Fatalf("quoted homework ID = %q, want %q", got, tc.wantID)
				}
				if got := message.Metadata["quoted_homework_ambiguous"] == "true"; got != tc.wantAmbiguous {
					t.Fatalf("ambiguous = %v, want %v", got, tc.wantAmbiguous)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("quoted message did not reach the common handler")
			}
		})
	}
}
