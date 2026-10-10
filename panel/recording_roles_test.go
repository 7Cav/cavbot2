package panel

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"testing"

	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// The recording roles (#383): the section beside the guild-wide moderator
// roles saves the roles whose holders may record, change-logs each save
// under no hub, and refuses a stale form, the way the guild-wide section
// does.

// recordingRolesForm is the recording roles section's form as a page loaded
// now posts it: the roles, and the version the store holds for the set now.
func recordingRolesForm(t *testing.T, st store.Store, roleIDs ...string) url.Values {
	t.Helper()
	return url.Values{"recording_roles": roleIDs, "version": {strconv.FormatInt(storedRecordingRoles(t, st).Version, 10)}}
}

// storedRecordingRoles reads the recording roles back through the store,
// the read the recording runtime makes at each start.
func storedRecordingRoles(t *testing.T, st store.Store) store.RecordingRoles {
	t.Helper()
	roles, err := st.GetRecordingRoles(context.Background(), testGuildID)
	if err != nil {
		t.Fatalf("GetRecordingRoles: %v", err)
	}
	return roles
}

func TestRecordingRolesSaveWritesTheSetAndOneEntry(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-mp", "role-hq"))

	assertRedirect(t, res, "/")
	if got := storedRecordingRoles(t, w.st).RoleIDs; !sameSet(got, []string{"role-hq", "role-mp"}) {
		t.Errorf("stored recording roles = %v, want role-hq and role-mp", got)
	}
	entries := storedChangeLog(t, w.st, 0)
	if len(entries) != 1 {
		t.Fatalf("%d entries under no hub, want 1", len(entries))
	}
	assertActor(t, entries[0], store.ChangeRecordingRoles)
	c, ok := decodeDiff(t, entries[0])["recording_roles"]
	if !ok {
		t.Fatalf("diff %s lacks recording_roles", entries[0].Diff)
	}
	if before := roleSet(t, c.Before); len(before) != 0 {
		t.Errorf("diff before = %v, want no roles", before)
	}
	if after := roleSet(t, c.After); !sameSet(after, []string{"role-hq", "role-mp"}) {
		t.Errorf("diff after = %v, want role-hq and role-mp", after)
	}

	assertRedirect(t, w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-hq")), "/")

	entries = storedChangeLog(t, w.st, 0)
	if len(entries) != 2 {
		t.Fatalf("%d entries under no hub after the second save, want 2", len(entries))
	}
	c = decodeDiff(t, entries[0])["recording_roles"]
	if before := roleSet(t, c.Before); !sameSet(before, []string{"role-hq", "role-mp"}) {
		t.Errorf("second entry before = %v, want role-hq and role-mp, the first save's", before)
	}
	if after := roleSet(t, c.After); !sameSet(after, []string{"role-hq"}) {
		t.Errorf("second entry after = %v, want role-hq alone", after)
	}
}

// recordingRolesSection returns the recording roles section of a parsed
// page, the element under data-section=recording-roles.
func recordingRolesSection(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	sec := findElement(doc, "section", "data-section", "recording-roles")
	if sec == nil {
		t.Fatal("page has no section under data-section=recording-roles")
	}
	return sec
}

// A saves role-mp. B saves role-hq from a section loaded before A's save and
// is refused: the store and the change log hold A's save alone. B then saves
// the section the refusal rendered, which goes through with role-mp as the
// entry's before.
func TestStaleRecordingRolesSectionIsRefusedThenItsRenderedSectionSaves(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	b := secondBrowser(t, w)
	formA := loadedForm(t, w.b, "/", "/recording-roles")
	formB := loadedForm(t, b, "/", "/recording-roles")
	formA["recording_roles"] = []string{"role-mp"}
	assertRedirect(t, w.b.postForm("/recording-roles", formA), "/")
	formB["recording_roles"] = []string{"role-hq"}

	res := b.postForm("/recording-roles", formB)

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if findElement(recordingRolesSection(t, doc), "", "data-error", "stale") == nil {
		t.Error("the recording roles section carries no data-error=stale note")
	}
	if got := storedRecordingRoles(t, w.st).RoleIDs; !sameSet(got, []string{"role-mp"}) {
		t.Errorf("stored recording roles = %v, want A's save alone: role-mp", got)
	}
	if entries := storedChangeLog(t, w.st, 0); len(entries) != 1 {
		t.Fatalf("%d entries under no hub, want A's one", len(entries))
	}

	assertRedirect(t, b.postForm("/recording-roles", formPosts(t, doc, "/recording-roles")), "/")

	entries := storedChangeLog(t, w.st, 0)
	if len(entries) != 2 {
		t.Fatalf("%d entries under no hub after B's second save, want 2", len(entries))
	}
	c := decodeDiff(t, entries[0])["recording_roles"]
	if before := roleSet(t, c.Before); !sameSet(before, []string{"role-mp"}) {
		t.Errorf("B's entry before = %v, want role-mp (A's)", before)
	}
	if after := roleSet(t, c.After); !sameSet(after, []string{"role-hq"}) {
		t.Errorf("B's entry after = %v, want role-hq", after)
	}
}

// The store checks the version again at the write, a backstop against
// another process saving between this save's read and its write: the save
// is refused as stale, and the store holds the other save alone.
func TestRecordingRolesSaveWhoseSetChangesBeforeItsWriteIsStale(t *testing.T) {
	w, st := newOtherWriterWorld(t)
	other := store.ChangeLogEntry{ForumUserID: 4321, ForumUsername: "Other.P", Action: store.ChangeRecordingRoles, Diff: []byte(`{}`)}
	st.before("SaveRecordingRoles", func() {
		roles := store.RecordingRoles{RoleIDs: []string{"role-mp"}, Version: storedRecordingRoles(t, st.Fake).Version}
		if err := st.Fake.SaveRecordingRoles(context.Background(), testGuildID, roles, other); err != nil {
			t.Errorf("the other save: %v", err)
		}
	})

	res := w.b.postForm("/recording-roles", recordingRolesForm(t, st.Fake, "role-hq"))

	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
	if got := storedRecordingRoles(t, st.Fake).RoleIDs; !sameSet(got, []string{"role-mp"}) {
		t.Errorf("stored recording roles = %v, want the other save's role-mp", got)
	}
	if entries := storedChangeLog(t, st.Fake, 0); len(entries) != 1 || entries[0].ForumUserID != other.ForumUserID {
		t.Errorf("entries under no hub = %+v, want the other save's alone", entries)
	}
}

// Only an eligible role can be added: a managed role is refused on the
// section, and the set and its entries stay as they were.
func TestRecordingRolesSaveRefusesAManagedRoleAndWritesNothing(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-mp")), "/")
	before := storedRecordingRoles(t, w.st)

	res := w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-mp", "role-bot"))

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	if findElement(recordingRolesSection(t, parseHTML(t, res)), "", "data-error", "recording_roles") == nil {
		t.Error("the recording roles section carries no data-error=recording_roles")
	}
	if after := storedRecordingRoles(t, w.st); !sameSet(after.RoleIDs, before.RoleIDs) || after.Version != before.Version {
		t.Errorf("stored recording roles = %+v, want them as before, %+v", after, before)
	}
	if entries := storedChangeLog(t, w.st, 0); len(entries) != 1 {
		t.Errorf("%d entries under no hub, want the first save's alone", len(entries))
	}
}

// seedRecordingRoles stores the recording roles through the store's own
// save, as a save made while each role was eligible would have left them.
func seedRecordingRoles(t *testing.T, st *store.Fake, roleIDs ...string) {
	t.Helper()
	entry := store.ChangeLogEntry{ForumUserID: testUserID, ForumUsername: testUsername, Action: store.ChangeRecordingRoles, Diff: []byte(`{}`)}
	roles := store.RecordingRoles{RoleIDs: roleIDs, Version: storedRecordingRoles(t, st).Version}
	if err := st.SaveRecordingRoles(context.Background(), testGuildID, roles, entry); err != nil {
		t.Fatalf("SaveRecordingRoles: %v", err)
	}
}

// A stored recording role that is no longer eligible, its Discord role since
// deleted or made managed, stays listed in the section, marked unavailable
// with its reason (ADR 0012).
func TestStoredRecordingRoleNoLongerEligibleRendersAsUnavailable(t *testing.T) {
	cases := []struct {
		roleID string
		reason string
	}{
		{"role-gone", "deleted"},
		{"role-bot", "managed"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			seedRecordingRoles(t, w.st, "role-mp", tc.roleID)
			signIn(t, w.forum, w.b)

			res := w.b.get("/")

			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET / status = %d, want 200", res.StatusCode)
			}
			picker := pickerRoot(t, recordingRolesSection(t, parseHTML(t, res)), "recording_roles")
			var reason string
			found := false
			for _, in := range postedInputs(picker, "recording_roles") {
				if id, _ := attrValue(in, "value"); id == tc.roleID {
					reason, _ = attrValue(in, "data-unavailable")
					found = true
				}
			}
			if !found {
				t.Fatalf("the recording roles picker posts no %s; it posts %v", tc.roleID, postedControls(picker, "recording_roles"))
			}
			if reason != tc.reason {
				t.Errorf("%s's control carries data-unavailable=%q, want %s", tc.roleID, reason, tc.reason)
			}
		})
	}
}

// A save keeps a stored recording role that is no longer eligible when it
// posts it: only a person removes it (ADR 0012). This save also adds
// role-hq, so it changes the set.
func TestRecordingRolesSaveKeepsAStoredRoleSinceMadeManaged(t *testing.T) {
	w := newTestWorld(t, testHub())
	seedRecordingRoles(t, w.st, "role-mp", "role-bot")
	signIn(t, w.forum, w.b)

	res := w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-mp", "role-bot", "role-hq"))

	assertRedirect(t, res, "/")
	if got := storedRecordingRoles(t, w.st).RoleIDs; !sameSet(got, []string{"role-bot", "role-hq", "role-mp"}) {
		t.Errorf("stored recording roles = %v, want role-bot kept beside role-hq and role-mp", got)
	}
}

// The section shows its own saves' entries, newest first, with the roles by
// name, and none of the guild-wide moderator section's.
func TestRecordingRolesSectionShowsItsOwnSavesNewestFirst(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-mp")), "/")
	assertRedirect(t, w.b.postForm("/moderators", moderatorsForm(t, w.st, "role-hq")), "/")
	assertRedirect(t, w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-hq")), "/")
	// Under no hub, newest first: the second recording roles save, the
	// moderators save, the first recording roles save.
	saves := storedChangeLog(t, w.st, 0)
	if len(saves) != 3 {
		t.Fatalf("%d entries under no hub, want 3", len(saves))
	}
	want := []int64{saves[0].ID, saves[2].ID}

	res := w.b.get("/")

	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", res.StatusCode)
	}
	sec := recordingRolesSection(t, parseHTML(t, res))
	if got := entryIDs(t, sec); !slices.Equal(got, want) {
		t.Fatalf("the section shows entries %v, want %v: the recording roles saves alone, newest first", got, want)
	}
	newest := findElement(sec, "", "data-entry", strconv.FormatInt(want[0], 10))
	change := findElement(newest, "", "data-change", "recording_roles")
	if change == nil {
		t.Fatal("the newest entry has no data-change=recording_roles element")
	}
	if got := fieldText(t, change, "before"); got != "Military Police" {
		t.Errorf("newest entry before = %q, want Military Police", got)
	}
	if got := fieldText(t, change, "after"); got != "Regimental HQ" {
		t.Errorf("newest entry after = %q, want Regimental HQ", got)
	}
}

// A save that would change nothing says so and stops (CODING_STANDARDS.md):
// with role-mp and role-hq stored, posting them in the other order is
// refused on the section, and the set, its version and its entries stay as
// they were.
func TestRecordingRolesSaveThatChangesNothingIsRefused(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	assertRedirect(t, w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-mp", "role-hq")), "/")
	before := storedRecordingRoles(t, w.st)

	res := w.b.postForm("/recording-roles", recordingRolesForm(t, w.st, "role-hq", "role-mp"))

	if !isClientError(res.StatusCode) {
		t.Errorf("status = %d, want 4xx", res.StatusCode)
	}
	if findElement(recordingRolesSection(t, parseHTML(t, res)), "", "data-error", "unchanged") == nil {
		t.Error("the recording roles section carries no data-error=unchanged note")
	}
	if after := storedRecordingRoles(t, w.st); !sameSet(after.RoleIDs, before.RoleIDs) || after.Version != before.Version {
		t.Errorf("stored recording roles = %+v, want them as before, %+v", after, before)
	}
	if entries := storedChangeLog(t, w.st, 0); len(entries) != 1 {
		t.Errorf("%d entries under no hub, want the first save's alone", len(entries))
	}
}
