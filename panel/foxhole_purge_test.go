package panel

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/bwmarrin/discordgo"
	"golang.org/x/net/html"
)

// precedes reports whether a comes before b in the document's order.
func precedes(doc, a, b *html.Node) bool {
	order := map[*html.Node]int{}
	eachElement(doc, func(n *html.Node) { order[n] = len(order) })
	return order[a] < order[b]
}

// purgeForm returns the After a war block's purge form, and fails the test
// when the page has none.
func purgeForm(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	block := findElement(doc, "", "data-field", "after-a-war")
	if block == nil {
		t.Fatal("the page has no After a war block")
	}
	form := findElement(block, "form", "data-field", "purge")
	if form == nil {
		t.Fatal("the After a war block has no purge form")
	}
	return form
}

// The After a war block sits above the holder list, so a long list never
// pushes it off screen, and its purge form offers the three scopes: both
// roles, Internal only and External only.
func TestAfterAWarBlockSitsAboveTheHolderListAndOffersEachPurgeScope(t *testing.T) {
	w := newFoxholeWorld(t)

	doc := parseHTML(t, w.b.get(foxholePath))

	form := purgeForm(t, doc)
	if !precedes(doc, form, findElement(doc, "", "data-field", "holders")) {
		t.Error("the purge form comes after the holder list, want it above")
	}
	var scopes []string
	eachElement(form, func(n *html.Node) {
		if name, _ := attrValue(n, "name"); n.Data == "input" && name == paramPurge {
			value, _ := attrValue(n, "value")
			scopes = append(scopes, value)
		}
	})
	slices.Sort(scopes)
	if want := []string{"both", "external", "internal"}; !slices.Equal(scopes, want) {
		t.Errorf("the purge form offers the scopes %v, want %v", scopes, want)
	}
}

// openPurge submits the page's purge form with the scope picked, the way a
// browser submits a GET form, and returns the page it opens.
func openPurge(t *testing.T, b *browser, doc *html.Node, scope string) *http.Response {
	t.Helper()
	form := purgeForm(t, doc)
	action, _ := attrValue(form, "action")
	return b.get(action + "?" + url.Values{paramPurge: {scope}}.Encode())
}

// purgeConfirmation returns the page's purge confirmation, and fails the
// test when the page has none.
func purgeConfirmation(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	confirm := findElement(doc, "", "data-field", "purge-confirm")
	if confirm == nil {
		t.Fatal("the page has no purge confirmation")
	}
	return confirm
}

// purgeEntries returns the change log's purge entries.
func purgeEntries(t *testing.T, doc *html.Node) []*html.Node {
	t.Helper()
	section := findElement(doc, "", "data-field", "changes")
	if section == nil {
		t.Fatal("the page has no change log")
	}
	var out []*html.Node
	for _, el := range entryElements(section) {
		if action, _ := attrValue(el, "data-action"); action == "purge" {
			out = append(out, el)
		}
	}
	return out
}

// The purge confirmation states how many holders lose each role the scope
// names, that notes and approvals stay, and the rough time at one change a
// second. Opening it starts nothing.
func TestPurgeConfirmationCountsTheHoldersOfEachRoleAndTheTime(t *testing.T) {
	cases := []struct {
		scope   string
		holders map[string]int
	}{
		{"both", map[string]int{"internal": 3, "external": 2}},
		{"internal", map[string]int{"internal": 3}},
		{"external", map[string]int{"external": 2}},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			w := newFoxholeWorld(t)

			doc := parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), tc.scope))

			confirm := purgeConfirmation(t, doc)
			got := map[string]int{}
			eachElement(confirm, func(n *html.Node) {
				if role, ok := attrValue(n, "data-purge-role"); ok {
					got[role], _ = strconv.Atoi(fieldText(t, n, "holders"))
				}
			})
			if !maps.Equal(got, tc.holders) {
				t.Errorf("the confirmation counts the holders %v, want %v", got, tc.holders)
			}
			if findElement(confirm, "", "data-field", "keeps") == nil {
				t.Error("the confirmation doesn't say notes and approvals stay")
			}
			estimate := findElement(confirm, "", "data-field", "estimate")
			if estimate == nil {
				t.Fatal("the confirmation gives no estimate")
			}
			changes := 0
			for _, n := range tc.holders {
				changes += n
			}
			want := time.Duration(changes) * changeEstimate
			if got, _ := attrValue(estimate, "data-seconds"); got != strconv.Itoa(int(want/time.Second)) {
				t.Errorf("the estimate is %s s, want %d s", got, int(want/time.Second))
			}
			if entries := purgeEntries(t, doc); len(entries) != 0 {
				t.Errorf("opening the confirmation left %d purge entries in the change log, want none", len(entries))
			}
		})
	}
}

// endWatch is the store with a signal each time a Foxhole action's report
// is written as ended, so a test knows an action has run to its end with
// no sleep. An end write that fails signals nothing.
type endWatch struct {
	store.Store
	ended chan struct{}
}

func (s endWatch) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	err := s.Store.EndFoxholeReport(ctx, id, diff)
	if err == nil {
		s.ended <- struct{}{}
	}
	return err
}

// errStoreBlip is a store call that failed once and would go through
// again, as on a dropped database connection.
var errStoreBlip = errors.New("store: connection reset")

// failingEnd is the store with its first report end write failing.
type failingEnd struct {
	store.Store
	mu     sync.Mutex
	failed bool
}

func (s *failingEnd) EndFoxholeReport(ctx context.Context, id int64, diff json.RawMessage) error {
	s.mu.Lock()
	first := !s.failed
	s.failed = true
	s.mu.Unlock()
	if first {
		return errStoreBlip
	}
	return s.Store.EndFoxholeReport(ctx, id, diff)
}

// awaitActionEnd waits for the running Foxhole action's report to be
// written as ended.
func (w *testWorld) awaitActionEnd(t *testing.T) {
	t.Helper()
	select {
	case <-w.actionEnded:
	case <-time.After(hangLimit):
		t.Fatal("the Foxhole action never ended")
	}
}

// roleHold holds each member role change the fake makes inside the write,
// after Discord has made it and before it answers, until the test lets it
// answer.
type roleHold struct {
	entered chan fakeRoleWrite
	release chan struct{}
	free    chan struct{}
	once    sync.Once
}

// holdRoleWrites holds every member role change from now on. The test's end
// lets every held change go.
func holdRoleWrites(t *testing.T, w *testWorld) *roleHold {
	t.Helper()
	h := &roleHold{entered: make(chan fakeRoleWrite, 64), release: make(chan struct{}), free: make(chan struct{})}
	w.discord.setDuringRoleWrite(func(write fakeRoleWrite) {
		h.entered <- write
		select {
		case <-h.release:
		case <-h.free:
		}
	})
	t.Cleanup(h.open)
	return h
}

// next waits for the next role change to reach the fake, and returns it
// held.
func (h *roleHold) next(t *testing.T) fakeRoleWrite {
	t.Helper()
	select {
	case write := <-h.entered:
		return write
	case <-time.After(hangLimit):
		t.Fatal("no role change reached Discord")
		return fakeRoleWrite{}
	}
}

// pass lets the held role change answer.
func (h *roleHold) pass(t *testing.T) {
	t.Helper()
	select {
	case h.release <- struct{}{}:
	case <-time.After(hangLimit):
		t.Fatal("no role change is held")
	}
}

// open lets the held role change and every one after it answer.
func (h *roleHold) open() {
	h.once.Do(func() { close(h.free) })
}

// confirmPurge posts the purge confirmation's form, the way a browser does,
// with the request's context ctx.
func confirmPurge(t *testing.T, b *browser, ctx context.Context, confirm *html.Node) *http.Response {
	t.Helper()
	form := findElement(confirm, "form", "", "")
	if form == nil {
		t.Fatal("the purge confirmation has no form")
	}
	return submitContext(t, b, ctx, form)
}

// reportBlock returns the report at the top of the page, and fails the
// test when the page shows none.
func reportBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	report := findElement(doc, "", "data-field", "report")
	if report == nil {
		t.Fatal("the page shows no report")
	}
	return report
}

// outcomeOf is a report's outcome code.
func outcomeOf(report *html.Node) string {
	outcome, _ := attrValue(report, "data-outcome")
	return outcome
}

// Confirming a purge starts it in the background: the POST redirects to
// the Foxhole page while the purge's first role change is still held inside
// Discord. The browser then goes away, and the purge still runs to its end
// and reports it.
func TestConfirmedPurgeRunsInTheBackgroundPastTheRequest(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "both")))
	ctx, leave := context.WithCancel(context.Background())
	answered := make(chan *http.Response, 1)

	go func() { answered <- confirmPurge(t, w.b, ctx, confirm) }()

	hold.next(t)
	select {
	case res := <-answered:
		assertRedirect(t, res, foxholePath)
	case <-time.After(hangLimit):
		t.Fatal("the confirm is still waiting while the purge's first role change is held")
	}
	leave()
	hold.open()
	w.awaitActionEnd(t)
	if got := outcomeOf(reportBlock(t, parseHTML(t, w.b.get(foxholePath)))); got != "done" {
		t.Errorf("the report's outcome is %q, want done", got)
	}
}

// A purge starts only with a complete member list, so its report names
// every holder it took a role from. Confirmed while the list is partial,
// it is refused: the page says nothing changed, and no report starts.
func TestPurgeConfirmedWhileTheMemberListIsPartialIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "both")))
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListArriving, Connected: true, PartsReceived: 3, PartsExpected: 10})

	doc := parseHTML(t, confirmPurge(t, w.b, context.Background(), confirm))

	if findLive(doc, "", "data-error", "member-list") == nil {
		t.Error("the page doesn't say the purge was refused for the member list")
	}
	if entries := purgeEntries(t, doc); len(entries) != 0 {
		t.Errorf("the change log holds %d purge entries, want none", len(entries))
	}
}

// startPurge opens the purge confirmation for the scope and confirms it, as
// a manager does, and fails the test unless the purge starts.
func startPurge(t *testing.T, w *testWorld, scope string) {
	t.Helper()
	confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), scope)))
	assertRedirect(t, confirmPurge(t, w.b, context.Background(), confirm), foxholePath)
}

// namedIn returns the IDs of the members an element names, sorted.
func namedIn(n *html.Node) []string {
	ids := valuesOf(n, "data-member")
	slices.Sort(ids)
	return slices.Compact(ids)
}

// A purge of both roles takes External off every holder before it takes
// Internal off anyone, so one that stops partway leaves the Cav members'
// role half done, never the collaborators'. It works from the holder list
// as it stood when it started: a member given Internal by hand in Discord
// while it runs keeps it, and the report doesn't name them.
func TestPurgeOfBothTakesExternalFirstFromTheHoldersAtItsStart(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "both")

	checked := false
	for i := range 5 {
		write := hold.next(t)
		if write.RoleID == roleInternal && !checked {
			checked = true
			if holders := w.discord.holdersOf(roleExternal); len(holders) != 0 {
				t.Errorf("at the purge's first Internal removal %v still hold External, want nobody", holders)
			}
		}
		if i == 0 {
			w.discord.editMember(memberVance.ID, func(m *commands.ListedMember) { m.RoleIDs = append(m.RoleIDs, roleInternal) })
		}
		hold.pass(t)
	}
	w.awaitActionEnd(t)

	if !checked {
		t.Fatal("the purge took Internal off nobody")
	}
	if got, want := w.discord.holdersOf(roleInternal), []string{memberVance.ID}; !slices.Equal(got, want) {
		t.Errorf("after the purge %v hold Internal, want only %v, granted by hand while it ran", got, want)
	}
	if named := namedIn(reportBlock(t, parseHTML(t, w.b.get(foxholePath)))); slices.Contains(named, memberVance.ID) {
		t.Errorf("the report names %v, want it not to name %s, who held no role when it started", named, memberVance.ID)
	}
}

// reportList returns one of a report's lists, by its data-list name, and
// fails the test when the report has none.
func reportList(t *testing.T, report *html.Node, name string) *html.Node {
	t.Helper()
	list := findElement(report, "", "data-list", name)
	if list == nil {
		t.Fatalf("the report has no %s list", name)
	}
	return list
}

// listedReasons returns the members a report list names, each with the
// reason code it gives.
func listedReasons(list *html.Node) map[string]string {
	out := map[string]string{}
	eachElement(list, func(n *html.Node) {
		if id, ok := attrValue(n, "data-member"); ok {
			out[id], _ = attrValue(n, "data-reason")
		}
	})
	return out
}

// A purge checks each member against the member list just before it
// changes them. A holder who lost the role by hand since it started, and
// one who left the server, are each skipped with that reason and never
// sent to Discord.
func TestPurgeSkipsAMemberWhoChangedSinceItStartedWithTheReason(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")

	first := hold.next(t)
	var rest []string
	for _, id := range []string{memberDoe.ID, memberAsh.ID, memberMarsh.ID} {
		if id != first.MemberID {
			rest = append(rest, id)
		}
	}
	dropped, left := rest[0], rest[1]
	w.discord.editMember(dropped, func(m *commands.ListedMember) {
		m.RoleIDs = slices.DeleteFunc(m.RoleIDs, func(id string) bool { return id == roleInternal })
	})
	w.discord.dropMember(left)
	hold.open()
	w.awaitActionEnd(t)

	skipped := listedReasons(reportList(t, reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "skipped"))
	if want := map[string]string{dropped: "not-holding", left: "left"}; !maps.Equal(skipped, want) {
		t.Errorf("the report skipped %v, want %v", skipped, want)
	}
	for _, write := range w.discord.roleChanges() {
		if write.MemberID == dropped || write.MemberID == left {
			t.Errorf("the purge sent Discord a change for %s, whom it should have skipped", write.MemberID)
		}
	}
}

// Every role change a purge makes carries the audit log reason the spec
// fixes, naming the purge and the forum user who started it, so a Discord
// moderator knows who did it without opening the panel.
func TestPurgeRoleChangesNameThePurgeAndTheForumUserWhoStartedIt(t *testing.T) {
	w := newFoxholeWorld(t)

	startPurge(t, w, "both")
	w.awaitActionEnd(t)

	writes := w.discord.roleChanges()
	if len(writes) == 0 {
		t.Fatal("the purge changed no role")
	}
	want := "Panel: Foxhole purge by " + managerUsername + " (forum user " + strconv.Itoa(managerUserID) + ")"
	for _, write := range writes {
		if write.Reason != want {
			t.Errorf("the change to %s carries the reason %q, want %q", write.MemberID, write.Reason, want)
		}
	}
}

// progressBlock returns the running action's progress block, and fails the
// test when the page shows none.
func progressBlock(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	progress := findElement(doc, "", "data-field", "progress")
	if progress == nil {
		t.Fatal("the page shows no progress block")
	}
	return progress
}

// While a purge runs, the page shows its progress block: the action, who
// started it, how many of its members are done and about how long is left
// at one change a second. It shows the state at page load, and a reload
// updates it.
func TestProgressBlockCountsTheMembersDoneAndTheTimeLeft(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "both")
	const total = 5 // 2 External holders and 3 Internal holders
	hold.next(t)
	hold.pass(t)
	hold.next(t)

	progress := progressBlock(t, parseHTML(t, w.b.get(foxholePath)))

	if action, _ := attrValue(progress, "data-action"); action != "purge" {
		t.Errorf("the progress block is for action %q, want purge", action)
	}
	if got := fieldText(t, progress, "started-by"); got != managerUsername {
		t.Errorf("the progress block names %q as its starter, want %q", got, managerUsername)
	}
	if done, all := fieldText(t, progress, "done"), fieldText(t, progress, "total"); done != "1" || all != strconv.Itoa(total) {
		t.Errorf("the progress block says %s of %s done, want 1 of %d", done, all, total)
	}
	left := findElement(progress, "", "data-field", "left")
	if left == nil {
		t.Fatal("the progress block gives no time left")
	}
	if got, want := attrOf(left, "data-seconds"), strconv.Itoa(int(time.Duration(total-1)*changeEstimate/time.Second)); got != want {
		t.Errorf("the progress block gives %s s left, want %s s", got, want)
	}
	hold.pass(t)
	hold.next(t)
	if done := fieldText(t, progressBlock(t, parseHTML(t, w.b.get(foxholePath))), "done"); done != "2" {
		t.Errorf("after one more change a reload says %s done, want 2", done)
	}
}

// attrOf is an attribute's value, empty when the element lacks it.
func attrOf(n *html.Node, key string) string {
	v, _ := attrValue(n, key)
	return v
}

// missingPermissionsBody is Discord's answer to a role change the bot lacks
// the permission for, as the production adapter hands it back inside a
// *discordgo.RESTError.
const missingPermissionsBody = `{"message": "Missing Permissions", "code": 50013}`

// missingPermissions is that answer as discordgo returns it.
func missingPermissions() error {
	return &discordgo.RESTError{
		Response:     &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden"},
		ResponseBody: []byte(missingPermissionsBody),
		Message:      &discordgo.APIErrorMessage{Code: discordgo.ErrCodeMissingPermissions, Message: "Missing Permissions"},
	}
}

// pairs returns the member and role each entry of a report list names, as
// "<member ID> <role>", sorted.
func pairs(list *html.Node) []string {
	var out []string
	eachElement(list, func(n *html.Node) {
		if id, ok := attrValue(n, "data-member"); ok {
			out = append(out, id+" "+attrOf(n, "data-role"))
		}
	})
	slices.Sort(out)
	return out
}

// A finished purge's report gives who started it, when it started and
// ended, and its outcome, then four lists, each with a count: the members
// changed, with each role taken; those skipped; those Discord refused, with
// its reason in plain words and never its raw answer; and, last, those
// never attempted.
func TestPurgeReportListsWhatItChangedFailedAndNeverAttempted(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.failRoleWrites(memberKestrel.ID, missingPermissions())

	startPurge(t, w, "both")
	w.awaitActionEnd(t)

	report := reportBlock(t, parseHTML(t, w.b.get(foxholePath)))
	if got := fieldText(t, report, "started-by"); got != managerUsername {
		t.Errorf("the report names %q as its starter, want %q", got, managerUsername)
	}
	for _, field := range []string{"started", "ended"} {
		if el := findElement(report, "time", "data-field", field); el == nil || attrOf(el, "datetime") == "" {
			t.Errorf("the report gives no %s time", field)
		}
	}
	if got := outcomeOf(report); got != "done" {
		t.Errorf("the report's outcome is %q, want done", got)
	}
	var lists []string
	eachElement(report, func(n *html.Node) {
		if name, ok := attrValue(n, "data-list"); ok {
			lists = append(lists, name)
			if findElement(n, "", "data-field", "count") == nil {
				t.Errorf("the %s list gives no count", name)
			}
		}
	})
	if want := []string{"changed", "failed", "not-attempted", "skipped"}; !slices.Equal(slices.Sorted(slices.Values(lists)), want) {
		t.Errorf("the report has the lists %v, want %v", lists, want)
	} else if lists[len(lists)-1] != "not-attempted" {
		t.Errorf("the report's lists come in the order %v, want not attempted last", lists)
	}
	changed := []string{
		memberDoe.ID + " internal", memberAsh.ID + " internal",
		memberMarsh.ID + " internal", memberMarsh.ID + " external",
	}
	slices.Sort(changed)
	if got := pairs(reportList(t, report, "changed")); !slices.Equal(got, changed) {
		t.Errorf("the report changed %v, want %v", got, changed)
	}
	failed := reportList(t, report, "failed")
	if got, want := pairs(failed), []string{memberKestrel.ID + " external"}; !slices.Equal(got, want) {
		t.Errorf("the report failed on %v, want %v", got, want)
	}
	reason := strings.TrimSpace(fieldText(t, failed, "reason"))
	if reason == "" || strings.Contains(reason, missingPermissionsBody) {
		t.Errorf("the failure's reason reads %q, want Discord's reason in plain words", reason)
	}
	if got := fieldText(t, reportList(t, report, "not-attempted"), "count"); got != "0" {
		t.Errorf("the report counts %s not attempted, want 0", got)
	}
}

// A purge's report is its entry in the Foxhole page's change log: the entry
// is there, marked running, from the start, still marked running once the
// purge has filled in a member, and the same entry is marked done when it
// ends, naming the members it changed. The purge has that one entry.
func TestPurgeReportIsItsChangeLogEntryFromStartToEnd(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	hold.next(t)
	hold.pass(t)
	hold.next(t)

	page := parseHTML(t, w.b.get(foxholePath))

	entries := purgeEntries(t, page)
	if len(entries) != 1 {
		t.Fatalf("the change log holds %d purge entries while the purge runs, want 1", len(entries))
	}
	id := attrOf(entries[0], "data-entry")
	if newest := entryElements(findElement(page, "", "data-field", "changes"))[0]; attrOf(newest, "data-entry") != id {
		t.Errorf("the change log's newest entry is %s, want the purge's, %s", attrOf(newest, "data-entry"), id)
	}
	if got := outcomeOf(entries[0]); got != "running" {
		t.Errorf("the purge's entry is marked %q while it runs, want running", got)
	}
	hold.open()
	w.awaitActionEnd(t)
	entries = purgeEntries(t, parseHTML(t, w.b.get(foxholePath)))
	if len(entries) != 1 || attrOf(entries[0], "data-entry") != id {
		t.Fatalf("after the purge the change log holds purge entries %v, want the one entry %s", valuesOfEach(entries, "data-entry"), id)
	}
	if got := outcomeOf(entries[0]); got != "done" {
		t.Errorf("the purge's entry is marked %q after it ends, want done", got)
	}
	want := []string{memberDoe.ID, memberAsh.ID, memberMarsh.ID}
	slices.Sort(want)
	if got := namedIn(reportList(t, entries[0], "changed")); !slices.Equal(got, want) {
		t.Errorf("the purge's entry names %v as changed, want %v", got, want)
	}
}

// valuesOfEach returns the attribute's value on each element.
func valuesOfEach(nodes []*html.Node, attr string) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, attrOf(n, attr))
	}
	return out
}

// The last report stays at the top of the Foxhole page for every manager,
// through later saves, until the next action replaces it, so whoever comes
// next sees how the last action went.
func TestLastReportShowsToEveryManagerUntilTheNextPurge(t *testing.T) {
	w := newFoxholeWorld(t)
	startPurge(t, w, "external")
	w.awaitActionEnd(t)
	first := attrOf(reportBlock(t, parseHTML(t, w.b.get(foxholePath))), "data-report")
	other := newBrowser(t, w.p)
	signInAs(t, w.forum, other, w.forum.addUser(1357, "Jones.K", 2, []int{testFoxholeGroupID}))
	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)

	if got := attrOf(reportBlock(t, parseHTML(t, other.get(foxholePath))), "data-report"); got != first {
		t.Errorf("another manager, after a note save, sees report %s, want the last purge's, %s", got, first)
	}
	startPurge(t, w, "internal")
	w.awaitActionEnd(t)
	if got := attrOf(reportBlock(t, parseHTML(t, other.get(foxholePath))), "data-report"); got == first {
		t.Errorf("after a second purge another manager still sees report %s, want the second purge's", got)
	}
}

// While the member list is partial the page shows a notice in the holder
// list's place, and still shows the last report, or a running action's
// progress block, which read no member list.
func TestReportAndProgressShowWhileTheMemberListIsPartial(t *testing.T) {
	partial := commands.MemberListSnapshot{Status: commands.MemberListArriving, Connected: true, PartsReceived: 3, PartsExpected: 10}
	cases := []struct {
		name  string
		field string
		run   func(t *testing.T, w *testWorld)
	}{
		{"the last report", "report", func(t *testing.T, w *testWorld) {
			startPurge(t, w, "external")
			w.awaitActionEnd(t)
		}},
		{"a running purge's progress block", "progress", func(t *testing.T, w *testWorld) {
			hold := holdRoleWrites(t, w)
			startPurge(t, w, "external")
			hold.next(t)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			w.p.pageBudget = partialListBudget
			tc.run(t, w)
			w.discord.setMemberList(partial)

			doc := parseHTML(t, w.b.get(foxholePath))

			memberListNotice(t, doc)
			if findElement(doc, "", "data-field", tc.field) == nil {
				t.Errorf("the page doesn't show %s beside the member list notice", tc.name)
			}
			if findElement(doc, "", "data-field", "holders") != nil {
				t.Error("the page shows a holder list from a partial member list")
			}
		})
	}
}

// disabled reports whether a control carries the disabled attribute.
func disabled(n *html.Node) bool {
	_, ok := attrValue(n, "disabled")
	return ok
}

// One Foxhole action runs at a time. While a purge runs, the page disables
// the controls that would start another, and the server refuses one posted
// anyway, by a browser with no script: the page says so and no second
// purge starts.
func TestSecondPurgeWhileOneRunsIsRefused(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	hold.next(t)

	page := parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "external"))

	if button := findElement(purgeForm(t, page), "button", "", ""); button == nil || !disabled(button) {
		t.Error("the purge form's button is enabled while a purge runs")
	}
	confirm := purgeConfirmation(t, page)
	if button := findElement(confirm, "", "data-field", "confirm"); button == nil || !disabled(button) {
		t.Error("the purge confirmation's button is enabled while a purge runs")
	}
	refused := parseHTML(t, confirmPurge(t, w.b, context.Background(), confirm))
	if findLive(refused, "", "data-error", "action-running") == nil {
		t.Error("the page doesn't say the second purge was refused for the one running")
	}
	hold.open()
	w.awaitActionEnd(t)
	if entries := purgeEntries(t, parseHTML(t, w.b.get(foxholePath))); len(entries) != 1 {
		t.Errorf("the change log holds %d purge entries, want the running purge's alone", len(entries))
	}
}

// Saves that change no Discord role stay open while a Foxhole action runs,
// so a long purge doesn't block a manager's bookkeeping: a note save and an
// Approve each land while a purge is held mid-run.
func TestNoteAndApproveSaveWhileAPurgeRuns(t *testing.T) {
	w := newFoxholeWorld(t)
	hold := holdRoleWrites(t, w)
	startPurge(t, w, "internal")
	hold.next(t)

	assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberKestrel.ID), "allied group lead"), foxholePath)
	approveThroughThePage(t, w, memberKestrel.ID)

	records := readNoteState(t, w.st).Records
	if len(records) != 1 || records[0].MemberID != memberKestrel.ID || records[0].Note != "allied group lead" || !records[0].Approved {
		t.Errorf("records = %+v, want %s's note saved and their approval", records, memberKestrel.ID)
	}
}

// A panel admin takes every Foxhole action, purge included, so one can step
// in when no Foxhole manager is around.
func TestPanelAdminWhoIsNoFoxholeManagerPurges(t *testing.T) {
	w := newFoxholeWorld(t)
	admin := newBrowser(t, w.p)
	signInAs(t, w.forum, admin, w.forum.addUser(1357, "Admin.A", 2, []int{47}))
	confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, admin, parseHTML(t, admin.get(foxholePath)), "external")))

	assertRedirect(t, confirmPurge(t, admin, context.Background(), confirm), foxholePath)

	w.awaitActionEnd(t)
	if got := outcomeOf(reportBlock(t, parseHTML(t, admin.get(foxholePath)))); got != "done" {
		t.Errorf("the panel admin's purge ended %q, want done", got)
	}
	if holders := w.discord.holdersOf(roleExternal); len(holders) != 0 {
		t.Errorf("after the panel admin's purge %v still hold External, want nobody", holders)
	}
}

// A Foxhole action refreshes the stored last-seen names of a member with a
// record whose names changed, so a member it takes a role from who leaves
// later shows under the names the panel saw last. Nothing loads the page in
// between, so the refresh is the action's.
func TestPurgeRefreshesTheStoredNamesOfAMemberWithARecord(t *testing.T) {
	w := newFoxholeWorld(t)
	entry := store.ChangeLogEntry{ForumUserID: managerUserID, ForumUsername: managerUsername, Action: store.ChangeNote, Diff: json.RawMessage(`{}`)}
	save := store.NoteSave{MemberID: memberDoe.ID, Note: "discharged 12 Sep", DisplayName: "PVT Doe.J", Username: "jdoe_old"}
	if err := w.st.SaveFoxholeNote(context.Background(), testGuildID, save, entry); err != nil {
		t.Fatalf("seed note: %v", err)
	}

	assertRedirect(t, w.b.postForm(foxholePurgePath, url.Values{fieldScope: {"internal"}}), foxholePath)
	w.awaitActionEnd(t)

	want := []store.FoxholeRecord{{MemberID: memberDoe.ID, Note: "discharged 12 Sep", DisplayName: "SGT Doe.J", Username: "jdoe"}}
	if got := readNoteState(t, w.st).Records; !reflect.DeepEqual(got, want) {
		t.Errorf("records after the purge = %+v, want %+v", got, want)
	}
}

// A purge whose report fails to be written as ended, on one store blip,
// still ends: the page shows its report done, never a progress block that
// stays for good.
func TestPurgeReportEndsThroughAStoreBlip(t *testing.T) {
	w := newFoxholeWorldWith(t, func(st store.Store) store.Store { return &failingEnd{Store: st} })

	startPurge(t, w, "external")
	w.awaitActionEnd(t)

	if got := outcomeOf(reportBlock(t, parseHTML(t, w.b.get(foxholePath)))); got != "done" {
		t.Errorf("the report's outcome is %q, want done", got)
	}
}

// reportedExtras returns the value each recorded Sentry event carries under
// key in its extra context, for the events that carry one.
func reportedExtras(t *testing.T, rec *sentryRecorder, key string) []string {
	t.Helper()
	var out []string
	for _, raw := range rec.sent() {
		var event struct {
			Contexts struct {
				Extra map[string]any `json:"extra"`
			} `json:"contexts"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode a recorded event: %v", err)
		}
		if v, ok := event.Contexts.Extra[key].(string); ok {
			out = append(out, v)
		}
	}
	return out
}

// A purge needs each role it names to be in the server by the name the
// commands use. One that isn't is a fault on fixed input (ADR 0002), so
// confirming the purge changes nothing, the page says so, no report
// starts, and the fault reaches Sentry naming the role.
func TestPurgeOfARoleMissingFromTheServerIsRefusedAndReported(t *testing.T) {
	w := newFoxholeWorld(t)
	reported := recordSentry(t)
	confirm := purgeConfirmation(t, parseHTML(t, openPurge(t, w.b, parseHTML(t, w.b.get(foxholePath)), "internal")))
	w.discord.removeRole(roleInternal)

	doc := parseHTML(t, confirmPurge(t, w.b, context.Background(), confirm))

	if findLive(doc, "", "data-error", "role-missing") == nil {
		t.Error("the page doesn't say the purge was refused for a missing role")
	}
	if entries := purgeEntries(t, doc); len(entries) != 0 {
		t.Errorf("the change log holds %d purge entries, want none", len(entries))
	}
	internalName, _ := commands.FoxholeRoleNames()
	if roles := reportedExtras(t, reported, "role"); !slices.Contains(roles, internalName) {
		t.Errorf("Sentry got events naming the roles %v, want one naming %q", roles, internalName)
	}
}
