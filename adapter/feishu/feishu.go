// Package feishu 提供飞书（Lark）Bot 适配器
//
// 通过 WebSocket 长连接接收飞书事件，无需公网地址。
// 回复通过飞书 OpenAPI 发送。
//
// 支持的功能：
//   - WebSocket 长连接（客户端主动连接飞书服务器）
//   - 接收文本消息（单聊/群聊 @机器人）
//   - Access Token 缓存（2 小时有效期）
//   - 消息回复（同步/流式拼接后发送）
//   - 自动重连与心跳维持
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

const (
	baseURL            = "https://open.feishu.cn/open-apis"
	tokenRefreshBuffer = 5 * time.Minute
)

// FeishuAdapter 飞书 Bot 适配器
//
// 通过 WebSocket 长连接接收飞书事件，将文本消息转换为统一 Message。
// 回复通过飞书 OpenAPI 发送。
type FeishuAdapter struct {
	cfg     config.FeishuConfig
	handler adapter.MessageHandler
	client  *http.Client
	queue   *adapter.SendQueue

	mu          sync.RWMutex
	accessToken string
	tokenExpiry time.Time

	conn    *websocket.Conn
	connMu  sync.Mutex
	stopped atomic.Bool
}

// New 创建飞书适配器
func New(cfg config.FeishuConfig) *FeishuAdapter {
	a := &FeishuAdapter{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	a.queue = adapter.NewPlatformSendQueue(adapter.PlatformFeishu, a.sendReplyNow)
	return a
}

func (a *FeishuAdapter) Name() string {
	if a.cfg.Name != "" {
		return a.cfg.Name
	}
	return "feishu"
}
func (a *FeishuAdapter) Platform() adapter.Platform { return adapter.PlatformFeishu }

// Attach 注册消息处理器。
func (a *FeishuAdapter) Attach(handler adapter.MessageHandler) error {
	a.handler = handler
	return nil
}

// Start 启动飞书 WebSocket 长连接
//
// 向飞书申请 WebSocket 端点，建立长连接接收事件。
// 支持自动重连和心跳维持。
func (a *FeishuAdapter) Start(_ context.Context, handler adapter.MessageHandler) error {
	if err := a.Attach(handler); err != nil {
		return err
	}

	a.stopped.Store(false)
	go a.connectLoop()
	log.Printf("飞书适配器 [%s] 已启动（WebSocket 长连接模式）", a.Name())
	return nil
}

// Stop 停止飞书适配器
func (a *FeishuAdapter) Stop(ctx context.Context) error {
	a.stopped.Store(true)
	if a.queue != nil {
		_ = a.queue.Stop(context.Background())
	}

	a.connMu.Lock()
	if a.conn != nil {
		_ = a.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = a.conn.Close()
		a.conn = nil
	}
	a.connMu.Unlock()

	log.Println("飞书适配器停止中...")
	return nil
}

// Handler 返回 HTTP Handler（保留向后兼容，WebSocket 模式下不使用）
func (a *FeishuAdapter) Handler() http.Handler {
	return http.HandlerFunc(a.handleWebhook)
}

// ============== WebSocket 长连接 ==============

// connectLoop 自动重连循环
func (a *FeishuAdapter) connectLoop() {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for !a.stopped.Load() {
		if err := a.connectAndListen(); err != nil {
			if !a.stopped.Load() {
				log.Printf("飞书 WebSocket 断开: %v，%v 后重连...", err, backoff)
				time.Sleep(backoff)
				backoff = min(backoff*2, maxBackoff)
			}
		} else {
			backoff = time.Second
		}
	}
}

// connectAndListen 建立 WebSocket 连接并监听事件
func (a *FeishuAdapter) connectAndListen() error {
	ctx := context.Background()

	token, err := a.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("获取 Access Token 失败: %w", err)
	}

	wsURL, pingInterval, err := a.getWSEndpoint(ctx, token)
	if err != nil {
		return fmt.Errorf("获取 WebSocket 端点失败: %w", err)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("WebSocket 连接失败: %w", err)
	}

	a.connMu.Lock()
	a.conn = conn
	a.connMu.Unlock()

	defer func() {
		_ = conn.Close()
		a.connMu.Lock()
		a.conn = nil
		a.connMu.Unlock()
	}()

	log.Printf("飞书 WebSocket 连接已建立")

	if pingInterval <= 0 {
		pingInterval = 120
	}

	stopPing := make(chan struct{})
	go a.pingLoop(conn, time.Duration(pingInterval)*time.Second, stopPing)
	defer close(stopPing)

	for !a.stopped.Load() {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if !a.stopped.Load() {
				return fmt.Errorf("读取消息失败: %w", err)
			}
			return nil
		}
		a.handleWSMessage(msg)
	}
	return nil
}

// getWSEndpoint 获取飞书 WebSocket 连接端点
func (a *FeishuAdapter) getWSEndpoint(ctx context.Context, token string) (wsURL string, pingInterval int, err error) {
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/callback/ws/endpoint", nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("请求 WebSocket 端点失败: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			URL          string `json:"URL"`
			ClientConfig struct {
				ReconnectCount    int `json:"ReconnectCount"`
				ReconnectInterval int `json:"ReconnectInterval"`
				ReconnectNonce    int `json:"ReconnectNonce"`
				PingInterval      int `json:"PingInterval"`
			} `json:"ClientConfig"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, fmt.Errorf("解析 WebSocket 端点响应失败: %w", err)
	}
	if result.Code != 0 {
		return "", 0, fmt.Errorf("飞书 API 错误: code=%d, msg=%s", result.Code, result.Msg)
	}
	if result.Data.URL == "" {
		return "", 0, fmt.Errorf("飞书返回的 WebSocket URL 为空")
	}

	return result.Data.URL, result.Data.ClientConfig.PingInterval, nil
}

// wsFrame 飞书 WebSocket 消息帧
type wsFrame struct {
	Type    string            `json:"type"`
	Headers map[string]string `json:"headers,omitempty"`
	Data    string            `json:"data,omitempty"`
}

// handleWSMessage 处理 WebSocket 收到的消息
func (a *FeishuAdapter) handleWSMessage(raw []byte) {
	var frame wsFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		log.Printf("飞书 WebSocket: 解析消息帧失败: %v", err)
		return
	}

	switch frame.Type {
	case "ping":
		a.sendWSPong(frame)
	case "pong":
		// 收到 pong 响应，无需处理
	case "event":
		a.handleWSEvent(frame)
	default:
		log.Printf("飞书 WebSocket: 未知消息类型: %s", frame.Type)
	}
}

// sendWSPong 响应 ping 消息
func (a *FeishuAdapter) sendWSPong(ping wsFrame) {
	pong := wsFrame{
		Type:    "pong",
		Headers: ping.Headers,
	}
	data, _ := json.Marshal(pong)

	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.conn != nil {
		_ = a.conn.WriteMessage(websocket.TextMessage, data)
	}
}

// pingLoop 定期发送 ping 保持连接
func (a *FeishuAdapter) pingLoop(conn *websocket.Conn, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ping := wsFrame{
				Type: "ping",
			}
			data, _ := json.Marshal(ping)
			a.connMu.Lock()
			err := conn.WriteMessage(websocket.TextMessage, data)
			a.connMu.Unlock()
			if err != nil {
				return
			}
		case <-stop:
			return
		}
	}
}

// handleWSEvent 处理 WebSocket 事件消息
func (a *FeishuAdapter) handleWSEvent(frame wsFrame) {
	if frame.Data == "" {
		return
	}

	var event feishuEvent
	if err := json.Unmarshal([]byte(frame.Data), &event); err != nil {
		log.Printf("飞书 WebSocket: 解析事件数据失败: %v", err)
		return
	}

	if event.Header.EventType == "im.message.receive_v1" {
		go a.handleMessage(event)
	}
}

// handleWebhook 处理飞书事件回调（向后兼容 HTTP Webhook）
func (a *FeishuAdapter) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var event feishuEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "解析事件失败", http.StatusBadRequest)
		return
	}

	if event.Challenge != "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"challenge": event.Challenge,
		})
		return
	}

	if event.Header.Token != "" && a.cfg.VerificationToken != "" {
		if event.Header.Token != a.cfg.VerificationToken {
			http.Error(w, "验证失败", http.StatusUnauthorized)
			return
		}
	}

	if event.Header.EventType == "im.message.receive_v1" {
		go a.handleMessage(event)
	}

	w.WriteHeader(http.StatusOK)
}

// ============== 消息处理 ==============

// Send 发送同步回复
func (a *FeishuAdapter) Send(ctx context.Context, chatID string, reply *adapter.Reply) error {
	if a.queue == nil {
		return a.sendReplyNow(ctx, chatID, reply)
	}
	return a.queue.Send(ctx, chatID, reply)
}

func (a *FeishuAdapter) sendReplyNow(ctx context.Context, chatID string, reply *adapter.Reply) error {
	if reply == nil {
		return nil
	}
	token, err := a.getAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("获取 Access Token 失败: %w", err)
	}

	body := map[string]any{
		"receive_id": chatID,
		"msg_type":   "text",
		"content":    marshalTextContent(reply.Content),
	}
	bodyJSON, _ := json.Marshal(body)

	url := baseURL + "/im/v1/messages?receive_id_type=chat_id"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("发送消息失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("飞书 API 返回 %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// SendStream 发送流式回复
func (a *FeishuAdapter) SendStream(ctx context.Context, chatID string, chunks <-chan *adapter.ReplyChunk) error {
	var sb strings.Builder
	var messageID string
	lastUpdateLen := 0
	const updateThreshold = 50

	for chunk := range chunks {
		if chunk.Error != nil {
			return chunk.Error
		}
		if chunk.Done {
			break
		}
		sb.WriteString(chunk.Content)

		if messageID == "" && sb.Len() >= updateThreshold {
			var err error
			messageID, err = a.sendAndGetID(ctx, chatID, sb.String()+"…")
			if err != nil {
				continue
			}
			lastUpdateLen = sb.Len()
			continue
		}

		if messageID != "" && sb.Len()-lastUpdateLen >= updateThreshold {
			_ = a.patchMessage(ctx, messageID, sb.String()+"…")
			lastUpdateLen = sb.Len()
		}
	}

	finalContent := sb.String()
	if finalContent == "" {
		return nil
	}
	if messageID != "" {
		return a.patchMessage(ctx, messageID, finalContent)
	}
	return a.Send(ctx, chatID, &adapter.Reply{Content: finalContent})
}

// sendAndGetID 发送消息并返回 message_id
func (a *FeishuAdapter) sendAndGetID(ctx context.Context, chatID, text string) (string, error) {
	token, err := a.getAccessToken(ctx)
	if err != nil {
		return "", err
	}

	body := map[string]any{
		"receive_id": chatID,
		"msg_type":   "text",
		"content":    marshalTextContent(text),
	}
	bodyJSON, _ := json.Marshal(body)

	url := baseURL + "/im/v1/messages?receive_id_type=chat_id"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result.Data.MessageID, nil
}

// patchMessage 编辑已发送的消息
func (a *FeishuAdapter) patchMessage(ctx context.Context, messageID, text string) error {
	token, err := a.getAccessToken(ctx)
	if err != nil {
		return err
	}

	body := map[string]any{
		"content": marshalTextContent(text),
	}
	bodyJSON, _ := json.Marshal(body)

	url := baseURL + "/im/v1/messages/" + messageID
	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// handleMessage 处理消息事件
func (a *FeishuAdapter) handleMessage(event feishuEvent) {
	if a.handler == nil {
		return
	}

	msgEvent := event.Event
	if msgEvent.Message.MessageType != "text" {
		log.Printf("飞书: 暂不支持 %s 类型消息", msgEvent.Message.MessageType)
		return
	}

	var textContent struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(msgEvent.Message.Content), &textContent); err != nil {
		log.Printf("飞书: 解析文本内容失败: %v", err)
		return
	}

	content := strings.TrimSpace(textContent.Text)
	if content == "" {
		return
	}

	msg := &adapter.Message{
		ID:         "feishu-" + idgen.ShortID(),
		Platform:   adapter.PlatformFeishu,
		InstanceID: a.Name(),
		ChatID:     msgEvent.Message.ChatID,
		UserID:     msgEvent.Sender.SenderID.OpenID,
		UserName:   msgEvent.Sender.SenderID.OpenID,
		Content:    content,
		Timestamp:  time.Now(),
		Metadata: map[string]string{
			"message_id":   msgEvent.Message.MessageID,
			"chat_type":    msgEvent.Message.ChatType,
			"message_type": msgEvent.Message.MessageType,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reply, err := a.handler(ctx, msg)
	if err != nil {
		log.Printf("飞书: 处理消息失败: %v", err)
		_ = a.Send(ctx, msg.ChatID, &adapter.Reply{
			Content: "处理消息时出现错误，请稍后重试。",
		})
		return
	}

	if reply == nil {
		return
	}
	if err := a.Send(ctx, msg.ChatID, reply); err != nil {
		log.Printf("飞书: 发送回复失败: %v", err)
	}
}

// Health 返回适配器健康状态。
func (a *FeishuAdapter) Health(_ context.Context) error {
	if a.handler == nil {
		return fmt.Errorf("feishu handler 未附加")
	}
	if a.cfg.AppID == "" || a.cfg.AppSecret == "" {
		return fmt.Errorf("feishu app_id/app_secret 未配置")
	}
	if a.stopped.Load() {
		return fmt.Errorf("feishu adapter stopped")
	}
	a.connMu.Lock()
	defer a.connMu.Unlock()
	if a.conn == nil {
		return fmt.Errorf("feishu WebSocket 未连接")
	}
	return nil
}

// ============== Token 管理 ==============

// getAccessToken 获取飞书 Tenant Access Token（带缓存）
func (a *FeishuAdapter) getAccessToken(ctx context.Context) (string, error) {
	a.mu.RLock()
	if a.accessToken != "" && time.Now().Before(a.tokenExpiry.Add(-tokenRefreshBuffer)) {
		token := a.accessToken
		a.mu.RUnlock()
		return token, nil
	}
	a.mu.RUnlock()

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.accessToken != "" && time.Now().Before(a.tokenExpiry.Add(-tokenRefreshBuffer)) {
		return a.accessToken, nil
	}

	body, _ := json.Marshal(map[string]string{
		"app_id":     a.cfg.AppID,
		"app_secret": a.cfg.AppSecret,
	})

	url := baseURL + "/auth/v3/tenant_access_token/internal"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("获取 Token 请求失败: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析 Token 响应失败: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("获取 Token 失败: code=%d, msg=%s", result.Code, result.Msg)
	}

	a.accessToken = result.TenantAccessToken
	a.tokenExpiry = time.Now().Add(time.Duration(result.Expire) * time.Second)
	log.Printf("飞书 Access Token 已刷新, 有效期 %d 秒", result.Expire)

	return a.accessToken, nil
}

// ============== 数据模型 ==============

// feishuEvent 飞书事件通用结构
type feishuEvent struct {
	Challenge string `json:"challenge,omitempty"`
	Token     string `json:"token,omitempty"`
	Type      string `json:"type,omitempty"`

	Header struct {
		EventID    string `json:"event_id"`
		EventType  string `json:"event_type"`
		CreateTime string `json:"create_time"`
		Token      string `json:"token"`
		AppID      string `json:"app_id"`
	} `json:"header"`

	Event struct {
		Sender struct {
			SenderID struct {
				UnionID string `json:"union_id"`
				UserID  string `json:"user_id"`
				OpenID  string `json:"open_id"`
			} `json:"sender_id"`
			SenderType string `json:"sender_type"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			RootID      string `json:"root_id"`
			ParentID    string `json:"parent_id"`
			CreateTime  string `json:"create_time"`
			ChatID      string `json:"chat_id"`
			ChatType    string `json:"chat_type"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
		} `json:"message"`
	} `json:"event"`
}

func marshalTextContent(text string) string {
	b, _ := json.Marshal(map[string]string{"text": text})
	return string(b)
}
