package knowledge

import "testing"

func TestWorkflowDocumentScopeAppliesBeforeTopK(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewSQLiteStore(db)
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, store, nil, WithSplitter(testSplitter()))
	allowed, err := manager.AddDocument(t.Context(), "selected report", "quartz selected report figures", "selected")
	if err != nil {
		t.Fatal(err)
	}
	other, err := manager.AddDocument(t.Context(), "other report", "quartz quartz quartz other confidential report figures", "other")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithDocumentScope(t.Context(), []string{allowed.ID})
	hits, err := manager.SearchWithFilter(ctx, "quartz", 1, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].DocID != allowed.ID {
		t.Fatalf("selected scope not applied before topK: %+v", hits)
	}
	hits, err = manager.SearchWithFilter(ctx, "quartz", 5, Filter{DocumentIDs: []string{other.ID}})
	if err != nil || len(hits) != 0 {
		t.Fatalf("conflicting filter broadened scope: %+v %v", hits, err)
	}
	hits, err = manager.SearchWithFilter(WithDocumentScope(t.Context(), nil), "quartz", 5, Filter{})
	if err != nil || len(hits) != 0 {
		t.Fatalf("empty override searched all documents: %+v %v", hits, err)
	}
	hits, err = manager.SearchWithFilter(t.Context(), "quartz", 5, Filter{})
	if err != nil || len(hits) != 2 {
		t.Fatalf("ordinary search changed: %+v %v", hits, err)
	}
}
