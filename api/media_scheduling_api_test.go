package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/toolkit/blobstore"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/secret"
	"github.com/hexagon-codes/hexclaw/skill"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

type reviewSchedulingAPI struct {
	app   *httptest.Server
	store *sqlitestore.Store
}

type reviewHTTPResult struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func reviewSchedulingFixture(t *testing.T, endpoint string) *reviewSchedulingAPI {
	t.Helper()
	root := t.TempDir()
	store, err := sqlitefixture.New(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobs, err := blobstore.NewStore(filepath.Join(root, "generated"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "review-scheduling-fixture"
	cfg.Compaction.Enabled = false
	cfg.FileMemory.Enabled = false
	cfg.LLM.Cache.Enabled = false
	cfg.LLM.Default = "gateway"
	cfg.Skill.Builtin.MediaGen = false
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"gateway": {
		APIKey: "fixture-provider-key", Compatible: "openai", BaseURL: endpoint,
		ModelSpecsMode: config.LLMModelSpecsModeExplicit,
		ModelSpecs: []config.LLMProviderModelSpec{
			{ID: "drawing-model", Capabilities: []string{config.LLMModelCapabilityImageGeneration}},
			{ID: "cogvideox-2", Capabilities: []string{config.LLMModelCapabilityVideoGeneration}},
		},
	}}
	router := mediaflow.NewRouter()
	router.Reload(cfg, nil)
	codec, err := secret.LoadBox(root)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := mediaflow.NewService(t.Context(), router, store.MediaRepository(), blobs, filepath.Join(root, "staging"), mediaflow.WithRecoveryCodec(codec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.Close)
	textRouter, err := llmrouter.New(cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.NewReActEngine(cfg, textRouter, store, skill.NewRegistry())
	eng.SetMediaFlow(flow)
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	server := NewServer(cfg, eng, nil, store)
	server.SetGenStore(blobs)
	server.SetMediaFlow(flow)
	app := httptest.NewServer(server.routes())
	t.Cleanup(app.Close)
	return &reviewSchedulingAPI{app: app, store: store}
}

func (f *reviewSchedulingAPI) call(ctx context.Context, method, path, key, body string) reviewHTTPResult {
	req, err := http.NewRequestWithContext(ctx, method, f.app.URL+path, strings.NewReader(body))
	if err != nil {
		return reviewHTTPResult{Error: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer review-scheduling-fixture")
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := f.app.Client().Do(req)
	if err != nil {
		return reviewHTTPResult{Error: err.Error()}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return reviewHTTPResult{Status: response.StatusCode, Error: err.Error()}
	}
	return reviewHTTPResult{Status: response.StatusCode, Body: data}
}

func (f *reviewSchedulingAPI) operationID(t *testing.T, requestID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var id string
		err := f.store.DB().QueryRowContext(ctx, `SELECT id FROM media_operations WHERE json_extract(data,'$.request.origin.request_id')=?`, requestID).Scan(&id)
		if err == nil {
			return id
		}
		select {
		case <-ctx.Done():
			t.Fatalf("operation %s missing: %v", requestID, err)
		case <-ticker.C:
		}
	}
}

func reviewWriteEvidence(t *testing.T, name string, value any) {
	t.Helper()
	reviewSchedulingEvidence := os.Getenv("MEDIA_SCHEDULING_EVIDENCE_DIR")
	if reviewSchedulingEvidence == "" {
		reviewSchedulingEvidence = t.TempDir()
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reviewSchedulingEvidence, name+".json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence=%s.json", name)
}

func reviewPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func reviewGeneratedFile(t *testing.T, f *reviewSchedulingAPI, result reviewHTTPResult) map[string]any {
	t.Helper()
	if result.Status != 200 || result.Error != "" {
		t.Fatalf("generation result: %+v", result)
	}
	var body struct {
		Images []struct {
			Path string `json:"file_path"`
		} `json:"images"`
	}
	if err := json.Unmarshal(result.Body, &body); err != nil || len(body.Images) != 1 || body.Images[0].Path == "" {
		t.Fatalf("invalid saved result: %s %v", result.Body, err)
	}
	file := f.call(t.Context(), "GET", "/api/v1/files/generated/"+body.Images[0].Path, "", "")
	_, err := png.Decode(bytes.NewReader(file.Body))
	if file.Status != 200 || err != nil {
		t.Fatalf("generated file not actual PNG: %d %v", file.Status, err)
	}
	return map[string]any{"http_status": file.Status, "bytes": len(file.Body), "png_decoded": true, "file_path": body.Images[0].Path}
}

func TestMediaHTTPDownloadCancellationReleasesExecutionSlots(t *testing.T) {
	data := reviewPNG(t)
	entered := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var calls atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/stalled/") {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data[:8])
			w.(http.Flusher).Flush()
			entered <- r.URL.Path
			select {
			case <-release:
				_, _ = w.Write(data[8:])
			case <-r.Context().Done():
			}
			return
		}
		if r.URL.Path != "/images/generations" {
			w.WriteHeader(404)
			return
		}
		calls.Add(1)
		var req struct {
			Prompt string `json:"prompt"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Prompt == "after stalls" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"url": upstream.URL + "/stalled/" + req.Prompt}}})
	}))
	defer upstream.Close()
	defer unblock()
	f := reviewSchedulingFixture(t, upstream.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	firstResults := make(chan reviewHTTPResult, 2)
	for _, key := range []string{"stall-first", "stall-second"} {
		go func(key string) {
			firstResults <- f.call(ctx, "POST", "/api/v1/images/generate", key, fmt.Sprintf(`{"prompt":%q,"model":"drawing-model"}`, key))
		}(key)
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("downloads did not enter", ctx.Err())
		}
	}
	third := make(chan reviewHTTPResult, 1)
	go func() {
		third <- f.call(ctx, "POST", "/api/v1/images/generate", "after-stalls", `{"prompt":"after stalls","model":"drawing-model"}`)
	}()
	id := f.operationID(t, "after-stalls")
	cancellation := []reviewHTTPResult{}
	for _, key := range []string{"stall-first", "stall-second"} {
		operation := f.operationID(t, key)
		cancellation = append(cancellation, f.call(ctx, "POST", "/api/v1/media/operations/"+operation+"/cancel", "", "{}"))
	}
	start := time.Now()
	window := time.NewTimer(6500 * time.Millisecond)
	defer window.Stop()
	var premature *reviewHTTPResult
	select {
	case result := <-third:
		premature = &result
	case <-window.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	blocked := f.call(ctx, "GET", "/api/v1/media/operations/"+id, "", "")
	var blockedView map[string]any
	_ = json.Unmarshal(blocked.Body, &blockedView)
	beforeRelease := calls.Load()
	unblock()
	var result reviewHTTPResult
	if premature != nil {
		result = *premature
	} else {
		select {
		case result = <-third:
		case <-ctx.Done():
			t.Fatal("request did not recover after CDN release", ctx.Err())
		}
	}
	artifact := reviewGeneratedFile(t, f, result)
	initial := []reviewHTTPResult{}
	for range 2 {
		select {
		case r := <-firstResults:
			initial = append(initial, r)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	reproduced := premature == nil && blocked.Status == 200 && blockedView["generation_state"] == "queued" && beforeRelease == 2 && calls.Load() == 3
	reviewWriteEvidence(t, "issue-4", map[string]any{"issue": 4, "defect_reproduced": reproduced, "entry": "real HTTP API -> media service -> real SQLite/Blobstore -> controlled HTTP generation and CDN", "observation_ms": time.Since(start).Milliseconds(), "stalled_downloads": 2, "cancellation_responses": cancellation, "blocked_operation": blockedView, "upstream_generation_calls_before_release": beforeRelease, "upstream_generation_calls_after_release": calls.Load(), "initial_request_results": initial, "third_result": result, "artifact": artifact, "cleanup": "CDN released; all HTTP requests completed; isolated services closed by cleanup", "limit": "Observation proves blocked progress until CDN release, not an infinite-duration empirical measurement."})
	if reproduced {
		t.Fatal("queued media made no progress after cancelling two stalled downloads; releasing CDN immediately restored the same request")
	}
}

func TestMediaHTTPOldPollingFailuresDoNotStarveNewRequests(t *testing.T) {
	data := reviewPNG(t)
	var videoSubmits, imageCalls, pollCalls atomic.Int32
	var resolveOld atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/videos/generations":
			id := videoSubmits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("old-task-%d", id), "task_status": "PROCESSING"})
		case strings.HasPrefix(r.URL.Path, "/async-result/"):
			pollCalls.Add(1)
			if !resolveOld.Load() {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"task_status": "FAIL"})
		case r.URL.Path == "/images/generations":
			imageCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	f := reviewSchedulingFixture(t, upstream.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	submissions := []reviewHTTPResult{}
	for index := range 16 {
		result := f.call(ctx, "POST", "/api/v1/videos/generate", fmt.Sprintf("old-video-%d", index), `{"prompt":"small square moving","model":"cogvideox-2"}`)
		if result.Status != 200 || result.Error != "" {
			t.Fatalf("history preparation request %d: %+v", index, result)
		}
		submissions = append(submissions, result)
	}
	var oldRunning int
	if err := f.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM media_operations WHERE generation_state='running'`).Scan(&oldRunning); err != nil {
		t.Fatal(err)
	}
	third := make(chan reviewHTTPResult, 1)
	go func() {
		third <- f.call(ctx, "POST", "/api/v1/images/generate", "new-after-old-videos", `{"prompt":"fresh square","model":"drawing-model"}`)
	}()
	id := f.operationID(t, "new-after-old-videos")
	pollsBefore := pollCalls.Load()
	start := time.Now()
	window := time.NewTimer(6500 * time.Millisecond)
	defer window.Stop()
	var premature *reviewHTTPResult
	select {
	case result := <-third:
		premature = &result
	case <-window.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	blocked := f.call(ctx, "GET", "/api/v1/media/operations/"+id, "", "")
	var blockedView map[string]any
	_ = json.Unmarshal(blocked.Body, &blockedView)
	beforeResolve := imageCalls.Load()
	pollsAtObservation := pollCalls.Load()
	observation := time.Since(start).Milliseconds()
	resolveOld.Store(true)
	var result reviewHTTPResult
	if premature != nil {
		result = *premature
	} else {
		select {
		case result = <-third:
		case <-ctx.Done():
			t.Fatal("new request did not recover after old polls became terminal", ctx.Err())
		}
	}
	artifact := reviewGeneratedFile(t, f, result)
	reproduced := oldRunning == 16 && premature == nil && blocked.Status == 200 && blockedView["generation_state"] == "queued" && beforeResolve == 0 && imageCalls.Load() == 1
	reviewWriteEvidence(t, "issue-5", map[string]any{"issue": 5, "defect_reproduced": reproduced, "entry": "real HTTP API; 16 historical video submissions made sequentially through production handlers, then one fresh image API request", "historical_submission_responses": submissions, "old_running_count": oldRunning, "video_submit_count": videoSubmits.Load(), "old_poll_calls_before_observation": pollsBefore, "old_poll_calls_at_observation": pollsAtObservation, "observation_ms": observation, "new_generation_calls_while_blocked": beforeResolve, "new_generation_calls_after_old_terminal": imageCalls.Load(), "blocked_operation": blockedView, "recovered_result": result, "artifact": artifact, "cleanup": "Controlled upstream returned terminal old tasks, fresh image completed; isolated services closed by cleanup", "limit": "No concurrent load; two existing execution slots and sequential historical setup. Controlled upstream, no real provider or billing."})
	if reproduced {
		t.Fatal("16 older polling failures occupied the pending page; fresh image started only after older tasks became terminal")
	}
}
