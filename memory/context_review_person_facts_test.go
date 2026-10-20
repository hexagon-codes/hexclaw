package memory

import (
	"testing"
	"time"
)

func TestContextReview_PersonFactsPreserveIndependentAttributes(t *testing.T) {
	t.Run("grade_and_interest_coexist", func(t *testing.T) {
		fm := newFM(t)
		facts := []string{"小明读三年级", "小明喜欢足球"}
		for _, fact := range facts {
			action, err := fm.UpsertStructuredEntryForRole(fact, "fact", "chat_extract", "", "人物:小明")
			if err != nil {
				t.Fatalf("upsert %q: %v", fact, err)
			}
			if action == "supersede" {
				t.Errorf("independent fact %q must not supersede another attribute", fact)
			}
		}

		entries := fm.ParseEntries()
		if len(entries) != 2 {
			t.Fatalf("grade and interest must both remain stored, got %+v", entries)
		}
		for _, fact := range facts {
			var found bool
			for _, entry := range entries {
				if entry.Content != fact {
					continue
				}
				found = true
				if entry.ValidTo != "" {
					t.Errorf("compatible fact %q was invalidated: valid_to=%q", fact, entry.ValidTo)
				}
				if entry.Supersedes != "" {
					t.Errorf("compatible fact %q incorrectly supersedes %q", fact, entry.Supersedes)
				}
			}
			if !found {
				t.Errorf("missing compatible fact %q in %+v", fact, entries)
			}
		}
	})

	t.Run("residence_change_supersedes_old_value", func(t *testing.T) {
		fm := newFM(t)
		const oldFact = "小明住在北京"
		const newFact = "小明现在住在上海"
		if _, err := fm.UpsertStructuredEntryForRole(oldFact, "fact", "chat_extract", "", "人物:小明"); err != nil {
			t.Fatalf("insert old residence: %v", err)
		}
		before := fm.ParseEntries()
		if len(before) != 1 || before[0].Content != oldFact {
			t.Fatalf("old residence must be stored before the move, got %+v", before)
		}
		oldID := before[0].ID

		if _, err := fm.UpsertStructuredEntryForRole(newFact, "fact", "chat_extract", "", "人物:小明"); err != nil {
			t.Fatalf("update residence: %v", err)
		}
		entries := fm.ParseEntries()
		if len(entries) != 2 {
			t.Fatalf("residence change must retain old and new records, got %+v", entries)
		}
		var foundOld, foundNew bool
		for _, entry := range entries {
			switch entry.Content {
			case oldFact:
				foundOld = true
				if entry.ID != oldID {
					t.Errorf("old residence identity changed: got %q, want %q", entry.ID, oldID)
				}
				validTo, err := time.Parse(time.RFC3339, entry.ValidTo)
				if err != nil || validTo.After(time.Now()) {
					t.Errorf("old residence must no longer be current: valid_to=%q, error=%v", entry.ValidTo, err)
				}
			case newFact:
				foundNew = true
				if entry.ValidTo != "" {
					t.Errorf("new residence must remain current: valid_to=%q", entry.ValidTo)
				}
				if entry.Supersedes != oldID {
					t.Errorf("new residence must reference the old record: got %q, want %q", entry.Supersedes, oldID)
				}
			}
		}
		if !foundOld || !foundNew {
			t.Errorf("residence history is incomplete: old=%t, new=%t, entries=%+v", foundOld, foundNew, entries)
		}
	})
}
