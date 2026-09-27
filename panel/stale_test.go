package panel

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// A stale form (#373): a hub's edit form, or the guild-wide moderator
// section, loaded before another save of the same settings took effect. A
// save from it is refused with 409 and a note carrying data-error="stale"
// on the form, and changes nothing. The forms here come from the pages the
// panel renders, so what the hidden inputs carry is under test too.

// formPosts returns what the form posting to action would post if
// submitted as the page left it: every named input that is not disabled, a
// checkbox or radio only when checked and "on" when it has no value, each
// select's selected option or its first, and each textarea's text. The
// contents of a <template> post nothing.
func formPosts(t *testing.T, doc *html.Node, action string) url.Values {
	t.Helper()
	form := findElement(doc, "form", "action", action)
	if form == nil {
		t.Fatalf("page has no form posting to %s", action)
	}
	out := url.Values{}
	eachLiveElement(form, func(n *html.Node) {
		name, _ := attrValue(n, "name")
		if name == "" {
			return
		}
		if _, disabled := attrValue(n, "disabled"); disabled {
			return
		}
		switch n.Data {
		case "input":
			typ, _ := attrValue(n, "type")
			value, hasValue := attrValue(n, "value")
			if typ == "checkbox" || typ == "radio" {
				if _, checked := attrValue(n, "checked"); !checked {
					return
				}
				if !hasValue {
					value = "on"
				}
			}
			out.Add(name, value)
		case "select":
			var first, chosen *html.Node
			eachLiveElement(n, func(o *html.Node) {
				if o.Data != "option" {
					return
				}
				if first == nil {
					first = o
				}
				if _, selected := attrValue(o, "selected"); selected && chosen == nil {
					chosen = o
				}
			})
			if chosen == nil {
				chosen = first
			}
			if chosen != nil {
				value, _ := attrValue(chosen, "value")
				out.Add(name, value)
			}
		case "textarea":
			out.Add(name, textOf(n))
		}
	})
	return out
}

// loadedForm loads the page at target through b and returns what its form
// posting to action would post untouched.
func loadedForm(t *testing.T, b *browser, target, action string) url.Values {
	t.Helper()
	res := b.get(target)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", target, res.StatusCode)
	}
	return formPosts(t, parseHTML(t, res), action)
}

// secondBrowser is another panel user's browser, signed in through the same
// forum. One browser's cookies are not safe to share across goroutines.
func secondBrowser(t *testing.T, w *testWorld) *browser {
	t.Helper()
	b := newBrowser(t, w.p)
	signIn(t, w.forum, b)
	return b
}

// staleLines returns the INFO records carrying the signed-in user and every
// field in want, found by level and fields, never by the sentence.
func staleLines(records []map[string]string, want map[string]string) []map[string]string {
	var out []map[string]string
	for _, r := range records {
		if r["level"] != "INFO" || r["username"] != testUsername || r["forum_user_id"] != strconv.Itoa(testUserID) {
			continue
		}
		matches := true
		for k, v := range want {
			if r[k] != v {
				matches = false
			}
		}
		if matches {
			out = append(out, r)
		}
	}
	return out
}

// (a) A saves the hub. B saves from a form loaded before A's save and is
// refused: the store, the running bot and the change log hold A's save
// alone, and B's changed channel name reaches no Discord call. B then saves
// the form the refusal rendered, which goes through with A's values as the
// entry's befores.
func TestStaleHubFormIsRefusedThenItsRenderedFormSaves(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	b := secondBrowser(t, w)
	id := storedHubID(t, w.st, "hub-1")
	idText := strconv.FormatInt(id, 10)
	page, action := "/?hub="+idText, "/hubs/"+idText
	formA := loadedForm(t, w.b, page, action)
	formB := loadedForm(t, b, page, action)
	formA.Set("user_limit", "5")
	assertRedirect(t, w.b.postForm(action, formA), "/")
	rec := recordSentry(t)
	logs := captureLogs(t)
	formB.Set("base_string", "Bravo Voice")
	formB.Set("channel_name", "Bravo Room")

	res := b.postForm(action, formB)

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if findElement(hubSection(t, doc, id), "", "data-error", "stale") == nil {
		t.Error("the hub's edit section carries no data-error=stale note")
	}
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("the stale save made Discord edits %+v, want none", edits)
	}
	if h := storedHubs(t, w.st)[0]; h.UserLimit != 5 || h.BaseString != "Arma Voice" {
		t.Errorf("stored hub = %+v, want A's save alone: user limit 5, base string Arma Voice", h)
	}
	entries := storedChangeLog(t, w.st, id)
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want A's one", len(entries))
	}
	if n := len(staleLines(logs(), map[string]string{"hub_id": idText})); n != 1 {
		t.Errorf("logged %d INFO lines with the hub and the user, want 1", n)
	}
	if errs := rec.recorded(); len(errs) != 0 {
		t.Errorf("Sentry got %d events, want none", len(errs))
	}
	w.join("user-a", "hub-1")
	if creates := w.discord.creates(); len(creates) != 1 || !strings.Contains(creates[0].Name, "Arma Voice") || creates[0].UserLimit != 5 {
		t.Errorf("a join after the refusal made creates %+v, want one from A's save: Arma Voice, limit 5", creates)
	}

	assertRedirect(t, b.postForm(action, formPosts(t, doc, action)), "/")

	entries = storedChangeLog(t, w.st, id)
	if len(entries) != 2 {
		t.Fatalf("the hub has %d entries after B's second save, want 2", len(entries))
	}
	diff := decodeDiff(t, entries[0])
	if got := diff["user_limit"]; got.Before != float64(5) || got.After != float64(0) {
		t.Errorf("B's entry user_limit = %+v, want before 5 (A's), after 0", got)
	}
	if got := diff["base_string"]; got.Before != "Arma Voice" || got.After != "Bravo Voice" {
		t.Errorf("B's entry base_string = %+v, want before Arma Voice, after Bravo Voice", got)
	}
}

// (c) The same for the guild-wide section. A saves role-mp. B saves role-hq
// from a section loaded before A's save and is refused: the store, the
// running bot and the change log hold A's save alone. B then saves the
// section the refusal rendered, which goes through with role-mp as the
// entry's before.
func TestStaleModeratorsSectionIsRefusedThenItsRenderedSectionSaves(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	b := secondBrowser(t, w)
	formA := loadedForm(t, w.b, "/", "/moderators")
	formB := loadedForm(t, b, "/", "/moderators")
	formA["moderator_roles"] = []string{"role-mp"}
	assertRedirect(t, w.b.postForm("/moderators", formA), "/")
	rec := recordSentry(t)
	logs := captureLogs(t)
	formB["moderator_roles"] = []string{"role-hq"}

	res := b.postForm("/moderators", formB)

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	doc := parseHTML(t, res)
	if findElement(moderatorsSection(t, doc), "", "data-error", "stale") == nil {
		t.Error("the guild-wide section carries no data-error=stale note")
	}
	if got := storedGuildRoles(t, w.st); !sameSet(got, []string{"role-mp"}) {
		t.Errorf("stored guild roles = %v, want A's save alone: role-mp", got)
	}
	if entries := storedChangeLog(t, w.st, 0); len(entries) != 1 {
		t.Fatalf("%d entries under no hub, want A's one", len(entries))
	}
	if n := len(staleLines(logs(), nil)); n != 1 {
		t.Errorf("logged %d INFO lines with the user, want 1", n)
	}
	if errs := rec.recorded(); len(errs) != 0 {
		t.Errorf("Sentry got %d events, want none", len(errs))
	}
	w.joinAs("user-owner", "hub-1", testRankSGT)
	w.joinAs("user-owner", "spawn-1", testRankSGT)
	w.joinAs("user-mod", "spawn-1", "role-hq")
	if _, err := w.runtime.Rename(commands.Invoker{UserID: "user-mod", Roles: []string{"role-hq"}}, "Alpha"); err == nil {
		t.Error("a rename by a holder of role-hq, which the refused save named, passed; want a refusal")
	}

	assertRedirect(t, b.postForm("/moderators", formPosts(t, doc, "/moderators")), "/")

	entries := storedChangeLog(t, w.st, 0)
	if len(entries) != 2 {
		t.Fatalf("%d entries under no hub after B's second save, want 2", len(entries))
	}
	c := decodeDiff(t, entries[0])["moderator_roles"]
	if before := roleSet(t, c.Before); !sameSet(before, []string{"role-mp"}) {
		t.Errorf("B's entry before = %v, want role-mp (A's)", before)
	}
	if after := roleSet(t, c.After); !sameSet(after, []string{"role-hq"}) {
		t.Errorf("B's entry after = %v, want role-hq", after)
	}
}

// A post whose version is missing, or does not parse, is a stale form: 409,
// and nothing written. The guild-wide row runs with no set stored, at
// version 0, where a version read as 0 would pass.
func TestSaveWithAMissingOrMalformedVersionIsStale(t *testing.T) {
	cases := []struct {
		name string
		post func(t *testing.T, w *testWorld) *http.Response
	}{
		{"a hub form with no version", func(t *testing.T, w *testWorld) *http.Response {
			form := updateForm(t, w.st)
			form.Del("version")
			form.Set("user_limit", "5")
			return w.b.postForm(hubPath(t, w.st, "hub-1"), form)
		}},
		{"a guild-wide section with version abc", func(t *testing.T, w *testWorld) *http.Response {
			form := moderatorsForm(t, w.st, "role-hq")
			form.Set("version", "abc")
			return w.b.postForm("/moderators", form)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorld(t, testHub())
			signIn(t, w.forum, w.b)
			before := storedHubs(t, w.st)[0]

			res := tc.post(t, w)

			if res.StatusCode != http.StatusConflict {
				t.Errorf("status = %d, want 409", res.StatusCode)
			}
			if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
				t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
			}
			if got := storedGuildRoles(t, w.st); len(got) != 0 {
				t.Errorf("stored guild roles = %v, want none", got)
			}
			if n := len(storedChangeLog(t, w.st, before.ID)) + len(storedChangeLog(t, w.st, 0)); n != 0 {
				t.Errorf("appended %d entries, want none", n)
			}
		})
	}
}

// A stale form is refused as stale before its fields are checked: a stale
// form with an invalid user limit gets 409, where a validation refusal is
// 422, and nothing is written.
func TestStaleFormWithAnInvalidFieldIsRefusedAsStale(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	stale := updateForm(t, w.st)
	moved := updateForm(t, w.st)
	moved.Set("base_string", "Bravo Voice")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), moved), "/")
	before := storedHubs(t, w.st)[0]
	stale.Set("user_limit", "100")

	res := w.b.postForm(hubPath(t, w.st, "hub-1"), stale)

	if res.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", res.StatusCode)
	}
	if after := storedHubs(t, w.st)[0]; !sameHubSettings(after, before) {
		t.Errorf("stored hub = %+v, want it unchanged from %+v", after, before)
	}
	if n := len(storedChangeLog(t, w.st, before.ID)); n != 1 {
		t.Errorf("the hub has %d entries, want the one earlier save's", n)
	}
}

// otherWriterStore is the store fake with another writer beside the panel.
// Armed with a method's name and a write, it makes that write on the fake
// just before the next call of that method goes through: another save
// landing between two steps of this one. The write is a setup method's,
// which adds one to the version as a save would.
type otherWriterStore struct {
	*store.Fake
	mu     sync.Mutex
	method string
	write  func()
}

// before arms the write to land just before the next call of method.
func (s *otherWriterStore) before(method string, write func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.method, s.write = method, write
}

// land makes the armed write if method is the one it waits for.
func (s *otherWriterStore) land(method string) {
	s.mu.Lock()
	write := s.write
	if s.method != method {
		write = nil
	}
	if write != nil {
		s.method, s.write = "", nil
	}
	s.mu.Unlock()
	if write != nil {
		write()
	}
}

func (s *otherWriterStore) ListHubs(ctx context.Context, guildID string) ([]store.Hub, error) {
	s.land("ListHubs")
	return s.Fake.ListHubs(ctx, guildID)
}

func (s *otherWriterStore) SaveHub(ctx context.Context, hub store.Hub, entry store.ChangeLogEntry) (store.Hub, error) {
	s.land("SaveHub")
	return s.Fake.SaveHub(ctx, hub, entry)
}

func (s *otherWriterStore) RemoveHub(ctx context.Context, id int64, entry store.ChangeLogEntry) error {
	s.land("RemoveHub")
	return s.Fake.RemoveHub(ctx, id, entry)
}

func (s *otherWriterStore) SaveGuildModeratorRoles(ctx context.Context, guildID string, roles store.GuildModeratorRoles, entry store.ChangeLogEntry) error {
	s.land("SaveGuildModeratorRoles")
	return s.Fake.SaveGuildModeratorRoles(ctx, guildID, roles, entry)
}

// newOtherWriterWorld is the test world over an otherWriterStore holding
// testHub, and that store. The world's st is nil; a test reads back
// through the store's Fake.
func newOtherWriterWorld(t *testing.T) (*testWorld, *otherWriterStore) {
	t.Helper()
	st := &otherWriterStore{Fake: store.NewFake()}
	if _, err := st.UpsertHub(context.Background(), testHub()); err != nil {
		t.Fatalf("UpsertHub: %v", err)
	}
	w := newTestWorldOver(t, st, newFakeForum(t))
	signIn(t, w.forum, w.b)
	return w, st
}

// otherSave is another save of the hub on hub-1 landing: its base string
// becomes Other Voice.
func otherSave(t *testing.T, st *otherWriterStore) func() {
	return func() {
		h := testHub()
		h.BaseString = "Other Voice"
		if _, err := st.UpsertHub(context.Background(), h); err != nil {
			t.Errorf("the other save: %v", err)
		}
	}
}

// otherRemove is another process removing the hub on hub-1.
func otherRemove(t *testing.T, st *otherWriterStore) func() {
	id := storedHubID(t, st.Fake, "hub-1")
	return func() {
		if err := st.DeleteHub(context.Background(), id); err != nil {
			t.Errorf("the other remove: %v", err)
		}
	}
}

// A form refused for another reason is rendered with the version it
// posted, never the stored one, so fixing the field and saving again still
// meets a save that landed since the form loaded. The other save here lands
// as the refused page lists the hubs, after the refusal and before the
// page reads the record's version.
func TestFormRefusedForAFieldKeepsItsVersionThroughAnotherSave(t *testing.T) {
	cases := []struct {
		name   string
		action func(t *testing.T, st *otherWriterStore) string
		// refused posts the form with a field the save refuses.
		refused func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response
		// other is the save that lands meanwhile.
		other func(t *testing.T, st *otherWriterStore) func()
		// fix corrects the refused field on the rendered form.
		fix func(form url.Values)
	}{
		{
			name:   "hub form",
			action: func(t *testing.T, st *otherWriterStore) string { return hubPath(t, st.Fake, "hub-1") },
			refused: func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response {
				form := updateForm(t, st.Fake)
				form.Set("user_limit", "100")
				return w.b.postForm(hubPath(t, st.Fake, "hub-1"), form)
			},
			other: otherSave,
			fix:   func(form url.Values) { form.Set("user_limit", "5") },
		},
		{
			name:   "guild-wide section",
			action: func(*testing.T, *otherWriterStore) string { return "/moderators" },
			refused: func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response {
				return w.b.postForm("/moderators", moderatorsForm(t, st.Fake, "role-gone"))
			},
			other: func(t *testing.T, st *otherWriterStore) func() {
				return func() {
					if err := st.SetGuildModeratorRoles(context.Background(), testGuildID, []string{"role-mp"}); err != nil {
						t.Errorf("the other save: %v", err)
					}
				}
			},
			fix: func(form url.Values) { form["moderator_roles"] = []string{"role-hq"} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, st := newOtherWriterWorld(t)
			st.before("ListHubs", tc.other(t, st))

			res := tc.refused(t, w, st)

			if res.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("the refused save's status = %d, want 422", res.StatusCode)
			}
			form := formPosts(t, parseHTML(t, res), tc.action(t, st))
			tc.fix(form)

			if res := w.b.postForm(tc.action(t, st), form); res.StatusCode != http.StatusConflict {
				t.Errorf("the save with the field fixed got status %d, want 409", res.StatusCode)
			}
		})
	}
}

// The store checks the version again at the write, a backstop against
// another process saving between this save's read and its write. With no
// Discord change made, that is a stale refusal and nothing is written. An
// update that already renamed the channel keeps the rename and fails with
// 5xx, and its one Sentry event names both channel names, whether another
// process saved the hub or removed it. An update of a hub another process
// removed, with no rename, changed nothing and is 404, and so is a remove
// of one. A register whose channel another process made a hub gets the
// "already a hub" refusal.
func TestSaveWhoseRecordChangesBeforeItsWriteWritesNothing(t *testing.T) {
	cases := []struct {
		name   string
		method string
		// other is the other process's save.
		other func(t *testing.T, st *otherWriterStore) func()
		post  func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response
		check func(t *testing.T, w *testWorld, st *otherWriterStore, res *http.Response, rec *sentryRecorder)
	}{
		{
			name:   "an update with the name unchanged",
			method: "SaveHub",
			other:  otherSave,
			post: func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response {
				form := updateForm(t, st.Fake)
				form.Set("user_limit", "5")
				return w.b.postForm(hubPath(t, st.Fake, "hub-1"), form)
			},
			check: func(t *testing.T, _ *testWorld, st *otherWriterStore, res *http.Response, _ *sentryRecorder) {
				if res.StatusCode != http.StatusConflict {
					t.Errorf("status = %d, want 409", res.StatusCode)
				}
				if h := storedHubs(t, st.Fake)[0]; h.BaseString != "Other Voice" || h.UserLimit != 0 {
					t.Errorf("stored hub = %+v, want the other save's alone: Other Voice, limit 0", h)
				}
			},
		},
		{
			name:   "an update with a rename",
			method: "SaveHub",
			other:  otherSave,
			post: func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response {
				form := updateForm(t, st.Fake)
				form.Set("channel_name", "Bravo Room")
				return w.b.postForm(hubPath(t, st.Fake, "hub-1"), form)
			},
			check: func(t *testing.T, w *testWorld, st *otherWriterStore, res *http.Response, rec *sentryRecorder) {
				if !isServerError(res.StatusCode) {
					t.Errorf("status = %d, want 5xx", res.StatusCode)
				}
				if ch, err := w.discord.Channel("hub-1"); err != nil || ch.Name != "Bravo Room" {
					t.Errorf("hub-1 = %+v, %v; want it renamed to Bravo Room: the rename stays", ch, err)
				}
				if h := storedHubs(t, st.Fake)[0]; h.BaseString != "Other Voice" {
					t.Errorf("stored hub = %+v, want the other save's", h)
				}
				errs := rec.recorded()
				if len(errs) != 1 {
					t.Fatalf("Sentry got %d events, want 1", len(errs))
				}
				if msg := errs[0].Error(); !strings.Contains(msg, "Join to create") || !strings.Contains(msg, "Bravo Room") {
					t.Errorf("the event's error %q does not name both Join to create and Bravo Room", msg)
				}
			},
		},
		{
			name:   "an update with a rename whose hub another process removed",
			method: "SaveHub",
			other:  otherRemove,
			post: func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response {
				form := updateForm(t, st.Fake)
				form.Set("channel_name", "Bravo Room")
				return w.b.postForm(hubPath(t, st.Fake, "hub-1"), form)
			},
			check: func(t *testing.T, w *testWorld, st *otherWriterStore, res *http.Response, rec *sentryRecorder) {
				if !isServerError(res.StatusCode) {
					t.Errorf("status = %d, want 5xx", res.StatusCode)
				}
				if ch, err := w.discord.Channel("hub-1"); err != nil || ch.Name != "Bravo Room" {
					t.Errorf("hub-1 = %+v, %v; want it renamed to Bravo Room: the rename stays", ch, err)
				}
				if hubs := storedHubs(t, st.Fake); len(hubs) != 0 {
					t.Errorf("stored hubs = %+v, want none: the hub stays removed", hubs)
				}
				errs := rec.recorded()
				if len(errs) != 1 {
					t.Fatalf("Sentry got %d events, want 1", len(errs))
				}
				if msg := errs[0].Error(); !strings.Contains(msg, "Join to create") || !strings.Contains(msg, "Bravo Room") {
					t.Errorf("the event's error %q does not name both Join to create and Bravo Room", msg)
				}
			},
		},
		{
			name:   "an update with the name unchanged whose hub another process removed",
			method: "SaveHub",
			other:  otherRemove,
			post: func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response {
				form := updateForm(t, st.Fake)
				form.Set("user_limit", "5")
				return w.b.postForm(hubPath(t, st.Fake, "hub-1"), form)
			},
			check: func(t *testing.T, w *testWorld, st *otherWriterStore, res *http.Response, rec *sentryRecorder) {
				if res.StatusCode != http.StatusNotFound {
					t.Errorf("status = %d, want 404", res.StatusCode)
				}
				if edits := w.discord.edits(); len(edits) != 0 {
					t.Errorf("Discord edits = %+v, want none", edits)
				}
				if hubs := storedHubs(t, st.Fake); len(hubs) != 0 {
					t.Errorf("stored hubs = %+v, want none: the hub stays removed", hubs)
				}
				if errs := rec.recorded(); len(errs) != 0 {
					t.Errorf("Sentry got %d events, want none", len(errs))
				}
			},
		},
		{
			name:   "a remove whose hub another process removed",
			method: "RemoveHub",
			other:  otherRemove,
			post: func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response {
				return w.b.postForm(hubPath(t, st.Fake, "hub-1")+"/remove", nil)
			},
			check: func(t *testing.T, _ *testWorld, st *otherWriterStore, res *http.Response, rec *sentryRecorder) {
				if res.StatusCode != http.StatusNotFound {
					t.Errorf("status = %d, want 404", res.StatusCode)
				}
				if hubs := storedHubs(t, st.Fake); len(hubs) != 0 {
					t.Errorf("stored hubs = %+v, want none", hubs)
				}
				if errs := rec.recorded(); len(errs) != 0 {
					t.Errorf("Sentry got %d events, want none", len(errs))
				}
			},
		},
		{
			name:   "a guild-wide save",
			method: "SaveGuildModeratorRoles",
			other: func(t *testing.T, st *otherWriterStore) func() {
				return func() {
					if err := st.SetGuildModeratorRoles(context.Background(), testGuildID, []string{"role-mp"}); err != nil {
						t.Errorf("the other save: %v", err)
					}
				}
			},
			post: func(t *testing.T, w *testWorld, st *otherWriterStore) *http.Response {
				return w.b.postForm("/moderators", moderatorsForm(t, st.Fake, "role-hq"))
			},
			check: func(t *testing.T, _ *testWorld, st *otherWriterStore, res *http.Response, _ *sentryRecorder) {
				if res.StatusCode != http.StatusConflict {
					t.Errorf("status = %d, want 409", res.StatusCode)
				}
				if got := storedGuildRoles(t, st.Fake); !sameSet(got, []string{"role-mp"}) {
					t.Errorf("stored guild roles = %v, want the other save's role-mp", got)
				}
			},
		},
		{
			name:   "a register",
			method: "SaveHub",
			other: func(t *testing.T, st *otherWriterStore) func() {
				return func() {
					h := testHub()
					h.HubChannelID, h.BaseString = "vc-2", "Other Voice"
					if _, err := st.UpsertHub(context.Background(), h); err != nil {
						t.Errorf("the other register: %v", err)
					}
				}
			},
			post: func(t *testing.T, w *testWorld, _ *otherWriterStore) *http.Response {
				return w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice"))
			},
			check: func(t *testing.T, _ *testWorld, st *otherWriterStore, res *http.Response, _ *sentryRecorder) {
				if field, ok := errorField(t, res); res.StatusCode != http.StatusUnprocessableEntity || !ok || field != "hub_channel" {
					t.Errorf("status %d, data-error %q (present %v); want 422 on hub_channel", res.StatusCode, field, ok)
				}
				var onVC2 []store.Hub
				for _, h := range storedHubs(t, st.Fake) {
					if h.HubChannelID == "vc-2" {
						onVC2 = append(onVC2, h)
					}
				}
				if len(onVC2) != 1 || onVC2[0].BaseString != "Other Voice" {
					t.Errorf("hubs on vc-2 = %+v, want the other register's alone", onVC2)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, st := newOtherWriterWorld(t)
			rec := recordSentry(t)
			st.before(tc.method, tc.other(t, st))

			res := tc.post(t, w, st)

			tc.check(t, w, st, res, rec)
			for _, h := range append(storedHubs(t, st.Fake), store.Hub{}) {
				if entries := storedChangeLog(t, st.Fake, h.ID); len(entries) != 0 {
					t.Errorf("hub %d has entries %+v, want none from the save", h.ID, entries)
				}
			}
		})
	}
}

// holdLimit is how long a held write waits, once the second save is past
// the forum, for that save's first store read before the test lets it go.
// Saves that take turns never read during the hold, so they always wait
// the whole limit; saves that do not reach their read in-process, well
// inside it.
const holdLimit = 200 * time.Millisecond

// holdingStore is the store fake with one save's write held. Armed, the
// next SaveHub or RemoveHub waits before it writes until any read of the
// store arrives, or until the test lets it go. Two saves that do not take
// turns would both read before either writes: the second's read ends the
// hold. Saves that take turns cannot read during it.
type holdingStore struct {
	*store.Fake
	mu      sync.Mutex
	armed   bool
	held    chan struct{}
	release chan struct{}
	holding bool
}

// holdNextWrite arms the hold and returns a channel closed once the next
// write is held.
func (s *holdingStore) holdNextWrite() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.armed, s.held, s.release = true, make(chan struct{}), make(chan struct{})
	return s.held
}

// endHoldLocked ends the hold, once, whoever calls it first. Caller holds
// mu.
func (s *holdingStore) endHoldLocked() {
	if s.holding {
		s.holding = false
		close(s.release)
	}
}

// letGo ends the hold if it is still on.
func (s *holdingStore) letGo() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endHoldLocked()
}

// read is what every read passes through: it ends a hold that is on.
func (s *holdingStore) read() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endHoldLocked()
}

// write is what every held-able write passes through: the armed one waits
// here until the hold ends.
func (s *holdingStore) write() {
	s.mu.Lock()
	if !s.armed {
		s.mu.Unlock()
		return
	}
	s.armed, s.holding = false, true
	release := s.release
	close(s.held)
	s.mu.Unlock()
	<-release
}

func (s *holdingStore) GetHub(ctx context.Context, id int64) (store.Hub, error) {
	s.read()
	return s.Fake.GetHub(ctx, id)
}

func (s *holdingStore) ListHubs(ctx context.Context, guildID string) ([]store.Hub, error) {
	s.read()
	return s.Fake.ListHubs(ctx, guildID)
}

func (s *holdingStore) GetGuildModeratorRoles(ctx context.Context, guildID string) (store.GuildModeratorRoles, error) {
	s.read()
	return s.Fake.GetGuildModeratorRoles(ctx, guildID)
}

func (s *holdingStore) SaveHub(ctx context.Context, hub store.Hub, entry store.ChangeLogEntry) (store.Hub, error) {
	s.write()
	return s.Fake.SaveHub(ctx, hub, entry)
}

func (s *holdingStore) RemoveHub(ctx context.Context, id int64, entry store.ChangeLogEntry) error {
	s.write()
	return s.Fake.RemoveHub(ctx, id, entry)
}

// userinfoRequestCount is how many group checks the forum has answered.
func (f *fakeForum) userinfoRequestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.userinfoRequests
}

// holdingWorld is the test world over a holdingStore holding the hubs, with
// the first browser signed in and a second one, and that store. The world's
// st is nil; a test reads back through the store's Fake.
func holdingWorld(t *testing.T, hubs ...store.Hub) (*testWorld, *browser, *holdingStore) {
	t.Helper()
	st := &holdingStore{Fake: store.NewFake()}
	for _, h := range hubs {
		if _, err := st.UpsertHub(context.Background(), h); err != nil {
			t.Fatalf("UpsertHub: %v", err)
		}
	}
	w := newTestWorldOver(t, st, newFakeForum(t))
	signIn(t, w.forum, w.b)
	return w, secondBrowser(t, w), st
}

// duringHold runs the first save until its write is held, then the second
// while it is. Once the forum has answered the second save's group check,
// the only I/O before its first store read, the hold ends at that read or
// holdLimit later. It returns both answers once both saves are done.
func duringHold(t *testing.T, w *testWorld, st *holdingStore, first, second func() *http.Response) (*http.Response, *http.Response) {
	t.Helper()
	held := st.holdNextWrite()
	firstDone := make(chan *http.Response, 1)
	go func() { firstDone <- first() }()
	select {
	case <-held:
	case <-time.After(neverReleased):
		t.Fatal("the first save never reached its write")
	}
	checks := w.forum.userinfoRequestCount()
	secondDone := make(chan *http.Response, 1)
	go func() { secondDone <- second() }()
	giveUp := time.After(neverReleased)
	for w.forum.userinfoRequestCount() == checks {
		select {
		case <-giveUp:
			t.Fatal("the second save never reached the forum")
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(holdLimit)
	st.letGo()
	return <-firstDone, <-secondDone
}

// (b), (d) Two updates of one hub, the second posted while the first is
// held before its write. The second waits for the first, reads what it
// wrote, and is refused as stale before its rename: the store, the change
// log and the running bot hold the first save alone.
func TestTwoSavesOfAHubTakeTurns(t *testing.T) {
	w, b, st := holdingWorld(t, testHub())
	path := hubPath(t, st.Fake, "hub-1")
	formA := updateForm(t, st.Fake)
	formA.Set("user_limit", "5")
	formB := updateForm(t, st.Fake)
	formB.Set("base_string", "Bravo Voice")
	formB.Set("channel_name", "Bravo Room")

	resA, resB := duringHold(t, w, st,
		func() *http.Response { return w.b.postForm(path, formA) },
		func() *http.Response { return b.postForm(path, formB) })

	assertRedirect(t, resA, "/")
	if resB.StatusCode != http.StatusConflict {
		t.Errorf("the second save's status = %d, want 409", resB.StatusCode)
	}
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("Discord edits = %+v, want none", edits)
	}
	h := storedHubs(t, st.Fake)[0]
	if h.UserLimit != 5 || h.BaseString != "Arma Voice" {
		t.Errorf("stored hub = %+v, want the first save's alone: limit 5, Arma Voice", h)
	}
	if entries := storedChangeLog(t, st.Fake, h.ID); len(entries) != 1 {
		t.Errorf("the hub has %d entries, want the first save's one", len(entries))
	}
	w.join("user-a", "hub-1")
	if creates := w.discord.creates(); len(creates) != 1 || !strings.Contains(creates[0].Name, "Arma Voice") || creates[0].UserLimit != 5 {
		t.Errorf("a join made creates %+v, want one from the first save: Arma Voice, limit 5", creates)
	}
}

// (e) A remove of a hub, and an update of the same hub posted while the
// remove is held before its write. The update waits for the remove and
// finds no hub: 404, no rename, no new row, and a join to the channel
// spawns nothing.
func TestUpdateThatWaitsForARemoveOfItsHubIsNotFound(t *testing.T) {
	w, b, st := holdingWorld(t, testHub())
	path := hubPath(t, st.Fake, "hub-1")
	form := updateForm(t, st.Fake)
	form.Set("channel_name", "Bravo Room")
	form.Set("user_limit", "5")

	resRemove, resUpdate := duringHold(t, w, st,
		func() *http.Response { return w.b.postForm(path+"/remove", nil) },
		func() *http.Response { return b.postForm(path, form) })

	assertRedirect(t, resRemove, "/")
	if resUpdate.StatusCode != http.StatusNotFound {
		t.Errorf("the update's status = %d, want 404", resUpdate.StatusCode)
	}
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("Discord edits = %+v, want none", edits)
	}
	if hubs := storedHubs(t, st.Fake); len(hubs) != 0 {
		t.Errorf("stored hubs = %+v, want none", hubs)
	}
	w.join("user-a", "hub-1")
	if n := w.discord.createCount(); n != 0 {
		t.Errorf("a join to hub-1 made %d creates, want none", n)
	}
}

// (f) Two registers of one channel, the second posted while the first is
// held before its write. One hub stands with the first register's settings
// and one register entry, and the second gets the "already a hub" refusal.
func TestTwoRegistersOfAChannelLeaveOneHub(t *testing.T) {
	w, b, st := holdingWorld(t)

	resFirst, resSecond := duringHold(t, w, st,
		func() *http.Response { return w.b.postForm("/hubs", registerForm("vc-2", "Squad Voice")) },
		func() *http.Response { return b.postForm("/hubs", registerForm("vc-2", "Other Voice")) })

	assertRedirect(t, resFirst, "/")
	if field, ok := errorField(t, resSecond); resSecond.StatusCode != http.StatusUnprocessableEntity || !ok || field != "hub_channel" {
		t.Errorf("the second register: status %d, data-error %q (present %v); want 422 on hub_channel", resSecond.StatusCode, field, ok)
	}
	hubs := storedHubs(t, st.Fake)
	if len(hubs) != 1 || hubs[0].HubChannelID != "vc-2" || hubs[0].BaseString != "Squad Voice" {
		t.Fatalf("stored hubs = %+v, want one on vc-2 with Squad Voice", hubs)
	}
	if entries := storedChangeLog(t, st.Fake, hubs[0].ID); len(entries) != 1 || entries[0].Action != store.ChangeRegister {
		t.Errorf("the hub's entries = %+v, want one register", entries)
	}
}

// renameInDiscord renames a channel the way a member does in Discord's own
// client: the guild's list has the new name, and the panel made no edit.
func (f *fakeDiscord) renameInDiscord(channelID, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.channels {
		if ch.ID == channelID {
			ch.Name = name
		}
	}
}

// (g) A rename made in Discord does not make a form stale, and a save
// renames the channel only when the user changed the name field from the
// name the form loaded. A save from a form loaded before the rename, with
// only the user limit changed, leaves the new name and records no
// channel_name. A save that changes the name field renames the channel,
// with the live name as the entry's before.
func TestRenameMadeInDiscordStandsUnlessTheUserChangesTheName(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	id := storedHubID(t, w.st, "hub-1")
	loadedBefore := updateForm(t, w.st)
	w.discord.renameInDiscord("hub-1", "Renamed Room")
	loadedBefore.Set("user_limit", "5")

	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), loadedBefore), "/")

	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("Discord edits = %+v, want none", edits)
	}
	if ch, _ := w.discord.Channel("hub-1"); ch.Name != "Renamed Room" {
		t.Errorf("hub-1 is named %q, want Renamed Room: the rename made in Discord stands", ch.Name)
	}
	entries := storedChangeLog(t, w.st, id)
	if len(entries) != 1 {
		t.Fatalf("the hub has %d entries, want 1", len(entries))
	}
	if c, ok := decodeDiff(t, entries[0])["channel_name"]; ok {
		t.Errorf("the entry records channel_name %+v, want none", c)
	}

	renaming := updateForm(t, w.st)
	renaming.Set("channel_name", "Bravo Room")
	assertRedirect(t, w.b.postForm(hubPath(t, w.st, "hub-1"), renaming), "/")

	if edits := w.discord.edits(); len(edits) != 1 || edits[0].ChannelID != "hub-1" || edits[0].Name != "Bravo Room" {
		t.Errorf("Discord edits = %+v, want hub-1 renamed to Bravo Room", edits)
	}
	entries = storedChangeLog(t, w.st, id)
	if len(entries) != 2 {
		t.Fatalf("the hub has %d entries, want 2", len(entries))
	}
	if c := decodeDiff(t, entries[0])["channel_name"]; c.Before != "Renamed Room" || c.After != "Bravo Room" {
		t.Errorf("the entry's channel_name = %+v, want before Renamed Room (the live name), after Bravo Room", c)
	}
}

// A stale refusal over a rename made in Discord, with the name field left
// as loaded: neither the refused save nor the save of its rendered form
// renames the channel back.
func TestStaleRefusalOverARenameMadeInDiscordRenamesNothing(t *testing.T) {
	w := newTestWorld(t, testHub())
	signIn(t, w.forum, w.b)
	b := secondBrowser(t, w)
	idText := strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)
	page, action := "/?hub="+idText, "/hubs/"+idText
	formB := loadedForm(t, b, page, action)
	w.discord.renameInDiscord("hub-1", "Renamed Room")
	formA := loadedForm(t, w.b, page, action)
	formA.Set("user_limit", "5")
	assertRedirect(t, w.b.postForm(action, formA), "/")
	formB.Set("base_string", "Bravo Voice")

	res := b.postForm(action, formB)

	if res.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	assertRedirect(t, b.postForm(action, formPosts(t, parseHTML(t, res), action)), "/")
	if edits := w.discord.edits(); len(edits) != 0 {
		t.Errorf("Discord edits = %+v, want none", edits)
	}
	if ch, _ := w.discord.Channel("hub-1"); ch.Name != "Renamed Room" {
		t.Errorf("hub-1 is named %q, want Renamed Room", ch.Name)
	}
}
