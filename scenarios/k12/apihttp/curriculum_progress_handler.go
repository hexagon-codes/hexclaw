package apihttp

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// estimateCurriculumProgress 只读目录建议；日期与校准由同一领域解析器计算。
func (h *handler) estimateCurriculumProgress(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := strings.TrimSpace(q.Get("agent"))
	subject := strings.TrimSpace(q.Get("subject"))
	if subject != "" && subject != "math" {
		writeErr(w, http.StatusBadRequest, "subject must be math")
		return
	}
	var owner string
	var err error
	if agent == "" {
		owner, err = h.ownerScope(r.Context())
	} else {
		owner, err = h.authorizedAgentOwnerScope(r.Context(), agent)
	}
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusInternalServerError), err.Error())
		return
	}
	profile := k12.ChildProfile{GradeTerm: strings.TrimSpace(q.Get("grade_term")), SubjectTextbooks: k12.SubjectTextbooks{Math: strings.TrimSpace(q.Get("textbook_edition"))}}
	if agent != "" && (profile.GradeTerm == "" || profile.SubjectTextbooks.Math == "") {
		stored, readErr := h.rt.Records.GetProfileState(r.Context(), agent)
		if readErr != nil {
			writeErr(w, httpStatusForK12Error(readErr, http.StatusInternalServerError), readErr.Error())
			return
		}
		if profile.GradeTerm == "" {
			profile.GradeTerm = stored.GradeTerm
		}
		if profile.SubjectTextbooks.Math == "" {
			profile.SubjectTextbooks.Math = stored.SubjectTextbooks.Math
		}
	}
	var selection *k12.CurriculumProgressSelection
	if q.Get("textbook_manifest_id") != "" || q.Get("lesson_id") != "" || q.Get("page_from") != "" || q.Get("page_to") != "" {
		selection = &k12.CurriculumProgressSelection{Subject: "math", TextbookManifestID: q.Get("textbook_manifest_id"), UnitID: q.Get("unit_id"), LessonID: q.Get("lesson_id"), EvidenceSource: k12.CurriculumSourceAIEstimated}
		for key, dest := range map[string]**int{"page_from": &selection.PageFrom, "page_to": &selection.PageTo} {
			if q.Get(key) != "" {
				value, parseErr := strconv.Atoi(q.Get(key))
				if parseErr != nil {
					writeErr(w, http.StatusBadRequest, "invalid "+key)
					return
				}
				*dest = &value
			}
		}
	}
	result, err := h.rt.Deps.ResolveCurriculumProgress(r.Context(), usecase.CurriculumProgressResolveRequest{OwnerID: owner, AgentName: agent, Profile: profile, Selection: selection})
	if err != nil {
		writeErr(w, httpStatusForK12Error(err, http.StatusInternalServerError), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"progress": result.Progress, "revision": result.Revision})
}
