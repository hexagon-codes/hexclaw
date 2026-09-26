package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hexagon-codes/hexclaw/knowledge"
)

var markdownInlineImage = regexp.MustCompile(`!\[[^\]]*\]\(\s*(<[^>]+>|[^\s]+?)(?:\s+["'][^"']*["'])?\s*\)`)
var markdownReferenceImage = regexp.MustCompile(`!\[([^\]]*)\]\[([^\]]*)\]`)
var markdownImageDefinition = regexp.MustCompile(`(?m)^\s*\[([^\]]+)\]:\s*(<[^>]+>|\S+)`)
var markdownHTMLImage = regexp.MustCompile(`(?i)<img\b[^>]*\bsrc\s*=\s*["']([^"']+)["'][^>]*>`)

// projectMarkdownSourceImages 仅投影图片目标，原文与索引继续保存来源地址。
func projectMarkdownSourceImages(text string, images map[string]string) string {
	for _, pattern := range []*regexp.Regexp{markdownInlineImage, markdownImageDefinition, markdownHTMLImage} {
		text = pattern.ReplaceAllStringFunc(text, func(match string) string {
			parts := pattern.FindStringSubmatch(match)
			index := 1
			if pattern == markdownImageDefinition {
				index = 2
			}
			target := strings.Trim(parts[index], "<>")
			if image, ok := images[target]; ok {
				return strings.Replace(match, target, image, 1)
			}
			return match
		})
	}
	return text
}

// prepareMarkdownSourceObjects 保留原正文，附件字节与来源关系独立持久化。
func prepareMarkdownSourceObjects(ctx context.Context, source knowledge.PersistedIngestDocument, text string, service *knowledge.SemanticIndexService) (*knowledge.SourceManifest, []string, func()) {
	lines := strings.Split(text, "\n")
	definitions := map[string]string{}
	for _, line := range lines {
		if m := markdownImageDefinition.FindStringSubmatch(line); len(m) > 0 {
			definitions[strings.ToLower(strings.TrimSpace(m[1]))] = strings.Trim(m[2], "<>")
		}
	}
	manifest := &knowledge.SourceManifest{SchemaVersion: 1, ParserVersion: "markdown-source-objects-v1", SourceDigest: source.SHA256}
	byURL := map[string]string{}
	blobs := map[string]knowledge.IngestBlob{}
	var warnings []string
	var releases []func()
	var once sync.Once
	release := func() {
		once.Do(func() {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
		})
	}
	fenced := false
	for i, line := range lines {
		block := knowledge.SourceContentBlock{ID: fmt.Sprintf("line:%d", i+1), Kind: "paragraph", Text: line}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			block.Incomplete = true
		}
		if markdownImageDefinition.MatchString(line) {
			block.Kind = "link_definition"
		}
		if !fenced && strings.HasPrefix(trimmed, "#") {
			block.Kind = "heading"
		}
		var urls []string
		if !fenced {
			for _, m := range markdownInlineImage.FindAllStringSubmatch(line, -1) {
				urls = append(urls, strings.Trim(m[1], "<>"))
			}
			for _, m := range markdownReferenceImage.FindAllStringSubmatch(line, -1) {
				label := m[2]
				if label == "" {
					label = m[1]
				}
				target, ok := definitions[strings.ToLower(strings.TrimSpace(label))]
				if !ok {
					target = "reference:" + label
				}
				urls = append(urls, target)
			}
			for _, m := range markdownHTMLImage.FindAllStringSubmatch(line, -1) {
				urls = append(urls, m[1])
			}
		}
		for _, target := range urls {
			id, seen := byURL[target]
			if !seen {
				sum := sha256.Sum256([]byte(target))
				id = "markdown:object:" + hex.EncodeToString(sum[:])
				byURL[target] = id
				object := knowledge.SourceContentObject{ID: id, Kind: "image", Path: target, Missing: true}
				blob, unlock, err := downloadMarkdownSourceImage(ctx, source, target, service, blobs)
				if err != nil {
					object.Issue = "Image source could not be retrieved: " + err.Error()
					warnings = append(warnings, object.Issue)
				} else {
					object.Missing = false
					object.StoragePath = blob.StoragePath
					object.Digest = blob.SHA256
					object.SizeBytes = blob.SizeBytes
					object.MediaType = blob.MediaType
					if unlock != nil {
						releases = append(releases, unlock)
					}
					blobs[blob.SHA256] = blob
				}
				manifest.Objects = append(manifest.Objects, object)
			}
			block.ObjectIDs = appendUniqueDOCX(block.ObjectIDs, id)
			manifest.Relations = append(manifest.Relations, knowledge.SourceContentRelation{Kind: "contains_image", From: block.ID, To: id})
		}
		manifest.Blocks = append(manifest.Blocks, block)
	}
	if len(manifest.Objects) == 0 {
		return nil, nil, release
	}
	return manifest, warnings, release
}

func downloadMarkdownSourceImage(ctx context.Context, source knowledge.PersistedIngestDocument, target string, service *knowledge.SemanticIndexService, blobs map[string]knowledge.IngestBlob) (knowledge.IngestBlob, func(), error) {
	if service == nil {
		return knowledge.IngestBlob{}, nil, knowledge.ErrDocumentIngestUnavailable
	}
	u, err := url.Parse(target)
	if err != nil {
		return knowledge.IngestBlob{}, nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return knowledge.IngestBlob{}, nil, fmt.Errorf("Image URL has no supported remote origin")
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, target, nil)
	if err != nil {
		return knowledge.IngestBlob{}, nil, err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return knowledge.IngestBlob{}, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return knowledge.IngestBlob{}, nil, fmt.Errorf("Image source returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, knowledge.MaxKnowledgeDocumentBytes+1))
	if err != nil {
		return knowledge.IngestBlob{}, nil, err
	}
	if int64(len(data)) > knowledge.MaxKnowledgeDocumentBytes {
		return knowledge.IngestBlob{}, nil, knowledge.ErrDocumentTooLarge
	}
	media := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if !strings.HasPrefix(media, "image/") {
		media = http.DetectContentType(data)
	}
	if !strings.HasPrefix(media, "image/") {
		return knowledge.IngestBlob{}, nil, fmt.Errorf("Image source did not return image content")
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if blob, ok := blobs[digest]; ok {
		return blob, nil, nil
	}
	return service.PersistSourceAttachment(ctx, source, media, bytes.NewReader(data))
}
