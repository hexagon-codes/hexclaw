package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProductFactsPublicIdentity(t *testing.T) {
	encoded, err := PublicProductFactsJSON()
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal([]byte(encoded), &facts); err != nil {
		t.Fatal(err)
	}
	if facts["canonical_name"] != "HexClaw" || facts["repositories"].(map[string]any)["backend"] != "https://github.com/hexagon-codes/hexclaw" {
		t.Fatalf("wrong host identity: %s", encoded)
	}
	if strings.Contains(encoded, "development_phase") || strings.Contains(encoded, "_internal") {
		t.Fatal("internal facts exposed")
	}
	wechat := facts["official_accounts"].(map[string]any)["wechat"].(map[string]any)
	if wechat["status"] != "confirmed_present" || wechat["name"] != "hexclaw" || wechat["id"] != "hexclaw" {
		t.Fatal("confirmed account identity was not preserved")
	}
}

func TestProductFactsInternalChangesDoNotAlterPublicDigest(t *testing.T) {
	before, err := buildPublicProductFacts([]byte(`{"product_id":"hexclaw","public":"known","_internal":{"phase":1},"private":{"visibility":"internal","secret":"a"}}`))
	if err != nil {
		t.Fatal(err)
	}
	after, err := buildPublicProductFacts([]byte(`{"product_id":"hexclaw","public":"known","_internal":{"phase":2},"private":{"visibility":"internal","secret":"b"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("private changes altered public projection: %s / %s", before, after)
	}
}
