package mediaflow_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
)

func TestMediaShortSpeechCompletesWhileIndependentImageIsRunning(t *testing.T) {
	imageData := pngData(t)
	audioData, err := os.ReadFile("testdata/short-audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(imageData)}}})
	}))
	defer upstream.Close()
	defer unblock()
	cfg := mediaConfig(upstream.URL)
	cfg.Voice.Enabled = true
	cfg.Voice.TTS.Provider = "fixture-tts"
	svc, router, _, _ := newMedia(t, cfg)
	speech := &fixtureSpeech{audio: audioData}
	router.Reload(cfg, voice.NewService(nil, speech))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	imageOp, err := svc.Begin(ctx, request("blocked-image"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("image did not enter the provider")
	}
	speechStarted := time.Now()
	audioOp, err := svc.Begin(ctx, mediaflow.Request{
		Origin: mediaflow.Origin{Owner: "owner", Kind: "direct_api", RequestID: "short-independent-speech"},
		Kind:   "audio", Action: "synthesize", Speech: &mediaflow.SpeechRequest{Text: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	audioOp, err = svc.Wait(ctx, "owner", audioOp.ID, false)
	if err != nil || audioOp.SaveState != "ready" || len(audioOp.Assets) != 1 || speech.calls.Load() != 1 {
		t.Fatalf("short speech waited for unrelated image: operation=%+v calls=%d err=%v", audioOp, speech.calls.Load(), err)
	}
	t.Logf("speech became ready while image was blocked in %d ms", time.Since(speechStarted).Milliseconds())
	imageOp, err = svc.Read(ctx, "owner", imageOp.ID)
	if err != nil || imageOp.GenerationState != "submitting" || calls.Load() != 1 {
		t.Fatalf("active image was reclaimed or classified unknown: operation=%+v calls=%d err=%v", imageOp, calls.Load(), err)
	}
	unblock()
	imageOp, err = svc.Wait(ctx, "owner", imageOp.ID, false)
	if err != nil || imageOp.SaveState != "ready" || len(imageOp.Assets) != 1 || calls.Load() != 1 || speech.calls.Load() != 1 {
		t.Fatalf("independent outputs did not settle exactly once: image=%+v calls=%d speech=%d err=%v", imageOp, calls.Load(), speech.calls.Load(), err)
	}
}
