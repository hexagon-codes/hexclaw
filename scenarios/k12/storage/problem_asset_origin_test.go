package k12storage_test

import (
	"context"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"testing"
)

func TestProblemAssets_ReuseProjectionBindsHistoricalAdoption(t *testing.T) {
	s, _ := problemAssetStore(t)
	ctx := context.Background()
	p := problemAssetPublication(t, s)
	v, _, err := s.PublishProblemAsset(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	a, _, err := s.AdoptProblemAsset(ctx, assetAdoption(v, "attempt-one"))
	if err != nil {
		t.Fatal(err)
	}
	source := k12.ProblemAnswerSource{Kind: k12.ProblemAnswerAsset, FactsDigest: v.FactsDigest, AssetID: v.AssetID, AssetVersion: v.Version, AssetRevision: v.Revision, AdoptionID: a.AdoptionID}
	got, err := s.GetProblemAssetReuse(ctx, v.OwnerID, a.JobID, a.ProblemID, source)
	if err != nil || got.Answer != "4" || got.Stem != p.Facts.Stem || got.SourceName != "Previously verified answer" || got.DocumentID != "" {
		t.Fatalf("actual adoption: %+v %v", got, err)
	}
	for _, identity := range [][3]string{{"other-owner", a.JobID, a.ProblemID}, {v.OwnerID, "other-job", a.ProblemID}, {v.OwnerID, a.JobID, "other-problem"}} {
		if _, err := s.GetProblemAssetReuse(ctx, identity[0], identity[1], identity[2], source); err == nil {
			t.Fatalf("unrelated identity received source: %v", identity)
		}
	}
	if err := s.ArchiveProblemAsset(ctx, v.OwnerID, v.AssetID, v.Revision); err != nil {
		t.Fatal(err)
	}
	historical, err := s.GetProblemAssetReuse(ctx, v.OwnerID, a.JobID, a.ProblemID, source)
	if err != nil || historical != got {
		t.Fatalf("archive rewrote adoption: %+v %v", historical, err)
	}
}
