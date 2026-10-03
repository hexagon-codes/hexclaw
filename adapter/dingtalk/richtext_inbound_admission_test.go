package dingtalk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	dtchatbot "github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dtpayload "github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"

	"github.com/hexagon-codes/hexclaw/adapter"
)

// 同一富文本消息同时包含两种合法图片下载字段及图片前后的正文。
const richTextPhotoFrame = `{"msgId":"rich-photo-1","conversationId":"conversation-1","conversationType":"1","senderStaffId":"parent-1","senderNick":"家长","msgtype":"richText","content":{"richText":[{"text":"请批改这两张作业"},{"type":"picture","downloadCode":"download-first"},{"text":"保留两页的结果"},{"type":"picture","pictureDownloadCode":"download-second"}]}}`

func TestDingTalkRichTextACKWaitsForAllPhotosAndDurableAdmission(t *testing.T) {
	a, pictureAPI, cleanup := newDurableAdmissionPictureAdapter(t)
	defer cleanup()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	port := &inboundPhotoAdmissionProbe{handled: true, entered: entered, release: release}
	a.SetInboundPhotoAdmissionPort(port)
	legacy := make(chan *adapter.Message, 2)
	a.handler = func(_ context.Context, message *adapter.Message) (*adapter.Reply, error) {
		legacy <- message
		return nil, nil
	}
	type callbackResult struct {
		ack *dtpayload.DataFrameResponse
		err error
	}
	returned := make(chan callbackResult, 1)
	frame := &dtpayload.DataFrame{Data: richTextPhotoFrame}
	go func() {
		ack, err := a.onChatBotFrame(context.Background(), frame)
		returned <- callbackResult{ack: ack, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("rich text photo never reached durable admission")
	}
	select {
	case result := <-returned:
		t.Fatalf("ACK returned before durable admission completed: %#v", result)
	case <-time.After(100 * time.Millisecond):
	}
	assertRichTextAdmittedMessage(t, port.captured(), 1)
	pictureAPI.mu.Lock()
	codes := append([]string(nil), pictureAPI.downloadCalls...)
	pictureAPI.mu.Unlock()
	if !reflect.DeepEqual(codes, []string{"download-first", "download-second"}) {
		t.Fatalf("download fields were lost or reordered: %v", codes)
	}
	assertNoLegacyDingTalkWorkerCall(t, legacy)
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-returned:
		if result.err != nil || result.ack == nil {
			t.Fatalf("durable admission did not ACK: %#v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not finish after durable admission")
	}

	// 平台重放继续使用同一消息身份进入既有接纳端口，不另启业务 worker。
	ack, err := a.onChatBotFrame(context.Background(), frame)
	if err != nil || ack == nil {
		t.Fatalf("replayed rich text was not admitted: ack=%v err=%v", ack, err)
	}
	assertRichTextAdmittedMessage(t, port.captured(), 2)
	assertNoLegacyDingTalkWorkerCall(t, legacy)
}

func TestDingTalkRichTextFailureDoesNotACKOrStartLegacyWorker(t *testing.T) {
	for _, tc := range []struct {
		name               string
		failSecondDownload bool
		admissionError     error
		wantAdmissionCalls int
	}{
		{name: "second_picture_download", failSecondDownload: true},
		{name: "durable_admission", admissionError: errors.New("durable store unavailable"), wantAdmissionCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			downloadCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				downloadCount++
				fail := tc.failSecondDownload && downloadCount == 2
				mu.Unlock()
				if fail {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(pngBytes)
			}))
			defer server.Close()
			a := newTestAdapter()
			a.openAPI = &fakePictureOpenAPI{fakeDingtalkOpenAPI: newFakeDingtalkOpenAPI("token"), downloadURL: server.URL}
			port := &inboundPhotoAdmissionProbe{err: tc.admissionError}
			a.SetInboundPhotoAdmissionPort(port)
			legacy := make(chan *adapter.Message, 1)
			a.handler = func(_ context.Context, message *adapter.Message) (*adapter.Reply, error) {
				legacy <- message
				return nil, nil
			}
			ack, err := a.onChatBotFrame(context.Background(), &dtpayload.DataFrame{Data: richTextPhotoFrame})
			if err == nil || ack != nil {
				t.Fatalf("failed photo admission must not ACK: ack=%v err=%v", ack, err)
			}
			if tc.admissionError != nil && !errors.Is(err, tc.admissionError) {
				t.Fatalf("admission error was lost: %v", err)
			}
			if got := len(port.captured()); got != tc.wantAdmissionCalls {
				t.Fatalf("admission calls = %d, want %d", got, tc.wantAdmissionCalls)
			}
			if tc.wantAdmissionCalls != 0 {
				assertRichTextAdmittedMessage(t, port.captured(), 1)
			}
			assertNoLegacyDingTalkWorkerCall(t, legacy)
		})
	}
}

func TestDingTalkRichTextPlainTextDoesNotEnterPhotoAdmission(t *testing.T) {
	a, pictureAPI, cleanup := newDurableAdmissionPictureAdapter(t)
	defer cleanup()
	port := &inboundPhotoAdmissionProbe{handled: true}
	a.SetInboundPhotoAdmissionPort(port)
	captured := make(chan *adapter.Message, 1)
	a.handler = func(_ context.Context, message *adapter.Message) (*adapter.Reply, error) {
		captured <- message
		return nil, nil
	}
	var data dtchatbot.BotCallbackDataModel
	const payload = `{"msgId":"rich-text-only","conversationId":"conversation-1","conversationType":"1","senderStaffId":"parent-1","msgtype":"richText","content":{"richText":[{"text":"请讲解分数乘法"},{"text":"按六年级的方法"}]}}`
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		t.Fatal(err)
	}
	ack, err := a.onChatBotMessage(context.Background(), &data)
	if err != nil || ack == nil {
		t.Fatalf("plain rich text was not ACKed: ack=%v err=%v", ack, err)
	}
	select {
	case message := <-captured:
		if message.Content != "请讲解分数乘法\n按六年级的方法" || len(message.Attachments) != 0 || message.ID != "rich-text-only" {
			t.Fatalf("rich text projection changed content or identity: %#v", message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("plain rich text did not reach the text handler")
	}
	a.workers.Wait()
	if len(port.captured()) != 0 || pictureDownloadCallCount(pictureAPI) != 0 {
		t.Fatal("plain rich text incorrectly entered the photo pipeline")
	}
}

func assertRichTextAdmittedMessage(t *testing.T, messages []*adapter.Message, wantCount int) {
	t.Helper()
	if len(messages) != wantCount {
		t.Fatalf("admitted count = %d, want %d", len(messages), wantCount)
	}
	for _, message := range messages {
		if message.ID != "rich-photo-1" || message.Platform != adapter.PlatformDingtalk ||
			message.InstanceID != "dingtalk" || message.ChatID != "parent-1" ||
			message.Content != "请批改这两张作业\n保留两页的结果" {
			t.Fatalf("rich text content or stable message identity changed: %#v", message)
		}
		if len(message.Attachments) != 2 {
			t.Fatalf("admission received %d photos, want both photos", len(message.Attachments))
		}
		for _, attachment := range message.Attachments {
			if attachment.Type != "image" || attachment.Data != base64.StdEncoding.EncodeToString(pngBytes) {
				t.Fatalf("admission received invalid photo bytes: %#v", attachment)
			}
		}
	}
}
