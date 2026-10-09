package api

// §11.8 交互层 API：Prompt 库 + 记忆薄版的服务端下发与 CRUD。
//   - GET    /api/v1/prompts            列出启用条目（前端 ✨/ 召唤拉取）
//   - GET    /api/v1/prompts/all        列出全部（管理 UI）
//   - POST   /api/v1/prompts            创建/更新（含 command 的 $ARGUMENTS 渲染由前端组装后发送）
//   - DELETE /api/v1/prompts/{id}       删除
//   - GET    /api/v1/memories           列出
//   - POST   /api/v1/memories           创建/更新（"记住这条" 写 fact）
//   - DELETE /api/v1/memories/{id}      删除

import (
	"encoding/json"
	"net/http"

	"github.com/hexagon-codes/hexclaw/library"
)

func (s *Server) handleListPrompts(w http.ResponseWriter, r *http.Request) {
	if s.promptStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"prompts": []any{}, "total": 0})
		return
	}
	list, err := s.promptStore.ListEnabled(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "获取 Prompt 列表失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": list, "total": len(list)})
}

func (s *Server) handleListAllPrompts(w http.ResponseWriter, r *http.Request) {
	if s.promptStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"prompts": []any{}, "total": 0})
		return
	}
	list, err := s.promptStore.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "获取 Prompt 列表失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": list, "total": len(list)})
}

func (s *Server) handleUpsertPrompt(w http.ResponseWriter, r *http.Request) {
	if s.promptStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Prompt 库未启用"})
		return
	}
	var p library.Prompt
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误: " + err.Error()})
		return
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid prompt request: " + err.Error()})
		return
	}
	if p.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title 不能为空"})
		return
	}
	var previous library.Prompt
	if p.ID != "" {
		var err error
		previous, _, err = s.promptStore.Get(r.Context(), p.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	// 旧客户端没有新增元数据字段时保留原值；显式空值仍可编辑，内置身份由Store保护。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid prompt request"})
		return
	}
	for key, target := range map[string]*string{"command": &p.Command, "description": &p.Description,
		"scenario": &p.Scenario, "subject": &p.Subject, "task_kind": &p.TaskKind} {
		if _, supplied := fields[key]; supplied {
			continue
		}
		switch key {
		case "command":
			*target = previous.Command
		case "description":
			*target = previous.Description
		case "scenario":
			*target = previous.Scenario
		case "subject":
			*target = previous.Subject
		case "task_kind":
			*target = previous.TaskKind
		}
	}
	if err := validatePromptInputLengths(p, previous); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	id, err := s.promptStore.Upsert(r.Context(), &p)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "保存 Prompt 失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) handleDeletePrompt(w http.ResponseWriter, r *http.Request) {
	if s.promptStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Prompt 库未启用"})
		return
	}
	id := r.PathValue("id")
	if err := s.promptStore.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "删除 Prompt 失败: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}
