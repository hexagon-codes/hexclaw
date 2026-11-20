package engineadapter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/curriculum"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"modernc.org/sqlite"
)

func TestBackupFreezesRecordsAndCanonicalDetailsInOneSnapshot(t *testing.T) {
	ctx := t.Context()
	fixture := newArchiveRestoreFixture(t)
	const agent = "mingming"
	const currentChildName = "同步档案"
	seedAccumulationArchiveV10(t, fixture, agent, "床前明月光", "《静夜思》")
	var file string
	var schema string
	var sequence int
	if err := fixture.db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &schema, &file); err != nil || file == "" {
		t.Fatalf("temporary SQLite location: %v", err)
	}
	var added string
	connector := &archiveReadConnector{sqlite: &sqlite.Driver{}, file: file, beforeFirstReadTx: func() {
		// 在首次读取事务启动时写入新记录和修改档案，SQL 仍由正式存储执行。
		added = seedAccumulationArchiveV10(t, fixture, agent, "春眠不觉晓", "《春晓》")
		cfg, ok := fixture.dispatcher.GetAgent(agent)
		if !ok {
			t.Fatal("fixture agent is missing")
		}
		updated := *cfg
		updated.Metadata = k12.ApplyProfileToMeta(cfg.Metadata, k12.ChildProfile{ChildName: currentChildName})
		if err := fixture.agentStore.SaveAgent(ctx, &updated); err != nil {
			t.Fatal(err)
		}
	}}
	reader := sql.OpenDB(connector)
	defer reader.Close()
	registry := scenario.NewRegistry()
	if err := registry.Assemble(k12.Pack(curriculum.New())); err != nil {
		t.Fatal(err)
	}
	deps := usecase.Deps{
		Records:  k12storage.NewStore(reader, registry.Records),
		Profiles: NewProfileAdapter(fixture.dispatcher, fixture.agentStore),
	}
	archive, err := deps.Backup(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	rootFound, detailFound := false, false
	for _, record := range archive.Records {
		rootFound = rootFound || record.RecordID == added
	}
	for _, detail := range archive.CurrentAccumulations {
		detailFound = detailFound || detail.AccumulationID == added
	}
	if err := usecase.VerifyHexbak(archive); err != nil || !rootFound || !detailFound {
		t.Fatalf("snapshot is not restorable or missed the committed record: root=%t detail=%t err=%v", rootFound, detailFound, err)
	}
	if archive.Profile == nil || archive.Profile.ChildName != currentChildName {
		t.Fatalf("backup used stale route metadata: profile=%+v", archive.Profile)
	}
}

type archiveReadConnector struct {
	sqlite            *sqlite.Driver
	file              string
	once              sync.Once
	beforeFirstReadTx func()
}

func (c *archiveReadConnector) Driver() driver.Driver { return c.sqlite }

func (c *archiveReadConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.sqlite.Open(c.file)
	if err != nil {
		return nil, err
	}
	return &archiveReadConn{Conn: conn, connector: c}, nil
}

type archiveReadConn struct {
	driver.Conn
	connector *archiveReadConnector
}

func (c *archiveReadConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	if options.ReadOnly {
		c.connector.once.Do(c.connector.beforeFirstReadTx)
	}
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func TestLearningArchiveIncludesDerivedAccumulationSource(t *testing.T) {
	ctx := t.Context()
	fixture := newArchiveRestoreFixture(t)
	const agent = "mingming"
	const source = "《静夜思》"
	id := seedAccumulationArchiveV10(t, fixture, agent, "床前明月光", source)
	deps := usecase.Deps{Records: fixture.records}
	item, err := deps.GetAccumulation(ctx, agent, id)
	if err != nil || item.Fields.Source != source {
		t.Fatalf("accumulation detail source=%q error=%v", item.Fields.Source, err)
	}
	archive, err := deps.ExportLearningArchiveMarkdown(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(archive.CanonicalMarkdown, "来源："+source) {
		t.Fatalf("current accumulation source is missing from the frozen Markdown: %s", archive.CanonicalMarkdown)
	}
}

func TestLearningArchiveExportsRestoredLegacyWriting(t *testing.T) {
	ctx := t.Context()
	const agent = "mingming"
	const original = "秋风吹着树叶。"
	const feedback = "本段动作描写清楚。"
	source, target := newArchiveRestoreFixture(t), newArchiveRestoreFixture(t)
	legacy, err := k12.NewCreativeWorkRecord(agent, "archive-legacy-writing", k12.CreativeWorkFields{
		GradeTerm: "五年级上", WorkType: k12.WorkTypeWriting,
		Versions: []k12.CreativeWorkVersion{
			{VersionID: "original", ContentMarkdown: original},
			{VersionID: "revision", ContentMarkdown: "秋风吹得树叶沙沙响。", Feedback: feedback},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.records.Put(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	saved, err := source.records.Get(ctx, legacy.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	archive := &usecase.Hexbak{
		Version: 6, AgentName: agent, ExportedAt: 100,
		Records: []*records.AgentRecord{saved}, Profile: &k12.ChildProfile{ChildName: "样例", GradeTerm: "五年级上"},
	}
	if err := usecase.SealHexbak(archive); err != nil {
		t.Fatal(err)
	}
	deps := usecase.Deps{Records: target.records, ArchiveRestorer: target.restore}
	if n, err := deps.Restore(ctx, archive); err != nil || n != 1 {
		t.Fatalf("restore legacy archive: count=%d error=%v", n, err)
	}
	exported, err := deps.ExportLearningArchiveMarkdown(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if exported.ObjectCounts.CreativeWorks != 1 ||
		!strings.Contains(exported.CanonicalMarkdown, original) ||
		!strings.Contains(exported.CanonicalMarkdown, feedback) {
		t.Fatalf("legacy writing original or feedback is missing: counts=%+v body=%s", exported.ObjectCounts, exported.CanonicalMarkdown)
	}
}
