package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplyDeliveryAttemptIsPrivateAndBoundToOriginalEvent(t *testing.T) {
	msg := &Message{ID: "event-a", Platform: PlatformSlack, InstanceID: "instance-a", UserID: "owner", ChatID: "chat-a"}
	reply := &Reply{AssistantMessageID: "assistant-a", Metadata: map[string]string{"delivery_attempted": "true"}}
	if reply.DeliveryAttempted(msg) {
		t.Fatal("metadata claimed an actual delivery attempt")
	}
	reply.MarkDeliveryAttempted(msg)
	if !reply.DeliveryAttempted(msg) {
		t.Fatal("original event lost its actual delivery attempt")
	}
	copy := *msg
	copy.SessionID, copy.Metadata = "resolved-session", map[string]string{"request_id": "resolved-request"}
	if !reply.DeliveryAttempted(&copy) {
		t.Fatal("later session projection changed the stable event identity")
	}
	copy.ID = "event-b"
	if reply.DeliveryAttempted(&copy) {
		t.Fatal("another event inherited an earlier delivery attempt")
	}
	copy = *msg
	copy.ChatID = "chat-b"
	if reply.DeliveryAttempted(&copy) {
		t.Fatal("another target inherited an earlier delivery attempt")
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "deliveryAttempted") || strings.Contains(string(encoded), "event-a") {
		t.Fatal("private delivery identity leaked into JSON")
	}
	var restored Reply
	if err := json.Unmarshal([]byte(`{"deliveryAttempted":true,"DeliveryAttempted":true,"delivery_attempted":true,"Metadata":{"delivery_attempted":"true"}}`), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.DeliveryAttempted(msg) {
		t.Fatal("JSON forged a trusted delivery attempt")
	}
}
