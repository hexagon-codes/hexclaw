package mediaflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
)

// stagedOutput 在校验前关联原始字节；文件名由可信操作和输出序号确定。
type stagedOutput struct {
	Kind  string `json:"kind"`
	Role  string `json:"role"`
	Index int    `json:"index"`
}

type stagingRecord struct {
	Operation
	Outputs []stagedOutput `json:"staged_outputs,omitempty"`
}

func (s *Service) readStaging(op *Operation) (*stagingRecord, error) {
	data, err := os.ReadFile(filepath.Join(s.staging, op.ID+".json"))
	if err != nil {
		return nil, err
	}
	var record stagingRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	if record.ID != op.ID || record.Owner != op.Owner {
		return nil, fmt.Errorf("media staging origin mismatch")
	}
	return &record, nil
}

func (s *Service) stageOperation(op *Operation, outputs ...stagedOutput) error {
	record, err := s.readStaging(op)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if record == nil {
		record = &stagingRecord{}
	}
	record.Operation = *op
	for _, output := range outputs {
		found := false
		for _, previous := range record.Outputs {
			if previous == output {
				found = true
				break
			}
		}
		if !found {
			record.Outputs = append(record.Outputs, output)
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.writeStaging(op.ID+".json", data)
}

func (s *Service) stagedOutputPath(op *Operation, output stagedOutput) string {
	return filepath.Join(s.staging, fmt.Sprintf("%s.%s.%s.%d.raw", op.ID, output.Kind, output.Role, output.Index))
}

func (s *Service) stageOutput(op *Operation, input io.Reader, output stagedOutput) error {
	if err := s.stageOperation(op, output); err != nil {
		return err
	}
	file, err := os.CreateTemp(s.staging, ".media-output-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := io.Copy(file, input); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.stagedOutputPath(op, output))
}

func (s *Service) savedOutput(op *Operation, output stagedOutput) (Asset, error) {
	file, err := os.Open(s.stagedOutputPath(op, output))
	if err != nil {
		return Asset{}, err
	}
	defer file.Close()
	return s.saveBlobReader(op, file, output.Kind, output.Role, output.Index)
}

// restoreAssets 只查回已有提交引用或原始字节，不重新发起生成。
func (s *Service) restoreAssets(op *Operation) bool {
	staged, err := s.readStaging(op)
	if err != nil {
		return false
	}
	if staged.UpstreamID != "" && staged.GenerationState == "running" {
		if op.GenerationState != "submitting" && op.GenerationState != "unknown" {
			return false
		}
		op.UpstreamID = staged.UpstreamID
		op.GenerationState = "running"
		op.Error, op.ErrorCode = "", ""
		mergeExecutionReceipts(op, &staged.Operation)
		return s.repo.Update(s.ctx, op, op.Revision) == nil
	}
	if staged.GenerationState != "succeeded" {
		return false
	}
	mergeExecutionReceipts(op, &staged.Operation)
	op.GenerationState = "succeeded"
	op.ActualProviderID = staged.ActualProviderID
	op.Result = staged.Result
	op.RecoveryKind, op.RecoveryRef = staged.RecoveryKind, staged.RecoveryRef
	op.ErrorCode, op.Error = staged.ErrorCode, staged.Error
	op.SaveState = staged.SaveState
	op.Assets = nil
	for _, asset := range staged.Assets {
		if s.assetReadable(asset) {
			op.Assets = append(op.Assets, asset)
		}
	}
	missing := false
	for _, output := range staged.Outputs {
		found := false
		for _, asset := range op.Assets {
			if asset.Index == output.Index && asset.Role == output.Role {
				found = true
				break
			}
		}
		if found {
			continue
		}
		asset, err := s.savedOutput(op, output)
		if err != nil {
			missing = true
			continue
		}
		op.Assets = append(op.Assets, asset)
	}
	if len(staged.Outputs) == 0 && len(op.Assets) != len(staged.Assets) {
		missing = true
	}
	if missing && op.RecoveryKind == "remote_url" {
		op.SaveState = "saving"
		return s.repo.Update(s.ctx, op, op.Revision) == nil
	}
	if missing || len(op.Assets) == 0 {
		op.SaveState = "failed"
		if len(op.Assets) > 0 {
			op.SaveState = "partial"
		}
		op.ErrorCode, op.Error = "persist_failed", "one or more original media outputs could not be recovered"
	} else if staged.SaveState == "saving" || staged.SaveState == "not_started" || staged.ErrorCode == "persist_failed" {
		op.SaveState = "ready"
		op.ErrorCode, op.Error = "", ""
	}
	sort.SliceStable(op.Assets, func(i, j int) bool { return op.Assets[i].Index < op.Assets[j].Index })
	refreshRecoveredResult(op)
	s.finalizeAssets(op)
	return true
}

func refreshRecoveredResult(op *Operation) {
	switch op.Request.Kind {
	case "image":
		var result imagegen.Result
		_ = json.Unmarshal(op.Result, &result)
		result.Provider, result.Model = op.Route.ProviderID, op.Route.Model
		for _, asset := range op.Assets {
			if asset.Role != "primary" {
				continue
			}
			for len(result.Images) <= asset.Index {
				result.Images = append(result.Images, imagegen.Image{})
			}
			result.Images[asset.Index].FilePath = asset.Path
			result.Images[asset.Index].URL, result.Images[asset.Index].B64JSON = "", ""
		}
		op.Result, _ = json.Marshal(result)
	case "video":
		var result videogen.TaskStatus
		_ = json.Unmarshal(op.Result, &result)
		result.VideoURL, result.CoverURL = "", ""
		for _, asset := range op.Assets {
			if asset.Role == "primary" {
				result.VideoFilePath = asset.Path
			} else if asset.Role == "cover" {
				result.CoverFilePath = asset.Path
			}
		}
		op.Result, _ = json.Marshal(result)
	}
}
