package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// AccumulationArchiveV10 保存积累的当前来源、派生依据及删除事实，不重建缺失的历史证据。
type AccumulationArchiveV10 struct {
	AccumulationID      string                    `json:"accumulation_id"`
	AgentName           string                    `json:"agent_name"`
	DerivedSourceRef    *string                   `json:"derived_source_ref,omitempty"`
	SubjectProvenance   *k12.DerivationProvenance `json:"subject_provenance,omitempty"`
	EntryTypeProvenance *k12.DerivationProvenance `json:"entry_type_provenance,omitempty"`
	SourceProvenance    *k12.DerivationProvenance `json:"source_provenance,omitempty"`
	RowVersion          int                       `json:"row_version"`
	DeletedAt           *int64                    `json:"deleted_at,omitempty"`
	DeletedBy           string                    `json:"deleted_by,omitempty"`
	DeleteCommandKey    string                    `json:"delete_command_key,omitempty"`
	DeleteReceiptJSON   string                    `json:"delete_receipt_json,omitempty"`
}

// ValidateAccumulationsArchiveV10 校验已携带的来源证据和删除回执；空列仍表达没有证据。
func ValidateAccumulationsArchiveV10(agent string, items []AccumulationArchiveV10) error {
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if item.AgentName != agent || item.AccumulationID == "" || item.RowVersion < 1 || seen[item.AccumulationID] {
			return fmt.Errorf("invalid current accumulation archive identity")
		}
		seen[item.AccumulationID] = true
		if item.DeletedAt != nil {
			var receipt k12.CurrentDeleteReceipt
			if *item.DeletedAt <= 0 || item.DeletedBy == "" || item.DeleteCommandKey == "" ||
				json.Unmarshal([]byte(item.DeleteReceiptJSON), &receipt) != nil ||
				!receipt.Deleted || receipt.ObjectKind != "accumulation" || receipt.ObjectID != item.AccumulationID ||
				receipt.RowVersion != item.RowVersion || receipt.DeletedAt != *item.DeletedAt {
				return fmt.Errorf("invalid current accumulation delete receipt")
			}
		} else if item.DeletedBy != "" || item.DeleteCommandKey != "" || item.DeleteReceiptJSON != "" {
			return fmt.Errorf("current accumulation delete receipt has no tombstone")
		}
		if item.DerivedSourceRef != nil && len([]rune(strings.TrimSpace(*item.DerivedSourceRef))) > 50 {
			return fmt.Errorf("current accumulation source exceeds limit")
		}
		if item.SourceProvenance != nil && (item.DerivedSourceRef == nil || strings.TrimSpace(*item.DerivedSourceRef) == "") {
			return fmt.Errorf("current accumulation source provenance has no source")
		}
		for _, provenance := range []*k12.DerivationProvenance{item.SubjectProvenance, item.EntryTypeProvenance, item.SourceProvenance} {
			if provenance != nil {
				if err := provenance.Validate(); err != nil {
					return fmt.Errorf("invalid current accumulation provenance: %w", err)
				}
			}
		}
	}
	return nil
}

func (s *Store) ExportAccumulationsArchiveV10(ctx context.Context, agent string) ([]AccumulationArchiveV10, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	items, err := s.ExportAccumulationsArchiveV10Tx(ctx, tx, agent)
	if err != nil {
		return nil, err
	}
	return items, tx.Commit()
}

func (s *Store) ExportAccumulationsArchiveV10Tx(ctx context.Context, tx *sql.Tx, agent string) ([]AccumulationArchiveV10, error) {
	if tx == nil {
		return nil, fmt.Errorf("current accumulation archive transaction is required")
	}
	rows, err := tx.QueryContext(ctx, `SELECT record_id,derived_source_ref,
		subject_provenance_json,entry_type_provenance_json,source_provenance_json,
		row_version,deleted_at,deleted_by,delete_command_key,delete_receipt_json
		FROM k12_accumulations WHERE agent_name=? ORDER BY record_id`, agent)
	if err != nil {
		return nil, err
	}
	items := make([]AccumulationArchiveV10, 0)
	for rows.Next() {
		item := AccumulationArchiveV10{AgentName: agent}
		var source sql.NullString
		var subject, entryType, sourceProvenance string
		if err := rows.Scan(&item.AccumulationID, &source, &subject, &entryType, &sourceProvenance,
			&item.RowVersion, &item.DeletedAt, &item.DeletedBy, &item.DeleteCommandKey, &item.DeleteReceiptJSON); err != nil {
			rows.Close()
			return nil, err
		}
		if source.Valid {
			item.DerivedSourceRef = &source.String
		}
		for _, field := range []struct {
			raw  string
			dest **k12.DerivationProvenance
		}{{subject, &item.SubjectProvenance}, {entryType, &item.EntryTypeProvenance}, {sourceProvenance, &item.SourceProvenance}} {
			if field.raw == "" {
				continue
			}
			var provenance k12.DerivationProvenance
			if err := json.Unmarshal([]byte(field.raw), &provenance); err != nil {
				rows.Close()
				return nil, fmt.Errorf("decode current accumulation provenance: %w", err)
			}
			*field.dest = &provenance
		}
		items = append(items, item)
	}
	if err := rowsDone(rows); err != nil {
		return nil, err
	}
	if err := ValidateAccumulationsArchiveV10(agent, items); err != nil {
		return nil, err
	}
	return items, nil
}

// ImportAccumulationsArchiveV10Tx 只更新同事务已恢复的根记录，nil 不覆盖旧包未携带的事实。
func (s *Store) ImportAccumulationsArchiveV10Tx(ctx context.Context, tx *sql.Tx, agent string, items []AccumulationArchiveV10) error {
	if tx == nil {
		return fmt.Errorf("current accumulation archive transaction is required")
	}
	if err := ValidateAccumulationsArchiveV10(agent, items); err != nil {
		return err
	}
	for _, item := range items {
		var source any
		if item.DerivedSourceRef != nil {
			source = *item.DerivedSourceRef
		}
		provenanceJSON := make([]string, 3)
		for index, provenance := range []*k12.DerivationProvenance{item.SubjectProvenance, item.EntryTypeProvenance, item.SourceProvenance} {
			if provenance != nil {
				raw, err := json.Marshal(provenance)
				if err != nil {
					return err
				}
				provenanceJSON[index] = string(raw)
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE k12_accumulations SET derived_source_ref=?,
			subject_provenance_json=?,entry_type_provenance_json=?,source_provenance_json=?,
			row_version=?,deleted_at=?,deleted_by=?,delete_command_key=?,delete_receipt_json=?
			WHERE record_id=? AND agent_name=?`, source, provenanceJSON[0], provenanceJSON[1], provenanceJSON[2],
			item.RowVersion, item.DeletedAt, item.DeletedBy, item.DeleteCommandKey, item.DeleteReceiptJSON, item.AccumulationID, agent)
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return err
		} else if affected != 1 {
			return records.ErrNotFound
		}
	}
	return nil
}
