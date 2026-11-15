package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// recordArchiveSQLiteFixture 为记录字段用例提供真实SQLite事务，不模拟文件或档案子系统。
type recordArchiveSQLiteFixture struct {
	db      *sql.DB
	records *k12storage.Store
}

func (fixture *recordArchiveSQLiteFixture) RestoreArchive(ctx context.Context, agent string, recs []*records.AgentRecord, profile *k12.ChildProfile) error {
	return fixture.RestoreHexbak(ctx, &Hexbak{AgentName: agent, Records: recs, Profile: profile})
}

func (fixture *recordArchiveSQLiteFixture) RestoreHexbak(ctx context.Context, archive *Hexbak) error {
	if archive.Profile != nil || len(archive.Assets) != 0 || len(archive.CreativeWorkOCR) != 0 || len(archive.ProblemAttempts) != 0 || archive.ProblemSource != nil || len(archive.CurrentCreativeWorks) != 0 || len(archive.TeachingDiagrams) != 0 || archive.ReviewSources != nil {
		return fmt.Errorf("records-only archive fixture does not support other subsystems")
	}
	tx, err := fixture.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fixture.records.ImportAgentRecordsTx(ctx, tx, archive.AgentName, archive.Records); err != nil {
		return err
	}
	if err := fixture.records.ImportAccumulationsArchiveV10Tx(ctx, tx, archive.AgentName, archive.CurrentAccumulations); err != nil {
		return err
	}
	return tx.Commit()
}

func TestHexbakV10KeepsLegacyV7ThroughV9Checksums(t *testing.T) {
	for version, checksum := range map[int]string{
		7: "c188c1e77a911704b4106c23cdb5f0be0c28dcdc7f53710c7279ac6da0013e4b",
		8: "335ddae25702a16f4e4a413726b906cfb6df80c38c365c3fa665d480cbcc55c5",
		9: "9c821cfc66ca00fb7805364e1b8a163aef327ad8d4050cabbc5882e708914ede",
	} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			raw := fmt.Sprintf(`{"version":%d,"archive_id":"legacy-fixed","agent_name":"mingming","exported_at":100,"records":[],"checksum":%q}`, version, checksum)
			var archive Hexbak
			if err := json.Unmarshal([]byte(raw), &archive); err != nil {
				t.Fatal(err)
			}
			if err := VerifyHexbak(&archive); err != nil {
				t.Fatalf("legacy checksum changed: %v", err)
			}
			if err := SealHexbak(&archive); err != nil || archive.Checksum != checksum {
				t.Fatalf("legacy reseal changed: checksum=%s err=%v", archive.Checksum, err)
			}
		})
	}
}

func TestHexbakV10CoversAccumulationEvidenceAndOwnerMigrationDoesNotAlias(t *testing.T) {
	record, err := k12.NewAccumRecord("mingming", "", k12.AccumFields{
		GradeTerm: "五年级上", Subject: "语文", EntryType: "古诗积累", Content: "床前明月光",
	})
	if err != nil {
		t.Fatal(err)
	}
	record.RecordID = "archive-accumulation"
	source := "《静夜思》"
	confidence := 0.99
	provenance := &k12.DerivationProvenance{Method: "model", Policy: "controlled-archive", Version: "1", Confidence: &confidence}
	archive := &Hexbak{Version: HexbakVersion, AgentName: "mingming", ExportedAt: 100,
		Records: []*records.AgentRecord{record},
		CurrentAccumulations: []k12storage.AccumulationArchiveV10{{
			AccumulationID: record.RecordID, AgentName: "mingming", DerivedSourceRef: &source, RowVersion: 1,
			SubjectProvenance: provenance, EntryTypeProvenance: provenance, SourceProvenance: provenance,
		}},
	}
	if err := SealHexbak(archive); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHexbak(archive); err != nil {
		t.Fatal(err)
	}
	migrated, err := MigrateHexbakOwner(archive, "target-child")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.CurrentAccumulations[0].AgentName != "target-child" {
		t.Fatal("current accumulation owner was not rewritten")
	}
	if err := VerifyHexbak(migrated); err != nil {
		t.Fatal(err)
	}
	*migrated.CurrentAccumulations[0].SourceProvenance.Confidence = 0.5
	*migrated.CurrentAccumulations[0].DerivedSourceRef = "changed source"
	if err := VerifyHexbak(migrated); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("current evidence is not checksum-covered: %v", err)
	}
	if source != "《静夜思》" || confidence != 0.99 {
		t.Fatal("owner rewrite aliased the immutable source provenance")
	}
	if err := VerifyHexbak(archive); err != nil {
		t.Fatalf("source archive changed: %v", err)
	}
}
