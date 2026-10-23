package mediaflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
)

// RecoveryCodec 复用应用已有凭据加密器，不把临时签名URL写成公开资产。
type RecoveryCodec interface {
	Seal([]byte) (string, error)
	Open(string) ([]byte, error)
}
type Option func(*Service)

func WithRecoveryCodec(codec RecoveryCodec) Option { return func(s *Service) { s.codec = codec } }

type recoveryResult struct {
	Image *imagegen.Result     `json:"image,omitempty"`
	Video *videogen.TaskStatus `json:"video,omitempty"`
}

func (s *Service) captureRecovery(op *Operation, result recoveryResult) {
	if s.codec == nil {
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return
	}
	sealed, err := s.codec.Seal(encoded)
	if err != nil {
		return
	}
	op.RecoveryKind = "remote_url"
	op.RecoveryRef = sealed
	op.SaveState = "saving"
	_ = s.repo.Update(s.ctx, op, op.Revision)
}

func (s *Service) recoverOperation(op *Operation) {
	if op.RecoveryKind != "remote_url" {
		s.restoreAssets(op)
		return
	}
	if s.codec == nil {
		s.saveFailure(op, fmt.Errorf("original recovery codec is unavailable"))
		return
	}
	encoded, err := s.codec.Open(op.RecoveryRef)
	if err != nil {
		s.saveFailure(op, err)
		return
	}
	var result recoveryResult
	if json.Unmarshal(encoded, &result) != nil {
		s.saveFailure(op, fmt.Errorf("invalid recovery source"))
		return
	}
	if result.Image != nil {
		s.saveImages(op, result.Image)
		return
	}
	if result.Video != nil {
		s.saveVideo(op, *result.Video)
		return
	}
	s.saveFailure(op, fmt.Errorf("original recovery source is empty"))
}

func (s *Service) assetReadable(asset Asset) bool {
	r, err := s.blobs.OpenReader(asset.Path)
	if err != nil {
		return false
	}
	h := sha256.New()
	_, err = io.Copy(h, r)
	closeErr := r.Close()
	return err == nil && closeErr == nil && hex.EncodeToString(h.Sum(nil)) == asset.Digest
}
