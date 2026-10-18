// Package email 提供邮件适配器
//
// 通过 IMAP 轮询收件箱获取新邮件，通过 SMTP 发送回复。
// 将邮件转换为统一的 adapter.Message 格式。
package email

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/toolkit/util/logger"
)

// EmailConfig 邮件适配器配置
type EmailConfig struct {
	IMAP         IMAPConfig `yaml:"imap"`
	SMTP         SMTPConfig `yaml:"smtp"`
	PollInterval int        `yaml:"poll_interval"` // 轮询间隔（秒），默认 60
	MaxFetch     int        `yaml:"max_fetch"`     // 每次最多拉取邮件数，默认 10
}

// IMAPConfig IMAP 配置
type IMAPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"` // 默认 993
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	TLS      bool   `yaml:"tls"`    // 默认 true
	Folder   string `yaml:"folder"` // 默认 INBOX
}

// SMTPConfig SMTP 配置
type SMTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"` // 默认 587
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
}

// OutboundAttachment 是已经生成的邮件附件，不在 SMTP 发送阶段下载或重新生成文件。
type OutboundAttachment struct {
	Name string
	MIME string
	Data []byte
}

// OutboundMessage 区分显式主题和回复主题，同时保留纯文本与 HTML 备选正文。
// MessageID 由上层冻结后传入；它标识同一封邮件，不代表 SMTP 提供幂等去重。
type OutboundMessage struct {
	To          []string
	Subject     string
	Text        string
	HTML        string
	MessageID   string
	Attachments []OutboundAttachment
}

// EmailAdapter 邮件适配器
type EmailAdapter struct {
	cfg      EmailConfig
	handler  adapter.MessageHandler
	stopped  atomic.Bool
	dialIMAP func(context.Context, IMAPConfig) (imapSession, error)
}

// New 创建邮件适配器
func New(cfg EmailConfig) *EmailAdapter {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 60
	}
	if cfg.MaxFetch <= 0 {
		cfg.MaxFetch = 10
	}
	if cfg.IMAP.Port == 0 {
		cfg.IMAP.Port = 993
	}
	if cfg.IMAP.Folder == "" {
		cfg.IMAP.Folder = "INBOX"
	}
	if cfg.SMTP.Port == 0 {
		cfg.SMTP.Port = 587
	}
	return &EmailAdapter{cfg: cfg, dialIMAP: func(ctx context.Context, cfg IMAPConfig) (imapSession, error) {
		return dialIMAP(ctx, cfg)
	}}
}

func (a *EmailAdapter) Name() string               { return "email" }
func (a *EmailAdapter) Platform() adapter.Platform { return adapter.PlatformEmail }

// Start 启动邮件轮询
func (a *EmailAdapter) Start(ctx context.Context, handler adapter.MessageHandler) error {
	a.handler = handler
	a.stopped.Store(false)

	go a.pollLoop(ctx)
	logger.Info("邮件适配器已启动，轮询间隔", "interval_seconds", a.cfg.PollInterval)
	return nil
}

// Stop 停止轮询
func (a *EmailAdapter) Stop(_ context.Context) error {
	a.stopped.Store(true)
	logger.Info("邮件适配器已停止")
	return nil
}

// Send 发送邮件回复
func (a *EmailAdapter) Send(ctx context.Context, chatID string, reply *adapter.Reply) error {
	if reply == nil {
		return fmt.Errorf("email reply is required")
	}
	subject := "Re: HexClaw"
	msg := OutboundMessage{To: []string{chatID}, Text: reply.Content}
	if reply.Metadata != nil {
		if s, ok := reply.Metadata["subject"]; ok {
			subject = "Re: " + s
		}
		msg.HTML = reply.Metadata["html"]
		msg.MessageID = reply.Metadata["message_id"]
	}
	msg.Subject = subject
	for _, attachment := range reply.Attachments {
		if attachment.Data == "" {
			return fmt.Errorf("email attachment data is required")
		}
		data, err := base64.StdEncoding.DecodeString(attachment.Data)
		if err != nil {
			return fmt.Errorf("invalid email attachment data: %w", err)
		}
		msg.Attachments = append(msg.Attachments, OutboundAttachment{
			Name: attachment.Name, MIME: attachment.Mime, Data: data,
		})
	}
	_, err := a.SendMessage(ctx, msg)
	return err
}

// SendStream 缓冲流式内容后发送
func (a *EmailAdapter) SendStream(ctx context.Context, chatID string, chunks <-chan *adapter.ReplyChunk) error {
	var buf strings.Builder
	for chunk := range chunks {
		if chunk.Error != nil {
			return chunk.Error
		}
		buf.WriteString(chunk.Content)
	}
	return a.Send(ctx, chatID, &adapter.Reply{Content: buf.String()})
}

// ValidateConfig validates IMAP/SMTP configuration by attempting an IMAP login.
func (a *EmailAdapter) ValidateConfig(ctx context.Context) error {
	if a.cfg.IMAP.Host == "" || a.cfg.IMAP.Username == "" || a.cfg.IMAP.Password == "" {
		return fmt.Errorf("email imap host/username/password 未配置")
	}
	if a.cfg.SMTP.Host == "" || a.cfg.SMTP.Username == "" || a.cfg.SMTP.Password == "" {
		return fmt.Errorf("email smtp host/username/password 未配置")
	}
	if a.cfg.SMTP.From == "" {
		return fmt.Errorf("email smtp from 未配置")
	}
	client, err := a.dialIMAP(ctx, a.cfg.IMAP)
	if err != nil {
		return fmt.Errorf("email IMAP 连接失败: %w", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.Login(a.cfg.IMAP.Username, a.cfg.IMAP.Password); err != nil {
		return fmt.Errorf("email IMAP 登录失败: %w", err)
	}
	_ = client.Logout()
	return nil
}

// smtpConnectError 把连接层失败（无法建连 / TLS 握手失败 / 读 greeting 得 EOF/重置）包装成
// 清晰、可操作的"连接失败"消息——与"认证失败"明确区分，避免用户误以为是密码问题。
// 典型触发：境外邮箱（如 Gmail）在当前网络被重置，或所经系统代理未生效。
func smtpConnectError(cfg SMTPConfig, err error) error {
	return fmt.Errorf("SMTP 连接失败：无法与 %s:%d 建立可用连接（%w）。请确认服务器地址与端口正确并检查网络连通性；若访问 Gmail 等境外邮箱受网络限制，请在系统中配置代理后重试", cfg.Host, cfg.Port, err)
}

// ProbeSMTP 对 SMTP 凭据做一次"连接级"探测：建立连接、EHLO、按服务器能力 STARTTLS、
// 然后 AUTH，最后 QUIT。它**不发送任何邮件**，仅用于"测试连接"场景验证 host/port/账号/口令
// 是否可用。凭据只在本次调用内瞬态使用，绝不持久化，调用方也不应记录其内容。
//
// ctx 控制整体超时；465 端口默认隐式 TLS，其余端口先明文连接再按服务器 STARTTLS 能力升级。
func ProbeSMTP(ctx context.Context, cfg SMTPConfig) error {
	if cfg.Host == "" || cfg.Username == "" || cfg.Password == "" {
		return fmt.Errorf("smtp host/username/password 未配置")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	// 经环境代理（或直连）建立连接：465 隐式 TLS，其余端口先明文（随后按服务器能力 STARTTLS）。
	// 走 dialTCPContext/dialTLSContext 而非裸 tls.Dial，使邮件与应用其余 HTTP 流量共用代理出口。
	var (
		conn net.Conn
		err  error
	)
	if cfg.Port == 465 {
		conn, err = dialTLSContext(ctx, cfg.Host, cfg.Port)
	} else {
		conn, err = dialTCPContext(ctx, cfg.Host, cfg.Port)
	}
	if err != nil {
		return smtpConnectError(cfg, err)
	}
	// 用 ctx 的 deadline（若有）约束后续 I/O，避免恶意/缓慢服务器拖死探测。
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		// EOF/重置发生在读取 greeting 阶段（认证尚未发生）→ 属连接层失败，不能报"认证失败"。
		return smtpConnectError(cfg, err)
	}
	defer func() {
		// QUIT 关闭会话；失败不影响探测结论，但仍尽力关闭底层连接。
		if quitErr := client.Quit(); quitErr != nil {
			_ = conn.Close()
		}
	}()

	// 端口 465 已是隐式 TLS；其余端口若服务器支持 STARTTLS 则升级。
	if cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return fmt.Errorf("SMTP STARTTLS 失败：%w", err)
			}
		}
	}

	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("SMTP 认证失败：%w。请确认用户名与密码（或应用专用密码）是否正确", err)
	}
	return nil
}

func (a *EmailAdapter) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(a.cfg.PollInterval) * time.Second)
	defer ticker.Stop()

	// 首次立即拉取
	a.fetchAndProcess(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if a.stopped.Load() {
				return
			}
			a.fetchAndProcess(ctx)
		}
	}
}

func (a *EmailAdapter) fetchAndProcess(ctx context.Context) {
	if a.handler == nil {
		return
	}

	logger.Info("邮件适配器: 检查新邮件 (", "username", a.cfg.IMAP.Username, "host", a.cfg.IMAP.Host, "port", a.cfg.IMAP.Port)

	client, err := a.dialIMAP(ctx, a.cfg.IMAP)
	if err != nil {
		logger.Error("邮件适配器: IMAP 连接失败", "error", err)
		return
	}
	defer func() { _ = client.Close() }()

	if err := client.Login(a.cfg.IMAP.Username, a.cfg.IMAP.Password); err != nil {
		logger.Error("邮件适配器: IMAP 登录失败", "error", err)
		return
	}
	defer func() {
		if err := client.Logout(); err != nil {
			logger.Error("邮件适配器: IMAP 登出失败", "error", err)
		}
	}()

	if err := client.Select(a.cfg.IMAP.Folder); err != nil {
		logger.Error("邮件适配器: 选择邮箱失败", "error", err)
		return
	}

	ids, err := client.SearchUnseen(a.cfg.MaxFetch)
	if err != nil {
		logger.Error("邮件适配器: 搜索未读邮件失败", "error", err)
		return
	}

	for _, id := range ids {
		// W3-6: 处理每封邮件前检查 stopped 标志。Stop 后在途轮询不应继续处理回复,
		// 否则会在适配器已停止后仍调用 handler 并发送回复。
		if a.stopped.Load() {
			return
		}

		raw, err := client.FetchRFC822(id)
		if err != nil {
			logger.Error("邮件适配器: 拉取邮件失败: id", "id", id, "err", err)
			continue
		}

		msg, subject, err := parseIncomingEmail(raw)
		if err != nil {
			logger.Error("邮件适配器: 解析邮件失败: id", "id", id, "err", err)
			continue
		}

		reply, err := a.handler(ctx, msg)
		if err != nil {
			logger.Error("邮件适配器: 处理邮件失败: id", "id", id, "err", err)
			continue
		}
		if reply != nil {
			if reply.Metadata == nil {
				reply.Metadata = make(map[string]string, 1)
			}
			if _, ok := reply.Metadata["subject"]; !ok && subject != "" {
				reply.Metadata["subject"] = subject
			}
			if err := a.Send(ctx, msg.ChatID, reply); err != nil {
				logger.Error("邮件适配器: 发送回复失败: id", "id", id, "err", err)
			}
		}

		if err := client.MarkSeen(id); err != nil {
			logger.Error("邮件适配器: 标记已读失败: id", "id", id, "err", err)
		}
	}
}

type imapClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	tagSeq int
}

type imapSession interface {
	Close() error
	Login(username, password string) error
	Select(folder string) error
	SearchUnseen(maxFetch int) ([]string, error)
	FetchRFC822(id string) ([]byte, error)
	MarkSeen(id string) error
	Logout() error
}

func dialIMAP(ctx context.Context, cfg IMAPConfig) (*imapClient, error) {
	// 经环境代理（或直连）建立连接，使 IMAP 与应用其余 HTTP 流量共用代理出口。
	var (
		conn net.Conn
		err  error
	)
	if cfg.TLS {
		conn, err = dialTLSContext(ctx, cfg.Host, cfg.Port)
	} else {
		conn, err = dialTCPContext(ctx, cfg.Host, cfg.Port)
	}
	if err != nil {
		return nil, err
	}

	client := &imapClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	line, err := client.readLine()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取 greeting 失败: %w", err)
	}
	if !strings.HasPrefix(line, "* OK") {
		_ = conn.Close()
		return nil, fmt.Errorf("IMAP greeting 非 OK: %s", strings.TrimSpace(line))
	}
	return client, nil
}

func (c *imapClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *imapClient) Login(username, password string) error {
	return c.runSimple("LOGIN %q %q", username, password)
}

func (c *imapClient) Select(folder string) error {
	return c.runSimple("SELECT %q", folder)
}

func (c *imapClient) MarkSeen(id string) error {
	return c.runSimple("STORE %s +FLAGS (\\Seen)", id)
}

func (c *imapClient) Logout() error {
	return c.runSimple("LOGOUT")
}

func (c *imapClient) SearchUnseen(maxFetch int) ([]string, error) {
	res, err := c.run("SEARCH UNSEEN")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range res.lines {
		if !strings.HasPrefix(line, "* SEARCH") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 2 {
			ids = append(ids, fields[2:]...)
		}
	}
	if maxFetch > 0 && len(ids) > maxFetch {
		ids = ids[:maxFetch]
	}
	return ids, nil
}

func (c *imapClient) FetchRFC822(id string) ([]byte, error) {
	res, err := c.run("FETCH %s RFC822", id)
	if err != nil {
		return nil, err
	}
	if len(res.literals) == 0 {
		return nil, fmt.Errorf("FETCH %s 未返回 RFC822 内容", id)
	}
	return bytes.TrimRight(res.literals[0], "\r\n"), nil
}

type imapResponse struct {
	lines    []string
	literals [][]byte
}

func (c *imapClient) runSimple(format string, args ...any) error {
	_, err := c.run(format, args...)
	return err
}

func (c *imapClient) run(format string, args ...any) (*imapResponse, error) {
	tag := c.nextTag()
	cmd := fmt.Sprintf(format, args...)
	if _, err := c.writer.WriteString(tag + " " + cmd + "\r\n"); err != nil {
		return nil, err
	}
	if err := c.writer.Flush(); err != nil {
		return nil, err
	}

	resp := &imapResponse{}
	for {
		line, err := c.readLine()
		if err != nil {
			return nil, err
		}
		resp.lines = append(resp.lines, line)

		if size, ok := parseLiteralSize(line); ok {
			literal := make([]byte, size)
			if _, err := io.ReadFull(c.reader, literal); err != nil {
				return nil, err
			}
			resp.literals = append(resp.literals, literal)
			continue
		} else if hasLiteralMarker(line) {
			// 行尾带 {...} literal 标记但 parseLiteralSize 拒绝(负数/非数字等畸形),
			// 属于协议层畸形数据。直接返回 error, 避免被当作普通行继续读取导致
			// 后续数据流错位, 也防止 pollLoop 协程被恶意服务器干扰。
			return nil, fmt.Errorf("IMAP %s 收到畸形 literal 标记: %s", cmd, strings.TrimSpace(line))
		}

		if strings.HasPrefix(line, tag+" ") {
			if !strings.Contains(line, " OK") {
				return nil, fmt.Errorf("IMAP %s 失败: %s", cmd, strings.TrimSpace(line))
			}
			return resp, nil
		}
	}
}

func (c *imapClient) nextTag() string {
	c.tagSeq++
	return fmt.Sprintf("A%04d", c.tagSeq)
}

func (c *imapClient) readLine() (string, error) {
	line, err := c.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return line, nil
}

func parseLiteralSize(line string) (int, bool) {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	start := strings.LastIndexByte(line, '{')
	end := strings.LastIndexByte(line, '}')
	if start < 0 || end <= start+1 || end != len(line)-1 {
		return 0, false
	}
	size, err := strconv.Atoi(line[start+1 : end])
	if err != nil {
		return 0, false
	}
	// IMAP literal 大小语义上必须 >= 0。负数(畸形或恶意服务器)若被当作合法大小,
	// 会导致 run() 中 make([]byte, size) 触发 panic 崩溃轮询协程, 因此一律拒绝。
	if size < 0 {
		return 0, false
	}
	return size, true
}

// hasLiteralMarker 判断行尾是否带有 IMAP literal 标记({...})的形状。
//
// 与 parseLiteralSize 不同, 本函数只判断"形状"是否像 literal 标记, 不校验内容合法性。
// run() 用它区分两种 parseLiteralSize 失败的情况:
//   - 普通响应行(根本没有 literal 标记): 正常继续处理
//   - 畸形 literal 标记(如负数大小 {-3}、非数字 {abc}): 协议层错误, 应返回 error
func hasLiteralMarker(line string) bool {
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	start := strings.LastIndexByte(line, '{')
	end := strings.LastIndexByte(line, '}')
	return start >= 0 && end == len(line)-1 && end > start
}

func parseIncomingEmail(raw []byte) (*adapter.Message, string, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}

	fromName, fromEmail := ParseEmailAddress(m.Header.Get("From"))
	subject := m.Header.Get("Subject")
	body, err := io.ReadAll(m.Body)
	if err != nil {
		return nil, "", err
	}

	ts := time.Now()
	if date := m.Header.Get("Date"); date != "" {
		if parsed, err := mail.ParseDate(date); err == nil {
			ts = parsed
		}
	}

	msgID := strings.TrimSpace(strings.Trim(m.Header.Get("Message-ID"), "<>"))
	if msgID == "" {
		msgID = fmt.Sprintf("email-%d", ts.UnixNano())
	}

	return &adapter.Message{
		ID:       msgID,
		Platform: adapter.PlatformEmail,
		ChatID:   fromEmail,
		UserID:   fromEmail,
		UserName: fromName,
		Content:  strings.TrimSpace(string(body)),
		Metadata: map[string]string{
			"subject":    subject,
			"from_email": fromEmail,
		},
		Timestamp: ts,
	}, subject, nil
}

// sanitizeHeader 过滤 SMTP 头部注入字符
func sanitizeHeader(s string) string {
	r := strings.NewReplacer("\r", "", "\n", "")
	return r.Replace(s)
}

// encodeSubject 对邮件 subject 做 RFC2047 编码。
//
// W3-7: 出站 subject 直接以原始 UTF-8 字节写入头部时, 不兼容 RFC2047 的收件端会乱码。
// 使用标准库 mime.QEncoding 生成 encoded-word(=?utf-8?q?...?=); 纯 ASCII 输入保持不变,
// 编码输出为纯 ASCII 且不含裸 CR/LF, 与 sanitizeHeader 协同防止头部注入。
func encodeSubject(subject string) string {
	return mime.QEncoding.Encode("utf-8", subject)
}

type outboundEnvelope struct {
	sender     string
	recipients []string
	messageID  string
	data       []byte
}

// SendMessage 直接使用已配置的 SMTP 连接发送一封完整邮件，不要求启动 IMAP。
// accepted 仅表示 SMTP 已受理；DATA 结束后未获得确认时返回 outcome_unknown，调用方不得盲目重发。
func (a *EmailAdapter) SendMessage(ctx context.Context, msg OutboundMessage) (adapter.DeliveryAck, error) {
	ack := adapter.DeliveryAck{Status: adapter.DeliveryFailed}
	envelope, err := composeOutboundMessage(a.cfg.SMTP.From, msg)
	if err != nil {
		return ack, err
	}
	ack.ExternalMessageID = envelope.messageID
	cfg := a.cfg.SMTP
	if cfg.Host == "" || cfg.Username == "" || cfg.Password == "" {
		return ack, fmt.Errorf("SMTP host, username and password are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 与连接探测共用代理、隐式 TLS 和 STARTTLS 策略。
	var conn net.Conn
	if cfg.Port == 465 {
		conn, err = dialTLSContext(ctx, cfg.Host, cfg.Port)
	} else {
		conn, err = dialTCPContext(ctx, cfg.Host, cfg.Port)
	}
	if err != nil {
		return ack, fmt.Errorf("SMTP connection failed: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return ack, fmt.Errorf("SMTP greeting failed: %w", err)
	}
	defer func() { _ = client.Close() }()
	if cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return ack, fmt.Errorf("SMTP STARTTLS failed: %w", err)
			}
		}
	}
	if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
		return ack, fmt.Errorf("SMTP authentication failed: %w", err)
	}
	if err := client.Mail(envelope.sender); err != nil {
		return ack, fmt.Errorf("SMTP sender rejected: %w", err)
	}
	for _, recipient := range envelope.recipients {
		if err := client.Rcpt(recipient); err != nil {
			return ack, fmt.Errorf("SMTP recipient rejected: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return ack, fmt.Errorf("SMTP DATA rejected: %w", err)
	}
	if _, err := w.Write(envelope.data); err != nil {
		// 尚未提交 DATA 终止符，服务器不能把不完整邮件视为已受理。
		return ack, fmt.Errorf("SMTP message write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		var response *textproto.Error
		if !errors.As(err, &response) || response.Code < 400 || response.Code >= 600 {
			ack.Status = adapter.DeliveryOutcomeUnknown
		}
		return ack, fmt.Errorf("SMTP submission failed: %w", err)
	}
	ack.Status = adapter.DeliveryAccepted
	// 已取得 DATA 250 后，QUIT 失败不改变受理事实。
	_ = client.Quit()
	return ack, nil
}

func composeOutboundMessage(from string, msg OutboundMessage) (outboundEnvelope, error) {
	var envelope outboundEnvelope
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return envelope, fmt.Errorf("invalid email sender: %w", err)
	}
	envelope.sender = sender.Address
	var recipients []string
	for _, raw := range msg.To {
		addresses, err := mail.ParseAddressList(raw)
		if err != nil {
			return envelope, fmt.Errorf("invalid email recipient: %w", err)
		}
		for _, address := range addresses {
			envelope.recipients = append(envelope.recipients, address.Address)
			recipients = append(recipients, address.String())
		}
	}
	if len(recipients) == 0 {
		return envelope, fmt.Errorf("at least one email recipient is required")
	}
	messageID := strings.TrimSpace(msg.MessageID)
	if messageID == "" {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return envelope, fmt.Errorf("email message ID creation failed: %w", err)
		}
		_, domain, _ := strings.Cut(sender.Address, "@")
		messageID = hex.EncodeToString(token[:]) + "@" + domain
	} else {
		messageID = strings.TrimPrefix(strings.TrimSuffix(messageID, ">"), "<")
	}
	if strings.Count(messageID, "@") != 1 || strings.ContainsAny(messageID, "<> \t\r\n") {
		return envelope, fmt.Errorf("invalid email message ID")
	}
	envelope.messageID = "<" + messageID + ">"
	contentType, encoding, body, err := composeOutboundBody(msg)
	if err != nil {
		return envelope, err
	}
	var data bytes.Buffer
	fmt.Fprintf(&data, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: %s\r\nMIME-Version: 1.0\r\nContent-Type: %s\r\n",
		sender.String(), strings.Join(recipients, ", "), sanitizeHeader(encodeSubject(msg.Subject)),
		time.Now().Format(time.RFC1123Z), envelope.messageID, contentType)
	if encoding != "" {
		fmt.Fprintf(&data, "Content-Transfer-Encoding: %s\r\n", encoding)
	}
	data.WriteString("\r\n")
	data.Write(body)
	envelope.data = data.Bytes()
	return envelope, nil
}

func composeOutboundBody(msg OutboundMessage) (string, string, []byte, error) {
	var body bytes.Buffer
	encoding := "quoted-printable"
	contentType := "text/plain; charset=UTF-8"
	if msg.HTML == "" {
		writer := quotedprintable.NewWriter(&body)
		if _, err := io.WriteString(writer, msg.Text); err != nil {
			return "", "", nil, err
		}
		if err := writer.Close(); err != nil {
			return "", "", nil, err
		}
	} else {
		alternative := multipart.NewWriter(&body)
		for _, part := range []struct{ mediaType, content string }{
			{"text/plain; charset=UTF-8", msg.Text}, {"text/html; charset=UTF-8", msg.HTML},
		} {
			writer, err := alternative.CreatePart(textproto.MIMEHeader{
				"Content-Type": {part.mediaType}, "Content-Transfer-Encoding": {"quoted-printable"},
			})
			if err != nil {
				return "", "", nil, err
			}
			encoded := quotedprintable.NewWriter(writer)
			if _, err := io.WriteString(encoded, part.content); err != nil {
				return "", "", nil, err
			}
			if err := encoded.Close(); err != nil {
				return "", "", nil, err
			}
		}
		if err := alternative.Close(); err != nil {
			return "", "", nil, err
		}
		contentType = mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alternative.Boundary()})
		encoding = ""
	}
	if len(msg.Attachments) == 0 {
		return contentType, encoding, body.Bytes(), nil
	}
	var mixedBody bytes.Buffer
	mixed := multipart.NewWriter(&mixedBody)
	header := textproto.MIMEHeader{"Content-Type": {contentType}}
	if encoding != "" {
		header.Set("Content-Transfer-Encoding", encoding)
	}
	part, err := mixed.CreatePart(header)
	if err != nil {
		return "", "", nil, err
	}
	if _, err := part.Write(body.Bytes()); err != nil {
		return "", "", nil, err
	}
	for _, attachment := range msg.Attachments {
		mediaType := attachment.MIME
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		mediaType, params, err := mime.ParseMediaType(mediaType)
		if err != nil {
			return "", "", nil, fmt.Errorf("invalid email attachment MIME type: %w", err)
		}
		name := attachment.Name
		if name == "" {
			name = "attachment"
		}
		params["name"] = name
		part, err := mixed.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {mime.FormatMediaType(mediaType, params)},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": name})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return "", "", nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(attachment.Data)
		for len(encoded) > 0 {
			n := min(76, len(encoded))
			if _, err := io.WriteString(part, encoded[:n]+"\r\n"); err != nil {
				return "", "", nil, err
			}
			encoded = encoded[n:]
		}
	}
	if err := mixed.Close(); err != nil {
		return "", "", nil, err
	}
	return mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mixed.Boundary()}), "", mixedBody.Bytes(), nil
}

// ParseEmailAddress 从 "Name <email>" 格式提取邮箱地址
func ParseEmailAddress(raw string) (name, email string) {
	re := regexp.MustCompile(`(?:(.+?)\s*)?<([^>]+)>`)
	matches := re.FindStringSubmatch(raw)
	if len(matches) >= 3 {
		return strings.TrimSpace(matches[1]), matches[2]
	}
	return "", strings.TrimSpace(raw)
}
