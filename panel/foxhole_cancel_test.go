package panel

import (
	"testing"

	"golang.org/x/net/html"
)

// Cancel on a preview an action block opened lands on that block's
// heading. The action blocks sit below the latest report, and under
// 1100px they stack in one column, so the page's top is far from the block
// the manager came from. That holds with and without anything to confirm.
func TestCancelOnAnActionBlocksPreviewLandsOnTheBlock(t *testing.T) {
	cases := []struct {
		name string
		// open opens the preview as a browser with no script does, and
		// returns the page it opens.
		open func(*testing.T, *testWorld) *html.Node
		// preview is the preview's data-field marker, and block its action
		// block's.
		preview, block string
		// confirm is the preview offering a Confirm: something to confirm.
		confirm bool
	}{
		{"purge confirmation", func(t *testing.T, w *testWorld) *html.Node {
			return parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external"))
		}, "purge-confirm", "after-a-war", true},
		{"purge confirmation with nobody to purge", func(t *testing.T, w *testWorld) *html.Node {
			setMembers(w, memberVance)
			return parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "both"))
		}, "purge-confirm", "after-a-war", false},
		{"add preview", func(t *testing.T, w *testWorld) *html.Node {
			page := parseHTML(t, w.b.get(foxholePath))
			return parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "jdoe"))
		}, "add-preview", "add-members", true},
		{"add preview with nothing to add", func(t *testing.T, w *testWorld) *html.Node {
			// Kestrel holds External already.
			page := parseHTML(t, w.b.get(foxholePath))
			return parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "kestrel_tlr"))
		}, "add-preview", "add-members", false},
		{"roster preview", func(t *testing.T, w *testWorld) *html.Node {
			serveRoster(t, trooperVance, trooperDoe)
			return parseHTML(t, previewRoster(t, w, "D/ACD"))
		}, "roster-preview", "add-roster", true},
		{"roster preview with nobody to add", func(t *testing.T, w *testWorld) *html.Node {
			// Doe holds Internal already.
			serveRoster(t, trooperDoe)
			return parseHTML(t, previewRoster(t, w, "D/ACD"))
		}, "roster-preview", "add-roster", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			preview := pendingBlock(t, tc.open(t, w), tc.preview)
			if got := findElement(preview, "", "data-field", "confirm") != nil; got != tc.confirm {
				t.Fatalf("the preview offers a Confirm: %v, want %v", got, tc.confirm)
			}
			cancel := cancelAddress(t, preview)

			doc, fragment := landing(t, w.b, cancel, w.b.get(cancel))

			landedOnBlock(t, doc, fragment, tc.block)
		})
	}
}
