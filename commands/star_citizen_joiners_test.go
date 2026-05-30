package commands

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func mustParseUTC(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm.UTC()
}

func TestNextJoinerReportFire(t *testing.T) {
	tests := []struct {
		name string
		now  string
		want string
	}{
		{
			name: "midweek-rolls-to-next-sunday",
			now:  "2026-05-20T12:00:00Z",
			want: "2026-05-24T04:20:00Z",
		},
		{
			name: "sunday-before-fire-rolls-to-today",
			now:  "2026-05-24T03:00:00Z",
			want: "2026-05-24T04:20:00Z",
		},
		{
			name: "sunday-at-fire-rolls-to-next-week",
			now:  "2026-05-24T04:20:00Z",
			want: "2026-05-31T04:20:00Z",
		},
		{
			name: "sunday-after-fire-rolls-to-next-week",
			now:  "2026-05-24T05:00:00Z",
			want: "2026-05-31T04:20:00Z",
		},
		{
			name: "non-utc-input-normalized",
			now:  "2026-05-24T01:00:00-05:00",
			want: "2026-05-31T04:20:00Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nextJoinerReportFire(mustParseUTC(t, tc.now))
			if want := mustParseUTC(t, tc.want); !got.Equal(want) {
				t.Errorf("nextJoinerReportFire(%s) = %s, want %s", tc.now, got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

func TestFormatJoinerReport(t *testing.T) {
	weekOf := mustParseUTC(t, "2026-05-17T04:20:00Z")
	matches := []*discordgo.Member{
		{
			User:     &discordgo.User{ID: "111", Username: "alice"},
			JoinedAt: mustParseUTC(t, "2026-05-18T10:00:00Z"),
		},
		{
			User:     &discordgo.User{ID: "222", Username: "bob"},
			Nick:     "BobbyTwoShoes",
			JoinedAt: mustParseUTC(t, "2026-05-19T11:30:00Z"),
		},
	}

	t.Run("empty-still-renders-header", func(t *testing.T) {
		got := formatJoinerReport(nil, weekOf)
		want := "Star Citizen joiner report for week of 2026-05-17: 0 new joiners."
		if got != want {
			t.Errorf("empty: got %q want %q", got, want)
		}
	})

	t.Run("singular-noun-for-one-match", func(t *testing.T) {
		got := formatJoinerReport(matches[:1], weekOf)
		if !strings.HasPrefix(got, "Star Citizen joiner report for week of 2026-05-17: 1 new joiner.") {
			t.Errorf("singular header missing: %q", got)
		}
		if !strings.Contains(got, "- alice — `111` — joined 2026-05-18T10:00:00Z") {
			t.Errorf("alice line missing: %q", got)
		}
	})

	t.Run("plural-noun-for-multiple-and-nick-shown", func(t *testing.T) {
		got := formatJoinerReport(matches, weekOf)
		if !strings.HasPrefix(got, "Star Citizen joiner report for week of 2026-05-17: 2 new joiners.") {
			t.Errorf("plural header missing: %q", got)
		}
		if !strings.Contains(got, "- BobbyTwoShoes (bob) — `222` — joined 2026-05-19T11:30:00Z") {
			t.Errorf("nick formatting missing: %q", got)
		}
		if strings.HasSuffix(got, "\n") {
			t.Errorf("trailing newline not stripped: %q", got)
		}
	})
}

// fakeJoinerSession captures GuildMembers/UserChannelCreate/ChannelMessageSend
// calls and serves canned responses. Pages are returned in order; when
// exhausted, an empty page is returned (signalling walk-complete).
type fakeJoinerSession struct {
	pages       [][]*discordgo.Member
	pageCalls   []string
	openedDMFor string
	sentTo      string
	sentBody    string

	guildMembersErr error
	userChannelErr  error
	sendMessageErr  error
}

func (f *fakeJoinerSession) GuildMembers(_ string, after string, _ int, _ ...discordgo.RequestOption) ([]*discordgo.Member, error) {
	f.pageCalls = append(f.pageCalls, after)
	if f.guildMembersErr != nil {
		return nil, f.guildMembersErr
	}
	if len(f.pages) == 0 {
		return nil, nil
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}

func (f *fakeJoinerSession) UserChannelCreate(recipientID string, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	f.openedDMFor = recipientID
	if f.userChannelErr != nil {
		return nil, f.userChannelErr
	}
	return &discordgo.Channel{ID: "dm-" + recipientID}, nil
}

func (f *fakeJoinerSession) ChannelMessageSend(channelID, content string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.sentTo = channelID
	f.sentBody = content
	if f.sendMessageErr != nil {
		return nil, f.sendMessageErr
	}
	return &discordgo.Message{ID: "msg-1"}, nil
}

// fillMembers returns count Members all holding the same role and joined at
// the supplied time, for exercising the page-size boundary.
func fillMembers(count int, joinedAt time.Time, roles []string) []*discordgo.Member {
	out := make([]*discordgo.Member, count)
	for i := 0; i < count; i++ {
		out[i] = &discordgo.Member{
			User:     &discordgo.User{ID: "u" + strconv.Itoa(i)},
			JoinedAt: joinedAt,
			Roles:    roles,
		}
	}
	return out
}

func TestWalkRecentJoinersWithRole_PaginatesAndFilters(t *testing.T) {
	now := mustParseUTC(t, "2026-05-24T04:20:00Z")
	cutoff := now.Add(-7 * 24 * time.Hour)

	recent := now.Add(-2 * 24 * time.Hour)
	stale := cutoff.Add(-1 * time.Second)

	fullPage := fillMembers(guildMembersPageLimit, recent, []string{starCitizenRoleID})
	tail := []*discordgo.Member{
		{User: &discordgo.User{ID: "match-late"}, JoinedAt: recent, Roles: []string{starCitizenRoleID, "other"}},
		{User: &discordgo.User{ID: "miss-no-role"}, JoinedAt: recent, Roles: []string{"other"}},
		{User: &discordgo.User{ID: "miss-stale"}, JoinedAt: stale, Roles: []string{starCitizenRoleID}},
		{User: nil, JoinedAt: recent, Roles: []string{starCitizenRoleID}},
		nil,
	}

	fake := &fakeJoinerSession{pages: [][]*discordgo.Member{fullPage, tail}}

	matches, err := walkRecentJoinersWithRole(fake, "guild", now, 7*24*time.Hour, starCitizenRoleID)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	wantCount := guildMembersPageLimit + 1
	if len(matches) != wantCount {
		t.Fatalf("matches=%d want %d", len(matches), wantCount)
	}

	if len(fake.pageCalls) != 2 {
		t.Errorf("expected 2 page calls, got %d (%v)", len(fake.pageCalls), fake.pageCalls)
	}
	if fake.pageCalls[0] != "" {
		t.Errorf("first page call after-cursor = %q, want empty", fake.pageCalls[0])
	}
	lastIDOfFullPage := fullPage[len(fullPage)-1].User.ID
	if fake.pageCalls[1] != lastIDOfFullPage {
		t.Errorf("second page after-cursor = %q, want %q", fake.pageCalls[1], lastIDOfFullPage)
	}

	if got := matches[len(matches)-1].User.ID; got != "match-late" {
		t.Errorf("last match ID = %q, want match-late (sort by JoinedAt is stable; tail item joined latest)", got)
	}
}

func TestWalkRecentJoinersWithRole_PropagatesError(t *testing.T) {
	fake := &fakeJoinerSession{guildMembersErr: errors.New("boom")}
	_, err := walkRecentJoinersWithRole(fake, "g", time.Now(), time.Hour, "role")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected wrapped boom error, got %v", err)
	}
}

// TestWalkRecentJoinersWithRole_JoinedAtBoundary pins both sides of the
// cutoff to catch silent .Before↔.After predicate flips on refactor.
func TestWalkRecentJoinersWithRole_JoinedAtBoundary(t *testing.T) {
	now := mustParseUTC(t, "2026-05-24T04:20:00Z")
	cutoff := now.Add(-7 * 24 * time.Hour)
	page := []*discordgo.Member{
		{User: &discordgo.User{ID: "exact-cutoff"}, JoinedAt: cutoff, Roles: []string{starCitizenRoleID}},
		{User: &discordgo.User{ID: "one-ns-before"}, JoinedAt: cutoff.Add(-time.Nanosecond), Roles: []string{starCitizenRoleID}},
	}
	fake := &fakeJoinerSession{pages: [][]*discordgo.Member{page}}
	matches, err := walkRecentJoinersWithRole(fake, "g", now, 7*24*time.Hour, starCitizenRoleID)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(matches) != 1 || matches[0].User.ID != "exact-cutoff" {
		t.Fatalf("expected exactly [exact-cutoff], got %d matches: %+v", len(matches), matches)
	}
}

// TestWalkRecentJoinersWithRole_ShortPageEndsWalk verifies the
// len(page) < limit early-exit specifically, independent of the
// empty-page branch (which terminates a different way).
func TestWalkRecentJoinersWithRole_ShortPageEndsWalk(t *testing.T) {
	now := mustParseUTC(t, "2026-05-24T04:20:00Z")
	recent := now.Add(-2 * 24 * time.Hour)
	full := fillMembers(guildMembersPageLimit, recent, []string{starCitizenRoleID})
	short := []*discordgo.Member{
		{User: &discordgo.User{ID: "tail-1"}, JoinedAt: recent, Roles: []string{starCitizenRoleID}},
	}
	// A third page is queued; the walker MUST NOT request it because page 2
	// is shorter than the limit.
	bonus := []*discordgo.Member{
		{User: &discordgo.User{ID: "ghost"}, JoinedAt: recent, Roles: []string{starCitizenRoleID}},
	}
	fake := &fakeJoinerSession{pages: [][]*discordgo.Member{full, short, bonus}}
	matches, err := walkRecentJoinersWithRole(fake, "g", now, 7*24*time.Hour, starCitizenRoleID)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(fake.pageCalls) != 2 {
		t.Fatalf("expected exactly 2 GuildMembers calls (short page 2 terminates), got %d: %v", len(fake.pageCalls), fake.pageCalls)
	}
	if len(matches) != guildMembersPageLimit+1 {
		t.Fatalf("matches=%d want %d", len(matches), guildMembersPageLimit+1)
	}
	for _, m := range matches {
		if m.User.ID == "ghost" {
			t.Fatalf("page 3 was requested despite short page 2 — early-exit broken")
		}
	}
}

// TestWalkRecentJoinersWithRole_ZeroMatchesPageStillAdvancesCursor covers a
// page that contains no matches but valid members — the cursor must still
// advance via lastSeenID, otherwise pagination would deadlock on the page.
func TestWalkRecentJoinersWithRole_ZeroMatchesPageStillAdvancesCursor(t *testing.T) {
	now := mustParseUTC(t, "2026-05-24T04:20:00Z")
	recent := now.Add(-2 * 24 * time.Hour)
	// Page 1 is full but no member holds the role → 0 matches, but cursor
	// must advance to the last valid ID.
	page1 := fillMembers(guildMembersPageLimit, recent, []string{"unrelated-role"})
	page2 := []*discordgo.Member{
		{User: &discordgo.User{ID: "real-match"}, JoinedAt: recent, Roles: []string{starCitizenRoleID}},
	}
	fake := &fakeJoinerSession{pages: [][]*discordgo.Member{page1, page2}}
	matches, err := walkRecentJoinersWithRole(fake, "g", now, 7*24*time.Hour, starCitizenRoleID)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(matches) != 1 || matches[0].User.ID != "real-match" {
		t.Fatalf("expected [real-match], got %+v", matches)
	}
	if len(fake.pageCalls) != 2 {
		t.Fatalf("expected 2 page calls, got %d", len(fake.pageCalls))
	}
	if fake.pageCalls[1] != page1[len(page1)-1].User.ID {
		t.Errorf("page 2 after-cursor = %q, want page1's last ID %q",
			fake.pageCalls[1], page1[len(page1)-1].User.ID)
	}
}

// TestWalkRecentJoinersWithRole_AllNilUserPageErrors verifies the safety net:
// a page entirely of nil-User entries can't advance the cursor, so the walker
// must surface an error instead of silently terminating (which would let an
// upstream Discord regression masquerade as "0 new joiners").
func TestWalkRecentJoinersWithRole_AllNilUserPageErrors(t *testing.T) {
	page := []*discordgo.Member{
		{User: nil},
		nil,
		{User: nil},
	}
	fake := &fakeJoinerSession{pages: [][]*discordgo.Member{page}}
	_, err := walkRecentJoinersWithRole(fake, "g", time.Now(), time.Hour, starCitizenRoleID)
	if err == nil {
		t.Fatalf("expected error for all-nil-User page, got nil")
	}
	if !strings.Contains(err.Error(), "no usable User.ID") {
		t.Errorf("error should mention 'no usable User.ID': %v", err)
	}
}

func TestRunJoinerReport_EmptyStillDMs(t *testing.T) {
	now := mustParseUTC(t, "2026-05-24T04:20:00Z")
	fake := &fakeJoinerSession{pages: [][]*discordgo.Member{nil}}

	if err := runJoinerReport(fake, "guild", now); err != nil {
		t.Fatalf("runJoinerReport: %v", err)
	}
	if fake.openedDMFor != sparrowDiscordID {
		t.Errorf("DM opened for %q, want %q", fake.openedDMFor, sparrowDiscordID)
	}
	if fake.sentTo != "dm-"+sparrowDiscordID {
		t.Errorf("message sent to %q, want dm-%s", fake.sentTo, sparrowDiscordID)
	}
	if !strings.Contains(fake.sentBody, "0 new joiners") {
		t.Errorf("empty-case body missing zero-count: %q", fake.sentBody)
	}
	// Lock the rendered weekOf so a drift in the lookback math doesn't slip
	// through silently (now=2026-05-24, lookback=7d ⇒ week of 2026-05-17).
	if !strings.Contains(fake.sentBody, "week of 2026-05-17") {
		t.Errorf("empty-case body missing weekOf header: %q", fake.sentBody)
	}
}

func TestRunJoinerReport_DMOpenError(t *testing.T) {
	fake := &fakeJoinerSession{
		pages:          [][]*discordgo.Member{nil},
		userChannelErr: errors.New("dm-locked"),
	}
	err := runJoinerReport(fake, "g", time.Now())
	if err == nil || !strings.Contains(err.Error(), "open DM") {
		t.Errorf("expected open-DM error, got %v", err)
	}
}

func TestRunJoinerReport_SendError(t *testing.T) {
	fake := &fakeJoinerSession{
		pages:          [][]*discordgo.Member{nil},
		sendMessageErr: errors.New("send-failed"),
	}
	err := runJoinerReport(fake, "g", time.Now())
	if err == nil || !strings.Contains(err.Error(), "send DM") {
		t.Errorf("expected send-DM error, got %v", err)
	}
}

func TestRunJoinerReport_WalkErrorBlocksDM(t *testing.T) {
	fake := &fakeJoinerSession{guildMembersErr: errors.New("rate-limited")}
	err := runJoinerReport(fake, "g", time.Now())
	if err == nil || !strings.Contains(err.Error(), "walk members") {
		t.Errorf("expected walk-members error, got %v", err)
	}
	if fake.openedDMFor != "" {
		t.Errorf("DM should not be opened when walk fails; opened for %q", fake.openedDMFor)
	}
}

// TestMustWeeklyFireTime_BoundsAndHappyPath locks each boundary so a typo
// like `>` → `>=` in the validator ships red.
func TestMustWeeklyFireTime_BoundsAndHappyPath(t *testing.T) {
	panicCases := []struct {
		name      string
		wd        time.Weekday
		hour, min int
	}{
		{"weekday-too-large", time.Weekday(7), 0, 0},
		{"weekday-too-small", time.Weekday(-1), 0, 0},
		{"hour-too-large", time.Sunday, 24, 0},
		{"hour-too-small", time.Sunday, -1, 0},
		{"minute-too-large", time.Sunday, 0, 60},
		{"minute-too-small", time.Sunday, 0, -1},
	}
	for _, tc := range panicCases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic for %s, got none", tc.name)
				}
			}()
			mustWeeklyFireTime(tc.wd, tc.hour, tc.min)
		})
	}
	t.Run("happy-path-boundary-values", func(t *testing.T) {
		got := mustWeeklyFireTime(time.Saturday, 23, 59)
		if got.weekday != time.Saturday || got.hour != 23 || got.minute != 59 {
			t.Errorf("got %+v, want {Sat, 23, 59}", got)
		}
	})
	t.Run("happy-path-low-boundary", func(t *testing.T) {
		got := mustWeeklyFireTime(time.Sunday, 0, 0)
		if got.weekday != time.Sunday || got.hour != 0 || got.minute != 0 {
			t.Errorf("got %+v, want {Sun, 0, 0}", got)
		}
	})
}

// panickingFakeSession panics on the first GuildMembers call, used to
// exercise the scheduler loop's per-fire defer-recover. Subsequent calls
// return cleanly so a test can assert the loop reached a later iteration.
type panickingFakeSession struct {
	mu    sync.Mutex
	calls int
}

func (p *panickingFakeSession) GuildMembers(string, string, int, ...discordgo.RequestOption) ([]*discordgo.Member, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 1 {
		panic("simulated discord-side panic")
	}
	return nil, nil
}

func (p *panickingFakeSession) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *panickingFakeSession) UserChannelCreate(string, ...discordgo.RequestOption) (*discordgo.Channel, error) {
	return &discordgo.Channel{ID: "dm"}, nil
}

func (p *panickingFakeSession) ChannelMessageSend(string, string, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{ID: "m"}, nil
}

// TestRunJoinerReportSchedulerLoop_PanicIsRecovered verifies the per-fire
// defer utils.RecoverPanic catches a runJoinerReport panic WITHOUT exiting
// the scheduler loop: the outer for survives and proceeds to the next
// weekly fire.
//
// Determinism: the injected clock returns a far-past time on the first
// call so the first time.Sleep returns immediately and the panicking fire
// runs; on the second call it returns a far-FUTURE time so the second
// time.Sleep blocks well past the test timeout. The test waits for the
// second-iteration signal (the clock's 2nd invocation), proving the loop
// advanced past the panic, then returns — leaving the goroutine parked in
// its long sleep (harmless; no resources held, no process crash).
func TestRunJoinerReportSchedulerLoop_PanicIsRecovered(t *testing.T) {
	fake := &panickingFakeSession{}

	var nowMu sync.Mutex
	nowCalls := 0
	secondIter := make(chan struct{})
	now := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		nowCalls++
		switch nowCalls {
		case 1:
			// Far past → first fire is overdue → sleep returns immediately.
			return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		case 2:
			// Reaching here proves the loop survived the panic and looped
			// back. Return a far-future time so the second sleep parks the
			// goroutine well beyond the test deadline.
			close(secondIter)
			return time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
		default:
			return time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
		}
	}

	go runJoinerReportSchedulerLoop(fake, "g", now)

	select {
	case <-secondIter:
		// Loop reached its second iteration after recovering the panic.
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduler loop did not reach a 2nd iteration within 2s — panic killed the loop instead of being recovered per-fire")
	}
	if fake.callCount() < 1 {
		t.Errorf("fake session was never invoked — loop body did not run")
	}
}

// TestRunJoinerReportSchedulerLoop_OrdinaryErrorContinues verifies a
// non-panic report error (e.g. a Discord API error) is captured/logged but
// does not stop the loop: the scheduler proceeds to the next fire without
// an immediate in-cycle retry.
func TestRunJoinerReportSchedulerLoop_OrdinaryErrorContinues(t *testing.T) {
	fake := &fakeJoinerSession{guildMembersErr: errors.New("rate-limited")}

	var nowMu sync.Mutex
	nowCalls := 0
	secondIter := make(chan struct{})
	now := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		nowCalls++
		switch nowCalls {
		case 1:
			return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		case 2:
			close(secondIter)
			return time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
		default:
			return time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
		}
	}

	go runJoinerReportSchedulerLoop(fake, "g", now)

	select {
	case <-secondIter:
		// Loop advanced to the next fire after the ordinary error.
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduler loop did not reach a 2nd iteration within 2s after an ordinary error")
	}
	// One fire = one GuildMembers attempt: no immediate in-cycle retry.
	if got := len(fake.pageCalls); got != 1 {
		t.Errorf("expected exactly 1 GuildMembers call (no in-cycle retry), got %d", got)
	}
}

// toggleErrJoinerSession errors on its first GuildMembers call and succeeds
// (empty roster → still DMs) thereafter, recording every call under a mutex so
// the scheduler test can assert a fire actually re-executed across iterations.
type toggleErrJoinerSession struct {
	mu        sync.Mutex
	calls     int
	secondRun chan struct{}
}

func (g *toggleErrJoinerSession) GuildMembers(string, string, int, ...discordgo.RequestOption) ([]*discordgo.Member, error) {
	g.mu.Lock()
	g.calls++
	n := g.calls
	g.mu.Unlock()
	if n == 1 {
		return nil, errors.New("rate-limited")
	}
	if n == 2 {
		// Second fire executed and got past the error — signal so the test can
		// observe the loop didn't merely advance but actually ran a later fire.
		close(g.secondRun)
	}
	return nil, nil
}

func (g *toggleErrJoinerSession) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func (g *toggleErrJoinerSession) UserChannelCreate(string, ...discordgo.RequestOption) (*discordgo.Channel, error) {
	return &discordgo.Channel{ID: "dm"}, nil
}

func (g *toggleErrJoinerSession) ChannelMessageSend(string, string, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{ID: "m"}, nil
}

// TestRunJoinerReportSchedulerLoop_ErrorDoesNotKillLoop strengthens the
// ordinary-error coverage from #119: rather than only proving the clock was
// re-read after an error (loop advanced), it drives TWO executed fires — the
// first returns a report error, the second runs to completion — and asserts the
// report itself re-ran (call count >= 2). That distinguishes "loop survived and
// fired again" from "loop merely recomputed the next fire". Determinism: the
// clock returns a far-past time on calls 1 and 2 so both fires are overdue and
// their sleeps return immediately, then a far-future time so the third sleep
// parks the goroutine past the test deadline.
func TestRunJoinerReportSchedulerLoop_ErrorDoesNotKillLoop(t *testing.T) {
	fake := &toggleErrJoinerSession{secondRun: make(chan struct{})}

	var nowMu sync.Mutex
	nowCalls := 0
	now := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		nowCalls++
		if nowCalls <= 2 {
			// Far past → fire is overdue → sleep returns immediately.
			return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		// Far future → park the goroutine well past the test deadline.
		return time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	go runJoinerReportSchedulerLoop(fake, "g", now)

	select {
	case <-fake.secondRun:
		// Second fire executed after the first returned an error.
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduler loop did not run a 2nd fire within 2s — the error killed the loop instead of continuing to the next fire")
	}
	if got := fake.callCount(); got < 2 {
		t.Errorf("expected the report to re-run (call count >= 2) after an error, got %d", got)
	}
}
