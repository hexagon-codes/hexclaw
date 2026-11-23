package adapter

import (
	"context"
	"time"
)

// PhotoArrival 表示已解析、尚未取图的原始平台消息。缺失或不可信的平台时刻为零。
// FrameDigest 用来识别输入是否变化；图片是否相同由各页实收字节判定。
type PhotoArrival struct {
	Message           *Message
	PageCount         int
	ReceivedAt        time.Time
	ProviderCreatedAt time.Time
	FrameDigest       string
}

// PhotoPageReader 用本次回调的临时凭据读取一页，页索引从零开始。
type PhotoPageReader func(ctx context.Context, pageIndex int) (Attachment, error)

// PhotoIntake 在回调返回前按需取图。整条消息可持久恢复才返回 true,nil；
// 出错时平台暂不确认。未读图且返回 false,nil 时交给已有接纳入口。
type PhotoIntake interface {
	ReceivePhotos(context.Context, PhotoArrival, PhotoPageReader) (handled bool, err error)
}
