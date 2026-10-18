package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hexagon-codes/hexagon"
)

// WorkflowCheckpoint 同时保留数据与控制流，恢复时不重新推导已完成分支。
type WorkflowCheckpoint struct {
	Outputs          map[string]string `json:"outputs"`
	Handoffs         map[string]string `json:"handoffs"`
	DeadNodes        map[string]string `json:"dead_nodes"`
	DeactivatedEdges map[string]string `json:"deactivated_edges"`
	Output           string            `json:"output,omitempty"`
}

func cloneWorkflowDefinition(wf *WorkflowData) *WorkflowData {
	if wf == nil {
		return nil
	}
	data, _ := json.Marshal(wf)
	var cloned WorkflowData
	_ = json.Unmarshal(data, &cloned)
	cloned.Revision = workflowRevision(&cloned)
	return &cloned
}

func workflowRevision(wf *WorkflowData) string {
	data, _ := json.Marshal([]any{wf.Name, wf.Description, wf.Nodes, wf.Edges, wf.Data})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cloneWorkflowRun(run *WorkflowRun) *WorkflowRun {
	data, _ := json.Marshal(run)
	var cloned WorkflowRun
	_ = json.Unmarshal(data, &cloned)
	return &cloned
}

// writeWorkflowFile 先同步临时文件再原子替换，失败时保留原始文件。
func writeWorkflowFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".workflow-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o640); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func (ws *WorkflowStore) reconcileInterruptedRuns() {
	changed := false
	for _, run := range ws.runs {
		if run.Status != "running" && run.Status != "pending" {
			continue
		}
		run.Status = "interrupted"
		run.Error = "Workflow execution was interrupted"
		run.FinishedAt = time.Now()
		for _, node := range run.NodeResults {
			if node.Status == nodeStatusRunning && k12WorkflowExternalNode(node.Type) {
				run.Status = "outcome_unknown"
				run.Error = "An external operation has no confirmed result; inspect it before retrying"
			}
		}
		changed = true
	}
	if changed {
		_ = ws.persistRuns()
	}
}

func checkpointState(checkpoint *WorkflowCheckpoint, input string) hexagon.MapState {
	if checkpoint == nil {
		checkpoint = &WorkflowCheckpoint{}
	}
	return hexagon.MapState{
		stateKeyInput: input, stateKeyNodeOutputs: cloneStringMap(checkpoint.Outputs),
		stateKeyNodeHandoffs: cloneStringMap(checkpoint.Handoffs), stateKeyDeadNodes: cloneStringMap(checkpoint.DeadNodes),
		stateKeyDeactivatedEdges: cloneStringMap(checkpoint.DeactivatedEdges), stateKeyWorkflowOutput: checkpoint.Output,
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

// publishCheckpoint 将并行分支的增量合并进同一持久运行记录。
func (e *workflowExecutor) publishCheckpoint(state hexagon.MapState) error {
	if e.run == nil || e.server == nil || e.server.workflowStore == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ws := e.server.workflowStore
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if e.run.Checkpoint == nil {
		e.run.Checkpoint = &WorkflowCheckpoint{}
	}
	cp := e.run.Checkpoint
	for _, target := range []struct {
		key  string
		dest *map[string]string
	}{
		{stateKeyNodeOutputs, &cp.Outputs}, {stateKeyNodeHandoffs, &cp.Handoffs},
		{stateKeyDeadNodes, &cp.DeadNodes}, {stateKeyDeactivatedEdges, &cp.DeactivatedEdges},
	} {
		if *target.dest == nil {
			*target.dest = map[string]string{}
		}
		for key, value := range stringMapStateValue(state, target.key) {
			(*target.dest)[key] = value
		}
	}
	if output := stringStateValue(state, stateKeyWorkflowOutput); output != "" {
		cp.Output = output
	}
	results := make([]WorkflowNodeRun, 0, len(e.nodes))
	for id, node := range e.nodes {
		if result := e.nodeRuns[id]; result != nil {
			copy := *result
			copy.Branches = append([]WorkflowBranchRun(nil), result.Branches...)
			copy.ResourceReceipts = cloneStepReceipts(result.ResourceReceipts)
			results = append(results, copy)
		} else {
			results = append(results, WorkflowNodeRun{NodeID: id, Type: node.Type, Label: node.Label, Status: nodeStatusPending})
		}
	}
	sort.Slice(results, func(i, j int) bool { return e.order[results[i].NodeID] < e.order[results[j].NodeID] })
	e.run.NodeResults = results
	ws.runs[e.run.ID] = e.run
	return ws.persistRuns()
}

func (s *Server) launchWorkflow(wf *WorkflowData, req RunWorkflowRequest, prior *WorkflowRun) (*WorkflowRun, error) {
	if err := validateWorkflowImportReview(wf); err != nil {
		return nil, err
	}
	if req.SessionID == "" {
		req.SessionID = req.Metadata["session_id"]
	}
	ws := s.workflowStore
	ws.mu.Lock()
	if req.IdempotencyKey != "" {
		identity, _ := json.Marshal([]string{wf.ID, req.UserID, req.Platform, req.InstanceID, req.ChatID, req.IdempotencyKey})
		sum := sha256.Sum256(identity)
		key := hex.EncodeToString(sum[:])
		if existing := ws.latestRunByTriggerLocked(key); existing != nil {
			result := cloneWorkflowRun(existing)
			ws.mu.Unlock()
			return result, nil
		}
	}
	var resources map[string]map[string]any
	var resourceErr error
	if prior != nil {
		resources = cloneWorkflowRun(prior).ResourceSnapshots
	} else {
		resources, resourceErr = s.prepareWorkflowResources(wf)
	}
	if resourceErr != nil {
		ws.mu.Unlock()
		if errors.Is(resourceErr, errWorkflowConfiguration) {
			return nil, resourceErr
		}
		return nil, fmt.Errorf("%w: %v", errWorkflowResourcePreparation, resourceErr)
	}
	timeout, resourceErr := workflowRunTimeout(wf)
	if resourceErr != nil {
		ws.mu.Unlock()
		return nil, resourceErr
	}
	run := s.newWorkflowRun(wf, req.Input, prior)
	run.ResourceSnapshots = resources
	run.Request = &req
	run.Checkpoint = &WorkflowCheckpoint{}
	if prior != nil {
		run.PriorRunID = prior.ID
		run.Checkpoint = cloneWorkflowRun(prior).Checkpoint
		run.NodeResults = cloneWorkflowRun(prior).NodeResults
		run.ArtifactSnapshots = cloneWorkflowRun(prior).ArtifactSnapshots
	}
	if req.IdempotencyKey != "" {
		identity, _ := json.Marshal([]string{wf.ID, req.UserID, req.Platform, req.InstanceID, req.ChatID, req.IdempotencyKey})
		sum := sha256.Sum256(identity)
		run.TriggerKey = hex.EncodeToString(sum[:])
	}
	if err := ws.addRun(run); err != nil {
		ws.mu.Unlock()
		return nil, fmt.Errorf("persist workflow run: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	if ws.cancels == nil {
		ws.cancels = map[string]context.CancelFunc{}
	}
	ws.cancels[run.ID] = cancel
	snapshot := cloneWorkflowRun(run)
	ws.mu.Unlock()
	go func() {
		defer cancel()
		defer func() { ws.mu.Lock(); delete(ws.cancels, run.ID); ws.mu.Unlock() }()
		s.executeWorkflow(ctx, run.Definition, run, req)
	}()
	return snapshot, nil
}

func (s *Server) handleListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	workflowID := firstNonEmpty(r.PathValue("id"), r.URL.Query().Get("workflow_id"))
	s.workflowStore.mu.RLock()
	runs := make([]*WorkflowRun, 0)
	for _, run := range s.workflowStore.runs {
		if !run.HistoryDeleted && (workflowID == "" || run.WorkflowID == workflowID) {
			runs = append(runs, workflowRunPublic(run))
		}
	}
	s.workflowStore.mu.RUnlock()
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleCancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	ws := s.workflowStore
	ws.mu.RLock()
	run, ok := ws.runs[r.PathValue("id")]
	cancel := ws.cancels[r.PathValue("id")]
	var snapshot *WorkflowRun
	if ok && !run.HistoryDeleted {
		snapshot = cloneWorkflowRun(run)
	} else {
		ok = false
	}
	ws.mu.RUnlock()
	if !ok {
		writeAPIError(w, 404, "NOT_FOUND", "Workflow run not found")
		return
	}
	if cancel != nil {
		cancel()
	}
	writeJSON(w, http.StatusOK, workflowRunPublic(snapshot))
}

// 删除只隐藏已确认结束的历史，原执行及触发身份继续由同一文件持有。
func (s *Server) handleDeleteWorkflowRun(w http.ResponseWriter, r *http.Request) {
	ws := s.workflowStore
	ws.mu.Lock()
	defer ws.mu.Unlock()
	run := ws.runs[r.PathValue("id")]
	if run == nil || run.HistoryDeleted {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "Workflow run not found")
		return
	}
	confirmed := run.Status == "completed" || run.Status == "failed" || run.Status == "cancelled" || run.Status == "interrupted"
	if !confirmed || ws.cancels[run.ID] != nil || workflowRunHasUnknownOutcome(run) {
		writeAPIError(w, http.StatusConflict, "WORKFLOW_RUN_NOT_DELETABLE", "Wait for the workflow run and its external operations to finish before deleting it")
		return
	}
	run.HistoryDeleted = true
	if err := ws.persistRuns(); err != nil {
		run.HistoryDeleted = false
		writeAPIError(w, http.StatusInternalServerError, "WORKFLOW_RUN_DELETE_FAILED", "Workflow run could not be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func workflowRunHasUnknownOutcome(run *WorkflowRun) bool {
	for _, file := range run.GeneratedFiles {
		if file.Status == "pending" || file.Status == "rendering" {
			return true
		}
	}
	for _, delivery := range run.Deliveries {
		if delivery.Kind == "email" && delivery.Status == "accepted" {
			continue
		}
		if delivery.Status != "delivered" && delivery.Status != "failed" {
			return true
		}
	}
	for _, node := range run.NodeResults {
		if node.OutcomeUnknown || node.Status == "outcome_unknown" || node.Status == nodeStatusRunning {
			return true
		}
		for _, branch := range node.Branches {
			if branch.OutcomeUnknown || branch.Status == "outcome_unknown" || branch.Status == nodeStatusRunning {
				return true
			}
		}
	}
	return false
}

func workflowRetrySafe(run *WorkflowRun) bool {
	for _, node := range run.NodeResults {
		for _, receipt := range node.ResourceReceipts {
			if receipt != nil && receipt.RetryRequiresReview {
				return false
			}
		}
		if node.OutcomeUnknown {
			return false
		}
		if !k12WorkflowExternalNode(node.Type) {
			continue
		}
		if node.Status == nodeStatusRunning || (node.Status == nodeStatusFailed && uncertainWorkflowError(node.Error)) {
			if node.Type == "parallel" && len(node.Branches) > 0 {
				safe := true
				for _, branch := range node.Branches {
					if branch.Status == nodeStatusRunning || uncertainWorkflowError(branch.Error) {
						safe = false
					}
				}
				if safe {
					continue
				}
			}
			return false
		}
	}
	return true
}

func uncertainWorkflowError(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "context canceled") || strings.Contains(message, "deadline exceeded") || strings.Contains(message, "outcome_unknown") || strings.Contains(message, "connection reset") || strings.Contains(message, "unexpected eof")
}
