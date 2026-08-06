package utils

import (
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// loaPost builds a forum post body in the canonical yellow-label template that
// parseLOAPost expects: a Username line (label, optional colon, value on the same
// line) followed by Start Date / End Date labels each with their value on the NEXT
// line. Callers pass raw date strings so malformed-date cases can be exercised.
func loaPost(username, start, end string) string {
	return "[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: " + username + "\n" +
		"[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B]\n" + start + "\n" +
		"[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B]\n" + end + "\n"
}

// mustLOATime parses a date in the canonical LOA layout ("Jan 2, 2006") for
// fixed-instant boundary tests; it panics on a malformed literal in test code.
func mustLOATime(s string) time.Time {
	t, err := time.Parse(loaDateLayout, s)
	if err != nil {
		panic("mustLOATime: " + err.Error())
	}
	return t
}

// TestParseLOAPost locks in the CURRENT contract of the brittle BBCode regexes.
// Each case asserts actual observed behavior of the existing regexes (verified by
// probe), not aspirational behavior — so this catches Xenforo template drift.
func TestParseLOAPost(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		wantOK    bool
		wantUser  string
		wantStart string // "2006-01-02"; checked only when wantOK
		wantEnd   string
	}{
		{
			name:      "well-formed yellow-label template",
			msg:       loaPost("TestUser", "Jan 1, 2099", "Jan 31, 2099"),
			wantOK:    true,
			wantUser:  "TestUser",
			wantStart: "2099-01-01",
			wantEnd:   "2099-01-31",
		},
		{
			name:      "rgb without spaces still matches",
			msg:       "[B][COLOR=rgb(213,185,0)]Username[/COLOR][/B]: Nospace\n[B][COLOR=rgb(213,185,0)]Start Date[/COLOR][/B]\nFeb 2, 2099\n[B][COLOR=rgb(213,185,0)]End Date[/COLOR][/B]\nFeb 9, 2099\n",
			wantOK:    true,
			wantUser:  "Nospace",
			wantStart: "2099-02-02",
			wantEnd:   "2099-02-09",
		},
		{
			name:      "username without colon separator",
			msg:       "[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B] NoColon\n[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B]\nMar 1, 2099\n[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B]\nMar 5, 2099\n",
			wantOK:    true,
			wantUser:  "NoColon",
			wantStart: "2099-03-01",
			wantEnd:   "2099-03-05",
		},
		{
			name:      "CRLF line endings still parse",
			msg:       "[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: CrlfUser\r\n[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B]\r\nApr 1, 2099\r\n[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B]\r\nApr 4, 2099\r\n",
			wantOK:    true,
			wantUser:  "CrlfUser",
			wantStart: "2099-04-01",
			wantEnd:   "2099-04-04",
		},
		{
			name:      "username with space captures only first token (\\S+ stops at space)",
			msg:       loaPost("First Last", "May 1, 2099", "May 9, 2099"),
			wantOK:    true,
			wantUser:  "First",
			wantStart: "2099-05-01",
			wantEnd:   "2099-05-09",
		},
		{
			name:   "missing Username field",
			msg:    "[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B]\nJan 1, 2099\n[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B]\nJan 31, 2099\n",
			wantOK: false,
		},
		{
			name:   "missing Start Date field",
			msg:    "[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: NoStart\n[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B]\nJan 31, 2099\n",
			wantOK: false,
		},
		{
			name:   "missing End Date field",
			msg:    "[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: NoEnd\n[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B]\nJan 1, 2099\n",
			wantOK: false,
		},
		{
			name:   "malformed start date (ISO format not accepted)",
			msg:    loaPost("BadStart", "2099-01-01", "Jan 31, 2099"),
			wantOK: false,
		},
		{
			name:   "malformed end date (ISO format not accepted)",
			msg:    loaPost("BadEnd", "Jan 1, 2099", "2099-01-31"),
			wantOK: false,
		},
		{
			name:   "legacy variant: date inline with label on same line does not parse",
			msg:    "[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: Inline\n[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B] Jan 1, 2099\n[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B] Jan 31, 2099\n",
			wantOK: false,
		},
		{
			// Behavior change (issue: cross-month/non-canonical LOAs silently dropped):
			// the yellow-color wrapper is no longer required. A well-formed post in any
			// (or no) label color now parses — formatting BBCode is stripped before matching.
			name:      "non-yellow label color still parses (color no longer required)",
			msg:       "[B][COLOR=rgb(0, 0, 0)]Username[/COLOR][/B]: Black\n[B][COLOR=rgb(0, 0, 0)]Start Date[/COLOR][/B]\nJan 1, 2099\n[B][COLOR=rgb(0, 0, 0)]End Date[/COLOR][/B]\nJan 31, 2099\n",
			wantOK:    true,
			wantUser:  "Black",
			wantStart: "2099-01-01",
			wantEnd:   "2099-01-31",
		},
		{
			// Real mirror variant (thread 87170): bold labels, NO color wrapper — the
			// single most common cause of historical silent failures.
			name:      "bold label without color wrapper parses",
			msg:       "[B]Username[/B]: Alpine.A\n[B]Start Date[/B]\nDec 6, 2025\n[B]End Date[/B]\nDec 22, 2025\n",
			wantOK:    true,
			wantUser:  "Alpine.A",
			wantStart: "2025-12-06",
			wantEnd:   "2025-12-22",
		},
		{
			// Real mirror variant (thread 83158): no formatting BBCode at all.
			name:      "plain labels with no formatting parse",
			msg:       "Username: Videnovic.Y\nStart Date\nOct 4, 2025\nEnd Date\nOct 17, 2025\n",
			wantOK:    true,
			wantUser:  "Videnovic.Y",
			wantStart: "2025-10-04",
			wantEnd:   "2025-10-17",
		},
		{
			// Real mirror variant (thread 87307): the date value is wrapped in [SIZE].
			name:      "size-wrapped date value parses",
			msg:       "[B]Username[/B]: Lake.W\n[B][SIZE=4]Start Date[/SIZE][/B]\n[SIZE=4]Jan 3, 2026[/SIZE]\n[B][SIZE=4]End Date[/SIZE][/B]\n[SIZE=4]Jan 16, 2026[/SIZE]\n",
			wantOK:    true,
			wantUser:  "Lake.W",
			wantStart: "2026-01-03",
			wantEnd:   "2026-01-16",
		},
		{
			// Real mirror variant (thread 93288): full month name instead of abbreviation.
			name:      "full month name parses",
			msg:       loaPost("Lawrie.A", "April 1, 2026", "April 23, 2026"),
			wantOK:    true,
			wantUser:  "Lawrie.A",
			wantStart: "2026-04-01",
			wantEnd:   "2026-04-23",
		},
		{
			// Real mirror variant (thread 94302): day with no comma before the year.
			name:      "date with no comma parses",
			msg:       loaPost("Siervo.W", "Apr 12 2026", "Apr 19 2026"),
			wantOK:    true,
			wantUser:  "Siervo.W",
			wantStart: "2026-04-12",
			wantEnd:   "2026-04-19",
		},
		{
			// Real mirror shape (thread 100403): an on-behalf PAF carries TWO
			// Username labels — the SUBMITTER first (auto-filled from the filing
			// account), then the SUBJECT (the trooper actually on leave) inside
			// the Rank/Username/Primary Billet block. The LOA belongs to the
			// Subject, so the LAST label wins, not the first. This is the #221
			// regression: the old first-match parser attributed the LOA to the
			// submitter. See docs/adr/0010-loa-subject-attribution.md.
			name: "on-behalf PAF: subject is the last Username label, not the submitter",
			msg: "[COLOR=rgb(213, 185, 0)][B]Username:[/B] [/COLOR]Filer.S\n\n" +
				"[B][COLOR=rgb(213, 185, 0)]Rank[/COLOR][/B]: SPC\n\n" +
				"[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: Leaver.T\n\n" +
				"[B][COLOR=rgb(213, 185, 0)]Primary Billet[/COLOR][/B]: A/ACD\n\n" +
				"[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B] \nJul 10, 2099\n" +
				"[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B] \nJul 24, 2099\n\n" +
				"[HR][/HR]\n[B][COLOR=rgb(213, 185, 0)]Submitter Name[/COLOR][/B] \nSSG.Filer.S\n",
			wantOK:    true,
			wantUser:  "Leaver.T",
			wantStart: "2099-07-10",
			wantEnd:   "2099-07-24",
		},
		{
			// Real mirror shape (thread 38835): an older PAF collapses Rank,
			// Username, and Billet onto ONE line, so the Username label is not at
			// the start of its line. The parser must still extract the Subject —
			// this pins the decision NOT to anchor the label to line-start (which
			// would silently drop this real LOA). See ADR 0010 Consequences.
			name: "collapsed single-line Rank/Username/Billet still parses the subject",
			msg: "[B]Rank[/B]: PVT [B]Username[/B]: Collapsed.C   [B]Billet[/B]: 2/B/ACD\n" +
				"Reason:\nDeployment\n\nStart Date\nJan 1, 2099\nEnd Date\nJan 8, 2099\n",
			wantOK:    true,
			wantUser:  "Collapsed.C",
			wantStart: "2099-01-01",
			wantEnd:   "2099-01-08",
		},
		{
			// Self PAF with the same two-label shape (submitter == subject): the
			// last-label rule must still resolve to that one person, so self-LOAs
			// stay correct after the on-behalf fix.
			name: "self PAF: duplicate Username labels resolve to the same subject",
			msg: "[COLOR=rgb(213, 185, 0)][B]Username:[/B] [/COLOR]Same.S\n\n" +
				"[B][COLOR=rgb(213, 185, 0)]Rank[/COLOR][/B]: SPC\n\n" +
				"[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: Same.S\n\n" +
				"[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B] \nAug 1, 2099\n" +
				"[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B] \nAug 8, 2099\n",
			wantOK:    true,
			wantUser:  "Same.S",
			wantStart: "2099-08-01",
			wantEnd:   "2099-08-08",
		},
		{
			// Free-text date (thread 95564, "probably May 8, 2026") must still be rejected —
			// leniency covers template variation, not unparseable prose.
			name:   "free-text uncertain date is still rejected",
			msg:    loaPost("Robinson.G", "May 3, 2026", "probably May 8, 2026"),
			wantOK: false,
		},
		{
			name:   "completely unrelated post content",
			msg:    "Hey everyone, just wanted to say thanks for the great op last night o7",
			wantOK: false,
		},
		{
			name:   "empty body",
			msg:    "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLOAPost(tt.msg)
			if ok != tt.wantOK {
				t.Fatalf("parseLOAPost ok = %v, want %v (entry=%+v)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.Username != tt.wantUser {
				t.Errorf("Username = %q, want %q", got.Username, tt.wantUser)
			}
			if gotStart := got.StartDate.Format("2006-01-02"); gotStart != tt.wantStart {
				t.Errorf("StartDate = %q, want %q", gotStart, tt.wantStart)
			}
			if gotEnd := got.EndDate.Format("2006-01-02"); gotEnd != tt.wantEnd {
				t.Errorf("EndDate = %q, want %q", gotEnd, tt.wantEnd)
			}
		})
	}
}

// fakeLOAFetcher is a loaPostFetcher test double. It records the `since` cursor it
// was called with per node and returns canned posts (or a canned error) per node,
// so the refresh/prune core can be exercised without MySQL.
type fakeLOAFetcher struct {
	byNode    map[int][]loaPostRow
	errByNode map[int]error
	gotSince  map[int]int64
	calls     int
}

func (f *fakeLOAFetcher) fetchLOAPosts(nodeID int, since int64) ([]loaPostRow, error) {
	if f.gotSince == nil {
		f.gotSince = map[int]int64{}
	}
	f.gotSince[nodeID] = since
	f.calls++
	if err := f.errByNode[nodeID]; err != nil {
		return nil, err
	}
	return f.byNode[nodeID], nil
}

func TestRefresh_IncrementalAdvance(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}

	// First refresh: cold cache → since should be ~one year ago (non-zero), and
	// lastSyncedPostDate should advance to the max post_date observed.
	firstPost := time.Now().Add(-48 * time.Hour).Unix()
	f := &fakeLOAFetcher{byNode: map[int][]loaPostRow{
		180: {{message: loaPost("alpha", "Jan 1, 2099", "Jan 31, 2099"), postDate: firstPost, threadID: 1}},
	}}
	c.refresh(f, []int{180})

	if c.lastSyncedPostDate != firstPost {
		t.Fatalf("after first refresh lastSyncedPostDate = %d, want %d", c.lastSyncedPostDate, firstPost)
	}
	yearAgo := time.Now().AddDate(-1, 0, 0).Unix()
	if got := f.gotSince[180]; got > yearAgo+5 || got < yearAgo-5 {
		t.Fatalf("cold-cache since = %d, want ~%d (one year ago)", got, yearAgo)
	}
	if _, ok := c.GetEntry("alpha"); !ok {
		t.Fatalf("expected alpha entry present after first refresh")
	}

	// Second refresh: warm cache → since must equal the prior high-water mark, and a
	// newer post advances the cursor again.
	secondPost := time.Now().Add(-1 * time.Hour).Unix()
	f2 := &fakeLOAFetcher{byNode: map[int][]loaPostRow{
		180: {{message: loaPost("bravo", "Jan 1, 2099", "Jan 31, 2099"), postDate: secondPost, threadID: 2}},
	}}
	c.refresh(f2, []int{180})

	if f2.gotSince[180] != firstPost {
		t.Fatalf("warm-cache since = %d, want prior high-water %d", f2.gotSince[180], firstPost)
	}
	if c.lastSyncedPostDate != secondPost {
		t.Fatalf("after second refresh lastSyncedPostDate = %d, want %d", c.lastSyncedPostDate, secondPost)
	}
}

// TestRefresh_RetainsEndedWindows replaces the old prune-on-expiry test: an ended
// (recently) window is now RETAINED as history, and only a window past the
// retention horizon is dropped. A still-active window survives regardless.
func TestRefresh_RetainsEndedWindows(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}
	// recentlyEnded: EndDate in the past but well within the retention horizon.
	c.entries["recentlyended"] = []LOAEntry{{Username: "recentlyEnded", StartDate: time.Now().Add(-72 * time.Hour), EndDate: time.Now().Add(-24 * time.Hour), ThreadID: 1}}
	// retired: EndDate older than the retention horizon (~1y).
	c.entries["retired"] = []LOAEntry{{Username: "retired", StartDate: time.Now().AddDate(-1, 0, -10), EndDate: time.Now().AddDate(-1, 0, -5), ThreadID: 2}}
	// active: currently within its window.
	c.entries["active"] = []LOAEntry{{Username: "active", StartDate: time.Now().Add(-24 * time.Hour), EndDate: time.Now().Add(24 * time.Hour), ThreadID: 3}}
	c.lastSyncedPostDate = 100 // warm cache so since is deterministic

	f := &fakeLOAFetcher{byNode: map[int][]loaPostRow{180: nil}}
	c.refresh(f, []int{180})

	if got := c.GetEntries("recentlyended"); len(got) != 1 {
		t.Errorf("recently-ended window must be RETAINED as history, got %d windows", len(got))
	}
	if got := c.GetEntries("retired"); len(got) != 0 {
		t.Errorf("window past the retention horizon must be dropped, got %d windows", len(got))
	}
	if got := c.GetEntries("active"); len(got) != 1 {
		t.Errorf("active window must survive, got %d windows", len(got))
	}
}

// TestRefresh_MultiWindowPerUser proves a username can hold multiple simultaneous
// windows and that refresh appends rather than overwrites.
func TestRefresh_MultiWindowPerUser(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}

	f1 := &fakeLOAFetcher{byNode: map[int][]loaPostRow{
		180: {{message: loaPost("echo", "Jan 1, 2099", "Jan 31, 2099"), postDate: time.Now().Add(-48 * time.Hour).Unix(), threadID: 100}},
	}}
	c.refresh(f1, []int{180})

	f2 := &fakeLOAFetcher{byNode: map[int][]loaPostRow{
		180: {{message: loaPost("echo", "Mar 1, 2099", "Mar 31, 2099"), postDate: time.Now().Add(-1 * time.Hour).Unix(), threadID: 200}},
	}}
	c.refresh(f2, []int{180})

	got := c.GetEntries("echo")
	if len(got) != 2 {
		t.Fatalf("expected 2 retained windows for echo, got %d (%+v)", len(got), got)
	}
	threads := map[int64]bool{}
	for _, w := range got {
		threads[w.ThreadID] = true
	}
	if !threads[100] || !threads[200] {
		t.Errorf("expected both thread 100 and 200 retained, got %+v", threads)
	}
}

// TestRefresh_DedupesByThreadID proves re-scanning the same thread (cold-restart
// backfill or overlapping fetch) updates the window in place instead of
// duplicating it.
func TestRefresh_DedupesByThreadID(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}

	post := loaPostRow{message: loaPost("foxtrot", "Jan 1, 2099", "Jan 31, 2099"), postDate: time.Now().Add(-48 * time.Hour).Unix(), threadID: 777}
	f1 := &fakeLOAFetcher{byNode: map[int][]loaPostRow{180: {post}}}
	c.refresh(f1, []int{180})

	// Re-scan the SAME thread with an edited window (same threadID, later end).
	post2 := loaPostRow{message: loaPost("foxtrot", "Jan 1, 2099", "Feb 15, 2099"), postDate: time.Now().Add(-1 * time.Hour).Unix(), threadID: 777}
	f2 := &fakeLOAFetcher{byNode: map[int][]loaPostRow{180: {post2}}}
	c.refresh(f2, []int{180})

	got := c.GetEntries("foxtrot")
	if len(got) != 1 {
		t.Fatalf("re-scanning thread 777 must not duplicate; got %d windows (%+v)", len(got), got)
	}
	if got[0].EndDate.Format("2006-01-02") != "2099-02-15" {
		t.Errorf("dedup must update in place: EndDate = %s, want 2099-02-15", got[0].EndDate.Format("2006-01-02"))
	}
}

// TestGetEntries_EmptyWhenNone pins the empty-slice (not nil-panicking) contract
// for an unknown username.
func TestGetEntries_EmptyWhenNone(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}
	got := c.GetEntries("ghost")
	if got == nil {
		t.Fatalf("GetEntries must return a non-nil empty slice for unknown user")
	}
	if len(got) != 0 {
		t.Errorf("GetEntries for unknown user = %d windows, want 0", len(got))
	}
}

// TestGetEntry_MostRelevantWindow pins the most-relevant selection used by the
// legacy GetEntry contract (active > upcoming > most-recently-ended) so /awol's
// [[LOA]] link and /loa's active/upcoming split keep rendering identically with
// multiple windows present.
func TestGetEntry_MostRelevantWindow(t *testing.T) {
	now := time.Now()
	c := &LOACache{entries: map[string][]LOAEntry{}}

	// User with an ended, an active, and an upcoming window — active must win.
	c.entries["mix"] = []LOAEntry{
		{Username: "mix", StartDate: now.AddDate(0, 0, -20), EndDate: now.AddDate(0, 0, -10), ThreadID: 1}, // ended
		{Username: "mix", StartDate: now.AddDate(0, 0, -2), EndDate: now.AddDate(0, 0, 2), ThreadID: 2},    // active
		{Username: "mix", StartDate: now.AddDate(0, 0, 10), EndDate: now.AddDate(0, 0, 20), ThreadID: 3},   // upcoming
	}
	got, ok := c.GetEntry("mix")
	if !ok || got.ThreadID != 2 {
		t.Errorf("GetEntry must surface the active window (thread 2), got %+v ok=%v", got, ok)
	}

	// User with only ended + upcoming — upcoming (soonest start) must win.
	c.entries["future"] = []LOAEntry{
		{Username: "future", StartDate: now.AddDate(0, 0, -20), EndDate: now.AddDate(0, 0, -10), ThreadID: 4}, // ended
		{Username: "future", StartDate: now.AddDate(0, 0, 30), EndDate: now.AddDate(0, 0, 40), ThreadID: 5},   // far upcoming
		{Username: "future", StartDate: now.AddDate(0, 0, 5), EndDate: now.AddDate(0, 0, 8), ThreadID: 6},     // soon upcoming
	}
	got, ok = c.GetEntry("future")
	if !ok || got.ThreadID != 6 {
		t.Errorf("GetEntry must surface the soonest upcoming window (thread 6), got %+v ok=%v", got, ok)
	}

	// User with only ended windows — most recently ended (latest EndDate) must win.
	c.entries["past"] = []LOAEntry{
		{Username: "past", StartDate: now.AddDate(0, 0, -40), EndDate: now.AddDate(0, 0, -30), ThreadID: 7},
		{Username: "past", StartDate: now.AddDate(0, 0, -15), EndDate: now.AddDate(0, 0, -5), ThreadID: 8},
	}
	got, ok = c.GetEntry("past")
	if !ok || got.ThreadID != 8 {
		t.Errorf("GetEntry must surface the most recently-ended window (thread 8), got %+v ok=%v", got, ok)
	}

	if _, ok := c.GetEntry("nobody"); ok {
		t.Errorf("GetEntry for unknown user must be false")
	}
}

func TestRefresh_NoNewPosts_IsNoOp(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}
	c.entries["keep"] = []LOAEntry{{Username: "keep", StartDate: time.Now().Add(-1 * time.Hour), EndDate: time.Now().Add(72 * time.Hour)}}
	c.lastSyncedPostDate = 555

	// Node returns no rows; the cursor must not move and the existing entry stays.
	f := &fakeLOAFetcher{byNode: map[int][]loaPostRow{180: {}}}
	c.refresh(f, []int{180})

	if c.lastSyncedPostDate != 555 {
		t.Errorf("lastSyncedPostDate moved to %d on no-op, want 555", c.lastSyncedPostDate)
	}
	if _, ok := c.GetEntry("keep"); !ok {
		t.Errorf("active entry must remain after no-op refresh")
	}
	if f.gotSince[180] != 555 {
		t.Errorf("since = %d, want warm-cache cursor 555", f.gotSince[180])
	}
}

func TestRefresh_FetcherError_DoesNotAdvanceCursor(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}
	c.lastSyncedPostDate = 42
	f := &fakeLOAFetcher{errByNode: map[int]error{180: errors.New("boom")}}

	c.refresh(f, []int{180})

	if c.lastSyncedPostDate != 42 {
		t.Errorf("cursor advanced on fetch error: %d, want 42", c.lastSyncedPostDate)
	}
	if !c.lastSuccessfulRefresh.IsZero() {
		t.Errorf("failed-only refresh must not record success")
	}
}

func TestGetEntry(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}
	c.entries["onloa"] = []LOAEntry{{Username: "OnLOA", StartDate: time.Now().Add(-1 * time.Hour), EndDate: time.Now().Add(1 * time.Hour)}}

	// GetEntry is case-insensitive on the lookup key.
	if _, ok := c.GetEntry("ONLOA"); !ok {
		t.Errorf("GetEntry should be case-insensitive")
	}
	if _, ok := c.GetEntry("missing"); ok {
		t.Errorf("GetEntry for absent user should be false")
	}
}

// TestLOAEntry_isActiveAt pins the inclusive active-window contract at the EXACT
// boundary instants. isActiveAt is clock-injected (cf. PR #135) precisely so the
// Start==now and End==now edges can be asserted against a fixed `now` — something
// a live time.Now() could never hit deterministically. The window is inclusive at
// both bounds. utils.ActiveWindow (which /awol uses for selection) delegates here.
func TestLOAEntry_isActiveAt(t *testing.T) {
	now := mustLOATime("Jun 15, 2099")
	entry := LOAEntry{
		Username:  "Boundary",
		StartDate: mustLOATime("Jun 10, 2099"),
		EndDate:   mustLOATime("Jun 20, 2099"),
	}

	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"Start==now is active (inclusive lower bound)", entry.StartDate, true},
		{"End==now is active (inclusive upper bound)", entry.EndDate, true},
		{"strictly inside the window is active", now, true},
		{"one tick before Start is not active", entry.StartDate.Add(-time.Nanosecond), false},
		{"one tick after End is not active", entry.EndDate.Add(time.Nanosecond), false},
		{"wholly before window is not active", entry.StartDate.AddDate(0, 0, -5), false},
		{"wholly after window is not active", entry.EndDate.AddDate(0, 0, 5), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := entry.isActiveAt(tt.now); got != tt.want {
				t.Errorf("isActiveAt(%s) = %v, want %v", tt.now.Format(loaDateLayout), got, tt.want)
			}
		})
	}
}

// TestGetEntry_TwoActiveWindows_LatestEndingWins pins the highest-value tie-break
// mutation testing flagged hollow: when a user holds TWO simultaneously-active
// windows, mostRelevant/GetEntry must surface the latest-ending one (state 3,
// tie=EndDate). This is the exact selection that drives /awol's [[LOA]] link, so a
// mutated `tie.After` → `tie.Before` must fail here.
func TestGetEntry_TwoActiveWindows_LatestEndingWins(t *testing.T) {
	now := time.Now()
	c := &LOACache{entries: map[string][]LOAEntry{}}

	// Both windows straddle now (active). Thread 2 ends later, so it must win.
	c.entries["dual"] = []LOAEntry{
		{Username: "dual", StartDate: now.AddDate(0, 0, -5), EndDate: now.AddDate(0, 0, 3), ThreadID: 1},
		{Username: "dual", StartDate: now.AddDate(0, 0, -2), EndDate: now.AddDate(0, 0, 9), ThreadID: 2},
	}
	got, ok := c.GetEntry("dual")
	if !ok || got.ThreadID != 2 {
		t.Fatalf("GetEntry must surface the latest-ending ACTIVE window (thread 2), got %+v ok=%v", got, ok)
	}

	// Order-independence: same windows, reversed slice order, same verdict.
	c.entries["dual"] = []LOAEntry{
		{Username: "dual", StartDate: now.AddDate(0, 0, -2), EndDate: now.AddDate(0, 0, 9), ThreadID: 2},
		{Username: "dual", StartDate: now.AddDate(0, 0, -5), EndDate: now.AddDate(0, 0, 3), ThreadID: 1},
	}
	got, ok = c.GetEntry("dual")
	if !ok || got.ThreadID != 2 {
		t.Fatalf("latest-ending active must win regardless of slice order, got %+v ok=%v", got, ok)
	}
}

// TestLOAEntry_isRetired_HorizonBoundary pins the retention cutoff at the EXACT
// historyHorizon boundary. isRetired uses a strict Before, so a window whose
// EndDate == historyHorizon(now) is RETAINED (not yet retired); one tick older is
// retired, one tick newer is retained. There is no other direct test for either
// isRetired or historyHorizon, so this guards a Before↔!After / ±tick mutation.
func TestLOAEntry_isRetired_HorizonBoundary(t *testing.T) {
	now := time.Now()
	horizon := historyHorizon(now)

	atHorizon := LOAEntry{EndDate: horizon}
	if atHorizon.isRetired(now) {
		t.Errorf("EndDate == historyHorizon must be RETAINED (strict Before), got retired")
	}
	oneTickNewer := LOAEntry{EndDate: horizon.Add(time.Nanosecond)}
	if oneTickNewer.isRetired(now) {
		t.Errorf("EndDate one tick after the horizon must be retained, got retired")
	}
	oneTickOlder := LOAEntry{EndDate: horizon.Add(-time.Nanosecond)}
	if !oneTickOlder.isRetired(now) {
		t.Errorf("EndDate one tick before the horizon must be retired, got retained")
	}
}

// TestRefresh_MixedWindowCompaction exercises the in-place kept := windows[:0]
// filter with a SINGLE user holding interleaved retired/retained windows — the
// real-world multi-window case the single-window tests never reach. The two
// survivors must remain, in their original relative order.
func TestRefresh_MixedWindowCompaction(t *testing.T) {
	now := time.Now()
	c := &LOACache{entries: map[string][]LOAEntry{}}

	// [retired, retained, retired, retained] for one user.
	c.entries["mixed"] = []LOAEntry{
		{Username: "mixed", StartDate: now.AddDate(-2, 0, 0), EndDate: now.AddDate(-1, 0, -30), ThreadID: 1}, // retired
		{Username: "mixed", StartDate: now.AddDate(0, 0, -40), EndDate: now.AddDate(0, 0, -30), ThreadID: 2}, // retained (recent)
		{Username: "mixed", StartDate: now.AddDate(-2, 0, 0), EndDate: now.AddDate(-1, 0, -20), ThreadID: 3}, // retired
		{Username: "mixed", StartDate: now.AddDate(0, 0, -10), EndDate: now.AddDate(0, 0, -2), ThreadID: 4},  // retained (recent)
	}
	c.lastSyncedPostDate = 100 // warm cache ⇒ no new posts, isolate the compaction

	f := &fakeLOAFetcher{byNode: map[int][]loaPostRow{180: nil}}
	c.refresh(f, []int{180})

	got := c.GetEntries("mixed")
	if len(got) != 2 {
		t.Fatalf("expected 2 surviving windows after compaction, got %d (%+v)", len(got), got)
	}
	if got[0].ThreadID != 2 || got[1].ThreadID != 4 {
		t.Errorf("survivors must be threads [2, 4] in order, got [%d, %d]", got[0].ThreadID, got[1].ThreadID)
	}
}

// TestGetEntry_SingleWindowContract_ForLoaRendering pins the S3 decision: /loa
// renders exactly one window per user via the single-value GetEntry contract and
// deliberately does NOT iterate GetEntries. Even when a user holds several
// concurrent UPCOMING windows, GetEntry must collapse them to one (the soonest
// start), so /loa shows a single row — matching the pre-history-store behavior.
// If a future change wires /loa onto GetEntries, this test should be revisited
// intentionally rather than silently.
func TestGetEntry_SingleWindowContract_ForLoaRendering(t *testing.T) {
	now := time.Now()
	c := &LOACache{entries: map[string][]LOAEntry{}}

	// Two concurrent upcoming windows for one user.
	c.entries["multi"] = []LOAEntry{
		{Username: "multi", StartDate: now.AddDate(0, 0, 20), EndDate: now.AddDate(0, 0, 30), ThreadID: 1},
		{Username: "multi", StartDate: now.AddDate(0, 0, 5), EndDate: now.AddDate(0, 0, 8), ThreadID: 2},
	}

	got, ok := c.GetEntry("multi")
	if !ok {
		t.Fatalf("GetEntry must return a window for a user with multiple windows")
	}
	if got.ThreadID != 2 {
		t.Errorf("single-window contract: GetEntry must surface the soonest-upcoming window (thread 2), got %d", got.ThreadID)
	}
	// The full history is still two windows — GetEntry is the lossy one-per-user view.
	if n := len(c.GetEntries("multi")); n != 2 {
		t.Errorf("GetEntries must still expose the full history (2), got %d", n)
	}
}

// TestGetEntries_CopyIsolation proves GetEntries returns a defensive copy: mutating
// a returned window must not leak back into the cache.
func TestGetEntries_CopyIsolation(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}
	c.entries["iso"] = []LOAEntry{
		{Username: "iso", StartDate: time.Now().Add(-time.Hour), EndDate: time.Now().Add(time.Hour), ThreadID: 42},
	}

	got := c.GetEntries("iso")
	if len(got) != 1 {
		t.Fatalf("expected 1 window, got %d", len(got))
	}
	got[0].ThreadID = 9999 // mutate the returned copy

	again := c.GetEntries("iso")
	if again[0].ThreadID != 42 {
		t.Errorf("mutating a GetEntries result must not alter the cache: ThreadID = %d, want 42", again[0].ThreadID)
	}
}

// TestUpsertWindow_ZeroThreadIDNotDeduped guards the S1 defensive path: a window
// with ThreadID 0 (impossible from the live non-null PK query, but a silent
// data-folding hazard if it ever occurred) must be appended rather than dedup-
// folded. Two distinct 0-thread windows for one user must both survive.
func TestUpsertWindow_ZeroThreadIDNotDeduped(t *testing.T) {
	now := time.Now()
	c := &LOACache{entries: map[string][]LOAEntry{}}

	c.upsertWindow(LOAEntry{Username: "Zed", StartDate: now.AddDate(0, 0, 1), EndDate: now.AddDate(0, 0, 5), ThreadID: 0})
	c.upsertWindow(LOAEntry{Username: "Zed", StartDate: now.AddDate(0, 0, 10), EndDate: now.AddDate(0, 0, 15), ThreadID: 0})

	got := c.GetEntries("zed")
	if len(got) != 2 {
		t.Fatalf("two zero-ThreadID windows must NOT fold into one; got %d (%+v)", len(got), got)
	}

	// A non-zero thread still dedupes in place alongside the un-folded zeros.
	c.upsertWindow(LOAEntry{Username: "Zed", StartDate: now.AddDate(0, 0, 20), EndDate: now.AddDate(0, 0, 25), ThreadID: 7})
	c.upsertWindow(LOAEntry{Username: "Zed", StartDate: now.AddDate(0, 0, 20), EndDate: now.AddDate(0, 0, 26), ThreadID: 7})
	got = c.GetEntries("zed")
	if len(got) != 3 {
		t.Fatalf("expected 3 windows (2 zero + 1 deduped thread 7), got %d (%+v)", len(got), got)
	}
}

// TestRefresh_RetentionBoundary cross-checks the retention step in refresh (which
// calls isRetired with time.Now()): a recently-ended window (EndDate within the
// retention horizon) survives a refresh cycle as history, while one whose EndDate
// predates the horizon is dropped. Replaces the former prune-on-expiry boundary
// test now that ended windows are retained.
func TestRefresh_RetentionBoundary(t *testing.T) {
	now := time.Now()

	c := &LOACache{entries: map[string][]LOAEntry{}}
	// survivor: ended yesterday — within the horizon, retained as history.
	c.entries["survivor"] = []LOAEntry{{Username: "Survivor", StartDate: now.AddDate(0, 0, -3), EndDate: now.AddDate(0, 0, -1), ThreadID: 1}}
	// retired: ended just past the retention horizon — dropped.
	c.entries["retired"] = []LOAEntry{{Username: "Retired", StartDate: historyHorizon(now).AddDate(0, 0, -2), EndDate: historyHorizon(now).AddDate(0, 0, -1), ThreadID: 2}}
	c.lastSyncedPostDate = 100 // warm cache ⇒ deterministic since, no new posts

	f := &fakeLOAFetcher{byNode: map[int][]loaPostRow{180: nil}}
	c.refresh(f, []int{180})

	if got := c.GetEntries("survivor"); len(got) != 1 {
		t.Errorf("recently-ended window must survive as history, got %d windows", len(got))
	}
	if got := c.GetEntries("retired"); len(got) != 0 {
		t.Errorf("window ended past the retention horizon must be dropped, got %d windows", len(got))
	}
}

// TestRefresh_AccumulatesDistinctThreads pins the multi-window history behavior
// that replaces the old "latest LOA wins" overwrite: a second valid post for the
// same username on a NEW thread is retained alongside the first (deduped only by
// ThreadID), and GetEntry still surfaces the single most-relevant window.
func TestRefresh_AccumulatesDistinctThreads(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}

	// First post: an LOA for "delta" with one window/thread.
	firstPost := time.Now().Add(-48 * time.Hour).Unix()
	f1 := &fakeLOAFetcher{byNode: map[int][]loaPostRow{
		180: {{message: loaPost("delta", "Jan 1, 2099", "Jan 31, 2099"), postDate: firstPost, threadID: 11}},
	}}
	c.refresh(f1, []int{180})

	if got := c.GetEntries("delta"); len(got) != 1 {
		t.Fatalf("expected 1 window after first refresh, got %d", len(got))
	}

	// Second post: a NEW LOA for the same username with a later window and a
	// different thread. Both windows must now be retained (no overwrite).
	secondPost := time.Now().Add(-1 * time.Hour).Unix()
	f2 := &fakeLOAFetcher{byNode: map[int][]loaPostRow{
		180: {{message: loaPost("delta", "Feb 1, 2099", "Feb 28, 2099"), postDate: secondPost, threadID: 22}},
	}}
	c.refresh(f2, []int{180})

	got := c.GetEntries("delta")
	if len(got) != 2 {
		t.Fatalf("expected 2 retained windows after second refresh, got %d (%+v)", len(got), got)
	}
	threads := map[int64]bool{}
	for _, w := range got {
		threads[w.ThreadID] = true
	}
	if !threads[11] || !threads[22] {
		t.Errorf("both threads 11 and 22 must be retained, got %+v", threads)
	}

	// Both windows are far-future ⇒ upcoming; GetEntry surfaces the soonest start.
	entry, ok := c.GetEntry("delta")
	if !ok || entry.ThreadID != 11 {
		t.Errorf("GetEntry must surface the soonest-upcoming window (thread 11), got %+v ok=%v", entry, ok)
	}
}

func TestIsHealthy(t *testing.T) {
	tests := []struct {
		name       string
		seed       time.Time // zero value means "never refreshed"
		maxAge     time.Duration
		wantOK     bool
		wantSeeded bool // true means returned timestamp must equal seed; false means must be zero
	}{
		{
			name:       "never refreshed returns false and zero time",
			seed:       time.Time{},
			maxAge:     30 * time.Minute,
			wantOK:     false,
			wantSeeded: false,
		},
		{
			name:       "just inside the threshold is healthy",
			seed:       time.Now().Add(-30*time.Minute + 5*time.Second),
			maxAge:     30 * time.Minute,
			wantOK:     true,
			wantSeeded: true,
		},
		{
			name:       "just outside the threshold is unhealthy",
			seed:       time.Now().Add(-30*time.Minute - 5*time.Second),
			maxAge:     30 * time.Minute,
			wantOK:     false,
			wantSeeded: true,
		},
		{
			name:       "well past threshold is unhealthy",
			seed:       time.Now().Add(-2 * time.Hour),
			maxAge:     30 * time.Minute,
			wantOK:     false,
			wantSeeded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &LOACache{entries: map[string][]LOAEntry{}}
			c.lastSuccessfulRefresh = tt.seed

			gotOK, gotTime := c.IsHealthy(tt.maxAge)
			if gotOK != tt.wantOK {
				t.Fatalf("IsHealthy ok = %v, want %v", gotOK, tt.wantOK)
			}
			if tt.wantSeeded {
				if !gotTime.Equal(tt.seed) {
					t.Fatalf("IsHealthy time = %v, want %v", gotTime, tt.seed)
				}
			} else if !gotTime.IsZero() {
				t.Fatalf("IsHealthy time = %v, want zero", gotTime)
			}
		})
	}
}

// loaQueryRegex is the regex sqlmock matches against the raw SELECT Refresh issues.
// QueryMatcherRegexp matches against the raw query string (whitespace/newlines intact),
// so we pin a substring that lives on a single line within the actual SQL — the
// column-list line — and escape the dots. That stays robust against later changes
// to FROM/WHERE/ORDER BY formatting while still rejecting an entirely different query.
const loaQueryRegex = `SELECT p\.message, t\.post_date, t\.thread_id`

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// validLOAMessage returns a forum post body that parseLOAPost accepts.
func validLOAMessage(username string) string {
	return "[B][COLOR=rgb(213, 185, 0)]Username[/COLOR][/B]: " + username + "\n" +
		"[B][COLOR=rgb(213, 185, 0)]Start Date[/COLOR][/B]\nJan 1, 2099\n" +
		"[B][COLOR=rgb(213, 185, 0)]End Date[/COLOR][/B]\nJan 31, 2099\n"
}

func TestRefresh_AllNodesFail_DoesNotUpdateTimestamp(t *testing.T) {
	db, mock := newMockDB(t)
	c := &LOACache{entries: map[string][]LOAEntry{}}
	seed := time.Now().Add(-2 * time.Hour)
	c.lastSuccessfulRefresh = seed

	nodeIDs := []int{180, 400, 540}
	for range nodeIDs {
		mock.ExpectQuery(loaQueryRegex).WillReturnError(sql.ErrConnDone)
	}

	c.Refresh(db, nodeIDs)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	if !c.lastSuccessfulRefresh.Equal(seed) {
		t.Fatalf("lastSuccessfulRefresh moved to %v, want unchanged %v",
			c.lastSuccessfulRefresh, seed)
	}
}

func TestRefresh_PartialSuccess_UpdatesTimestamp(t *testing.T) {
	db, mock := newMockDB(t)
	c := &LOACache{entries: map[string][]LOAEntry{}}
	seed := time.Now().Add(-2 * time.Hour)
	c.lastSuccessfulRefresh = seed

	rows := sqlmock.NewRows([]string{"message", "post_date", "thread_id"}).
		AddRow(validLOAMessage("alice"), time.Now().Unix(), int64(12345))
	mock.ExpectQuery(loaQueryRegex).WillReturnRows(rows)
	mock.ExpectQuery(loaQueryRegex).WillReturnError(sql.ErrConnDone)

	before := time.Now()
	c.Refresh(db, []int{180, 400})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	if !c.lastSuccessfulRefresh.After(seed) {
		t.Fatalf("lastSuccessfulRefresh = %v, expected later than seed %v",
			c.lastSuccessfulRefresh, seed)
	}
	if c.lastSuccessfulRefresh.Before(before) {
		t.Fatalf("lastSuccessfulRefresh = %v, expected >= %v",
			c.lastSuccessfulRefresh, before)
	}
}

func TestRefresh_RowsErrMidStream_DoesNotCount(t *testing.T) {
	db, mock := newMockDB(t)
	c := &LOACache{entries: map[string][]LOAEntry{}}
	seed := time.Now().Add(-2 * time.Hour)
	c.lastSuccessfulRefresh = seed

	// Node 1: Query succeeds, returns a row, then rows.Err() is non-nil at end.
	// Node 2: Query fails outright.
	streamErr := errors.New("simulated stream failure")
	rows := sqlmock.NewRows([]string{"message", "post_date", "thread_id"}).
		AddRow(validLOAMessage("bob"), time.Now().Unix(), int64(999)).
		RowError(0, streamErr) // injected: surfaces via rows.Err() after iteration
	mock.ExpectQuery(loaQueryRegex).WillReturnRows(rows)
	mock.ExpectQuery(loaQueryRegex).WillReturnError(sql.ErrConnDone)

	c.Refresh(db, []int{180, 400})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	if !c.lastSuccessfulRefresh.Equal(seed) {
		t.Fatalf("lastSuccessfulRefresh moved to %v, want unchanged %v "+
			"(mid-stream failures must not count as success)",
			c.lastSuccessfulRefresh, seed)
	}
}

// concurrentLOAFetcher is a loaPostFetcher safe for concurrent refresh calls. It
// returns a fresh, distinct post on every call (advancing post_date and rotating
// the username) so each Refresh mutates the entries map and the lastSynced cursor
// — maximizing the window for an unprotected read to observe a torn write. It holds
// no lock of its own: the contention under test is on LOACache's own RWMutex, so a
// thread-safe fetcher keeps any race the detector flags squarely in the cache.
type concurrentLOAFetcher struct {
	n atomic.Int64
}

func (f *concurrentLOAFetcher) fetchLOAPosts(_ int, _ int64) ([]loaPostRow, error) {
	i := f.n.Add(1)
	user := "user" + strconv.FormatInt(i%8, 10)
	return []loaPostRow{{
		message:  loaPost(user, "Jan 1, 2099", "Jan 31, 2099"),
		postDate: time.Now().Unix() + i,
		threadID: i,
	}}, nil
}

// TestLOACache_ConcurrentRefreshAndReads fans out many goroutines that hammer
// Refresh (writer path) alongside GetEntry / IsHealthy (reader paths) on
// one cache. Its job is to fail under `go test -race` if the cache's locking ever
// regresses — e.g. a dropped Lock/RLock or a read of a guarded field outside the
// mutex. With locking intact it is a fast, deterministic no-op assertion (the cache
// stays usable); under -race a lock regression trips the detector and fails the run.
func TestLOACache_ConcurrentRefreshAndReads(t *testing.T) {
	c := &LOACache{entries: map[string][]LOAEntry{}}
	fetcher := &concurrentLOAFetcher{}
	nodeIDs := []int{180, 400, 540}

	const (
		writers      = 8
		readers      = 16
		opsPerWriter = 50
		opsPerReader = 100
	)

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerWriter; j++ {
				c.refresh(fetcher, nodeIDs)
			}
		}()
	}

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerReader; j++ {
				// Touch every guarded read path; results are intentionally
				// ignored — the race detector, not an assertion, is the oracle.
				_, _ = c.GetEntry("user1")
				_, _ = c.IsHealthy(30 * time.Minute)
			}
		}()
	}

	wg.Wait()

	// Sanity: after the storm the cache is still coherent and readable under lock.
	if ok, _ := c.IsHealthy(time.Hour); !ok {
		t.Fatalf("cache should report healthy after concurrent refreshes")
	}
	if _, ok := c.GetEntry("user1"); !ok {
		t.Fatalf("expected at least one rotated user entry to be present")
	}
}

func TestRefresh_RowParseFailure_StillCountsNodeAsSuccess(t *testing.T) {
	db, mock := newMockDB(t)
	c := &LOACache{entries: map[string][]LOAEntry{}}
	seed := time.Now().Add(-2 * time.Hour)
	c.lastSuccessfulRefresh = seed

	// Single node returns one well-formed row + one row whose body parseLOAPost rejects.
	// Both rows complete; rows.Err() is nil. The node should still count as successful.
	rows := sqlmock.NewRows([]string{"message", "post_date", "thread_id"}).
		AddRow(validLOAMessage("carol"), time.Now().Unix(), int64(1)).
		AddRow("not-a-loa-post-body", time.Now().Unix(), int64(2))
	mock.ExpectQuery(loaQueryRegex).WillReturnRows(rows)

	c.Refresh(db, []int{180})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	if !c.lastSuccessfulRefresh.After(seed) {
		t.Fatalf("lastSuccessfulRefresh = %v, expected updated (row-level parse "+
			"noise must not invalidate the node)", c.lastSuccessfulRefresh)
	}
}
