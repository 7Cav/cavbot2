package commands

import (
	"errors"
	"strconv"
	"strings"
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
