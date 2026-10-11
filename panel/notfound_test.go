package panel

import (
	"net/http"
	"testing"

	"golang.org/x/net/html"
)

// assertNotFoundPage checks res is the panel's not-found page, with a way
// back that leads to a page the browser b loads, and returns the page.
func assertNotFoundPage(t *testing.T, b *browser, res *http.Response) *html.Node {
	t.Helper()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if got := failureOf(t, doc); got != "not-found" {
		t.Errorf("failure page = %q, want not-found", got)
	}
	back := findElement(doc, "a", "data-retry", "")
	if back == nil {
		t.Fatal("the page has no way back")
	}
	if got := follow(t, b, b.get(attrOf(back, "href"))); got.StatusCode != http.StatusOK {
		t.Errorf("the way back lands with status %d, want 200", got.StatusCode)
	}
	return doc
}

// A panel address that names nothing, such as a hub or a recording that
// isn't there or a path the panel doesn't serve, answers with the panel's
// not-found page, in the rail, with a way back into the panel.
func TestAddressThatNamesNothingIsNotFoundInsideThePanel(t *testing.T) {
	for _, path := range []string{"/?hub=999", "/?hub=abc", "/no-such-page", "/recordings/abc"} {
		t.Run(path, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)

			res := w.b.get(path)

			assertSignedInPage(t, assertNotFoundPage(t, w.b, res))
		})
	}
	t.Run("a path the panel doesn't serve, signed out", func(t *testing.T) {
		w := newTestWorld(t, testHub())
		b := newBrowser(t, w.p)

		res := b.get("/no-such-page")

		assertNotFoundPage(t, b, res)
	})
}
