package render

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// 标准PDF模板与产物检查共用正文边界，避免排版和几何验证使用不同边距。
const (
	standardPDFMarginLeftMM   = 18
	standardPDFMarginRightMM  = 18
	standardPDFMarginTopMM    = 22
	standardPDFMarginBottomMM = 23
)

// PDF样式与品牌资产由共享渲染器持有，不依赖宿主安装路径。
//
//go:embed assets/pdf-style.typ
var pdfStyleTemplate string

//go:embed assets/pdf-brand.png
var pdfBrandImage []byte

// PDFBrandImage 返回共享页眉已有品牌图的独立副本。
func PDFBrandImage() []byte { return append([]byte(nil), pdfBrandImage...) }

// PDF使用与桌面一致的矢量工具标记，避免依赖宿主Emoji字体。
//
//go:embed assets/tool-wrench.svg
var pdfToolIcon []byte

//go:embed assets/pdf-numbered-lines.lua
var pdfNumberedLinesFilter []byte

var pdfStyleDigest = func() string {
	h := sha256.New()
	h.Write([]byte(pdfStyleTemplate))
	h.Write(pdfBrandImage)
	h.Write(pdfToolIcon)
	h.Write(pdfNumberedLinesFilter)
	h.Write([]byte(strings.Join([]string{
		strconv.Itoa(standardPDFMarginLeftMM), strconv.Itoa(standardPDFMarginRightMM),
		strconv.Itoa(standardPDFMarginTopMM), strconv.Itoa(standardPDFMarginBottomMM),
	}, ":")))
	return hex.EncodeToString(h.Sum(nil))
}()

func (r *PandocRenderer) preparePDFNumberedLinesFilter() (string, error) {
	f, err := os.CreateTemp(r.SandboxDir, "pdf-numbered-lines-*.lua")
	if err != nil {
		return "", err
	}
	if _, err = f.Write(pdfNumberedLinesFilter); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// 每次渲染使用独占临时样式；Logo仅以固定资产字节进入Typst，正文不能指定本机路径。
func (r *PandocRenderer) preparePDFStyle(opts RenderOptions) (path string, err error) {
	return r.preparePDFStyleWithImageHeight(opts, 220)
}

func (r *PandocRenderer) preparePDFStyleWithImageHeight(opts RenderOptions, imageHeightMM int) (path string, err error) {
	content := strings.ReplaceAll(pdfStyleTemplate, "__HC_HEADER_FOOTER__", strconv.FormatBool(opts.pdfHeaderFooterEnabled()))
	content = strings.ReplaceAll(content, "__HC_IMAGE_MAX_HEIGHT__", strconv.Itoa(imageHeightMM))
	content = strings.NewReplacer(
		"__HC_MARGIN_LEFT__", strconv.Itoa(standardPDFMarginLeftMM),
		"__HC_MARGIN_RIGHT__", strconv.Itoa(standardPDFMarginRightMM),
		"__HC_MARGIN_TOP__", strconv.Itoa(standardPDFMarginTopMM),
		"__HC_MARGIN_BOTTOM__", strconv.Itoa(standardPDFMarginBottomMM),
	).Replace(content)
	metadata, err := json.Marshal(opts.PDFMetadata)
	if err != nil {
		return "", err
	}
	// JSON作为字节解析，元数据里的引号、换行或Typst标记始终只是文字。
	content = strings.ReplaceAll(content, "__HC_METADATA__", "json("+typstAssetBytes(metadata)+")")
	for placeholder, asset := range map[string][]byte{"__HC_LOGO__": pdfBrandImage, "__HC_TOOL_ICON__": pdfToolIcon} {
		content = strings.ReplaceAll(content, placeholder, typstAssetBytes(asset))
	}
	f, err := os.CreateTemp(r.SandboxDir, "pdf-style-*.typ")
	if err != nil {
		return "", err
	}
	path = f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if _, err = f.WriteString(content); err != nil {
		_ = f.Close()
		return path, err
	}
	err = f.Close()
	return path, err
}

func typstAssetBytes(data []byte) string {
	var encoded strings.Builder
	encoded.WriteString("bytes((")
	for i, b := range data {
		if i > 0 {
			encoded.WriteByte(',')
		}
		encoded.WriteString(strconv.Itoa(int(b)))
	}
	encoded.WriteString("))")
	return encoded.String()
}
