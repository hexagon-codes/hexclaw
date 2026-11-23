package dingtalk

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	dtchatbot "github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dtpayload "github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"

	"github.com/hexagon-codes/hexclaw/adapter"
)

type testPhotoIntake func(context.Context, adapter.PhotoArrival, adapter.PhotoPageReader) (bool, error)

func (f testPhotoIntake) ReceivePhotos(ctx context.Context, source adapter.PhotoArrival, read adapter.PhotoPageReader) (bool, error) {
	return f(ctx, source, read)
}

func TestPhotoIntakeAtSDKStreamAndWebhookBoundaries(t *testing.T) {
	created := time.Now().Add(-time.Second).UnixMilli()
	ts := strconv.FormatInt(created, 10)
	payload := strings.Replace(richTextPhotoFrame, "{", "{\"createAt\":"+ts+",", 1)
	var commonDigest string
	for _, entry := range []string{"stream", "webhook", "sdk"} {
		t.Run(entry, func(t *testing.T) {
			a, api, cleanup := newDurableAdmissionPictureAdapter(t)
			defer cleanup()
			legacy := &inboundPhotoAdmissionProbe{handled: true}
			a.SetInboundPhotoAdmissionPort(legacy)
			observed := make(chan adapter.PhotoArrival, 1)
			reader := make(chan adapter.PhotoPageReader, 1)
			started := time.Now()
			a.SetPhotoIntake(testPhotoIntake(func(ctx context.Context, got adapter.PhotoArrival, read adapter.PhotoPageReader) (bool, error) {
				if len(api.DownloadCalls()) != 0 {
					return false, errors.New("download occurred before shared intake")
				}
				observed <- got
				reader <- read
				for i := 0; i < got.PageCount; i++ {
					att, err := read(ctx, i)
					if err != nil || att.Size != int64(len(pngBytes)) || att.Data != base64.StdEncoding.EncodeToString(pngBytes) {
						return false, errors.New("missing picture bytes")
					}
				}
				return true, nil
			}))
			switch entry {
			case "stream":
				ack, err := a.onChatBotFrame(t.Context(), &dtpayload.DataFrame{Data: payload})
				if err != nil || ack == nil {
					t.Fatalf("stream ACK: %v %v", ack, err)
				}
			case "sdk":
				var callback dtchatbot.BotCallbackDataModel
				if err := json.Unmarshal([]byte(payload), &callback); err != nil {
					t.Fatal(err)
				}
				ack, err := a.onChatBotMessage(t.Context(), &callback)
				if err != nil || ack == nil {
					t.Fatalf("SDK ACK: %v %v", ack, err)
				}
			case "webhook":
				server := httptest.NewServer(a.Handler())
				defer server.Close()
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, strings.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				mac := hmac.New(sha256.New, []byte(a.cfg.AppSecret))
				_, _ = mac.Write([]byte(ts + "\n" + a.cfg.AppSecret))
				request.Header.Set("timestamp", ts)
				request.Header.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
				resp, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("webhook ACK: %d", resp.StatusCode)
				}
			}
			var got adapter.PhotoArrival
			select {
			case got = <-observed:
			default:
				t.Fatal("provider ACK bypassed shared intake")
			}
			if got.PageCount != 2 || got.ProviderCreatedAt.UnixMilli() != created || got.ReceivedAt.Before(started) || got.ReceivedAt.After(time.Now()) ||
				got.Message.ID != "rich-photo-1" || got.Message.Content != "请批改这两张作业\n保留两页的结果" || len(got.Message.Attachments) != 0 {
				t.Fatalf("source was not observed at the callback boundary: %+v %+v", got, got.Message)
			}
			v, err := hex.DecodeString(got.FrameDigest)
			if err != nil || len(v) != sha256.Size || commonDigest != "" && got.FrameDigest != commonDigest {
				t.Fatal("inconsistent frame digest")
			}
			commonDigest = got.FrameDigest
			arrival, err := json.Marshal(got)
			if err != nil || strings.Contains(string(arrival), "download-first") || strings.Contains(string(arrival), "download-second") {
				t.Fatal("download credential in intake header")
			}
			_, err = (<-reader)(t.Context(), 0)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("reader remained usable beyond callback: %v", err)
			}
			if len(legacy.captured()) != 0 || !slices.Equal(api.DownloadCalls(), []string{"download-first", "download-second"}) {
				t.Fatal("wrong page order or duplicate admission")
			}
		})
	}
}

func TestPhotoIntakeOriginalPageOrderAndLegacyFallback(t *testing.T) {
	a, api, cleanup := newDurableAdmissionPictureAdapter(t)
	defer cleanup()
	legacy := &inboundPhotoAdmissionProbe{handled: true}
	a.SetInboundPhotoAdmissionPort(legacy)
	var digests []string
	a.SetPhotoIntake(testPhotoIntake(func(_ context.Context, src adapter.PhotoArrival, _ adapter.PhotoPageReader) (bool, error) {
		if src.PageCount != 2 || !src.ProviderCreatedAt.IsZero() {
			t.Fatal("lost page position or invented a timestamp")
		}
		digests = append(digests, src.FrameDigest)
		return len(digests) < 3, nil
	}))
	duplicate := strings.Replace(richTextPhotoFrame, "download-second", "download-first", 1)
	for _, payload := range []string{richTextPhotoFrame, duplicate, duplicate} {
		if ack, err := a.onChatBotFrame(t.Context(), &dtpayload.DataFrame{Data: payload}); err != nil || ack == nil {
			t.Fatalf("frame: %v %v", ack, err)
		}
	}
	if digests[0] == digests[1] || digests[1] != digests[2] {
		t.Fatal("changed media credential did not invalidate frame digest")
	}
	messages := legacy.captured()
	if !slices.Equal(api.DownloadCalls(), []string{"download-first"}) || len(messages) != 1 || len(messages[0].Attachments) != 1 {
		t.Fatal("old receipt did not preserve historical page identity")
	}
}

func TestPhotoReplyReturnsProviderReceiptForEveryAttemptedPart(t *testing.T) {
	for _, lastStatus := range []adapter.DeliveryStatus{adapter.DeliveryAccepted, adapter.DeliveryOutcomeUnknown} {
		t.Run(string(lastStatus), func(t *testing.T) {
			a := newTestAdapter()
			api := &fakeDingTalkFileMediaAPI{fakeDingtalkOpenAPI: newFakeDingtalkOpenAPI("token"), mediaID: "@safe-file"}
			a.openAPI = api
			if lastStatus == adapter.DeliveryOutcomeUnknown {
				api.sendErrs = []error{nil, errors.New("transport disconnected after send")}
			}
			reply := &adapter.Reply{Content: "这是讲解。", Attachments: []adapter.Attachment{validPDFReplyAttachment()}}
			acks, err := a.SendPhotoReplyWithReceipts(t.Context(), "parent", reply)
			if (err != nil) != (lastStatus == adapter.DeliveryOutcomeUnknown) || len(acks) != 2 ||
				acks[0] != (adapter.DeliveryAck{ExternalMessageID: "pqk-0", Status: adapter.DeliveryAccepted}) || acks[1].Status != lastStatus {
				t.Fatalf("partial physical receipts were lost: %+v %v", acks, err)
			}
			if lastStatus == adapter.DeliveryAccepted && acks[1].ExternalMessageID != "pqk-1" {
				t.Fatalf("missing PDF receipt: %+v", acks)
			}
			if !slices.Equal(api.order, []string{"upload", "send", "send"}) {
				t.Fatalf("unexpected retry or part order: %v", api.order)
			}
		})
	}
}

func TestPhotoReaderFailureWithholdsACKAndRedactsDownloadURL(t *testing.T) {
	a, api, cleanup := newDurableAdmissionPictureAdapter(t)
	defer cleanup()
	api.downloadURL = "https://%zz/private?signature=ephemeral-secret"
	legacy := &inboundPhotoAdmissionProbe{handled: true}
	a.SetInboundPhotoAdmissionPort(legacy)
	a.SetPhotoIntake(testPhotoIntake(func(ctx context.Context, _ adapter.PhotoArrival, read adapter.PhotoPageReader) (bool, error) {
		if _, err := read(ctx, 1); err == nil {
			t.Fatal("read beyond the single-picture message")
		}
		_, err := read(ctx, 0)
		return false, err
	}))
	ack, err := a.onChatBotMessage(t.Context(), durableAdmissionPictureCallback("single"))
	if ack != nil || err == nil || strings.Contains(err.Error(), "ephemeral-secret") || strings.Contains(err.Error(), "%zz") ||
		len(legacy.captured()) != 0 || len(api.DownloadCalls()) != 1 {
		t.Fatalf("unexpected callback result for failed page: %q %v", ack, err)
	}
}
