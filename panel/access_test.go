package panel

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// outsiderUsername is the forum username of the outsider addOutsider adds.
const outsiderUsername = "Roe.R"

// addOutsider makes the forum know an outsider.
func addOutsider(f *fakeForum) *forumAccount {
	return f.addUser(5678, outsiderUsername, 2, []int{35, 72})
}

// assertNoAccessPage checks a page is the no-access page and carries none of
// the hub page's settings.
func assertNoAccessPage(t *testing.T, doc *html.Node) {
	t.Helper()
	if findElement(doc, "", "data-field", "no-access") == nil {
		t.Error("the page has no data-field=no-access")
	}
	if findElement(doc, "", "data-section", "moderators") != nil {
		t.Error("the page shows the guild-wide moderator section")
	}
}

// pageLoadCases is a case for each page the panel serves behind a gate:
// the path loaded, the sign-in of a user the page admits, and the page that
// user gets there.
var pageLoadCases = []struct {
	path  string
	admit func(t *testing.T, f *fakeForum, b *browser) *http.Response
	page  string
}{
	{"/", signIn, pageHubs},
	{foxholePath, func(t *testing.T, f *fakeForum, b *browser) *http.Response {
		return signInAs(t, f, b, addFoxholeManager(f))
	}, pageFoxhole},
}

// An outsider who loads a page behind a gate gets the no-access page, with
// no settings on it. A user the page admits then loads it and gets the
// page, so no case passes on a route that refuses everyone.
func TestPageLoadByAnOutsiderIsRefused(t *testing.T) {
	for _, tc := range pageLoadCases {
		t.Run("GET "+tc.path, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signInAs(t, w.forum, w.b, addOutsider(w.forum))

			res := follow(t, w.b, w.b.get(tc.path))

			assertNoAccessPage(t, parseHTML(t, res))
			admitted := newBrowser(t, w.p)
			tc.admit(t, w.forum, admitted)
			if got := pageOf(t, parseHTML(t, follow(t, admitted, admitted.get(tc.path)))); got != tc.page {
				t.Errorf("a user the page admits lands on page %q, want %s", got, tc.page)
			}
		})
	}
}

// settingsSaveCases is a case for each save the hub page posts: a hub's
// create, register, update and remove, and the guild-wide moderator roles.
// Each reads the path it posts to and its form off a store that holds
// testHub.
var settingsSaveCases = []struct {
	name string
	path func(t *testing.T, st store.Store) string
	form func(t *testing.T, st store.Store) url.Values
}{
	{
		name: "create",
		path: func(*testing.T, store.Store) string { return "/hubs" },
		form: fixedForm(createForm("cat-1", "Squad Join", "Squad Voice")),
	},
	{
		name: "register",
		path: func(*testing.T, store.Store) string { return "/hubs" },
		form: fixedForm(registerForm("vc-2", "Squad Voice")),
	},
	{
		name: "update",
		path: func(t *testing.T, st store.Store) string { return hubPath(t, st, "hub-1") },
		form: func(t *testing.T, st store.Store) url.Values {
			form := updateForm(t, st)
			form.Set("base_string", "Bravo Voice")
			return form
		},
	},
	{
		name: "remove",
		path: func(t *testing.T, st store.Store) string { return hubPath(t, st, "hub-1") + "/remove" },
		form: fixedForm(nil),
	},
	{
		name: "moderators",
		path: func(*testing.T, store.Store) string { return "/moderators" },
		form: func(t *testing.T, st store.Store) url.Values { return moderatorsForm(t, st, "role-hq") },
	},
}

// A forum user in none of the panel's admin groups saves nothing: each
// settings save they post answers with the no-access page, which says
// nothing changed, and leaves the store as it was. A panel admin then
// posts the same form and it saves, so no row passes on a form the save
// would refuse anyway. The Foxhole manager's rows are regression pins: they
// passed before the Foxhole page existed, when the group check read a
// Foxhole manager as an outsider. They pin that no hub route moved
// under the Foxhole page's gate.
func TestUserOutsideTheAdminGroupsSavesNothing(t *testing.T) {
	users := []struct {
		name string
		add  func(*fakeForum) *forumAccount
	}{
		{"outsider", addOutsider},
		{"Foxhole manager", addFoxholeManager},
	}
	for _, u := range users {
		for _, tc := range settingsSaveCases {
			t.Run(u.name+"/"+tc.name, func(t *testing.T) {
				w := newTestWorld(t, testHub())
				signInAs(t, w.forum, w.b, u.add(w.forum))
				path, form := tc.path(t, w.st), tc.form(t, w.st)
				before := readSavedState(t, w.st)

				res := w.b.postForm(path, form)

				assertNoAccessPage(t, parseHTML(t, follow(t, w.b, res)))
				if after := readSavedState(t, w.st); !reflect.DeepEqual(after, before) {
					t.Errorf("the store after the %s's save = %+v, want it as before, %+v", u.name, after, before)
				}
				panelAdmin := newBrowser(t, w.p)
				signIn(t, w.forum, panelAdmin)
				panelAdmin.postForm(path, form)
				if after := readSavedState(t, w.st); reflect.DeepEqual(after, before) {
					t.Error("the same form posted by a panel admin saved nothing: the row's form is one a save refuses")
				}
			})
		}
	}
}

// foxholeEffects is everything a Foxhole POST route can change: the
// store's Foxhole records and change log, Discord's member roles, the
// running Foxhole action and the 7Cav API's requests.
type foxholeEffects struct {
	Store       noteState
	RoleWrites  []fakeRoleWrite
	Running     commands.RunningAction
	IsRunning   bool
	APIRequests int
}

func readFoxholeEffects(t *testing.T, w *testWorld, api *rosterAPI) foxholeEffects {
	t.Helper()
	running, ok := w.foxhole.Running()
	return foxholeEffects{Store: readNoteState(t, w.st), RoleWrites: w.discord.roleChanges(),
		Running: running, IsRunning: ok, APIRequests: api.requestCount()}
}

// foxholePostCase readies a world for one Foxhole POST route's request, the
// way a Foxhole manager reaches it, and returns the form they post and the
// check that the request, posted by them and answered with res, did what
// the route does.
type foxholePostCase func(t *testing.T, w *testWorld, api *rosterAPI) (form url.Values, tookEffect func(t *testing.T, res *http.Response))

// foxholePostCases is a case for each Foxhole POST route, by path.
var foxholePostCases = map[string]foxholePostCase{
	foxholeNotesPath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		return noteSaveForm(memberDoe.ID, "", "discharged 12 Sep"), func(t *testing.T, _ *http.Response) {
			t.Helper()
			if got := recordOf(t, w, memberDoe.ID); got.Note != "discharged 12 Sep" {
				t.Errorf("%s's record after the manager's save = %+v, want the note saved", memberDoe.ID, got)
			}
		}
	},
	foxholeApprovalsPath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		form := formPosts(t, parseHTML(t, w.b.get(foxholePath)), foxholeApprovalsPath)
		form.Set(fieldMember, memberKestrel.ID)
		form.Set(fieldOp, opApprove)
		return form, func(t *testing.T, _ *http.Response) {
			t.Helper()
			if got := recordOf(t, w, memberKestrel.ID); !got.Approved {
				t.Errorf("%s's record after the manager's Approve = %+v, want them approved", memberKestrel.ID, got)
			}
		}
	},
	foxholeAddPreviewPath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		form := formPosts(t, parseHTML(t, w.b.get(foxholePath)), foxholeAddPreviewPath)
		form.Set(fieldRole, "external")
		form.Set(fieldLines, memberDoe.Username)
		return form, func(t *testing.T, res *http.Response) {
			t.Helper()
			addPreviewBlock(t, parseHTML(t, res))
		}
	},
	foxholeAddPath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		page := parseHTML(t, w.b.get(foxholePath))
		doc := parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", memberDoe.Username))
		return formPosts(t, doc, foxholeAddPath), startedAction(t, w, store.ChangeAdd)
	},
	foxholeRosterPreviewPath: func(t *testing.T, w *testWorld, api *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		form := formPosts(t, parseHTML(t, w.b.get(foxholePath)), foxholeRosterPreviewPath)
		form.Set(fieldUnit, "D/ACD")
		calls := api.requestCount()
		return form, func(t *testing.T, res *http.Response) {
			t.Helper()
			rosterPreviewBlock(t, parseHTML(t, res))
			if api.requestCount() == calls {
				t.Error("the manager's roster preview made no request of the 7Cav API")
			}
		}
	},
	foxholeRosterPath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		doc := parseHTML(t, previewRoster(t, w, "D/ACD"))
		return formPosts(t, doc, foxholeRosterPath), startedAction(t, w, store.ChangeRosterAdd)
	},
	foxholeRemovePath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		doc := parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID))
		return formPosts(t, doc, foxholeRemovePath), startedAction(t, w, store.ChangeRemoval)
	},
	foxholePurgePath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		doc := parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "both"))
		return formPosts(t, doc, foxholePurgePath), thenNoRoleChangeNamesTheOutsider(t, w, startedAction(t, w, store.ChangePurge))
	},
	foxholeReAddPath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		seedApproved(t, w, namesOf(memberDoe))
		doc := parseHTML(t, w.b.get(foxholePath))
		return formPosts(t, doc, foxholeReAddPath), thenNoRoleChangeNamesTheOutsider(t, w, startedAction(t, w, store.ChangeReAdd))
	},
	foxholeRetryPath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
		startPurge(t, w, "external")
		w.awaitActionEnd(t)
		w.discord.failRoleWrites(memberKestrel.ID, nil)
		return formPosts(t, openRetry(t, w.b), foxholeRetryPath), thenNoRoleChangeNamesTheOutsider(t, w, startedAction(t, w, store.ChangePurge))
	},
	// A purge runs, held inside its first role change, until the manager's
	// Stop.
	foxholeStopPath: func(t *testing.T, w *testWorld, _ *rosterAPI) (url.Values, func(*testing.T, *http.Response)) {
		hold := holdRoleWrites(t, w)
		startPurge(t, w, "both")
		hold.next(t)
		doc := parseHTML(t, w.b.get(foxholePath))
		return formPosts(t, doc, foxholeStopPath), func(t *testing.T, res *http.Response) {
			t.Helper()
			assertRedirect(t, res, foxholePath)
			if running, _ := w.foxhole.Running(); running.StopPressedBy != managerUsername {
				t.Errorf("the running action's Stop was pressed by %q, want %s", running.StopPressedBy, managerUsername)
			}
			hold.open()
			w.awaitActionEnd(t)
		}
	},
}

// recordOf is the store's Foxhole record of the member given, the zero
// record for a member with none.
func recordOf(t *testing.T, w *testWorld, memberID string) store.FoxholeRecord {
	t.Helper()
	for _, rec := range readNoteState(t, w.st).Records {
		if rec.MemberID == memberID {
			return rec
		}
	}
	return store.FoxholeRecord{}
}

// startedAction is the check that a request started the Foxhole action
// given, as the manager: it answers with the Foxhole page, and the change
// log holds a report of that action by the manager that it didn't hold
// when the check was made. The check waits for the action to end.
func startedAction(t *testing.T, w *testWorld, action store.ChangeAction) func(*testing.T, *http.Response) {
	t.Helper()
	before := readNoteState(t, w.st).Entries
	return func(t *testing.T, res *http.Response) {
		t.Helper()
		assertRedirect(t, res, foxholePath)
		w.awaitActionEnd(t)
		for _, e := range readNoteState(t, w.st).Entries {
			isNew := !slices.ContainsFunc(before, func(b store.ChangeLogEntry) bool { return b.ID == e.ID })
			if isNew && e.Action == action && e.ForumUsername == managerUsername {
				return
			}
		}
		t.Errorf("the change log holds no new %s report started by %s", action, managerUsername)
	}
}

// thenNoRoleChangeNamesTheOutsider runs the check started, then checks
// that the manager's action changed a role and that no role change names
// the outsider. By then the manager's action has ended. Actions run one at
// a time, so an action the outsider's request had started would have made
// its role changes first, or kept the manager's from starting.
func thenNoRoleChangeNamesTheOutsider(t *testing.T, w *testWorld, started func(*testing.T, *http.Response)) func(*testing.T, *http.Response) {
	t.Helper()
	before := len(w.discord.roleChanges())
	return func(t *testing.T, res *http.Response) {
		t.Helper()
		started(t, res)
		writes := w.discord.roleChanges()
		if len(writes) == before {
			t.Error("the manager's action changed no role, so no role change could name the outsider either")
		}
		for _, write := range writes {
			if strings.Contains(write.Reason, outsiderUsername) {
				t.Errorf("the change to %s names the outsider: %q", write.MemberID, write.Reason)
			}
		}
	}
}

// An outsider changes nothing through any Foxhole POST route: each answers
// with the no-access page, and no store write, role change, action or 7Cav
// API request follows. A Foxhole manager then posts the same form and it
// does what the route does, so no case passes on a form the route would
// refuse anyway.
func TestFoxholePostByAnOutsiderDoesNothing(t *testing.T) {
	for _, path := range slices.Sorted(maps.Keys(foxholePostCases)) {
		t.Run(path, func(t *testing.T) {
			w := newFoxholeWorld(t)
			api := serveRoster(t, trooperVance)
			form, tookEffect := foxholePostCases[path](t, w, api)
			outsider := newBrowser(t, w.p)
			signInAs(t, w.forum, outsider, addOutsider(w.forum))
			before := readFoxholeEffects(t, w, api)

			res := outsider.postForm(path, form)

			assertNoAccessPage(t, parseHTML(t, res))
			if after := readFoxholeEffects(t, w, api); !reflect.DeepEqual(after, before) {
				t.Errorf("after the outsider's post:\n%+v\nwant it as before:\n%+v", after, before)
			}
			tookEffect(t, w.b.postForm(path, form))
		})
	}
}

// openRoutes is every route the panel serves to anyone, signed in or not,
// by its pattern as routes() writes it.
var openRoutes = []string{"GET /static/", "GET /signin", "POST /auth/start", "GET /auth/callback", "POST /auth/signout"}

// Every route the panel serves is in openRoutes or has an outsider case, a
// case whose request the route serves. A route added later fails here until
// someone writes its case or opens it on purpose, whatever gate it sits
// behind.
func TestEveryRouteHasAnOutsiderCaseOrIsOpen(t *testing.T) {
	w := newTestWorld(t, testHub())
	mux := http.NewServeMux()
	for _, rt := range w.p.routes() {
		mux.Handle(rt.pattern, rt.handler)
	}
	reached := map[string]bool{}
	reach := func(method, path string) {
		_, pattern := mux.Handler(httptest.NewRequest(method, path, nil))
		reached[pattern] = true
	}
	for path := range foxholePostCases {
		reach(http.MethodPost, path)
	}
	for _, tc := range settingsSaveCases {
		reach(http.MethodPost, tc.path(t, w.st))
	}
	for _, tc := range pageLoadCases {
		reach(http.MethodGet, tc.path)
	}
	for _, rt := range w.p.routes() {
		if !reached[rt.pattern] && !slices.Contains(openRoutes, rt.pattern) {
			t.Errorf("%s has no outsider case and isn't in openRoutes", rt.pattern)
		}
	}
}

// A panel admin whose forum account leaves every panel admin group keeps
// their panel session, and their next page is the no-access page.
func TestPanelAdminWhoLeavesTheAdminGroupsDropsToTheNoAccessPage(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	w.forum.setUserinfo(http.StatusOK, userinfoJSON(2, []int{35, 72}))

	res := w.b.get("/")

	assertNoAccessPage(t, parseHTML(t, res))
}

// A form on another site can post to the panel from a signed-in manager's
// browser, which sends the manager's cookie with it. A remove preview's
// Confirm posted that way is refused and starts no removal. The same
// Confirm posted from the panel's own page then starts it.
func TestConfirmPostedFromAnotherSiteIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	preview := removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))
	form := findElement(preview, "form", "", "")
	if form == nil {
		t.Fatal("the remove preview has no form")
	}
	action, _ := attrValue(form, "action")

	res := w.b.do(http.MethodPost, action, http.Header{
		"Content-Type":   {"application/x-www-form-urlencoded"},
		"Origin":         {"https://elsewhere.example"},
		"Sec-Fetch-Site": {"cross-site"},
	}, strings.NewReader(formValues(form).Encode()))

	if res.StatusCode < 400 || res.StatusCode > 499 {
		t.Errorf("the Confirm posted from another site answered %d, want a refusal", res.StatusCode)
	}
	if entries := changeEntries(t, parseHTML(t, w.b.get(foxholePath)), "removal"); len(entries) != 0 {
		t.Errorf("the Confirm posted from another site started %d removals, want none", len(entries))
	}
	assertRedirect(t, confirmRemoval(t, w.b, preview), foxholePath)
	w.awaitActionEnd(t)
	if slices.Contains(w.discord.holdersOf(roleExternal), memberKestrel.ID) {
		t.Errorf("the same Confirm posted from the panel's page left %s holding External", memberKestrel.ID)
	}
}
