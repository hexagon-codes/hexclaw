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
