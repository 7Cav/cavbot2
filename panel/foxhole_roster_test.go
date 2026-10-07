package panel

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"golang.org/x/net/html"
)

// The Foxhole page's Add a unit's roster block (#452, spec #434 "Add a
// unit's roster"). The 7Cav API is an httptest server reached through the
// base URL production uses, so the page's roster lookup is the real one.

// rosterSearchPath is where the 7Cav API answers the position-group search
// for D/ACD, the one validated internal unit (ADR 0009).
const rosterSearchPath = "/milpacs/position/search/D/ACD"

// rosterAPI plays the 7Cav API's position-group search for D/ACD and counts
// every request it gets, at any path.
type rosterAPI struct {
	mu       sync.Mutex
	status   int
	troopers []utils.LiteProfileResponse
	requests int
}

// serveRoster points the 7Cav API at a test server answering D/ACD's
// roster with the troopers given, for the rest of the test.
func serveRoster(t *testing.T, troopers ...utils.LiteProfileResponse) *rosterAPI {
	t.Helper()
	api := &rosterAPI{status: http.StatusOK, troopers: troopers}
	srv := httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(srv.Close)
	t.Cleanup(utils.SetAPIBaseURLForTest(srv.URL))
	return api
}

func (a *rosterAPI) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests++
	if r.URL.Path != rosterSearchPath {
		http.NotFound(w, r)
		return
	}
	if a.status != http.StatusOK {
		w.WriteHeader(a.status)
		return
	}
	profiles := map[string]utils.LiteProfileResponse{}
	for _, p := range a.troopers {
		profiles[p.User.Username] = p
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(utils.LiteRosterResponse{LiteProfiles: profiles})
}

// requestCount is how many requests the API has had.
func (a *rosterAPI) requestCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests
}

// setTroopers makes the roster the troopers given from now on.
func (a *rosterAPI) setTroopers(troopers ...utils.LiteProfileResponse) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.troopers = troopers
}

// setStatus makes the roster search answer the status given from now on,
// with no body for any status but 200.
func (a *rosterAPI) setStatus(status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = status
}

// trooper is a roster trooper with the forum username and the Discord ID
// on their milpac, empty for none, the way the command suite encodes it.
func trooper(username, discordID string) utils.LiteProfileResponse {
	return utils.LiteProfileResponse{User: utils.User{Username: username}, DiscordID: discordID}
}

// rosterBlock returns the page's Add a unit's roster block, and fails the
// test when the page has none.
func rosterBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	block := findElement(doc, "", "data-field", "add-roster")
	if block == nil {
		t.Fatal("the page has no Add a unit's roster block")
	}
	return block
}

// The block offers the validated internal units the command's picker
// offers, each under the same label.
func TestRosterBlockOffersTheUnitsTheCommandsPickerOffers(t *testing.T) {
	w := newFoxholeWorld(t)

	block := rosterBlock(t, parseHTML(t, w.b.get(foxholePath)))

	var offered []string
	eachElement(block, func(n *html.Node) {
		if n.Data == "option" {
			offered = append(offered, attrOf(n, "value")+" "+textOf(n))
		}
	})
	var want []string
	for _, opt := range commands.FoxholeBulkAddInternal(nil).Definition.Options {
		for _, choice := range opt.Choices {
			want = append(want, choice.Value.(string)+" "+choice.Name)
		}
	}
	if slices.Sort(offered); !slices.Equal(offered, sorted(want)) {
		t.Errorf("the block offers the units %q, want the command's %q", offered, want)
	}
}

// previewRoster loads the Foxhole page and presses Preview roster in its Add
// a unit's roster block with the unit given picked, the way a browser posts
// the form.
func previewRoster(t *testing.T, w *testWorld, unit string) *http.Response {
	t.Helper()
	page := parseHTML(t, w.b.get(foxholePath))
	form := findElement(rosterBlock(t, page), "form", "data-field", "roster-form")
	if form == nil {
		t.Fatal("the Add a unit's roster block has no form")
	}
	fields := formFields(page, form)
	fields.Set(fieldUnit, unit)
	return w.b.postForm(attrOf(form, "action"), fields)
}

// rosterPreviewBlock returns the page's roster preview, and fails the test
// when the page has none.
func rosterPreviewBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	preview := findElement(doc, "", "data-field", "roster-preview")
	if preview == nil {
		t.Fatal("the page has no roster preview")
	}
	return preview
}

// rosterRows returns the roster preview's rows keyed by the forum username
// each names.
func rosterRows(preview *html.Node) map[string]*html.Node {
	rows := map[string]*html.Node{}
	eachElement(preview, func(n *html.Node) {
		if name, ok := attrValue(n, "data-trooper"); ok && n.Data == "tr" {
			rows[name] = n
		}
	})
	return rows
}

// The roster add's four troopers: one in the server without Internal, one
// holding it, one whose milpac names a Discord ID no member of the server
// has, and one whose milpac names no Discord account.
var (
	trooperVance  = trooper("Vance.R", memberVance.ID)
	trooperDoe    = trooper("Doe.J", memberDoe.ID)
	trooperGone   = trooper("Gone.P", "100000000000000099")
	trooperNolink = trooper("Nolink.Q", "")
)

// Preview roster fetches the unit's roster from the 7Cav API and shows a
// row per forum username on it, saying whether the trooper gets Internal,
// already holds it, isn't in the server, or has no Discord account on the
// milpac.
func TestRosterPreviewGivesEachTroopersResult(t *testing.T) {
	w := newFoxholeWorld(t)
	serveRoster(t, trooperVance, trooperDoe, trooperGone, trooperNolink)

	rows := rosterRows(rosterPreviewBlock(t, parseHTML(t, previewRoster(t, w, "D/ACD"))))

	want := map[string][2]string{
		"Vance.R":  {"gets", memberVance.ID},
		"Doe.J":    {"holding", memberDoe.ID},
		"Gone.P":   {"not-in-server", trooperGone.DiscordID},
		"Nolink.Q": {"no-discord", ""},
	}
	if got := keys(rows); !slices.Equal(got, keys(want)) {
		t.Errorf("the preview has rows for %v, want one for each trooper on the roster, %v", got, keys(want))
	}
	for name, w := range want {
		row, ok := rows[name]
		if !ok {
			continue
		}
		if got := attrOf(row, "data-result"); got != w[0] {
			t.Errorf("%s's row reads %q, want %q", name, got, w[0])
		}
		if got := attrOf(row, "data-member"); got != w[1] {
			t.Errorf("%s's row names member %q, want %q", name, got, w[1])
		}
	}
}

// Managers pick from the validated units and never type one, so a typo
// can't add half the regiment. A Preview roster posting a unit outside the
// registry, as a crafted form could, is refused before any roster fetch.
func TestRosterPreviewOfAUnitOutsideTheRegistryIsRefusedBeforeAnyFetch(t *testing.T) {
	w := newFoxholeWorld(t)
	api := serveRoster(t, trooperVance)
	page := parseHTML(t, w.b.get(foxholePath))
	fields := formFields(page, findElement(rosterBlock(t, page), "form", "data-field", "roster-form"))
	fields.Set(fieldUnit, "7")

	res := w.b.postForm(foxholeRosterPreviewPath, fields)

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want a client error", res.StatusCode)
	}
	if n := api.requestCount(); n != 0 {
		t.Errorf("the 7Cav API got %d requests, want none", n)
	}
}

// Each roster row shows its member's note, read-only, whether or not they
// are in the server, and a line above the table counts the members in the
// preview with a note.
func TestRosterPreviewRowsShowEachMembersNoteWithACount(t *testing.T) {
	w := newFoxholeWorld(t)
	serveRoster(t, trooperVance, trooperDoe, trooperGone, trooperNolink)
	seedNote(t, w, namesOf(memberVance), "discharged 12 Sep, ask S1 before re-adding")
	seedNote(t, w, store.MemberNames{MemberID: trooperGone.DiscordID, DisplayName: "PFC Gone.P", Username: "pgone"}, "left for another unit")

	preview := rosterPreviewBlock(t, parseHTML(t, previewRoster(t, w, "D/ACD")))

	rows := rosterRows(preview)
	for name, want := range map[string]string{"Vance.R": "discharged 12 Sep, ask S1 before re-adding", "Gone.P": "left for another unit"} {
		row, ok := rows[name]
		if !ok {
			t.Fatalf("the preview has no row for %s", name)
		}
		if got := fieldText(t, row, "note"); got != want {
			t.Errorf("%s's row shows the note %q, want %q", name, got, want)
		}
	}
	if got := fieldText(t, preview, "note-count"); got != "2" {
		t.Errorf("the preview counts %s members with a note, want 2", got)
	}
}

// A page load never waits on or fails with the 7Cav API: it makes no call
// to it, and how many on the roster hold Internal shows in the roster
// preview instead. The page load half is a regression pin, green from the
// roster block's first build.
func TestRosterHoldingCountShowsInThePreviewNotOnThePage(t *testing.T) {
	w := newFoxholeWorld(t)
	api := serveRoster(t, trooperVance, trooperDoe, trooper("Ash.R", memberAsh.ID))

	w.b.get(foxholePath)

	if n := api.requestCount(); n != 0 {
		t.Errorf("a page load made %d requests to the 7Cav API, want none", n)
	}
	preview := rosterPreviewBlock(t, parseHTML(t, previewRoster(t, w, "D/ACD")))
	if got := fieldText(t, preview, "holding-count"); got != "2" {
		t.Errorf("the preview counts %s on the roster holding Internal, want 2", got)
	}
}

// While the member list is partial the page can't tell who holds Internal
// or is in the server, so a Preview roster pressed then is refused: the page
// says nothing changed, opens no roster preview and fetches no roster.
func TestRosterPreviewWhileTheMemberListIsPartialIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	w.p.pageBudget = partialListBudget
	api := serveRoster(t, trooperVance)
	page := parseHTML(t, w.b.get(foxholePath))
	fields := formFields(page, findElement(rosterBlock(t, page), "form", "data-field", "roster-form"))
	w.discord.setMemberList(arrivingList)

	doc := parseHTML(t, w.b.postForm(foxholeRosterPreviewPath, fields))

	if findLive(doc, "", "data-error", "member-list") == nil {
		t.Error("the page doesn't say the preview was refused for the member list")
	}
	if findElement(doc, "", "data-field", "roster-preview") != nil {
		t.Error("the page opens a roster preview from a partial member list")
	}
	if n := api.requestCount(); n != 0 {
		t.Errorf("the 7Cav API got %d requests, want none", n)
	}
}

// A validated unit's roster is fixed input, so an empty one is a fault, not
// an empty unit (ADR 0002). Preview roster then says nothing changed and
// opens no roster preview, and the empty roster reaches Sentry once. A
// roster fetch that fails says the same.
func TestRosterPreviewOfAnEmptyOrUnfetchableRosterSaysNothingChanged(t *testing.T) {
	cases := []struct {
		name   string
		status int
		empty  bool
	}{
		{"empty roster", http.StatusOK, true},
		{"fetch failure", http.StatusInternalServerError, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			reported := recordSentry(t)
			serveRoster(t).setStatus(tc.status)

			doc := parseHTML(t, previewRoster(t, w, "D/ACD"))

			if refusal := findLive(doc, "div", "role", "alert"); refusal == nil || attrOf(refusal, "data-error") == "" {
				t.Error("the page doesn't say the preview was refused")
			}
			if findElement(doc, "", "data-field", "roster-preview") != nil {
				t.Error("the page opens a roster preview")
			}
			if n := len(reported.recorded()); tc.empty && n != 1 {
				t.Errorf("Sentry got %d events for the empty roster, want 1", n)
			}
		})
	}
}

// confirmRoster presses Confirm in the page's roster preview, the way a
// browser posts the form. It fails the test when the preview offers no
// Confirm to press.
func confirmRoster(t *testing.T, b *browser, doc *html.Node) *http.Response {
	t.Helper()
	preview := rosterPreviewBlock(t, doc)
	if button := findElement(preview, "button", "data-field", "confirm"); button == nil || disabled(button) {
		t.Fatal("the roster preview offers no Confirm to press")
	}
	return b.postForm(foxholeRosterPath, formPosts(t, doc, foxholeRosterPath))
}

// trooperReasons returns the troopers a report list names, by forum
// username, each with the reason code it gives.
func trooperReasons(list *html.Node) map[string]string {
	out := map[string]string{}
	eachElement(list, func(n *html.Node) {
		if name, ok := attrValue(n, "data-trooper"); ok {
			out[name] = attrOf(n, "data-reason")
		}
	})
	return out
}

// Confirming a roster preview adds the whole roster as previewed: Internal
// for each trooper in the server who doesn't hold it, and no role change
// for anyone else. The report lists the rest as skipped, each with why: the
// trooper who already holds Internal, the one not in the server, and the
// one with no Discord account on the milpac.
func TestConfirmedRosterAddGivesInternalToEachTrooperInTheServerWithoutIt(t *testing.T) {
	w := newFoxholeWorld(t)
	serveRoster(t, trooperVance, trooperDoe, trooperGone, trooperNolink)
	doc := parseHTML(t, previewRoster(t, w, "D/ACD"))

	assertRedirect(t, confirmRoster(t, w.b, doc), foxholePath)
	w.awaitActionEnd(t)

	want := sorted([]string{memberDoe.ID, memberAsh.ID, memberMarsh.ID, memberVance.ID})
	if got := w.discord.holdersOf(roleInternal); !slices.Equal(got, want) {
		t.Errorf("after the roster add %v hold Internal, want %v", got, want)
	}
	var writes []string
	for _, write := range w.discord.roleChanges() {
		writes = append(writes, write.MemberID+" "+write.RoleID)
	}
	if want := []string{memberVance.ID + " " + roleInternal}; !slices.Equal(writes, want) {
		t.Errorf("the roster add made the role changes %v, want %v", writes, want)
	}
	skipped := trooperReasons(reportList(t, reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "skipped"))
	if want := map[string]string{"Doe.J": "holding", "Gone.P": "not-in-server", "Nolink.Q": "no-discord"}; !maps.Equal(skipped, want) {
		t.Errorf("the report skipped %v, want %v", skipped, want)
	}
}

// Every role change a roster add makes carries the audit log reason the
// spec fixes, naming the unit's roster add and the forum user who started
// it, so a Discord moderator knows who did it without opening the panel.
func TestRosterAddRoleChangesNameTheUnitAndTheForumUserWhoStartedIt(t *testing.T) {
	w := newFoxholeWorld(t)
	serveRoster(t, trooperVance, trooper("Kestrel.T", memberKestrel.ID))
	doc := parseHTML(t, previewRoster(t, w, "D/ACD"))

	assertRedirect(t, confirmRoster(t, w.b, doc), foxholePath)
	w.awaitActionEnd(t)

	writes := w.discord.roleChanges()
	if len(writes) == 0 {
		t.Fatal("the roster add changed no role")
	}
	want := "Panel: Foxhole D/ACD roster add by Smith.F (forum user 2468)"
	for _, write := range writes {
		if write.Reason != want {
			t.Errorf("the change to %s carries the reason %q, want %q", write.MemberID, write.Reason, want)
		}
	}
}

// Confirm takes the roster as previewed. When the unit's roster has
// changed by the time Confirm is pressed, nothing starts: the page says the
// roster changed and shows the roster preview as it stands now, to check
// and confirm again.
func TestRosterAddConfirmedAfterTheRosterChangedStartsNothing(t *testing.T) {
	w := newFoxholeWorld(t)
	api := serveRoster(t, trooperVance)
	doc := parseHTML(t, previewRoster(t, w, "D/ACD"))
	api.setTroopers(trooperVance, trooper("Kestrel.T", memberKestrel.ID))

	after := parseHTML(t, confirmRoster(t, w.b, doc))

	if findLive(after, "", "data-error", "roster-changed") == nil {
		t.Error("the page doesn't say the roster changed since the preview")
	}
	if got, want := keys(rosterRows(rosterPreviewBlock(t, after))), []string{"Kestrel.T", "Vance.R"}; !slices.Equal(got, want) {
		t.Errorf("the roster preview lists %v, want the roster as it stands now, %v", got, want)
	}
	if n := len(w.discord.roleChanges()); n != 0 {
		t.Errorf("the roster add made %d role changes, want none", n)
	}
	if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "roster_add"); len(entries) != 0 {
		t.Errorf("the change log holds %d roster add entries, want none", len(entries))
	}
}

// The roster block sits behind the Foxhole page's gate: a signed-in forum
// user who opens no page gets the no-access page for Preview roster and for
// a roster preview's Confirm, which fetch no roster and start no roster add.
func TestRosterAddByAUserInNeitherGroupIsRefused(t *testing.T) {
	for _, path := range []string{foxholeRosterPreviewPath, foxholeRosterPath} {
		t.Run(path, func(t *testing.T) {
			w := newFoxholeWorld(t)
			api := serveRoster(t, trooperVance)
			preview := parseHTML(t, previewRoster(t, w, "D/ACD"))
			calls := api.requestCount()
			outsider := newBrowser(t, w.p)
			signInAs(t, w.forum, outsider, addUserOutsideAdminGroups(w.forum))

			res := outsider.postForm(path, formPosts(t, preview, foxholeRosterPath))

			assertNoAccessPage(t, parseHTML(t, follow(t, outsider, res)))
			if n := api.requestCount() - calls; n != 0 {
				t.Errorf("the 7Cav API got %d requests, want none", n)
			}
			if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "roster_add"); len(entries) != 0 {
				t.Errorf("the change log holds %d roster add entries, want none", len(entries))
			}
		})
	}
}

// A roster add that would give Internal to nobody says so and offers no
// Confirm: every trooper on the roster already holds Internal, isn't in the
// server, or has no Discord account on the milpac.
func TestRosterPreviewWithNobodyToAddCantBeConfirmed(t *testing.T) {
	w := newFoxholeWorld(t)
	serveRoster(t, trooperDoe, trooperGone, trooperNolink)

	preview := rosterPreviewBlock(t, parseHTML(t, previewRoster(t, w, "D/ACD")))

	if button := findElement(preview, "button", "data-field", "confirm"); button != nil && !disabled(button) {
		t.Error("the preview of a roster add that gives Internal to nobody offers a Confirm")
	}
	if findElement(preview, "", "data-field", "nobody-to-add") == nil {
		t.Error("the preview doesn't say there's nobody to add")
	}
}

// A roster add's Confirm refuses before it fetches the roster again when
// the add couldn't start anyway: while the member list is partial, and
// while another Foxhole action runs. Each says why, so a roster fetch that
// fails then never stands in for the real reason.
func TestRosterAddConfirmedWhenItCantStartFetchesNoRoster(t *testing.T) {
	cases := []struct {
		name    string
		refusal string
		before  func(t *testing.T, w *testWorld)
	}{
		{"member list partial", "member-list", func(_ *testing.T, w *testWorld) {
			w.discord.setMemberList(arrivingList)
		}},
		{"another action running", "action-running", func(t *testing.T, w *testWorld) {
			hold := holdRoleWrites(t, w)
			startPurge(t, w, "internal")
			hold.next(t)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			w.p.pageBudget = partialListBudget
			api := serveRoster(t, trooperVance)
			doc := parseHTML(t, previewRoster(t, w, "D/ACD"))
			tc.before(t, w)
			calls := api.requestCount()

			after := parseHTML(t, w.b.postForm(foxholeRosterPath, formPosts(t, doc, foxholeRosterPath)))

			if findLive(after, "", "data-error", tc.refusal) == nil {
				t.Errorf("the page doesn't give the %s refusal", tc.refusal)
			}
			if n := api.requestCount() - calls; n != 0 {
				t.Errorf("the 7Cav API got %d more requests, want none", n)
			}
		})
	}
}
