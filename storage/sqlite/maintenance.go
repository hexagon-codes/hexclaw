package sqlite

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/hexagon-codes/toolkit/util/logger"
)

// vacuumThreshold 可回收空闲页超过此大小才触发自动 VACUUM（默认 200 MB）。
// 用 var 以便测试注入小阈值（生产不应修改）
var vacuumThreshold int64 = 200 * 1024 * 1024

// StartMaintenance 启动周期性维护：
//   - 每 interval（默认 7 天）检查可回收空间
//   - 只有可回收空闲页超过 vacuumThreshold 才执行 VACUUM
//   - 同时 checkpoint WAL 到 main DB
//
// 设计上不阻塞启动；返回 stop 用于优雅关闭。
func (s *Store) StartMaintenance(ctx context.Context, dbPath string, interval time.Duration) func() {
	if interval <= 0 {
		interval = 7 * 24 * time.Hour
	}
	loopCtx, cancel := context.WithCancel(ctx)
	go func() {
		// 启动即检查一次
		s.runMaintenance(dbPath)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-t.C:
				s.runMaintenance(dbPath)
			}
		}
	}()
	return cancel
}

func (s *Store) runMaintenance(dbPath string) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("[sqlite.maintenance] panic recovered", "panic", r)
		}
	}()

	// checkpoint WAL：强制把 WAL 内容合并到 main DB，缩小 WAL 文件
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		logger.Warn("[sqlite.maintenance] wal_checkpoint 失败", "error", err)
	}

	if info, err := os.Stat(dbPath); err == nil && info.Size() > vacuumThreshold {
		var freePages, pageSize int64
		if err := s.db.QueryRow("PRAGMA freelist_count").Scan(&freePages); err != nil {
			logger.Warn("[sqlite.maintenance] cannot inspect free pages", "error", err)
			return
		}
		if err := s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
			logger.Warn("[sqlite.maintenance] cannot inspect page size", "error", err)
			return
		}
		reclaimableBytes := freePages * pageSize
		if reclaimableBytes <= vacuumThreshold {
			logger.Info("[sqlite.maintenance] VACUUM skipped", "size_bytes", info.Size(), "reclaimable_bytes", reclaimableBytes, "threshold_bytes", vacuumThreshold)
			return
		}
		logger.Info("[sqlite.maintenance] reclaimable space triggers VACUUM", "size_bytes", info.Size(), "reclaimable_bytes", reclaimableBytes)
		if _, err := s.db.Exec("VACUUM"); err != nil {
			logger.Error("[sqlite.maintenance] VACUUM 失败", "error", err)
		}
	}
}

// VacuumNow 立即执行一次 VACUUM（供测试和手动维护使用）
func (s *Store) VacuumNow(_ context.Context) error {
	_, err := s.db.Exec("VACUUM")
	return err
}

// DB 暴露底层 *sql.DB 供测试使用（不导出给 domain logic）
func (s *Store) rawDB() *sql.DB { return s.db }
