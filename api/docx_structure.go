package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/hexagon-codes/hexclaw/knowledge"
)

type docxNode struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Text     string
	Children []*docxNode
}

func (n *docxNode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
func (n *docxNode) child(name string) *docxNode {
	for _, c := range n.Children {
		if c.Name.Local == name {
			return c
		}
	}
	return nil
}
func (n *docxNode) walk(fn func(*docxNode)) {
	fn(n)
	for _, c := range n.Children {
		c.walk(fn)
	}
}
func parseDOCXNode(data []byte) (*docxNode, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	root := &docxNode{}
	stack := []*docxNode{root}
	for {
		token, err := d.Token()
		if err == io.EOF {
			return root, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			n := &docxNode{Name: t.Name, Attrs: t.Attr}
			p := stack[len(stack)-1]
			p.Children = append(p.Children, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].Text += string(t)
		}
	}
}

type docxNumberLevel struct {
	Format, Text string
	Start        int
}
type docxStructureParser struct {
	files      map[string]*zip.File
	rels       map[string]docxRelationship
	levels     map[string]map[int]docxNumberLevel
	counters   map[string]map[int]int
	manifest   *knowledge.SourceManifest
	objects    map[string]bool
	blockCount int
}
type docxRelationship struct {
	Target   string
	External bool
}

func (p *docxStructureParser) read(name string) ([]byte, error) {
	f := p.files[name]
	if f == nil {
		return nil, nil
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readDOCXXMLLimited(r, 100<<20)
}

// extractDOCXStructure 保留 OOXML 实际段落顺序、列表编号、表格单元格及媒体关系。
// 图片本体仍在原文件中；结构快照记录可定位路径和摘要，不创建第二份附件事实源。
func extractDOCXStructure(data []byte) (string, *knowledge.SourceManifest, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", nil, err
	}
	p := &docxStructureParser{files: map[string]*zip.File{}, rels: map[string]docxRelationship{}, levels: map[string]map[int]docxNumberLevel{}, counters: map[string]map[int]int{}, objects: map[string]bool{}, manifest: &knowledge.SourceManifest{SchemaVersion: 1, ParserVersion: "docx-structure-v1"}}
	for _, f := range zr.File {
		p.files[f.Name] = f
	}
	raw, err := p.read("word/document.xml")
	if err != nil {
		return "", nil, err
	}
	if len(raw) == 0 {
		return "", nil, nil
	}
	root, err := parseDOCXNode(raw)
	if err != nil {
		return "", nil, err
	}
	if err = p.loadRelationships(); err != nil {
		return "", nil, err
	}
	if err = p.loadNumbering(); err != nil {
		return "", nil, err
	}
	var body *docxNode
	root.walk(func(n *docxNode) {
		if n.Name.Local == "body" {
			body = n
		}
	})
	if body == nil {
		return "", nil, fmt.Errorf("DOCX document body is missing")
	}
	var texts []string
	for _, n := range body.Children {
		for _, b := range p.blocks(n) {
			p.manifest.Blocks = append(p.manifest.Blocks, b)
			if b.Text != "" {
				texts = append(texts, b.Text)
			}
		}
	}
	return strings.Join(texts, "\n\n"), p.manifest, nil
}

func (p *docxStructureParser) loadRelationships() error {
	raw, err := p.read("word/_rels/document.xml.rels")
	if err != nil || len(raw) == 0 {
		return err
	}
	root, err := parseDOCXNode(raw)
	if err != nil {
		return err
	}
	root.walk(func(n *docxNode) {
		if n.Name.Local == "Relationship" {
			p.rels[n.attr("Id")] = docxRelationship{Target: n.attr("Target"), External: n.attr("TargetMode") == "External"}
		}
	})
	return nil
}

func (p *docxStructureParser) loadNumbering() error {
	raw, err := p.read("word/numbering.xml")
	if err != nil || len(raw) == 0 {
		return err
	}
	root, err := parseDOCXNode(raw)
	if err != nil {
		return err
	}
	abstract := map[string]map[int]docxNumberLevel{}
	root.walk(func(n *docxNode) {
		if n.Name.Local != "abstractNum" {
			return
		}
		levels := map[int]docxNumberLevel{}
		for _, c := range n.Children {
			if c.Name.Local != "lvl" {
				continue
			}
			level, _ := strconv.Atoi(c.attr("ilvl"))
			value := docxNumberLevel{Start: 1}
			if x := c.child("numFmt"); x != nil {
				value.Format = x.attr("val")
			}
			if x := c.child("lvlText"); x != nil {
				value.Text = x.attr("val")
			}
			if x := c.child("start"); x != nil {
				value.Start, _ = strconv.Atoi(x.attr("val"))
			}
			levels[level] = value
		}
		abstract[n.attr("abstractNumId")] = levels
	})
	root.walk(func(n *docxNode) {
		if n.Name.Local != "num" {
			return
		}
		a := n.child("abstractNumId")
		if a == nil {
			return
		}
		levels := map[int]docxNumberLevel{}
		for k, v := range abstract[a.attr("val")] {
			levels[k] = v
		}
		for _, c := range n.Children {
			if c.Name.Local == "lvlOverride" {
				i, _ := strconv.Atoi(c.attr("ilvl"))
				v := levels[i]
				if c.child("lvl") != nil {
					v.Format = "unsupported_override"
				}
				if x := c.child("startOverride"); x != nil {
					v.Start, _ = strconv.Atoi(x.attr("val"))
				}
				levels[i] = v
			}
		}
		p.levels[n.attr("numId")] = levels
	})
	return nil
}

func (p *docxStructureParser) nextID() string {
	p.blockCount++
	return fmt.Sprintf("docx:block:%d", p.blockCount)
}
func (p *docxStructureParser) blocks(n *docxNode) []knowledge.SourceContentBlock {
	switch n.Name.Local {
	case "p":
		return []knowledge.SourceContentBlock{p.paragraph(n)}
	case "tbl":
		return []knowledge.SourceContentBlock{p.table(n)}
	case "sectPr":
		return nil
	case "altChunk", "object", "oMath", "oMathPara":
		return []knowledge.SourceContentBlock{{ID: p.nextID(), Kind: "unsupported", Incomplete: true}}
	default:
		var blocks []knowledge.SourceContentBlock
		for _, child := range n.Children {
			blocks = append(blocks, p.blocks(child)...)
		}
		return blocks
	}
}
func (p *docxStructureParser) paragraph(n *docxNode) knowledge.SourceContentBlock {
	b := knowledge.SourceContentBlock{ID: p.nextID(), Kind: "paragraph"}
	var text strings.Builder
	var walk func(*docxNode)
	walk = func(c *docxNode) {
		switch c.Name.Local {
		case "del":
			b.Incomplete = true
			return
		case "t":
			text.WriteString(c.Text)
		case "tab":
			text.WriteString("\t")
		case "br", "cr":
			text.WriteString("\n")
		case "oMath", "oMathPara":
			b.Incomplete = true
		case "footnoteReference", "endnoteReference", "altChunk", "object", "chart":
			b.Incomplete = true
		case "blip", "imagedata":
			id := c.attr("embed")
			if id == "" {
				id = c.attr("id")
			}
			if id == "" {
				id = c.attr("link")
			}
			if id != "" {
				object := p.image(id)
				b.ObjectIDs = appendUniqueDOCX(b.ObjectIDs, object)
				p.manifest.Relations = append(p.manifest.Relations, knowledge.SourceContentRelation{Kind: "contains_image", From: b.ID, To: object})
			} else {
				b.Incomplete = true
			}
		}
		for _, child := range c.Children {
			walk(child)
		}
	}
	walk(n)
	b.Text = strings.TrimSpace(text.String())
	if pr := n.child("pPr"); pr != nil {
		if style := pr.child("pStyle"); style != nil && strings.HasPrefix(strings.ToLower(style.attr("val")), "heading") {
			b.Kind = "heading"
		}
		if num := pr.child("numPr"); num != nil {
			b.Kind = "list_item"
			if id := num.child("numId"); id != nil {
				b.ListID = id.attr("val")
			}
			if level := num.child("ilvl"); level != nil {
				b.ListLevel, _ = strconv.Atoi(level.attr("val"))
			}
			definition, ok := p.levels[b.ListID][b.ListLevel]
			simpleNumber := definition.Text == "%1." || definition.Text == "%1、" || definition.Text == "%1)" || definition.Text == "%1"
			if !ok || definition.Format != "decimal" || b.ListLevel != 0 || !simpleNumber {
				b.Incomplete = true
				b.Text = "• " + b.Text
			} else {
				if p.counters[b.ListID] == nil {
					p.counters[b.ListID] = map[int]int{}
				}
				value, seen := p.counters[b.ListID][b.ListLevel]
				if !seen {
					value = definition.Start
				} else {
					value++
				}
				p.counters[b.ListID][b.ListLevel] = value
				b.Text = strconv.Itoa(value) + ". " + b.Text
			}
		}
	}
	return b
}
func (p *docxStructureParser) table(n *docxNode) knowledge.SourceContentBlock {
	b := knowledge.SourceContentBlock{ID: p.nextID(), Kind: "table"}
	row := 0
	var lines []string
	for _, tr := range n.Children {
		if tr.Name.Local != "tr" {
			continue
		}
		row++
		column := 1
		var values []string
		for _, tc := range tr.Children {
			if tc.Name.Local != "tc" {
				continue
			}
			cell := knowledge.SourceTableCell{Row: row, Column: column, ColumnSpan: 1}
			if pr := tc.child("tcPr"); pr != nil {
				if span := pr.child("gridSpan"); span != nil {
					if v, e := strconv.Atoi(span.attr("val")); e == nil && v > 0 {
						cell.ColumnSpan = v
					}
				}
				if merge := pr.child("vMerge"); merge != nil {
					cell.VerticalMerge = merge.attr("val")
					if cell.VerticalMerge == "" {
						cell.VerticalMerge = "continue"
					}
				}
			}
			var parts []string
			for _, child := range tc.Children {
				if child.Name.Local == "tcPr" {
					continue
				}
				for _, block := range p.blocks(child) {
					cell.Blocks = append(cell.Blocks, block)
					parts = append(parts, block.Text)
					b.ObjectIDs = append(b.ObjectIDs, block.ObjectIDs...)
					p.manifest.Relations = append(p.manifest.Relations, knowledge.SourceContentRelation{Kind: "table_cell", From: b.ID, To: block.ID})
				}
			}
			values = append(values, strings.Join(parts, " / "))
			b.Cells = append(b.Cells, cell)
			column += cell.ColumnSpan
		}
		lines = append(lines, "| "+strings.Join(values, " | ")+" |")
	}
	b.Text = strings.Join(lines, "\n")
	return b
}
func (p *docxStructureParser) image(id string) string {
	objectID := "docx:object:" + id
	if p.objects[id] {
		return objectID
	}
	p.objects[id] = true
	rel, ok := p.rels[id]
	o := knowledge.SourceContentObject{ID: objectID, Kind: "image", Missing: !ok || rel.External}
	if rel.External {
		o.Path = rel.Target
	} else {
		o.Path = path.Clean(path.Join("word", rel.Target))
		if strings.HasPrefix(rel.Target, "/") {
			o.Path = strings.TrimPrefix(rel.Target, "/")
		}
	}
	if !o.Missing {
		if f := p.files[o.Path]; f != nil {
			if r, err := f.Open(); err == nil {
				hash := sha256.New()
				_, err = io.Copy(hash, r)
				r.Close()
				if err == nil {
					o.Digest = hex.EncodeToString(hash.Sum(nil))
				} else {
					o.Missing = true
				}
			} else {
				o.Missing = true
			}
		} else {
			o.Missing = true
		}
	}
	p.manifest.Objects = append(p.manifest.Objects, o)
	return objectID
}
func appendUniqueDOCX(values []string, value string) []string {
	for _, old := range values {
		if old == value {
			return values
		}
	}
	return append(values, value)
}
