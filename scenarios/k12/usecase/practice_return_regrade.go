package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
)

const PracticeReturnGradingSourceKind = "practice_return"

// PracticeReturnGrading is the narrow internal seam used by automatic
// practice-return regrading. The public client never addresses a GradingJob.
type PracticeReturnGrading interface {
	StartPhotoGradingJob(context.Context, StartPhotoGradingInput) (GradingJobView, bool, error)
	RunGradingJob(context.Context, string) (GradingJobView, error)
	PhotoResult(string) (PhotoGradeResult, bool)
	RecognizedQuestionsForOwner(context.Context, string, string) ([]RecognizedQuestion, bool)
}

// PracticeReturnRegradeCoordinator joins immutable PracticeReturnAsset evidence
// to the existing durable GradingJob. SQLite/PracticeSet remains the source of
// truth; the in-process map only prevents concurrent duplicate workers.
type PracticeReturnRegradeCoordinator struct {
	Deps        *Deps
	Grading     PracticeReturnGrading
	BaseContext context.Context

	mu          sync.Mutex
	active      map[string]bool
	sealed      bool
	workerCount int
	workerIdle  chan struct{}
	runCtx      context.Context
	runCancel   context.CancelFunc
}

var ErrPracticeReturnRegradeShutdown = errors.New("practice return regrade coordinator is shut down")

type practiceReturnRegradeProjection struct {
	JobID            string
	Status           string
	RouteSnapshot    k12.GradingModelSnapshot
	ReplaceResult    bool
	AnnotatedAssetID string
	ResultMarkdown   string
	Unresolved       []string
	Covered          []string
}

func (c *PracticeReturnRegradeCoordinator) initLocked() {
	if c.active == nil {
		c.active = make(map[string]bool)
	}
	if c.runCtx == nil {
		base := c.BaseContext
		if base == nil {
			base = context.Background()
		}
		c.runCtx, c.runCancel = context.WithCancel(base)
	}
	if c.workerIdle == nil {
		c.workerIdle = make(chan struct{})
		close(c.workerIdle)
	}
}

func practiceReturnWorkerKey(agentName, setID, returnID string) string {
	return agentName + "\x00" + setID + "\x00" + returnID
}

func (c *PracticeReturnRegradeCoordinator) validate() error {
	if c == nil || c.Deps == nil || c.Deps.Records == nil || c.Grading == nil {
		return fmt.Errorf("%w: automatic regrade dependencies are incomplete", ErrInvalidInput)
	}
	return nil
}

// StartAsync schedules an already-persisted return. It never creates evidence
// and therefore cannot lose the command if the process exits before this call.
func (c *PracticeReturnRegradeCoordinator) StartAsync(agentName, setID, returnID string) bool {
	if c == nil || c.validate() != nil {
		return false
	}
	key := practiceReturnWorkerKey(agentName, setID, returnID)
	c.mu.Lock()
	c.initLocked()
	if c.sealed || c.active[key] {
		c.mu.Unlock()
		return false
	}
	c.active[key] = true
	if c.workerCount == 0 {
		c.workerIdle = make(chan struct{})
	}
	c.workerCount++
	runCtx := c.runCtx
	c.mu.Unlock()

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("K12 练习回传自动复批 worker panic",
					"agent", agentName, "set", setID, "return", returnID, "panic", recovered)
			}
			c.mu.Lock()
			delete(c.active, key)
			c.workerCount--
			if c.workerCount == 0 {
				close(c.workerIdle)
			}
			c.mu.Unlock()
		}()
		if err := c.Process(runCtx, agentName, setID, returnID); err != nil &&
			!errors.Is(err, context.Canceled) {
			slog.Warn("K12 练习回传自动复批未完成",
				"agent", agentName, "set", setID, "return", returnID, "err", err)
		}
	}()
	return true
}

// Process advances one durable return. Re-entry is idempotent: the same
// return_id binds the same GradingJob route and system conclusions are replayed
// without repeating review projection.
func (c *PracticeReturnRegradeCoordinator) Process(
	ctx context.Context,
	agentName, setID, returnID string,
) error {
	if err := c.validate(); err != nil {
		return err
	}
	view, ret, err := c.loadReturn(ctx, agentName, setID, returnID)
	if err != nil {
		return err
	}
	if ret.RegradeStatus == k12.PracticeRegradeCompleted {
		return nil
	}
	matchingIDs := ret.ItemIDs
	if ret.AutoMatch {
		matchingIDs = ret.CandidateItemIDs
	}
	references, paperSize, err := freezePracticeGradingReferences(view.Fields, matchingIDs)
	if err != nil {
		return c.projectFailure(ctx, agentName, setID, returnID, ret,
			k12.PracticeRegradeFailedTerminal, err)
	}
	path, err := assetstore.PathFromID(ret.AssetID)
	if err != nil {
		return c.projectFailure(ctx, agentName, setID, returnID, ret,
			k12.PracticeRegradeFailedTerminal, err)
	}
	image, err := os.ReadFile(path)
	if err != nil {
		return c.projectFailure(ctx, agentName, setID, returnID, ret,
			k12.PracticeRegradeFailedTerminal, err)
	}
	job, _, err := c.Grading.StartPhotoGradingJob(ctx, StartPhotoGradingInput{
		Photo: PhotoGradeRequest{
			AgentName:          agentName,
			SourceSession:      view.Record.SourceSession,
			Image:              image,
			TaskIntent:         PhotoTaskCompletedHomework,
			PracticeReferences: references, PracticePaperSize: paperSize,
		},
		SourceKind: PracticeReturnGradingSourceKind,
		SourceKey:  setID + ":" + returnID,
	})
	if err != nil {
		return c.projectFailure(ctx, agentName, setID, returnID, ret,
			k12.PracticeRegradeFailedTerminal, err)
	}
	projection := practiceReturnRegradeProjection{
		JobID: job.Record.RecordID, Status: k12.PracticeRegradeRunning,
		RouteSnapshot: job.Fields.ModelSnapshot,
	}
	if err := c.updateProjection(ctx, agentName, setID, returnID, projection); err != nil {
		return err
	}

	for {
		job, runErr := c.Grading.RunGradingJob(ctx, job.Record.RecordID)
		switch job.Record.Status {
		case k12.GradingStageCompleted:
			if err := c.projectCompleted(ctx, agentName, setID, returnID, references, paperSize, job); err != nil {
				return c.projectFailure(ctx, agentName, setID, returnID, ret,
					k12.PracticeRegradeFailedRetryable, err)
			}
			return nil
		case k12.GradingStageAwaitingConfirmation:
			if job.Fields.ConfirmationState == k12.GradingConfirmationPending {
				questions, _ := c.Grading.RecognizedQuestionsForOwner(
					ctx, agentName, job.Record.RecordID,
				)
				reviewReferences := references
				var covered []string
				if ret.AutoMatch {
					reviewReferences, covered, _ = matchedPracticeReturnCoverage(references, paperSize, questions)
				}
				unresolved := unresolvedPracticeReturnItems(reviewReferences, paperSize, questions)
				return c.updateProjection(ctx, agentName, setID, returnID,
					practiceReturnRegradeProjection{
						JobID: job.Record.RecordID, Status: k12.PracticeRegradeNeedsReview,
						RouteSnapshot: job.Fields.ModelSnapshot, ReplaceResult: true,
						Unresolved: unresolved, Covered: covered,
					})
			}
			// Clear recognition has already auto-frozen but the independent
			// anchor branch may still be running. Keep the same worker/job and
			// wait for its explicit terminal state instead of mislabelling it.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
				continue
			}
		case k12.GradingStageOutcomeUnknown:
			return c.projectFailure(ctx, agentName, setID, returnID, ret,
				k12.PracticeRegradeOutcomeUnknown, runErr)
		case k12.GradingStageFailedRetryable:
			return c.projectFailure(ctx, agentName, setID, returnID, ret,
				k12.PracticeRegradeFailedRetryable, runErr)
		case k12.GradingStageFailedTerminal, k12.GradingStageCancelled:
			return c.projectFailure(ctx, agentName, setID, returnID, ret,
				k12.PracticeRegradeFailedTerminal, runErr)
		default:
			if runErr != nil {
				return c.projectFailure(ctx, agentName, setID, returnID, ret,
					k12.PracticeRegradeFailedRetryable, runErr)
			}
		}
		if runErr != nil {
			return runErr
		}
	}
}

func (c *PracticeReturnRegradeCoordinator) projectCompleted(
	ctx context.Context,
	agentName, setID, returnID string,
	references []PracticeGradingReference,
	paperSize int,
	job GradingJobView,
) error {
	result, ok := c.Grading.PhotoResult(job.Record.RecordID)
	if !ok {
		return c.updateProjection(ctx, agentName, setID, returnID,
			practiceReturnRegradeProjection{
				JobID: job.Record.RecordID, Status: k12.PracticeRegradeOutcomeUnknown,
				RouteSnapshot: job.Fields.ModelSnapshot,
			})
	}
	_, ret, err := c.loadReturn(ctx, agentName, setID, returnID)
	if err != nil {
		return err
	}
	var covered []string
	unmatched := false
	if ret.AutoMatch {
		questions := make([]RecognizedQuestion, len(result.Items))
		for i := range result.Items {
			questions[i] = result.Items[i].Recognized
		}
		references, covered, unmatched = matchedPracticeReturnCoverage(references, paperSize, questions)
		// 实际照片覆盖先持久化；复批结论不得反向伪造 Returned。
		if err := c.updateProjection(ctx, agentName, setID, returnID, practiceReturnRegradeProjection{
			JobID: job.Record.RecordID, Covered: covered,
		}); err != nil {
			return err
		}
	}
	grades, unresolved := alignedPracticeReturnResults(references, paperSize, result.Items)
	if len(grades) > 0 {
		if _, err := c.Deps.GradePracticeSetItems(ctx, agentName, setID, grades); err != nil {
			return err
		}
	}
	annotatedAssetID := ""
	if result.AnnotatedImage != nil && len(result.AnnotatedImage.Data) > 0 {
		saved, err := assetstore.Save(agentName, result.AnnotatedImage.Data)
		if err != nil {
			return err
		}
		annotatedAssetID = saved
	}
	status := k12.PracticeRegradeCompleted
	if len(unresolved) > 0 || unmatched || (ret.AutoMatch && len(covered) == 0) {
		status = k12.PracticeRegradeNeedsReview
	}
	markdown := result.Markdown
	for _, item := range result.Items {
		if photoPracticeUnmatched(item) {
			// 匹配失败的汇总沿用当前共享投影，其他既有结果和冻结判定保持不变。
			markdown = photoGradeMarkdown(result)
			break
		}
	}
	return c.updateProjection(ctx, agentName, setID, returnID,
		practiceReturnRegradeProjection{
			JobID: job.Record.RecordID, Status: status,
			RouteSnapshot:    job.Fields.ModelSnapshot,
			ReplaceResult:    true,
			AnnotatedAssetID: annotatedAssetID,
			ResultMarkdown:   markdown,
			Unresolved:       unresolved,
			Covered:          covered,
		})
}

// matchedPracticeReturnCoverage 只投影实际出现且可唯一绑定的题目，不把候选中的缺页算作歧义。
func matchedPracticeReturnCoverage(references []PracticeGradingReference, paperSize int, questions []RecognizedQuestion) ([]PracticeGradingReference, []string, bool) {
	matched := practiceQuestionReferences(references, paperSize, questions)
	seen := make(map[string]bool, len(matched))
	unmatched := false
	for _, ref := range matched {
		if ref == nil {
			unmatched = true
		} else {
			seen[ref.ItemID] = true
		}
	}
	coveredRefs := make([]PracticeGradingReference, 0, len(seen))
	covered := make([]string, 0, len(seen))
	for _, ref := range references {
		if seen[ref.ItemID] {
			coveredRefs = append(coveredRefs, ref)
			covered = append(covered, ref.ItemID)
		}
	}
	return coveredRefs, covered, unmatched
}

func alignedPracticeReturnResults(
	references []PracticeGradingReference,
	paperSize int,
	items []PhotoGradeItem,
) ([]PracticeGradeResult, []string) {
	questions := make([]RecognizedQuestion, len(items))
	for i := range items {
		questions[i] = items[i].Recognized
	}
	matched := practiceQuestionReferences(references, paperSize, questions)
	grades := make([]PracticeGradeResult, 0, len(references))
	resolved := make(map[string]bool, len(references))
	for index, item := range items {
		ref := matched[index]
		if ref == nil || item.PracticeItemID != ref.ItemID || item.PracticeProblemID != ref.PracticeProblemID {
			continue
		}
		switch item.Status {
		case PhotoCorrect:
			grades = append(grades, PracticeGradeResult{ItemID: ref.ItemID, Correct: true})
			resolved[ref.ItemID] = true
		case PhotoWrong:
			grades = append(grades, PracticeGradeResult{ItemID: ref.ItemID, Correct: false})
			resolved[ref.ItemID] = true
		}
	}
	unresolved := make([]string, 0)
	for _, ref := range references {
		if !resolved[ref.ItemID] {
			unresolved = append(unresolved, ref.ItemID)
		}
	}
	return grades, unresolved
}

func unresolvedPracticeReturnItems(
	references []PracticeGradingReference,
	paperSize int,
	questions []RecognizedQuestion,
) []string {
	matched := practiceQuestionReferences(references, paperSize, questions)
	clear := make(map[string]bool, len(references))
	for index, question := range questions {
		if matched[index] != nil && !NormalizeRecognizedQuestion(question).ConfirmationRequired {
			clear[matched[index].ItemID] = true
		}
	}
	unresolved := make([]string, 0)
	for _, ref := range references {
		if !clear[ref.ItemID] {
			unresolved = append(unresolved, ref.ItemID)
		}
	}
	if len(unresolved) == 0 {
		for _, ref := range references {
			unresolved = append(unresolved, ref.ItemID)
		}
	}
	return unresolved
}

func freezePracticeGradingReferences(fields k12.PracticeSetFields, itemIDs []string) ([]PracticeGradingReference, int, error) {
	selected := make(map[string]bool, len(itemIDs))
	for _, id := range itemIDs {
		selected[id] = true
	}
	refs := make([]PracticeGradingReference, 0, len(itemIDs))
	paperSize := 0
	for _, item := range fields.Items {
		if !k12.PracticeItemPublishable(item) {
			continue
		}
		paperSize++
		if !selected[item.ItemID] {
			continue
		}
		if item.PracticeProblemID == "" || item.PaperSeq <= 0 {
			return nil, 0, fmt.Errorf("%w: practice item has no frozen paper identity", ErrInvalidInput)
		}
		refs = append(refs, PracticeGradingReference{ItemID: item.ItemID, PracticeProblemID: item.PracticeProblemID,
			PaperSeq: item.PaperSeq, Subject: item.Subject, QuestionMarkdown: item.QuestionMarkdown,
			ExpectedAnswerMarkdown: item.ExpectedAnswerMarkdown})
	}
	if len(refs) != len(selected) || len(refs) == 0 {
		return nil, 0, fmt.Errorf("%w: practice return includes items outside the frozen paper", ErrInvalidInput)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].PaperSeq < refs[j].PaperSeq })
	return refs, paperSize, nil
}

// practiceQuestionReferences 优先使用原卷题号；整卷均无题号时才按卷面顺序对齐。
func practiceQuestionReferences(refs []PracticeGradingReference, paperSize int, questions []RecognizedQuestion) []*PracticeGradingReference {
	matched := make([]*PracticeGradingReference, len(questions))
	bySeq := make(map[int]*PracticeGradingReference, len(refs))
	refCounts := make(map[int]int, len(refs))
	for i := range refs {
		refCounts[refs[i].PaperSeq]++
		bySeq[refs[i].PaperSeq] = &refs[i]
	}
	seqs := make([]int, len(questions))
	counts := make(map[int]int, len(questions))
	hasNumber := false
	for i, q := range questions {
		if len(q.SourceNumberPath) == 0 && strings.TrimSpace(q.DisplayLabel) == "" {
			continue
		}
		hasNumber = true
		if len(q.SourceNumberPath) != 1 {
			continue
		}
		number := strings.Trim(strings.TrimSpace(q.SourceNumberPath[0]), "第题.．、()（）[]【】 ")
		seq, err := strconv.Atoi(number)
		if err == nil && seq > 0 {
			seqs[i] = seq
			counts[seq]++
		}
	}
	if hasNumber {
		for i, seq := range seqs {
			if seq > 0 && counts[seq] == 1 && refCounts[seq] == 1 {
				matched[i] = bySeq[seq]
			}
		}
		return matched
	}
	if paperSize <= 0 || len(questions) != paperSize {
		return matched
	}
	for i := range questions {
		if refCounts[i+1] == 1 {
			matched[i] = bySeq[i+1]
		}
	}
	return matched
}

func (c *PracticeReturnRegradeCoordinator) loadReturn(
	ctx context.Context,
	agentName, setID, returnID string,
) (PracticeSetView, k12.PracticeReturnAsset, error) {
	view, err := c.Deps.GetPracticeSet(ctx, strings.TrimSpace(agentName), strings.TrimSpace(setID))
	if err != nil {
		return PracticeSetView{}, k12.PracticeReturnAsset{}, err
	}
	for _, ret := range view.Fields.ReturnAssets {
		if ret.ReturnID == strings.TrimSpace(returnID) {
			return view, ret, nil
		}
	}
	return PracticeSetView{}, k12.PracticeReturnAsset{},
		fmt.Errorf("%w: practice return %q not found", records.ErrNotFound, returnID)
}

func (c *PracticeReturnRegradeCoordinator) updateProjection(
	ctx context.Context,
	agentName, setID, returnID string,
	projection practiceReturnRegradeProjection,
) error {
	for attempt := 0; attempt < 5; attempt++ {
		view, _, err := c.loadReturn(ctx, agentName, setID, returnID)
		if err != nil {
			return err
		}
		for index := range view.Fields.ReturnAssets {
			ret := &view.Fields.ReturnAssets[index]
			if ret.ReturnID != returnID {
				continue
			}
			if ret.RegradeJobID != "" && projection.JobID != "" &&
				ret.RegradeJobID != projection.JobID {
				return fmt.Errorf("%w: return_id %q already binds grading job %q",
					ErrInvalidInput, returnID, ret.RegradeJobID)
			}
			if projection.JobID != "" {
				ret.RegradeJobID = projection.JobID
			}
			if projection.Status != "" {
				ret.RegradeStatus = projection.Status
			}
			if projection.RouteSnapshot.Provider != "" || projection.RouteSnapshot.Model != "" {
				ret.RouteSnapshot = k12.NormalizeGradingModelSnapshot(projection.RouteSnapshot)
			}
			if ret.AutoMatch && projection.Covered != nil {
				for _, id := range projection.Covered {
					for itemIndex := range view.Fields.Items {
						if view.Fields.Items[itemIndex].ItemID == id {
							view.Fields.Items[itemIndex].Returned = true
						}
					}
				}
				ret.ItemIDs = append([]string{}, projection.Covered...)
			}
			if projection.ReplaceResult {
				ret.AnnotatedAssetID = projection.AnnotatedAssetID
				ret.ResultMarkdown = projection.ResultMarkdown
				ret.UnresolvedItemIDs = append([]string(nil), projection.Unresolved...)
			}
			ret.RegradeUpdatedAt = c.Deps.now()
			break
		}
		if err := c.Deps.savePracticeFields(ctx, view, view.Record.Status); err == nil {
			return nil
		} else if !errors.Is(err, records.ErrVersionConflict) {
			return err
		}
	}
	return fmt.Errorf("%w: automatic regrade projection CAS exhausted", records.ErrVersionConflict)
}

func (c *PracticeReturnRegradeCoordinator) projectFailure(
	ctx context.Context,
	agentName, setID, returnID string,
	ret k12.PracticeReturnAsset,
	status string,
	cause error,
) error {
	if err := c.updateProjection(ctx, agentName, setID, returnID,
		practiceReturnRegradeProjection{
			JobID: ret.RegradeJobID, Status: status, RouteSnapshot: ret.RouteSnapshot,
		}); err != nil {
		return err
	}
	if cause == nil {
		return nil
	}
	return cause
}

// Recover schedules every nonterminal return projection for the supplied
// owner set. It never retries failed/outcome-unknown work blindly.
func (c *PracticeReturnRegradeCoordinator) Recover(
	ctx context.Context,
	agentNames []string,
) (int, error) {
	if err := c.validate(); err != nil {
		return 0, err
	}
	count := 0
	for _, agentName := range agentNames {
		sets, err := c.Deps.ListPracticeSets(ctx, agentName, "")
		if err != nil {
			return count, err
		}
		for _, set := range sets {
			for _, ret := range set.Fields.ReturnAssets {
				switch ret.RegradeStatus {
				case "", k12.PracticeRegradeQueued, k12.PracticeRegradeRunning:
					if c.StartAsync(agentName, set.Record.RecordID, ret.ReturnID) {
						count++
					}
				case k12.PracticeRegradeFailedRetryable, k12.PracticeRegradeOutcomeUnknown:
					if ret.RegradeJobID == "" {
						continue
					}
					// 底层 Job 可独立恢复；只同步其权威状态，不在投影恢复中重试任务。
					job, err := c.Deps.GetGradingJob(ctx, agentName, ret.RegradeJobID)
					if err != nil {
						return count, err
					}
					projection := practiceReturnRegradeProjection{
						JobID: job.Record.RecordID, RouteSnapshot: job.Fields.ModelSnapshot,
					}
					switch job.Record.Status {
					case k12.GradingStageCompleted:
						if _, loaded := c.Grading.PhotoResult(job.Record.RecordID); !loaded {
							// 已完成 Job 只加载持久产物，不推进模型阶段。
							job, err = c.Grading.RunGradingJob(ctx, job.Record.RecordID)
							if err != nil {
								return count, err
							}
							if job.Record == nil || job.Record.Status != k12.GradingStageCompleted {
								return count, fmt.Errorf("completed grading result could not be loaded")
							}
						}
						references, paperSize, err := freezePracticeGradingReferences(set.Fields, ret.ItemIDs)
						if err != nil {
							return count, err
						}
						if err := c.projectCompleted(ctx, agentName, set.Record.RecordID, ret.ReturnID,
							references, paperSize, job); err != nil {
							return count, err
						}
					case k12.GradingStageOutcomeUnknown:
						projection.Status = k12.PracticeRegradeOutcomeUnknown
					case k12.GradingStageFailedRetryable:
						projection.Status = k12.PracticeRegradeFailedRetryable
					case k12.GradingStageFailedTerminal, k12.GradingStageCancelled:
						projection.Status = k12.PracticeRegradeFailedTerminal
					case k12.GradingStageQueued, k12.GradingStageNormalizing, k12.GradingStageRecognizing,
						k12.GradingStageAwaitingConfirmation, k12.GradingStageAssessing,
						k12.GradingStageRendering, k12.GradingStageProjecting:
						if c.StartAsync(agentName, set.Record.RecordID, ret.ReturnID) {
							count++
						}
						continue
					default:
						continue
					}
					if projection.Status != "" {
						if err := c.updateProjection(ctx, agentName, set.Record.RecordID, ret.ReturnID, projection); err != nil {
							return count, err
						}
					}
					count++
				}
			}
		}
	}
	return count, nil
}

func (c *PracticeReturnRegradeCoordinator) Wait(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.initLocked()
	done := c.workerIdle
	c.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *PracticeReturnRegradeCoordinator) Shutdown(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.initLocked()
	if !c.sealed {
		c.sealed = true
		if c.runCancel != nil {
			c.runCancel()
		}
	}
	done := c.workerIdle
	c.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
