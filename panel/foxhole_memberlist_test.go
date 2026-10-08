package panel

import (
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// The Foxhole page while the member list is partial (#445, spec #434 "While
// the member list is partial"). Each snapshot a test sets is one the
// production adapter returns: no members until the list is complete, and
// for a guild Discord hasn't sent, the guild's data the gateway state holds
// beside it.

// fixtureList is the complete member list of the fixture's members.
func fixtureList() commands.MemberListSnapshot {
	return commands.MemberListSnapshot{Status: commands.MemberListComplete, Connected: true,
		Members: []commands.ListedMember{memberDoe, memberAsh, memberKestrel, memberMarsh, memberVance}}
}

// Just after a deploy the member list is on its way. A page load waits for
// it within its time budget, and shows the holder list once the list
// completes: from the READY placeholder, from a list arriving in parts, and
// from a refusal whose retry falls inside the budget.
func TestFoxholePageWaitsForAMemberListOnItsWay(t *testing.T) {
	cases := []struct {
		name  string
		guild commands.GuildDataStatus
		list  func(w *testWorld) commands.MemberListSnapshot
	}{
		{"READY placeholder", commands.GuildDataArriving, func(*testWorld) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListNoGuild, Connected: true}
		}},
		{"arriving", commands.GuildDataPresent, func(*testWorld) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListArriving, Connected: true, PartsReceived: 3, PartsExpected: 10}
		}},
		{"refused, retry inside the budget", commands.GuildDataPresent, func(w *testWorld) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListRefused, Connected: true, RetryAt: w.clock.Now().Add(hubPageBudget / 2)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			w.discord.setGuild(tc.guild)
			w.discord.completeAfterOneRead(tc.list(w), fixtureList())

			res := w.b.get(foxholePath)

			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 once the list completes", res.StatusCode)
			}
			if _, ok := holderRows(t, parseHTML(t, res))[memberDoe.ID]; !ok {
				t.Errorf("the holder list has no row for %s, want the list shown once it completes", memberDoe.ID)
			}
		})
	}
}

// A list that can't complete within the page's time budget gets no wait:
// the page answers at once with the notice in the list's place. Discord
// refused the bot's request and the bot asks again only after the budget
// would end, whether the list reads refused or, past the late mark, late,
// which keeps its late notice (#495); or an outage's GUILD_DELETE took the
// guild out of the gateway state, and the member list with it, which the
// hub page doesn't wait for either.
func TestFoxholePageAnswersAtOnceForAListThatCantCompleteInTime(t *testing.T) {
	cases := []struct {
		name  string
		guild commands.GuildDataStatus
		list  func(at time.Time) commands.MemberListSnapshot
		// status is the notice's list status the case checks, empty for a
		// case that checks only that the notice is there.
		status string
	}{
		{"refused, retry past the budget", commands.GuildDataPresent, func(at time.Time) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListRefused, Connected: true, RetryAt: at.Add(2 * hubPageBudget)}
		}, ""},
		{"late, refusal retrying past the budget", commands.GuildDataPresent, func(at time.Time) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListLate, Connected: true, RetryAt: at.Add(2 * hubPageBudget)}
		}, "late"},
		{"guild absent", commands.GuildDataAbsent, func(time.Time) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListNoGuild, Connected: true}
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			w.discord.setGuild(tc.guild)
			w.discord.setMemberList(tc.list(w.clock.Now()))

			start := w.clock.Now()
			res := w.b.get(foxholePath)
			waited := w.clock.since(start)

			if res.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", res.StatusCode)
			}
			notice := memberListNotice(t, parseHTML(t, res))
			if got, _ := attrValue(notice, "data-list-status"); tc.status != "" && got != tc.status {
				t.Errorf("the notice's list status = %q, want %q", got, tc.status)
			}
			if waited > hubPageBudget/5 {
				t.Errorf("answered after waiting %v, want well before the %v budget", waited, hubPageBudget)
			}
		})
	}
}

// memberListNotice returns the notice in the holder list's place, and fails
// the test when the page has none.
func memberListNotice(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	notice := findElement(doc, "", "data-notice", "member-list")
	if notice == nil {
		t.Fatal("the page has no member list notice")
	}
	return notice
}

// When the wait ends with the list still partial, one notice in the holder
// list's place says how far along the list is: arriving, with the parts
// counted once the first part says how many; refused, with the seconds to
// the bot's next request; late; or no guild, Discord's data not sent. Each
// carries a Reload link. The page shows no holder list and still shows the
// change log, which reads no member list: both regression pins, true before
// the notice said which state the list is in.
func TestFoxholePageNamesTheMemberListStateInItsNotice(t *testing.T) {
	const retryIn = 25 * time.Second
	cases := []struct {
		name   string
		guild  commands.GuildDataStatus
		list   func(at time.Time) commands.MemberListSnapshot
		status string
		// parts are the received and expected counts the notice shows,
		// nil for a notice that shows none.
		parts []string
	}{
		{"arriving, no part yet", commands.GuildDataPresent, func(time.Time) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListArriving, Connected: true}
		}, "arriving", nil},
		{"arriving, 3 of 10 parts", commands.GuildDataPresent, func(time.Time) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListArriving, Connected: true, PartsReceived: 3, PartsExpected: 10}
		}, "arriving", []string{"3", "10"}},
		{"refused", commands.GuildDataPresent, func(at time.Time) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListRefused, Connected: true, RetryAt: at.Add(retryIn)}
		}, "refused", nil},
		{"late", commands.GuildDataPresent, func(time.Time) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListLate, Connected: true, PartsReceived: 3, PartsExpected: 10}
		}, "late", nil},
		{"no guild", commands.GuildDataArriving, func(time.Time) commands.MemberListSnapshot {
			return commands.MemberListSnapshot{Status: commands.MemberListNoGuild, Connected: true}
		}, "no-guild", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			assertRedirect(t, submitNote(t, w.b, openNote(t, w.b, memberDoe.ID), "discharged 12 Sep"), foxholePath)
			w.discord.setGuild(tc.guild)
			w.discord.setMemberList(tc.list(w.clock.Now()))

			res := w.b.get(foxholePath)

			if res.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", res.StatusCode)
			}
			doc := parseHTML(t, res)
			notice := memberListNotice(t, doc)
			if got, _ := attrValue(notice, "data-list-status"); got != tc.status {
				t.Errorf("the notice's list status = %q, want %q", got, tc.status)
			}
			if tc.parts == nil {
				if hasField(notice, "parts_received") || hasField(notice, "parts_expected") {
					t.Error("the notice counts parts, want no count")
				}
			} else if got := []string{fieldText(t, notice, "parts_received"), fieldText(t, notice, "parts_expected")}; !slices.Equal(got, tc.parts) {
				t.Errorf("the notice counts %v parts, want %v", got, tc.parts)
			}
			if tc.status == "refused" {
				if got := fieldText(t, notice, "retry_in"); got != "25" {
					t.Errorf("the notice says the retry is in %q s, want 25", got)
				}
			}
			if !hasField(notice, "reload") {
				t.Error("the notice has no Reload link")
			}
			if findElement(doc, "", "data-field", "holders") != nil {
				t.Error("the page shows a holder list")
			}
			if entries := foxholeChangeLog(t, doc); len(entries) != 1 {
				t.Errorf("the change log shows %d entries, want the note save's", len(entries))
			}
		})
	}
}

// The notice's Reload link loads the page again in the view it shows, the
// search and the filter kept, so a manager without script who reloads once
// the list arrives sees what they asked for. Two members match the search:
// one holding Internal, the filter, and one holding External alone.
func TestMemberListNoticeReloadLinkKeepsTheView(t *testing.T) {
	w := newFoxholeWorld(t)
	marshall := commands.ListedMember{ID: "100000000000000007", Username: "marshall_k", RoleIDs: []string{roleExternal}}
	w.discord.setMemberList(arrivingList)
	notice := memberListNotice(t, parseHTML(t, w.b.get("/foxhole?q=marsh&filter=internal")))
	reload := findElement(notice, "a", "data-field", "reload")
	if reload == nil {
		t.Fatal("the notice has no Reload link")
	}
	href, _ := attrValue(reload, "href")
	setMembers(w, memberDoe, memberAsh, memberKestrel, memberMarsh, memberVance, marshall)

	rows := holderRows(t, parseHTML(t, w.b.get(href)))

	if got, want := keys(rows), []string{memberMarsh.ID}; !slices.Equal(got, want) {
		t.Errorf("the Reload link %s lists %v, want %v, the Internal holders matching the search", href, got, want)
	}
}

// While the bot's gateway connection is down the member list holds the
// members as they were when it dropped. A complete list still shows in
// full, with a short notice that it may be out of date, and once the
// connection is back the notice goes. The full holder list is a regression
// pin: the page showed it while disconnected before the notice.
func TestFoxholePageWithTheConnectionDownSaysTheHolderListMayBeOutOfDate(t *testing.T) {
	w := newFoxholeWorld(t)
	down := fixtureList()
	down.Connected = false
	w.discord.setMemberList(down)

	doc := parseHTML(t, w.b.get(foxholePath))

	if findElement(doc, "", "data-notice", "disconnected") == nil {
		t.Error("the page has no disconnected notice while the connection is down")
	}
	if _, ok := holderRows(t, doc)[memberDoe.ID]; !ok {
		t.Errorf("the holder list has no row for %s, want the list in full", memberDoe.ID)
	}
	w.discord.setMemberList(fixtureList())

	if findElement(parseHTML(t, w.b.get(foxholePath)), "", "data-notice", "disconnected") != nil {
		t.Error("the page still has the disconnected notice with the connection back up")
	}
}

// A guild Discord hasn't sent is Discord's delay, which nobody on call can
// fix: the page load that shows its notice leaves an INFO line naming the
// manager, as the hub page's does, and no Sentry event. No Sentry event is
// a regression pin: the page sent none before the line.
func TestFoxholePageWithNoGuildDataLogsItAndSendsNoSentryEvent(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.setGuild(commands.GuildDataArriving)
	w.discord.setMemberList(commands.MemberListSnapshot{Status: commands.MemberListNoGuild, Connected: true})
	logs := captureLogs(t)
	reported := recordSentry(t)

	memberListNotice(t, parseHTML(t, w.b.get(foxholePath)))

	var lines int
	for _, r := range logs() {
		if _, ok := r["step"]; ok && r["level"] == "INFO" && r["username"] == managerUsername && r["forum_user_id"] == strconv.Itoa(managerUserID) {
			lines++
		}
	}
	if lines == 0 {
		t.Error("the page load left no INFO line with a step naming the manager")
	}
	if n := len(reported.recorded()); n != 0 {
		t.Errorf("sent %d Sentry events, want none", n)
	}
}
