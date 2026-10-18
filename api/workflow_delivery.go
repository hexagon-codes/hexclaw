package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	emailadapter "github.com/hexagon-codes/hexclaw/adapter/email"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/storage"
)

// WorkflowArtifactSnapshot 只引用现有生成文件存储，不重复持久化附件字节。
type WorkflowArtifactSnapshot struct {
	Ref      messagecontent.AttachmentRef `json:"ref"`
	FilePath string                       `json:"file_path,omitempty"`
	Error    string                       `json:"error,omitempty"`
}

// WorkflowDelivery 冻结某个输出节点的一次投递，计算与投递分别收敛。
type WorkflowDelivery struct {
	ID                string                         `json:"id"`
	NodeID            string                         `json:"node_id"`
	Kind              string                         `json:"kind"`
	Status            string                         `json:"status"`
	UserID            string                         `json:"user_id"`
	SessionMode       string                         `json:"session_mode,omitempty"`
	SessionID         string                         `json:"session_id,omitempty"`
	InstanceID        string                         `json:"instance_id,omitempty"`
	ChatID            string                         `json:"chat_id,omitempty"`
	To                []string                       `json:"to,omitempty"`
	Subject           string                         `json:"subject,omitempty"`
	ExternalMessageID string                         `json:"external_message_id,omitempty"`
	Attempts          int                            `json:"attempts,omitempty"`
	Directory         *WorkflowDirectoryTarget       `json:"directory,omitempty"`
	Files             []WorkflowDeliveryFile         `json:"files,omitempty"`
	Content           string                         `json:"content"`
	MessageContent    *messagecontent.MessageContent `json:"message_content,omitempty"`
	RenderManifest    *messagecontent.RenderManifest `json:"render_manifest,omitempty"`
	Parts             []WorkflowDeliveryPart         `json:"parts,omitempty"`
	Error             string                         `json:"error,omitempty"`
	ErrorCode         string                         `json:"error_code,omitempty"`
	CreatedAt         time.Time                      `json:"created_at"`
	UpdatedAt         time.Time                      `json:"updated_at"`
	active            bool
}

type WorkflowDeliveryPart struct {
	Ordinal            int                     `json:"ordinal"`
	Kind               messagecontent.PartKind `json:"kind"`
	ArtifactID         string                  `json:"artifact_id,omitempty"`
	MIME               string                  `json:"mime,omitempty"`
	Digest             string                  `json:"digest"`
	PreparedResourceID string                  `json:"prepared_resource_id,omitempty"`
	Status             string                  `json:"status"`
	ExternalMessageID  string                  `json:"external_message_id,omitempty"`
	Attempts           int                     `json:"attempts"`
	Error              string                  `json:"error,omitempty"`
}

// 外部传输是唯一替身边界；持久化、领取与重试仍走同一运行账本。
type workflowDeliveryTransport interface {
	PrepareDeliveryPartResource(context.Context, string, adapter.DeliveryPart) (string, error)
	SendPreparedPartWithReceipt(context.Context, string, string, adapter.DeliveryPart) (adapter.DeliveryAck, error)
	QueryReceipt(context.Context, string, string) (adapter.DeliveryAck, error)
}

type workflowEmailTransport interface {
	SendEmail(context.Context, string, emailadapter.OutboundMessage) (adapter.DeliveryAck, error)
}

// workflowRunPublic 仅投影公开运行回执，平台媒体准备引用只留在耐久账本。
func workflowRunPublic(run *WorkflowRun) *WorkflowRun {
	if run == nil {
		return nil
	}
	result := cloneWorkflowRun(run)
	for i := range result.Deliveries {
		for j := range result.Deliveries[i].Parts {
			result.Deliveries[i].Parts[j].PreparedResourceID = ""
		}
	}
	return result
}

func workflowDeliveryIdentity(values ...string) string {
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:24]
}

// prepareWorkflowDeliveries 在计算终态提交前构造快照，不查询服务也不产生副作用。
func prepareWorkflowDeliveries(run *WorkflowRun) []WorkflowDelivery {
	if run == nil || run.Status != "completed" || run.Definition == nil {
		return nil
	}
	owner := "api-user"
	if run.Request != nil {
		owner = firstNonEmpty(run.Request.UserID, owner)
	}
	var deliveries []WorkflowDelivery
	for _, raw := range run.Definition.Nodes {
		node, ok := raw.(map[string]any)
		if !ok || stringValue(node["type"]) != "output" {
			continue
		}
		nodeID := stringValue(node["id"])
		config := resourceMap(node["data"])
		if len(config) == 0 {
			config = resourceMap(node["config"])
		}
		settings := resourceMap(config["delivery"])
		content, canonical := run.Output, run.MessageContent
		active := true
		for _, result := range run.NodeResults {
			if result.NodeID == nodeID {
				active = result.Status == nodeStatusCompleted
				content = result.Output
				canonical = result.MessageContent
				break
			}
		}
		if !active {
			continue
		}
		if canonical == nil {
			value, err := messagecontent.New(messagecontent.ProducerWorkflow, "und", content, nil)
			if err == nil {
				canonical = &value
			}
		}
		makeDelivery := func(kind string) WorkflowDelivery {
			now := time.Now()
			return WorkflowDelivery{ID: "dlv-" + workflowDeliveryIdentity(run.ID, nodeID, kind), NodeID: nodeID, Kind: kind,
				Status: "pending", UserID: owner, Content: content, MessageContent: canonical, CreatedAt: now, UpdatedAt: now}
		}
		session := resourceMap(settings["session"])
		if mode := stringValue(session["mode"]); mode != "" && mode != "none" {
			delivery := makeDelivery("session")
			delivery.SessionMode = mode
			switch mode {
			case "source":
				if run.Request != nil {
					delivery.SessionID = run.Request.SessionID
				}
				if delivery.SessionID == "" {
					delivery.Status, delivery.Error = "failed", "This run has no source conversation; select the workflow conversation"
				}
			case "workflow":
				delivery.SessionID = "sess-wf-" + workflowDeliveryIdentity(owner, run.WorkflowID)
			default:
				delivery.Status, delivery.Error = "failed", "Unsupported workflow conversation mode"
			}
			deliveries = append(deliveries, delivery)
		}
		ding := resourceMap(settings["dingtalk"])
		if enabled, _ := ding["enabled"].(bool); enabled {
			delivery := makeDelivery("dingtalk")
			delivery.InstanceID, delivery.ChatID = stringValue(ding["instance_id"]), stringValue(ding["chat_id"])
			if delivery.InstanceID == "" || delivery.ChatID == "" {
				delivery.Status, delivery.Error = "failed", "A DingTalk instance and bound conversation are required"
			} else if delivery.ChatID[0] < 0x20 {
				delivery.Status, delivery.Error = "failed", "DingTalk workflow delivery supports direct conversations only"
			}
			if err := prepareWorkflowDeliveryParts(&delivery); err != nil {
				delivery.Status, delivery.Error = "failed", err.Error()
			}
			deliveries = append(deliveries, delivery)
		}
		email := resourceMap(settings["email"])
		if enabled, _ := email["enabled"].(bool); enabled {
			delivery := makeDelivery("email")
			delivery.InstanceID = stringValue(email["instance_id"])
			delivery.Subject = firstNonEmpty(stringValue(email["subject"]), run.Definition.Name, "Workflow result")
			addresses, parseErr := mail.ParseAddressList(stringValue(email["to"]))
			if parseErr != nil || len(addresses) == 0 || delivery.InstanceID == "" {
				delivery.Status, delivery.ErrorCode, delivery.Error = "failed", "DELIVERY_EMAIL_INVALID", "An email connection and valid recipients are required"
			} else {
				for _, address := range addresses {
					delivery.To = append(delivery.To, address.Address)
				}
			}
			deliveries = append(deliveries, delivery)
		}
		directory := resourceMap(settings["directory"])
		if enabled, _ := directory["enabled"].(bool); enabled {
			delivery := makeDelivery("directory")
			delivery.Directory = &WorkflowDirectoryTarget{Location: stringValue(directory["location"]), Path: stringValue(directory["path"]),
				DirectoryRef: stringValue(directory["directory_ref"]), ClientID: stringValue(directory["client_id"])}
			valid := delivery.Directory.Location == "service" && filepath.IsAbs(delivery.Directory.Path)
			valid = valid || (delivery.Directory.Location == "desktop" && delivery.Directory.DirectoryRef != "" && delivery.Directory.ClientID != "")
			if !valid {
				delivery.Status, delivery.ErrorCode, delivery.Error = "failed", "DELIVERY_DIRECTORY_INVALID", "A service directory or desktop directory reference is required"
			}
			deliveries = append(deliveries, delivery)
		}
	}
	return deliveries
}

func prepareWorkflowDeliveryParts(delivery *WorkflowDelivery) error {
	if delivery.MessageContent == nil {
		return errors.New("Workflow output contains no deliverable content")
	}
	content := delivery.MessageContent
	renderParts := []messagecontent.RenderPart{}
	delivery.Parts = nil
	if strings.TrimSpace(content.Markdown) != "" {
		renderParts = append(renderParts, messagecontent.RenderPart{Kind: messagecontent.PartMarkdown, Text: content.Markdown})
		sum := sha256.Sum256([]byte(content.Markdown))
		delivery.Parts = append(delivery.Parts, WorkflowDeliveryPart{Ordinal: 1, Kind: messagecontent.PartMarkdown, Digest: "sha256:" + hex.EncodeToString(sum[:]), Status: "pending"})
	}
	for _, ref := range content.Attachments {
		renderParts = append(renderParts, messagecontent.RenderPart{Kind: messagecontent.PartArtifact, ArtifactRef: ref.AssetID,
			ArtifactDigest: ref.Digest, AltText: ref.AltText})
	}
	manifest, err := messagecontent.BuildManifest(*content, messagecontent.RenderRequest{Surface: messagecontent.SurfaceChannel,
		RendererVersion: "workflow-delivery-v1", Capabilities: messagecontent.CapabilitySnapshot{Markdown: true, TeXMath: true, Attachments: true}, Parts: renderParts})
	if err != nil {
		return err
	}
	delivery.RenderManifest = &manifest
	for _, ref := range content.Attachments {
		part := WorkflowDeliveryPart{Ordinal: len(delivery.Parts) + 1, Kind: messagecontent.PartArtifact, ArtifactID: ref.AssetID, MIME: ref.MIME, Digest: ref.Digest, Status: "pending"}
		if !strings.HasPrefix(strings.ToLower(ref.MIME), "image/") && ref.MIME != "application/pdf" {
			part.Status, part.Error = "failed", "DingTalk workflow delivery supports text, images and PDF files"
		}
		delivery.Parts = append(delivery.Parts, part)
	}
	return nil
}

// freezeWorkflowArtifacts 在运行快照里保存 genStore 相对引用与冻结失败，不改变模型计算结果。
func (s *Server) freezeWorkflowArtifacts(ctx context.Context, reply *adapter.Reply) map[string]WorkflowArtifactSnapshot {
	result := map[string]WorkflowArtifactSnapshot{}
	if reply == nil || reply.MessageContent == nil {
		return result
	}
	for index, ref := range reply.MessageContent.Attachments {
		snapshot := WorkflowArtifactSnapshot{Ref: ref}
		if s.genStore == nil {
			snapshot.Error = "Generated file storage is unavailable"
		} else if index >= len(reply.Attachments) {
			snapshot.Error = "Workflow artifact bytes are missing"
		} else {
			snapshot.FilePath, snapshot.Error = s.freezeWorkflowAttachment(ctx, ref, reply.Attachments[index])
		}
		result[ref.AssetID] = snapshot
	}
	return result
}

func (s *Server) freezeWorkflowAttachment(ctx context.Context, ref messagecontent.AttachmentRef, attachment adapter.Attachment) (string, string) {
	var raw []byte
	var err error
	path := ""
	ext := strings.TrimPrefix(filepath.Ext(ref.Name), ".")
	if ext == "" {
		if extensions, _ := mime.ExtensionsByType(ref.MIME); len(extensions) > 0 {
			ext = strings.TrimPrefix(extensions[0], ".")
		} else {
			ext = "bin"
		}
	}
	if strings.TrimSpace(attachment.Data) != "" {
		raw, err = base64.StdEncoding.DecodeString(attachment.Data)
	} else if attachment.URL != "" {
		parsed, parseErr := url.Parse(attachment.URL)
		if parseErr == nil {
			const generatedPrefix = "/api/v1/files/generated/"
			if strings.HasPrefix(parsed.Path, generatedPrefix) {
				path = strings.TrimPrefix(parsed.Path, generatedPrefix)
			}
		}
		if path == "" {
			path, err = s.genStore.SaveFromURL(ctx, attachment.URL, ext)
		}
		if err == nil {
			var file io.ReadCloser
			file, err = s.genStore.Open(path)
			if err == nil {
				raw, err = io.ReadAll(file)
				_ = file.Close()
			}
		}
	} else {
		err = errors.New("missing artifact bytes")
	}
	if err != nil || len(raw) == 0 {
		return "", "Workflow artifact bytes could not be frozen"
	}
	sum := sha256.Sum256(raw)
	if ref.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return "", "Workflow artifact bytes do not match the output digest"
	}
	if path == "" {
		path, err = s.genStore.SaveBytes(raw, ext)
		if err != nil {
			return "", "Workflow artifact could not be persisted"
		}
	}
	return path, ""
}

func (s *Server) workflowDeliverySnapshot(runID, deliveryID string) (*WorkflowRun, *WorkflowDelivery) {
	s.workflowStore.mu.RLock()
	defer s.workflowStore.mu.RUnlock()
	source := s.workflowStore.runs[runID]
	if source == nil {
		return nil, nil
	}
	run := cloneWorkflowRun(source)
	for i := range run.Deliveries {
		if run.Deliveries[i].ID == deliveryID {
			return run, &run.Deliveries[i]
		}
	}
	return run, nil
}

func (s *Server) updateWorkflowDelivery(runID, deliveryID string, change func(*WorkflowDelivery) error) error {
	ws := s.workflowStore
	ws.mu.Lock()
	defer ws.mu.Unlock()
	run := ws.runs[runID]
	if run == nil {
		return storage.ErrNotFound
	}
	for i := range run.Deliveries {
		if run.Deliveries[i].ID == deliveryID {
			previous := run.Deliveries[i]
			next := previous
			next.Parts = append([]WorkflowDeliveryPart(nil), previous.Parts...)
			next.Files = append([]WorkflowDeliveryFile(nil), previous.Files...)
			if err := change(&next); err != nil {
				return err
			}
			next.UpdatedAt = time.Now()
			run.Deliveries[i] = next
			if err := ws.persistRuns(); err != nil {
				run.Deliveries[i] = previous
				return err
			}
			return nil
		}
	}
	return storage.ErrNotFound
}

func (s *Server) dispatchWorkflowDelivery(ctx context.Context, runID string) {
	var transport workflowDeliveryTransport
	if s.instanceMgr != nil {
		transport = s.instanceMgr
	}
	s.dispatchWorkflowDeliveryWithTransport(ctx, runID, "", false, transport)
}

func (s *Server) dispatchWorkflowDeliveryWithTransport(ctx context.Context, runID, deliveryID string, retry bool, transport workflowDeliveryTransport) {
	nodeID := ""
	if deliveryID != "" {
		_, target := s.workflowDeliverySnapshot(runID, deliveryID)
		if target == nil {
			return
		}
		nodeID = target.NodeID
	}
	s.generateWorkflowOutputFiles(ctx, runID, nodeID, retry)
	s.workflowStore.mu.RLock()
	run := cloneWorkflowRun(s.workflowStore.runs[runID])
	s.workflowStore.mu.RUnlock()
	if run == nil || run.Status != "completed" {
		return
	}
	for _, delivery := range run.Deliveries {
		if deliveryID != "" && delivery.ID != deliveryID {
			continue
		}
		if delivery.Status == "delivered" || delivery.Status == "sending" || (delivery.Kind == "email" && (delivery.Status == "accepted" || delivery.Status == "outcome_unknown")) || (delivery.Status == "failed" && !retry) {
			continue
		}
		release := s.claimWorkflowDelivery(runID, delivery.ID)
		if release == nil {
			continue
		}
		if err := s.attachWorkflowGeneratedFiles(runID, delivery.ID); err != nil {
			_ = s.updateWorkflowDelivery(runID, delivery.ID, func(current *WorkflowDelivery) error {
				current.Status, current.ErrorCode, current.Error = "failed", "OUTPUT_RENDER_FAILED", "Required workflow output files could not be generated"
				return nil
			})
			release()
			continue
		}
		if delivery.Kind == "session" {
			s.deliverWorkflowSession(ctx, runID, delivery.ID)
		} else if delivery.Kind == "dingtalk" {
			s.deliverWorkflowDingTalk(ctx, runID, delivery.ID, retry, transport)
		} else if delivery.Kind == "email" {
			var emailTransport workflowEmailTransport
			if candidate, ok := transport.(workflowEmailTransport); ok {
				emailTransport = candidate
			} else if s.instanceMgr != nil {
				emailTransport = s.instanceMgr
			}
			s.deliverWorkflowEmail(ctx, runID, delivery.ID, retry, emailTransport)
		} else if delivery.Kind == "directory" {
			s.deliverWorkflowDirectory(ctx, runID, delivery.ID)
		}
		release()
	}
}

// 进程内领取只协调工作者；重启后的事实仍由耐久 part 状态决定。
func (s *Server) claimWorkflowDelivery(runID, deliveryID string) func() {
	ws := s.workflowStore
	ws.mu.Lock()
	defer ws.mu.Unlock()
	run := ws.runs[runID]
	if run == nil {
		return nil
	}
	for i := range run.Deliveries {
		if run.Deliveries[i].ID == deliveryID {
			if run.Deliveries[i].active {
				return nil
			}
			run.Deliveries[i].active = true
			return func() {
				ws.mu.Lock()
				defer ws.mu.Unlock()
				if current := ws.runs[runID]; current != nil {
					for index := range current.Deliveries {
						if current.Deliveries[index].ID == deliveryID {
							current.Deliveries[index].active = false
							break
						}
					}
				}
			}
		}
	}
	return nil
}

func (s *Server) deliverWorkflowSession(ctx context.Context, runID, deliveryID string) {
	if err := s.updateWorkflowDelivery(runID, deliveryID, func(delivery *WorkflowDelivery) error {
		if delivery.Status == "sending" || delivery.Status == "delivered" {
			return errors.New("Workflow delivery is already active")
		}
		delivery.Status, delivery.Error = "sending", ""
		return nil
	}); err != nil {
		return
	}
	run, delivery := s.workflowDeliverySnapshot(runID, deliveryID)
	err := s.persistWorkflowSessionResult(ctx, run, delivery)
	_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
		current.Status, current.Error = "delivered", ""
		if err != nil {
			current.Status, current.Error = "failed", err.Error()
		}
		return nil
	})
}

func (s *Server) persistWorkflowSessionResult(ctx context.Context, run *WorkflowRun, delivery *WorkflowDelivery) error {
	if s.store == nil || run == nil || delivery == nil || delivery.SessionID == "" {
		return errors.New("Workflow conversation is unavailable")
	}
	sess, err := s.store.GetSession(ctx, delivery.SessionID)
	if errors.Is(err, storage.ErrNotFound) && delivery.SessionMode == "workflow" {
		meta, _ := json.Marshal(map[string]string{"workflow_id": run.WorkflowID, "agent_name": workflowResultAgent(run)})
		sess = &storage.Session{ID: delivery.SessionID, UserID: delivery.UserID, Platform: "desktop", Status: 1, Title: run.Definition.Name, Meta: string(meta)}
		if err = s.store.CreateSession(ctx, sess); err != nil {
			sess, err = s.store.GetSession(ctx, delivery.SessionID)
		}
	}
	if err != nil || sess == nil || sess.UserID != delivery.UserID {
		return errors.New("Workflow conversation does not belong to this user or no longer exists")
	}
	attachments, err := s.workflowDeliveryAttachments(run, delivery)
	if err != nil {
		return err
	}
	if delivery.MessageContent != nil {
		for i, ref := range delivery.MessageContent.Attachments {
			attachments[i].Data = ""
			attachments[i].URL = "/api/v1/files/generated/" + run.ArtifactSnapshots[ref.AssetID].FilePath
		}
	}
	metadata, _ := json.Marshal(map[string]any{"producer_kind": "workflow", "workflow_id": run.WorkflowID,
		"workflow_run_id": run.ID, "workflow_node_id": delivery.NodeID, "agent_name": workflowResultAgent(run), "attachments": attachments})
	id := "msg-wf-" + workflowDeliveryIdentity(run.ID, delivery.NodeID, delivery.SessionID)
	if previous, lookupErr := s.store.GetMessage(ctx, id); lookupErr == nil {
		if workflowSessionMessageMatches(previous, sess.ID, delivery.Content, metadata) {
			return nil
		}
		return errors.New("Workflow conversation message identity conflicts with an existing result")
	} else if !errors.Is(lookupErr, storage.ErrNotFound) {
		return errors.New("Workflow conversation message could not be checked")
	}
	record := buildMessageRecord(sess.ID, &appendMessageRequest{ID: id, Role: "assistant", Content: delivery.Content,
		Metadata: metadata, RequestID: run.ID})
	if err = s.store.SaveMessage(ctx, record); err != nil {
		previous, lookupErr := s.store.GetMessage(ctx, id)
		if lookupErr == nil && workflowSessionMessageMatches(previous, sess.ID, delivery.Content, metadata) {
			return nil
		}
		return errors.New("Workflow conversation message could not be persisted")
	}
	return nil
}

func workflowSessionMessageMatches(record *storage.MessageRecord, sessionID, content string, metadata []byte) bool {
	if record == nil || record.SessionID != sessionID || record.Content != content {
		return false
	}
	var previous, expected struct {
		RunID       string               `json:"workflow_run_id"`
		NodeID      string               `json:"workflow_node_id"`
		Attachments []adapter.Attachment `json:"attachments"`
	}
	if json.Unmarshal([]byte(record.Metadata), &previous) != nil || json.Unmarshal(metadata, &expected) != nil {
		return false
	}
	before, _ := json.Marshal(previous)
	after, _ := json.Marshal(expected)
	return string(before) == string(after)
}

// 邮件作为一个完整MIME信封发送；SMTP受理不等同于收件人已收到。
func (s *Server) deliverWorkflowEmail(ctx context.Context, runID, deliveryID string, retry bool, transport workflowEmailTransport) {
	run, delivery := s.workflowDeliverySnapshot(runID, deliveryID)
	if delivery == nil || (delivery.Status != "pending" && !(retry && delivery.Status == "failed")) {
		return
	}
	attachments, err := s.workflowDeliveryAttachments(run, delivery)
	if err != nil || transport == nil || delivery.InstanceID == "" || len(delivery.To) == 0 {
		problem, code := "Workflow email connection is unavailable", "DELIVERY_EMAIL_UNAVAILABLE"
		if err != nil {
			problem, code = err.Error(), "DELIVERY_EMAIL_FAILED"
		}
		_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
			current.Status, current.ErrorCode, current.Error = "failed", code, problem
			return nil
		})
		return
	}
	message := emailadapter.OutboundMessage{To: append([]string(nil), delivery.To...), Subject: delivery.Subject, Text: delivery.Content, MessageID: delivery.ID}
	for _, attachment := range attachments {
		raw, decodeErr := base64.StdEncoding.DecodeString(attachment.Data)
		if decodeErr != nil {
			_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
				current.Status, current.ErrorCode, current.Error = "failed", "DELIVERY_EMAIL_FAILED", "Workflow email attachment could not be decoded"
				return nil
			})
			return
		}
		message.Attachments = append(message.Attachments, emailadapter.OutboundAttachment{Name: attachment.Name, MIME: attachment.Mime, Data: raw})
	}
	for _, file := range run.GeneratedFiles {
		if file.NodeID != delivery.NodeID || file.Format != "html" || file.Status != "ready" {
			continue
		}
		for _, attachment := range message.Attachments {
			if attachment.Name == file.Name {
				message.HTML = string(attachment.Data)
				break
			}
		}
	}
	if err = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
		if current.Status != "pending" && !(retry && current.Status == "failed") {
			return errors.New("Workflow email is already active")
		}
		current.Status, current.ErrorCode, current.Error = "sending", "", ""
		current.Attempts++
		return nil
	}); err != nil {
		return
	}
	ack, sendErr := transport.SendEmail(ctx, delivery.InstanceID, message)
	status, code, problem := string(ack.Status), "", ""
	if status != "accepted" && status != "failed" {
		status = "outcome_unknown"
	}
	if sendErr != nil && status != "failed" && status != "accepted" {
		status = "outcome_unknown"
	}
	if status == "failed" {
		code, problem = "DELIVERY_EMAIL_FAILED", "Workflow email was rejected before SMTP acceptance"
	} else if status == "outcome_unknown" {
		code, problem = "DELIVERY_OUTCOME_UNKNOWN", "Workflow email outcome is unknown and cannot be resent automatically"
	}
	_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
		current.Status, current.ErrorCode, current.Error = status, code, problem
		current.ExternalMessageID = ack.ExternalMessageID
		return nil
	})
}

func workflowResultAgent(run *WorkflowRun) string {
	for i := len(run.NodeResults) - 1; i >= 0; i-- {
		if result := run.NodeResults[i]; result.Status == nodeStatusCompleted && result.AgentRole != "" {
			return result.AgentRole
		}
	}
	for i := len(run.Definition.Nodes) - 1; i >= 0; i-- {
		if node, ok := run.Definition.Nodes[i].(map[string]any); ok && (stringValue(node["type"]) == "agent" || stringValue(node["type"]) == "llm") {
			config := resourceMap(node["data"])
			if len(config) == 0 {
				config = resourceMap(node["config"])
			}
			if agent := stringValue(config["agent"]); agent != "" {
				return agent
			}
		}
	}
	return ""
}

func (s *Server) workflowDeliveryAttachments(run *WorkflowRun, delivery *WorkflowDelivery) ([]adapter.Attachment, error) {
	var attachments []adapter.Attachment
	if delivery.MessageContent == nil {
		return attachments, nil
	}
	for _, ref := range delivery.MessageContent.Attachments {
		snapshot, ok := run.ArtifactSnapshots[ref.AssetID]
		if !ok || snapshot.Error != "" || snapshot.FilePath == "" || s.genStore == nil {
			return nil, fmt.Errorf("Workflow artifact is unavailable: %s", firstNonEmpty(snapshot.Error, ref.Name, ref.AssetID))
		}
		file, err := s.genStore.Open(snapshot.FilePath)
		if err != nil {
			return nil, errors.New("Workflow artifact file is unavailable")
		}
		raw, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil {
			return nil, errors.New("Workflow artifact file could not be read")
		}
		sum := sha256.Sum256(raw)
		if ref.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
			return nil, errors.New("Workflow artifact file no longer matches the frozen output")
		}
		kind := "file"
		if strings.HasPrefix(ref.MIME, "image/") {
			kind = "image"
		}
		attachments = append(attachments, adapter.Attachment{Type: kind, Name: ref.Name, Mime: ref.MIME, Data: base64.StdEncoding.EncodeToString(raw)})
	}
	return attachments, nil
}

func workflowDeliveryStatus(parts []WorkflowDeliveryPart) string {
	counts := map[string]int{}
	for _, part := range parts {
		counts[part.Status]++
	}
	for _, status := range []string{"sending", "preparing", "outcome_unknown", "failed", "accepted", "pending"} {
		if counts[status] > 0 {
			if status == "preparing" {
				return "sending"
			}
			return status
		}
	}
	if len(parts) > 0 && counts["delivered"] == len(parts) {
		return "delivered"
	}
	return "failed"
}

func (s *Server) updateWorkflowDeliveryPart(runID, deliveryID string, ordinal int, change func(*WorkflowDeliveryPart) error) error {
	return s.updateWorkflowDelivery(runID, deliveryID, func(delivery *WorkflowDelivery) error {
		for i := range delivery.Parts {
			if delivery.Parts[i].Ordinal == ordinal {
				if err := change(&delivery.Parts[i]); err != nil {
					return err
				}
				delivery.Status, delivery.Error = workflowDeliveryStatus(delivery.Parts), ""
				for _, part := range delivery.Parts {
					if part.Error != "" {
						delivery.Error = part.Error
						break
					}
				}
				return nil
			}
		}
		return errors.New("Workflow delivery part was not found")
	})
}

func (s *Server) deliverWorkflowDingTalk(ctx context.Context, runID, deliveryID string, retry bool, transport workflowDeliveryTransport) {
	run, delivery := s.workflowDeliverySnapshot(runID, deliveryID)
	if delivery == nil || len(delivery.Parts) == 0 {
		return
	}
	needsSend := false
	for _, part := range delivery.Parts {
		if part.Status == "pending" || (retry && part.Status == "failed") {
			needsSend = true
		}
		if (part.Status == "accepted" || part.Status == "outcome_unknown") && transport != nil {
			s.queryWorkflowDeliveryPart(ctx, runID, deliveryID, part, transport)
		}
	}
	if !needsSend {
		return
	}
	// 所有附件先核对，不能先发正文后才发现必要产物缺失。
	attachments, artifactErr := s.workflowDeliveryAttachments(run, delivery)
	if artifactErr != nil || transport == nil || delivery.InstanceID == "" || delivery.ChatID == "" || delivery.ChatID[0] < 0x20 {
		problem := "DingTalk workflow delivery is unavailable"
		if artifactErr != nil {
			problem = artifactErr.Error()
		}
		_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error { current.Status, current.Error = "failed", problem; return nil })
		return
	}
	artifactPayloads := make(map[string]adapter.Attachment, len(attachments))
	if delivery.MessageContent != nil {
		for index, ref := range delivery.MessageContent.Attachments {
			artifactPayloads[ref.AssetID] = attachments[index]
		}
	}
	for _, part := range delivery.Parts {
		if part.Kind == messagecontent.PartArtifact && !strings.HasPrefix(strings.ToLower(part.MIME), "image/") && part.MIME != "application/pdf" {
			_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
				current.Status, current.Error = "failed", "DingTalk workflow delivery supports text, images and PDF files"
				return nil
			})
			return
		}
	}
	for _, initial := range delivery.Parts {
		_, latest := s.workflowDeliverySnapshot(runID, deliveryID)
		part := latest.Parts[initial.Ordinal-1]
		if part.Status == "accepted" || part.Status == "outcome_unknown" {
			continue
		}
		if part.Status != "pending" && !(retry && part.Status == "failed") {
			continue
		}
		payload := adapter.DeliveryPart{Kind: part.Kind, MIME: part.MIME, Ordinal: part.Ordinal, Digest: part.Digest,
			MessageContent: delivery.MessageContent, RenderManifest: delivery.RenderManifest, PreparedResourceID: part.PreparedResourceID}
		if part.Kind == messagecontent.PartMarkdown {
			payload.Text = delivery.RenderManifest.Parts[part.Ordinal-1].Text
		} else {
			attachment, ok := artifactPayloads[part.ArtifactID]
			if !ok {
				_ = s.updateWorkflowDeliveryPart(runID, deliveryID, part.Ordinal, func(current *WorkflowDeliveryPart) error {
					current.Status, current.Error = "failed", "Workflow delivery artifact could not be matched to the frozen part"
					return nil
				})
				continue
			}
			payload.Attachment = &attachment
		}
		if err := s.updateWorkflowDeliveryPart(runID, deliveryID, part.Ordinal, func(current *WorkflowDeliveryPart) error {
			if current.Status != "pending" && !(retry && current.Status == "failed") {
				return errors.New("Workflow delivery part is already active")
			}
			current.Status, current.Error = "preparing", ""
			return nil
		}); err != nil {
			continue
		}
		if payload.Kind == messagecontent.PartArtifact && payload.PreparedResourceID == "" {
			resource, err := transport.PrepareDeliveryPartResource(ctx, delivery.InstanceID, payload)
			if err != nil {
				_ = s.updateWorkflowDeliveryPart(runID, deliveryID, part.Ordinal, func(current *WorkflowDeliveryPart) error {
					current.Status, current.Error = "failed", "DingTalk workflow attachment preparation failed"
					return nil
				})
				continue
			}
			payload.PreparedResourceID = resource
		}
		if err := s.updateWorkflowDeliveryPart(runID, deliveryID, part.Ordinal, func(current *WorkflowDeliveryPart) error {
			current.PreparedResourceID = payload.PreparedResourceID
			current.Status, current.Error = "sending", ""
			current.Attempts++
			return nil
		}); err != nil {
			continue
		}
		ack, sendErr := transport.SendPreparedPartWithReceipt(ctx, delivery.InstanceID, delivery.ChatID, payload)
		status := string(ack.Status)
		if status != "failed" && status != "accepted" && status != "delivered" {
			status = "outcome_unknown"
		}
		if sendErr != nil && status != "failed" {
			status = "outcome_unknown"
		}
		persistErr := s.updateWorkflowDeliveryPart(runID, deliveryID, part.Ordinal, func(current *WorkflowDeliveryPart) error {
			current.Status, current.ExternalMessageID, current.Error = status, ack.ExternalMessageID, ""
			if sendErr != nil {
				current.Error = "DingTalk workflow delivery failed; inspect its status before retrying"
			}
			return nil
		})
		if persistErr == nil && (status == "accepted" || status == "outcome_unknown") {
			part.ExternalMessageID, part.Status = ack.ExternalMessageID, status
			s.queryWorkflowDeliveryPart(ctx, runID, deliveryID, part, transport)
		}
	}
}

func (s *Server) queryWorkflowDeliveryPart(ctx context.Context, runID, deliveryID string, part WorkflowDeliveryPart, transport workflowDeliveryTransport) {
	if part.ExternalMessageID == "" {
		_ = s.updateWorkflowDeliveryPart(runID, deliveryID, part.Ordinal, func(current *WorkflowDeliveryPart) error {
			current.Status, current.Error = "outcome_unknown", "Delivery has no provider receipt; it cannot be resent automatically"
			return nil
		})
		return
	}
	for attempt := 0; attempt < 5; attempt++ {
		if ctx.Err() != nil {
			return
		}
		ack, err := transport.QueryReceipt(ctx, s.workflowDeliveryInstance(runID, deliveryID), part.ExternalMessageID)
		status := string(ack.Status)
		if status != "delivered" && status != "failed" && status != "accepted" {
			status = "outcome_unknown"
		}
		if err != nil {
			return
		}
		if err = s.updateWorkflowDeliveryPart(runID, deliveryID, part.Ordinal, func(current *WorkflowDeliveryPart) error {
			current.Status, current.Error = status, ""
			return nil
		}); err != nil || status != "accepted" {
			return
		}
		if attempt < 4 {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func (s *Server) workflowDeliveryInstance(runID, deliveryID string) string {
	_, delivery := s.workflowDeliverySnapshot(runID, deliveryID)
	if delivery == nil {
		return ""
	}
	return delivery.InstanceID
}

// RecoverWorkflowDeliveries 只恢复已经提交的投递，不从历史定义补造新的外发。
func (s *Server) RecoverWorkflowDeliveries(ctx context.Context) error {
	ws := s.workflowStore
	ws.mu.Lock()
	var runIDs []string
	for id, run := range ws.runs {
		if run.Status != "completed" || (len(run.Deliveries) == 0 && len(run.GeneratedFiles) == 0) {
			continue
		}
		runIDs = append(runIDs, id)
		for i := range run.GeneratedFiles {
			run.GeneratedFiles[i].active = false
			if run.GeneratedFiles[i].Status == "rendering" {
				run.GeneratedFiles[i].Status = "pending"
			}
		}
		for i := range run.Deliveries {
			delivery := &run.Deliveries[i]
			if (delivery.Kind == "session" || delivery.Kind == "directory") && delivery.Status == "sending" {
				delivery.Status = "pending"
			} else if delivery.Kind == "email" && delivery.Status == "sending" {
				delivery.Status, delivery.ErrorCode, delivery.Error = "outcome_unknown", "DELIVERY_OUTCOME_UNKNOWN", "Email sending was interrupted after entering the SMTP boundary"
			}
			for j := range delivery.Parts {
				part := &delivery.Parts[j]
				if part.Status == "sending" {
					part.Status, part.Error = "outcome_unknown", "Delivery was interrupted after entering the provider boundary"
				} else if part.Status == "preparing" {
					part.Status = "pending"
				}
			}
			if len(delivery.Parts) > 0 {
				delivery.Status = workflowDeliveryStatus(delivery.Parts)
			}
		}
	}
	err := ws.persistRuns()
	ws.mu.Unlock()
	if err != nil {
		return err
	}
	sort.Strings(runIDs)
	for _, id := range runIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.dispatchWorkflowDelivery(ctx, id)
	}
	return nil
}

func (s *Server) handleRetryWorkflowDelivery(w http.ResponseWriter, r *http.Request) {
	runID, deliveryID := r.PathValue("id"), r.PathValue("delivery")
	run, delivery := s.workflowDeliverySnapshot(runID, deliveryID)
	if run == nil || delivery == nil || run.HistoryDeleted || (run.Request != nil && run.Request.UserID != "" && run.Request.UserID != sessionUserIDFromRequest(r)) {
		writeAPIError(w, 404, "NOT_FOUND", "Workflow delivery was not found")
		return
	}
	if delivery.Status == "sending" || delivery.Status == "pending" || delivery.Status == "delivered" {
		writeAPIError(w, 409, "DELIVERY_NOT_RETRYABLE", "Workflow delivery is active or already delivered")
		return
	}
	if delivery.Kind == "email" && (delivery.Status == "accepted" || delivery.Status == "outcome_unknown") {
		writeAPIError(w, 409, "DELIVERY_OUTCOME_UNKNOWN", "Email is accepted or its outcome is unknown; it cannot be resent automatically")
		return
	}
	if delivery.Status == "outcome_unknown" {
		for _, part := range delivery.Parts {
			if part.Status == "outcome_unknown" && part.ExternalMessageID == "" {
				writeAPIError(w, 409, "DELIVERY_OUTCOME_UNKNOWN", "Delivery has no provider receipt; it cannot be resent automatically")
				return
			}
		}
	}
	retry := delivery.Status == "failed"
	if retry {
		if err := s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
			if current.active || current.Status != "failed" {
				return errors.New("Workflow delivery is already active")
			}
			for i := range current.Parts {
				if current.Parts[i].Status == "failed" {
					current.Parts[i].Status, current.Parts[i].Error = "pending", ""
				}
			}
			current.Status, current.Error, current.ErrorCode = "pending", "", ""
			return nil
		}); err != nil {
			writeAPIError(w, 409, "DELIVERY_NOT_RETRYABLE", "Workflow delivery could not be scheduled; reload its status")
			return
		}
		_, delivery = s.workflowDeliverySnapshot(runID, deliveryID)
	}
	var transport workflowDeliveryTransport
	if s.instanceMgr != nil {
		transport = s.instanceMgr
	}
	go s.dispatchWorkflowDeliveryWithTransport(context.Background(), runID, deliveryID, retry, transport)
	current, _ := s.workflowDeliverySnapshot(runID, deliveryID)
	writeJSON(w, http.StatusAccepted, workflowRunPublic(current))
}

func (s *Server) handleWorkflowDeliveryTargets(w http.ResponseWriter, r *http.Request) {
	type target struct {
		ChatID string `json:"chat_id"`
		Label  string `json:"label"`
	}
	type instance struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Status   string   `json:"status"`
		Provider string   `json:"provider"`
		Targets  []target `json:"targets"`
	}
	result := make([]instance, 0)
	if s.instanceMgr == nil {
		writeJSON(w, 200, map[string]any{"instances": result})
		return
	}
	list, err := s.instanceMgr.List(r.Context())
	if err != nil {
		writeAPIError(w, 500, "DELIVERY_TARGETS_UNAVAILABLE", "Workflow delivery targets could not be loaded")
		return
	}
	for _, inst := range list {
		if inst.Provider != "dingtalk" && inst.Provider != "email" {
			continue
		}
		item := instance{ID: inst.ID, Name: inst.Name, Status: string(inst.Status), Provider: inst.Provider, Targets: []target{}}
		if inst.Provider == "email" {
			result = append(result, item)
			continue
		}
		seen := map[string]bool{}
		add := func(chatID, label string) {
			chatID = strings.TrimSpace(chatID)
			if chatID == "" || chatID[0] < 0x20 {
				return
			}
			if seen[chatID] {
				if label != "" {
					for index := range item.Targets {
						if item.Targets[index].ChatID == chatID && item.Targets[index].Label == chatID {
							item.Targets[index].Label = label
						}
					}
				}
				return
			}
			seen[chatID] = true
			item.Targets = append(item.Targets, target{ChatID: chatID, Label: firstNonEmpty(label, chatID)})
		}
		if s.agentRouter != nil {
			for _, rule := range s.agentRouter.ListRules() {
				if rule.Platform != "dingtalk" {
					continue
				}
				matches := rule.InstanceID == inst.ID || rule.InstanceID == inst.Name
				if rule.InstanceID == "" {
					id, resolveErr := s.instanceMgr.ResolveRunningInstanceID("dingtalk", "")
					matches = resolveErr == nil && id == inst.ID
				}
				if matches {
					chatID := firstNonEmpty(rule.ChatID, rule.UserID)
					label := ""
					if s.store != nil && chatID != "" {
						for _, ref := range []string{inst.Name, inst.ID} {
							if sess, lookupErr := s.store.FindSessionByScope(r.Context(), firstNonEmpty(rule.UserID, chatID), "dingtalk", ref, chatID); lookupErr == nil {
								label = sess.Title
								break
							}
						}
					}
					add(chatID, label)
				}
			}
		}
		if s.store != nil {
			for offset := 0; ; offset += 100 {
				sessions, listErr := s.store.ListSessions(r.Context(), sessionUserIDFromRequest(r), 100, offset)
				if listErr != nil {
					writeAPIError(w, 500, "DELIVERY_TARGETS_UNAVAILABLE", "Workflow conversations could not be loaded")
					return
				}
				for _, sess := range sessions {
					if sess.Platform == "dingtalk" && (sess.InstanceID == inst.ID || sess.InstanceID == inst.Name) {
						add(sess.ChatID, sess.Title)
					}
				}
				if len(sessions) < 100 {
					break
				}
			}
		}
		sort.Slice(item.Targets, func(i, j int) bool { return item.Targets[i].ChatID < item.Targets[j].ChatID })
		result = append(result, item)
	}
	writeJSON(w, 200, map[string]any{"instances": result})
}
