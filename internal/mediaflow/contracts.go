package mediaflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/ai-core/media/voicechat"
)

var (
	ErrNotFound    = errors.New("media operation not found")
	ErrConflict    = errors.New("media operation revision or intent conflict")
	ErrUnavailable = errors.New("requested media capability is unavailable")
	ErrAmbiguous   = errors.New("multiple media routes match; specify provider and model")
)

// Origin 只接受入口提供的真实身份，不从模型参数读取目标或归属。
type Origin struct {
	ToolCallID    string `json:"tool_call_id,omitempty"`
	RequestDigest string `json:"request_digest,omitempty"`
	AgentName     string `json:"agent_name,omitempty"`
	Owner         string `json:"owner"`
	Kind          string `json:"kind"`
	RequestID     string `json:"request_id"`
	Ordinal       string `json:"ordinal"`
	SessionID     string `json:"session_id,omitempty"`
	Platform      string `json:"platform,omitempty"`
	InstanceID    string `json:"instance_id,omitempty"`
	ChatID        string `json:"chat_id,omitempty"`
}

type SpeechRequest struct {
	Text    string                  `json:"text"`
	Options voice.SynthesizeOptions `json:"options"`
}

// Request 的参数形态区分不同操作；未指定的默认值直到首次建操作时才解析。
type Request struct {
	Selection         *Selection         `json:"selection,omitempty"`
	Origin            Origin             `json:"origin"`
	Kind              string             `json:"kind"`
	Action            string             `json:"action"`
	Provider          string             `json:"provider,omitempty"`
	Model             string             `json:"model,omitempty"`
	PreferredProvider string             `json:"-"`
	ParentAssetID     string             `json:"parent_asset_id,omitempty"`
	Image             *imagegen.Request  `json:"image,omitempty"`
	Video             *videogen.Request  `json:"video,omitempty"`
	Speech            *SpeechRequest     `json:"speech,omitempty"`
	Chat              *voicechat.Request `json:"chat,omitempty"`
}

type Asset struct {
	Index         int    `json:"output_index"`
	ID            string `json:"asset_id"`
	OperationID   string `json:"operation_id"`
	Kind          string `json:"kind"`
	Role          string `json:"role"`
	Path          string `json:"file_path"`
	MIME          string `json:"mime"`
	Digest        string `json:"digest"`
	Size          int64  `json:"size"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	ParentAssetID string `json:"parent_asset_id,omitempty"`
}

type Operation struct {
	CandidateRoutes    []Route             `json:"candidate_routes,omitempty"`
	AttemptIndex       int                 `json:"attempt_index,omitempty"`
	Attempts           []GenerationAttempt `json:"attempts,omitempty"`
	ActualProviderID   string              `json:"actual_provider_instance_id,omitempty"`
	InputBlob          string              `json:"input_blob,omitempty"`
	ID                 string              `json:"operation_id"`
	Owner              string              `json:"owner"`
	IdempotencyKey     string              `json:"-"`
	IntentDigest       string              `json:"intent_digest"`
	ExecutionDigest    string              `json:"execution_digest"`
	Request            Request             `json:"request"`
	Route              Route               `json:"route"`
	GenerationState    string              `json:"generation_state"`
	SaveState          string              `json:"save_state"`
	UpstreamID         string              `json:"upstream_task_id,omitempty"`
	CancelRequested    bool                `json:"cancel_requested,omitempty"`
	Revision           int64               `json:"revision"`
	CompletionRevision int64               `json:"completion_revision,omitempty"`
	Assets             []Asset             `json:"assets"`
	ErrorCode          string              `json:"error_code,omitempty"`
	Error              string              `json:"error,omitempty"`
	RecoveryKind       string              `json:"recovery_source_kind,omitempty"`
	RecoveryRef        string              `json:"recovery_source_ref,omitempty"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
	Timings            map[string]int64    `json:"timings_ms,omitempty"`
	Result             json.RawMessage     `json:"result,omitempty"`
}

func (o *Operation) Settled() bool {
	return o.SaveState == "ready" || o.SaveState == "failed" || o.SaveState == "partial" || o.GenerationState == "failed" || o.GenerationState == "unknown" || o.GenerationState == "cancelled"
}

// Repository 的更新以真实数据库 revision 为依据，操作和资产一起提交。
type Repository interface {
	ListForRequest(context.Context, string, Origin) ([]*Operation, error)
	Find(context.Context, string, string) (*Operation, error)
	Get(context.Context, string, string) (*Operation, error)
	Create(context.Context, *Operation) (*Operation, error)
	Update(context.Context, *Operation, int64) error
	Pending(context.Context, int) ([]*Operation, error)
	Interrupt(context.Context) error
}

type AssetPathRepository interface {
	AuthorizePath(context.Context, string, string) (bool, error)
}

type UnknownOperationError struct{ OperationID string }

func (e *UnknownOperationError) Error() string {
	return "original media operation " + e.OperationID + " has an unknown result; query or resume it instead of generating a replacement"
}

type RecoveryRequiredError struct{ OperationID string }

func (e *RecoveryRequiredError) Error() string {
	return "media operation " + e.OperationID + " already completed generation; resume it to restore pending assets instead of generating a replacement"
}

func SourceRequestDigest(content string, attachments any) string {
	return digest(struct {
		Content     string
		Attachments any
	}{content, attachments})
}

// ReplyCompletion 由原回合编排提供；单个媒体操作不能创建或提前结束回复。
type ReplyCompletion struct {
	Resend           bool
	ResendOperations []string
	Owner            string
	SessionID        string
	RequestID        string
	Operations       []string
	Assets           []Asset
	Platform         string
	InstanceID       string
	ChatID           string
}

type replyCompletionKey struct{}

func WithReplyCompletion(ctx context.Context, c ReplyCompletion) context.Context {
	return context.WithValue(ctx, replyCompletionKey{}, c)
}
func ReplyCompletionFromContext(ctx context.Context) (ReplyCompletion, bool) {
	c, ok := ctx.Value(replyCompletionKey{}).(ReplyCompletion)
	return c, ok
}

func DeliveryID(c ReplyCompletion) string {
	return "media-delivery-" + digest([]string{c.Owner, c.SessionID, c.RequestID, c.Platform, c.InstanceID, c.ChatID})
}
