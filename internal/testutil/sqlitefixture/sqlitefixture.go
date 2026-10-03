// Package sqlitefixture 为业务测试复制完成真实迁移的独立 SQLite 空库。
package sqlitefixture

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/hexagon-codes/hexclaw/storage/sqlite"
	modernsqlite "modernc.org/sqlite"
)

var (
	templateOnce sync.Once
	templateData []byte
	templateErr  error
)

// New 复制进程内只迁移一次的空库；调用方仍保留原有 Init 和 Close 流程。
func New(path string) (_ *sqlite.Store, returnErr error) {
	templateOnce.Do(func() {
		templateData, templateErr = buildTemplate()
	})
	if templateErr != nil {
		return nil, fmt.Errorf("prepare SQLite fixture template: %w", templateErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create SQLite fixture directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("create SQLite fixture file: %w", err)
	}
	ready := false
	defer func() {
		if !ready {
			if err := os.Remove(path); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove incomplete SQLite fixture: %w", err))
			}
		}
	}()
	_, writeErr := file.Write(templateData)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return nil, fmt.Errorf("copy SQLite fixture template: %w", err)
	}
	store, err := sqlite.New(path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite fixture: %w", err)
	}
	defer func() {
		if !ready {
			if err := store.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close incomplete SQLite fixture: %w", err))
			}
		}
	}()
	result, err := store.DB().ExecContext(context.Background(),
		`UPDATE backend_metadata SET value=lower(hex(randomblob(16))) WHERE key='backend_id'`)
	if err != nil {
		return nil, fmt.Errorf("reset SQLite fixture backend identity: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("check SQLite fixture backend identity: %w", err)
	}
	if rows != 1 {
		return nil, fmt.Errorf("reset SQLite fixture backend identity: affected %d rows, want 1", rows)
	}
	ready = true
	return store, nil
}

// Open 为默认 DELETE journal 的文件夹具复制空库，再按原 DSN 打开。
// 调用方继续负责连接数、连接级 PRAGMA、迁移和返回数据库的关闭。
func Open(path, dsn string) (_ *sql.DB, returnErr error) {
	store, err := New(path)
	if err != nil {
		return nil, err
	}
	ready := false
	defer func() {
		if !ready {
			if err := os.Remove(path); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove incomplete SQLite DSN fixture: %w", err))
			}
		}
	}()
	if err := store.Close(); err != nil {
		return nil, fmt.Errorf("close SQLite DSN fixture source: %w", err)
	}
	// WAL 模式持久化在主库中，先恢复原文件夹具的 DELETE 模式再重开。
	converter, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite DSN fixture journal converter: %w", err)
	}
	var journalMode string
	queryErr := converter.QueryRowContext(context.Background(), "PRAGMA journal_mode=DELETE").Scan(&journalMode)
	closeErr := converter.Close()
	if err := errors.Join(queryErr, closeErr); err != nil {
		return nil, fmt.Errorf("restore SQLite DSN fixture journal mode: %w", err)
	}
	if journalMode != "delete" {
		return nil, fmt.Errorf("restore SQLite DSN fixture journal mode: got %q, want delete", journalMode)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite DSN fixture: %w", err)
	}
	ready = true
	return db, nil
}

// Memory 通过 SQLite 恢复接口复制到真正的内存库，保留单连接与默认 PRAGMA 语义。
func Memory() (db *sql.DB, returnErr error) {
	directory, err := os.MkdirTemp("", "hexclaw-sqlite-fixture-memory-")
	if err != nil {
		return nil, fmt.Errorf("create SQLite memory fixture directory: %w", err)
	}
	var memory *sql.DB
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove SQLite memory fixture directory: %w", err))
		}
		if returnErr != nil && memory != nil {
			if err := memory.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close incomplete SQLite memory fixture: %w", err))
			}
			db = nil
		}
	}()
	path := filepath.Join(directory, "fixture.db")
	store, err := New(path)
	if err != nil {
		return nil, err
	}
	if err := store.Close(); err != nil {
		return nil, fmt.Errorf("close SQLite memory fixture source: %w", err)
	}
	sourcePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite memory fixture source: %w", err)
	}
	uriPath := filepath.ToSlash(sourcePath)
	if uriPath[0] != '/' {
		uriPath = "/" + uriPath
	}
	sourceURI := (&url.URL{Scheme: "file", Path: uriPath}).String()
	memory, err = sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open SQLite memory fixture: %w", err)
	}
	memory.SetMaxOpenConns(1)
	memory.SetMaxIdleConns(1)
	connection, err := memory.Conn(context.Background())
	if err != nil {
		return nil, fmt.Errorf("acquire SQLite memory fixture connection: %w", err)
	}
	restoreErr := connection.Raw(func(driverConnection any) error {
		restorer, ok := driverConnection.(interface {
			NewRestore(string) (*modernsqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite memory fixture driver does not support restore")
		}
		backup, err := restorer.NewRestore(sourceURI)
		if err != nil {
			return fmt.Errorf("start SQLite memory fixture restore: %w", err)
		}
		if backup == nil {
			return errors.New("SQLite memory fixture restore returned no backup")
		}
		remaining, stepErr := backup.Step(-1)
		finishErr := backup.Finish()
		if remaining {
			stepErr = errors.Join(stepErr, errors.New("SQLite memory fixture restore did not complete"))
		}
		if err := errors.Join(stepErr, finishErr); err != nil {
			return fmt.Errorf("restore SQLite memory fixture: %w", err)
		}
		return nil
	})
	closeErr := connection.Close()
	if err := errors.Join(restoreErr, closeErr); err != nil {
		return nil, fmt.Errorf("prepare SQLite memory fixture: %w", err)
	}
	return memory, nil
}

func buildTemplate() (data []byte, returnErr error) {
	directory, err := os.MkdirTemp("", "hexclaw-sqlite-fixture-template-")
	if err != nil {
		return nil, fmt.Errorf("create SQLite fixture template directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(directory); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove SQLite fixture template directory: %w", err))
		}
	}()
	path := filepath.Join(directory, "template.db")
	store, err := sqlite.New(path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite fixture template: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			if err := store.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close SQLite fixture template: %w", err))
			}
		}
	}()
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		return nil, fmt.Errorf("migrate SQLite fixture template: %w", err)
	}
	// TRUNCATE 完成后 WAL 必须为空，关闭全部连接再复制主库文件。
	var busy, logPages, checkpointedPages int
	if err := store.DB().QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &logPages, &checkpointedPages); err != nil {
		return nil, fmt.Errorf("checkpoint SQLite fixture template: %w", err)
	}
	if busy != 0 || logPages != 0 || checkpointedPages != 0 {
		return nil, fmt.Errorf("checkpoint SQLite fixture template incomplete: busy=%d log=%d checkpointed=%d",
			busy, logPages, checkpointedPages)
	}
	closeErr := store.Close()
	closed = true
	if closeErr != nil {
		return nil, fmt.Errorf("close SQLite fixture template: %w", closeErr)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read SQLite fixture template: %w", err)
	}
	return data, nil
}
