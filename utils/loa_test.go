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
			name:   "legacy variant: wrong color (non-yellow label) does not match",
			msg:    "[B][COLOR=rgb(0, 0, 0)]Username[/COLOR][/B]: Black\n[B][COLOR=rgb(0, 0, 0)]Start Date[/COLOR][/B]\nJan 1, 2099\n[B][COLOR=rgb(0, 0, 0)]End Date[/COLOR][/B]\nJan 31, 2099\n",
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
	c := &LOACache{entries: map[string]LOAEntry{}}

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

func TestRefresh_PrunesExpiredEntries(t *testing.T) {
	c := &LOACache{entries: map[string]LOAEntry{}}
	// Seed: one expired (EndDate in the past) and one still-active entry.
	c.entries["expired"] = LOAEntry{Username: "expired", StartDate: time.Now().Add(-72 * time.Hour), EndDate: time.Now().Add(-24 * time.Hour)}
	c.entries["active"] = LOAEntry{Username: "active", StartDate: time.Now().Add(-24 * time.Hour), EndDate: time.Now().Add(24 * time.Hour)}
	c.lastSyncedPostDate = 100 // warm cache so since is deterministic

	f := &fakeLOAFetcher{byNode: map[int][]loaPostRow{180: nil}}
	c.refresh(f, []int{180})

	if _, ok := c.GetEntry("expired"); ok {
		t.Errorf("expired entry should have been pruned")
	}
	if _, ok := c.GetEntry("active"); !ok {
		t.Errorf("active entry must survive prune")
	}
}

func TestRefresh_NoNewPosts_IsNoOp(t *testing.T) {
	c := &LOACache{entries: map[string]LOAEntry{}}
	c.entries["keep"] = LOAEntry{Username: "keep", StartDate: time.Now().Add(-1 * time.Hour), EndDate: time.Now().Add(72 * time.Hour)}
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
	c := &LOACache{entries: map[string]LOAEntry{}}
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

func TestGetEntryAndIsOnLOA(t *testing.T) {
	c := &LOACache{entries: map[string]LOAEntry{}}
	c.entries["onloa"] = LOAEntry{Username: "OnLOA", StartDate: time.Now().Add(-1 * time.Hour), EndDate: time.Now().Add(1 * time.Hour)}
	c.entries["future"] = LOAEntry{Username: "Future", StartDate: time.Now().Add(24 * time.Hour), EndDate: time.Now().Add(48 * time.Hour)}

	// GetEntry is case-insensitive on the lookup key.
	if _, ok := c.GetEntry("ONLOA"); !ok {
		t.Errorf("GetEntry should be case-insensitive")
	}
	if _, ok := c.GetEntry("missing"); ok {
		t.Errorf("GetEntry for absent user should be false")
	}
	if !c.IsOnLOA("onloa") {
		t.Errorf("IsOnLOA should be true for currently-active window")
	}
	if c.IsOnLOA("future") {
		t.Errorf("IsOnLOA should be false before StartDate")
	}
	if c.IsOnLOA("missing") {
		t.Errorf("IsOnLOA should be false for unknown user")
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
			c := &LOACache{entries: map[string]LOAEntry{}}
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
	c := &LOACache{entries: map[string]LOAEntry{}}
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
	c := &LOACache{entries: map[string]LOAEntry{}}
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
	c := &LOACache{entries: map[string]LOAEntry{}}
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
// Refresh (writer path) alongside GetEntry / IsOnLOA / IsHealthy (reader paths) on
// one cache. Its job is to fail under `go test -race` if the cache's locking ever
// regresses — e.g. a dropped Lock/RLock or a read of a guarded field outside the
// mutex. With locking intact it is a fast, deterministic no-op assertion (the cache
// stays usable); under -race a lock regression trips the detector and fails the run.
func TestLOACache_ConcurrentRefreshAndReads(t *testing.T) {
	c := &LOACache{entries: map[string]LOAEntry{}}
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
				_ = c.IsOnLOA("user2")
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
	c := &LOACache{entries: map[string]LOAEntry{}}
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
