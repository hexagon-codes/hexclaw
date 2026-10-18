package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hexagon-codes/hexclaw/cron"
	"github.com/hexagon-codes/hexclaw/webhook"
)

type workflowTriggerBinding struct {
	EndpointRef string `json:"endpoint_ref,omitempty"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Schedule    string `json:"schedule,omitempty"`
	Input       string `json:"input,omitempty"`
	Secret      string `json:"secret,omitempty"`
	UserID      string `json:"user_id"`
}

func (s *Server) workflowDefinition(id string) *WorkflowData {
	s.workflowStore.mu.RLock()
	defer s.workflowStore.mu.RUnlock()
	return cloneWorkflowDefinition(s.workflowStore.workflows[id])
}

func (s *Server) handleBindWorkflowTrigger(w http.ResponseWriter, r *http.Request) {
	wf := s.workflowDefinition(r.PathValue("id"))
	if wf == nil {
		writeAPIError(w, 404, "NOT_FOUND", "Workflow not found")
		return
	}
	var binding workflowTriggerBinding
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&binding); err != nil {
		writeAPIError(w, 400, "INVALID_REQUEST", "Invalid trigger binding")
		return
	}
	binding.UserID = sessionUserIDFromRequestOrBody(r, binding.UserID)
	if binding.Name == "" {
		binding.Name = wf.Name
	}
	switch binding.Kind {
	case "cron":
		if s.scheduler == nil {
			writeAPIError(w, 503, "UNAVAILABLE", "Scheduler is unavailable")
			return
		}
		sum := sha256.Sum256([]byte(wf.ID + ":" + binding.UserID + ":" + binding.Schedule))
		id := "wf-cron-" + hex.EncodeToString(sum[:12])
		job := &cron.Job{ID: id, Name: binding.Name, Type: cron.JobTypeCron, Schedule: binding.Schedule, UserID: binding.UserID,
			SourcePrompt: "Workflow: " + wf.Name, SourceKey: "workflow:" + wf.ID + "/" + id, Spec: &cron.JobSpec{Runtime: "workflow", TimeoutSec: 600, Inputs: map[string]any{"workflow_id": wf.ID, "input": binding.Input}}}
		saved, err := s.scheduler.UpsertWorkflowJob(r.Context(), job)
		if err != nil {
			writeAPIError(w, 400, "WORKFLOW_BIND_FAILED", err.Error())
			return
		}
		writeJSON(w, 200, saved)
	case "webhook":
		if s.webhookMgr == nil {
			writeAPIError(w, 503, "UNAVAILABLE", "Webhook manager is unavailable")
			return
		}
		if binding.EndpointRef != "" {
			endpoint, err := s.webhookMgr.BindWorkflow(r.Context(), binding.EndpointRef, binding.UserID, wf.ID)
			if err != nil {
				writeAPIError(w, 409, "WORKFLOW_BIND_FAILED", err.Error())
				return
			}
			writeJSON(w, 200, endpoint)
			return
		}
		if existing, ok := s.webhookMgr.Get(binding.Name); ok {
			if existing.WorkflowID != wf.ID || existing.UserID != binding.UserID {
				writeAPIError(w, 409, "CONFLICT", "Webhook name is already in use")
				return
			}
			writeJSON(w, 200, existing)
			return
		}
		payload, _ := json.Marshal(RegisterWebhookRequest{Name: binding.Name, Type: string(webhook.TypeGeneric), WorkflowID: wf.ID, UserID: binding.UserID, Secret: binding.Secret})
		request := r.Clone(r.Context())
		request.Body = io.NopCloser(bytes.NewReader(payload))
		request.ContentLength = int64(len(payload))
		s.handleRegisterWebhook(w, request)
	default:
		writeAPIError(w, 400, "INVALID_REQUEST", "Trigger kind must be cron or webhook")
	}
}

func (s *Server) handleListWorkflowTriggers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	owner := sessionUserIDFromRequestOrBody(r, r.URL.Query().Get("user_id"))
	var bindings []any
	if s.scheduler != nil {
		jobs, err := s.scheduler.ListJobs(r.Context(), owner)
		if err != nil {
			writeAPIError(w, 500, "WORKFLOW_BIND_FAILED", "Could not read scheduled triggers")
			return
		}
		for _, job := range jobs {
			if job.Spec != nil && job.Spec.Runtime == "workflow" && stringValue(job.Spec.Inputs["workflow_id"]) == id {
				location := job.ScheduleLocation()
				zoneTime := job.NextRunAt
				if zoneTime.IsZero() {
					zoneTime = time.Now()
				}
				abbreviation, offset := zoneTime.In(location).Zone()
				bindings = append(bindings, map[string]any{
					"kind": "cron", "id": job.ID, "name": job.Name, "schedule": job.Schedule, "status": job.Status,
					"next_run_at": job.NextRunAt, "timezone": location.String(),
					"timezone_abbreviation": abbreviation, "utc_offset_seconds": offset,
				})
			}
		}
	}
	if s.webhookMgr != nil {
		webhooks, err := s.webhookMgr.List(r.Context(), owner)
		if err != nil {
			writeAPIError(w, 500, "WORKFLOW_BIND_FAILED", "Could not read webhook triggers")
			return
		}
		for _, wh := range webhooks {
			if wh.WorkflowID == id {
				bindings = append(bindings, map[string]any{"kind": "webhook", "id": wh.ID, "name": wh.Name, "enabled": wh.Enabled})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"bindings": bindings})
}

func (s *Server) handleRemoveWorkflowTrigger(w http.ResponseWriter, r *http.Request) {
	id, target := r.PathValue("id"), r.PathValue("target")
	switch r.PathValue("kind") {
	case "cron":
		if s.scheduler == nil {
			writeAPIError(w, 503, "UNAVAILABLE", "Scheduler is unavailable")
			return
		}
		jobs, err := s.scheduler.ListJobs(r.Context(), sessionUserIDFromRequestOrBody(r, r.URL.Query().Get("user_id")))
		if err != nil {
			writeAPIError(w, 500, "WORKFLOW_BIND_FAILED", "Could not read scheduled triggers")
			return
		}
		var job *cron.Job
		for _, candidate := range jobs {
			if candidate.ID == target {
				job = candidate
				break
			}
		}
		if job == nil || job.Spec == nil || job.Spec.Runtime != "workflow" || stringValue(job.Spec.Inputs["workflow_id"]) != id {
			writeAPIError(w, 404, "NOT_FOUND", "Trigger binding not found")
			return
		}
		if err := s.scheduler.RemoveJobForOwner(r.Context(), target, job.UserID); err != nil {
			writeAPIError(w, 500, "WORKFLOW_BIND_FAILED", "Trigger binding could not be removed")
			return
		}
	case "webhook":
		if s.webhookMgr == nil {
			writeAPIError(w, 503, "UNAVAILABLE", "Webhook manager is unavailable")
			return
		}
		wh, ok := s.webhookMgr.Get(target)
		if !ok || wh.WorkflowID != id {
			writeAPIError(w, 404, "NOT_FOUND", "Trigger binding not found")
			return
		}
		if err := s.webhookMgr.UnbindWorkflow(r.Context(), target, wh.UserID, id); err != nil {
			writeAPIError(w, 500, "WORKFLOW_BIND_FAILED", "Trigger binding could not be removed")
			return
		}
	default:
		writeAPIError(w, 400, "INVALID_REQUEST", "Unknown trigger kind")
		return
	}
	writeJSON(w, 200, map[string]bool{"removed": true})
}

func (s *Server) waitWorkflow(ctx context.Context, run *WorkflowRun) (*WorkflowRun, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.workflowStore.mu.RLock()
		current := cloneWorkflowRun(s.workflowStore.runs[run.ID])
		s.workflowStore.mu.RUnlock()
		if current.Status != "running" && current.Status != "pending" {
			if current.Status != "completed" {
				return current, fmt.Errorf("workflow %s: %s", current.Status, current.Error)
			}
			return current, nil
		}
		select {
		case <-ctx.Done():
			return current, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) runWorkflowCron(ctx context.Context, job *cron.Job) (*cron.RunResult, error) {
	wf := s.workflowDefinition(stringValue(job.Spec.Inputs["workflow_id"]))
	if wf == nil {
		return nil, fmt.Errorf("Workflow not found")
	}
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{Input: stringValue(job.Spec.Inputs["input"]), UserID: job.UserID, Platform: job.Platform, ChatID: job.ChatID,
		IdempotencyKey: fmt.Sprintf("cron:%s:%s:%s", job.ID, job.Generation, job.NextRunAt.UTC().Format(time.RFC3339Nano))}, nil)
	if err != nil {
		return nil, err
	}
	final, err := s.waitWorkflow(ctx, run)
	if err != nil {
		if ctx.Err() != nil {
			s.workflowStore.mu.RLock()
			cancel := s.workflowStore.cancels[run.ID]
			s.workflowStore.mu.RUnlock()
			if cancel != nil {
				cancel()
			}
		}
		return nil, err
	}
	return &cron.RunResult{Status: "success", Data: final.Output}, nil
}

func (s *Server) runWorkflowWebhook(ctx context.Context, event *webhook.Event, _ string) error {
	wf := s.workflowDefinition(event.WorkflowID)
	if wf == nil {
		return fmt.Errorf("Workflow not found")
	}
	input, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	key := ""
	if event.EventID != "" {
		key = "webhook:" + event.WebhookID + ":" + event.EventID
	}
	run, err := s.launchWorkflow(wf, RunWorkflowRequest{Input: string(input), UserID: event.UserID, IdempotencyKey: key}, nil)
	if err != nil {
		return err
	}
	_, err = s.waitWorkflow(ctx, run)
	return err
}

func (s *Server) removeWorkflowBindings(ctx context.Context, id string) error {
	if s.scheduler != nil {
		if _, err := s.scheduler.RemoveJobsBySourceKeyPrefix(ctx, "workflow:"+id+"/"); err != nil {
			return err
		}
	}
	if s.webhookMgr != nil {
		for _, wh := range s.webhookMgr.WorkflowBindings(id) {
			if err := s.webhookMgr.UnbindWorkflow(ctx, wh.Name, wh.UserID, id); err != nil {
				return err
			}
		}
	}
	return nil
}
