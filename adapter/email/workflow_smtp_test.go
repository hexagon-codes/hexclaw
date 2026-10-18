package email_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/adapter/email"
)

type smtpCapture struct {
	raw        []byte
	recipients []string
	err        error
}

// workflowSMTP 只在本机回环上执行 SMTP 协议，不连接真实邮箱或发送外部邮件。
func workflowSMTP(t *testing.T, finalReply string) (email.SMTPConfig, <-chan smtpCapture) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan smtpCapture, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		reader := textproto.NewReader(bufio.NewReader(connection))
		writer := bufio.NewWriter(connection)
		write := func(response string) bool {
			_, err := fmt.Fprintf(writer, "%s\r\n", response)
			if err != nil {
				return false
			}
			return writer.Flush() == nil
		}
		if !write("220 workflow-test ESMTP") {
			return
		}
		var recipients []string
		for {
			line, err := reader.ReadLine()
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO "):
				if !write("250-workflow-test\r\n250 AUTH PLAIN") {
					return
				}
			case strings.HasPrefix(line, "AUTH "):
				if !write("235 2.7.0 authenticated") {
					return
				}
			case strings.HasPrefix(line, "MAIL FROM:"):
				if !write("250 sender accepted") {
					return
				}
			case strings.HasPrefix(line, "RCPT TO:"):
				recipients = append(recipients, strings.TrimPrefix(line, "RCPT TO:"))
				if !write("250 recipient accepted") {
					return
				}
			case line == "DATA":
				if finalReply == "before-data" {
					_ = write("550 message rejected before DATA")
					return
				}
				if !write("354 end with a dot") {
					return
				}
				raw, err := io.ReadAll(reader.DotReader())
				captured <- smtpCapture{raw: raw, recipients: recipients, err: err}
				if err != nil || finalReply == "lost" {
					return
				}
				if finalReply == "wait" {
					_, _ = reader.ReadLine()
					return
				}
				if !write(finalReply) {
					return
				}
				if strings.HasPrefix(finalReply, "250") {
					// 受理后立即断开，验证 QUIT 失败不会抹去已取得的 250。
					return
				}
			case line == "QUIT":
				_ = write("221 bye")
				return
			default:
				if !write("500 unsupported command") {
					return
				}
			}
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return email.SMTPConfig{
		Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port,
		Username: "sender@example.test", Password: "local-only",
		From: "HexClaw <sender@example.test>",
	}, captured
}

func readSMTPCapture(t *testing.T, captured <-chan smtpCapture) smtpCapture {
	t.Helper()
	select {
	case got := <-captured:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP did not receive a complete message")
		return smtpCapture{}
	}
}

func TestWorkflowSMTPMIMEAndAcceptedReceipt(t *testing.T) {
	cfg, captured := workflowSMTP(t, "250 2.0.0 queued")
	client := email.New(email.EmailConfig{SMTP: cfg})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	attachment := []byte("%PDF-1.7\n工作流结果\n")
	ack, err := client.SendMessage(ctx, email.OutboundMessage{
		To:      []string{"收件人 <first@example.test>", "second@example.test"},
		Subject: "每日工作流报告", Text: "报告纯文本\n.正文点号\n", HTML: "<h1>报告正文</h1>",
		MessageID:   "workflow-run-delivery@example.test",
		Attachments: []email.OutboundAttachment{{Name: "每日工作流报告.pdf", MIME: "application/pdf", Data: attachment}},
	})
	if err != nil || ack.Status != adapter.DeliveryAccepted || ack.ExternalMessageID != "<workflow-run-delivery@example.test>" {
		t.Fatalf("expected SMTP acceptance and frozen message ID, got %+v, %v", ack, err)
	}
	got := readSMTPCapture(t, captured)
	if strings.Join(got.recipients, ",") != "<first@example.test>,<second@example.test>" {
		t.Fatalf("wrong SMTP recipients: %v", got.recipients)
	}
	message, err := mail.ReadMessage(bytes.NewReader(got.raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || subject != "每日工作流报告" {
		t.Fatalf("explicit subject was changed: %q, %v", subject, err)
	}
	if message.Header.Get("Bcc") != "" || message.Header.Get("Message-ID") != ack.ExternalMessageID {
		t.Fatalf("unexpected recipient header or changed message identity: %v", message.Header)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("expected MIME mixed, got %q, %v", mediaType, err)
	}
	mixed := multipart.NewReader(message.Body, params["boundary"])
	body, err := mixed.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err = mime.ParseMediaType(body.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("expected text and HTML alternatives, got %q, %v", mediaType, err)
	}
	alternatives := multipart.NewReader(body, params["boundary"])
	for _, want := range []struct{ mediaType, body string }{
		{"text/plain", "报告纯文本\n.正文点号\n"}, {"text/html", "<h1>报告正文</h1>"},
	} {
		part, err := alternatives.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		mediaType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		data, readErr := io.ReadAll(part)
		if err != nil || readErr != nil || mediaType != want.mediaType || string(data) != want.body {
			t.Fatalf("MIME alternative changed: type=%q body=%q errors=%v/%v", mediaType, data, err, readErr)
		}
	}
	if _, err := alternatives.NextPart(); err != io.EOF {
		t.Fatalf("unexpected alternative: %v", err)
	}
	file, err := mixed.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	_, disposition, err := mime.ParseMediaType(file.Header.Get("Content-Disposition"))
	if err != nil || disposition["filename"] != "每日工作流报告.pdf" {
		t.Fatalf("attachment filename changed: %v, %v", disposition, err)
	}
	data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, file))
	if err != nil || !bytes.Equal(data, attachment) {
		t.Fatalf("attachment bytes changed: %q, %v", data, err)
	}
	if _, err := mixed.NextPart(); err != io.EOF {
		t.Fatalf("unexpected attachment: %v", err)
	}
}

func TestWorkflowSMTPSubmissionFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		want           adapter.DeliveryStatus
	}{
		{"before DATA", "before-data", adapter.DeliveryFailed},
		{"explicit rejection", "550 5.7.1 rejected", adapter.DeliveryFailed},
		{"temporary rejection", "451 4.3.0 try later", adapter.DeliveryFailed},
		{"confirmation lost", "lost", adapter.DeliveryOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := workflowSMTP(t, tc.response)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ack, err := email.New(email.EmailConfig{SMTP: cfg}).SendMessage(ctx, email.OutboundMessage{
				To: []string{"recipient@example.test"}, Subject: "report", Text: "result",
				MessageID: "same-run@example.test",
			})
			if err == nil || ack.Status != tc.want || ack.ExternalMessageID != "<same-run@example.test>" {
				t.Fatalf("wrong submission result: %+v, %v", ack, err)
			}
		})
	}
}

func TestWorkflowSMTPCancellationAfterSubmissionIsUnknown(t *testing.T) {
	cfg, captured := workflowSMTP(t, "wait")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		ack adapter.DeliveryAck
		err error
	}
	completed := make(chan result, 1)
	go func() {
		ack, err := email.New(email.EmailConfig{SMTP: cfg}).SendMessage(ctx, email.OutboundMessage{
			To: []string{"recipient@example.test"}, Subject: "report", Text: "result",
			MessageID: "cancelled-run@example.test",
		})
		completed <- result{ack, err}
	}()
	_ = readSMTPCapture(t, captured)
	cancel()
	select {
	case got := <-completed:
		if got.err == nil || got.ack.Status != adapter.DeliveryOutcomeUnknown {
			t.Fatalf("submitted cancellation must remain unknown: %+v, %v", got.ack, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP did not observe caller cancellation")
	}
}

func TestWorkflowSMTPLegacyReplyKeepsAttachments(t *testing.T) {
	cfg, captured := workflowSMTP(t, "250 accepted")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := email.New(email.EmailConfig{SMTP: cfg}).Send(ctx, "recipient@example.test", &adapter.Reply{
		Content: "reply", Metadata: map[string]string{"subject": "original"},
		Attachments: []adapter.Attachment{{Name: "report.md", Mime: "text/markdown", Data: base64.StdEncoding.EncodeToString([]byte("# result"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := readSMTPCapture(t, captured)
	message, err := mail.ReadMessage(bytes.NewReader(got.raw))
	if err != nil {
		t.Fatal(err)
	}
	if message.Header.Get("Subject") != "Re: original" || !bytes.Contains(got.raw, []byte("report.md")) {
		t.Fatalf("reply subject or attachment was dropped: %v", message.Header)
	}
}
