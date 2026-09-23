package instances

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
)

// TestDelivery 保存一次主动连接测试的逐目标回执。
type TestDelivery struct {
	ChatID            string `json:"chat_id"`
	Status            string `json:"status"`
	ExternalMessageID string `json:"external_message_id,omitempty"`
}

// TestDeliveryResult 区分连接状态、平台接收和实际送达。
type TestDeliveryResult struct {
	RequestID  string         `json:"request_id"`
	Success    bool           `json:"success"`
	Message    string         `json:"message"`
	Pending    bool           `json:"pending"`
	Deliveries []TestDelivery `json:"deliveries"`
}

func projectTestDelivery(requestID string, deliveries []TestDelivery) TestDeliveryResult {
	result := TestDeliveryResult{RequestID: requestID, Success: true, Deliveries: deliveries}
	if len(deliveries) == 0 {
		result.Message = "Connection healthy. No bound conversation; no test message was sent."
		return result
	}
	counts := map[string]int{}
	for _, delivery := range deliveries {
		counts[delivery.Status]++
	}
	result.Pending = counts["sending"]+counts["not_sent"]+counts[string(adapter.DeliveryOutcomeUnknown)] > 0
	result.Success = !result.Pending && counts[string(adapter.DeliveryFailed)] == 0
	result.Message = fmt.Sprintf("Connection healthy. Test messages: %d delivered, %d accepted, %d failed, %d unknown, %d sending, %d not sent.", counts["delivered"], counts["accepted"], counts["failed"], counts["outcome_unknown"], counts["sending"], counts["not_sent"])
	if counts["accepted"] > 0 {
		result.Message += " Accepted does not confirm delivery."
	}
	if result.Pending {
		result.Message += " No automatic resend."
	}
	return result
}

// ReadTestDelivery 只读取原回执；发送超时后不再创建第二次发送。
func (m *Manager) ReadTestDelivery(ctx context.Context, instanceID, requestID string) (TestDeliveryResult, error) {
	var raw string
	var deadline int64
	if err := m.db.QueryRowContext(ctx, `SELECT deliveries_json,deadline_ms FROM platform_test_requests WHERE instance_id=? AND request_id=?`, instanceID, requestID).Scan(&raw, &deadline); err != nil {
		return TestDeliveryResult{}, err
	}
	var deliveries []TestDelivery
	if err := json.Unmarshal([]byte(raw), &deliveries); err != nil {
		return TestDeliveryResult{}, err
	}
	if time.Now().UnixMilli() >= deadline {
		for i := range deliveries {
			if deliveries[i].Status == "sending" {
				deliveries[i].Status = string(adapter.DeliveryOutcomeUnknown)
			}
		}
	}
	return projectTestDelivery(requestID, deliveries), nil
}

func (m *Manager) writeTestDeliveries(ctx context.Context, instanceID, requestID string, deliveries []TestDelivery) error {
	raw, err := json.Marshal(deliveries)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `UPDATE platform_test_requests SET deliveries_json=? WHERE instance_id=? AND request_id=?`, string(raw), instanceID, requestID)
	return err
}

// SendBoundTest 冻结目标后执行一次；重放只返回原回执，不恢复未发送或未知目标。
func (m *Manager) SendBoundTest(ctx context.Context, instanceID, requestID string, chatIDs []string, content string) (TestDeliveryResult, error) {
	instanceID, requestID = strings.TrimSpace(instanceID), strings.TrimSpace(requestID)
	if instanceID == "" || requestID == "" {
		return TestDeliveryResult{}, fmt.Errorf("instance ID and request ID are required")
	}
	seen := map[string]bool{}
	targets := make([]string, 0, len(chatIDs))
	for _, chatID := range chatIDs {
		chatID = strings.TrimSpace(chatID)
		if chatID != "" && !seen[chatID] {
			seen[chatID] = true
			targets = append(targets, chatID)
		}
	}
	sort.Strings(targets)
	deliveries := make([]TestDelivery, 0, len(targets))
	for _, chatID := range targets {
		deliveries = append(deliveries, TestDelivery{ChatID: chatID, Status: "not_sent"})
	}
	raw, _ := json.Marshal(deliveries)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	insert, err := m.db.ExecContext(ctx, `INSERT OR IGNORE INTO platform_test_requests(instance_id,request_id,deliveries_json,deadline_ms) VALUES(?,?,?,?)`, instanceID, requestID, string(raw), deadline.UnixMilli())
	if err != nil {
		return TestDeliveryResult{}, err
	}
	n, err := insert.RowsAffected()
	if err != nil {
		return TestDeliveryResult{}, err
	}
	if n == 0 {
		return m.ReadTestDelivery(ctx, instanceID, requestID)
	}
	// 旧客户端未提供国际化文本时沿用原消息；重放不消费新的文本。
	if strings.TrimSpace(content) == "" {
		content = "HexClaw connection test. This message confirms that the configured IM connection can send messages."
	}
	adp := m.resolveRunningAdapterByStableID(instanceID)
	for i := range deliveries {
		if adp == nil {
			deliveries[i].Status = string(adapter.DeliveryFailed)
			continue
		}
		if ctx.Err() != nil {
			break
		}
		deliveries[i].Status = "sending"
		if err := m.writeTestDeliveries(ctx, instanceID, requestID, deliveries); err != nil {
			return TestDeliveryResult{}, err
		}
		reply := &adapter.Reply{Content: content}
		if transport, ok := adp.(adapter.DeliveryReceiptAdapter); ok {
			ack, sendErr := transport.SendWithReceipt(ctx, deliveries[i].ChatID, reply)
			deliveries[i].ExternalMessageID = ack.ExternalMessageID
			switch ack.Status {
			case adapter.DeliveryAccepted, adapter.DeliveryDelivered, adapter.DeliveryFailed:
				deliveries[i].Status = string(ack.Status)
			default:
				deliveries[i].Status = string(adapter.DeliveryOutcomeUnknown)
			}
			if sendErr != nil && ack.Status != adapter.DeliveryFailed {
				deliveries[i].Status = string(adapter.DeliveryOutcomeUnknown)
			}
		} else if err := adp.Send(ctx, deliveries[i].ChatID, reply); err != nil {
			deliveries[i].Status = string(adapter.DeliveryOutcomeUnknown)
		} else {
			deliveries[i].Status = string(adapter.DeliveryAccepted)
		}
		// 请求取消不应丢弃已经取得的外部回执；这里只写本地结果。
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		err := m.writeTestDeliveries(persistCtx, instanceID, requestID, deliveries)
		persistCancel()
		if err != nil {
			return TestDeliveryResult{}, err
		}
	}
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer persistCancel()
	if err := m.writeTestDeliveries(persistCtx, instanceID, requestID, deliveries); err != nil {
		return TestDeliveryResult{}, err
	}
	return projectTestDelivery(requestID, deliveries), nil
}
