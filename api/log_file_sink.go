package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hexagon-codes/toolkit/util/idgen"
)

// LogFileSink 日志文件持久化 (JSONL + 轮转)
//
// 业界标准做法：运行时日志写入 JSONL 文件，单文件上限后轮转，
// 保留最近 N 份历史文件。用户可提交日志文件反馈 Bug。
//
// 文件路径: ~/.hexclaw/logs/hexclaw.log
// 轮转规则: 默认单文件 10 MiB，保留 100 份历史及当前文件，条目保留 7 天。
// 格式: 每行一条 JSON。
type LogFileSink struct {
	mu          sync.Mutex
	file        *os.File
	path        string
	size        int64
	maxSize     int64 // 单文件最大字节
	maxFiles    int   // 保留历史文件数
	maxAge      time.Duration
	lastCleanup time.Time
}

// LogFileSinkConfig 日志文件配置
type LogFileSinkConfig struct {
	Dir      string        // 日志目录 (默认 ~/.hexclaw/logs)
	FileName string        // 日志文件名 (默认 hexclaw.log)
	MaxSize  int64         // 单文件最大字节 (默认 10MB)
	MaxFiles int           // 保留历史文件数 (默认 100)
	MaxAge   time.Duration // 条目保留时长 (默认 7 天)
}

// NewLogFileSink 创建日志文件写入器
func NewLogFileSink(cfg LogFileSinkConfig) (*LogFileSink, error) {
	if cfg.Dir == "" {
		home, _ := os.UserHomeDir()
		cfg.Dir = filepath.Join(home, ".hexclaw", "logs")
	}
	if cfg.FileName == "" {
		cfg.FileName = "hexclaw.log"
	}
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = 10 * 1024 * 1024 // 10 MB
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = 100
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 7 * 24 * time.Hour
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	path := filepath.Join(cfg.Dir, cfg.FileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	info, _ := f.Stat()
	size := int64(0)
	if info != nil {
		size = info.Size()
	}

	sink := &LogFileSink{
		file:     f,
		path:     path,
		size:     size,
		maxSize:  cfg.MaxSize,
		maxFiles: cfg.MaxFiles,
		maxAge:   cfg.MaxAge,
	}
	if err := sink.cleanupExpiredLocked(time.Now()); err != nil {
		sink.Close()
		return nil, fmt.Errorf("clean up expired logs: %w", err)
	}
	return sink, nil
}

// logFileEntry JSON Lines 日志条目
type logFileEntry struct {
	ID        string         `json:"id,omitempty"`
	Timestamp string         `json:"ts"`
	Level     string         `json:"level"`
	Source    string         `json:"source,omitempty"`
	Domain    string         `json:"domain,omitempty"`
	Message   string         `json:"msg"`
	Fields    map[string]any `json:"fields,omitempty"`
	TraceID   string         `json:"trace_id,omitempty"`
}

// Write 写入一条日志到文件。线程安全。
func (s *LogFileSink) Write(entry LogEntry) {
	// 新写入的记录始终持久化身份，旧格式的内容序号只用于兼容回读。
	if entry.ID == "" {
		entry.ID = idgen.ShortID()
	}
	data, err := json.Marshal(logFileEntry{
		ID:        entry.ID,
		Timestamp: entry.Timestamp,
		Level:     entry.Level,
		Source:    entry.Source,
		Domain:    entry.Domain,
		Message:   entry.Message,
		Fields:    entry.Fields,
		TraceID:   entry.TraceID,
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file == nil {
		return
	}
	// 保留维护不增加后台任务，正常写入和历史查询共用同一个周期。
	_ = s.cleanupExpiredLocked(time.Now())
	if s.file == nil {
		return
	}

	n, err := s.file.Write(data)
	if err != nil {
		return
	}
	s.size += int64(n)

	// 检查是否需要轮转
	if s.size >= s.maxSize {
		s.rotate()
	}
}

// rotate 执行日志轮转
// hexclaw.log → hexclaw.log.1 → hexclaw.log.2 → hexclaw.log.3 (删除)
func (s *LogFileSink) rotate() {
	s.file.Close()

	// 移动历史文件: .3→删, .2→.3, .1→.2, current→.1
	for i := s.maxFiles; i >= 1; i-- {
		src := s.path
		if i > 1 {
			src = fmt.Sprintf("%s.%d", s.path, i-1)
		}
		dst := fmt.Sprintf("%s.%d", s.path, i)

		if i == s.maxFiles {
			os.Remove(dst)
		}
		os.Rename(src, dst)
	}

	// 创建新文件
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		s.file = nil
		return
	}
	s.file = f
	s.size = 0
}

// Close 关闭文件
func (s *LogFileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file != nil {
		err := s.file.Close()
		s.file = nil
		return err
	}
	return nil
}

// Path 返回当前日志文件路径
func (s *LogFileSink) Path() string {
	return s.path
}

// RotatedFiles 返回所有日志文件路径（当前 + 历史），用于导出/上传
func (s *LogFileSink) RotatedFiles() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotatedFilesLocked()
}

func (s *LogFileSink) rotatedFilesLocked() []string {
	files := []string{s.path}
	for i := 1; i <= s.maxFiles; i++ {
		p := fmt.Sprintf("%s.%d", s.path, i)
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	return files
}

// cleanupExpiredLocked 只删除可可靠解析且超过保留期的条目，未知原始行保持不变。
func (s *LogFileSink) cleanupExpiredLocked(now time.Time) error {
	if !s.lastCleanup.IsZero() && now.Sub(s.lastCleanup) < time.Hour {
		return nil
	}
	// 维护失败仍按小时重试，避免单个文件错误使每次日志写入重扫历史。
	s.lastCleanup = now
	cutoff := now.Add(-s.maxAge)
	for _, path := range s.rotatedFilesLocked() {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		kept := make([]byte, 0, len(data))
		changed := false
		for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
			var entry struct {
				Timestamp string `json:"ts"`
			}
			if _, err := decodeLogFileMetadata(line, &entry); err == nil {
				if timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil && timestamp.Before(cutoff) {
					changed = true
					continue
				}
			}
			kept = append(kept, line...)
		}
		if !changed {
			continue
		}
		if path != s.path && len(kept) == 0 {
			if err := os.Remove(path); err != nil {
				return err
			}
			continue
		}
		if err := s.replaceLogFileLocked(path, kept); err != nil {
			return err
		}
	}
	return nil
}

func (s *LogFileSink) replaceLogFileLocked(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hexclaw-log-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	active := path == s.path && s.file != nil
	if active {
		if err := s.file.Close(); err != nil {
			return err
		}
		s.file = nil
	}
	renameErr := os.Rename(tmpPath, path)
	if active {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		s.file = file
		if info, err := file.Stat(); err == nil {
			s.size = info.Size()
		}
	}
	return renameErr
}

type logFileSnapshot struct {
	file *os.File
	size int64
}

type logFilePosition struct {
	snapshot int
	line     int
}

// logFileQueryEntry 延后解码结构化字段，扫描计数不为未返回的日志分配字段对象。
type logFileQueryEntry struct {
	ID        string          `json:"id,omitempty"`
	Timestamp string          `json:"ts"`
	Level     string          `json:"level"`
	Source    string          `json:"source,omitempty"`
	Domain    string          `json:"domain,omitempty"`
	Message   string          `json:"msg"`
	Fields    json.RawMessage `json:"fields,omitempty"`
	TraceID   string          `json:"trace_id,omitempty"`
}

// decodeLogFileMetadata 对既有 JSONL 尾部 fields 保留原始字节，只解析查询元数据。
// 完整校验 fields JSON；其他字段顺序及 trace_id 后置的格式仍由标准解码器读取。
func decodeLogFileMetadata(line []byte, value any) (json.RawMessage, error) {
	line = bytes.TrimSpace(line)
	marker := []byte(`,"fields":`)
	if position := bytes.Index(line, marker); position > 0 && len(line) > 0 && line[len(line)-1] == '}' {
		fields := line[position+len(marker) : len(line)-1]
		if json.Valid(fields) {
			metadata := make([]byte, position+1)
			copy(metadata, line[:position])
			metadata[position] = '}'
			if err := json.Unmarshal(metadata, value); err == nil {
				return fields, nil
			}
		}
	}
	return nil, json.Unmarshal(line, value)
}

// LogHistoryQuery 沿用实时查询的过滤与分页，并以含端点的时间范围查询保留期内日志。
type LogHistoryQuery struct {
	Level, Source, Domain, Keyword string
	Start, End                     time.Time
	Limit, Offset                  int
}

// QueryHistory 固定文件句柄和长度后释放写锁；轮转和清理不改变本次读取快照。
func (s *LogFileSink) QueryHistory(ctx context.Context, query LogHistoryQuery) ([]LogEntry, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	now := time.Now()
	s.mu.Lock()
	if err := s.cleanupExpiredLocked(now); err != nil {
		s.mu.Unlock()
		return nil, 0, err
	}
	var snapshots []logFileSnapshot
	for _, path := range s.rotatedFilesLocked() {
		file, err := openLogFileReader(path)
		if err != nil {
			s.mu.Unlock()
			closeLogSnapshots(snapshots)
			return nil, 0, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			s.mu.Unlock()
			closeLogSnapshots(snapshots)
			return nil, 0, err
		}
		snapshots = append(snapshots, logFileSnapshot{file: file, size: info.Size()})
	}
	s.mu.Unlock()
	defer closeLogSnapshots(snapshots)

	cutoff := now.Add(-s.maxAge)
	if !query.Start.IsZero() && query.Start.After(cutoff) {
		cutoff = query.Start
	}
	limit := max(query.Limit, 0)
	offset := max(query.Offset, 0)
	entries := make([]LogEntry, 0, limit)
	total := 0
	legacyOccurrences := make(map[[32]byte]int)
	legacyPositions := make(map[logFilePosition]int)
	legacyPrefixes := make(map[[64]byte]struct{})
	matcher := newKeywordMatcher(query.Keyword)
	for snapshotIndex, snapshot := range snapshots {
		data, err := readLogSnapshot(ctx, snapshot)
		if err != nil {
			return nil, 0, err
		}
		lines := bytes.Split(data, []byte{'\n'})
		for i := len(lines) - 1; i >= 0; i-- {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			var stored logFileQueryEntry
			rawFields, err := decodeLogFileMetadata(lines[i], &stored)
			if err != nil {
				continue
			}
			if rawFields != nil {
				stored.Fields = rawFields
			}
			fieldsJSON := bytes.TrimSpace(stored.Fields)
			if len(fieldsJSON) > 0 && fieldsJSON[0] != '{' && !bytes.Equal(fieldsJSON, []byte("null")) {
				continue
			}
			timestamp, err := time.Parse(time.RFC3339Nano, stored.Timestamp)
			if err != nil || timestamp.Before(cutoff) || (!query.End.IsZero() && timestamp.After(query.End)) {
				continue
			}
			if stored.Domain == "" {
				stored.Domain = inferLogDomain(stored.Source)
			}
			if (query.Level != "" && stored.Level != query.Level) ||
				(query.Source != "" && stored.Source != query.Source) ||
				(query.Domain != "" && stored.Domain != query.Domain) || !matcher.Contains(stored.Message) {
				continue
			}
			total++
			if total <= offset || len(entries) >= limit {
				continue
			}
			// 只保存本页旧记录的身份候选，深分页不保留此前全部记录的摘要。
			if stored.ID == "" {
				hash := sha256.Sum256(lines[i])
				legacyOccurrences[hash] = 0
				legacyPositions[logFilePosition{snapshot: snapshotIndex, line: i}] = len(entries)
				var prefix [64]byte
				copy(prefix[:], lines[i])
				legacyPrefixes[prefix] = struct{}{}
			}
			var fields map[string]any
			if len(fieldsJSON) > 0 {
				decoder := json.NewDecoder(bytes.NewReader(fieldsJSON))
				decoder.UseNumber()
				if err := decoder.Decode(&fields); err != nil {
					return nil, 0, err
				}
			}
			entries = append(entries, LogEntry{
				ID: stored.ID, Timestamp: stored.Timestamp, Level: stored.Level,
				Source: stored.Source, Domain: stored.Domain, Message: stored.Message,
				Fields: fields, TraceID: stored.TraceID,
			})
		}
	}
	if len(legacyPositions) > 0 {
		// 沿同一文件快照补算候选的原始出现序号，原内容相同的记录不会被筛选拆分。
		// 仅扫描到本页最后一个候选；字段不重复解码，身份集合最多为本页记录数。
		for snapshotIndex, snapshot := range snapshots {
			data, err := readLogSnapshot(ctx, snapshot)
			if err != nil {
				return nil, 0, err
			}
			lines := bytes.Split(data, []byte{'\n'})
			for i := len(lines) - 1; i >= 0; i-- {
				if err := ctx.Err(); err != nil {
					return nil, 0, err
				}
				// 相同原始行必有相同前缀；预筛只省略不可能命中的全文摘要计算。
				var prefix [64]byte
				copy(prefix[:], lines[i])
				if _, candidate := legacyPrefixes[prefix]; !candidate {
					continue
				}
				hash := sha256.Sum256(lines[i])
				occurrence, candidate := legacyOccurrences[hash]
				if !candidate {
					continue
				}
				legacyOccurrences[hash] = occurrence + 1
				position := logFilePosition{snapshot: snapshotIndex, line: i}
				if index, selected := legacyPositions[position]; selected {
					entries[index].ID = fmt.Sprintf("legacy-%x-%d", hash, occurrence+1)
					delete(legacyPositions, position)
					if len(legacyPositions) == 0 {
						return entries, total, nil
					}
				}
			}
		}
		return nil, 0, io.ErrUnexpectedEOF
	}
	return entries, total, nil
}

func readLogSnapshot(ctx context.Context, snapshot logFileSnapshot) ([]byte, error) {
	// 冻结长度已知，避免扫描大文件时缓冲反复扩容和复制已读取的内容。
	data := bytes.NewBuffer(make([]byte, 0, snapshot.size))
	reader := io.NewSectionReader(snapshot.file, 0, snapshot.size)
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := reader.Read(buf)
		data.Write(buf[:n])
		if err == io.EOF {
			if int64(data.Len()) != snapshot.size {
				return nil, io.ErrUnexpectedEOF
			}
			return data.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func closeLogSnapshots(snapshots []logFileSnapshot) {
	for _, snapshot := range snapshots {
		snapshot.file.Close()
	}
}

// AttachToCollector 将文件写入器挂载到 LogCollector
// 在 LogCollector.Add 中调用 sink.Write 持久化日志
func AttachToCollector(collector *LogCollector, sink *LogFileSink) {
	collector.mu.Lock()
	collector.fileSink = sink
	collector.mu.Unlock()
}
