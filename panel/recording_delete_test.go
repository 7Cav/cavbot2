package panel

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// Deleting a recording early (#391). A recording's page links to its
// delete preview, and the preview's Confirm deletes it, files and row.
// Every recording is made by the real recording runtime, as on the
// Recordings page's own tests.

// deleteConfirm opens a recording's page in a browser, follows the
// recording's Delete link to its preview, and returns the preview's
// Confirm form.
func deleteConfirm(t *testing.T, b *browser, id int64) *html.Node {
	t.Helper()
	row, ok := listedRecordings(t, parseHTML(t, b.get(commands.RecordingPath(id))))[id]
	if !ok {
		t.Fatalf("recording %d's page doesn't show it", id)
	}
	link := findElement(row, "a", "data-field", "delete")
	if link == nil {
		t.Fatalf("recording %d's block has no Delete link", id)
	}
	href, _ := attrValue(link, "href")
	form := findElement(parseHTML(t, b.get(href)), "form", "data-field", "delete-confirm")
	if form == nil {
		t.Fatalf("recording %d's delete preview has no Confirm form", id)
	}
	return form
}

// submitDeclared sends a form from a browser by the method the form
// declares, the way the browser would, with any extra headers given.
func submitDeclared(t *testing.T, b *browser, form *html.Node, header http.Header) *http.Response {
	t.Helper()
	action, _ := attrValue(form, "action")
	method, _ := attrValue(form, "method")
	if !strings.EqualFold(method, http.MethodPost) {
		doc := form
		for doc.Parent != nil {
			doc = doc.Parent
		}
		return b.do(http.MethodGet, formAddress(t, action, formEntries(doc, form, nil)), header, nil)
	}
	h := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}
	for k, v := range header {
		h[k] = v
	}
	return b.do(http.MethodPost, action, h, strings.NewReader(formValues(form).Encode()))
}

// regularFiles lists every regular file under a directory.
func regularFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return files
}

// The starter, and a panel admin, each delete a recording through its
// page's Delete and the preview's Confirm. Then the starter's Recordings
// page doesn't list it, its mix is refused, and none of its files is left
// on the disk.
func TestRecordingDeletedByItsStarterOrAPanelAdminIsGone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deleter func(t *testing.T, w *testWorld) *browser
	}{
		{"the starter", func(_ *testing.T, w *testWorld) *browser { return w.b }},
		{"a panel admin", func(t *testing.T, w *testWorld) *browser {
			admin := newBrowser(t, w.p)
			signIn(t, w.forum, admin)
			return admin
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			a := w.addStarter(5001, "Able.A", "discord-a")
			rec := w.record(t, a.discordID)
			signInAs(t, w.forum, w.b, a.account)
			mix := linkHref("mix-download")(t, parseHTML(t, w.b.get(recordingsPath)), rec)
			deleter := tc.deleter(t, w)

			submit(t, deleter, deleteConfirm(t, deleter, rec))

			if _, ok := listedRecordings(t, parseHTML(t, follow(t, w.b, w.b.get(recordingsPath))))[rec]; ok {
				t.Errorf("the starter's Recordings page still lists recording %d", rec)
			}
			if body := readBody(t, w.b.get(mix)); bytes.Equal(body, testMix) {
				t.Errorf("recording %d's mix still downloads", rec)
			}
			if files := regularFiles(t, w.recordingsDir); len(files) != 0 {
				t.Errorf("files left on the disk after the delete: %v", files)
			}
		})
	}
}

// Anyone who is neither a recording's starter nor a panel admin, here
// another starter, an outsider and a Foxhole manager, who posts its Confirm
// gets the no-access page saying the recording is open only to its starter
// and panel admins, and nothing is deleted: its starter's page still lists
// it, and its mix still downloads.
func TestRecordingDeletePostedByAnyoneElseDeletesNothing(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	b := w.addStarter(5002, "Baker.B", "discord-b")
	rec := w.record(t, a.discordID)
	w.record(t, b.discordID)
	signInAs(t, w.forum, w.b, a.account)
	confirm := deleteConfirm(t, w.b, rec)
	mix := linkHref("mix-download")(t, parseHTML(t, w.b.get(recordingsPath)), rec)

	for _, v := range recordingRefusedVisitors(w, b) {
		t.Run(v.who, func(t *testing.T) {
			visitor := newBrowser(t, w.p)
			signInAs(t, w.forum, visitor, v.account)

			res := submit(t, visitor, confirm)

			assertNoAccessSays(t, parseHTML(t, res), noAccessRecording)
			if _, ok := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]; !ok {
				t.Errorf("the starter's Recordings page no longer lists recording %d", rec)
			}
			if body := readBody(t, w.b.get(mix)); !bytes.Equal(body, testMix) {
				t.Errorf("recording %d's mix no longer downloads", rec)
			}
		})
	}
}

// A panel admin's delete of a recording leaves a log record naming the
// recording and the panel admin, by the forum username and forum user ID
// they signed in with.
func TestRecordingDeleteLogsTheRecordingAndWhoDeletedIt(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.record(t, a.discordID)
	signIn(t, w.forum, w.b)
	confirm := deleteConfirm(t, w.b, rec)
	logs := captureLogs(t)

	submit(t, w.b, confirm)

	want := map[string]string{"recording_id": strconv.FormatInt(rec, 10), "username": testUsername, "forum_user_id": strconv.Itoa(testUserID)}
	for _, record := range logs() {
		if containsAll(record, want) {
			return
		}
	}
	t.Errorf("no log record carries %v; records: %v", want, logs())
}

// containsAll reports whether a log record carries every field given, with
// its value.
func containsAll(record, fields map[string]string) bool {
	for k, v := range fields {
		if record[k] != v {
			return false
		}
	}
	return true
}

// A form on another site can send a recording's Confirm from its
// starter's signed-in browser, which carries the starter's cookie. Sent
// that way, by the method the Confirm's form declares, it is refused and
// deletes nothing. The same Confirm sent from the panel's own page then
// deletes the recording.
func TestRecordingDeleteSentFromAnotherSiteIsRefused(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.record(t, a.discordID)
	signInAs(t, w.forum, w.b, a.account)
	confirm := deleteConfirm(t, w.b, rec)

	res := submitDeclared(t, w.b, confirm, http.Header{"Origin": {"https://elsewhere.example"}, "Sec-Fetch-Site": {"cross-site"}})

	if !isClientError(res.StatusCode) {
		t.Errorf("the Confirm sent from another site answered %d, want a refusal", res.StatusCode)
	}
	if _, ok := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]; !ok {
		t.Fatalf("the Confirm sent from another site deleted recording %d", rec)
	}
	submitDeclared(t, w.b, confirm, nil)
	if _, ok := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]; ok {
		t.Errorf("the same Confirm sent from the panel's page left recording %d listed", rec)
	}
}

// A recording's Confirm posted while it is still recording, from an
// address typed by hand since its page offers no Delete then, deletes
// nothing. Its starter stops it afterwards, and it is listed stopped with
// its zip to download.
func TestRecordingStillRecordingIsNotDeleted(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.startRecording(t, a.discordID)
	signInAs(t, w.forum, w.b, a.account)

	w.b.postForm(recordingDeletePath(rec), nil)

	w.stopRecording(t, a.discordID)
	page := parseHTML(t, w.b.get(recordingsPath))
	if _, ok := listedRecordings(t, page)[rec]; !ok {
		t.Fatalf("recording %d isn't listed once stopped", rec)
	}
	res := w.b.get(linkHref("zip-download")(t, page, rec))
	body := readBody(t, res)
	if _, err := zip.NewReader(bytes.NewReader(body), int64(len(body))); res.StatusCode != http.StatusOK || err != nil {
		t.Errorf("recording %d's zip answered %d, a zip read error %v, want 200 with a zip", rec, res.StatusCode, err)
	}
}

// A starter who deletes their only recording lands on a page saying it was
// deleted.
func TestRecordingDeleteLandsOnAPageSayingItWasDeleted(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.record(t, a.discordID)
	signInAs(t, w.forum, w.b, a.account)

	doc := parseHTML(t, follow(t, w.b, submit(t, w.b, deleteConfirm(t, w.b, rec))))

	if findElement(doc, "", "data-field", "deleted") == nil {
		t.Error("the page the delete lands on doesn't say the recording was deleted")
	}
}

// A starter who isn't a panel admin, here a Foxhole manager and an
// outsider, deletes their only recording and lands on the Recordings page
// saying it was deleted and that none are left, as a panel admin does.
func TestStarterWhoDeletesTheirLastRecordingLandsOnTheRecordingsPage(t *testing.T) {
	cases := []struct {
		who      string
		add      func(*fakeForum) *forumAccount
		username string
	}{
		{"a Foxhole manager", addFoxholeManager, managerUsername},
		{"an outsider", addOutsider, outsiderUsername},
	}
	for _, tc := range cases {
		t.Run(tc.who, func(t *testing.T) {
			w := newTestWorld(t)
			account := tc.add(w.forum)
			w.milpacs.set(tc.username, "discord-m")
			rec := w.record(t, "discord-m")
			signInAs(t, w.forum, w.b, account)

			res := follow(t, w.b, submit(t, w.b, deleteConfirm(t, w.b, rec)))

			if res.StatusCode != http.StatusOK {
				t.Errorf("the page the delete lands on has status %d, want 200", res.StatusCode)
			}
			doc := parseHTML(t, res)
			assertRecordingsPageEmpty(t, doc, noRecordingsLeft)
			if findElement(doc, "", "data-field", "deleted") == nil {
				t.Error("the page the delete lands on doesn't say the recording was deleted")
			}
		})
	}
}

// A panel admin who deletes a recording someone else started, as for an S6
// deletion ticket, lands on the list of every recording, where the next
// one is.
func TestRecordingDeletedByAPanelAdminLandsOnEveryRecording(t *testing.T) {
	w := newTestWorld(t)
	b := w.addStarter(5002, "Baker.B", "discord-b")
	c := w.addStarter(5003, "Charlie.C", "discord-c")
	recB := w.record(t, b.discordID)
	recC := w.record(t, c.discordID)
	signIn(t, w.forum, w.b)

	doc := parseHTML(t, follow(t, w.b, submit(t, w.b, deleteConfirm(t, w.b, recB))))

	if _, ok := listedRecordings(t, doc)[recC]; !ok {
		t.Errorf("the page the admin's delete lands on doesn't list C's recording %d", recC)
	}
}
