package usecase

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
)

func submitWholeSetInternal(t *testing.T, d Deps, agent, setID string, automatic ...bool) PracticeSetView {
	t.Helper()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	assetID, err := assetstore.Save(agent, raw)
	if err != nil {
		t.Fatal(err)
	}
	v, err := d.GetPracticeSet(context.Background(), agent, setID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, it := range v.Fields.Items {
		if k12.PracticeItemPublishable(it) {
			ids = append(ids, it.ItemID)
		}
	}
	autoMatch := len(automatic) > 0 && automatic[0]
	if autoMatch {
		ids = nil
	}
	v, err = d.SubmitReturns(context.Background(), agent, setID, []PracticeReturnInput{{
		ReturnID: "return-test-" + setID, AssetID: assetID, ItemIDs: ids, AutoMatch: autoMatch,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
