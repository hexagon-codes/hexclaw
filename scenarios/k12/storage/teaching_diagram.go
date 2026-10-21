package k12storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
)

// TeachingDiagramArchive 保存内容寻址字节和归属，不包含模型原文或调用凭据。
type TeachingDiagramArchive struct {
	OwnerScope  string `json:"owner_scope"`
	AgentName   string `json:"agent_name"`
	AssetID     string `json:"asset_id"`
	AssetDigest string `json:"asset_digest"`
	SVG         []byte `json:"svg"`
	CreatedAt   int64  `json:"created_at"`
}

func ValidateTeachingDiagramArchive(agent string, assets []TeachingDiagramArchive) error {
	seen := map[string]bool{}
	for _, asset := range assets {
		id, digest := teachingDiagramIdentity(agent, asset.SVG)
		if asset.OwnerScope == "" || asset.AgentName != agent || len(asset.SVG) == 0 || id != asset.AssetID || digest != asset.AssetDigest || seen[id] {
			return errors.New("invalid archived teaching diagram asset")
		}
		seen[id] = true
	}
	return nil
}

func (s *Store) ExportTeachingDiagramAssets(ctx context.Context, agent string) ([]TeachingDiagramArchive, error) {
	return exportTeachingDiagramAssets(ctx, s.db, agent)
}

// ExportTeachingDiagramAssetsTx 保证恢复前快照与其记录引用来自同一事务视图。
func (s *Store) ExportTeachingDiagramAssetsTx(ctx context.Context, tx *sql.Tx, agent string) ([]TeachingDiagramArchive, error) {
	return exportTeachingDiagramAssets(ctx, tx, agent)
}

func exportTeachingDiagramAssets(ctx context.Context, q dbQueryer, agent string) ([]TeachingDiagramArchive, error) {
	rows, err := q.QueryContext(ctx, `SELECT owner_scope,agent_name,asset_id,asset_digest,svg,created_at FROM k12_teaching_diagram_assets WHERE agent_name=? ORDER BY asset_id`, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []TeachingDiagramArchive
	for rows.Next() {
		var asset TeachingDiagramArchive
		if err := rows.Scan(&asset.OwnerScope, &asset.AgentName, &asset.AssetID, &asset.AssetDigest, &asset.SVG, &asset.CreatedAt); err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return assets, ValidateTeachingDiagramArchive(agent, assets)
}

// ImportTeachingDiagramAssetsTx 与记录及档案恢复共用事务，摘要冲突不能部分提交。
func (s *Store) ImportTeachingDiagramAssetsTx(ctx context.Context, tx *sql.Tx, agent string, assets []TeachingDiagramArchive) error {
	if err := ValidateTeachingDiagramArchive(agent, assets); err != nil {
		return err
	}
	for _, asset := range assets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO k12_teaching_diagram_assets(owner_scope,agent_name,asset_id,asset_digest,svg,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, asset.OwnerScope, agent, asset.AssetID, asset.AssetDigest, asset.SVG, asset.CreatedAt); err != nil {
			return err
		}
		var owner, digest string
		var data []byte
		if err := tx.QueryRowContext(ctx, `SELECT owner_scope,asset_digest,svg FROM k12_teaching_diagram_assets WHERE agent_name=? AND asset_id=?`, agent, asset.AssetID).Scan(&owner, &digest, &data); err != nil {
			return err
		}
		if owner != asset.OwnerScope || digest != asset.AssetDigest || string(data) != string(asset.SVG) {
			return errors.New("archived teaching diagram identity conflict")
		}
	}
	return nil
}

func teachingDiagramIdentity(agent string, data []byte) (string, string) {
	digest := sha256.Sum256(data)
	encoded := hex.EncodeToString(digest[:])
	return "asset://" + agent + "/" + encoded + ".svg", encoded
}

// PutTeachingDiagramAsset 仅由已核验图示构造器调用；同字节幂等，旧资产不能被覆盖。
func (s *Store) PutTeachingDiagramAsset(ctx context.Context, owner, agent string, data []byte, at int64) (string, error) {
	if strings.TrimSpace(owner) == "" || strings.TrimSpace(agent) == "" || len(data) == 0 {
		return "", errors.New("teaching diagram identity is incomplete")
	}
	id, digest := teachingDiagramIdentity(agent, data)
	_, err := s.db.ExecContext(ctx, `INSERT INTO k12_teaching_diagram_assets(owner_scope,agent_name,asset_id,asset_digest,svg,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, owner, agent, id, digest, data, at)
	if err != nil {
		return "", fmt.Errorf("store teaching diagram: %w", err)
	}
	saved, err := s.GetTeachingDiagramAsset(ctx, owner, agent, id)
	if err != nil || string(saved) != string(data) {
		return "", errors.New("teaching diagram immutable identity conflict")
	}
	return id, nil
}

// GetTeachingDiagramAsset 供已认证资产接口读取；每次重新核对内容寻址身份。
func (s *Store) GetTeachingDiagramAsset(ctx context.Context, owner, agent, id string) ([]byte, error) {
	var data []byte
	var digest string
	err := s.db.QueryRowContext(ctx, `SELECT svg,asset_digest FROM k12_teaching_diagram_assets WHERE owner_scope=? AND agent_name=? AND asset_id=?`, owner, agent, id).Scan(&data, &digest)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, records.ErrNotFound
		}
		return nil, err
	}
	actualID, actualDigest := teachingDiagramIdentity(agent, data)
	if actualID != id || actualDigest != digest {
		return nil, errors.New("teaching diagram asset digest mismatch")
	}
	return data, nil
}

// TeachingDiagramOwnerForAgent 用于已绑定Artifact的后台读取，不替代HTTP请求归属校验。
func (s *Store) TeachingDiagramOwnerForAgent(ctx context.Context, agent, id string) (string, error) {
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT owner_scope FROM k12_teaching_diagram_assets WHERE agent_name=? AND asset_id=?`, agent, id).Scan(&owner)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", records.ErrNotFound
		}
		return "", err
	}
	if owner == "" {
		return "", records.ErrNotFound
	}
	return owner, nil
}
