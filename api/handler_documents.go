package api

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// DocumentExtractResponse /documents/extract 的返回体。
type DocumentExtractResponse struct {
	Text      string   `json:"text"`
	FileName  string   `json:"file_name"`
	PageCount int      `json:"page_count,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// handleExtractDocument 把上传文档解析为纯文本，返回文本供前端注入对话上下文。无状态、不入库。
//
// 格式路由（见 docextract.go）：
//   - .pdf  → poppler pdftotext（CJK 完整，降级 hexagon）
//   - .doc  → textutil或antiword（OLE 二进制文本层）
//   - .docx → extractDocxText（OOXML，知识库同款）
//   - .pptx → hexagon PPTXLoader
//   - .html/.htm/.odt/.rtf/.epub → Pandoc固定reader提取文本层
//   - .txt/.md/.csv/.json → 直接按 UTF-8 文本
//
// 注：.xlsx/.xls 由前端 SheetJS 解析（唯一能解老 .xls 且更强），不走本端点。
func (s *Server) handleExtractDocument(w http.ResponseWriter, r *http.Request) {
	const (
		maxUpload          = maxChatFileBytes
		maxMultipartMemory = 8 << 20
	)
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+(1<<20))
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "解析上传失败: " + err.Error()})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未找到上传文件"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUpload+1))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取文件失败: " + err.Error()})
		return
	}
	if int64(len(data)) > maxUpload {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("文件过大，最大允许 %dMB", maxUpload>>20)})
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	var extracted documentExtractionResult
	switch ext {
	case ".pdf", ".doc", ".docx", ".pptx", ".txt", ".md", ".csv", ".json":
		extracted, err = extractDocumentForKnowledge(r.Context(), ext, data, s.kb)
	case ".html", ".htm", ".odt", ".rtf", ".epub":
		extracted.Text, err = extractPandocDocumentText(r.Context(), ext, data)
		if err == nil {
			extracted.Warnings = append(extracted.Warnings, "Only the document text was extracted; embedded images in this format are not visually analyzed.")
		}
	case ".pages", ".numbers", ".key":
		// 苹果 iWork 私有格式（现代版为压缩 Protobuf，无可靠解析方案）——引导导出 PDF。
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "苹果 " + strings.TrimPrefix(ext, ".") + " 格式暂不支持直接解析，请在 Pages/Numbers/Keynote 里「文件 → 导出为 → PDF」后再上传",
		})
		return
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Unsupported document format: " + ext + " (supported: .pdf / .doc / .docx / .pptx / .html / .htm / .odt / .rtf / .epub / .txt / .md / .csv / .json; spreadsheets: .xlsx/.xls)",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "解析文档失败: " + err.Error()})
		return
	}
	if strings.TrimSpace(extracted.Text) == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    "未能从文件中提取到文本（可能是扫描件或纯图片）；如需解析视觉内容，请配置视觉模型 / VLM",
			"warnings": extracted.Warnings,
		})
		return
	}

	writeJSON(w, http.StatusOK, DocumentExtractResponse{
		Text:      extracted.Text,
		FileName:  header.Filename,
		PageCount: extracted.PageCount,
		Warnings:  extracted.Warnings,
	})
}
