package render

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
)

// PDF样式与品牌资产由共享渲染器持有，不依赖宿主安装路径。
//
//go:embed assets/pdf-style.typ
var pdfStyleTemplate string

//go:embed assets/pdf-brand.png
var pdfBrandImage []byte

// PDF使用与桌面一致的矢量工具标记，避免依赖宿主Emoji字体。
//
//go:embed assets/tool-wrench.svg
var pdfToolIcon []byte

var pdfStyleDigest = func() string {
	h := sha256.New()
	h.Write([]byte(pdfStyleTemplate))
	h.Write(pdfBrandImage)
	h.Write(pdfToolIcon)
	return hex.EncodeToString(h.Sum(nil))
}()

// 每次渲染使用独占临时样式；Logo仅以固定资产字节进入Typst，正文不能指定本机路径。
func (r *PandocRenderer) preparePDFStyle(opts RenderOptions) (path string, err error) {
	return r.preparePDFStyleWithImageHeight(opts, 220)
}

func (r *PandocRenderer) preparePDFStyleWithImageHeight(opts RenderOptions, imageHeightMM int) (path string, err error) {
	content := strings.ReplaceAll(pdfStyleTemplate, "__HC_HEADER_FOOTER__", strconv.FormatBool(opts.pdfHeaderFooterEnabled()))
	content = strings.ReplaceAll(content, "__HC_IMAGE_MAX_HEIGHT__", strconv.Itoa(imageHeightMM))
	for placeholder, asset := range map[string][]byte{"__HC_LOGO__": pdfBrandImage, "__HC_TOOL_ICON__": pdfToolIcon} {
		var encoded strings.Builder
		encoded.WriteString("bytes((")
		for i, b := range asset {
			if i > 0 {
				encoded.WriteByte(',')
			}
			encoded.WriteString(strconv.Itoa(int(b)))
		}
		encoded.WriteString("))")
		content = strings.ReplaceAll(content, placeholder, encoded.String())
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
