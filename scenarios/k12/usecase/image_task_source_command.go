package usecase

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 单图和多图的第一页使用同一个来源唯一键。接纳第一页的事务同时冻结
// 当前页请求与整条来源消息的有序资产集合。
type imageTaskSourceCommand struct {
	rootKey       string
	orderedDigest string
	pageCount     int
	legacyPages   bool
}

func imageTaskSourceKey(in CreateImageTaskInput, sourceRef string) string {
	return fmt.Sprintf("%s:%s:g%d", in.SourceKind, sourceRef, in.AttemptGeneration)
}

func imageTaskPageRef(ref string, page int) string {
	return fmt.Sprintf("%s:image:%d", ref, page+1)
}

func (s imageTaskSourceCommand) pageKey(in CreateImageTaskInput, page int) string {
	if s.legacyPages {
		return imageTaskSourceKey(in, in.SourceRef)
	}
	if page == 0 {
		return s.rootKey
	}
	// 序号在根键之前，外部来源即便天然含有 :image:N 也不占用其他页。
	return fmt.Sprintf("image-page-v2:%d:%s", page+1, s.rootKey)
}

func imageTaskPageRequestDigest(stored k12.ImageTaskDispatch, orderedDigest string) string {
	return digestJSON(struct {
		PageRequest, OrderedAssets string
	}{imageTaskAdmissionDigest(stored), orderedDigest})
}

func sameImageTaskCreateInput(stored k12.ImageTaskDispatch, in CreateImageTaskInput) bool {
	return stored.LearnerID == in.LearnerID && stored.SourceKind == in.SourceKind &&
		stored.SourceRef == in.SourceRef && stored.SourceSessionID == in.SourceSessionID &&
		slices.Equal(stored.SourceAssetRefs, in.SourceAssetRefs) && stored.MessageIntent == in.MessageIntent &&
		stored.AttemptGeneration == in.AttemptGeneration && sameCreativeEntry(stored.CreativeEntry, in.CreativeEntry) &&
		sameCreateImageTaskRouteRequest(stored, in)
}

func hasSingleImageTaskCommand(d k12.ImageTaskDispatch) bool {
	return len(d.SourceAssetRefs) == 1 && d.RequestDigest == imageTaskPageRequestDigest(d, digestJSON(d.SourceAssetRefs))
}

// legacyImageTaskPage 只识别旧版按可见页号单独建键的页。新版单图使用整组
// 摘要可证明自身独立；新版嵌套批次第一页的真实来源也不同于这个可见页号。
func (c *ImageTaskCoordinator) legacyImageTaskPage(ctx context.Context, in CreateImageTaskInput, page int) (k12.ImageTaskDispatch, bool, error) {
	ref := imageTaskPageRef(in.SourceRef, page)
	d, err := c.Records.GetImageTaskDispatchByIdempotency(ctx, in.AgentName, imageTaskSourceKey(in, ref))
	if errors.Is(err, k12storage.ErrImageTaskNotFound) {
		return k12.ImageTaskDispatch{}, false, nil
	}
	if err != nil {
		return k12.ImageTaskDispatch{}, false, err
	}
	if d.SourceRef != ref || hasSingleImageTaskCommand(d) {
		return k12.ImageTaskDispatch{}, false, nil
	}
	return d, true, nil
}

func (c *ImageTaskCoordinator) imageTaskSourceCommand(ctx context.Context, in CreateImageTaskInput) (imageTaskSourceCommand, error) {
	cmd := imageTaskSourceCommand{
		rootKey: imageTaskSourceKey(in, in.SourceRef), orderedDigest: digestJSON(in.SourceAssetRefs),
		pageCount: len(in.SourceAssetRefs),
	}
	root, err := c.Records.GetImageTaskDispatchByIdempotency(ctx, in.AgentName, cmd.rootKey)
	rootFound := err == nil
	switch {
	case rootFound && cmd.pageCount == 1:
		if root.SourceRef == in.SourceRef {
			return cmd, nil
		}
		return cmd, k12storage.ErrImageTaskConflict
	case rootFound && root.SourceRef == imageTaskPageRef(in.SourceRef, 0):
		if root.RequestDigest != imageTaskPageRequestDigest(root, cmd.orderedDigest) {
			return cmd, k12storage.ErrImageTaskConflict
		}
		return cmd, nil
	case rootFound:
		if root.SourceRef != in.SourceRef || len(root.SourceAssetRefs) != 1 || hasSingleImageTaskCommand(root) {
			return cmd, k12storage.ErrImageTaskConflict
		}
		// 同一历史来源可能已产生单页和多页两组回执，只核对已存在的一组。
	case !errors.Is(err, k12storage.ErrImageTaskNotFound):
		return cmd, err
	}

	// 旧版逐页写入，从第一页顺序生成，无完整组摘要。只能复用已存的整组，
	// 不从新请求猜测缺页或尾部总数。已存的页任务和历史调用仍可逐个查询。
	for page := 0; ; page++ {
		stored, found, readErr := c.legacyImageTaskPage(ctx, in, page)
		if readErr != nil {
			return cmd, readErr
		}
		if !found {
			if page == 0 && !rootFound {
				return cmd, nil
			}
			if page == cmd.pageCount {
				cmd.legacyPages = true
				return cmd, nil
			}
			return cmd, k12storage.ErrImageTaskConflict
		}
		if cmd.pageCount == 1 || page == cmd.pageCount {
			return cmd, k12storage.ErrImageTaskConflict
		}
		child := in
		child.SourceRef, child.SourceAssetRefs = imageTaskPageRef(in.SourceRef, page), in.SourceAssetRefs[page:page+1]
		if !sameImageTaskCreateInput(stored, child) {
			return cmd, k12storage.ErrImageTaskConflict
		}
	}
}
