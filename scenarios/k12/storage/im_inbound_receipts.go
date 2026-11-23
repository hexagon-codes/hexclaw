package k12storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/toolkit/util/idgen"

	"github.com/hexagon-codes/hexclaw/records"
)

// ErrInboundPhotoConflict 表示同一提供方消息身份被重放为不同的不可变命令或图片。
var ErrInboundPhotoConflict = errors.New("inbound photo identity conflict")

type InboundPhotoProcessingStatus string
type InboundPhotoRoutingDecision string
type InboundPhotoConfirmationStatus string
type InboundPhotoReplyStatus string
type InboundPhotoTerminalStatus string
type InboundPhotoTerminalStage string
type InboundPhotoRoutingStage string

const (
	InboundPhotoRoutingStageIntent    InboundPhotoRoutingStage = "intent"
	InboundPhotoRoutingStageCandidate InboundPhotoRoutingStage = "candidate"
)

const (
	InboundPhotoAdmitted           InboundPhotoProcessingStatus = "admitted"
	InboundPhotoImageTaskSubmitted InboundPhotoProcessingStatus = "image_task_submitted"
	InboundPhotoFinalArtifactReady InboundPhotoProcessingStatus = "final_artifact_ready"

	InboundPhotoRoutePending       InboundPhotoRoutingDecision = "pending"
	InboundPhotoRouteRegrade       InboundPhotoRoutingDecision = "regrade"
	InboundPhotoRouteNewSubmission InboundPhotoRoutingDecision = "new_submission"
	InboundPhotoRouteAskedUser     InboundPhotoRoutingDecision = "asked_user"

	InboundPhotoConfirmationNotRequired InboundPhotoConfirmationStatus = "not_required"
	InboundPhotoConfirmationWaiting     InboundPhotoConfirmationStatus = "waiting"
	InboundPhotoConfirmationConfirmed   InboundPhotoConfirmationStatus = "confirmed"

	InboundPhotoReplyPending   InboundPhotoReplyStatus = "pending"
	InboundPhotoReplyReady     InboundPhotoReplyStatus = "ready"
	InboundPhotoReplyBound     InboundPhotoReplyStatus = "bound"
	InboundPhotoReplyDelivered InboundPhotoReplyStatus = "delivered"

	InboundPhotoTerminalFailed InboundPhotoTerminalStatus = "failed"

	InboundPhotoTerminalStageImageTask InboundPhotoTerminalStage = "image_task"
	InboundPhotoTerminalStageGrading   InboundPhotoTerminalStage = "grading"
	InboundPhotoTerminalStageDelivery  InboundPhotoTerminalStage = "delivery"
)

// InboundPhotoIdentity 是外部 direct 消息的唯一身份；图片摘要不参与该身份。
type InboundPhotoIdentity struct {
	Platform          string
	InstanceID        string
	ChatID            string
	ProviderMessageID string
	PageIndex         int
}

// InboundPhotoAdmission 是 ACK 之前必须原子持久化的完整输入。
type InboundPhotoAdmission struct {
	OwnerScope  string
	AgentName   string
	BindingID   string
	Identity    InboundPhotoIdentity
	AssetName   string
	AssetMIME   string
	AssetBytes  []byte
	CommandJSON string
}

type InboundPhotoReceipt struct {
	ReceiptID     string
	PageCount     int
	OwnerScope    string
	AgentName     string
	BindingID     string
	Identity      InboundPhotoIdentity
	CommandDigest string
	CommandJSON   string
	CreatedAt     int64
	UpdatedAt     int64
}

type InboundPhotoAsset struct {
	AssetID   string
	ReceiptID string
	Name      string
	MIME      string
	Size      int
	Digest    string
	Bytes     []byte
	CreatedAt int64
}

type InboundPhotoDispatchState struct {
	ProcessingStatus   InboundPhotoProcessingStatus
	RoutingDecision    InboundPhotoRoutingDecision
	ConfirmationStatus InboundPhotoConfirmationStatus
	ImageTaskID        string
	FinalArtifactID    string
	ReplyStatus        InboundPhotoReplyStatus
	DeliveryBatchID    string
	TerminalStatus     InboundPhotoTerminalStatus
	TerminalStage      InboundPhotoTerminalStage
	FailureKind        string
}

type InboundPhotoDispatch struct {
	DispatchID string
	ReceiptID  string
	InboundPhotoDispatchState
	Version   int64
	CreatedAt int64
	UpdatedAt int64
}

// State 返回可用于下一次版本 CAS 的独立状态值。
func (d InboundPhotoDispatch) State() InboundPhotoDispatchState {
	return d.InboundPhotoDispatchState
}

type InboundPhotoBundle struct {
	Receipt  InboundPhotoReceipt
	Asset    InboundPhotoAsset
	Dispatch InboundPhotoDispatch
}

// InboundPhotoRoutingCandidate 是一次候选快照中的可展示事实；PracticeSetID 只在服务端
// 选择确认时使用，渠道文案必须通过序号、卷面号和日期投影，不能暴露该内部 ID。
type InboundPhotoRoutingCandidate struct {
	PracticeSetID string `json:"practice_set_id"`
	PaperNo       string `json:"paper_no"`
	Title         string `json:"title,omitempty"`
	SentAt        int64  `json:"sent_at"`
}

// InboundPhotoRoutingSnapshot 冻结候选顺序和确认结果，重启后只能读取此快照，不能重新
// 依据可变练习集列表推导另一张卷。
type InboundPhotoRoutingSnapshot struct {
	ReceiptID             string                         `json:"receipt_id"`
	Stage                 InboundPhotoRoutingStage       `json:"stage"`
	SnapshotDigest        string                         `json:"snapshot_digest"`
	RequestDigest         string                         `json:"request_digest,omitempty"`
	Candidates            []InboundPhotoRoutingCandidate `json:"candidates"`
	SelectedPracticeSetID string                         `json:"selected_practice_set_id,omitempty"`
	SelectionDigest       string                         `json:"selection_digest,omitempty"`
	Version               int64                          `json:"version"`
	CreatedAt             int64                          `json:"created_at"`
	UpdatedAt             int64                          `json:"updated_at"`
}

func inboundPhotoDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeInboundPhotoIdentity(identity InboundPhotoIdentity) (InboundPhotoIdentity, error) {
	identity.Platform = strings.ToLower(strings.TrimSpace(identity.Platform))
	identity.InstanceID = strings.TrimSpace(identity.InstanceID)
	identity.ChatID = strings.TrimSpace(identity.ChatID)
	identity.ProviderMessageID = strings.TrimSpace(identity.ProviderMessageID)
	if identity.Platform == "" || identity.InstanceID == "" || identity.ChatID == "" ||
		identity.ProviderMessageID == "" || identity.PageIndex < 0 {
		return InboundPhotoIdentity{}, fmt.Errorf("k12storage: inbound photo identity is incomplete")
	}
	return identity, nil
}

func normalizeInboundPhotoAdmission(input InboundPhotoAdmission, receiptID string, receivedAt int64) (InboundPhotoBundle, error) {
	input.OwnerScope = strings.TrimSpace(input.OwnerScope)
	input.AgentName = strings.TrimSpace(input.AgentName)
	input.BindingID = strings.TrimSpace(input.BindingID)
	input.AssetName = strings.TrimSpace(input.AssetName)
	input.AssetMIME = strings.ToLower(strings.TrimSpace(input.AssetMIME))
	input.CommandJSON = strings.TrimSpace(input.CommandJSON)
	identity, err := normalizeInboundPhotoIdentity(input.Identity)
	if err != nil {
		return InboundPhotoBundle{}, err
	}
	if input.OwnerScope == "" || input.AgentName == "" || input.BindingID == "" ||
		input.AssetName == "" || !strings.HasPrefix(input.AssetMIME, "image/") ||
		len(input.AssetBytes) == 0 {
		return InboundPhotoBundle{}, fmt.Errorf("k12storage: inbound photo admission is incomplete")
	}
	if input.CommandJSON == "" || !json.Valid([]byte(input.CommandJSON)) {
		return InboundPhotoBundle{}, fmt.Errorf("k12storage: inbound photo command is not valid JSON")
	}
	assetBytes := append([]byte(nil), input.AssetBytes...)
	return InboundPhotoBundle{
		Receipt: InboundPhotoReceipt{
			ReceiptID: receiptID, OwnerScope: input.OwnerScope, AgentName: input.AgentName,
			BindingID: input.BindingID, Identity: identity,
			CommandDigest: inboundPhotoDigest([]byte(input.CommandJSON)),
			CommandJSON:   input.CommandJSON, CreatedAt: receivedAt, UpdatedAt: receivedAt,
		},
		Asset: InboundPhotoAsset{
			AssetID: idgen.NanoID(), ReceiptID: receiptID, Name: input.AssetName,
			MIME: input.AssetMIME, Size: len(assetBytes),
			Digest: inboundPhotoDigest(assetBytes), Bytes: assetBytes, CreatedAt: receivedAt,
		},
		Dispatch: InboundPhotoDispatch{
			DispatchID: idgen.NanoID(), ReceiptID: receiptID,
			InboundPhotoDispatchState: InboundPhotoDispatchState{
				ProcessingStatus:   InboundPhotoAdmitted,
				RoutingDecision:    InboundPhotoRoutePending,
				ConfirmationStatus: InboundPhotoConfirmationNotRequired,
				ReplyStatus:        InboundPhotoReplyPending,
			},
			CreatedAt: receivedAt, UpdatedAt: receivedAt,
		},
	}, nil
}

const inboundPhotoDispatchColumns = `d.dispatch_id,d.receipt_id,d.processing_status,d.routing_decision,
	d.confirmation_status,d.image_task_id,d.final_artifact_id,d.reply_status,
	d.delivery_batch_id,d.terminal_status,d.terminal_stage,d.failure_kind,
	d.version,d.created_at,d.updated_at`

func inboundPhotoDispatchScanArgs(d *InboundPhotoDispatch) []any {
	return []any{&d.DispatchID, &d.ReceiptID, &d.ProcessingStatus, &d.RoutingDecision,
		&d.ConfirmationStatus, &d.ImageTaskID, &d.FinalArtifactID, &d.ReplyStatus,
		&d.DeliveryBatchID, &d.TerminalStatus, &d.TerminalStage, &d.FailureKind,
		&d.Version, &d.CreatedAt, &d.UpdatedAt}
}

const inboundPhotoBundleSelect = `SELECT
	p.receipt_id,r.owner_scope,r.agent_name,r.binding_id,
	r.platform,r.instance_id,r.chat_id,r.provider_message_id,p.page_index,r.page_count,
	r.command_digest,r.command_json,r.created_at,r.updated_at,
	a.asset_id,a.receipt_id,a.asset_name,a.asset_mime,a.byte_size,
	a.content_digest,a.asset_bytes,a.created_at,
	` + inboundPhotoDispatchColumns + `
	FROM k12_im_inbound_receipts AS r
	JOIN k12_im_inbound_pages AS p ON p.source_receipt_id=r.receipt_id
	JOIN k12_im_inbound_assets AS a ON a.receipt_id=p.receipt_id
	JOIN k12_im_inbound_dispatches AS d ON d.receipt_id=p.receipt_id`

func scanInboundPhotoBundle(row rowScanner) (InboundPhotoBundle, error) {
	var bundle InboundPhotoBundle
	args := []any{
		&bundle.Receipt.ReceiptID, &bundle.Receipt.OwnerScope,
		&bundle.Receipt.AgentName, &bundle.Receipt.BindingID,
		&bundle.Receipt.Identity.Platform, &bundle.Receipt.Identity.InstanceID,
		&bundle.Receipt.Identity.ChatID, &bundle.Receipt.Identity.ProviderMessageID,
		&bundle.Receipt.Identity.PageIndex, &bundle.Receipt.PageCount,
		&bundle.Receipt.CommandDigest, &bundle.Receipt.CommandJSON,
		&bundle.Receipt.CreatedAt, &bundle.Receipt.UpdatedAt,
		&bundle.Asset.AssetID, &bundle.Asset.ReceiptID, &bundle.Asset.Name,
		&bundle.Asset.MIME, &bundle.Asset.Size, &bundle.Asset.Digest,
		&bundle.Asset.Bytes, &bundle.Asset.CreatedAt,
	}
	if err := row.Scan(append(args, inboundPhotoDispatchScanArgs(&bundle.Dispatch)...)...); err != nil {
		return InboundPhotoBundle{}, err
	}
	bundle.Asset.Bytes = append([]byte(nil), bundle.Asset.Bytes...)
	return bundle, nil
}

type inboundPhotoQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getInboundPhotoByIdentity(
	ctx context.Context, q inboundPhotoQueryer, identity InboundPhotoIdentity,
) (InboundPhotoBundle, error) {
	return scanInboundPhotoBundle(q.QueryRowContext(ctx, inboundPhotoBundleSelect+`
		WHERE r.platform=? AND r.instance_id=? AND r.chat_id=? AND r.provider_message_id=? AND p.page_index=?`,
		identity.Platform, identity.InstanceID, identity.ChatID, identity.ProviderMessageID, identity.PageIndex,
	))
}

func getInboundPhotoByReceipt(
	ctx context.Context, q inboundPhotoQueryer, agentName, receiptID string,
) (InboundPhotoBundle, error) {
	return scanInboundPhotoBundle(q.QueryRowContext(ctx, inboundPhotoBundleSelect+`
		WHERE r.agent_name=? AND p.receipt_id=?`, agentName, receiptID))
}

func sameInboundPhotoContent(stored, candidate InboundPhotoBundle) bool {
	return stored.Asset.Digest == candidate.Asset.Digest &&
		bytes.Equal(stored.Asset.Bytes, candidate.Asset.Bytes)
}

// AdmitInboundPhoto 保留单图的零页序和原始收据身份。
func (s *Store) AdmitInboundPhoto(ctx context.Context, input InboundPhotoAdmission) (InboundPhotoBundle, bool, error) {
	pages, created, err := s.AdmitInboundPhotoPages(ctx, []InboundPhotoAdmission{input})
	if err != nil {
		return InboundPhotoBundle{}, false, err
	}
	return pages[0], created, nil
}

// AdmitInboundPhotoPages 在一个事务中接纳整条物理消息；仅已完整提交的页可以被 worker 恢复。
func (s *Store) AdmitInboundPhotoPages(ctx context.Context, inputs []InboundPhotoAdmission) ([]InboundPhotoBundle, bool, error) {
	if len(inputs) == 0 {
		return nil, false, fmt.Errorf("inbound photo admission requires at least one page")
	}
	sourceReceiptID := idgen.NanoID()
	receivedAt := nowUnix()
	pages := make([]InboundPhotoBundle, len(inputs))
	for i, input := range inputs {
		if input.Identity.PageIndex != 0 && input.Identity.PageIndex != i {
			return nil, false, fmt.Errorf("%w: invalid page order", ErrInboundPhotoConflict)
		}
		input.Identity.PageIndex = 0
		pageID := sourceReceiptID
		if len(inputs) > 1 {
			// 单图沿用物理收据的原 ID；组内每页使用独立的内部 ID，不会与旧单图的 NanoID 相等。
			pageID = fmt.Sprintf("%s:page:%d", sourceReceiptID, i)
		}
		page, err := normalizeInboundPhotoAdmission(input, pageID, receivedAt)
		if err != nil {
			return nil, false, err
		}
		if i > 0 {
			first := pages[0].Receipt
			if page.Receipt.Identity != first.Identity || page.Receipt.OwnerScope != first.OwnerScope ||
				page.Receipt.AgentName != first.AgentName || page.Receipt.BindingID != first.BindingID ||
				page.Receipt.CommandJSON != first.CommandJSON {
				return nil, false, fmt.Errorf("%w: pages must have the same message and route", ErrInboundPhotoConflict)
			}
		}
		page.Receipt.Identity.PageIndex = i
		page.Receipt.PageCount = len(inputs)
		pages[i] = page
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin inbound photo admission: %w", err)
	}
	defer tx.Rollback()
	created, err := insertInboundPhotoSource(ctx, tx, sourceReceiptID, pages[0].Receipt)
	if err != nil {
		return nil, false, err
	}
	for i, page := range pages {
		if created {
			if err := insertInboundPhotoPage(ctx, tx, sourceReceiptID, page); err != nil {
				return nil, false, err
			}
			continue
		}
		stored, err := getInboundPhotoByIdentity(ctx, tx, page.Receipt.Identity)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, records.ErrScopeNotFound
		}
		if err != nil {
			return nil, false, fmt.Errorf("read replayed inbound photo: %w", err)
		}
		if stored.Receipt.PageCount != len(inputs) || !sameInboundPhotoContent(stored, page) {
			return nil, false, fmt.Errorf("%w: different page count or content at page %d", ErrInboundPhotoConflict, i)
		}
		pages[i] = stored
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit inbound photo admission: %w", err)
	}
	return pages, created, nil
}

func insertInboundPhotoSource(ctx context.Context, tx *sql.Tx, sourceReceiptID string, receipt InboundPhotoReceipt) (bool, error) {
	identity := receipt.Identity
	// 首个数据库操作取得写锁。重复回调使用原消息已冻结的归属和路由。
	result, err := tx.ExecContext(ctx, `INSERT INTO k12_im_inbound_receipts(
		receipt_id,owner_scope,agent_name,binding_id,platform,instance_id,chat_id,
		provider_message_id,page_count,command_digest,command_json,created_at,updated_at
	) SELECT ?,?,?,?,?,?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM agents WHERE name=?)
	ON CONFLICT(platform,instance_id,chat_id,provider_message_id) DO NOTHING`,
		sourceReceiptID, receipt.OwnerScope, receipt.AgentName, receipt.BindingID,
		identity.Platform, identity.InstanceID, identity.ChatID, identity.ProviderMessageID,
		receipt.PageCount, receipt.CommandDigest, receipt.CommandJSON,
		receipt.CreatedAt, receipt.UpdatedAt, receipt.AgentName)
	if err != nil {
		return false, fmt.Errorf("insert inbound photo source: %w", err)
	}
	created, err := result.RowsAffected()
	return created == 1, err
}

func insertInboundPhotoPage(ctx context.Context, tx *sql.Tx, sourceReceiptID string, candidate InboundPhotoBundle) error {
	receipt := candidate.Receipt
	if receipt.PageCount > 1 {
		// 单图页关联由消息表的 INSERT 触发器建立，包含旧版本程序写入的消息。
		if _, err := tx.ExecContext(ctx, `INSERT INTO k12_im_inbound_pages(receipt_id,source_receipt_id,page_index) VALUES(?,?,?)`,
			receipt.ReceiptID, sourceReceiptID, receipt.Identity.PageIndex); err != nil {
			return fmt.Errorf("insert inbound photo page: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO k12_im_inbound_assets(
		asset_id,receipt_id,asset_name,asset_mime,byte_size,content_digest,asset_bytes,created_at
	) VALUES(?,?,?,?,?,?,?,?)`,
		candidate.Asset.AssetID, receipt.ReceiptID, candidate.Asset.Name,
		candidate.Asset.MIME, candidate.Asset.Size, candidate.Asset.Digest,
		candidate.Asset.Bytes, candidate.Asset.CreatedAt,
	); err != nil {
		return fmt.Errorf("insert inbound photo asset: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO k12_im_inbound_dispatches(
		dispatch_id,receipt_id,processing_status,routing_decision,confirmation_status,
		image_task_id,final_artifact_id,reply_status,delivery_batch_id,version,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		candidate.Dispatch.DispatchID, receipt.ReceiptID,
		candidate.Dispatch.ProcessingStatus, candidate.Dispatch.RoutingDecision,
		candidate.Dispatch.ConfirmationStatus, candidate.Dispatch.ImageTaskID,
		candidate.Dispatch.FinalArtifactID, candidate.Dispatch.ReplyStatus,
		candidate.Dispatch.DeliveryBatchID, candidate.Dispatch.Version,
		candidate.Dispatch.CreatedAt, candidate.Dispatch.UpdatedAt,
	); err != nil {
		return fmt.Errorf("insert inbound photo dispatch: %w", err)
	}
	return nil
}

func (s *Store) GetInboundPhotoByIdentity(
	ctx context.Context, identity InboundPhotoIdentity,
) (InboundPhotoBundle, error) {
	identity, err := normalizeInboundPhotoIdentity(identity)
	if err != nil {
		return InboundPhotoBundle{}, err
	}
	bundle, err := getInboundPhotoByIdentity(ctx, s.db, identity)
	if errors.Is(err, sql.ErrNoRows) {
		return InboundPhotoBundle{}, records.ErrNotFound
	}
	if err != nil {
		return InboundPhotoBundle{}, fmt.Errorf("k12storage: get inbound photo by identity: %w", err)
	}
	return bundle, nil
}

func (s *Store) GetInboundPhoto(
	ctx context.Context, agentName, receiptID string,
) (InboundPhotoBundle, error) {
	agentName = strings.TrimSpace(agentName)
	receiptID = strings.TrimSpace(receiptID)
	if agentName == "" || receiptID == "" {
		return InboundPhotoBundle{}, fmt.Errorf("k12storage: inbound photo owner or receipt is empty")
	}
	bundle, err := getInboundPhotoByReceipt(ctx, s.db, agentName, receiptID)
	if errors.Is(err, sql.ErrNoRows) {
		return InboundPhotoBundle{}, records.ErrNotFound
	}
	if err != nil {
		return InboundPhotoBundle{}, fmt.Errorf("k12storage: get inbound photo: %w", err)
	}
	return bundle, nil
}

// PendingEarlierInboundPhotoPages 仅读同一来源先前各页的调度状态，供按页投递使用。
func (s *Store) PendingEarlierInboundPhotoPages(ctx context.Context, identity InboundPhotoIdentity) ([]InboundPhotoDispatch, error) {
	identity, err := normalizeInboundPhotoIdentity(identity)
	if err != nil {
		return nil, err
	}
	if identity.PageIndex == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+inboundPhotoDispatchColumns+`
		FROM k12_im_inbound_receipts r JOIN k12_im_inbound_pages p ON p.source_receipt_id=r.receipt_id
		JOIN k12_im_inbound_dispatches d ON d.receipt_id=p.receipt_id
		WHERE r.platform=? AND r.instance_id=? AND r.chat_id=? AND r.provider_message_id=? AND p.page_index<?
		AND d.reply_status!='delivered' AND d.terminal_status='' ORDER BY p.page_index`,
		identity.Platform, identity.InstanceID, identity.ChatID, identity.ProviderMessageID, identity.PageIndex)
	if err != nil {
		return nil, err
	}
	var pending []InboundPhotoDispatch
	for rows.Next() {
		var dispatch InboundPhotoDispatch
		if err := rows.Scan(inboundPhotoDispatchScanArgs(&dispatch)...); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		pending = append(pending, dispatch)
	}
	return pending, errors.Join(rows.Err(), rows.Close())
}

func inboundPhotoProcessingRank(status InboundPhotoProcessingStatus) int {
	switch status {
	case InboundPhotoAdmitted:
		return 0
	case InboundPhotoImageTaskSubmitted:
		return 1
	case InboundPhotoFinalArtifactReady:
		return 2
	default:
		return -1
	}
}

func inboundPhotoReplyRank(status InboundPhotoReplyStatus) int {
	switch status {
	case InboundPhotoReplyPending:
		return 0
	case InboundPhotoReplyReady:
		return 1
	case InboundPhotoReplyBound:
		return 2
	case InboundPhotoReplyDelivered:
		return 3
	default:
		return -1
	}
}

func normalizeInboundPhotoDispatchState(state InboundPhotoDispatchState) InboundPhotoDispatchState {
	state.ImageTaskID = strings.TrimSpace(state.ImageTaskID)
	state.FinalArtifactID = strings.TrimSpace(state.FinalArtifactID)
	state.DeliveryBatchID = strings.TrimSpace(state.DeliveryBatchID)
	state.TerminalStage = InboundPhotoTerminalStage(strings.TrimSpace(string(state.TerminalStage)))
	state.FailureKind = strings.TrimSpace(state.FailureKind)
	return state
}

func inboundPhotoFailureKindIsStructured(kind string) bool {
	if kind == "" {
		return false
	}
	for i, r := range kind {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '_' && i > 0) {
			continue
		}
		return false
	}
	return !strings.HasSuffix(kind, "_")
}

func validateInboundPhotoTerminalTransition(
	current InboundPhotoDispatch, next InboundPhotoDispatchState,
) error {
	if current.TerminalStatus != "" {
		return fmt.Errorf("%w: inbound photo dispatch is already terminal", records.ErrIllegalTransition)
	}
	switch next.TerminalStatus {
	case "":
		if next.TerminalStage != "" || next.FailureKind != "" {
			return fmt.Errorf("k12storage: incomplete inbound photo terminal state")
		}
		return nil
	case InboundPhotoTerminalFailed:
		switch next.TerminalStage {
		case InboundPhotoTerminalStageImageTask, InboundPhotoTerminalStageGrading:
			if current.ProcessingStatus != InboundPhotoImageTaskSubmitted || current.ImageTaskID == "" {
				return fmt.Errorf("k12storage: task terminal stage requires a submitted image task")
			}
		case InboundPhotoTerminalStageDelivery:
			if current.ReplyStatus != InboundPhotoReplyBound || current.DeliveryBatchID == "" {
				return fmt.Errorf("k12storage: delivery terminal stage requires a bound delivery batch")
			}
		default:
			return fmt.Errorf("k12storage: invalid inbound photo terminal stage")
		}
		if !inboundPhotoFailureKindIsStructured(next.FailureKind) {
			return fmt.Errorf("k12storage: invalid inbound photo failure kind")
		}
		currentState := current.State()
		currentState.TerminalStatus = ""
		currentState.TerminalStage = ""
		currentState.FailureKind = ""
		nonTerminalNext := next
		nonTerminalNext.TerminalStatus = ""
		nonTerminalNext.TerminalStage = ""
		nonTerminalNext.FailureKind = ""
		if currentState != nonTerminalNext {
			return fmt.Errorf("k12storage: terminal transition changed inbound photo checkpoint")
		}
		return nil
	default:
		return fmt.Errorf("k12storage: invalid inbound photo terminal status")
	}
}

func validateInboundPhotoDispatchTransition(
	current InboundPhotoDispatch, next InboundPhotoDispatchState,
) error {
	next = normalizeInboundPhotoDispatchState(next)
	if err := validateInboundPhotoTerminalTransition(current, next); err != nil {
		return err
	}
	currentProcessing := inboundPhotoProcessingRank(current.ProcessingStatus)
	nextProcessing := inboundPhotoProcessingRank(next.ProcessingStatus)
	if currentProcessing < 0 || nextProcessing < currentProcessing || nextProcessing > currentProcessing+1 {
		return fmt.Errorf("k12storage: invalid inbound photo processing transition")
	}
	currentReply := inboundPhotoReplyRank(current.ReplyStatus)
	nextReply := inboundPhotoReplyRank(next.ReplyStatus)
	if current.ReplyStatus == InboundPhotoReplyDelivered {
		return fmt.Errorf("%w: inbound photo reply is already delivered", records.ErrIllegalTransition)
	}
	if currentReply < 0 || nextReply < currentReply || nextReply > currentReply+1 {
		return fmt.Errorf("k12storage: invalid inbound photo reply transition")
	}
	if current.ImageTaskID != "" && next.ImageTaskID != current.ImageTaskID {
		return fmt.Errorf("k12storage: inbound photo image task identity is immutable")
	}
	if current.FinalArtifactID != "" && next.FinalArtifactID != current.FinalArtifactID {
		return fmt.Errorf("k12storage: inbound photo final artifact identity is immutable")
	}
	if current.DeliveryBatchID != "" && next.DeliveryBatchID != current.DeliveryBatchID {
		return fmt.Errorf("k12storage: inbound photo delivery batch identity is immutable")
	}
	switch next.ProcessingStatus {
	case InboundPhotoAdmitted:
		if next.ImageTaskID != "" || next.FinalArtifactID != "" {
			return fmt.Errorf("k12storage: admitted inbound photo cannot own task artifacts")
		}
	case InboundPhotoImageTaskSubmitted:
		if next.ImageTaskID == "" || next.FinalArtifactID != "" {
			return fmt.Errorf("k12storage: submitted inbound photo requires one image task")
		}
	case InboundPhotoFinalArtifactReady:
		if next.ImageTaskID == "" || next.FinalArtifactID == "" {
			return fmt.Errorf("k12storage: finalized inbound photo requires task and artifact")
		}
	default:
		return fmt.Errorf("k12storage: invalid inbound photo processing status")
	}
	switch current.RoutingDecision {
	case InboundPhotoRoutePending:
	case InboundPhotoRouteAskedUser:
		if next.RoutingDecision != InboundPhotoRouteAskedUser &&
			next.RoutingDecision != InboundPhotoRouteRegrade &&
			next.RoutingDecision != InboundPhotoRouteNewSubmission {
			return fmt.Errorf("k12storage: invalid confirmed inbound photo route")
		}
	case InboundPhotoRouteRegrade, InboundPhotoRouteNewSubmission:
		if next.RoutingDecision != current.RoutingDecision {
			return fmt.Errorf("k12storage: inbound photo routing decision is immutable")
		}
	default:
		return fmt.Errorf("k12storage: invalid current inbound photo route")
	}
	switch next.RoutingDecision {
	case InboundPhotoRoutePending:
		if next.ConfirmationStatus != InboundPhotoConfirmationNotRequired {
			return fmt.Errorf("k12storage: pending inbound photo route cannot wait for confirmation")
		}
	case InboundPhotoRouteAskedUser:
		if next.ConfirmationStatus != InboundPhotoConfirmationWaiting {
			return fmt.Errorf("k12storage: asked-user inbound photo route must wait for confirmation")
		}
	case InboundPhotoRouteRegrade, InboundPhotoRouteNewSubmission:
		if current.RoutingDecision == InboundPhotoRouteAskedUser {
			if next.ConfirmationStatus != InboundPhotoConfirmationConfirmed {
				return fmt.Errorf("k12storage: confirmed inbound photo route is missing confirmation")
			}
		} else if next.ConfirmationStatus != InboundPhotoConfirmationNotRequired &&
			next.ConfirmationStatus != InboundPhotoConfirmationConfirmed {
			return fmt.Errorf("k12storage: invalid inbound photo confirmation status")
		}
	default:
		return fmt.Errorf("k12storage: invalid inbound photo routing decision")
	}
	if current.ConfirmationStatus == InboundPhotoConfirmationConfirmed &&
		next.ConfirmationStatus != InboundPhotoConfirmationConfirmed {
		return fmt.Errorf("k12storage: inbound photo confirmation is immutable")
	}
	if next.ProcessingStatus == InboundPhotoFinalArtifactReady &&
		next.RoutingDecision != InboundPhotoRouteRegrade &&
		next.RoutingDecision != InboundPhotoRouteNewSubmission {
		return fmt.Errorf("k12storage: finalized inbound photo requires a resolved route")
	}
	switch next.ReplyStatus {
	case InboundPhotoReplyPending:
		if next.DeliveryBatchID != "" {
			return fmt.Errorf("k12storage: pending inbound photo reply cannot own a delivery batch")
		}
	case InboundPhotoReplyReady:
		if next.ProcessingStatus != InboundPhotoFinalArtifactReady || next.DeliveryBatchID != "" {
			return fmt.Errorf("k12storage: ready inbound photo reply requires a final artifact")
		}
	case InboundPhotoReplyBound, InboundPhotoReplyDelivered:
		if next.ProcessingStatus != InboundPhotoFinalArtifactReady || next.DeliveryBatchID == "" {
			return fmt.Errorf("k12storage: bound inbound photo reply requires artifact and delivery batch")
		}
	default:
		return fmt.Errorf("k12storage: invalid inbound photo reply status")
	}
	return nil
}

// CompareAndSwapInboundPhotoDispatch 只推进可恢复调度状态；版本不匹配时无写入。
func (s *Store) CompareAndSwapInboundPhotoDispatch(
	ctx context.Context,
	agentName, receiptID string,
	expectedVersion int64,
	next InboundPhotoDispatchState,
) (InboundPhotoDispatch, error) {
	current, err := s.GetInboundPhoto(ctx, agentName, receiptID)
	if err != nil {
		return InboundPhotoDispatch{}, err
	}
	if current.Dispatch.Version != expectedVersion {
		return InboundPhotoDispatch{}, records.ErrVersionConflict
	}
	next = normalizeInboundPhotoDispatchState(next)
	if err := validateInboundPhotoDispatchTransition(current.Dispatch, next); err != nil {
		return InboundPhotoDispatch{}, err
	}
	updatedAt := nowUnix()
	result, err := s.db.ExecContext(ctx, `UPDATE k12_im_inbound_dispatches SET
		processing_status=?,routing_decision=?,confirmation_status=?,image_task_id=?,
		final_artifact_id=?,reply_status=?,delivery_batch_id=?,terminal_status=?,terminal_stage=?,
		failure_kind=?,version=version+1,updated_at=?
		WHERE receipt_id=? AND version=? AND EXISTS(
			SELECT 1 FROM k12_im_inbound_pages p JOIN k12_im_inbound_receipts r ON r.receipt_id=p.source_receipt_id
			WHERE p.receipt_id=k12_im_inbound_dispatches.receipt_id AND r.agent_name=?
		)`, next.ProcessingStatus, next.RoutingDecision, next.ConfirmationStatus,
		next.ImageTaskID, next.FinalArtifactID, next.ReplyStatus, next.DeliveryBatchID,
		next.TerminalStatus, next.TerminalStage, next.FailureKind, updatedAt,
		receiptID, expectedVersion, strings.TrimSpace(agentName),
	)
	if err != nil {
		return InboundPhotoDispatch{}, fmt.Errorf("k12storage: advance inbound photo dispatch: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return InboundPhotoDispatch{}, records.ErrVersionConflict
	}
	current.Dispatch.InboundPhotoDispatchState = next
	current.Dispatch.Version++
	current.Dispatch.UpdatedAt = updatedAt
	return current.Dispatch, nil
}

func (s *Store) ListRecoverableInboundPhotos(
	ctx context.Context, limit int,
) ([]InboundPhotoBundle, error) {
	return s.listRecoverableInboundPhotos(ctx, limit, nil)
}

// ListRecoverableInboundPhotosAfter 使用不可变派发身份分页，状态时间变化不会改变扫描位置。
// 空游标读取第一页；返回空页表示本次扫描没有后续可恢复记录。
func (s *Store) ListRecoverableInboundPhotosAfter(
	ctx context.Context, afterDispatchID string, limit int,
) ([]InboundPhotoBundle, error) {
	return s.listRecoverableInboundPhotos(ctx, limit, &afterDispatchID)
}

func (s *Store) listRecoverableInboundPhotos(
	ctx context.Context, limit int, afterDispatchID *string,
) ([]InboundPhotoBundle, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("k12storage: inbound photo recovery limit is invalid")
	}
	query := inboundPhotoBundleSelect + ` WHERE d.terminal_status='' AND d.reply_status!='delivered'`
	args := []any{}
	if afterDispatchID == nil {
		query += ` ORDER BY d.updated_at,d.dispatch_id LIMIT ?`
	} else {
		query += ` AND d.dispatch_id>? ORDER BY d.dispatch_id LIMIT ?`
		args = append(args, *afterDispatchID)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("k12storage: list recoverable inbound photos: %w", err)
	}
	defer rows.Close()
	bundles := make([]InboundPhotoBundle, 0)
	for rows.Next() {
		bundle, scanErr := scanInboundPhotoBundle(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("k12storage: scan recoverable inbound photo: %w", scanErr)
		}
		bundles = append(bundles, bundle)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("k12storage: iterate recoverable inbound photos: %w", err)
	}
	return bundles, nil
}
