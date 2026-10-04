package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func historyFixture(t *testing.T, timestamp time.Time, message string) []byte {
	t.Helper()
	data, err := json.Marshal(logFileEntry{
		Timestamp: timestamp.UTC().Format(time.RFC3339Nano),
		Level:     "warn", Source: "trace", Message: message,
	})
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func readHistoryFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLogFileSinkHistoryRetentionStartupPreservesMixedAndUnknownRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hexclaw.log")
	now := time.Now()
	expired := historyFixture(t, now.Add(-8*24*time.Hour), "expired")
	recent := historyFixture(t, now.Add(-time.Hour), "recent")
	unknown := []byte("{\"ts\":\"unknown\",\"msg\":\"unparsed\"}\nraw-unknown-row\n")
	if err := os.WriteFile(path, bytes.Join([][]byte{expired, recent, unknown}, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", expired, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".2", recent, 0o600); err != nil {
		t.Fatal(err)
	}
	sink, err := NewLogFileSink(LogFileSinkConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if sink.maxAge != 7*24*time.Hour || sink.maxSize != 10*1024*1024 || sink.maxFiles != 100 {
		t.Fatalf("default retention changed: age=%v size=%d files=%d", sink.maxAge, sink.maxSize, sink.maxFiles)
	}
	if got := readHistoryFixtureFile(t, path); !bytes.Equal(got, append(recent, unknown...)) {
		t.Fatalf("mixed active file was not precisely retained: %q", got)
	}
	if _, err := os.Stat(path + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired history file remains: %v", err)
	}
	if got := readHistoryFixtureFile(t, path+".2"); !bytes.Equal(got, recent) {
		t.Fatal("unexpired historical row changed")
	}
	sink.Write(LogEntry{ID: "after-cleanup", Timestamp: now.UTC().Format(time.RFC3339Nano), Level: "info", Message: "new"})
	entries, total, err := sink.QueryHistory(context.Background(), LogHistoryQuery{Limit: 10})
	if err != nil || total != 3 || len(entries) != 3 || entries[0].ID != "after-cleanup" {
		t.Fatalf("replacement lost active writer or duplicates: total=%d rows=%v err=%v", total, entries, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || info.Size() != sink.size {
		t.Fatalf("replacement changed file metadata: info=%v size=%d err=%v", info, sink.size, err)
	}
}

func TestLogFileSinkHistoryRetentionRunsOnWriteAndQuery(t *testing.T) {
	for _, maintenance := range []string{"write", "query"} {
		t.Run(maintenance, func(t *testing.T) {
			sink, err := NewLogFileSink(LogFileSinkConfig{Dir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer sink.Close()
			now := time.Now()
			sink.Write(LogEntry{Timestamp: now.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano), Level: "info", Message: "expired"})
			sink.Write(LogEntry{Timestamp: now.Add(-time.Hour).Format(time.RFC3339Nano), Level: "info", Message: "kept"})
			if !bytes.Contains(readHistoryFixtureFile(t, sink.Path()), []byte("expired")) {
				t.Fatal("maintenance ran before its hourly interval")
			}
			// 控制维护时钟，不等待一小时，也不改变生产时钟来源。
			sink.lastCleanup = now.Add(-2 * time.Hour)
			if maintenance == "write" {
				sink.Write(LogEntry{Timestamp: now.Format(time.RFC3339Nano), Level: "info", Message: "after"})
			} else if _, _, err := sink.QueryHistory(context.Background(), LogHistoryQuery{Limit: 10}); err != nil {
				t.Fatal(err)
			}
			data := readHistoryFixtureFile(t, sink.Path())
			if bytes.Contains(data, []byte("expired")) || !bytes.Contains(data, []byte("kept")) {
				t.Fatalf("hourly %s maintenance did not retain the right rows: %s", maintenance, data)
			}
		})
	}
}

func TestLogFileSinkHistoryCapacityAndNumericRotationOrder(t *testing.T) {
	sink, err := NewLogFileSink(LogFileSinkConfig{Dir: t.TempDir(), MaxSize: 100, MaxFiles: 12})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 1; i <= 14; i++ {
		sink.Write(LogEntry{ID: fmt.Sprint(i), Timestamp: now, Level: "info", Message: strings.Repeat("padding", 20)})
	}
	entries, total, err := sink.QueryHistory(context.Background(), LogHistoryQuery{Limit: 20})
	if err != nil || total != 12 || len(entries) != 12 {
		t.Fatalf("capacity mismatch: total=%d rows=%d err=%v", total, len(entries), err)
	}
	for i, entry := range entries {
		if entry.ID != fmt.Sprint(14-i) {
			t.Fatalf("numeric rotation ordering mismatch at %d: %s", i, entry.ID)
		}
	}
	if _, err := os.Stat(sink.Path() + ".13"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history exceeded configured capacity: %v", err)
	}
}

func TestLogFileSinkHistoryPreservesNewAndLegacyIdentityAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hexclaw.log")
	now := time.Now().Add(-time.Hour)
	legacy := historyFixture(t, now, "same legacy row")
	legacy = bytes.Replace(legacy, []byte("}\n"), []byte(",\"fields\":{\"nested\":{\"count\":9007199254740993}}}\n"), 1)
	if err := os.WriteFile(path, bytes.Join([][]byte{legacy, legacy}, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	sink, err := NewLogFileSink(LogFileSinkConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	newEntry := LogEntry{ID: "new-id", Timestamp: now.Add(time.Minute).UTC().Format(time.RFC3339Nano),
		Level: "warn", Source: "trace", Domain: "integration", Message: "new row",
		Fields: map[string]any{"retry_attempt": json.Number("2"), "large_integer": json.Number("9007199254740993"), "error": "unavailable"}, TraceID: "trace-new"}
	sink.Write(newEntry)
	before, total, err := sink.QueryHistory(context.Background(), LogHistoryQuery{Limit: 10})
	if err != nil || total != 4 || len(before) != 4 {
		t.Fatalf("new and legacy rows lost: total=%d rows=%d err=%v", total, len(before), err)
	}
	if !reflect.DeepEqual(before[0], newEntry) {
		t.Fatalf("new structured fields not preserved: %#v", before[0])
	}
	ids := map[string]bool{}
	for _, entry := range before {
		if ids[entry.ID] {
			t.Fatalf("legitimate duplicate collapsed: %s", entry.ID)
		}
		ids[entry.ID] = true
	}
	if before[1].Domain != "engine" || before[1].Timestamp != now.UTC().Format(time.RFC3339Nano) {
		t.Fatal("legacy row timestamp/domain reconstructed incorrectly")
	}
	for range 2 {
		sink.Write(LogEntry{Timestamp: now.UTC().Format(time.RFC3339Nano), Level: "warn", Source: "trace", Message: "same legacy row",
			Fields: map[string]any{"nested": map[string]any{"count": json.Number("9007199254740993")}}})
	}
	appended, total, err := sink.QueryHistory(context.Background(), LogHistoryQuery{Limit: 10})
	if err != nil || total != 6 || len(appended) != 6 || !reflect.DeepEqual(appended[2:], before) {
		t.Fatalf("new empty-ID writes changed retained identities: total=%d rows=%#v err=%v", total, appended, err)
	}
	if appended[0].ID == "" || appended[0].ID == appended[1].ID || strings.HasPrefix(appended[0].ID, "legacy-") {
		t.Fatal("new empty-ID writes did not persist distinct identities")
	}
	before = appended
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLogFileSink(LogFileSinkConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, total, err := reopened.QueryHistory(context.Background(), LogHistoryQuery{Limit: 10})
	if err != nil || total != 6 || !reflect.DeepEqual(after, before) {
		t.Fatalf("history changed across restart: total=%d after=%#v err=%v", total, after, err)
	}
	page, total, err := reopened.QueryHistory(context.Background(), LogHistoryQuery{Limit: 1, Offset: 2})
	if err != nil || total != 6 || len(page) != 1 || !reflect.DeepEqual(page[0], before[2]) {
		t.Fatalf("pagination renumbered legacy identities: total=%d page=%#v err=%v", total, page, err)
	}
	for _, item := range []struct{ offset, index int }{{2, 3}, {4, 5}} {
		filtered, total, err := reopened.QueryHistory(context.Background(), LogHistoryQuery{
			Keyword: "same legacy row", Limit: 1, Offset: item.offset,
		})
		if err != nil || total != 5 || len(filtered) != 1 || !reflect.DeepEqual(filtered[0], before[item.index]) {
			t.Fatalf("filtered page changed retained identity: offset=%d total=%d rows=%#v err=%v", item.offset, total, filtered, err)
		}
	}
}

func TestLogFileSinkHistorySnapshotSurvivesAppendRotationAndCancellation(t *testing.T) {
	sink, err := NewLogFileSink(LogFileSinkConfig{Dir: t.TempDir(), MaxSize: 1000, MaxFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sink.Write(LogEntry{ID: "before", Timestamp: now, Level: "info", Message: "before"})
	reader, err := openLogFileReader(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	info, err := reader.Stat()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := logFileSnapshot{file: reader, size: info.Size()}
	sink.Write(LogEntry{ID: "after", Timestamp: now, Level: "info", Message: strings.Repeat("x", 1000)})
	data, err := readLogSnapshot(context.Background(), snapshot)
	if err != nil || !bytes.Contains(data, []byte("before")) || bytes.Contains(data, []byte("after")) {
		t.Fatalf("fixed-length snapshot changed during rotation: %s %v", data, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := sink.QueryHistory(ctx, LogHistoryQuery{Limit: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("query ignored cancellation: %v", err)
	}
	if _, err := readLogSnapshot(ctx, snapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("snapshot reader ignored cancellation: %v", err)
	}
}

func TestHandleGetLogsHistoryFilteringPaginationAndRealtimeIsolation(t *testing.T) {
	sink, err := NewLogFileSink(LogFileSinkConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	now := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		sink.Write(LogEntry{ID: fmt.Sprint(i), Timestamp: now.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Level: "warn", Source: "dingtalk", Domain: "integration", Message: "RETRY unavailable"})
	}
	collector := NewLogCollector(2)
	AttachToCollector(collector, sink)
	collector.Add("info", "system", "live only", nil)
	stream, subID := collector.subscribe()
	defer collector.unsubscribe(subID)
	server := &Server{logCollector: collector}
	beforeVersion := collector.Version()
	params := url.Values{"start": {now.Add(time.Second).Format(time.RFC3339Nano)}, "end": {now.Add(4 * time.Second).Format(time.RFC3339Nano)},
		"level": {"warn"}, "source": {"dingtalk"}, "domain": {"integration"}, "keyword": {"retry"}, "limit": {"2"}, "offset": {"1"}}
	response := httptest.NewRecorder()
	server.handleGetLogs(response, httptest.NewRequest("GET", "/api/v1/logs?"+params.Encode(), nil))
	var result logsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Total != 4 || len(result.Logs) != 2 || result.Logs[0].ID != "3" || result.Logs[1].ID != "2" {
		t.Fatalf("inclusive range/filter/page mismatch: status=%d result=%#v", response.Code, result)
	}
	if collector.Version() != beforeVersion || collector.Total() != 1 || collector.Stats().ByLevel["warn"] != 0 {
		t.Fatal("history query was reinjected into realtime collector")
	}
	select {
	case entry := <-stream:
		t.Fatalf("history query broadcast an old entry: %#v", entry)
	default:
	}
	response = httptest.NewRecorder()
	server.handleGetLogs(response, httptest.NewRequest("GET", "/api/v1/logs", nil))
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Total != 1 || len(result.Logs) != 1 || result.Logs[0].Message != "live only" || result.Logs[0].Fields != nil {
		t.Fatalf("ordinary memory query changed: %#v", result)
	}
	for _, request := range []string{"history=true", "start=", "end="} {
		response = httptest.NewRecorder()
		server.handleGetLogs(response, httptest.NewRequest("GET", "/api/v1/logs?"+request, nil))
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || result.Total != 7 {
			t.Fatalf("explicit history mode %s failed: %#v status=%d", request, result, response.Code)
		}
	}
}

func TestHandleGetLogsHistoryRejectsInvalidRangesAndUnavailableStorage(t *testing.T) {
	server := &Server{logCollector: NewLogCollector(10)}
	for _, query := range []string{"start=invalid", "end=invalid", "start=2026-10-04T00:00:01Z&end=2026-10-04T00:00:00Z"} {
		response := httptest.NewRecorder()
		server.handleGetLogs(response, httptest.NewRequest("GET", "/api/v1/logs?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid range accepted: %s status=%d", query, response.Code)
		}
	}
	response := httptest.NewRecorder()
	server.handleGetLogs(response, httptest.NewRequest("GET", "/api/v1/logs?history=true", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable history disguised as empty realtime success: %d", response.Code)
	}
	sink, err := NewLogFileSink(LogFileSinkConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sink.Close()
	AttachToCollector(server.logCollector, sink)
	if err := os.Remove(sink.Path()); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	server.handleGetLogs(response, httptest.NewRequest("GET", "/api/v1/logs?history=true", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("history I/O error disguised as success: %d", response.Code)
	}
}
