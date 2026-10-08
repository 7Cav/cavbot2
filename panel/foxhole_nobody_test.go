package panel

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// A Foxhole action's form kept from an earlier load still posts after
// something else changed its members, as when a second manager runs the
// same action to its end from another tab. An action that would then
// change nobody is refused: it makes no role change, adds no change log
// entry, and leaves the last report on top. It is an expected outcome, so
// nothing reaches Sentry.
func TestKeptFormOfAnActionThatWouldChangeNobodyIsRefused(t *testing.T) {
	cases := []struct {
		name string
		// action is the change log's data-action marker of the action posted,
		// and refusal the data-error marker its refusal carries.
		action, refusal string
		// stale loads the action's form and keeps it, changes its members
		// another way, and returns the post of the kept form.
		stale func(t *testing.T, w *testWorld) func() *http.Response
		// paste is an add's pasted lines, whose refusal lands on its preview
		// with them kept. The others land on the Foxhole page.
		paste string
	}{
		{name: "purge of Internal", action: "purge", refusal: "nobody-to-change", stale: stalePurge("internal")},
		{name: "purge of External", action: "purge", refusal: "nobody-to-change", stale: stalePurge("external")},
		{name: "purge of both", action: "purge", refusal: "nobody-to-change", stale: stalePurge("both")},
		{name: "re-add", action: "re_add", refusal: "nobody-to-change", stale: func(t *testing.T, w *testWorld) func() *http.Response {
			// The collaborator who left counts for nobody: the re-add would skip them.
			seedApproved(t, w, namesOf(memberDoe), collaboratorGone)
			page := parseHTML(t, w.b.get(foxholePath))
			pressReAdd(t, w.b)
			w.awaitActionEnd(t)
			return func() *http.Response { return postReAdd(t, w.b, page) }
		}},
		{name: "removal", action: "removal", refusal: "nobody-to-change", stale: func(t *testing.T, w *testWorld) func() *http.Response {
			preview := func() *html.Node {
				return removePreviewBlock(t, parseHTML(t, submitSelection(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external", memberKestrel.ID)))
			}
			kept := preview()
			assertRedirect(t, confirmRemoval(t, w.b, preview()), foxholePath)
			w.awaitActionEnd(t)
			return func() *http.Response { return confirmRemoval(t, w.b, kept) }
		}},
		{name: "add", action: "add", refusal: "nobody-to-change", paste: "rvance\nnobody_here", stale: func(t *testing.T, w *testWorld) func() *http.Response {
			// The line naming nobody counts for nobody: the add lists it and
			// changes no one for it.
			preview := func() *html.Node {
				page := parseHTML(t, w.b.get(foxholePath))
				return parseHTML(t, previewPaste(t, w.b, page, addForm(t, page), "external", "rvance\nnobody_here"))
			}
			kept := preview()
			assertRedirect(t, confirmAdd(t, w.b, preview(), nil), foxholePath)
			w.awaitActionEnd(t)
			return func() *http.Response { return confirmAdd(t, w.b, kept, nil) }
		}},
		{name: "roster add", action: "roster_add", refusal: "nobody-to-change", stale: func(t *testing.T, w *testWorld) func() *http.Response {
			// Once Vance holds Internal, the roster holds only a trooper with no
			// Discord account and members holding Internal already.
			serveRoster(t, trooperVance, trooperDoe, trooperNolink)
			kept := parseHTML(t, previewRoster(t, w, "D/ACD"))
			assertRedirect(t, confirmRoster(t, w.b, parseHTML(t, previewRoster(t, w, "D/ACD"))), foxholePath)
			w.awaitActionEnd(t)
			return func() *http.Response { return confirmRoster(t, w.b, kept) }
		}},
		{name: "purge's Retry", action: "purge", refusal: "nothing-to-retry", stale: func(t *testing.T, w *testWorld) func() *http.Response {
			w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
			startPurge(t, w, "external")
			w.awaitActionEnd(t)
			kept := openRetry(t, w.b)
			retryConfirmation(t, kept, "purge")
			w.discord.editMember(memberKestrel.ID, func(m *commands.ListedMember) {
				m.RoleIDs = slices.DeleteFunc(m.RoleIDs, func(id string) bool { return id == roleExternal })
			})
			return func() *http.Response { return w.b.postForm(foxholeRetryPath, formPosts(t, kept, foxholeRetryPath)) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			post := tc.stale(t, w)
			before := parseHTML(t, w.b.get(foxholePath))
			top := attrOf(reportBlock(t, before), "data-report")
			entries := len(changeEntries(t, before, tc.action))
			writes := len(w.discord.roleChanges())
			reported := recordSentry(t)

			doc := parseHTML(t, post())

			if findLive(doc, "", "data-error", tc.refusal) == nil {
				t.Errorf("the page doesn't give the %s refusal", tc.refusal)
			}
			if tc.paste != "" {
				form := findElement(addPreviewBlock(t, doc), "form", "data-field", "add-form")
				if form == nil {
					t.Fatal("the add preview has no paste box")
				}
				if got := formFields(doc, form).Get(fieldLines); got != tc.paste {
					t.Errorf("the paste box holds %q, want the pasted lines %q", got, tc.paste)
				}
			} else {
				for _, pending := range []string{"purge-confirm", "remove-preview", "add-preview", "roster-preview", "retry-confirm"} {
					if findElement(doc, "", "data-field", pending) != nil {
						t.Errorf("the refusal opens the %s, want the Foxhole page with nothing open", pending)
					}
				}
			}
			if got := w.discord.roleChanges()[writes:]; len(got) != 0 {
				t.Errorf("the refused action made the role changes %v, want none", got)
			}
			after := parseHTML(t, w.b.get(foxholePath))
			if n := len(changeEntries(t, after, tc.action)); n != entries {
				t.Errorf("the change log holds %d %s entries, want %d as before", n, tc.action, entries)
			}
			if got := attrOf(reportBlock(t, after), "data-report"); got != top {
				t.Errorf("the page shows report %s on top, want the last one, %s", got, top)
			}
			if n := len(reported.sent()); n != 0 {
				t.Errorf("Sentry got %d events, want none", n)
			}
		})
	}
}

// stalePurge keeps the purge confirmation of the scope given, then purges
// the scope to its end from a second load, as another manager would.
func stalePurge(scope string) func(t *testing.T, w *testWorld) func() *http.Response {
	return func(t *testing.T, w *testWorld) func() *http.Response {
		confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), scope)))
		startPurge(t, w, scope)
		w.awaitActionEnd(t)
		return func() *http.Response { return confirmPurge(t, w.b, context.Background(), confirm) }
	}
}

// A refused action gives the one-action-at-a-time rule back at once, so
// another action starts straight after it.
func TestActionStartsRightAfterARefusalOfOneThatWouldChangeNobody(t *testing.T) {
	w := newFoxholeWorld(t)
	seedApproved(t, w, namesOf(memberKestrel))

	refused := parseHTML(t, postReAdd(t, w.b, parseHTML(t, w.b.get(foxholePath))))

	if findLive(refused, "", "data-error", "nobody-to-change") == nil {
		t.Fatal("the re-add of a collaborator holding External wasn't refused")
	}
	startPurge(t, w, "internal")
}

// A purge of both roles kept from before External was purged elsewhere
// still has Internal holders to take, so it runs and takes Internal off
// each of them.
func TestKeptPurgeOfBothWithInternalStillHeldTakesInternal(t *testing.T) {
	w := newFoxholeWorld(t)
	confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "both")))
	startPurge(t, w, "external")
	w.awaitActionEnd(t)

	confirmPurge(t, w.b, context.Background(), confirm)
	w.awaitActionEnd(t)

	if got := w.discord.holdersOf(roleInternal); len(got) != 0 {
		t.Errorf("after the purge %v hold Internal, want nobody", got)
	}
}

// A kept purge that would change nobody still gets the refusals that come
// before that check, each its own: another action running, a partial
// member list, and a Foxhole role missing from the server, which is a
// configuration fault and reaches Sentry.
func TestKeptPurgeThatWouldChangeNobodyGetsEachEarlierRefusalFirst(t *testing.T) {
	cases := []struct {
		name, refusal string
		before        func(t *testing.T, w *testWorld)
		sentry        int
	}{
		{"another action running", "action-running", func(t *testing.T, w *testWorld) {
			hold := holdRoleWrites(t, w)
			startPurge(t, w, "external")
			hold.next(t)
		}, 0},
		{"member list partial", "member-list", func(_ *testing.T, w *testWorld) {
			w.discord.setMemberList(arrivingList)
		}, 0},
		{"role missing", "role-missing", func(_ *testing.T, w *testWorld) {
			w.discord.removeRole(roleInternal)
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			post := stalePurge("internal")(t, w)
			tc.before(t, w)
			reported := recordSentry(t)

			doc := parseHTML(t, post())

			if findLive(doc, "", "data-error", tc.refusal) == nil {
				t.Errorf("the page doesn't give the %s refusal", tc.refusal)
			}
			if n := len(reported.sent()); n != tc.sentry {
				t.Errorf("Sentry got %d events, want %d", n, tc.sentry)
			}
		})
	}
}
