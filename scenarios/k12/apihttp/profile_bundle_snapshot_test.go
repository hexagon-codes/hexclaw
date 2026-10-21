package apihttp_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func TestProfileBundleSnapshotReadsCurrentFieldsAndRevisionsWithoutWrites(t *testing.T) {
	h, deps, _ := newWeeklyBundleContractServer(t)
	if rec, _ := do(t, h, http.MethodPut, "/profile-bundle", a01BundleBody(t, "snapshot-original")); rec.Code != http.StatusOK {
		t.Fatalf("initial bundle: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := deps.Records.DB().Exec(`UPDATE agents SET display_name='最新名称',system_prompt='最新语气',
	    metadata=json_set(metadata,'$."k12.child_name"','最新称呼') WHERE name='mingming'`); err != nil {
		t.Fatal(err)
	}
	var revision, commands int
	if err := deps.Records.DB().QueryRow(`SELECT revision FROM k12_profile_revisions WHERE agent_name='mingming'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := deps.Records.DB().QueryRow(`SELECT count(*) FROM k12_profile_bundle_commands`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	rec, snapshot := do(t, h, http.MethodGet, "/profile-bundle?agent=mingming", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}
	config := snapshot["agent_config"].(map[string]any)
	profile := snapshot["profile"].(map[string]any)
	if config["display_name"] != "最新名称" || config["system_prompt"] != "最新语气" ||
		profile["child_name"] != "最新称呼" || profile["revision"] != float64(revision) || snapshot["command"] != nil {
		t.Fatalf("fields and revision are not one snapshot: %v", snapshot)
	}
	if len(profile["subject_textbooks"].(map[string]any)) != 6 || snapshot["progress_revision"] != float64(1) {
		t.Fatalf("incomplete snapshot: %v", snapshot)
	}
	var afterRevision, afterCommands int
	_ = deps.Records.DB().QueryRow(`SELECT revision FROM k12_profile_revisions WHERE agent_name='mingming'`).Scan(&afterRevision)
	_ = deps.Records.DB().QueryRow(`SELECT count(*) FROM k12_profile_bundle_commands`).Scan(&afterCommands)
	if afterRevision != revision || afterCommands != commands {
		t.Fatal("snapshot GET changed profile or commands")
	}
	if rec, _ := do(t, h, http.MethodGet, "/profile-bundle?agent=missing", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing agent: %d", rec.Code)
	}
}

func TestProfileBundleCommandReceiptAndReplayKeepOriginalAndCurrentState(t *testing.T) {
	h, deps, _ := newWeeklyBundleContractServer(t)
	original := a01BundleBody(t, "receipt-original")
	rec, first := do(t, h, http.MethodPut, "/profile-bundle", original)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	_, snapshot := do(t, h, http.MethodGet, "/profile-bundle?agent=mingming&idempotency_key=receipt-original", "")
	if !reflect.DeepEqual(snapshot["command"], first) {
		t.Fatalf("receipt changed: %v", snapshot)
	}
	var next map[string]any
	if err := json.Unmarshal([]byte(original), &next); err != nil {
		t.Fatal(err)
	}
	next["idempotency_key"] = "receipt-newer"
	next["expected_profile_revision"] = snapshot["profile"].(map[string]any)["revision"]
	next["expected_progress_revision"] = snapshot["progress_revision"]
	next["expected_settings_revision"] = snapshot["weekly_practice_settings"].(map[string]any)["revision"]
	next["agent_config"].(map[string]any)["display_name"] = "后续名称"
	raw, _ := json.Marshal(next)
	if rec, _ := do(t, h, http.MethodPut, "/profile-bundle", string(raw)); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	// 已提交的原请求即使当前教材不可用，也应只读重放原回执。
	if _, err := deps.Records.DB().Exec(`UPDATE k12_textbook_manifests SET state='stale' WHERE manifest_id=?`, weeklyBundleManifestID); err != nil {
		t.Fatal(err)
	}
	rec, replay := do(t, h, http.MethodPut, "/profile-bundle", original)
	if rec.Code != http.StatusOK || replay["replayed"] != true {
		t.Fatalf("original replay: %d %s", rec.Code, rec.Body.String())
	}
	_, latest := do(t, h, http.MethodGet, "/profile-bundle?agent=mingming&idempotency_key=receipt-original", "")
	if latest["agent_config"].(map[string]any)["display_name"] != "后续名称" || !reflect.DeepEqual(latest["command"], first) {
		t.Fatalf("replay replaced current state or stored receipt: %v", latest)
	}
	_, missing := do(t, h, http.MethodGet, "/profile-bundle?agent=other&idempotency_key=receipt-original", "")
	if missing["command"] != nil {
		t.Fatal("receipt escaped its agent scope")
	}
	_, absent := do(t, h, http.MethodGet, "/profile-bundle?agent=mingming&idempotency_key=absent", "")
	if absent["command"] != nil {
		t.Fatal("missing command fabricated a receipt")
	}
}
