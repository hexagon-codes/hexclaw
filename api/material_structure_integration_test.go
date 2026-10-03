package api

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/knowledge"
)

func materialDOCXFixture(t *testing.T, body string, images bool) []byte {
	t.Helper()
	files := map[string][]byte{
		"[Content_Types].xml": []byte(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/></Types>`),
		"word/document.xml":   []byte(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>` + body + `</w:body></w:document>`),
		"word/numbering.xml":  []byte(`<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:abstractNum w:abstractNumId="3"><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="%1."/></w:lvl></w:abstractNum><w:num w:numId="7"><w:abstractNumId w:val="3"/></w:num></w:numbering>`),
	}
	if images {
		var img bytes.Buffer
		if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
			t.Fatal(err)
		}
		files["word/media/figure.png"] = img.Bytes()
		files["word/_rels/document.xml.rels"] = []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/figure.png"/></Relationships>`)
	}
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func materialDOCXInput(data []byte) func(*sql.DB, *knowledge.CreateDocumentInput) {
	return func(_ *sql.DB, in *knowledge.CreateDocumentInput) {
		in.Filename = "数学六年级上册.docx"
		in.MediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		in.Body = bytes.NewReader(data)
		in.SizeBytes = int64(len(data))
	}
}

func TestMaterialPreparationMultilineAndExplicitAnswerNumbers(t *testing.T) {
	_, records, worker, doc := materialImportFixture(t, "# 数学练习\n1. 商店3本书售价18元，\n7本同样的书一共多少元？\n2. 12÷3=\n## 参考答案\n2. 999\n1. 42元\n")
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || !summary.ExtractionComplete || len(summary.Items) != 2 {
		t.Fatalf("parsed multiline: %+v %v", summary, err)
	}
	if summary.Items[0].Stem != "商店3本书售价18元，\n7本同样的书一共多少元？" || summary.Items[0].ReferenceAnswer != "42元" || summary.Items[1].ReferenceAnswer != "999" {
		t.Fatalf("numbered answer ownership: %+v", summary.Items)
	}
	local := worker.Solver
	boundary := &materialControlledSolver{t: t}
	for i := 0; i < 2; i++ {
		p, e := records.NextMaterialPreparation(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		worker.Solver = local
		if strings.Contains(p.Candidate.Facts.Stem, "商店") {
			worker.Solver = boundary
			worker.ResolveModel = materialModelRoute
		}
		if _, e = worker.RunOnce(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
	summary, err = records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.State != "ready" || summary.Counts["ready"] != 2 || boundary.calls != 2 {
		t.Fatalf("independent prepared answers: %+v %v calls=%d", summary, err, boundary.calls)
	}
	if strings.Contains(summary.Items[1].Answer, "999") {
		t.Fatal("wrong supplied answer was published")
	}
}

func TestMaterialPreparationDOCXParagraphListsAndAnswersPersist(t *testing.T) {
	data := materialDOCXFixture(t, `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>算式 &amp; 练习</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="7"/></w:numPr></w:pPr><w:r><w:t>4.5×</w:t></w:r><w:r><w:t>2=</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="7"/></w:numPr></w:pPr><w:r><w:t>12÷3=</w:t></w:r></w:p>
<w:p><w:r><w:t>参考答案</w:t></w:r></w:p>
<w:p><w:r><w:t>2. 999</w:t></w:r></w:p>
<w:p><w:r><w:t>1. 9</w:t></w:r></w:p>`, false)
	db, records, worker, doc := materialImportFixture(t, "", materialDOCXInput(data))
	for i := 0; i < 2; i++ {
		if _, err := worker.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || !summary.ExtractionComplete || summary.Counts["ready"] != 2 {
		t.Fatalf("DOCX completion %+v %v", summary, err)
	}
	if summary.Items[0].Stem != "4.5×2=" || summary.Items[1].Stem != "12÷3=" || summary.Items[1].ReferenceAnswer != "999" || strings.Contains(summary.Items[1].Answer, "999") {
		t.Fatalf("DOCX answer order: %+v", summary.Items)
	}
	var raw, content string
	if err = db.QueryRow(`SELECT manifest_json FROM kb_ingest_source_manifests WHERE document_id=? AND content_generation=1`, doc).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m knowledge.SourceManifest
	if err = json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Blocks) != 6 || m.Blocks[0].Text != "算式 & 练习" || m.Blocks[1].Kind != "list_item" || m.Blocks[1].ListID != "7" || m.Blocks[2].Text != "2. 12÷3=" || m.SourceDigest == "" {
		t.Fatalf("durable source structure: %+v", m)
	}
	if err = db.QueryRow(`SELECT content FROM kb_documents WHERE id=?`, doc).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "1. 4.5×2=\n\n2. 12÷3=") || strings.Contains(content, "block_id") {
		t.Fatalf("readable source content: %s", content)
	}
	var materialManifest string
	if err = db.QueryRow(`SELECT manifest_json FROM k12_material_manifests WHERE document_id=?`, doc).Scan(&materialManifest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(materialManifest, `"kind":"answer_for"`) {
		t.Fatal("explicit answer relation was not persisted")
	}
	t.Logf("Actual DOCX source manifest: %s", raw)
}

func TestMaterialPreparationDOCXImagesAndTablesNeverDetach(t *testing.T) {
	data := materialDOCXFixture(t, `<w:p><w:r><w:t>1. 根据下表计算总价？</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t>数量</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>3</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:r><w:t>2. 4.5×2=</w:t></w:r></w:p>
<w:p><w:r><w:drawing><a:blip r:embed="rId4"/></w:drawing></w:r></w:p>
<w:p><w:r><w:t>3. 12÷3=</w:t></w:r></w:p>`, true)
	db, records, worker, doc := materialImportFixture(t, "", materialDOCXInput(data))
	for n := 0; n < 2; n++ {
		if _, err := worker.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.ExtractionComplete || summary.Counts["ready"] != 1 || summary.Counts["needs_review"] != 1 || len(summary.Items) != 2 {
		t.Fatalf("unclosed dependent question published: %+v %v", summary, err)
	}
	for _, item := range summary.Items {
		if item.State == "ready" && item.Stem != "12÷3=" {
			t.Fatalf("dependent item published without interpretation: %+v", item)
		}
	}
	var raw string
	if err = db.QueryRow(`SELECT manifest_json FROM kb_ingest_source_manifests WHERE document_id=?`, doc).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m knowledge.SourceManifest
	if err = json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Blocks) != 5 || m.Blocks[1].Kind != "table" || len(m.Blocks[1].Cells) != 2 || m.Blocks[1].Cells[0].ColumnSpan != 2 || m.Blocks[1].Cells[1].Column != 3 || m.Blocks[1].Cells[0].Blocks[0].Text != "数量" {
		t.Fatalf("table boundaries lost: %+v", m.Blocks)
	}
	if len(m.Objects) != 1 || m.Objects[0].Path != "word/media/figure.png" || m.Objects[0].Digest == "" || m.Objects[0].Missing || len(m.Blocks[3].ObjectIDs) != 1 {
		t.Fatalf("image relationship lost: %+v", m)
	}
	var state string
	if err = db.QueryRow(`SELECT text_state FROM kb_semantic_document_bindings WHERE document_id=?`, doc).Scan(&state); err != nil || state != "ready" {
		t.Fatalf("knowledge publication blocked %s %v", state, err)
	}
	t.Logf("Actual DOCX table/image manifest: %s", raw)
}

func TestMaterialPreparationAmbiguousAnswerNumbersDoNotGuess(t *testing.T) {
	_, records, worker, doc := materialImportFixture(t, "1. 4.5×2=\n1. 8÷2=\n2. 12÷3=\n参考答案\n1. 9\n")
	if _, err := worker.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	summary, err := records.GetMaterialPreparationSummary(t.Context(), "desktop-user", doc)
	if err != nil || summary.ExtractionComplete || summary.Counts["ready"] != 1 || len(summary.Items) != 1 || summary.Items[0].Stem != "12÷3=" {
		t.Fatalf("ambiguous ownership guessed: %+v %v", summary, err)
	}
}
