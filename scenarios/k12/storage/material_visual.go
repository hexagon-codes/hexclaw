package k12storage

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type materialVisualObject struct {
	ID          string `json:"object_id"`
	Path        string `json:"path"`
	StoragePath string `json:"storage_path"`
	Digest      string `json:"digest"`
	Kind        string `json:"kind"`
	Missing     bool   `json:"missing"`
}

type MaterialVisualReading struct {
	Complete    bool     `json:"complete"`
	VisualFacts []string `json:"visual_facts"`
	Issues      []string `json:"issues"`
}

type MaterialVisualReceipt struct {
	Model        k12.GradingModelSnapshot `json:"model"`
	Response     string                   `json:"response"`
	InputDigest  string                   `json:"input_digest"`
	ObjectDigest string                   `json:"object_digest"`
	Reading      MaterialVisualReading    `json:"reading"`
}

var materialImageMarkup = regexp.MustCompile(`!\[[^\]]*\](?:\([^\n]*?\)|\[[^\]]*\])|(?i:<img\b[^>]*>)`)

// MaterialVisualPromptFacts 去除图片地址标记；实际图片只经字节传给模型。
func MaterialVisualPromptFacts(facts k12.ProblemAssetFacts) k12.ProblemAssetFacts {
	facts.Stem = strings.TrimSpace(materialImageMarkup.ReplaceAllString(facts.Stem, "[Attached source image]"))
	facts.SharedMaterial = append([]string(nil), facts.SharedMaterial...)
	for i, text := range facts.SharedMaterial {
		facts.SharedMaterial[i] = materialImageMarkup.ReplaceAllString(text, "[Attached source image]")
	}
	return facts
}

type MaterialVisualEvidence struct {
	InvocationID string                `json:"invocation_id"`
	ResultDigest string                `json:"result_digest"`
	Facts        k12.ProblemAssetFacts `json:"facts"`
}

// prepareMaterialVisualCandidates 只把来源明确关联的单图交给视觉准备，不清除其他缺失项。
func prepareMaterialVisualCandidates(m MaterialManifest, candidates []MaterialCandidate) []MaterialCandidate {
	var objects []materialVisualObject
	_ = json.Unmarshal(m.Objects, &objects)
	byID := map[string]materialVisualObject{}
	for _, object := range objects {
		byID[object.ID] = object
	}
	blocks := map[string]MaterialBlock{}
	for _, block := range m.Blocks {
		blocks[block.ID] = block
	}
	for i := range candidates {
		c := &candidates[i]
		for _, id := range c.SourceBlockIDs {
			for _, object := range blocks[id].ObjectIDs {
				c.VisualObjectIDs = materialAppendUnique(c.VisualObjectIDs, object)
			}
		}
		if len(c.VisualObjectIDs) == 0 {
			continue
		}
		if len(c.VisualObjectIDs) != 1 {
			c.Issues = append(c.Issues, "Multiple source images require reliable association")
			continue
		}
		o, ok := byID[c.VisualObjectIDs[0]]
		if !ok || o.Missing || o.Kind != "image" || o.Digest == "" {
			c.Issues = append(c.Issues, "Source image is unavailable")
			continue
		}
		var remaining []string
		for _, issue := range c.Issues {
			if issue != "Object dependency requires reliable interpretation" {
				remaining = append(remaining, issue)
			}
		}
		c.Issues = remaining
		c.Facts.Objects = []k12.ProblemAssetObject{{Role: "image", Digest: o.Digest}}
	}
	return candidates
}

// MaterialSourceImage 沿同一来源代次读取保存的图片，禁止再次访问原 URL。
func (s *Store) MaterialSourceImage(ctx context.Context, p MaterialPreparation) ([]byte, string, error) {
	if len(p.Candidate.VisualObjectIDs) != 1 {
		return nil, "", ErrProblemAssetEvidence
	}
	var raw, sourcePath string
	err := s.db.QueryRowContext(ctx, `SELECT m.manifest_json,bl.storage_path FROM k12_material_manifests m JOIN kb_ingest_document_sources d ON d.owner_id=m.owner_id AND d.document_id=m.document_id AND d.content_generation=m.source_revision JOIN kb_ingest_blobs bl ON bl.owner_id=d.owner_id AND bl.corpus_uid=d.corpus_uid AND bl.sha256=d.blob_sha256 WHERE m.owner_id=? AND m.document_id=? AND m.source_revision=?`, p.OwnerID, p.DocumentID, p.SourceRevision).Scan(&raw, &sourcePath)
	if err != nil {
		return nil, "", err
	}
	var m MaterialManifest
	if err = json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, "", err
	}
	var objects []materialVisualObject
	if err = json.Unmarshal(m.Objects, &objects); err != nil {
		return nil, "", err
	}
	for _, o := range objects {
		if o.ID != p.Candidate.VisualObjectIDs[0] || o.Missing {
			continue
		}
		var data []byte
		if o.StoragePath != "" {
			data, err = os.ReadFile(o.StoragePath)
		} else {
			var archive *zip.ReadCloser
			archive, err = zip.OpenReader(sourcePath)
			if err != nil {
				return nil, "", err
			}
			defer archive.Close()
			for _, f := range archive.File {
				if f.Name == o.Path {
					var reader io.ReadCloser
					reader, err = f.Open()
					if err == nil {
						data, err = io.ReadAll(reader)
						_ = reader.Close()
					}
					break
				}
			}
		}
		if err != nil {
			return nil, "", err
		}
		if len(data) == 0 || strings.TrimPrefix(problemAssetRequestDigest(data), "sha256:") != o.Digest {
			return nil, "", ErrProblemAssetEvidence
		}
		return data, o.Digest, nil
	}
	return nil, "", ErrProblemAssetEvidence
}

func materialVisualFacts(p MaterialPreparation, receipt MaterialVisualReceipt) (k12.ProblemAssetFacts, error) {
	if receipt.InputDigest != p.InputDigest || !receipt.Reading.Complete || len(receipt.Reading.Issues) > 0 || len(receipt.Reading.VisualFacts) == 0 || (len(p.Candidate.VisualPDFPages) == 0 && (len(p.Candidate.Facts.Objects) != 1 || p.Candidate.Facts.Objects[0].Digest != receipt.ObjectDigest)) {
		return k12.ProblemAssetFacts{}, ErrProblemAssetEvidence
	}
	facts := MaterialVisualPromptFacts(p.Candidate.Facts)
	if len(p.Candidate.VisualPDFPages) > 0 {
		facts.Objects = []k12.ProblemAssetObject{{Role: "image", Digest: receipt.ObjectDigest}}
	}
	facts.VisualFacts = nil
	for _, fact := range receipt.Reading.VisualFacts {
		if strings.TrimSpace(fact) == "" {
			return facts, ErrProblemAssetEvidence
		}
		facts.VisualFacts = append(facts.VisualFacts, fact)
	}
	return facts, nil
}

func (s *Store) MaterialVisualEvidence(ctx context.Context, p MaterialPreparation, inv MaterialInvocation) (*MaterialVisualEvidence, error) {
	var receipt MaterialVisualReceipt
	if json.Unmarshal([]byte(inv.ResultJSON), &receipt) != nil {
		return nil, ErrProblemAssetEvidence
	}
	facts, err := materialVisualFacts(p, receipt)
	if err == nil {
		err = validateMaterialPDFVisualReceipt(ctx, s.db, p, receipt)
	}
	if err != nil {
		return nil, err
	}
	return &MaterialVisualEvidence{InvocationID: inv.ID, ResultDigest: problemAssetRequestDigest([]byte(inv.ResultJSON)), Facts: facts}, nil
}

// materialResultWithVisual 保留原始候选；解读证据只追加到该次解答结果。
func materialResultWithVisual(p MaterialPreparation, result string) (string, error) {
	if p.VisualEvidence == nil {
		return result, nil
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(result), &value); err != nil {
		return "", err
	}
	value["visual_evidence"] = p.VisualEvidence
	raw, err := json.Marshal(value)
	return string(raw), err
}

func materialFactsForResult(ctx context.Context, db dbHandle, p MaterialPreparation, result string) (k12.ProblemAssetFacts, error) {
	if len(p.Candidate.VisualObjectIDs) == 0 && len(p.Candidate.VisualPDFPages) == 0 {
		return p.Candidate.Facts, nil
	}
	var value struct {
		Visual *MaterialVisualEvidence `json:"visual_evidence"`
	}
	if json.Unmarshal([]byte(result), &value) != nil || value.Visual == nil {
		return k12.ProblemAssetFacts{}, ErrProblemAssetEvidence
	}
	var raw, digest string
	if err := db.QueryRowContext(ctx, `SELECT result_json,result_digest FROM k12_material_invocations WHERE invocation_id=? AND task_id=? AND operation='visual_extract' AND status='succeeded'`, value.Visual.InvocationID, p.TaskID).Scan(&raw, &digest); err != nil {
		return k12.ProblemAssetFacts{}, err
	}
	if digest != value.Visual.ResultDigest || digest != problemAssetRequestDigest([]byte(raw)) {
		return k12.ProblemAssetFacts{}, ErrProblemAssetEvidence
	}
	var receipt MaterialVisualReceipt
	if json.Unmarshal([]byte(raw), &receipt) != nil {
		return k12.ProblemAssetFacts{}, ErrProblemAssetEvidence
	}
	facts, err := materialVisualFacts(p, receipt)
	if err == nil {
		err = validateMaterialPDFVisualReceipt(ctx, db, p, receipt)
	}
	if err != nil || !reflect.DeepEqual(facts, value.Visual.Facts) {
		return facts, errors.Join(err, ErrProblemAssetEvidence)
	}
	return facts, nil
}

func validateMaterialPDFVisualReceipt(ctx context.Context, db dbHandle, p MaterialPreparation, receipt MaterialVisualReceipt) error {
	if len(p.Candidate.VisualPDFPages) == 0 {
		return nil
	}
	source, err := materialPDFVisualSource(ctx, db, p)
	if err != nil {
		return err
	}
	if source == nil || source.CompositeDigest != receipt.ObjectDigest {
		return ErrProblemAssetEvidence
	}
	return nil
}
