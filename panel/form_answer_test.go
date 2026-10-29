package panel

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"golang.org/x/net/html"
)

// answerStore is the store a form answer's world runs over: the fake, with
// ctxStore's page reads that can be made to fail, and each save's write
// refused while writesFail is set.
type answerStore struct {
	*ctxStore
	writesFail atomic.Bool
}

// refused is errStoreRefused while writes fail, nil otherwise.
func (s *answerStore) refused() error {
	if s.writesFail.Load() {
		return errStoreRefused
	}
	return nil
}

func (s *answerStore) SaveHub(ctx context.Context, hub store.Hub, entry store.ChangeLogEntry) (store.Hub, error) {
	if err := s.refused(); err != nil {
		return store.Hub{}, err
	}
	return s.ctxStore.SaveHub(ctx, hub, entry)
}

func (s *answerStore) RemoveHub(ctx context.Context, id int64, entry store.ChangeLogEntry) (int64, error) {
	if err := s.refused(); err != nil {
		return 0, err
	}
	return s.ctxStore.RemoveHub(ctx, id, entry)
}

func (s *answerStore) SaveGuildModeratorRoles(ctx context.Context, guildID string, roles store.GuildModeratorRoles, entry store.ChangeLogEntry) error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.ctxStore.SaveGuildModeratorRoles(ctx, guildID, roles, entry)
}

func (s *answerStore) SaveRecordingRoles(ctx context.Context, guildID string, roles store.RecordingRoles, entry store.ChangeLogEntry) error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.ctxStore.SaveRecordingRoles(ctx, guildID, roles, entry)
}

func (s *answerStore) SaveRankRoles(ctx context.Context, guildID string, roles store.RankRoles, entry store.ChangeLogEntry) error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.ctxStore.SaveRankRoles(ctx, guildID, roles, entry)
}

func (s *answerStore) DeleteRecording(ctx context.Context, id int64) error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.ctxStore.DeleteRecording(ctx, id)
}

func (s *answerStore) SaveFoxholeNote(ctx context.Context, guildID string, save store.NoteSave, entry store.ChangeLogEntry) error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.ctxStore.SaveFoxholeNote(ctx, guildID, save, entry)
}

func (s *answerStore) ApproveFoxholeMembers(ctx context.Context, guildID string, approvals []store.MemberApprovals, entry store.ChangeLogEntry) error {
	if err := s.refused(); err != nil {
		return err
	}
	return s.ctxStore.ApproveFoxholeMembers(ctx, guildID, approvals, entry)
}

func (s *answerStore) StartFoxholeReport(ctx context.Context, entry store.ChangeLogEntry) (store.ChangeLogEntry, error) {
	if err := s.refused(); err != nil {
		return store.ChangeLogEntry{}, err
	}
	return s.ctxStore.StartFoxholeReport(ctx, entry)
}

// foxholeAnswerWorld is the world a form answer on the Foxhole page is sent
// in: newFoxholeWorld's, over an answerStore.
func foxholeAnswerWorld(t *testing.T) *testWorld {
	t.Helper()
	return newFoxholeWorldWith(t, func(st store.Store) store.Store {
		return &answerStore{ctxStore: &ctxStore{Fake: st.(*store.Fake)}}
	})
}

// recordingDelete records a recording by a starter the world adds, and
// opens its delete preview in the browser of the panel admin signed in. It
// returns the starter and the preview's Confirm.
func recordingDelete(t *testing.T, w *testWorld) (starter, pageForm) {
	t.Helper()
	a := w.addStarter(5001, "Able.A", "discord-a")
	id := w.record(t, a.discordID)
	confirm := deleteConfirm(t, w.b, id)
	doc := confirm
	for doc.Parent != nil {
		doc = doc.Parent
	}
	return a, pageForm{action: attrOf(confirm, "action"), values: formFields(doc, confirm)}
}

// answerStoreOf is the answerStore a form answer's world runs over.
func answerStoreOf(w *testWorld) *answerStore {
	if st, ok := w.store.(*answerStore); ok {
		return st
	}
	return w.store.(endWatch).Store.(*answerStore)
}

// newAnswerWorld is the world a form answer on the hub page or the Settings
// page is sent in: testHub stored, over an answerStore, with the browser
// signed in as a panel admin.
func newAnswerWorld(t *testing.T) *testWorld {
	t.Helper()
	st := &answerStore{ctxStore: &ctxStore{Fake: store.NewFake()}}
	if _, err := st.UpsertHub(context.Background(), testHub()); err != nil {
		t.Fatalf("UpsertHub: %v", err)
	}
	w := newTestWorldOver(t, st, newFakeForum(t))
	st.clock, w.st = w.clock, st.Fake
	signIn(t, w.forum, w.b)
	return w
}

// formAnswer is one answer to a form's POST that renders a page, for the
// walk over every route that takes a POST (ADR 0019).
type formAnswer struct {
	name string
	// route is the pattern in routes() the form posts to.
	route string
	// world builds the world the form is sent in, newAnswerWorld when nil.
	world func(t *testing.T) *testWorld
	// send readies the world and sends the form the way a browser with no
	// script does. It returns the address the form posted to, fragment
	// included, and the answer.
	send func(t *testing.T, w *testWorld) (string, *http.Response)
	// status is the answer's status, and shown the status a GET of the
	// address the answer names ends on.
	status, shown int
	// clear undoes a failure page's failure before its address loads, nil
	// for an answer that is no failure page.
	clear func(t *testing.T, w *testWorld)
	// refusals are the kinds of refusal a GET of the address shows, none
	// but for a page that refuses on a load too, as a removed hub's remove
	// preview does.
	refusals []string
	// pasteInTab marks the add preview, whose address names no pasted line
	// (ADR 0019). A GET of it shows the paste box and no preview, and the
	// page script loads the preview again from the paste the tab keeps.
	pasteInTab bool
}

// landedPage is what a page shows as a browser lands on it: its status, the
// kind of page, and the block heading its address's fragment names.
type landedPage struct {
	status int
	kind   string
	// heading is the ID and text of the heading the page opens at, empty
	// for a page that opens at its top.
	heading string
}

// landedPageOf reads a page a browser landed on at fragment.
func landedPageOf(t *testing.T, res *http.Response, fragment string) (landedPage, *html.Node) {
	t.Helper()
	doc := parseHTML(t, res)
	page := landedPage{status: res.StatusCode, kind: pageKindOf(t, doc)}
	if el := landedOn(doc, fragment); el != nil {
		page.heading = fragment + ": " + textOf(el)
	}
	return page, doc
}

// pageKindOf names which page a page is: the page data-page names, the
// failure the error page reports, or the no-access page.
func pageKindOf(t *testing.T, doc *html.Node) string {
	t.Helper()
	main := findElement(doc, "main", "", "")
	if main == nil {
		t.Fatal("the page has no <main>")
	}
	if page, ok := attrValue(main, "data-page"); ok && page != "" {
		return "page " + page
	}
	if f, ok := attrValue(main, "data-failure"); ok {
		return "failure " + f
	}
	if findElement(doc, "", "data-field", "no-access") != nil {
		return "no access"
	}
	return "unknown"
}

// fork is another tab of the same browser, signed in the same way, showing
// the page this one shows. What it loads leaves this one as it was.
func fork(b *browser) *browser {
	tab := *b
	tab.cookies = maps.Clone(b.cookies)
	return &tab
}

// pageForm is one form as a page rendered it: the action it posts to,
// fragment included, and what it posts untouched.
type pageForm struct {
	action string
	values url.Values
}

// formOn loads the page at page through b, following any redirect, and
// reads its form that posts to path, as pageFormIn does.
func formOn(t *testing.T, b *browser, page, path, section string) pageForm {
	t.Helper()
	res := follow(t, b, b.get(page))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", page, res.StatusCode)
	}
	return pageFormIn(t, parseHTML(t, res), path, section)
}

// pageFormIn reads the form of the page doc that posts to path. When several
// forms post to path, section names the data-section of the one read.
func pageFormIn(t *testing.T, doc *html.Node, path, section string) pageForm {
	t.Helper()
	var form *html.Node
	eachLiveElement(doc, func(n *html.Node) {
		posts, _, _ := strings.Cut(attrOf(n, "action"), "#")
		if form == nil && n.Data == "form" && posts == path && (section == "" || attrOf(n, "data-section") == section) {
			form = n
		}
	})
	if form == nil {
		t.Fatalf("the page has no form posting to %s", path)
	}
	return pageForm{action: attrOf(form, "action"), values: formFields(doc, form)}
}

// send posts the form through b the way a browser with no script does,
// with the changes given, and returns the address it posted to and the
// answer.
func (f pageForm) send(b *browser, change func(url.Values)) (string, *http.Response) {
	values := url.Values{}
	for k, v := range f.values {
		values[k] = slices.Clone(v)
	}
	if change != nil {
		change(values)
	}
	path, _, _ := strings.Cut(f.action, "#")
	return f.action, b.postForm(path, values)
}

// hubForm is the address of the stored hub on hub-1's edit form, and the
// address that form saves to.
func hubForm(t *testing.T, w *testWorld) (page, path string) {
	t.Helper()
	id := storedHubID(t, w.st, "hub-1")
	return "/?hub=" + strconv.FormatInt(id, 10), hubAddress(id)
}

// clearWrites lets the answer store's writes through again.
func clearWrites(_ *testing.T, w *testWorld) { answerStoreOf(w).writesFail.Store(false) }

// formAnswers is every answer to a form's POST that renders a page.
var formAnswers = []formAnswer{
	{name: "a stale hub edit", route: "POST /hubs/{id}", status: http.StatusConflict, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			page, path := hubForm(t, w)
			stale := formOn(t, w.b, page, path, "")
			other := secondBrowser(t, w)
			_, res := formOn(t, other, page, path, "").send(other, func(v url.Values) { v.Set(fieldUserLimit, "5") })
			assertRedirect(t, res, "/")
			return stale.send(w.b, func(v url.Values) { v.Set(fieldDeleteDelay, "30") })
		}},
	{name: "a hub edit whose user limit is no number", route: "POST /hubs/{id}", status: http.StatusUnprocessableEntity, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			page, path := hubForm(t, w)
			return formOn(t, w.b, page, path, "").send(w.b, func(v url.Values) { v.Set(fieldUserLimit, "lots") })
		}},
	{name: "a register with no base string", route: "POST /hubs", status: http.StatusUnprocessableEntity, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			return formOn(t, w.b, "/", "/hubs", "register").send(w.b, func(v url.Values) { v.Set(fieldHubChannel, "vc-2") })
		}},
	{name: "a removal Confirm with no version, from the hub list", route: "POST /hubs/{id}/remove", status: http.StatusConflict, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			id := storedHubID(t, w.st, "hub-1")
			return formOn(t, w.b, "/?remove="+strconv.FormatInt(id, 10), hubAddress(id)+"/remove", "").
				send(w.b, func(v url.Values) { v.Del(fieldVersion) })
		}},
	{name: "a removal Confirm with no version, from the hub's edit form", route: "POST /hubs/{id}/remove", status: http.StatusConflict, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			id := strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)
			return formOn(t, w.b, "/?hub="+id+"&remove="+id, "/hubs/"+id+"/remove", "").
				send(w.b, func(v url.Values) { v.Del(fieldVersion) })
		}},
	{name: "a removal Confirm of a hub someone has removed", route: "POST /hubs/{id}/remove", status: http.StatusNotFound, shown: http.StatusNotFound,
		refusals: []string{refusalGone},
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			id := storedHubID(t, w.st, "hub-1")
			confirm := formOn(t, w.b, "/?remove="+strconv.FormatInt(id, 10), hubAddress(id)+"/remove", "")
			assertRedirect(t, removeHubThroughThePreview(t, secondBrowser(t, w), id), "/")
			return confirm.send(w.b, nil)
		}},
	{name: "a hub edit of a hub someone has removed", route: "POST /hubs/{id}", status: http.StatusNotFound, shown: http.StatusNotFound,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			page, path := hubForm(t, w)
			form := formOn(t, w.b, page, path, "")
			assertRedirect(t, removeHubThroughThePreview(t, secondBrowser(t, w), storedHubID(t, w.st, "hub-1")), "/")
			return form.send(w.b, nil)
		}},
	{name: "a stale save of the moderator roles for every hub", route: "POST /moderators", status: http.StatusConflict, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			stale := formOn(t, w.b, "/", "/moderators", "")
			other := secondBrowser(t, w)
			_, res := formOn(t, other, "/", "/moderators", "").send(other, func(v url.Values) { v.Set(fieldModeratorRoles, "role-mp") })
			assertRedirect(t, res, "/")
			return stale.send(w.b, func(v url.Values) { v.Set(fieldModeratorRoles, "role-hq") })
		}},
	{name: "a stale save of the recording roles", route: "POST /recording-roles", status: http.StatusConflict, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			stale := formOn(t, w.b, settingsPath, "/recording-roles", "")
			other := secondBrowser(t, w)
			_, res := formOn(t, other, settingsPath, "/recording-roles", "").send(other, func(v url.Values) { v.Set(fieldRecordingRoles, "role-mp") })
			assertRedirect(t, res, settingsPath)
			return stale.send(w.b, func(v url.Values) { v.Set(fieldRecordingRoles, "role-hq") })
		}},
	{name: "a stale save of the rank roles", route: "POST /rank-roles", status: http.StatusConflict, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			stale := formOn(t, w.b, settingsPath, "/rank-roles", "")
			other := secondBrowser(t, w)
			_, res := formOn(t, other, settingsPath, "/rank-roles", "").send(other, func(v url.Values) { v.Set(rankRoleField("RCT"), "role-hq") })
			assertRedirect(t, res, settingsPath)
			return stale.send(w.b, func(v url.Values) { v.Set(rankRoleField("RCT"), "role-mp") })
		}},
	{name: "a hub edit that finds no data for the guild", route: "POST /hubs/{id}", status: http.StatusServiceUnavailable, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			page, path := hubForm(t, w)
			form := formOn(t, w.b, page, path, "")
			w.discord.setGuild(commands.GuildDataAbsent)
			return form.send(w.b, func(v url.Values) { v.Set(fieldDeleteDelay, "30") })
		},
		clear: func(_ *testing.T, w *testWorld) { w.discord.setGuild(commands.GuildDataPresent) }},
	{name: "a refused hub edit whose page fails to load", route: "POST /hubs/{id}", status: http.StatusServiceUnavailable, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			page, path := hubForm(t, w)
			form := formOn(t, w.b, page, path, "")
			answerStoreOf(w).failRead("ListModeratorChanges")
			return form.send(w.b, func(v url.Values) { v.Set(fieldBaseString, "   ") })
		},
		clear: func(_ *testing.T, w *testWorld) { answerStoreOf(w).failRead("") }},
	{name: "a recording roles save that finds no data for the guild", route: "POST /recording-roles", status: http.StatusServiceUnavailable, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			form := formOn(t, w.b, settingsPath, "/recording-roles", "")
			w.discord.setGuild(commands.GuildDataAbsent)
			return form.send(w.b, func(v url.Values) { v.Set(fieldRecordingRoles, "role-hq") })
		},
		clear: func(_ *testing.T, w *testWorld) { w.discord.setGuild(commands.GuildDataPresent) }},
	{name: "a refused recording roles save whose page fails to load", route: "POST /recording-roles", status: http.StatusServiceUnavailable, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			form := formOn(t, w.b, settingsPath, "/recording-roles", "")
			answerStoreOf(w).failRead("ListRecordingRoleChanges")
			return form.send(w.b, func(v url.Values) { v.Set(fieldVersion, "") })
		},
		clear: func(_ *testing.T, w *testWorld) { answerStoreOf(w).failRead("") }},
	{name: "a hub edit whose store write fails", route: "POST /hubs/{id}", status: http.StatusInternalServerError, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			page, path := hubForm(t, w)
			answerStoreOf(w).writesFail.Store(true)
			return formOn(t, w.b, page, path, "").send(w.b, func(v url.Values) { v.Set(fieldDeleteDelay, "30") })
		},
		clear: clearWrites},
	{name: "a removal Confirm whose store write fails, from the hub's edit form", route: "POST /hubs/{id}/remove", status: http.StatusInternalServerError, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			id := strconv.FormatInt(storedHubID(t, w.st, "hub-1"), 10)
			answerStoreOf(w).writesFail.Store(true)
			return formOn(t, w.b, "/?hub="+id+"&remove="+id, "/hubs/"+id+"/remove", "").send(w.b, nil)
		},
		clear: clearWrites},
	{name: "a create whose store write fails", route: "POST /hubs", status: http.StatusInternalServerError, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			answerStoreOf(w).writesFail.Store(true)
			return formOn(t, w.b, "/", "/hubs", "create").send(w.b, func(v url.Values) {
				v.Set(fieldCategory, "cat-1")
				v.Set(fieldChannelName, "Squad Join")
				v.Set(fieldBaseString, "Squad Voice")
			})
		},
		clear: clearWrites},
	{name: "a save of the moderator roles for every hub whose store write fails", route: "POST /moderators", status: http.StatusInternalServerError, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			answerStoreOf(w).writesFail.Store(true)
			return formOn(t, w.b, "/", "/moderators", "").send(w.b, func(v url.Values) { v.Set(fieldModeratorRoles, "role-hq") })
		},
		clear: clearWrites},
	{name: "a recording roles save whose store write fails", route: "POST /recording-roles", status: http.StatusInternalServerError, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			answerStoreOf(w).writesFail.Store(true)
			return formOn(t, w.b, settingsPath, "/recording-roles", "").send(w.b, func(v url.Values) { v.Set(fieldRecordingRoles, "role-hq") })
		},
		clear: clearWrites},
	{name: "a rank roles save whose store write fails", route: "POST /rank-roles", status: http.StatusInternalServerError, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			answerStoreOf(w).writesFail.Store(true)
			return formOn(t, w.b, settingsPath, "/rank-roles", "").send(w.b, func(v url.Values) { v.Set(rankRoleField("RCT"), "role-hq") })
		},
		clear: clearWrites},
	{name: "a rank roles Confirm whose preview moved", route: "POST /rank-roles", status: http.StatusConflict, shown: http.StatusOK,
		world: withSergeantInSpawn,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			address := location(t, w.b.postForm("/rank-roles", rankRolesForm(t, w.st, map[string]string{"SGT": ""}))).String()
			confirm := formOn(t, w.b, address, "/rank-roles", "")
			w.joinAs("user-sgt2", "hub-1", testRankSGT)
			w.joinAs("user-sgt2", "spawn-2", testRankSGT)
			return confirm.send(w.b, nil)
		}},
	{name: "a delete of a recording someone has deleted", route: "POST /recordings/{id}/delete", status: http.StatusNotFound, shown: http.StatusNotFound,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			_, confirm := recordingDelete(t, w)
			other := secondBrowser(t, w)
			_, res := confirm.send(other, nil)
			assertRedirect(t, res, recordingsPath)
			return confirm.send(w.b, nil)
		}},
	{name: "a delete by someone who neither started the recording nor is a panel admin", route: "POST /recordings/{id}/delete", status: http.StatusForbidden, shown: http.StatusForbidden,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			_, confirm := recordingDelete(t, w)
			signInAs(t, w.forum, w.b, w.addStarter(5002, "Baker.B", "discord-b").account)
			return confirm.send(w.b, nil)
		}},
	{name: "a delete whose store write fails", route: "POST /recordings/{id}/delete", status: http.StatusInternalServerError, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			_, confirm := recordingDelete(t, w)
			answerStoreOf(w).writesFail.Store(true)
			return confirm.send(w.b, nil)
		},
		clear: clearWrites},
	{name: "a stale note save", route: "POST " + foxholeNotesPath, status: http.StatusConflict, shown: http.StatusOK, world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			seedNote(t, w, namesOf(memberAsh), "allied liaison")
			page := editNoteLink(t, parseHTML(t, w.b.get(foxholePath)), memberAsh.ID)
			stale := formOn(t, w.b, page, foxholeNotesPath, "")
			_, res := formOn(t, w.b, page, foxholeNotesPath, "").send(w.b, func(v url.Values) { v.Set(fieldNote, "liaison to the 101st") })
			assertRedirect(t, res, foxholePath)
			return stale.send(w.b, func(v url.Values) { v.Set(fieldNote, "liaison, ask S1") })
		}},
	{name: "a note save whose store write fails", route: "POST " + foxholeNotesPath, status: http.StatusInternalServerError, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			form := formOn(t, w.b, editNoteLink(t, parseHTML(t, w.b.get(foxholePath)), memberAsh.ID), foxholeNotesPath, "")
			answerStoreOf(w).writesFail.Store(true)
			return form.send(w.b, func(v url.Values) { v.Set(fieldNote, "liaison, ask S1") })
		},
		clear: clearWrites},
	{name: "an Approve of a member approved already", route: "POST " + foxholeApprovalsPath, status: http.StatusConflict, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			approveThroughThePage(t, w, memberMarsh.ID)
			return formOn(t, w.b, foxholePath, foxholeApprovalsPath, "").send(w.b, func(v url.Values) {
				v.Set(fieldMember, memberMarsh.ID)
				v.Set(fieldOp, opApprove)
			})
		}},
	{name: "a refused Approve whose page fails to load", route: "POST " + foxholeApprovalsPath, status: http.StatusServiceUnavailable, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			approveThroughThePage(t, w, memberMarsh.ID)
			form := formOn(t, w.b, foxholePath, foxholeApprovalsPath, "")
			answerStoreOf(w).failRead("ListFoxholeChanges")
			return form.send(w.b, func(v url.Values) {
				v.Set(fieldMember, memberMarsh.ID)
				v.Set(fieldOp, opApprove)
			})
		},
		clear: func(_ *testing.T, w *testWorld) { answerStoreOf(w).failRead("") }},
	{name: "an Approve whose store write fails", route: "POST " + foxholeApprovalsPath, status: http.StatusInternalServerError, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			form := formOn(t, w.b, foxholePath, foxholeApprovalsPath, "")
			answerStoreOf(w).writesFail.Store(true)
			return form.send(w.b, func(v url.Values) {
				v.Set(fieldMember, memberKestrel.ID)
				v.Set(fieldOp, opApprove)
			})
		},
		clear: clearWrites},
	{name: "a purge Confirm with nobody left to purge", route: "POST " + foxholePurgePath, status: http.StatusConflict, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			form := formOn(t, w.b, foxholePath+"?"+paramPurge+"=external", foxholePurgePath, "")
			startPurge(t, w, "external")
			w.awaitActionEnd(t)
			return form.send(w.b, nil)
		}},
	{name: "a purge Confirm whose start the store refuses", route: "POST " + foxholePurgePath, status: http.StatusInternalServerError, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			form := formOn(t, w.b, foxholePath+"?"+paramPurge+"=external", foxholePurgePath, "")
			answerStoreOf(w).writesFail.Store(true)
			return form.send(w.b, nil)
		},
		clear: clearWrites},
	{name: "an add preview", route: "POST " + foxholeAddPreviewPath, status: http.StatusOK, shown: http.StatusOK, world: foxholeAnswerWorld,
		pasteInTab: true,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			return formOn(t, w.b, foxholePath, foxholeAddPreviewPath, "").send(w.b, func(v url.Values) {
				v.Set(fieldRole, "external")
				v.Set(fieldLines, memberDoe.Username)
			})
		}},
	{name: "an add Confirm with nobody left to add", route: "POST " + foxholeAddPath, status: http.StatusConflict, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			preview := func() pageForm {
				_, res := formOn(t, w.b, foxholePath, foxholeAddPreviewPath, "").send(w.b, func(v url.Values) {
					v.Set(fieldRole, "external")
					v.Set(fieldLines, "rvance")
				})
				return pageFormIn(t, parseHTML(t, res), foxholeAddPath, "")
			}
			kept := preview()
			_, res := preview().send(w.b, nil)
			assertRedirect(t, res, foxholePath)
			w.awaitActionEnd(t)
			return kept.send(w.b, nil)
		}},
	{name: "a roster add Confirm with nobody left to add", route: "POST " + foxholeRosterPath, status: http.StatusConflict, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			serveRoster(t, trooperVance, trooperDoe, trooperNolink)
			page := foxholePath + "?" + url.Values{fieldUnit: {"D/ACD"}}.Encode()
			kept := formOn(t, w.b, page, foxholeRosterPath, "")
			_, res := formOn(t, w.b, page, foxholeRosterPath, "").send(w.b, nil)
			assertRedirect(t, res, foxholePath)
			w.awaitActionEnd(t)
			return kept.send(w.b, nil)
		}},
	{name: "a remove Confirm with nobody left to remove the role from", route: "POST " + foxholeRemovePath, status: http.StatusConflict, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			page := foxholePath + "?" + url.Values{paramRemove: {"external"}, fieldMember: {memberKestrel.ID}}.Encode()
			form := formOn(t, w.b, page, foxholeRemovePath, "")
			w.discord.editMember(memberKestrel.ID, func(m *commands.ListedMember) {
				m.RoleIDs = slices.DeleteFunc(m.RoleIDs, func(id string) bool { return id == roleExternal })
			})
			return form.send(w.b, nil)
		}},
	{name: "a re-add with nobody left to re-add", route: "POST " + foxholeReAddPath, status: http.StatusConflict, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			seedApproved(t, w, namesOf(memberDoe), foxholerGone)
			form := formOn(t, w.b, foxholePath, foxholeReAddPath, "")
			pressReAdd(t, w.b)
			w.awaitActionEnd(t)
			return form.send(w.b, nil)
		}},
	{name: "a Retry Confirm with nothing left to retry", route: "POST " + foxholeRetryPath, status: http.StatusConflict, shown: http.StatusOK,
		world: foxholeAnswerWorld,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())
			startPurge(t, w, "external")
			w.awaitActionEnd(t)
			w.discord.failRoleWrites(memberKestrel.ID, nil)
			form := formOn(t, w.b, attrOf(retryLink(reportBlock(t, parseHTML(t, w.b.get(foxholePath)))), "href"), foxholeRetryPath, "")
			w.discord.editMember(memberKestrel.ID, func(m *commands.ListedMember) {
				m.RoleIDs = slices.DeleteFunc(m.RoleIDs, func(id string) bool { return id == roleExternal })
			})
			return form.send(w.b, nil)
		}},
	{name: "a create with no channel name", route: "POST /hubs", status: http.StatusUnprocessableEntity, shown: http.StatusOK,
		send: func(t *testing.T, w *testWorld) (string, *http.Response) {
			return formOn(t, w.b, "/", "/hubs", "create").send(w.b, func(v url.Values) {
				v.Set(fieldCategory, "cat-1")
				v.Set(fieldBaseString, "Squad Voice")
			})
		}},
}

// signedOutPosts are the routes that take a POST signed out. Each answers
// with a redirect. Only a failure of the system's random source makes the
// sign-in start render a page, which no test can bring about.
var signedOutPosts = []string{"POST /auth/start", "POST /auth/signout"}

// gatelessPosts are the routes behind no group check: a recording's delete
// is open to every signed-in visitor and refused per recording, so its own
// rows give its no-access page.
var gatelessPosts = []string{"POST " + recordingsPath + "/{id}/delete"}

// redirectOnlyPosts are the routes behind a gate whose own answers are all
// redirects, or plain text for a form that can't be read, so the gate's
// pages are the only pages they answer with.
var redirectOnlyPosts = []string{"POST " + foxholeStopPath}

// postRoutes is the pattern of every route the panel serves that takes a
// POST.
func postRoutes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, rt := range newTestWorld(t).p.routes() {
		if strings.HasPrefix(rt.pattern, http.MethodPost+" ") {
			out = append(out, rt.pattern)
		}
	}
	return out
}

// gateAnswers is, for each route that takes a POST signed in, the page its
// gate answers with: the no-access page to an outsider, behind a group
// check, and the error page of a forum that doesn't answer the group check.
func gateAnswers(t *testing.T) []formAnswer {
	t.Helper()
	var out []formAnswer
	for _, route := range postRoutes(t) {
		if slices.Contains(signedOutPosts, route) {
			continue
		}
		path := strings.ReplaceAll(strings.TrimPrefix(route, http.MethodPost+" "), "{id}", "1")
		if !slices.Contains(gatelessPosts, route) {
			out = append(out, formAnswer{name: "from an outsider", route: route, status: http.StatusForbidden, shown: http.StatusForbidden,
				send: func(t *testing.T, w *testWorld) (string, *http.Response) {
					signInAs(t, w.forum, w.b, addOutsider(w.forum))
					return path, w.b.postForm(path, nil)
				}})
		}
		out = append(out, formAnswer{name: "while the forum doesn't answer", route: route, status: http.StatusBadGateway, shown: http.StatusOK,
			send: func(t *testing.T, w *testWorld) (string, *http.Response) {
				w.forum.setUserinfoDrop(true)
				return path, w.b.postForm(path, nil)
			},
			clear: func(_ *testing.T, w *testWorld) { w.forum.setUserinfoDrop(false) }})
	}
	return out
}

// Every page the panel renders in answer to a form names a GET address
// that shows it, which the page script puts in the tab, so a reload, a
// Back or a Forward never sends the form again (ADR 0019). The answer keeps
// its status. A GET of the address shows the page as it stands then: at the
// block the answer opened at and with no refusal note, or for a failure
// page, the page its Try again opens once the failure has cleared.
// Every route that takes a POST has rows of its own here, so a form added
// later fails until its answers are walked, whatever gate it sits behind.
func TestEveryFormAnswerNamesAnAddressThatShowsItsPage(t *testing.T) {
	for _, route := range postRoutes(t) {
		if !slices.ContainsFunc(formAnswers, func(a formAnswer) bool { return a.route == route }) &&
			!slices.Contains(signedOutPosts, route) && !slices.Contains(redirectOnlyPosts, route) {
			t.Errorf("%s has no form answer of its own", route)
		}
	}
	for _, tc := range append(slices.Clone(formAnswers), gateAnswers(t)...) {
		t.Run(tc.route+" "+tc.name, func(t *testing.T) {
			world := tc.world
			if world == nil {
				world = newAnswerWorld
			}
			w := world(t)

			posted, res := tc.send(t, w)

			if res.StatusCode != tc.status {
				t.Fatalf("the answer's status = %d, want %d", res.StatusCode, tc.status)
			}
			answered, doc := landedPageOf(t, res, fragmentOf(t, posted))
			address := namedAddress(t, doc)
			if len(address) >= proxyRequestLine {
				t.Errorf("the address is %d bytes long, want it under the proxy's %d-byte request line", len(address), proxyRequestLine)
			}
			if tc.clear != nil {
				tc.clear(t, w)
			}
			tab := fork(w.b)
			tab.replaceState(address)
			loaded, fragment := arrive(t, tab, address, tab.reload())
			got, gotDoc := landedPageOf(t, loaded, fragment)

			want := answered
			want.status = tc.shown
			if tc.pasteInTab {
				want.heading = ""
			}
			if retry := findElement(doc, "a", "data-retry", ""); retry != nil {
				href := attrOf(retry, "href")
				again, againFragment := arrive(t, w.b, href, w.b.get(href))
				want, _ = landedPageOf(t, again, againFragment)
			}
			if got != want {
				t.Errorf("a GET of the address %s shows %+v, want %+v", address, got, want)
			}
			if kinds := refusalKinds(gotDoc); !slices.Equal(kinds, tc.refusals) {
				t.Errorf("a GET of the address %s gives the refusals %v, want %v", address, kinds, tc.refusals)
			}
		})
	}
}

// fragmentOf is the fragment of an address, empty for none.
func fragmentOf(t *testing.T, address string) string {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatalf("address %q: %v", address, err)
	}
	return u.Fragment
}
