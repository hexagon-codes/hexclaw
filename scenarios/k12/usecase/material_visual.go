package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// prepareVisual 只读取持久原图，先冻结并提交回执再进入独立解答。
func (w *MaterialPreparationWorker) prepareVisual(ctx context.Context, p k12storage.MaterialPreparation) (k12storage.MaterialPreparation, error) {
	if w.ReadVisual == nil || w.ResolveVisualModel == nil {
		return p, errors.New("Source image interpretation is unavailable")
	}
	var image []byte
	var digest string
	var err error
	if len(p.Candidate.VisualPDFPages) > 0 {
		if w.PreparePDFSource == nil {
			return p, errors.New("Source PDF image preparation is unavailable")
		}
		image, digest, err = w.PreparePDFSource(ctx, p)
	} else {
		image, digest, err = w.Records.MaterialSourceImage(ctx, p)
	}
	if err != nil {
		return p, err
	}
	var policy k12storage.MaterialModelPolicy
	if json.Unmarshal([]byte(p.Policy), &policy) != nil {
		snapshot, e := w.ResolveVisualModel(ctx, k12.GradingModelSnapshot{})
		if e != nil {
			return p, e
		}
		policy, err = w.Records.FreezeMaterialModel(ctx, p, snapshot)
		if err != nil {
			return p, err
		}
		encoded, _ := json.Marshal(policy)
		p.Policy = string(encoded)
	}
	busy, err := w.Records.MaterialForegroundBusy(ctx)
	if err != nil {
		return p, err
	}
	if busy {
		return p, errMaterialForegroundDeferred
	}
	prompt := "Read the attached source diagram for this one problem. Return only JSON {\"complete\":true,\"visual_facts\":[\"...\"],\"issues\":[]}. Transcribe every answer-relevant number, unit, label, relation and printed condition in Chinese. The image may contain ordered source pages; read only the specified question and its explicitly shared material. If another question or printed answer cannot be separated reliably, set complete=false. Do not solve the problem, infer missing dimensions, or use a printed answer as evidence. If any necessary part is unreadable or the image does not belong to the question, set complete=false and explain issues.\nProblem and shared material:\n" + materialSolverProblem(k12storage.MaterialVisualPromptFacts(p.Candidate.Facts))
	request, _ := json.Marshal(struct{ Input, Image, Prompt, Policy string }{p.InputDigest, digest, prompt, p.Policy})
	h := sha256.Sum256(request)
	inv, fresh, err := w.Records.ClaimMaterialInvocation(ctx, p, "visual_extract", hex.EncodeToString(h[:]))
	if err != nil {
		return p, err
	}
	if fresh {
		timeout := time.Duration(policy.Model.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = 3 * time.Minute
		}
		callCtx, cancel := context.WithTimeout(k12.WithGradingModelSnapshot(ctx, policy.Model), timeout)
		response, callErr := w.ReadVisual(callCtx, image, prompt)
		cancel()
		receipt := k12storage.MaterialVisualReceipt{Model: policy.Model, InputDigest: p.InputDigest, ObjectDigest: digest, Response: response}
		text := strings.TrimSpace(response)
		if strings.HasPrefix(text, "```") {
			if i := strings.IndexByte(text, '\n'); i >= 0 {
				text = strings.TrimSpace(strings.TrimSuffix(text[i+1:], "```"))
			}
		}
		if json.Unmarshal([]byte(text), &receipt.Reading) != nil {
			receipt.Reading.Issues = []string{"Visual response is not valid JSON"}
		}
		payload, _ := json.Marshal(receipt)
		if err = w.Records.FinishMaterialInvocation(context.WithoutCancel(ctx), inv, string(payload), callErr); err != nil {
			return p, err
		}
		if callErr != nil {
			return p, fmt.Errorf("%w: %v", k12storage.ErrMaterialPreparationUnknown, callErr)
		}
		inv.ResultJSON = string(payload)
	}
	p.VisualEvidence, err = w.Records.MaterialVisualEvidence(ctx, p, inv)
	return p, err
}
