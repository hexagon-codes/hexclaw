package webhook

import "testing"

func TestWorkflowReferencePreservesEndpoint(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	manager := NewManager(db)
	if err := manager.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	endpoint := &Webhook{Name: "existing", Type: TypeGeneric, Secret: "fixture-secret", UserID: "owner", Enabled: false}
	if err := manager.Register(t.Context(), endpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BindWorkflow(t.Context(), endpoint.Name, "owner", "wf-one"); err != nil {
		t.Fatal(err)
	}
	reloaded := NewManager(db)
	if err := reloaded.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get(endpoint.Name)
	if !ok || got.WorkflowID != "wf-one" || got.Secret != "fixture-secret" || got.Enabled || got.EventCount != 0 || !got.CreatedAt.Equal(endpoint.CreatedAt) {
		t.Fatalf("endpoint changed: %+v", got)
	}
	if _, err := reloaded.BindWorkflow(t.Context(), endpoint.Name, "owner", "wf-other"); err == nil {
		t.Fatal("replaced existing workflow target")
	}
	if err := reloaded.UnbindWorkflow(t.Context(), endpoint.Name, "owner", "wf-one"); err != nil {
		t.Fatal(err)
	}
	got, ok = reloaded.Get(endpoint.Name)
	if !ok || got.WorkflowID != "" || got.Secret != "fixture-secret" || got.Enabled {
		t.Fatal("removing reference destroyed the endpoint")
	}
}
