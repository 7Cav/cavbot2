package panel

import (
	"net/http"
	"testing"
)

// An address that names nothing in the panel answers with the panel's
// not-found page: the rail keeps the signed-in identity, and the page's
// way back loads a page.
func TestAddressThatNamesNothingGetsTheNotFoundPage(t *testing.T) {
	cases := []struct {
		name string
		send func(t *testing.T, w *testWorld) *http.Response
	}{
		{"a hub no hub has the ID of", func(t *testing.T, w *testWorld) *http.Response {
			return w.b.get("/?hub=999")
		}},
		{"a hub ID that is not one", func(t *testing.T, w *testWorld) *http.Response {
			return w.b.get("/?hub=abc")
		}},
		{"an address the panel has no page at", func(t *testing.T, w *testWorld) *http.Response {
			return w.b.get("/no-such-page")
		}},
		{"a save to a hub that is gone", func(t *testing.T, w *testWorld) *http.Response {
			return w.b.postForm("/hubs/999", updateForm(t, w.st))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)

			res := tc.send(t, w)

			if res.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", res.StatusCode)
			}
			doc := parseHTML(t, res)
			if got := failureOf(t, doc); got != "not-found" {
				t.Errorf("failure page = %q, want not-found", got)
			}
			assertSignedInPage(t, doc)
			back := findElement(doc, "a", "data-back", "")
			if back == nil {
				t.Fatal("the not-found page has no way back under data-back")
			}
			if got := follow(t, w.b, w.b.get(attrOf(back, "href"))); got.StatusCode != http.StatusOK {
				t.Errorf("the way back loads with status %d, want 200", got.StatusCode)
			}
		})
	}
}

// A recording address whose ID is not one is the page of a recording the
// panel doesn't hold, the same page an ID no recording has gets.
func TestRecordingAddressWithAMalformedIDIsTheGonePage(t *testing.T) {
	w := newTestWorld(t)
	starter := w.addStarter(5001, "Able.A", "discord-a")
	w.record(t, starter.discordID)
	signInAs(t, w.forum, w.b, starter.account)

	res := w.b.get(recordingsPath + "/abc")

	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	if findElement(parseHTML(t, res), "", "data-field", "recording-gone") == nil {
		t.Error("the page does not say the recording isn't in the panel")
	}
}
