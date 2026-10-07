package usecase

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type CurriculumProgressResolveRequest struct {
	OwnerID, AgentName string
	Profile            k12.ChildProfile
	Selection          *k12.CurriculumProgressSelection
	// 写命令必须保留显式选择，目录不匹配时不能退化为未提供进度。
	RequireSelectedProgress bool
}
type CurriculumProgressResolution struct {
	Progress *k12.CurriculumProgress
	Revision int
	Preserve bool
}
type ConfirmedCurriculumProgressRequest struct {
	OwnerID, AgentName       string
	ExpectedProgressRevision int
	Selection                k12.CurriculumProgressSelection
}

func profileForProgress(p k12.WeeklyProfile) k12.ChildProfile {
	return k12.ChildProfile{ChildName: p.ChildName, GradeTerm: p.GradeTerm, SubjectTextbooks: p.SubjectTextbooks, TextbookEdition: p.TextbookEdition}
}
func progressVolume(gradeTerm string) string {
	if strings.HasSuffix(gradeTerm, "上") {
		return "上册"
	}
	if strings.HasSuffix(gradeTerm, "下") {
		return "下册"
	}
	return ""
}
func progressMatchesProfile(p *k12.CurriculumProgress, profile, stored k12.ChildProfile, selection *k12.CurriculumProgressSelection) bool {
	if p == nil || p.TextbookEdition != profile.SubjectTextbooks.Math || p.Volume != progressVolume(profile.GradeTerm) {
		return false
	}
	grade := p.GradeTerm
	if grade == "" {
		grade = stored.GradeTerm
	}
	if grade != profile.GradeTerm {
		return false
	}
	return selection == nil || selection.TextbookManifestID == "" || selection.TextbookManifestID == p.TextbookManifestID
}
func catalogMatchesProfile(c k12.CurriculumCatalog, p k12.ChildProfile) bool {
	return c.GradeTerm == p.GradeTerm && c.TextbookEdition == p.SubjectTextbooks.Math && c.Volume == progressVolume(p.GradeTerm)
}

func (d Deps) progressCatalog(ctx context.Context, owner, agent string, profile k12.ChildProfile, selection *k12.CurriculumProgressSelection) (*k12.CurriculumCatalog, error) {
	if selection != nil && strings.TrimSpace(selection.TextbookManifestID) != "" {
		catalog, err := d.Records.GetTextbookCreationCatalog(ctx, owner, selection.TextbookManifestID)
		if err != nil {
			return nil, err
		}
		if !catalogMatchesProfile(catalog, profile) {
			return nil, nil
		}
		return &catalog, nil
	}
	if agent != "" {
		catalog, _, err := d.Records.GetActiveTextbookCatalogReadOnly(ctx, k12storage.TextbookScope{OwnerID: owner, AgentName: agent, Subject: "math"})
		if err != nil && !errors.Is(err, records.ErrNotFound) {
			return nil, err
		}
		if err == nil && catalogMatchesProfile(catalog, profile) {
			return &catalog, nil
		}
	}
	options, err := d.Records.ListTextbookCreationOptions(ctx, owner)
	if err != nil {
		return nil, err
	}
	var selected *k12.CurriculumCatalog
	for _, option := range options {
		if option.State != "ready_for_confirmation" {
			continue
		}
		catalog, err := d.Records.GetTextbookCreationCatalog(ctx, owner, option.ManifestID)
		if errors.Is(err, records.ErrNotFound) || errors.Is(err, records.ErrIllegalTransition) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !catalogMatchesProfile(catalog, profile) {
			continue
		}
		if selected != nil {
			return nil, nil
		}
		selected = &catalog
	}
	return selected, nil
}

// ResolveCurriculumProgress 只读同一权威进度与真实目录，日期推算不构造学习事实。
func (d Deps) ResolveCurriculumProgress(ctx context.Context, req CurriculumProgressResolveRequest) (CurriculumProgressResolution, error) {
	req.OwnerID, req.AgentName = strings.TrimSpace(req.OwnerID), strings.TrimSpace(req.AgentName)
	if req.OwnerID == "" || d.Records == nil {
		return CurriculumProgressResolution{}, fmt.Errorf("%w: curriculum owner and records required", ErrInvalidInput)
	}
	result := CurriculumProgressResolution{}
	var current *k12.CurriculumProgress
	var stored k12.ChildProfile
	if req.AgentName != "" {
		p, err := d.Records.GetProfileState(ctx, req.AgentName)
		if err == nil {
			stored = profileForProgress(p)
			current, result.Revision, err = d.Records.GetCurriculumProgressState(ctx, req.AgentName, "math")
			if err != nil {
				return result, err
			}
		}
		if err != nil && !errors.Is(err, records.ErrNotFound) {
			return result, err
		}
	}
	manual := req.Selection != nil && req.Selection.EvidenceSource == k12.CurriculumSourceParentConfirmed
	if !manual && current != nil && current.EvidenceSource == k12.CurriculumSourceParentConfirmed && progressMatchesProfile(current, req.Profile, stored, req.Selection) {
		result.Progress = current
		result.Preserve = true
		return result, nil
	}
	if req.Selection != nil && req.Selection.EvidenceSource != k12.CurriculumSourceParentConfirmed && req.Selection.EvidenceSource != k12.CurriculumSourceAIEstimated {
		return result, fmt.Errorf("%w: invalid curriculum source", ErrInvalidInput)
	}
	if manual {
		catalog, err := d.Records.GetTextbookCreationCatalog(ctx, req.OwnerID, req.Selection.TextbookManifestID)
		if err != nil {
			return result, err
		}
		p, err := resolveCurriculumProgress(req.AgentName, selectionProgressInput(*req.Selection), catalog, d.now())
		if err != nil {
			return result, err
		}
		p.GradeTerm = req.Profile.GradeTerm
		result.Progress = &p
		return result, nil
	}
	catalog, err := d.progressCatalog(ctx, req.OwnerID, req.AgentName, req.Profile, req.Selection)
	if err != nil {
		return result, err
	}
	if catalog == nil {
		if req.RequireSelectedProgress && req.Selection != nil {
			return result, fmt.Errorf("%w: selected curriculum progress does not match an available textbook catalog", ErrInvalidInput)
		}
		// 没有新建议不等于清除当前投影，只有显式 null 才推进清除生命周期。
		result.Preserve = current == nil || progressMatchesProfile(current, req.Profile, stored, req.Selection)
		return result, nil
	}
	unit, basis, err := k12.EstimateCurriculumUnit(*catalog, req.Profile.GradeTerm, time.Unix(d.now(), 0))
	if err != nil {
		return result, err
	}
	selection := k12.CurriculumProgressSelection{Subject: "math", TextbookManifestID: catalog.TextbookManifestID, Volume: catalog.Volume, UnitID: unit.UnitID, EvidenceSource: k12.CurriculumSourceParentConfirmed}
	if req.Selection != nil {
		selection.LessonID = req.Selection.LessonID
		selection.PageFrom = req.Selection.PageFrom
		selection.PageTo = req.Selection.PageTo
	}
	if req.Selection == nil && current != nil && current.EvidenceSource == k12.CurriculumSourceAIEstimated && current.TextbookManifestID == catalog.TextbookManifestID && progressMatchesProfile(current, req.Profile, stored, nil) {
		selection.LessonID = current.LessonID
		selection.PageFrom = current.RequestedPageFrom
		selection.PageTo = current.RequestedPageTo
	}
	// 用户显式课时或页码只在目录唯一映射时校准；原输入由后续目录校验保留。
	calibrated := false
	if selection.LessonID != "" || selection.PageFrom != nil || selection.PageTo != nil {
		matches := []string{}
		for _, u := range catalog.Units {
			match := false
			if selection.LessonID != "" {
				for _, l := range u.Lessons {
					if l.LessonID == selection.LessonID {
						match = true
					}
				}
			} else {
				from, to := selection.PageFrom, selection.PageTo
				if from == nil {
					from = to
				}
				if to == nil {
					to = from
				}
				match = from != nil && *from >= u.PageFrom && *to <= u.PageTo && *from <= *to
			}
			if match {
				matches = append(matches, u.UnitID)
			}
		}
		if len(matches) == 1 {
			selection.UnitID = matches[0]
			calibrated = true
		}
	}
	if !calibrated && req.AgentName != "" && catalog.TextbookBindingID != "" {
		scope, found, scopeErr := d.Records.GetActiveTextbookGroundingScopeReadOnly(ctx, k12storage.TextbookScope{OwnerID: req.OwnerID, AgentName: req.AgentName, Subject: "math"})
		if scopeErr != nil {
			return result, scopeErr
		}
		if found {
			u, sourceAt, hash, found, evidenceErr := d.curriculumProgressHomeworkEvidence(ctx, req.OwnerID, req.AgentName, *catalog, scope, current)
			if evidenceErr != nil {
				return result, evidenceErr
			}
			if found && time.Unix(sourceAt, 0).In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02") >= basis.ReferenceTermStart && time.Unix(sourceAt, 0).In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02") <= basis.ReferenceTermEnd {
				selection.UnitID = u
				basis.EvidenceReceiptHash = hash
			}
		}
	}
	if !calibrated && current != nil && current.EvidenceSource == k12.CurriculumSourceAIEstimated && progressMatchesProfile(current, req.Profile, stored, nil) && current.TextbookManifestID == catalog.TextbookManifestID {
		oldIndex, newIndex := -1, -1
		for i, u := range catalog.Units {
			if u.UnitID == current.UnitID {
				oldIndex = i
			}
			if u.UnitID == selection.UnitID {
				newIndex = i
			}
		}
		if oldIndex > newIndex {
			selection.UnitID = current.UnitID
		}
	}
	p, err := resolveCurriculumProgress(req.AgentName, selectionProgressInput(selection), *catalog, d.now())
	if err != nil {
		return result, err
	}
	p.EvidenceSource = k12.CurriculumSourceAIEstimated
	p.ConfirmedAt = 0
	p.EstimateBasis = basis
	p.GradeTerm = req.Profile.GradeTerm
	p.Revision = result.Revision
	result.Progress = &p
	return result, nil
}
func selectionProgressInput(s k12.CurriculumProgressSelection) CurriculumProgressInput {
	return CurriculumProgressInput{Subject: s.Subject, TextbookManifestID: s.TextbookManifestID, Volume: s.Volume, UnitID: s.UnitID, LessonID: s.LessonID, PageFrom: s.PageFrom, PageTo: s.PageTo, EvidenceSource: s.EvidenceSource}
}

func (d Deps) PreviewCurriculumProgress(ctx context.Context, ownerID, agentName string) (*k12.CurriculumProgress, error) {
	p, err := d.Records.GetProfileState(ctx, agentName)
	if err != nil {
		return nil, err
	}
	r, err := d.ResolveCurriculumProgress(ctx, CurriculumProgressResolveRequest{OwnerID: ownerID, AgentName: agentName, Profile: profileForProgress(p)})
	return r.Progress, err
}
func (d Deps) GetProgressCurriculumCatalog(ctx context.Context, ownerID, agentName string) (k12.CurriculumCatalog, error) {
	p, err := d.Records.GetProfileState(ctx, agentName)
	if err != nil {
		return k12.CurriculumCatalog{}, err
	}
	c, err := d.progressCatalog(ctx, ownerID, agentName, profileForProgress(p), nil)
	if err != nil {
		return k12.CurriculumCatalog{}, err
	}
	if c == nil {
		return k12.CurriculumCatalog{}, records.ErrNotFound
	}
	return *c, nil
}

// EnsureCurriculumProgress 仅由用户新业务命令采用建议，不由预览触发。
func (d Deps) EnsureCurriculumProgress(ctx context.Context, ownerID, agentName string) (*k12.CurriculumProgress, error) {
	p, err := d.Records.GetProfileState(ctx, agentName)
	if err != nil {
		return nil, err
	}
	profile := profileForProgress(p)
	r, err := d.ResolveCurriculumProgress(ctx, CurriculumProgressResolveRequest{OwnerID: ownerID, AgentName: agentName, Profile: profile})
	if err != nil || r.Progress == nil || r.Preserve {
		return r.Progress, err
	}
	current, _, err := d.Records.GetCurriculumProgressState(ctx, agentName, "math")
	if err != nil {
		return nil, err
	}
	if current != nil && current.TextbookManifestID == r.Progress.TextbookManifestID && current.UnitID == r.Progress.UnitID && current.LessonID == r.Progress.LessonID && reflect.DeepEqual(current.EstimateBasis, r.Progress.EstimateBasis) {
		return current, nil
	}
	return d.Records.SaveCurriculumProgress(ctx, k12storage.TextbookScope{OwnerID: ownerID, AgentName: agentName, Subject: "math"}, profile, r.Progress, r.Revision, d.now())
}
func (d Deps) SetConfirmedCurriculumProgress(ctx context.Context, req ConfirmedCurriculumProgressRequest) (*k12.CurriculumProgress, error) {
	p, err := d.Records.GetProfileState(ctx, req.AgentName)
	if err != nil {
		return nil, err
	}
	profile := profileForProgress(p)
	req.Selection.EvidenceSource = k12.CurriculumSourceParentConfirmed
	r, err := d.ResolveCurriculumProgress(ctx, CurriculumProgressResolveRequest{OwnerID: req.OwnerID, AgentName: req.AgentName, Profile: profile, Selection: &req.Selection, RequireSelectedProgress: true})
	if err != nil {
		return nil, err
	}
	return d.Records.SaveCurriculumProgress(ctx, k12storage.TextbookScope{OwnerID: req.OwnerID, AgentName: req.AgentName, Subject: "math"}, profile, r.Progress, req.ExpectedProgressRevision, d.now())
}
