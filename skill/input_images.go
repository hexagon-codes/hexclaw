package skill

import "context"

// InputImage 只携带当前已接纳图片，不作为任意文件读取或跨会话下载授权。
type InputImage struct {
	ID   string
	Name string
	MIME string
	Data string
	URL  string
}

type inputImagesKey struct{}

// WithInputImages 冻结本轮图片；空切片清除继承的其他回合输入。
func WithInputImages(ctx context.Context, images []InputImage) context.Context {
	return context.WithValue(ctx, inputImagesKey{}, append([]InputImage(nil), images...))
}

// InputImages 返回独立列表，图片正文始终由入口提供而非模型参数生成。
func InputImages(ctx context.Context) []InputImage {
	images, _ := ctx.Value(inputImagesKey{}).([]InputImage)
	return append([]InputImage(nil), images...)
}
