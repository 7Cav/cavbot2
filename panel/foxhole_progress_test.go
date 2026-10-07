package panel

import (
	"slices"
	"testing"
)

// The progress block's Reload link loads the page again in the view it
// shows, the search and the filter kept, so a manager without script sees
// how far the action has got without losing their place. The page's script
// loads the same link in the background. The purge takes Internal only and
// the view lists External holders: of the two members matching the search,
// one holds External and keeps it whatever the purge has done, and the
// other holds Internal alone.
func TestProgressBlockReloadLinkKeepsTheView(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	hold.next(t)
	progress := progressBlock(t, parseHTML(t, w.b.get("/foxhole?q=sh&filter=external")))
	reload := findElement(progress, "a", "data-field", "reload")
	if reload == nil {
		t.Fatal("the progress block has no Reload link")
	}
	href := attrOf(reload, "href")

	rows := holderRows(t, parseHTML(t, w.b.get(href)))

	if got, want := keys(rows), []string{memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the Reload link %s lists %v, want %v, the External holders matching the search", href, got, want)
	}
}
