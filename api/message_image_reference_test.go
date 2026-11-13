package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/storage"
)

func TestMessageImageHistoryProjectsDurableAssetReferences(t *testing.T) {
	asset := "asset://tutor/" + strings.Repeat("a", 64) + ".jpg"
	for _, test := range []struct {
		name, data, sourceURL, wantURL string
	}{
		{name: "asset in data", data: asset, wantURL: "/api/k12/assets/" + strings.Repeat("a", 64) + ".jpg?agent=tutor"},
		{name: "asset in url", sourceURL: asset, wantURL: "/api/k12/assets/" + strings.Repeat("a", 64) + ".jpg?agent=tutor"},
		{name: "inline image", data: "aW1hZ2U=", wantURL: "/api/v1/sessions/session/messages/message/attachments/0/content"},
		{name: "external image", sourceURL: "https://example.com/original.jpg", wantURL: "https://example.com/original.jpg"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, err := json.Marshal(map[string]any{"attachments": []map[string]string{{"type": "image", "name": "original.jpg", "data": test.data, "url": test.sourceURL}}})
			if err != nil {
				t.Fatal(err)
			}
			message := &storage.MessageRecord{ID: "message", SessionID: "session", Metadata: string(original), Attachments: string(original)}
			projectMessageImages([]*storage.MessageRecord{message})
			var projected struct {
				Attachments []struct{ Data, URL string } `json:"attachments"`
			}
			if err := json.Unmarshal([]byte(message.Metadata), &projected); err != nil {
				t.Fatal(err)
			}
			if len(projected.Attachments) != 1 || projected.Attachments[0].URL != test.wantURL || projected.Attachments[0].Data != "" {
				t.Fatalf("projected image = %#v", projected)
			}
			if message.Attachments != string(original) {
				t.Fatal("durable original attachment changed")
			}
		})
	}
}
