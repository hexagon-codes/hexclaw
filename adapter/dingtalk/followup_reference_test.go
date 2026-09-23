package dingtalk

import (
	"encoding/json"
	"testing"
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
