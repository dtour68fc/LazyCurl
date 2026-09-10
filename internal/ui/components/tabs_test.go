package components

import "testing"

// TestTabsSetItems verifies SetItems replaces the tab list and keeps the
// active index valid - this is what powers the request editor swapping
// between its HTTP tab set and its gRPC tab set depending on the loaded
// request's protocol.
func TestTabsSetItems(t *testing.T) {
	tabs := NewTabs([]string{"Params", "Authorization", "Headers", "Body", "Scripts"})
	tabs.SetActive(3) // "Body"
	if got := tabs.GetActive(); got != "Body" {
		t.Fatalf("GetActive() = %q, want %q", got, "Body")
	}

	// Switching to a shorter list while the active index is still in range
	// for the new list should keep pointing at the same index (not reset).
	tabs.SetItems([]string{"Server", "Metadata", "Message", "Scripts"})
	if got := tabs.GetActive(); got != "Scripts" {
		t.Fatalf("GetActive() after SetItems (in-range index) = %q, want %q", got, "Scripts")
	}

	// Switching to an even shorter list where the active index would now
	// be out of range must reset to 0, not leave GetActive() returning "".
	tabs.SetActive(3) // last item in the 4-item gRPC set
	tabs.SetItems([]string{"Params", "Authorization"})
	if got := tabs.GetActive(); got != "Params" {
		t.Fatalf("GetActive() after SetItems (out-of-range index) = %q, want %q (reset to 0)", got, "Params")
	}
	if tabs.ActiveIndex != 0 {
		t.Errorf("ActiveIndex = %d, want 0", tabs.ActiveIndex)
	}
}

// TestTabsSetItemsResetsShortcuts verifies Shortcuts is resized to match
// the new Items length rather than left stale from the old tab set (which
// would misalign shortcut hints with the new tab names).
func TestTabsSetItemsResetsShortcuts(t *testing.T) {
	tabs := NewTabsWithShortcuts([]TabItem{
		{Name: "Params", Shortcut: "1"},
		{Name: "Authorization", Shortcut: "2"},
		{Name: "Headers", Shortcut: "3"},
		{Name: "Body", Shortcut: "4"},
		{Name: "Scripts", Shortcut: "5"},
	})

	tabs.SetItems([]string{"Server", "Metadata", "Message", "Scripts"})

	if len(tabs.Shortcuts) != len(tabs.Items) {
		t.Fatalf("len(Shortcuts) = %d, len(Items) = %d, want equal", len(tabs.Shortcuts), len(tabs.Items))
	}
}
