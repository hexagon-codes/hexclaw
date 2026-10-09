package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/internal/upstreamerr"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

type UnitSummaryRequest struct {
	OwnerID             string                   `json:"-"`
	AgentName           string                   `json:"agent"`
	SessionID           string                   `json:"session_id"`
	SourceMessageID     string                   `json:"source_message_id"`
	IdempotencyKey      string                   `json:"idempotency_key"`
	FinalUserText       string                   `json:"final_user_text"`
	PromptInvocation    json.RawMessage          `json:"prompt_invocation,omitempty"`
	ActiveRevisionID    string                   `json:"active_revision_id,omitempty"`
	Subject             string                   `json:"subject,omitempty"`
	Intent              string                   `json:"intent,omitempty"`
	UnitID              string                   `json:"unit_id,omitempty"`
	UnitTitle           string                   `json:"unit_title,omitempty"`
	MaterialDocumentIDs []string                 `json:"material_document_ids,omitempty"`
	Format              string                   `json:"format,omitempty"`
	ModelSnapshot       k12.GradingModelSnapshot `json:"model_snapshot,omitempty"`
}
type UnitSummaryResumeRequest struct {
	ExpectedRevision  int    `json:"expected_revision"`
	IdempotencyKey    string `json:"idempotency_key"`
	FollowupMessageID string `json:"followup_message_id,omitempty"`
}
type UnitSummaryResolution struct {
	Subject                string   `json:"subject"`
	Intent                 string   `json:"intent"`
	UnitID                 string   `json:"unit_id,omitempty"`
	UnitTitle              string   `json:"unit_title,omitempty"`
	MaterialDocumentIDs    []string `json:"material_document_ids,omitempty"`
	Format                 string   `json:"format"`
	Requirements           string   `json:"requirements,omitempty"`
	MissingFields          []string `json:"missing_fields,omitempty"`
	Clarification          string   `json:"clarification,omitempty"`
	UseActiveRevision      bool     `json:"use_active_revision,omitempty"`
	RenderOnly             bool     `json:"render_only,omitempty"`
	EvidenceRefs           []string `json:"evidence_refs,omitempty"`
	ProvidedMaterialText   string   `json:"provided_material_text,omitempty"`
	SessionMaterialIndexes []int    `json:"session_material_indexes,omitempty"`
}
type UnitSummaryResolveInput struct {
	Request              UnitSummaryRequest                       `json:"request"`
	Profile              k12.WeeklyProfile                        `json:"profile"`
	MathCatalog          *k12.CurriculumCatalog                   `json:"math_catalog,omitempty"`
	MathProgress         *k12.CurriculumProgress                  `json:"math_progress,omitempty"`
	ActiveRevision       *k12.UnitSummaryRevision                 `json:"active_revision,omitempty"`
	Materials            []k12storage.UnitSummaryMaterial         `json:"materials"`
	Evidence             []k12.UnitSummaryFrozenSource            `json:"evidence"`
	SessionDocuments     []k12storage.UnitSummarySessionDocument  `json:"session_documents,omitempty"`
	ImageAttachmentCount int                                      `json:"image_attachment_count,omitempty"`
	ProvidedMaterials    []k12storage.UnitSummaryProvidedMaterial `json:"provided_materials,omitempty"`
}
type UnitSummaryGenerationInput struct {
	Context         k12.UnitSummaryContext      `json:"context"`
	FinalUserText   string                      `json:"final_user_text"`
	Requirements    k12.UnitSummaryRequirements `json:"requirements"`
	BaseRevisionID  string                      `json:"base_revision_id,omitempty"`
	PreviousContent *k12.UnitSummaryContentV1   `json:"previous_content,omitempty"`
}
type UnitSummaryGenerator interface {
	ResolveUnitSummary(context.Context, UnitSummaryResolveInput) (UnitSummaryResolution, error)
	GenerateUnitSummary(context.Context, UnitSummaryGenerationInput) (k12.UnitSummaryContentV1, error)
}
type UnitSummaryRouteResolver func(context.Context, k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error)
type UnitSummaryArtifact struct {
	ArtifactID  string `json:"artifact_id"`
	SourceKind  string `json:"source_kind"`
	ContentType string `json:"content_type"`
	ByteDigest  string `json:"byte_digest"`
	ByteSize    int64  `json:"byte_size"`
}
type UnitSummaryMaterialView struct {
	k12.UnitSummaryRevision
	Subject         string              `json:"subject"`
	Title           string              `json:"title"`
	Artifact        UnitSummaryArtifact `json:"artifact"`
	GradeTerm       string              `json:"grade_term,omitempty"`
	TextbookEdition string              `json:"textbook_edition,omitempty"`
	UnitNumber      string              `json:"unit_number,omitempty"`
	UnitTitle       string              `json:"unit_title,omitempty"`
}
type UnitSummaryView struct {
	Job      k12.UnitSummaryAttempt   `json:"job"`
	Document *k12.UnitSummaryDocument `json:"document,omitempty"`
	Material *UnitSummaryMaterialView `json:"material,omitempty"`
	Delivery struct {
		Complete bool `json:"complete"`
	} `json:"delivery"`
	Replayed bool `json:"replayed"`
}
type UnitSummaryDocumentsView struct {
	Documents  []UnitSummaryView        `json:"documents"`
	Jobs       []k12.UnitSummaryAttempt `json:"jobs"`
	NextCursor string                   `json:"next_cursor"`
}

// Coordinator只管理进程生命周期，SQLite lease与checkpoint拥有任务状态。
type UnitSummaryCoordinator struct {
	Deps         Deps
	Generator    UnitSummaryGenerator
	ResolveRoute UnitSummaryRouteResolver
	mu           sync.Mutex
	base         context.Context
	active       map[string]bool
	workerID     string
}

func NewUnitSummaryCoordinator(deps Deps, generator UnitSummaryGenerator, route UnitSummaryRouteResolver) *UnitSummaryCoordinator {
	return &UnitSummaryCoordinator{Deps: deps, Generator: generator, ResolveRoute: route, active: map[string]bool{}, workerID: "unitworker-" + idgen.ShortID()}
}
func (c *UnitSummaryCoordinator) SetBaseContext(ctx context.Context) {
	c.mu.Lock()
	c.base = ctx
	c.mu.Unlock()
}

// PendingInput只返回当前会话唯一待补问任务；多任务歧义由已有会话交互消解。
func (c *UnitSummaryCoordinator) PendingInput(ctx context.Context, owner, agent, session string) (*k12.UnitSummaryAttempt, error) {
	items, err := c.Deps.Records.ListUnitSummaryAttempts(ctx, owner, agent, session, true)
	if err != nil {
		return nil, err
	}
	var pending *k12.UnitSummaryAttempt
	for _, a := range items {
		if a.State != "needs_input" {
			continue
		}
		if pending != nil {
			return nil, nil
		}
		copy := a
		pending = &copy
	}
	return pending, nil
}
func (c *UnitSummaryCoordinator) ResumeAndWait(ctx context.Context, owner, agent, id string, req UnitSummaryResumeRequest) (UnitSummaryView, bool, error) {
	v, replayed, err := c.Resume(ctx, owner, agent, id, req)
	if err != nil {
		return v, replayed, err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for !k12.UnitSummaryAttemptTerminal(v.Job.State) {
		select {
		case <-ctx.Done():
			return v, replayed, nil
		case <-ticker.C:
			v, err = c.Get(ctx, owner, agent, id)
			if err != nil {
				return v, replayed, err
			}
		}
	}
	v.Replayed = replayed
	return v, replayed, nil
}
func (c *UnitSummaryCoordinator) validate() error {
	if c == nil || c.Deps.Records == nil || c.Generator == nil || c.ResolveRoute == nil {
		return fmt.Errorf("unit summary dependencies are unavailable")
	}
	return nil
}

func (c *UnitSummaryCoordinator) Start(ctx context.Context, req UnitSummaryRequest) (UnitSummaryView, bool, error) {
	if err := c.validate(); err != nil {
		return UnitSummaryView{}, false, err
	}
	req.OwnerID = strings.TrimSpace(req.OwnerID)
	req.AgentName = strings.TrimSpace(req.AgentName)
	if req.OwnerID == "" || req.AgentName == "" || req.SessionID == "" || req.SourceMessageID == "" || req.IdempotencyKey == "" || strings.TrimSpace(req.FinalUserText) == "" {
		return UnitSummaryView{}, false, fmt.Errorf("%w: unit summary trusted identity, session, message and final text are required", ErrInvalidInput)
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return UnitSummaryView{}, false, err
	}
	if existing, e := c.Deps.Records.GetUnitSummaryAttemptByKey(ctx, req.OwnerID, req.AgentName, req.IdempotencyKey); e == nil {
		if existing.RequestDigest != k12.UnitSummaryDigest(req) {
			return UnitSummaryView{}, false, k12storage.ErrUnitSummaryConflict
		}
		c.schedule(existing)
		v, e := c.Get(ctx, req.OwnerID, req.AgentName, existing.AttemptID)
		v.Replayed = true
		return v, true, e
	} else if !errors.Is(e, records.ErrNotFound) {
		return UnitSummaryView{}, false, e
	}
	// 路由与孩子称呼在接收时冻结，重放仍读取原尝试的快照。
	route, err := c.ResolveRoute(ctx, req.ModelSnapshot)
	if err != nil {
		return UnitSummaryView{}, false, err
	}
	profile, err := c.Deps.Records.GetProfileState(ctx, req.AgentName)
	if err != nil {
		return UnitSummaryView{}, false, err
	}
	a, replayed, err := c.Deps.Records.CreateUnitSummaryAttempt(ctx, k12.UnitSummaryAttempt{OwnerID: req.OwnerID, AgentName: req.AgentName, SessionID: req.SessionID, SourceMessageID: req.SourceMessageID, IdempotencyKey: req.IdempotencyKey, RequestDigest: k12.UnitSummaryDigest(req), FinalUserText: req.FinalUserText, RequestJSON: string(raw), Context: k12.UnitSummaryContext{ProfileSnapshot: profile, ChildName: profile.ChildName, GradeTerm: profile.GradeTerm, ModelSnapshot: route}})
	if err != nil {
		return UnitSummaryView{}, false, err
	}
	c.schedule(a)
	v, err := c.Get(ctx, a.OwnerID, a.AgentName, a.AttemptID)
	v.Replayed = replayed
	return v, replayed, err
}
func (c *UnitSummaryCoordinator) StartAndWait(ctx context.Context, req UnitSummaryRequest) (UnitSummaryView, bool, error) {
	v, replayed, err := c.Start(ctx, req)
	if err != nil {
		return v, replayed, err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for !k12.UnitSummaryAttemptTerminal(v.Job.State) {
		select {
		case <-ctx.Done():
			return v, replayed, nil
		case <-ticker.C:
			v, err = c.Get(ctx, req.OwnerID, req.AgentName, v.Job.AttemptID)
			if err != nil {
				return v, replayed, err
			}
		}
	}
	v.Replayed = replayed
	return v, replayed, nil
}
func unitMaterialView(r k12.UnitSummaryRevision) *UnitSummaryMaterialView {
	return &UnitSummaryMaterialView{UnitSummaryRevision: r, Subject: r.Content.Subject, Title: r.Content.Title, Artifact: UnitSummaryArtifact{r.ArtifactID, k12.PrintSourceUnitSummary, "application/pdf", r.ByteDigest, r.ByteSize}, GradeTerm: r.Context.GradeTerm, TextbookEdition: r.Context.TextbookEdition, UnitNumber: r.Context.UnitNumber, UnitTitle: r.Context.UnitTitle}
}
func (c *UnitSummaryCoordinator) Get(ctx context.Context, owner, agent, id string) (UnitSummaryView, error) {
	a, err := c.Deps.Records.GetUnitSummaryAttempt(ctx, owner, agent, id)
	if err != nil {
		return UnitSummaryView{}, err
	}
	v := UnitSummaryView{Job: a}
	if a.DocumentID != "" {
		d, e := c.Deps.Records.GetUnitSummaryDocument(ctx, owner, agent, a.DocumentID)
		if e != nil {
			return v, e
		}
		v.Document = &d
	}
	if a.ResultRevisionID != "" {
		r, e := c.Deps.Records.GetUnitSummaryRevision(ctx, owner, agent, a.DocumentID, a.ResultRevisionID)
		if e != nil {
			return v, e
		}
		v.Material = unitMaterialView(r)
		v.Delivery.Complete = true
		v.Job.Content = nil
	}
	return v, nil
}
func (c *UnitSummaryCoordinator) GetDocument(ctx context.Context, owner, agent, document, id string) (UnitSummaryView, error) {
	d, err := c.Deps.Records.GetUnitSummaryDocument(ctx, owner, agent, document)
	if err != nil {
		return UnitSummaryView{}, err
	}
	if id == "" {
		id = d.CurrentRevisionID
	}
	if id == "" {
		return UnitSummaryView{Document: &d}, nil
	}
	r, err := c.Deps.Records.GetUnitSummaryRevision(ctx, owner, agent, document, id)
	if err != nil {
		return UnitSummaryView{}, err
	}
	a, err := c.Deps.Records.GetUnitSummaryAttempt(ctx, owner, agent, r.AttemptID)
	if err != nil {
		return UnitSummaryView{}, err
	}
	v := UnitSummaryView{Job: a, Document: &d, Material: unitMaterialView(r)}
	v.Delivery.Complete = true
	v.Job.Content = nil
	return v, nil
}
func (c *UnitSummaryCoordinator) ListDocuments(ctx context.Context, owner, agent, session, cursor string, limit int) (UnitSummaryDocumentsView, error) {
	docs, next, err := c.Deps.Records.ListUnitSummaryDocuments(ctx, owner, agent, session, cursor, limit)
	if err != nil {
		return UnitSummaryDocumentsView{}, err
	}
	v := UnitSummaryDocumentsView{Documents: []UnitSummaryView{}, NextCursor: next}
	for _, d := range docs {
		entry, e := c.GetDocument(ctx, owner, agent, d.DocumentID, d.CurrentRevisionID)
		if e != nil {
			return v, e
		}
		v.Documents = append(v.Documents, entry)
	}
	v.Jobs, err = c.Deps.Records.ListUnitSummaryAttempts(ctx, owner, agent, session, true)
	return v, err
}

func (c *UnitSummaryCoordinator) Resume(ctx context.Context, owner, agent, id string, req UnitSummaryResumeRequest) (UnitSummaryView, bool, error) {
	a, err := c.Deps.Records.GetUnitSummaryAttempt(ctx, owner, agent, id)
	if err != nil {
		return UnitSummaryView{}, false, err
	}
	if req.IdempotencyKey == "" {
		return UnitSummaryView{}, false, fmt.Errorf("%w: resume idempotency key required", ErrInvalidInput)
	}
	next := a
	digest := k12.UnitSummaryDigest(req)
	if replayed, e := c.Deps.Records.ReplayUnitSummaryResume(ctx, owner, agent, id, req.IdempotencyKey, digest); e != nil {
		return UnitSummaryView{}, false, e
	} else if replayed {
		v, e := c.Get(ctx, owner, agent, id)
		v.Replayed = true
		return v, true, e
	}
	if a.State == "needs_input" {
		if req.FollowupMessageID == "" {
			// 技术误判所致补问只复用可重新验证的既有成功解析，不要求家长重发已给出的信息。
			recovered, eligible, e := c.clarificationRecoveryResult(ctx, a)
			if e != nil {
				return UnitSummaryView{}, false, e
			}
			if !eligible {
				return UnitSummaryView{}, false, fmt.Errorf("%w: followup message required", ErrInvalidInput)
			}
			next = recovered
		} else {
			text, e := c.Deps.Records.ReadUnitSummaryFollowup(ctx, a.SessionID, req.FollowupMessageID)
			if e != nil {
				return UnitSummaryView{}, false, e
			}
			var original UnitSummaryRequest
			if e = json.Unmarshal([]byte(a.RequestJSON), &original); e != nil {
				return UnitSummaryView{}, false, e
			}
			original.FinalUserText = a.FinalUserText + "\n\n" + text
			raw, _ := json.Marshal(original)
			next.RequestJSON = string(raw)
			next.FinalUserText = original.FinalUserText
			next.ResolutionJSON = ""
			next.ResolveInputJSON = ""
			next.InvocationRef = ""
			next.State = "resolving"
			next.Stage = "resolve"
			next.Clarification = ""
		}
	} else if a.State == "failed" {
		if a.FailureKind == "content_invalid" {
			if a.Content == nil {
				return UnitSummaryView{}, false, fmt.Errorf("%w: invalid content requires an explicit corrected request", ErrInvalidInput)
			}
			if next.GeneratedContentJSON == "" {
				original, e := json.Marshal(a.Content)
				if e != nil {
					return UnitSummaryView{}, false, e
				}
				next.GeneratedContentJSON = string(original)
			}
			normalized, e := c.normalizeContentReferences(ctx, a.OwnerID, *a.Content, a.Context)
			if e != nil || k12.ValidateUnitSummaryContent(normalized, a.Context) != nil {
				return UnitSummaryView{}, false, fmt.Errorf("%w: invalid content requires an explicit corrected request", ErrInvalidInput)
			}
			next.Content = &normalized
			next.ContentDigest = unitSummaryContentDigest(normalized, a.Context, a.Requirements)
			next.Stage = "render"
		}
		next.State = map[string]string{"resolve": "resolving", "generate": "generating", "render": "rendering", "publish": "publishing"}[next.Stage]
		if next.State == "" {
			return UnitSummaryView{}, false, k12storage.ErrUnitSummaryCAS
		}
	} else if a.State == "reconciling" {
		next.State = "reconciling"
	} else {
		return UnitSummaryView{}, false, k12storage.ErrUnitSummaryCAS
	}
	// 回执重放在Store先判定，已应用命令不因客户端旧revision再次推进。
	if a.Revision != req.ExpectedRevision {
		existing, e := c.Deps.Records.ReplayUnitSummaryResume(ctx, owner, agent, id, req.IdempotencyKey, digest)
		if e != nil {
			return UnitSummaryView{}, false, e
		}
		if !existing {
			return UnitSummaryView{}, false, k12storage.ErrUnitSummaryCAS
		}
		v, e := c.Get(ctx, owner, agent, id)
		return v, true, e
	}
	a, replayed, err := c.Deps.Records.ResumeUnitSummaryAttempt(ctx, a, next, req.IdempotencyKey, digest)
	if err != nil {
		return UnitSummaryView{}, false, err
	}
	c.schedule(a)
	v, err := c.Get(ctx, owner, agent, id)
	v.Replayed = replayed
	return v, replayed, err
}

func (c *UnitSummaryCoordinator) clarificationRecoveryResult(ctx context.Context, a k12.UnitSummaryAttempt) (k12.UnitSummaryAttempt, bool, error) {
	if a.Stage != "resolve" || a.Content != nil || a.GeneratedContentJSON != "" || a.Candidate != nil || a.ResolveInputJSON == "" || a.InvocationRef == "" {
		return a, false, nil
	}
	var input UnitSummaryResolveInput
	if err := json.Unmarshal([]byte(a.ResolveInputJSON), &input); err != nil {
		return a, false, err
	}
	invocations, err := c.Deps.Records.ListUnitSummaryModelInvocations(ctx, a.AgentName, a.AttemptID)
	if err != nil {
		return a, false, err
	}
	for _, invocation := range invocations {
		if invocation.InvocationID != a.InvocationRef || invocation.Stage != "unit_summary_resolve" || invocation.RequestDigest != k12.UnitSummaryDigest(input) || (invocation.Status != k12.ModelInvocationSucceeded && invocation.Status != k12.ModelInvocationReconciled) {
			continue
		}
		var resolution UnitSummaryResolution
		if err = json.Unmarshal([]byte(invocation.ResultJSON), &resolution); err != nil {
			return a, false, err
		}
		if len(resolution.MissingFields) != 0 || !k12.UnitSummarySubjectAllowed(resolution.Subject) {
			return a, false, nil
		}
		next := a
		if resolution.Intent == "open" || resolution.Intent == "follow_up" {
			if input.Request.ActiveRevisionID == "" || input.ActiveRevision == nil || input.ActiveRevision.RevisionID != input.Request.ActiveRevisionID || len(input.ProvidedMaterials) != 0 || len(input.SessionDocuments) != 0 || input.ImageAttachmentCount != 0 {
				return a, false, nil
			}
			active, e := c.Deps.Records.FindUnitSummaryRevision(ctx, a.OwnerID, a.AgentName, input.Request.ActiveRevisionID)
			if e != nil {
				return a, false, e
			}
			resolution = unitSummaryActiveResolution(&active, a.FinalUserText, resolution)
			if !resolution.UseActiveRevision || active.Content.Subject != resolution.Subject || !unitSummarySelectsActiveSources(active.Context, resolution) || (resolution.UnitTitle != "" && resolution.UnitTitle != active.Context.UnitTitle && resolution.UnitTitle != active.Content.Title) {
				return a, false, nil
			}
			next.DocumentID = active.DocumentID
			next.ResultRevisionID = active.RevisionID
			next.ArtifactID = active.ArtifactID
			next.Intent = resolution.Intent
			next.State = "reused"
			next.Stage = "complete"
		} else {
			if a.ResolutionJSON == "" || (resolution.Intent != "generate" && resolution.Intent != "revise") {
				return a, false, nil
			}
			frozen, e := c.freezeContext(ctx, a, input, resolution)
			if e != nil || len(frozen.Sources) == 0 || strings.TrimSpace(frozen.UnitTitle) == "" {
				return a, false, nil
			}
			next.Context = frozen
			if resolution.Intent == "revise" && input.ActiveRevision != nil && unitSummaryScopeKey(frozen) == unitSummaryScopeKey(input.ActiveRevision.Context) {
				next.Context.BaseRevisionID = input.ActiveRevision.RevisionID
			}
			next.Intent = resolution.Intent
			next.Requirements = k12.UnitSummaryRequirements{Format: resolution.Format, Instructions: resolution.Requirements}
			if next.Requirements.Format != "brief" {
				next.Requirements.Format = "standard"
			}
			next.State = "queued"
			next.Stage = "generate"
		}
		next.Clarification = ""
		return next, true, nil
	}
	return a, false, nil
}

func (c *UnitSummaryCoordinator) schedule(a k12.UnitSummaryAttempt) {
	if k12.UnitSummaryAttemptTerminal(a.State) {
		return
	}
	key := a.AgentName + "\x00" + a.AttemptID
	c.mu.Lock()
	if c.active[key] {
		c.mu.Unlock()
		return
	}
	c.active[key] = true
	base := c.base
	if base == nil {
		base = context.Background()
	}
	c.mu.Unlock()
	go func() {
		defer func() { c.mu.Lock(); delete(c.active, key); c.mu.Unlock() }()
		if err := c.process(base, a); err != nil && !errors.Is(err, k12storage.ErrUnitSummaryCAS) {
			slog.Warn("K12 unit summary checkpoint retained", "attempt_id", a.AttemptID, "error", err)
		}
	}()
}

// Run恢复已持久阶段，关闭页面与观察连接不取消领域任务。
func (c *UnitSummaryCoordinator) Run(ctx context.Context) {
	defer func() {
		if err := c.Deps.Records.ReleaseUnitSummaryWorkerLeases(context.Background(), c.workerID); err != nil {
			slog.Warn("K12 unit summary worker lease release failed", "error", err)
		}
	}()
	c.mu.Lock()
	c.base = ctx
	c.mu.Unlock()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		jobs, err := c.Deps.Records.ListRecoverableUnitSummaryAttempts(ctx)
		if err == nil {
			for _, a := range jobs {
				c.schedule(a)
			}
		}
		if err = c.Deps.Records.FlushUnitSummaryMessageProjections(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("K12 unit summary message projection pending", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *UnitSummaryCoordinator) fail(a k12.UnitSummaryAttempt, kind string, err error) (k12.UnitSummaryAttempt, error) {
	next := a
	next.State = "failed"
	next.FailureKind = kind
	next.FailureDetail = upstreamerr.KnowledgeFailureMessage(err.Error())
	next.LeaseOwner = ""
	next.LeaseExpiresAt = 0
	return c.Deps.Records.CheckpointUnitSummaryAttempt(context.Background(), a, next)
}
func (c *UnitSummaryCoordinator) checkpoint(ctx context.Context, a, next k12.UnitSummaryAttempt) (k12.UnitSummaryAttempt, error) {
	next.LeaseExpiresAt = c.Deps.now() + 900
	return c.Deps.Records.CheckpointUnitSummaryAttempt(ctx, a, next)
}

// 两种模型调用共用既有物理调用账本。unknown只读原invocation，不发送第二次请求。
func (c *UnitSummaryCoordinator) invoke(ctx context.Context, a k12.UnitSummaryAttempt, stage string, input any, call func(context.Context) (any, error)) (any, k12.UnitSummaryAttempt, error) {
	digest := k12.UnitSummaryDigest(input)
	all, err := c.Deps.Records.ListUnitSummaryModelInvocations(ctx, a.AgentName, a.AttemptID)
	if err != nil {
		return nil, a, err
	}
	attempt := 1
	for _, i := range all {
		if i.Stage == stage && i.Attempt >= attempt {
			attempt = i.Attempt
			if i.Status == k12.ModelInvocationFailed || ((i.Status == k12.ModelInvocationSucceeded || i.Status == k12.ModelInvocationReconciled) && i.RequestDigest != digest) {
				attempt++
			}
		}
	}
	inv, _, err := c.Deps.Records.PrepareUnitSummaryModelInvocation(ctx, k12.ModelInvocation{InvocationID: "modelinv-" + idgen.ShortID(), AgentName: a.AgentName, JobID: a.AttemptID, Stage: stage, RequestDigest: digest, RouteSnapshot: a.Context.ModelSnapshot, Attempt: attempt, ProviderIdempotencyKey: fmt.Sprintf("k12-unit:%s:%s:%d", a.AttemptID, stage, attempt)})
	if err != nil {
		return nil, a, err
	}
	next := a
	next.InvocationRef = inv.InvocationID
	next.Stage = map[string]string{"unit_summary_resolve": "resolve", "unit_summary_generate": "generate"}[stage]
	a, err = c.checkpoint(ctx, a, next)
	if err != nil {
		return nil, a, err
	}
	if inv.Status == k12.ModelInvocationSucceeded || inv.Status == k12.ModelInvocationReconciled {
		if inv.ResultJSON == "" {
			return nil, a, fmt.Errorf("%w: unit summary invocation result missing", ErrModelInvocationRequiresReconciliation)
		}
		var result any
		err = json.Unmarshal([]byte(inv.ResultJSON), &result)
		return result, a, err
	}
	if inv.Status != k12.ModelInvocationPrepared {
		return nil, a, fmt.Errorf("%w: %s", ErrModelInvocationRequiresReconciliation, inv.InvocationID)
	}
	if _, err = c.Deps.Records.MarkUnitSummaryModelInvocationSent(ctx, a.AgentName, inv.InvocationID, inv.ProviderIdempotencyKey); err != nil {
		return nil, a, err
	}
	callCtx := k12.WithGradingModelSnapshot(ctx, a.Context.ModelSnapshot)
	callCtx = egress.WithProviderClientRequestKey(callCtx, inv.ProviderIdempotencyKey)
	result, err := call(callCtx)
	if err != nil {
		if !sentProviderOutcomeUnknown(err, nil) {
			_, ledgerErr := c.Deps.Records.MarkUnitSummaryModelInvocationFailed(context.Background(), a.AgentName, inv.InvocationID, "provider_response")
			return nil, a, errors.Join(err, ledgerErr)
		}
		_, ledgerErr := c.Deps.Records.MarkUnitSummaryModelInvocationOutcomeUnknown(context.Background(), a.AgentName, inv.InvocationID, "provider_outcome_unknown")
		return nil, a, errors.Join(ErrModelInvocationRequiresReconciliation, err, ledgerErr)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, a, err
	}
	if _, err = c.Deps.Records.MarkUnitSummaryModelInvocationSucceededWithResult(context.Background(), a.AgentName, inv.InvocationID, modelInvocationResultDigest(result), string(raw), ""); err != nil {
		return nil, a, errors.Join(ErrModelInvocationRequiresReconciliation, err)
	}
	return result, a, nil
}

func (c *UnitSummaryCoordinator) process(ctx context.Context, requested k12.UnitSummaryAttempt) error {
	a, claimed, err := c.Deps.Records.ClaimUnitSummaryAttempt(ctx, requested.OwnerID, requested.AgentName, requested.AttemptID, c.workerID, c.Deps.now()+900)
	if err != nil || !claimed {
		return err
	}
	defer func() { _ = c.Deps.Records.FlushUnitSummaryMessageProjections(context.Background()) }()
	if a.Content == nil {
		if a.ResolutionJSON == "" {
			input, e := c.resolveInput(ctx, a)
			if e != nil {
				_, err = c.fail(a, "context_failed", e)
				return err
			}
			if a.ResolveInputJSON == "" {
				raw, _ := json.Marshal(input)
				next := a
				next.ResolveInputJSON = string(raw)
				a, err = c.checkpoint(ctx, a, next)
				if err != nil {
					return err
				}
			}
			result, updated, e := c.invoke(ctx, a, "unit_summary_resolve", input, func(modelctx context.Context) (any, error) { return c.Generator.ResolveUnitSummary(modelctx, input) })
			a = updated
			if e != nil {
				return c.handleInvocationFailure(a, e)
			}
			raw, _ := json.Marshal(result)
			var resolution UnitSummaryResolution
			if e = json.Unmarshal(raw, &resolution); e != nil {
				_, err = c.fail(a, "context_failed", e)
				return err
			}
			validIntent := false
			switch resolution.Intent {
			case "generate", "revise", "reuse", "open", "follow_up", "none":
				validIntent = true
			}
			if !validIntent {
				_, err = c.fail(a, "context_invalid", fmt.Errorf("unit summary resolution has an invalid intent"))
				return err
			}
			next := a
			next.ResolutionJSON = string(raw)
			// 模板仅是候选；最终正文不是资料任务时只保留解析审计，普通会话继续且不产生资料引用。
			if resolution.Intent == "none" {
				next.Intent = "none"
				next.State = "ignored"
				next.Stage = "complete"
				next.Clarification = ""
				next.LeaseOwner = ""
				next.LeaseExpiresAt = 0
				_, err = c.Deps.Records.CheckpointUnitSummaryAttempt(ctx, a, next)
				return err
			}
			if len(resolution.MissingFields) > 0 || !k12.UnitSummarySubjectAllowed(resolution.Subject) {
				if len(input.ProvidedMaterials) == 0 && (len(input.SessionDocuments) > 0 || input.ImageAttachmentCount > 0) {
					_, err = c.fail(a, "source_reader_unavailable", fmt.Errorf("the current session attachment has no readable material text or verified source reference"))
					return err
				}
				next.State = "needs_input"
				next.Clarification = resolution.Clarification
				if next.Clarification == "" {
					next.Clarification = "Please specify the subject and unit or provide the learning material."
				}
				next.LeaseOwner = ""
				next.LeaseExpiresAt = 0
				_, err = c.Deps.Records.CheckpointUnitSummaryAttempt(ctx, a, next)
				return err
			}
			activeResolution := unitSummaryActiveResolution(input.ActiveRevision, a.FinalUserText, resolution)
			if resolution.RenderOnly && resolution.Intent != "revise" {
				_, err = c.fail(a, "context_invalid", fmt.Errorf("layout-only rendering requires a revision of the opened brief material"))
				return err
			}
			// 同一已打开资料的明确复用保留旧要求与冻结文件，不把模型遗漏旧说明误当新生成要求。
			reuseActive := activeResolution.Intent == "reuse" && activeResolution.UseActiveRevision && input.ActiveRevision != nil &&
				input.ActiveRevision.Content.Subject == activeResolution.Subject && unitSummarySelectsActiveSources(input.ActiveRevision.Context, activeResolution) &&
				(activeResolution.UnitTitle == "" || activeResolution.UnitTitle == input.ActiveRevision.Context.UnitTitle || activeResolution.UnitTitle == input.ActiveRevision.Content.Title) &&
				len(input.ProvidedMaterials) == 0 && len(input.SessionDocuments) == 0 && input.ImageAttachmentCount == 0 &&
				(activeResolution.ProvidedMaterialText == "" || !strings.Contains(a.FinalUserText, activeResolution.ProvidedMaterialText))
			if resolution.Intent == "open" || resolution.Intent == "follow_up" || reuseActive {
				return c.finishReadOnly(ctx, a, input.ActiveRevision, resolution)
			}
			next.Intent = resolution.Intent
			next.Requirements = k12.UnitSummaryRequirements{Format: resolution.Format, Instructions: resolution.Requirements}
			if next.Requirements.Format != "brief" {
				next.Requirements.Format = "standard"
			}
			frozen, e := c.freezeContext(ctx, a, input, resolution)
			if e != nil {
				if errors.Is(e, ErrInvalidInput) {
					next.State = "needs_input"
					next.Clarification = e.Error()
					next.LeaseOwner = ""
					next.LeaseExpiresAt = 0
					_, err = c.Deps.Records.CheckpointUnitSummaryAttempt(ctx, a, next)
					return err
				}
				_, err = c.fail(a, "source_failed", e)
				return err
			}
			next.Context = frozen
			if resolution.RenderOnly {
				base, e := c.renderOnlyUnitSummaryBase(ctx, a, input, resolution, frozen)
				if e != nil {
					_, err = c.fail(a, "context_invalid", e)
					return err
				}
				next.Context = base.Context
				next.Context.BaseRevisionID = base.RevisionID
				next.Requirements = base.Requirements
			} else if resolution.Intent == "revise" && input.ActiveRevision != nil && unitSummaryScopeKey(frozen) == unitSummaryScopeKey(input.ActiveRevision.Context) {
				next.Context.BaseRevisionID = input.ActiveRevision.RevisionID
			}
			a, err = c.checkpoint(ctx, a, next)
			if err != nil {
				return err
			}
		}
		if a.DocumentID == "" {
			scope := unitSummaryScopeKey(a.Context)
			a, _, err = c.Deps.Records.BindUnitSummaryDocument(ctx, a, scope)
			if err != nil {
				return err
			}
		}
		var resolution UnitSummaryResolution
		if err = json.Unmarshal([]byte(a.ResolutionJSON), &resolution); err != nil {
			return err
		}
		if resolution.RenderOnly {
			input, e := c.resolveInput(ctx, a)
			if e != nil {
				_, err = c.fail(a, "context_invalid", e)
				return err
			}
			base, e := c.renderOnlyUnitSummaryBase(ctx, a, input, resolution, a.Context)
			if e != nil {
				_, err = c.fail(a, "context_invalid", e)
				return err
			}
			// 纯版式更新复制已冻结正文，不伪造新生成调用或改写既有规范内容。
			next := a
			next.Context = base.Context
			next.Context.BaseRevisionID = base.RevisionID
			next.Requirements = base.Requirements
			next.Content = &base.Content
			next.ContentDigest = unitSummaryContentDigest(base.Content, next.Context, next.Requirements)
			next.State = "rendering"
			next.Stage = "render"
			a, err = c.checkpoint(ctx, a, next)
			if err != nil {
				return err
			}
			return c.publish(ctx, a)
		}
		// 相同来源、个人证据和实际要求先复用，纯Prompt措辞不触发新生成。
		doc, e := c.Deps.Records.GetUnitSummaryDocument(ctx, a.OwnerID, a.AgentName, a.DocumentID)
		if e != nil {
			return e
		}
		if doc.CurrentRevisionID != "" && a.Intent != "revise" {
			r, e := c.Deps.Records.GetUnitSummaryRevision(ctx, a.OwnerID, a.AgentName, a.DocumentID, doc.CurrentRevisionID)
			if e != nil {
				return e
			}
			if unitSummaryInputDigest(a.Context, a.Requirements) == unitSummaryInputDigest(r.Context, r.Requirements) {
				next := a
				next.Content = &r.Content
				if a.Requirements.Format == "brief" {
					// 相同正文在新brief合同下重排时仍使用旧版真实来源引用，避免新的消息定位改变正文事实。
					next.Context = r.Context
					next.Context.BaseRevisionID = r.RevisionID
					next.ContentDigest = unitSummaryContentDigest(r.Content, next.Context, next.Requirements)
					next.State = "rendering"
					next.Stage = "render"
					a, err = c.checkpoint(ctx, a, next)
					if err != nil {
						return err
					}
					return c.publish(ctx, a)
				}
				next.ContentDigest = r.ContentDigest
				next.State = "publishing"
				next.Stage = "publish"
				a, err = c.checkpoint(ctx, a, next)
				if err != nil {
					return err
				}
				_, _, err = c.Deps.Records.PublishUnitSummary(ctx, a, nil)
				return err
			}
		}
		next := a
		next.State = "generating"
		next.Stage = "generate"
		a, err = c.checkpoint(ctx, a, next)
		if err != nil {
			return err
		}
		input := UnitSummaryGenerationInput{Context: a.Context, FinalUserText: a.FinalUserText, Requirements: a.Requirements}
		if a.Intent == "revise" && a.Context.BaseRevisionID != "" {
			base, e := c.Deps.Records.GetUnitSummaryRevision(ctx, a.OwnerID, a.AgentName, a.DocumentID, a.Context.BaseRevisionID)
			if e != nil {
				_, err = c.fail(a, "source_failed", e)
				return err
			}
			input.BaseRevisionID = base.RevisionID
			input.PreviousContent = &base.Content
		}
		result, updated, e := c.invoke(ctx, a, "unit_summary_generate", input, func(modelctx context.Context) (any, error) { return c.Generator.GenerateUnitSummary(modelctx, input) })
		a = updated
		if e != nil {
			return c.handleInvocationFailure(a, e)
		}
		raw, _ := json.Marshal(result)
		var content k12.UnitSummaryContentV1
		if e = json.Unmarshal(raw, &content); e != nil {
			_, err = c.fail(a, "content_invalid", e)
			return err
		}
		// 原始生成结果仅写私有checkpoint；规范内容的确定性投影不覆盖调用诊断。
		a.GeneratedContentJSON = string(raw)
		// 来源显示值由冻结事实提供，模型只负责引用ref，不能补造页码或教材身份。
		content.Sources = append(unitSummaryPublicSources(a.Context.Sources), unitSummaryPublicSources(a.Context.Evidence)...)
		content.Coverage = a.Context.Coverage
		normalized, referenceErr := c.normalizeContentReferences(ctx, a.OwnerID, content, a.Context)
		if referenceErr == nil {
			content = normalized
		}
		if e = k12.ValidateUnitSummaryContent(content, a.Context); e != nil || referenceErr != nil {
			if referenceErr != nil {
				e = referenceErr
			}
			next := a
			next.Content = &content
			a, err = c.checkpoint(ctx, a, next)
			if err != nil {
				return err
			}
			_, err = c.fail(a, "content_invalid", e)
			return err
		}
		next = a
		next.Content = &content
		next.ContentDigest = unitSummaryContentDigest(content, a.Context, a.Requirements)
		next.State = "rendering"
		next.Stage = "render"
		a, err = c.checkpoint(ctx, a, next)
		if err != nil {
			return err
		}
	}
	if a.FailureKind == "content_invalid" {
		return fmt.Errorf("unit summary content requires a corrected generation")
	}
	return c.publish(ctx, a)
}
func (c *UnitSummaryCoordinator) handleInvocationFailure(a k12.UnitSummaryAttempt, e error) error {
	if errors.Is(e, ErrModelInvocationRequiresReconciliation) {
		next := a
		next.State = "reconciling"
		next.FailureKind = "invocation_outcome_unknown"
		// 对账只读得到的“原调用仍未知”不能覆盖首次出口诊断原因。
		if a.State != "reconciling" || a.FailureDetail == "" {
			next.FailureDetail = upstreamerr.KnowledgeFailureMessage(e.Error())
		}
		next.LeaseOwner = ""
		next.LeaseExpiresAt = c.Deps.now() + 30
		_, err := c.Deps.Records.CheckpointUnitSummaryAttempt(context.Background(), a, next)
		return err
	}
	_, err := c.fail(a, "generation_failed", e)
	return err
}
