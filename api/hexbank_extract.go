package api

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/hexagon-codes/hexclaw/knowledge"
)

type hexbankManifest struct {
	SchemaVersion int                             `json:"schema_version"`
	Questions     string                          `json:"questions"`
	Objects       []knowledge.SourceContentObject `json:"objects"`
}
type hexbankQuestion struct {
	SchemaVersion   int                              `json:"schema_version"`
	ID              string                           `json:"id"`
	Subject         string                           `json:"subject"`
	Stem            string                           `json:"stem"`
	SharedMaterial  []string                         `json:"shared_material"`
	Options         []knowledge.SourceQuestionOption `json:"options"`
	ReferenceAnswer string                           `json:"reference_answer"`
	GradeTerm       string                           `json:"grade_term"`
	ObjectIDs       []string                         `json:"object_ids"`
	Formulas        []string                         `json:"formulas"`
	Source          struct {
		Label    string `json:"label"`
		Page     int    `json:"page"`
		Location string `json:"location"`
	} `json:"source"`
}

// extractQuestionExchange 只解析原包和结构化记录；包内身份及 verified 声明不会变成服务端资格。
func extractQuestionExchange(ext string, data []byte) (documentExtractionResult, error) {
	m := &knowledge.SourceManifest{SchemaVersion: 1, ParserVersion: "question-exchange-v1"}
	res := documentExtractionResult{SourceManifest: m}
	jsonl := data
	if ext == ".hexbank" {
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return res, fmt.Errorf("invalid hexbank archive: %w", err)
		}
		files := map[string]*zip.File{}
		duplicates := map[string]bool{}
		for _, f := range archive.File {
			if files[f.Name] != nil {
				duplicates[f.Name] = true
			}
			files[f.Name] = f
		}
		if duplicates["manifest.json"] {
			return res, fmt.Errorf("hexbank manifest is ambiguous")
		}
		raw, err := readExchangeFile(files["manifest.json"])
		if err != nil {
			return res, err
		}
		var descriptor hexbankManifest
		if err = json.Unmarshal(raw, &descriptor); err != nil {
			return res, fmt.Errorf("invalid hexbank manifest: %w", err)
		}
		if descriptor.SchemaVersion != 1 {
			return res, fmt.Errorf("unsupported hexbank schema version %d", descriptor.SchemaVersion)
		}
		if !exchangeRelativePath(descriptor.Questions) || duplicates[descriptor.Questions] {
			return res, fmt.Errorf("hexbank questions must identify one relative JSONL file")
		}
		jsonl, err = readExchangeFile(files[descriptor.Questions])
		if err != nil {
			return res, err
		}
		ids := map[string]int{}
		for _, source := range descriptor.Objects {
			o := knowledge.SourceContentObject{ID: source.ID, Path: source.Path, Kind: source.Kind}
			if o.ID == "" || !exchangeRelativePath(o.Path) || duplicates[o.Path] {
				o.Missing = true
			} else if f := files[o.Path]; f != nil {
				r, e := f.Open()
				if e != nil {
					o.Missing = true
				} else {
					hash := sha256.New()
					_, e = io.Copy(hash, r)
					_ = r.Close()
					if e != nil {
						o.Missing = true
					} else {
						o.Digest = hex.EncodeToString(hash.Sum(nil))
						o.Missing = source.Digest != "" && !strings.EqualFold(source.Digest, o.Digest)
					}
				}
			} else {
				o.Missing = true
			}
			ids[o.ID]++
			m.Objects = append(m.Objects, o)
		}
		for i := range m.Objects {
			if ids[m.Objects[i].ID] != 1 {
				m.Objects[i].Missing = true
			}
		}
	}
	objects := map[string]knowledge.SourceContentObject{}
	for _, o := range m.Objects {
		objects[o.ID] = o
	}
	reader := bufio.NewReader(bytes.NewReader(jsonl))
	line := 0
	recordIDs := map[string]int{}
	for {
		raw, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return res, err
		}
		line++
		if len(bytes.TrimSpace(raw)) > 0 {
			id := fmt.Sprintf("exchange:line:%d", line)
			var record hexbankQuestion
			q := knowledge.SourceQuestion{BlockID: id, Line: line}
			if decodeErr := json.Unmarshal(raw, &record); decodeErr != nil {
				q.Issues = append(q.Issues, "Question record is not valid JSON")
			}
			q.RecordID = record.ID
			q.SourceLabel = record.Source.Label
			q.SourceLocation = record.Source.Location
			q.SourcePage = record.Source.Page
			q.Facts = knowledge.SourceQuestionFacts{Subject: strings.TrimSpace(record.Subject), Stem: strings.TrimSpace(record.Stem), SharedMaterial: record.SharedMaterial, Options: record.Options, VisualFacts: record.Formulas, AnswerContext: map[string]string{"grade_term": record.GradeTerm}}
			q.ReferenceAnswer = strings.TrimSpace(record.ReferenceAnswer)
			if record.SchemaVersion != 1 {
				q.Issues = append(q.Issues, fmt.Sprintf("Unsupported question schema version %d", record.SchemaVersion))
			}
			if record.ID == "" {
				q.Issues = append(q.Issues, "Question source ID is missing")
			}
			recordIDs[record.ID]++
			if q.Facts.Subject == "" || q.Facts.Stem == "" {
				q.Issues = append(q.Issues, "Question subject or stem is missing")
			}
			optionLabels := map[string]bool{}
			for _, option := range record.Options {
				if option.Label == "" || strings.TrimSpace(option.Text) == "" || optionLabels[option.Label] {
					q.Issues = append(q.Issues, "Question options are incomplete or ambiguous")
				}
				optionLabels[option.Label] = true
			}
			for _, shared := range record.SharedMaterial {
				if strings.TrimSpace(shared) == "" {
					q.Issues = append(q.Issues, "Shared material is incomplete")
				}
			}
			for _, objectID := range record.ObjectIDs {
				o, ok := objects[objectID]
				if !ok || o.Missing {
					q.Issues = append(q.Issues, "Required object is missing or its digest does not match")
				} else {
					q.Issues = append(q.Issues, "Object dependency requires reliable interpretation")
				}
				if ok {
					q.Facts.Objects = append(q.Facts.Objects, knowledge.SourceQuestionObject{Role: o.Kind, Digest: o.Digest})
				}
				m.Relations = append(m.Relations, knowledge.SourceContentRelation{Kind: "question_object", From: id, To: objectID})
			}
			if len(record.Formulas) > 0 {
				q.Issues = append(q.Issues, "Formula dependencies require reliable interpretation")
			}
			if exchangeUnresolvedTextDependency(record) {
				q.Issues = append(q.Issues, "Question references material that is not reliably bound")
			}
			if q.Facts.Stem == "" {
				q.Facts.Stem = fmt.Sprintf("Unparsed question at line %d", line)
			}
			b := knowledge.SourceContentBlock{ID: id, Kind: "question", Text: exchangeQuestionText(q), ObjectIDs: record.ObjectIDs, Incomplete: len(q.Issues) > 0}
			m.Blocks = append(m.Blocks, b)
			m.Questions = append(m.Questions, q)
			if q.ReferenceAnswer != "" {
				answerID := id + ":answer"
				m.Blocks = append(m.Blocks, knowledge.SourceContentBlock{ID: answerID, Kind: "reference_answer", Text: q.ReferenceAnswer})
				m.Relations = append(m.Relations, knowledge.SourceContentRelation{Kind: "answer_for", From: answerID, To: id})
			}
		}
		if err == io.EOF {
			break
		}
	}
	if len(m.Questions) == 0 {
		return res, fmt.Errorf("question exchange contains no records")
	}
	for i := range m.Questions {
		q := &m.Questions[i]
		if recordIDs[q.RecordID] > 1 {
			q.Issues = append(q.Issues, "Question source ID is duplicated")
		}
		if len(q.Issues) > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("Question line %d: %s", q.Line, strings.Join(q.Issues, "; ")))
		}
		for j := range m.Blocks {
			if m.Blocks[j].ID == q.BlockID {
				m.Blocks[j].Incomplete = len(q.Issues) > 0
			}
		}
	}
	var texts []string
	for _, block := range m.Blocks {
		if block.Kind == "reference_answer" {
			texts = append(texts, "Reference answer: "+block.Text)
		} else {
			texts = append(texts, block.Text)
		}
	}
	res.Text = strings.Join(texts, "\n\n")
	return res, nil
}
func readExchangeFile(file *zip.File) ([]byte, error) {
	if file == nil {
		return nil, fmt.Errorf("required hexbank file is missing")
	}
	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	// 题目正文沿用现有文本解析内存预算，原包仍完整保留。
	data, err := io.ReadAll(io.LimitReader(r, maxAsyncTextSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxAsyncTextSourceBytes {
		return nil, fmt.Errorf("question exchange text exceeds existing ingest memory budget")
	}
	return data, nil
}
func exchangeRelativePath(name string) bool {
	return name != "" && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}
func exchangeQuestionText(q knowledge.SourceQuestion) string {
	var parts []string
	parts = append(parts, q.Facts.SharedMaterial...)
	parts = append(parts, q.Facts.Stem)
	for _, option := range q.Facts.Options {
		parts = append(parts, option.Label+". "+option.Text)
	}
	parts = append(parts, q.Facts.VisualFacts...)
	return strings.Join(parts, "\n")
}

func exchangeUnresolvedTextDependency(q hexbankQuestion) bool {
	text := q.Stem + "\n" + strings.Join(q.SharedMaterial, "\n")
	for _, marker := range []string{"![", "<img", "<svg", "如图", "下图", "上图", "图中"} {
		if strings.Contains(text, marker) && len(q.ObjectIDs) == 0 {
			return true
		}
	}
	for _, marker := range []string{"下表", "上表", "根据表格"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	for _, marker := range []string{"根据材料", "上述", "上题", "同上"} {
		if strings.Contains(q.Stem, marker) && len(q.SharedMaterial) == 0 {
			return true
		}
	}
	for _, marker := range []string{"下列", "选项"} {
		if strings.Contains(q.Stem, marker) && len(q.Options) == 0 {
			return true
		}
	}
	return false
}
