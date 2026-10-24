package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/ai-core/media/voicechat"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
)

func (s *Server) validateMediaReplayRequest(r *http.Request, req ChatRequest) (bool, error) {
	validator, ok := s.engine.(interface {
		ValidateMediaReplay(context.Context, *adapter.Message) (bool, error)
	})
	if !ok || req.RequestID == "" || req.SessionID == "" {
		return false, nil
	}
	metadata := make(map[string]string, len(req.Metadata)+3)
	for key, value := range req.Metadata {
		metadata[key] = value
	}
	metadata["request_id"], metadata["provider"], metadata["model"] = req.RequestID, req.Provider, req.Model
	return validator.ValidateMediaReplay(r.Context(), &adapter.Message{ID: req.RequestID, UserID: httpPrincipalFromRequest(r).userID, SessionID: req.SessionID, Content: req.Message, Attachments: req.Attachments, Metadata: metadata})
}

// 显式媒体入口使用同一媒体路由校验，文本请求仍遵守原 completion 能力合同。
func (s *Server) validateRequestedConversationModel(provider, model string, metadata map[string]string) error {
	if s.mediaFlow != nil && strings.TrimSpace(model) != "" {
		if kind := engine.RequestedMediaKind(model, metadata); kind != "" {
			_, err := s.mediaFlow.Router().Resolve(kind, "generate", provider, model, "")
			return err
		}
	}
	return validateRequestedCompletionModel(s.activeLLMConfig(), provider, model)
}

func (s *Server) SetMediaFlow(flow *mediaflow.Service) {
	s.mediaFlow = flow
	if flow != nil {
		if snapshot := flow.Router().Current(); snapshot != nil {
			s.imagegenSvc, s.videogenSvc, s.voiceChatSvc = snapshot.Images, snapshot.Videos, snapshot.Chat
			s.voiceSvc = snapshot.Voice
		}
	}
}

func (s *Server) activeVoice() *voice.Service {
	if s.mediaFlow != nil {
		if snapshot := s.mediaFlow.Router().Current(); snapshot != nil {
			return snapshot.Voice
		}
	}
	return s.voiceSvc
}

func (s *Server) mediaStatus(w http.ResponseWriter, kind, action string) {
	providers, models := []string{}, []string{}
	explicitCandidates := []map[string]string{}
	seenProviders, seenModels := map[string]bool{}, map[string]bool{}
	if snapshot := s.mediaFlow.Router().Current(); snapshot != nil {
		for _, route := range snapshot.Routes {
			if route.Kind != kind || route.Action != action {
				continue
			}
			if !seenProviders[route.ProviderID] {
				providers = append(providers, route.ProviderID)
				seenProviders[route.ProviderID] = true
			}
			if route.Model != "" && !seenModels[route.Model] {
				models = append(models, route.Model)
				seenModels[route.Model] = true
			}
		}
		if kind == "image" && action == "generate" {
			for _, route := range snapshot.ExplicitImageRoutes {
				explicitCandidates = append(explicitCandidates, map[string]string{"provider_instance_id": route.ProviderID, "model": route.Model})
			}
		}
	}
	sort.Strings(providers)
	sort.Strings(models)
	response := map[string]any{"enabled": len(providers)+len(explicitCandidates) > 0, "providers": providers, "models": models}
	if kind == "image" {
		response["explicit_candidates"] = explicitCandidates
	}
	writeJSON(w, http.StatusOK, response)
}

func mediaOrigin(r *http.Request, kind, action, key string) mediaflow.Origin {
	if key == "" {
		key = r.Header.Get("Idempotency-Key")
	}
	if key == "" {
		key = r.Header.Get("X-Request-ID")
	}
	return mediaflow.Origin{Owner: httpPrincipalFromRequest(r).userID, Kind: "direct_api", RequestID: key, Ordinal: kind + ":" + action}
}

func writeMediaError(w http.ResponseWriter, op *mediaflow.Operation, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, mediaflow.ErrConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, mediaflow.ErrNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, mediaflow.ErrUnavailable) {
		status = http.StatusServiceUnavailable
	}
	if errors.Is(err, mediaflow.ErrAmbiguous) {
		status = http.StatusBadRequest
	}
	if op != nil {
		w.Header().Set("X-Media-Operation-ID", op.ID)
	}
	if err == nil && op != nil {
		err = fmt.Errorf("media operation %s: %s", op.ID, op.ErrorCode)
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleMediaImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider,omitempty"`
		imagegen.Request
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		writeJSON(w, 400, map[string]string{"error": "invalid image generation request"})
		return
	}
	op, err := s.mediaFlow.Begin(r.Context(), mediaflow.Request{Origin: mediaOrigin(r, "image", "generate", req.IdempotencyKey), Kind: "image", Provider: req.Provider, Model: req.Model, Image: &req.Request})
	if err != nil {
		writeMediaError(w, nil, err)
		return
	}
	w.Header().Set("X-Media-Operation-ID", op.ID)
	op, err = s.mediaFlow.Wait(r.Context(), op.Owner, op.ID, false)
	if err != nil || op.SaveState != "ready" {
		writeMediaError(w, op, err)
		return
	}
	var result imagegen.Result
	if err := json.Unmarshal(op.Result, &result); err != nil {
		writeMediaError(w, op, err)
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleMediaVideoSubmit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider,omitempty"`
		videogen.Request
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Prompt) == "" && strings.TrimSpace(req.ImageURL) == "" {
		writeJSON(w, 400, map[string]string{"error": "invalid video generation request"})
		return
	}
	op, err := s.mediaFlow.Begin(r.Context(), mediaflow.Request{Origin: mediaOrigin(r, "video", "generate", req.IdempotencyKey), Kind: "video", Provider: req.Provider, Model: req.Model, Video: &req.Request})
	if err != nil {
		writeMediaError(w, nil, err)
		return
	}
	op, err = s.mediaFlow.Wait(r.Context(), op.Owner, op.ID, true)
	if err != nil || op.UpstreamID == "" {
		writeMediaError(w, op, err)
		return
	}
	w.Header().Set("X-Media-Operation-ID", op.ID)
	writeJSON(w, 200, map[string]string{"task_id": op.ID})
}

func (s *Server) handleMediaVideoPoll(w http.ResponseWriter, r *http.Request) {
	op, err := s.mediaFlow.Read(r.Context(), httpPrincipalFromRequest(r).userID, r.PathValue("id"))
	if err != nil {
		writeMediaError(w, nil, err)
		return
	}
	status := videogen.TaskStatus{TaskID: op.ID, Provider: op.Route.ProviderID, Model: op.Route.Model, Status: op.GenerationState, Done: op.Settled()}
	if op.Route.Kind != "video" {
		writeMediaError(w, nil, mediaflow.ErrNotFound)
		return
	}
	if len(op.Result) > 0 {
		_ = json.Unmarshal(op.Result, &status)
	}
	status.TaskID = op.ID
	if op.SaveState != "ready" && op.Settled() {
		status.Status = "failed"
		status.Error = op.ErrorCode
		status.Done = true
	}
	writeJSON(w, 200, status)
}

func (s *Server) handleMediaSpeech(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text  string `json:"text"`
		Voice string `json:"voice,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeJSON(w, 400, map[string]string{"error": "invalid speech request"})
		return
	}
	op, err := s.mediaFlow.Begin(r.Context(), mediaflow.Request{Origin: mediaOrigin(r, "audio", "synthesize", ""), Kind: "audio", Action: "synthesize", Speech: &mediaflow.SpeechRequest{Text: req.Text, Options: voice.SynthesizeOptions{Voice: req.Voice}}})
	if err != nil {
		writeMediaError(w, nil, err)
		return
	}
	op, err = s.mediaFlow.Wait(r.Context(), op.Owner, op.ID, false)
	if err != nil || op.SaveState != "ready" || len(op.Assets) != 1 {
		writeMediaError(w, op, err)
		return
	}
	reader, err := s.genStore.OpenReader(op.Assets[0].Path)
	if err != nil {
		writeMediaError(w, op, err)
		return
	}
	defer reader.Close()
	w.Header().Set("X-Media-Operation-ID", op.ID)
	w.Header().Set("Content-Type", op.Assets[0].MIME)
	w.WriteHeader(200)
	_, _ = io.Copy(w, reader)
}

func (s *Server) handleMediaVoiceChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider,omitempty"`
		voicechat.Request
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid audio chat request"})
		return
	}
	op, err := s.mediaFlow.Begin(r.Context(), mediaflow.Request{Origin: mediaOrigin(r, "audio", "chat", ""), Kind: "audio", Action: "chat", Provider: req.Provider, Model: req.Model, Chat: &req.Request})
	if err != nil {
		writeMediaError(w, nil, err)
		return
	}
	op, err = s.mediaFlow.Wait(r.Context(), op.Owner, op.ID, false)
	if err != nil || op.SaveState != "ready" {
		writeMediaError(w, op, err)
		return
	}
	var result voicechat.Result
	if err := json.Unmarshal(op.Result, &result); err != nil {
		writeMediaError(w, op, err)
		return
	}
	w.Header().Set("X-Media-Operation-ID", op.ID)
	writeJSON(w, 200, struct {
		*voicechat.Result
		AudioFilePath string `json:"audio_file_path"`
	}{&result, op.Assets[0].Path})
}

func (s *Server) handleMediaOperation(w http.ResponseWriter, r *http.Request) {
	if s.mediaFlow == nil {
		writeJSON(w, 503, map[string]string{"error": "media service is unavailable"})
		return
	}
	owner, id := httpPrincipalFromRequest(r).userID, r.PathValue("id")
	var op *mediaflow.Operation
	var err error
	switch r.PathValue("action") {
	case "resume":
		op, err = s.mediaFlow.Resume(r.Context(), owner, id)
	case "cancel":
		op, err = s.mediaFlow.Cancel(r.Context(), owner, id)
	case "":
		op, err = s.mediaFlow.Read(r.Context(), owner, id)
	default:
		writeJSON(w, 400, map[string]string{"error": "unsupported media operation action"})
		return
	}
	if err != nil {
		writeMediaError(w, nil, err)
		return
	}
	deliveries, err := s.mediaFlow.DeliveryStatuses(r.Context(), owner, id)
	if err != nil {
		writeMediaError(w, op, err)
		return
	}
	writeJSON(w, 200, map[string]any{"operation_id": op.ID, "kind": op.Route.Kind, "action": op.Route.Action, "provider_instance_id": op.Route.ProviderID, "actual_provider_instance_id": op.ActualProviderID, "model": op.Route.Model, "generation_state": op.GenerationState, "save_state": op.SaveState, "assets": op.Assets, "deliveries": deliveries, "cancel_requested": op.CancelRequested, "error_code": op.ErrorCode, "error": op.Error, "revision": op.Revision, "created_at": op.CreatedAt, "updated_at": op.UpdatedAt, "timings_ms": op.Timings, "attempts": op.Attempts, "selection": op.Request.Selection})
}
