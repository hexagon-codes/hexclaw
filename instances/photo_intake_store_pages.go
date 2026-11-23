package instances

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (s *PhotoIntakeStore) GetPage(ctx context.Context, key PhotoIntakeKey, index int) (PhotoIntakePage, error) {
	p, err := scanPhotoPage(s.db.QueryRowContext(ctx, `SELECT `+photoPageColumns+`,data FROM im_photo_intake_pages WHERE intake_id=`+photoSourceID+` AND page_index=?`, append(key.args(), index)...), true)
	if err != nil {
		return p, err
	}
	if p.Data != nil && (p.Size != int64(len(p.Data)) || p.Digest != photoIntakeDigest(p.Data)) {
		return PhotoIntakePage{}, fmt.Errorf("%w: stored content differs at page %d", ErrPhotoIntakeConflict, index)
	}
	return p, nil
}

func photoIntakeDigest(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }

func (s *PhotoIntakeStore) StartPage(ctx context.Context, key PhotoIntakeKey, index int, atMS int64) error {
	if atMS <= 0 {
		return fmt.Errorf("IM photo download start time is required")
	}
	args := append([]any{atMS}, key.args()...)
	res, err := s.db.ExecContext(ctx, `UPDATE im_photo_intake_pages SET started_at_ms=CASE WHEN started_at_ms=0 THEN ? ELSE started_at_ms END
		WHERE intake_id=`+photoSourceID+` AND page_index=?`, append(args, index)...)
	return photoIntakeAffected(res, err)
}

// SavePage verifies any refreshed media handle against the original accepted bytes (digest after release).
// New page bytes and the last-page barrier commit together.
func (s *PhotoIntakeStore) SavePage(ctx context.Context, key PhotoIntakeKey, index int, signature, name, mime string, data []byte, downloadedAtMS int64) error {
	if !validPhotoSignature(signature) || len(data) == 0 || !strings.HasPrefix(mime, "image/") || downloadedAtMS <= 0 {
		return fmt.Errorf("invalid IM photo page input")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	digest := photoIntakeDigest(data)
	args := []any{name, mime, len(data), digest, data, downloadedAtMS}
	args = append(args, key.args()...)
	args = append(args, index, signature)
	res, err := tx.ExecContext(ctx, `UPDATE im_photo_intake_pages SET name=?,mime=?,byte_size=?,digest=?,data=?,downloaded_at_ms=?,stored_at_ms=CAST(ROUND(unixepoch('subsec')*1000) AS INTEGER)
		WHERE intake_id=`+photoSourceID+` AND page_index=? AND signature=? AND digest=''`, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		p, readErr := scanPhotoPage(tx.QueryRowContext(ctx, `SELECT `+photoPageColumns+`,data FROM im_photo_intake_pages WHERE intake_id=`+photoSourceID+` AND page_index=?`, append(key.args(), index)...), true)
		if readErr != nil {
			return readErr
		}
		if p.Digest != digest || p.Size != int64(len(data)) || p.MIME != mime || (p.Data != nil && !bytes.Equal(p.Data, data)) {
			return fmt.Errorf("%w: different content at page %d", ErrPhotoIntakeConflict, index)
		}
		return tx.Commit()
	}
	args = append([]any{time.Now().UnixMilli()}, key.args()...)
	if _, err = tx.ExecContext(ctx, `UPDATE im_photo_intakes SET assets_ready_at_ms=? WHERE `+photoKeyWhere+` AND assets_ready_at_ms=0
		AND NOT EXISTS (SELECT 1 FROM im_photo_intake_pages p WHERE p.intake_id=im_photo_intakes.id AND p.digest='')`, args...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PhotoIntakeStore) FailPage(ctx context.Context, key PhotoIntakeKey, index int, kind string, atMS int64) error {
	if !validPhotoFailureKind(kind) || atMS <= 0 {
		return fmt.Errorf("invalid IM photo download failure")
	}
	args := append([]any{kind, atMS}, key.args()...)
	res, err := s.db.ExecContext(ctx, `UPDATE im_photo_intake_pages SET failure_kind=?,failed_at_ms=?
		WHERE intake_id=`+photoSourceID+` AND page_index=?`, append(args, index)...)
	return photoIntakeAffected(res, err)
}

func validPhotoFailureKind(kind string) bool {
	if kind == "" {
		return false
	}
	for _, c := range kind {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

func photoIntakeAffected(res sql.Result, execErr error) error {
	if execErr != nil {
		return execErr
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DropPages leaves stable fingerprints and sizes available to validate later callbacks.
func (s *PhotoIntakeStore) DropPages(ctx context.Context, key PhotoIntakeKey) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE im_photo_intake_pages SET data=NULL WHERE data IS NOT NULL AND intake_id=(
		SELECT id FROM im_photo_intakes WHERE `+photoKeyWhere+` AND assets_ready_at_ms>0 AND json_extract(work_json,'$.status')='completed')`, key.args()...)
	if err != nil {
		return err
	}
	var entered bool
	if err = tx.QueryRowContext(ctx, `SELECT assets_ready_at_ms>0 AND json_extract(work_json,'$.status')='completed' FROM im_photo_intakes WHERE `+photoKeyWhere, key.args()...).Scan(&entered); err != nil {
		return err
	}
	if !entered {
		return fmt.Errorf("%w: photo bytes have no durable handoff", ErrPhotoIntakeConflict)
	}
	return tx.Commit()
}
