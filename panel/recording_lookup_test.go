package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/7cav/cavbot2/commands"
)

// A starter the panel can't find in Discord (#752). The panel knows a
// starter's recordings by the Discord ID their milpac names, looked up at
// sign-in. When that lookup fails, or the milpac names no Discord account,
// the recording pages say so and say what to do, in place of saying the
// starter has none or that the recording isn't theirs.

// checkAgainIn is the note a page shows when the panel couldn't find the
// visitor's Discord account, and the note's Check again link.
func checkAgainIn(t *testing.T, doc *html.Node) (note, link *html.Node) {
	t.Helper()
	note = findElement(doc, "", "data-field", "discord-note")
	if note == nil {
		t.Fatal("the page shows no note about the visitor's Discord account")
	}
	link = findElement(note, "a", "data-field", "check-again")
	if link == nil {
		t.Fatal("the note has no Check again link")
	}
	return note, link
}

// assertDiscordNote checks a page shows the note of the kind given, with a
// Check again link to the address given, and returns the note.
func assertDiscordNote(t *testing.T, doc *html.Node, want discordNote, checkAgain string) *html.Node {
	t.Helper()
	note, link := checkAgainIn(t, doc)
	if got, _ := attrValue(note, "data-note"); got != string(want) {
		t.Errorf("the note is %q, want %q", got, want)
	}
	if href, _ := attrValue(link, "href"); href != checkAgain {
		t.Errorf("the note's Check again link goes to %q, want %s", href, checkAgain)
	}
	return note
}

// A starter whose milpac lookup fails at sign-in still signs in. Their
// Recordings page says the panel couldn't check their Discord account, not
// that they have no recordings, and its Check again link reloads the page.
func TestRecordingsPageAfterAFailedMilpacLookupSaysSo(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	w.record(t, x.discordID)
	w.milpacs.fail("Xray.X", http.StatusInternalServerError)

	res := signInAs(t, w.forum, w.b, x.account)

	assertRedirect(t, res, "/")
	doc := parseHTML(t, follow(t, w.b, res))
	assertDiscordNote(t, doc, noteLookupFailed, recordingsPath)
	if findElement(doc, "", "data-field", "no-recordings") != nil {
		t.Error("the Recordings page says the starter has no recordings")
	}
}

// A starter whose milpac names no Discord account, since their forum
// account has none connected, sees a note on the Recordings page that
// links to the forum page where they connect one, with Check again.
func TestRecordingsPageOfAStarterWithNoConnectedDiscordAccountSaysSo(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	w.record(t, x.discordID)
	w.milpacs.set("Xray.X", "")

	doc := parseHTML(t, follow(t, w.b, signInAs(t, w.forum, w.b, x.account)))

	note := assertDiscordNote(t, doc, noteNotConnected, recordingsPath)
	const connect = "https://7cav.us/account/connected-accounts/"
	if findElement(note, "a", "href", connect) == nil {
		t.Errorf("the note has no link to %s", connect)
	}
}

// pressCheckAgain presses the Check again link of the note a page shows
// about the visitor's Discord account.
func pressCheckAgain(t *testing.T, b *browser, doc *html.Node) *html.Node {
	t.Helper()
	_, link := checkAgainIn(t, doc)
	_, res := press(t, b, doc, link)
	return parseHTML(t, res)
}

// A starter the panel couldn't find at sign-in is found once the 7Cav API
// recovers, or once their milpac names their Discord account. The next load
// of the Recordings page, or of the recording's address, from the note's
// Check again, shows their recording, and the load after it makes no
// milpac lookup.
func TestRecordingPagesFindTheStarterOnceTheMilpacLookupCan(t *testing.T) {
	recovers := func(api *milpacAPI) { api.restore("Xray.X") }
	failing := func(api *milpacAPI) { api.fail("Xray.X", http.StatusInternalServerError) }
	listPage := func(int64) string { return recordingsPath }
	for _, tc := range []struct {
		name            string
		atSignin, later func(api *milpacAPI)
		page            func(rec int64) string
	}{
		{"the 7Cav API recovers, on the Recordings page", failing, recovers, listPage},
		{"the 7Cav API recovers, on the recording's address", failing, recovers, commands.RecordingPath},
		{"the milpac names their Discord account, on the Recordings page",
			func(api *milpacAPI) { api.set("Xray.X", "") },
			func(api *milpacAPI) { api.set("Xray.X", "discord-x") }, listPage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			x := w.addStarter(5003, "Xray.X", "discord-x")
			rec := w.record(t, x.discordID)
			tc.atSignin(w.milpacs)
			signInAs(t, w.forum, w.b, x.account)
			unmatched := parseHTML(t, w.b.get(tc.page(rec)))
			tc.later(w.milpacs)

			doc := pressCheckAgain(t, w.b, unmatched)

			if _, ok := listedRecordings(t, doc)[rec]; !ok {
				t.Errorf("%s doesn't show recording %d", tc.page(rec), rec)
			}
			if n := w.milpacs.lookupsDuring("Xray.X", func() { w.b.get(tc.page(rec)) }); n != 0 {
				t.Errorf("the load after made %d milpac lookups, want none", n)
			}
		})
	}
}

// A page lookup moves the session to what it finds. A session whose milpac
// named no Discord account and whose page lookup fails shows the
// failed-lookup note. A session whose lookup failed and whose page lookup
// finds no milpac says there are no recordings yet, and looks no more.
func TestRecordingsPageGoesByItsOwnMilpacLookup(t *testing.T) {
	t.Run("no Discord account, then a failed lookup", func(t *testing.T) {
		w := newTestWorld(t)
		x := w.addStarter(5003, "Xray.X", "discord-x")
		w.record(t, x.discordID)
		w.milpacs.set("Xray.X", "")
		signInAs(t, w.forum, w.b, x.account)
		w.milpacs.fail("Xray.X", http.StatusInternalServerError)

		assertDiscordNote(t, parseHTML(t, w.b.get(recordingsPath)), noteLookupFailed, recordingsPath)
	})
	t.Run("a failed lookup, then no milpac", func(t *testing.T) {
		w := newTestWorld(t)
		x := w.addStarter(5003, "Xray.X", "discord-x")
		w.record(t, x.discordID)
		w.milpacs.fail("Xray.X", http.StatusInternalServerError)
		signInAs(t, w.forum, w.b, x.account)
		w.milpacs.fail("Xray.X", http.StatusNotFound)

		assertEmptyRecordingsPageFor(t, w.b.get(recordingsPath), noRecordingsYet, true)
		if n := w.milpacs.lookupsDuring("Xray.X", func() { w.b.get(recordingsPath) }); n != 0 {
			t.Errorf("the load after made %d milpac lookups, want none", n)
		}
	})
}

// A starter the panel couldn't find in Discord who opens their recording's
// address, as its notice or DM links it, sees the note in place of the
// recording, with none of its details. The note's Check again reloads the
// address.
func TestRecordingAddressOfAStarterThePanelCantFindSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		atSignin func(api *milpacAPI)
		want     discordNote
	}{
		{"the lookup failed", func(api *milpacAPI) { api.fail("Xray.X", http.StatusInternalServerError) }, noteLookupFailed},
		{"no Discord account connected", func(api *milpacAPI) { api.set("Xray.X", "") }, noteNotConnected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			x := w.addStarter(5003, "Xray.X", "discord-x")
			rec := w.record(t, x.discordID)
			tc.atSignin(w.milpacs)
			signInAs(t, w.forum, w.b, x.account)

			doc := parseHTML(t, w.b.get(commands.RecordingPath(rec)))

			assertDiscordNote(t, doc, tc.want, commands.RecordingPath(rec))
			if listed := listedRecordings(t, doc); len(listed) != 0 {
				t.Errorf("the page shows recording %d's details", rec)
			}
		})
	}
}

// A recording's download and its delete Confirm, reached by a starter the
// panel couldn't find in Discord, say from a page loaded before, serve
// nothing and lead to the recording's address, which says why.
func TestRecordingRoutesOfAStarterThePanelCantFindLeadToItsAddress(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	rec := w.record(t, x.discordID)
	w.milpacs.fail("Xray.X", http.StatusInternalServerError)
	signInAs(t, w.forum, w.b, x.account)
	admin := newBrowser(t, w.p)
	signIn(t, w.forum, admin)

	t.Run("mix", func(t *testing.T) {
		href := linkHref("mix-download")(t, parseHTML(t, admin.get(commands.RecordingPath(rec))), rec)

		assertRedirect(t, w.b.get(href), commands.RecordingPath(rec))
	})
	t.Run("delete Confirm", func(t *testing.T) {
		confirm := deleteConfirm(t, admin, rec)

		assertRedirect(t, submit(t, w.b, confirm), commands.RecordingPath(rec))
	})
}

// A starter whose sign-in found their Discord account opens the Recordings
// page and their recording's address with no further milpac lookup.
func TestRecordingPagesOfAStarterFoundAtSignInLookNoMore(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.record(t, a.discordID)
	signInAs(t, w.forum, w.b, a.account)

	n := w.milpacs.lookupsDuring("Able.A", func() {
		w.b.get(recordingsPath)
		w.b.get(commands.RecordingPath(rec))
	})

	if n != 0 {
		t.Errorf("the page loads made %d milpac lookups, want none", n)
	}
}

// A panel admin whose milpac lookup fails at sign-in sees the note in
// place of their own recordings. Every recording, and each recording's
// address, open as they do for any panel admin.
func TestPanelAdminWhoseMilpacLookupFailsSeesTheNoteInTheirOwnListOnly(t *testing.T) {
	w := newTestWorld(t)
	a := w.addStarter(5001, "Able.A", "discord-a")
	rec := w.record(t, a.discordID)
	w.milpacs.fail(testUsername, http.StatusInternalServerError)
	signIn(t, w.forum, w.b)

	assertDiscordNote(t, parseHTML(t, w.b.get(recordingsPath)), noteLookupFailed, recordingsPath)
	for _, page := range []string{recordingsPath + "?" + paramView + "=" + viewAll, commands.RecordingPath(rec)} {
		if _, ok := listedRecordings(t, parseHTML(t, w.b.get(page)))[rec]; !ok {
			t.Errorf("%s doesn't show recording %d", page, rec)
		}
	}
}

// A milpac lookup that hangs at sign-in ends the sign-in once 3 seconds
// pass, and the starter lands on the failed-lookup note while the 7Cav API
// is still down.
func TestSignInWhoseMilpacLookupHangsEndsAfterThreeSeconds(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	w.record(t, x.discordID)
	waiting := w.milpacs.hold("Xray.X")
	q := assertRedirect(t, w.b.post("/auth/start"), "/oauth2/authorize").Query()
	w.forum.setChallenge(q.Get("code_challenge"))
	done := make(chan *http.Response, 1)
	go func() {
		done <- w.b.get("/auth/callback?code=" + url.QueryEscape(x.account.code) + "&state=" + url.QueryEscape(q.Get("state")))
	}()
	select {
	case <-waiting:
	case <-time.After(neverReleased):
		t.Fatal("the sign-in never reached the milpac lookup")
	}

	w.clock.advance(3 * time.Second)

	var res *http.Response
	select {
	case res = <-done:
	case <-time.After(neverReleased):
		// Let the lookup go, so the test can end.
		w.clock.advance(time.Hour)
		<-done
		t.Fatal("the sign-in still waits on the milpac lookup 3 seconds after it began")
	}
	w.milpacs.fail("Xray.X", http.StatusInternalServerError)
	assertDiscordNote(t, parseHTML(t, follow(t, w.b, res)), noteLookupFailed, recordingsPath)
}

// sentryMessages is the message each event recorded was captured under,
// in order.
func sentryMessages(t *testing.T, r *sentryRecorder) []string {
	t.Helper()
	var out []string
	for _, raw := range r.sent() {
		var event struct {
			Tags map[string]string `json:"tags"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("the event %s does not unmarshal: %v", raw, err)
		}
		out = append(out, event.Tags["message"])
	}
	return out
}

// A page lookup that fails reaches Sentry once, under a message of its
// own, so staff can tell it from a lookup that failed at sign-in.
func TestRecordingsPageWhoseMilpacLookupFailsIsReported(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	w.milpacs.fail("Xray.X", http.StatusInternalServerError)
	reported := recordSentry(t)
	signInAs(t, w.forum, w.b, x.account)
	atSignin := sentryMessages(t, reported)

	w.b.get(recordingsPath)

	got := sentryMessages(t, reported)[len(atSignin):]
	if len(atSignin) != 1 || len(got) != 1 || got[0] == atSignin[0] {
		t.Errorf("sign-in sent events under %q and the page load under %q, want one each under different messages", atSignin, got)
	}
}

// A milpac that names no Discord account is no fault: a sign-in and a page
// lookup that find one send nothing to Sentry, and the page lookup leaves
// an INFO line naming the forum user.
func TestRecordingsPageOfAStarterWithNoConnectedDiscordAccountIsLoggedNotReported(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	w.milpacs.set("Xray.X", "")
	reported := recordSentry(t)
	signInAs(t, w.forum, w.b, x.account)
	logs := captureLogs(t)

	w.b.get(recordingsPath)

	if n := len(reported.recorded()); n != 0 {
		t.Errorf("sent %d Sentry events, want none", n)
	}
	want := map[string]string{"level": "INFO", "username": "Xray.X"}
	for _, record := range logs() {
		if containsAll(record, want) {
			return
		}
	}
	t.Errorf("no log record carries %v; records: %v", want, logs())
}

// A browser that leaves the Recordings page while its milpac lookup waits
// is an abandoned page load, which sends nothing to Sentry.
func TestRecordingsPageAbandonedDuringTheMilpacLookupIsNotReported(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	w.milpacs.fail("Xray.X", http.StatusInternalServerError)
	signInAs(t, w.forum, w.b, x.account)
	reported := recordSentry(t)
	waiting := w.milpacs.hold("Xray.X")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.b.doContext(ctx, http.MethodGet, recordingsPath, nil, nil)
	}()
	select {
	case <-waiting:
	case <-time.After(neverReleased):
		t.Fatal("the page load never reached the milpac lookup")
	}

	cancel()
	<-done

	if n := len(reported.recorded()); n != 0 {
		t.Errorf("sent %d Sentry events, want none", n)
	}
}

// A Discord ID a page lookup finds stays on the session. A group check on
// a concurrent request, here a load of the panel's root in another tab
// that read the session before the lookup found the ID and stores it
// after, leaves the ID, so the next load lists the recording with no
// lookup.
func TestFoundDiscordIDOutlastsAConcurrentGroupCheck(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	rec := w.record(t, x.discordID)
	w.milpacs.fail("Xray.X", http.StatusInternalServerError)
	signInAs(t, w.forum, w.b, x.account)
	tab := newBrowser(t, w.p)
	tab.cookies[sessionCookie] = w.b.cookies[sessionCookie]
	waiting, release := w.forum.holdUserinfo()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tab.get("/")
	}()
	select {
	case <-waiting:
	case <-time.After(neverReleased):
		t.Fatal("the other tab's load never reached its group check")
	}
	w.milpacs.restore("Xray.X")
	if _, ok := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]; !ok {
		t.Fatalf("the Recordings page doesn't list recording %d once the 7Cav API recovers", rec)
	}

	release()
	<-done

	var listed bool
	n := w.milpacs.lookupsDuring("Xray.X", func() {
		_, listed = listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]
	})
	if !listed {
		t.Errorf("the next Recordings page load doesn't list recording %d", rec)
	}
	if n != 0 {
		t.Errorf("the next load made %d milpac lookups, want none", n)
	}
}

// A Discord ID a page lookup finds stays on the session when a slower
// lookup in another tab, which began while the 7Cav API was down, fails
// after it. The next load lists the recording with no lookup.
func TestFoundDiscordIDOutlastsASlowerFailedLookup(t *testing.T) {
	w := newTestWorld(t)
	x := w.addStarter(5003, "Xray.X", "discord-x")
	rec := w.record(t, x.discordID)
	w.milpacs.fail("Xray.X", http.StatusInternalServerError)
	signInAs(t, w.forum, w.b, x.account)
	tab := newBrowser(t, w.p)
	tab.cookies[sessionCookie] = w.b.cookies[sessionCookie]
	waiting := w.milpacs.hold("Xray.X")
	done := make(chan struct{})
	go func() {
		defer close(done)
		tab.get(recordingsPath)
	}()
	select {
	case <-waiting:
	case <-time.After(neverReleased):
		t.Fatal("the other tab's load never reached the milpac lookup")
	}
	w.milpacs.restore("Xray.X")
	if _, ok := listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]; !ok {
		w.clock.advance(time.Hour)
		<-done
		t.Fatalf("the Recordings page doesn't list recording %d once the 7Cav API recovers", rec)
	}

	w.clock.advance(3 * time.Second)
	<-done

	var listed bool
	n := w.milpacs.lookupsDuring("Xray.X", func() {
		_, listed = listedRecordings(t, parseHTML(t, w.b.get(recordingsPath)))[rec]
	})
	if !listed {
		t.Errorf("the next Recordings page load doesn't list recording %d", rec)
	}
	if n != 0 {
		t.Errorf("the next load made %d milpac lookups, want none", n)
	}
}
