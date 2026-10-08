package panel

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// The hub page and the panel's saves read the guild's channels, roles and
// boost tier from the gateway state, never Discord's API (#400). In these
// tests every read of the API fails, as it does while Discord's API is
// down, and the fake counts each one.

// optionValues returns the values a select under data-field=name offers,
// in document order, the empty placeholder option left out.
func optionValues(t *testing.T, n *html.Node, name string) []string {
	t.Helper()
	sel := findElement(n, "select", "data-field", name)
	if sel == nil {
		t.Fatalf("no select under data-field=%q", name)
	}
	var out []string
	eachLiveElement(sel, func(o *html.Node) {
		if o.Data != "option" {
			return
		}
		if value, _ := attrValue(o, "value"); value != "" {
			out = append(out, value)
		}
	})
	return out
}

func TestHubPageReadsTheGuildFromTheGatewayState(t *testing.T) {
	w := newTestWorld(t, testHub(), secondHub())
	signIn(t, w.forum, w.b)
	w.discord.breakChannel("vc-2", false)
	healthy, broken := storedHubID(t, w.st, "hub-1"), storedHubID(t, w.st, "vc-2")

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	doc := parseHTML(t, res)
	row := findElement(doc, "", "data-hub", strconv.FormatInt(healthy, 10))
	if row == nil {
		t.Fatalf("page has no row for hub %d", healthy)
	}
	if got := fieldText(t, row, "channel_name"); got != "Join to create" {
		t.Errorf("hub-1's channel shows %q, want Join to create", got)
	}
	if got := fieldText(t, row, "category_name"); got != "Arma Reforger" {
		t.Errorf("hub-1's category shows %q, want Arma Reforger", got)
	}
	if hasField(row, "broken") {
		t.Error("hub-1 shows as broken, want healthy")
	}
	if b := findElement(doc, "", "data-hub", strconv.FormatInt(broken, 10)); b == nil || !hasField(b, "broken") {
		t.Error("vc-2's hub, whose channel has no category, does not show as broken")
	}
	if got := optionValues(t, doc, "category"); !slices.Equal(got, []string{"cat-1"}) {
		t.Errorf("category picker offers %v, want cat-1", got)
	}
	if got := searchRows(pickerRoot(t, doc, "hub_channel")); !slices.Equal(got, []string{"vc-noparent"}) {
		t.Errorf("register picker offers %v, want vc-noparent, the one voice channel that is not a hub", got)
	}
	if got := searchRows(rolePickerOn(t, moderatorsSection(t, doc))); !slices.Contains(got, "role-mp") {
		t.Errorf("the guild-wide role picker offers %v, want role-mp among them", got)
	}

	sec := editSection(t, w.b.get("/?hub="+strconv.FormatInt(healthy, 10)), healthy)

	if got := inputValue(t, sec, "channel_name"); got != "Join to create" {
		t.Errorf("the edit form's channel name holds %q, want Join to create", got)
	}
	if got := searchRows(rolePickerOn(t, sec)); !slices.Contains(got, "role-mp") {
		t.Errorf("the hub's role picker offers %v, want role-mp among them", got)
	}
	if n := w.discord.apiReadCount(); n != 0 {
		t.Errorf("made %d reads of Discord's API, want none", n)
	}
}

// gatewaySave is one save kind as a user posts it, over a world holding
// testHub.
type gatewaySave struct {
	name string
	post func(t *testing.T, w *testWorld) *http.Response
}

// The saves that check their forms against the guild: a create, a
// register, an edit save that renames the hub channel, and the guild-wide
// moderator roles.
var (
	createSave = gatewaySave{"a create", func(_ *testing.T, w *testWorld) *http.Response {
		return w.b.postForm("/hubs", createForm("cat-1", "Squad Join", "Squad Voice"))
	}}
	registerSave = gatewaySave{"a register", func(_ *testing.T, w *testWorld) *http.Response {
		return w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice"))
	}}
	renameSave = gatewaySave{"an edit save", func(t *testing.T, w *testWorld) *http.Response {
		form := updateForm(t, w.st)
		form.Set("channel_name", "Join here")
		form["moderator_roles"] = []string{"role-mp"}
		return w.b.postForm(hubPath(t, w.st, "hub-1"), form)
	}}
	moderatorsSave = gatewaySave{"a moderator roles save", func(t *testing.T, w *testWorld) *http.Response {
		return w.b.postForm("/moderators", moderatorsForm(t, w.st, "role-mp"))
	}}
	gatewaySaves = []gatewaySave{createSave, registerSave, renameSave, moderatorsSave}
)

func TestSavesCheckTheirFormsAgainstTheGatewayState(t *testing.T) {
	for _, save := range gatewaySaves {
		t.Run(save.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)

			res := save.post(t, w)

			if res.StatusCode != http.StatusSeeOther {
				t.Errorf("status = %d, want 303 after a save that went through", res.StatusCode)
			}
			if n := w.discord.apiReadCount(); n != 0 {
				t.Errorf("made %d reads of Discord's API, want none", n)
			}
		})
	}
}

// wantNoGuildData is the data-failure of the page that says Discord has
// not sent the bot the server's data.
const wantNoGuildData = "no-guild-data"

// An outage's GUILD_DELETE takes the guild out of the gateway state. The
// page says so at once rather than wait out its budget, and it is
// Discord's trouble, not the panel's, so nothing reaches Sentry.
func TestHubPageWithTheGuildAbsentSaysDiscordHasNotSentIt(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	reported := recordSentry(t)
	w.discord.setGuild(commands.GuildDataAbsent)

	start := w.clock.Now()
	res := w.b.get("/")
	waited := w.clock.since(start)

	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	if got := failureOf(t, parseHTML(t, res)); got != wantNoGuildData {
		t.Errorf("failure page = %q, want %s", got, wantNoGuildData)
	}
	if waited > hubPageBudget/5 {
		t.Errorf("answered after waiting %v, want well before the %v budget", waited, hubPageBudget)
	}
	if n := len(reported.recorded()); n != 0 {
		t.Errorf("sent %d Sentry events, want none", n)
	}
}

// hangLimit bounds a test's real-time wait for something the panel or the
// runtime does, so a wait that never ends fails the test instead of hanging
// the suite.
const hangLimit = 3 * time.Second

// After a deploy the gateway state holds the READY placeholder until the
// guild's GUILD_CREATE lands. A page load waits for the data within its
// time budget, and renders once it lands. A budget that runs out first
// gets the page that says Discord has not sent the data, not the page
// that says the panel took too long, and nothing reaches Sentry.
func TestHubPageWaitsForTheGuildDataOnItsWay(t *testing.T) {
	t.Run("data that lands within the budget", func(t *testing.T) {
		w := newTestWorld(t, testHub())
		signIn(t, w.forum, w.b)
		w.discord.landAfterOneRead()

		res := w.b.get("/")

		if res.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 once the data lands", res.StatusCode)
		}
		row := findElement(parseHTML(t, res), "", "data-hub", strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10))
		if row == nil {
			t.Fatal("page has no row for hub-1")
		}
		if got := fieldText(t, row, "channel_name"); got != "Join to create" || hasField(row, "broken") {
			t.Errorf("hub-1 shows channel %q, broken %v; want Join to create, not broken", got, hasField(row, "broken"))
		}
	})

	t.Run("data that never lands", func(t *testing.T) {
		w := newTestWorld(t, testHub())
		signIn(t, w.forum, w.b)
		reported := recordSentry(t)
		w.discord.holdPlaceholder()

		start := w.clock.Now()
		res := w.b.get("/")
		waited := w.clock.since(start)

		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", res.StatusCode)
		}
		if got := failureOf(t, parseHTML(t, res)); got != wantNoGuildData {
			t.Errorf("failure page = %q, want %s", got, wantNoGuildData)
		}
		if waited < hubPageBudget {
			t.Errorf("answered after waiting %v, want the page to wait out its %v budget", waited, hubPageBudget)
		}
		if n := len(reported.recorded()); n != 0 {
			t.Errorf("sent %d Sentry events, want none", n)
		}
	})
}

// A save that meets no guild data writes nothing and answers with the page
// that says Discord has not sent the data. With the guild absent it fails
// at once; with the data on its way it waits out the page's time budget,
// which bounds the wait since a save's own context has no deadline.
func TestSaveWithNoGuildDataWritesNothing(t *testing.T) {
	for _, save := range gatewaySaves {
		t.Run(save.name+" with the guild absent", func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			w.discord.setGuild(commands.GuildDataAbsent)
			before := storedHubs(t, w.st)[0]

			res := save.post(t, w)

			assertNoGuildDataAndNothingWritten(t, w, before, res)
		})
	}

	t.Run("a create with the data never landing", func(t *testing.T) {
		w := newTestWorld(t, testHub())
		signIn(t, w.forum, w.b)
		w.discord.holdPlaceholder()
		before := storedHubs(t, w.st)[0]

		start := w.clock.Now()
		res := createSave.post(t, w)
		waited := w.clock.since(start)

		assertNoGuildDataAndNothingWritten(t, w, before, res)
		if waited < hubPageBudget {
			t.Errorf("answered after waiting %v, want the save to wait out the page's %v budget", waited, hubPageBudget)
		}
	})
}

// assertNoGuildDataAndNothingWritten checks a save answered with the page
// that says Discord has not sent the data, and changed nothing: the store
// holds the hub before as it was, with no change log entry and no
// guild-wide roles, and Discord was asked to create and rename nothing.
func assertNoGuildDataAndNothingWritten(t *testing.T, w *testWorld, before store.Hub, res *http.Response) {
	t.Helper()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", res.StatusCode)
	}
	if got := failureOf(t, parseHTML(t, res)); got != wantNoGuildData {
		t.Errorf("failure page = %q, want %s", got, wantNoGuildData)
	}
	hubs := storedHubs(t, w.st)
	if len(hubs) != 1 || !sameHubSettings(hubs[0], before) {
		t.Errorf("stored hubs = %+v, want %+v alone, unchanged", hubs, before)
	}
	if entries := storedChangeLog(t, w.st, before.ID); len(entries) != 0 {
		t.Errorf("the hub has %d change log entries, want none", len(entries))
	}
	if roles := storedGuildRoles(t, w.st); len(roles) != 0 {
		t.Errorf("stored guild-wide roles = %v, want none", roles)
	}
	if n := w.discord.createCount(); n != 0 {
		t.Errorf("made %d creates, want none", n)
	}
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("made edits %+v, want none", edits)
	}
}

// hasDisconnectedNotice reports whether a page carries the notice that the
// bot has lost its connection to Discord, found by its data-notice hook.
func hasDisconnectedNotice(doc *html.Node) bool {
	return findElement(doc, "", "data-notice", "disconnected") != nil
}

// While the gateway connection is down the state keeps the guild as it was
// when the connection dropped. The page renders from it and says the
// channel and role details may be out of date, and a save goes ahead, its
// rename still sent to Discord. Once the connection is back the notice
// goes.
func TestHubPageWithTheConnectionDownSaysTheGuildMayBeOutOfDate(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.discord.setConnected(false)

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the data the state holds", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if !hasDisconnectedNotice(doc) {
		t.Error("page carries no data-notice=disconnected while the connection is down")
	}
	row := findElement(doc, "", "data-hub", strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10))
	if row == nil || fieldText(t, row, "channel_name") != "Join to create" {
		t.Error("page does not list hub-1 with its channel from the state")
	}

	if res := renameSave.post(t, w); res.StatusCode != http.StatusSeeOther {
		t.Errorf("%s while the connection is down: status = %d, want 303", renameSave.name, res.StatusCode)
	}
	if edits := w.discord.edits(); len(edits) != 1 || edits[0].Name != "Join here" {
		t.Errorf("edits = %+v, want the rename to Join here sent", edits)
	}

	w.discord.setConnected(true)

	if hasDisconnectedNotice(parseHTML(t, w.b.get("/"))) {
		t.Error("page carries data-notice=disconnected with the connection back up")
	}
}

// Saves take turns under one lock (#373). A save that finds the guild's
// data on its way waits for it before it takes its turn, so saves made at
// the same moment wait side by side. None waits out another's budget before
// starting its own, which would push a queue of them past the reverse
// proxy's timeout.
func TestSavesWaitForTheGuildDataSideBySide(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	other := secondBrowser(t, w)
	w.discord.holdPlaceholderForTwo()
	moderators := moderatorsForm(t, w.st, "role-mp")

	type answer struct {
		res    *http.Response
		waited time.Duration
	}
	post := func(b *browser, path string, form url.Values) <-chan answer {
		done := make(chan answer, 1)
		go func() {
			start := w.clock.Now()
			res := b.postForm(path, form)
			done <- answer{res, w.clock.since(start)}
		}()
		return done
	}
	answers := map[string]<-chan answer{
		createSave.name:     post(w.b, "/hubs", createForm("cat-1", "Squad Join", "Squad Voice")),
		moderatorsSave.name: post(other, "/moderators", moderators),
	}

	// Waiting in turn, the second save would answer after two budgets.
	limit := hubPageBudget * 5 / 3
	for name, done := range answers {
		a := <-done
		if a.res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", name, a.res.StatusCode)
			continue
		}
		if got := failureOf(t, parseHTML(t, a.res)); got != wantNoGuildData {
			t.Errorf("%s: failure page = %q, want %s", name, got, wantNoGuildData)
		}
		if a.waited > limit {
			t.Errorf("%s answered after waiting %v, want within %v: saves wait for the data side by side, not in turn", name, a.waited, limit)
		}
	}
}
