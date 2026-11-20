package panel

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/store"
)

// forumGroupsForm is the forum groups section's form as a page loaded now
// posts it, with the two fields as typed.
func forumGroupsForm(t *testing.T, st store.Store, foxhole, joiners string) url.Values {
	t.Helper()
	return url.Values{fieldFoxholeGroup: {foxhole}, fieldJoinersGroups: {joiners},
		fieldVersion: {strconv.FormatInt(storedForumGroups(t, st).Version, 10)}}
}

// storedForumGroups reads the forum groups back through the store.
func storedForumGroups(t *testing.T, st store.Store) store.ForumGroups {
	t.Helper()
	groups, err := st.GetForumGroups(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("GetForumGroups: %v", err)
	}
	return groups
}

// seedForumGroups saves the Foxhole group testFoxholeGroupID and the Joiners
// group testJoinersGroupID through the store's own save.
func seedForumGroups(t *testing.T, st store.Store) {
	t.Helper()
	entry := store.ChangeLogEntry{ForumUserID: testUserID, ForumUsername: testUsername, Action: store.ChangeForumGroups, Diff: []byte(`{}`)}
	groups := store.ForumGroups{FoxholeGroupID: testFoxholeGroupID, JoinersGroupIDs: []int{testJoinersGroupID},
		Version: storedForumGroups(t, st).Version}
	if err := st.SaveForumGroups(context.Background(), testGuildID, groups, entry); err != nil {
		t.Fatalf("SaveForumGroups: %v", err)
	}
}

// A forum groups save decides who opens the Foxhole page and the Joiners
// page from the next request on. A Foxhole manager signed in under the old
// Foxhole group loses the Foxhole page with no new sign-in, a user in the
// new one lands on it, and a user in a new Joiners group lands on the
// Joiners page.
func TestForumGroupsSaveCountsFromTheNextRequest(t *testing.T) {
	w := newTestWorld(t)
	signIn(t, w.forum, w.b)
	former := newBrowser(t, w.p)
	signInAs(t, w.forum, former, addFoxholeManager(w.forum))

	assertRedirect(t, w.b.postForm("/forum-groups", forumGroupsForm(t, w.st, "400", "410 411")), settingsPath)

	assertRecordingsPage(t, follow(t, former, former.get(foxholePath)))
	landings := []struct {
		name   string
		groups []int
		want   string
	}{
		{"a user in the new Foxhole group", []int{35, 400}, pageFoxhole},
		{"a user in a new Joiners group", []int{35, 411}, pageJoiners},
	}
	for i, l := range landings {
		b := newBrowser(t, w.p)
		res := follow(t, b, signInAs(t, w.forum, b, w.forum.addUser(7001+i, "User.U", 2, l.groups)))
		if got := pageOf(t, parseHTML(t, res)); res.StatusCode != http.StatusOK || got != l.want {
			t.Errorf("%s lands on page %q with status %d, want %s with 200", l.name, got, res.StatusCode, l.want)
		}
	}
}

// A forum groups save the section can't take is refused on the section with
// the field it refuses, or as unchanged when it posts the groups as stored,
// and stores nothing: the groups stay at version 0 with no entry. The form
// comes back as typed.
func TestForumGroupsSaveRefusalsWriteNothing(t *testing.T) {
	cases := []struct {
		name, foxhole, joiners, dataError string
	}{
		{"a Foxhole group that is not a number", "foxhole", "", fieldFoxholeGroup},
		{"a Foxhole group of 0", "0", "", fieldFoxholeGroup},
		{"a Joiners group that is not a number", "323", "410, joiners", fieldJoinersGroups},
		{"a Joiners group below 0", "323", "-410", fieldJoinersGroups},
		{"the groups as stored", " 323 ", " ", refusalUnchanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t)
			signIn(t, w.forum, w.b)

			res := w.b.postForm("/forum-groups", forumGroupsForm(t, w.st, tc.foxhole, tc.joiners))

			if !isClientError(res.StatusCode) {
				t.Errorf("status = %d, want 4xx", res.StatusCode)
			}
			sec := findElement(parseHTML(t, res), "section", "data-section", formForumGroups)
			if sec == nil {
				t.Fatalf("page has no section under data-section=%s", formForumGroups)
			}
			if findElement(sec, "", "data-error", tc.dataError) == nil {
				t.Errorf("the forum groups section carries no data-error=%s", tc.dataError)
			}
			if input := findElement(sec, "input", "name", fieldFoxholeGroup); input == nil || attrOf(input, "value") != tc.foxhole {
				t.Errorf("the Foxhole group field does not hold %q as typed", tc.foxhole)
			}
			if got := storedForumGroups(t, w.st); got.Version != 0 || got.FoxholeGroupID != testFoxholeGroupID {
				t.Errorf("stored forum groups = %+v, want the Foxhole group %d at version 0", got, testFoxholeGroupID)
			}
			if entries := storedChangeLog(t, w.st, 0); len(entries) != 0 {
				t.Errorf("%d entries under no hub, want none", len(entries))
			}
		})
	}
}

// A failed read of the forum groups is reported, and the request it fails
// opens neither the Foxhole page nor the Joiners page for a user in both
// groups. A panel admin still opens the hub page.
func TestForumGroupsReadFailureOpensNoGroupPageButLeavesPanelAdmins(t *testing.T) {
	w, st := newCtxWorld(t, testHub())
	seedForumGroups(t, st)
	signIn(t, w.forum, w.b)
	member := newBrowser(t, w.p)
	signInAs(t, w.forum, member, w.forum.addUser(7101, "Both.B", 2, []int{testFoxholeGroupID, testJoinersGroupID}))
	reported := recordSentry(t)
	st.failRead("GetForumGroups")

	assertRecordingsPage(t, follow(t, member, member.get(foxholePath)))
	assertNoAccessSays(t, parseHTML(t, member.get(joinersPath)), noAccessRefused)
	res := w.b.get("/")

	if got := pageOf(t, parseHTML(t, res)); res.StatusCode != http.StatusOK || got != pageHubs {
		t.Errorf("the panel admin's hub page is page %q with status %d, want %s with 200", got, res.StatusCode, pageHubs)
	}
	errs := reported.recorded()
	if len(errs) == 0 {
		t.Fatal("the failed reads sent no Sentry event")
	}
	for _, err := range errs {
		if !errors.Is(err, errStoreDown) {
			t.Errorf("Sentry got %v, want the failed read", err)
		}
	}
}
