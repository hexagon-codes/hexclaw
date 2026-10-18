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

var pdfStyleDigest = func() string {
	h := sha256.New()
	h.Write([]byte(pdfStyleTemplate))
	h.Write(pdfBrandImage)
	return hex.EncodeToString(h.Sum(nil))
}()

// 每次渲染使用独占临时样式；Logo仅以固定资产字节进入Typst，正文不能指定本机路径。
func (r *PandocRenderer) preparePDFStyle() (path string, err error) {
	var logo strings.Builder
	logo.WriteString("bytes((")
	for i, b := range pdfBrandImage {
		if i > 0 {
			logo.WriteByte(',')
		}
		logo.WriteString(strconv.Itoa(int(b)))
	}
	logo.WriteString("))")
	content := strings.ReplaceAll(pdfStyleTemplate, "__HC_LOGO__", logo.String())
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
