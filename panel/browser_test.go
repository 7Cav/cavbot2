package panel

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// The test browser submits a GET form the way a browser does, so a test
// that compares a link's address with a form's agrees with a browser on
// whether the press loads the page or only scrolls (issue #714). The page
// here belongs to the test, so no panel template decides the address.
// Chromium sends the same address for it.
func TestAGETFormSendsItsFieldsInTheOrderABrowserDoes(t *testing.T) {
	var sent []string
	b := &browser{t: t, cookies: map[string]string{}, h: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.URL.RequestURI())
	})}
	doc, err := html.Parse(strings.NewReader(`<!DOCTYPE html>
<input name="before" value="1" form="f">
<form id="f" method="get" action="/find#results">
  <input type="hidden" name="z" value="1">
  <input name="a" value="2">
  <button name="op" value="other">Other</button>
  <button name="a" value="pressed" data-go>Go</button>
  <input type="checkbox" name="m" value="3" checked>
</form>
<input type="checkbox" name="m" value="4" form="f" checked>`))
	if err != nil {
		t.Fatalf("parse the page: %v", err)
	}

	press(t, b, doc, findElement(doc, "button", "data-go", ""))

	// The form's controls go in tree order. The two outside the form that
	// name it count at their own places, the pressed button sits at its
	// place after the field that shares its name, and the other button sends
	// nothing.
	if want := "/find?before=1&z=1&a=2&a=pressed&m=3&m=4"; len(sent) != 1 || sent[0] != want {
		t.Errorf("pressing Go requested %q, want only %q", sent, want)
	}
}
