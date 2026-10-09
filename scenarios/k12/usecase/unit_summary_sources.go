package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func (c *UnitSummaryCoordinator) resolveInput(ctx context.Context, a k12.UnitSummaryAttempt) (UnitSummaryResolveInput, error) {
	var input UnitSummaryResolveInput
	if a.ResolveInputJSON != "" {
		err := json.Unmarshal([]byte(a.ResolveInputJSON), &input)
		if err != nil {
			return input, err
		}
		input.Request.OwnerID = a.OwnerID
		if input.ActiveRevision != nil {
			// 公开解析DTO不序列化私有Context；恢复只补同一不可变版本，不改读current或重解析来源。
			active, e := c.Deps.Records.GetUnitSummaryRevision(ctx, a.OwnerID, a.AgentName, input.ActiveRevision.DocumentID, input.ActiveRevision.RevisionID)
			if e != nil {
				return input, e
			}
			input.ActiveRevision.Context = active.Context
		}
		return input, nil
	}
	if err := json.Unmarshal([]byte(a.RequestJSON), &input.Request); err != nil {
		return input, err
	}
	input.Request.OwnerID = a.OwnerID
	profile := a.Context.ProfileSnapshot
	var err error
	if profile.ChildName == "" {
		profile, err = c.Deps.Records.GetProfileState(ctx, a.AgentName)
		if err != nil {
			return input, err
		}
	}
	profile.ChildName = a.Context.ChildName
	profile.GradeTerm = a.Context.GradeTerm
	input.Profile = profile
	materials, err := c.Deps.Records.ReadUnitSummaryMaterials(ctx, a.OwnerID, a.AgentName)
	if err != nil {
		return input, err
	}
	input.Materials = materials
	input.SessionDocuments, input.ImageAttachmentCount, err = c.Deps.Records.ReadUnitSummarySessionSources(ctx, a.SessionID, a.SourceMessageID)
	if err != nil {
		return input, err
	}
	input.ProvidedMaterials, err = c.Deps.Records.ReadUnitSummaryProvidedMaterials(ctx, a.SessionID, a.SourceMessageID)
	if err != nil {
		return input, err
	}
	input.Evidence, err = c.Deps.Records.ReadUnitSummaryStudentEvidence(ctx, a.AgentName)
	if err != nil {
		return input, err
	}
	if input.Request.ActiveRevisionID != "" {
		r, e := c.Deps.Records.FindUnitSummaryRevision(ctx, a.OwnerID, a.AgentName, input.Request.ActiveRevisionID)
		if e != nil {
			return input, e
		}
		input.ActiveRevision = &r
	}
	catalog, ready, err := c.Deps.Records.GetActiveTextbookCatalogReadOnly(ctx, k12storage.TextbookScope{OwnerID: a.OwnerID, AgentName: a.AgentName, Subject: "math"})
	if err != nil && !errors.Is(err, records.ErrNotFound) {
		return input, err
	}
	if ready && catalogMatchesProfile(catalog, profileForProgress(profile)) {
		input.MathCatalog = &catalog
	}
	progress, _, err := c.Deps.Records.GetCurriculumProgressState(ctx, a.AgentName, "math")
	if err != nil && !errors.Is(err, records.ErrNotFound) {
		return input, err
	}
	if k12.CurriculumProgressUsable(progress) && input.MathCatalog != nil && progress.TextbookManifestID == input.MathCatalog.TextbookManifestID && progressMatchesProfile(progress, profileForProgress(profile), profileForProgress(profile), nil) {
		input.MathProgress = progress
	}
	return input, nil
}

func unitSummarySubjectNormalized(subject string) string {
	switch strings.TrimSpace(subject) {
	case "数学":
		return "math"
	case "语文":
		return "chinese"
	case "英语":
		return "english"
	case "科学":
		return "science"
	case "信息科技", "信息技术":
		return "information_technology"
	case "美术":
		return "art"
	}
	return subject
}

func (c *UnitSummaryCoordinator) freezeContext(ctx context.Context, a k12.UnitSummaryAttempt, input UnitSummaryResolveInput, r UnitSummaryResolution) (k12.UnitSummaryContext, error) {
	r = unitSummaryActiveResolution(input.ActiveRevision, a.FinalUserText, r)
	// 明确编辑同一来源的旧资料时，模型复制或改写的旧AI正文不是用户新提供的教材。
	// 只有无新文件、无新单位/来源且正文不属于真实用户输入时才忽略该字段，实际原文仍精确校验。
	if r.Intent == "revise" && r.UseActiveRevision && input.ActiveRevision != nil &&
		input.ActiveRevision.Content.Subject == r.Subject && unitSummarySelectsActiveSources(input.ActiveRevision.Context, r) &&
		len(input.ProvidedMaterials) == 0 && len(input.SessionDocuments) == 0 && input.ImageAttachmentCount == 0 &&
		r.ProvidedMaterialText != "" && !strings.Contains(a.FinalUserText, r.ProvidedMaterialText) {
		r.ProvidedMaterialText = ""
	}
	// 模型把已选择的KB原文填入会话粘贴字段时，按可核对的真实KB身份归一，不另造来源。
	if r.ProvidedMaterialText != "" && !strings.Contains(a.FinalUserText, r.ProvidedMaterialText) {
		for _, id := range r.MaterialDocumentIDs {
			for _, m := range input.Materials {
				if m.DocumentID == id && strings.Contains(unitSummaryQuoteNormalized(m.Preview), unitSummaryQuoteNormalized(r.ProvidedMaterialText)) {
					r.ProvidedMaterialText = ""
					break
				}
			}
			if r.ProvidedMaterialText == "" {
				break
			}
		}
	}
	frozen := a.Context
	frozen.Subject = r.Subject
	frozen.UnitID = r.UnitID
	frozen.UnitTitle = r.UnitTitle
	frozen.Sources = []k12.UnitSummaryFrozenSource{}
	frozen.Evidence = []k12.UnitSummaryFrozenSource{}
	frozen.Coverage = k12.UnitSummaryCoverage{Level: "partial", Covered: []string{}, Missing: []string{}}
	knownEvidence := map[string]k12.UnitSummaryFrozenSource{}
	for _, source := range input.Evidence {
		knownEvidence[source.Ref] = source
	}
	for _, ref := range r.EvidenceRefs {
		source, ok := knownEvidence[ref]
		if !ok {
			return frozen, fmt.Errorf("selected student work evidence is unavailable")
		}
		frozen.Evidence = append(frozen.Evidence, source)
	}
	// “这份”更新采用显式正在阅读的成功版本，而非创建顺序；旧来源正文在版本私有快照内。
	if input.ActiveRevision != nil && input.ActiveRevision.Content.Subject == r.Subject && unitSummarySelectsActiveSources(input.ActiveRevision.Context, r) && r.ProvidedMaterialText == "" && len(input.ProvidedMaterials) == 0 && (r.UseActiveRevision || ((r.Intent == "revise" || r.Intent == "reuse") && r.UnitID == "" && r.UnitTitle == "")) {
		old := input.ActiveRevision.Context
		old.ModelSnapshot = a.Context.ModelSnapshot
		old.ChildName = a.Context.ChildName
		return old, nil
	}
	if r.Subject == "math" && len(r.MaterialDocumentIDs) == 0 && r.ProvidedMaterialText == "" && len(input.ProvidedMaterials) == 0 && input.MathCatalog != nil {
		catalog := input.MathCatalog
		unitID := r.UnitID
		if unitID == "" && r.UnitTitle == "" && input.MathProgress != nil && input.MathProgress.TextbookManifestID == catalog.TextbookManifestID {
			unitID = input.MathProgress.UnitID
			frozen.ProgressSource = input.MathProgress.EvidenceSource
		}
		var unit *k12.CurriculumCatalogUnit
		for i := range catalog.Units {
			candidate := &catalog.Units[i]
			if (unitID != "" && candidate.UnitID == unitID) || (unitID == "" && r.UnitTitle != "" && candidate.Title == r.UnitTitle) {
				unit = candidate
				// 目录当前没有独立可信序号字段，数组下标不能冒充教材印刷单元号。
				frozen.UnitNumber = ""
				break
			}
		}
		if unit == nil {
			return frozen, fmt.Errorf("%w: please specify a unit available in the current mathematics textbook", ErrInvalidInput)
		}
		scope, ok, err := c.Deps.Records.GetActiveTextbookGroundingScopeReadOnly(ctx, k12storage.TextbookScope{OwnerID: a.OwnerID, AgentName: a.AgentName, Subject: "math"})
		if err != nil {
			return frozen, err
		}
		if !ok || scope.TextbookManifestID != catalog.TextbookManifestID {
			return frozen, fmt.Errorf("verified mathematics textbook source is unavailable")
		}
		pages, err := c.Deps.Records.ReadVerifiedTextbookPages(ctx, k12.VerifiedTextbookReadRequest{OwnerID: a.OwnerID, AgentName: a.AgentName, Subject: "math", Scope: scope})
		if err != nil {
			return frozen, err
		}
		frozen.UnitID = unit.UnitID
		frozen.UnitTitle = unit.Title
		frozen.TextbookEdition = catalog.TextbookEdition
		frozen.Volume = catalog.Volume
		present := map[int]bool{}
		for _, page := range pages {
			if page.LogicalPage < unit.PageFrom || page.LogicalPage > unit.PageTo {
				continue
			}
			present[page.LogicalPage] = true
			ref := fmt.Sprintf("textbook:%s:p%d", scope.TextbookManifestID, page.LogicalPage)
			frozen.Sources = append(frozen.Sources, k12.UnitSummaryFrozenSource{UnitSummarySource: k12.UnitSummarySource{Ref: ref, Origin: "textbook", Label: catalog.Title, Locator: scope.DocumentID, Page: strconv.Itoa(page.LogicalPage), DocumentID: scope.DocumentID, ManifestID: scope.TextbookManifestID, Generation: scope.DocumentGeneration, SourceDigest: scope.SourceDigest, SegmentRefs: page.SegmentRefs}, Text: page.Content})
		}
		if len(frozen.Sources) == 0 {
			return frozen, fmt.Errorf("verified textbook unit has no readable source pages")
		}
		full := unit.PageFrom > 0 && unit.PageTo >= unit.PageFrom
		for p := unit.PageFrom; p <= unit.PageTo; p++ {
			if !present[p] {
				full = false
				frozen.Coverage.Missing = append(frozen.Coverage.Missing, "教材第"+strconv.Itoa(p)+"页")
			}
		}
		if full {
			frozen.Coverage.Level = "full"
		}
		frozen.Coverage.Covered = []string{unit.Title}
		return frozen, nil
	}
	indexes := r.SessionMaterialIndexes
	if len(input.ProvidedMaterials) == 0 {
		indexes = nil
	}
	if len(indexes) == 0 {
		for i := range input.ProvidedMaterials {
			indexes = append(indexes, i)
		}
	}
	selected := map[int]bool{}
	for _, i := range indexes {
		if i < 0 || i >= len(input.ProvidedMaterials) {
			return frozen, fmt.Errorf("selected session material is unavailable")
		}
		if selected[i] {
			continue
		}
		selected[i] = true
		m := input.ProvidedMaterials[i]
		if strings.TrimSpace(m.Text) == "" {
			return frozen, fmt.Errorf("session attachment parsed text is unavailable")
		}
		digest := k12.UnitSummaryDigest(m.Text)
		label := m.Name
		if label == "" {
			label = "本次附件中的学习材料"
		}
		frozen.Sources = append(frozen.Sources, k12.UnitSummaryFrozenSource{UnitSummarySource: k12.UnitSummarySource{Kind: "session_attachment", Ref: fmt.Sprintf("session-material:%s:%d:%s", a.SourceMessageID, i, digest), Origin: "provided_material", Label: label, Locator: "session:" + a.SessionID + ":message:" + a.SourceMessageID, SourceDigest: digest, PageCount: m.Pages}, Text: m.Text})
		frozen.Coverage.Covered = append(frozen.Coverage.Covered, label)
	}
	if r.ProvidedMaterialText != "" {
		if !strings.Contains(a.FinalUserText, r.ProvidedMaterialText) {
			return frozen, fmt.Errorf("provided learning material differs from the persisted user text")
		}
		digest := k12.UnitSummaryDigest(r.ProvidedMaterialText)
		kind, label := "session_text", "本次提供的学习材料"
		if len(input.SessionDocuments) > 0 {
			kind = "session_attachment"
			label = "本次附件中的学习材料"
			if len(input.SessionDocuments) == 1 {
				label = input.SessionDocuments[0].Name
			}
		}
		frozen.Sources = append(frozen.Sources, k12.UnitSummaryFrozenSource{UnitSummarySource: k12.UnitSummarySource{Kind: kind, Ref: kind + ":" + digest, Origin: "provided_material", Label: label, Locator: "session:" + a.SessionID + ":message:" + a.SourceMessageID, SourceDigest: digest}, Text: r.ProvidedMaterialText})
		frozen.Coverage.Covered = append(frozen.Coverage.Covered, "本次提供的学习材料")
	}
	if len(r.MaterialDocumentIDs) == 0 && len(frozen.Sources) == 0 {
		if len(input.SessionDocuments) > 0 || input.ImageAttachmentCount > 0 {
			return frozen, fmt.Errorf("the current session attachment material reader is unavailable")
		}
		return frozen, fmt.Errorf("%w: please provide the %s unit or learning material to summarize", ErrInvalidInput, k12.UnitSummarySubjectLabel(r.Subject))
	}
	// 解析器只能选择已读取的当前归属材料；范围不明确时不把整份文档称为完整单元。
	available := map[string]k12storage.UnitSummaryMaterial{}
	for _, m := range input.Materials {
		available[m.DocumentID] = m
	}
	seen := map[string]bool{}
	for _, id := range r.MaterialDocumentIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		m, ok := available[id]
		if !ok {
			return frozen, fmt.Errorf("selected learning material is not available in the current owner scope")
		}
		if normalized := unitSummarySubjectNormalized(m.Subject); normalized != "" && normalized != r.Subject {
			return frozen, fmt.Errorf("selected learning material belongs to a different subject")
		}
		if strings.TrimSpace(m.Content) == "" { // 目录checkpoint不存正文，恢复时按相同代次重新读，不能静默换代。
			current, e := c.Deps.Records.ReadUnitSummaryMaterials(ctx, a.OwnerID, a.AgentName)
			if e != nil {
				return frozen, e
			}
			for _, actual := range current {
				if actual.DocumentID == id && actual.Generation == m.Generation && actual.SourceDigest == m.SourceDigest {
					m = actual
					break
				}
			}
		}
		if m.Content == "" {
			return frozen, fmt.Errorf("the frozen learning material text is unavailable")
		}
		frozen.Sources = append(frozen.Sources, k12.UnitSummaryFrozenSource{UnitSummarySource: k12.UnitSummarySource{Ref: "material:" + id, Origin: "provided_material", Label: m.Title, Locator: id, DocumentID: id, Generation: m.Generation, SourceDigest: m.SourceDigest}, Text: m.Content})
		frozen.Coverage.Covered = append(frozen.Coverage.Covered, m.Title)
	}
	if frozen.UnitTitle == "" && len(frozen.Sources) == 1 {
		frozen.UnitTitle = frozen.Sources[0].Label
	}
	if frozen.UnitTitle == "" && len(frozen.Sources) > 1 {
		// 可靠多源的实际标签已描述本次partial范围，不另要求家长为内部标题补答。
		labels, seenLabels := []string{}, map[string]bool{}
		for _, source := range frozen.Sources {
			label := strings.TrimSpace(source.Label)
			if label != "" && !seenLabels[label] {
				labels = append(labels, label)
				seenLabels[label] = true
			}
		}
		frozen.UnitTitle = strings.Join(labels, "、")
	}
	if frozen.UnitTitle == "" {
		return frozen, fmt.Errorf("%w: please specify the title or scope of the learning materials", ErrInvalidInput)
	}
	frozen.Coverage.Missing = []string{"未核对教材完整单元范围"}
	return frozen, nil
}

// 编辑对象的真实ID与旧正文是基线，不是教材单元ID或新粘贴来源。
func unitSummaryActiveResolution(active *k12.UnitSummaryRevision, finalText string, r UnitSummaryResolution) UnitSummaryResolution {
	if active == nil || !r.UseActiveRevision || active.Content.Subject != r.Subject {
		return r
	}
	if r.UnitID == active.DocumentID || r.UnitID == active.RevisionID {
		r.UnitID = active.Context.UnitID
	}
	// 已校验的当前资料身份不是KB来源；仅精确同一资料/版本别名可归一，混合其他ID仍保留校验。
	onlyActiveIDs := len(r.MaterialDocumentIDs) > 0
	for _, id := range r.MaterialDocumentIDs {
		if id != active.DocumentID && id != active.RevisionID {
			onlyActiveIDs = false
			break
		}
	}
	if onlyActiveIDs {
		r.MaterialDocumentIDs = []string{}
	}
	if r.UnitID != "" && (r.UnitID == active.Context.UnitTitle || r.UnitID == active.Content.Title) {
		candidate := r
		candidate.UnitID = active.Context.UnitID
		if unitSummarySelectsActiveSources(active.Context, candidate) {
			r = candidate
		}
	}
	// canonical末尾单个换行是序列化边界，不作为模型复制编辑基线后产生的新材料。
	activeTextMatches := r.ProvidedMaterialText == active.CanonicalMarkdown || r.ProvidedMaterialText == strings.TrimSuffix(active.CanonicalMarkdown, "\n")
	if r.ProvidedMaterialText != "" && !strings.Contains(finalText, r.ProvidedMaterialText) && activeTextMatches {
		r.ProvidedMaterialText = ""
	}
	return r
}

// 明确编辑正在阅读的版本时，重复返回同一真实来源ID不算来源变更；标题显示变体不重定义旧范围。
func unitSummarySelectsActiveSources(active k12.UnitSummaryContext, r UnitSummaryResolution) bool {
	if r.UnitID != "" && r.UnitID != active.UnitID {
		return false
	}
	if len(r.MaterialDocumentIDs) == 0 {
		return true
	}
	known, selected := map[string]bool{}, map[string]bool{}
	for _, source := range active.Sources {
		if source.DocumentID != "" {
			known[source.DocumentID] = true
		}
	}
	for _, id := range r.MaterialDocumentIDs {
		if !known[id] {
			return false
		}
		selected[id] = true
	}
	return len(selected) == len(known)
}

// 引号字形与空白格式差异只用于辨认已选实际来源；冻结仍读取原正文，不保存模型改写的引文。
func unitSummaryQuoteNormalized(text string) string {
	normalized := strings.Map(func(r rune) rune {
		switch r {
		case '“', '”':
			return '"'
		case '‘', '’':
			return '\''
		}
		return r
	}, text)
	return strings.Join(strings.Fields(normalized), " ")
}

func unitSummaryPublicSources(sources []k12.UnitSummaryFrozenSource) []k12.UnitSummarySource {
	out := make([]k12.UnitSummarySource, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.UnitSummarySource)
	}
	return out
}
func unitSummaryInputDigest(ctx k12.UnitSummaryContext, requirements k12.UnitSummaryRequirements) string {
	sources, evidence := unitSummaryPublicSources(ctx.Sources), unitSummaryPublicSources(ctx.Evidence)
	sort.Slice(sources, func(i, j int) bool { return sources[i].Ref < sources[j].Ref })
	for i := range sources {
		if sources[i].Kind == "session_text" || sources[i].Kind == "session_attachment" {
			sources[i].Locator = ""
			sources[i].Ref = sources[i].Kind + ":" + sources[i].SourceDigest
		}
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].Ref < evidence[j].Ref })
	return k12.UnitSummaryDigest(struct {
		Subject, GradeTerm, Edition, Volume, UnitID, Title string
		Sources, Evidence                                  []k12.UnitSummarySource
		Requirements                                       k12.UnitSummaryRequirements
	}{ctx.Subject, ctx.GradeTerm, ctx.TextbookEdition, ctx.Volume, ctx.UnitID, ctx.UnitTitle, sources, evidence, requirements})
}
func unitSummaryContentDigest(content k12.UnitSummaryContentV1, frozen k12.UnitSummaryContext, requirements k12.UnitSummaryRequirements) string {
	if requirements.Format == "brief" {
		// 此指纹原已含来源及输出要求；brief合同是输出语义维度，旧standard公式保持不变。
		return k12.UnitSummaryDigest(struct {
			Content        k12.UnitSummaryContentV1
			ContextDigest  string
			RenderContract string
		}{content, unitSummaryInputDigest(frozen, requirements), unitSummaryRenderContract(requirements)})
	}
	return k12.UnitSummaryDigest(struct {
		Content       k12.UnitSummaryContentV1
		ContextDigest string
	}{content, unitSummaryInputDigest(frozen, requirements)})
}

func (c *UnitSummaryCoordinator) renderOnlyUnitSummaryBase(ctx context.Context, a k12.UnitSummaryAttempt, input UnitSummaryResolveInput, resolution UnitSummaryResolution, frozen k12.UnitSummaryContext) (k12.UnitSummaryRevision, error) {
	if !resolution.RenderOnly || resolution.Intent != "revise" || !resolution.UseActiveRevision || resolution.Format != "brief" || input.Request.ActiveRevisionID == "" || input.ActiveRevision == nil || input.ActiveRevision.RevisionID != input.Request.ActiveRevisionID {
		return k12.UnitSummaryRevision{}, fmt.Errorf("layout-only rendering requires the exact opened brief revision")
	}
	if len(input.ProvidedMaterials) != 0 || len(input.SessionDocuments) != 0 || input.ImageAttachmentCount != 0 || resolution.ProvidedMaterialText != "" && strings.Contains(a.FinalUserText, resolution.ProvidedMaterialText) {
		return k12.UnitSummaryRevision{}, fmt.Errorf("layout-only rendering cannot replace or add learning material")
	}
	base, err := c.Deps.Records.FindUnitSummaryRevision(ctx, a.OwnerID, a.AgentName, input.Request.ActiveRevisionID)
	if err != nil {
		return base, err
	}
	resolved := unitSummaryActiveResolution(&base, a.FinalUserText, resolution)
	if base.Requirements.Format != "brief" || base.Content.Subject != resolved.Subject || !unitSummarySelectsActiveSources(base.Context, resolved) || unitSummaryScopeKey(base.Context) != unitSummaryScopeKey(frozen) || k12.UnitSummaryDigest(base.Context.Evidence) != k12.UnitSummaryDigest(frozen.Evidence) {
		return base, fmt.Errorf("layout-only rendering scope differs from the frozen brief material")
	}
	if len(resolved.EvidenceRefs) > 0 {
		known, selected := map[string]bool{}, map[string]bool{}
		for _, source := range base.Context.Evidence {
			known[source.Ref] = true
		}
		for _, ref := range resolved.EvidenceRefs {
			if !known[ref] {
				return base, fmt.Errorf("layout-only rendering cannot replace personal evidence")
			}
			selected[ref] = true
		}
		if len(selected) != len(known) {
			return base, fmt.Errorf("layout-only rendering personal evidence differs from the frozen material")
		}
	}
	if err = k12.ValidateUnitSummaryContent(base.Content, base.Context); err != nil {
		return base, err
	}
	return base, nil
}
func unitSummaryScopeKey(ctx k12.UnitSummaryContext) string {
	type sourceIdentity struct {
		Ref, Document, Manifest, Digest string
		Generation                      int64
		Page                            string
		Segments                        []string
	}
	sources := []sourceIdentity{}
	for _, s := range ctx.Sources {
		ref := s.Ref
		if s.Kind == "session_text" || s.Kind == "session_attachment" {
			ref = s.Kind + ":" + s.SourceDigest
		}
		sources = append(sources, sourceIdentity{ref, s.DocumentID, s.ManifestID, s.SourceDigest, s.Generation, s.Page, s.SegmentRefs})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Ref < sources[j].Ref })
	title := ""
	if ctx.UnitID == "" {
		title = ctx.UnitTitle
	}
	return k12.UnitSummaryDigest(struct {
		Subject, GradeTerm, Edition, Volume, UnitID, Title string
		Sources                                            []sourceIdentity
	}{ctx.Subject, ctx.GradeTerm, ctx.TextbookEdition, ctx.Volume, ctx.UnitID, title, sources})
}

func (c *UnitSummaryCoordinator) finishReadOnly(ctx context.Context, a k12.UnitSummaryAttempt, active *k12.UnitSummaryRevision, r UnitSummaryResolution) error {
	r = unitSummaryActiveResolution(active, a.FinalUserText, r)
	usesActive := active != nil && active.Content.Subject == r.Subject && r.UseActiveRevision && unitSummarySelectsActiveSources(active.Context, r)
	if !usesActive && (active == nil || active.Content.Subject != r.Subject || r.UnitTitle != "" && active.Context.UnitTitle != r.UnitTitle || r.UnitID != "" && active.Context.UnitID != r.UnitID) {
		docs, _, err := c.Deps.Records.ListUnitSummaryDocuments(ctx, a.OwnerID, a.AgentName, "", "", 100)
		if err != nil {
			return err
		}
		var selected *k12.UnitSummaryRevision
		for _, doc := range docs {
			if doc.Subject != r.Subject {
				continue
			}
			revision, e := c.Deps.Records.GetUnitSummaryRevision(ctx, a.OwnerID, a.AgentName, doc.DocumentID, doc.CurrentRevisionID)
			if e != nil {
				return e
			}
			if r.UnitID != "" && r.UnitID != revision.Context.UnitID || r.UnitTitle != "" && r.UnitTitle != revision.Context.UnitTitle {
				continue
			}
			if selected == nil || revision.GeneratedAt > selected.GeneratedAt {
				copy := revision
				selected = &copy
			}
		}
		active = selected
	}
	if active == nil {
		next := a
		next.State = "needs_input"
		next.Clarification = "No saved material matches this scope. Please specify another unit or request a new summary."
		next.LeaseOwner = ""
		next.LeaseExpiresAt = 0
		_, err := c.Deps.Records.CheckpointUnitSummaryAttempt(ctx, a, next)
		return err
	}
	next := a
	next.DocumentID = active.DocumentID
	next.ResultRevisionID = active.RevisionID
	next.ArtifactID = active.ArtifactID
	next.Intent = r.Intent
	next.State = "reused"
	next.Stage = "complete"
	next.LeaseOwner = ""
	next.LeaseExpiresAt = 0
	_, err := c.Deps.Records.CheckpointUnitSummaryAttempt(ctx, a, next)
	return err
}
