package commands

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// makeBulkAddEntries builds a comma-separated discordname value with n distinct
// non-blank entries (entry-0, entry-1, ...). splitCommaSeparated trims and drops
// blanks, so distinct non-blank tokens map one-to-one onto parsed entries.
func makeBulkAddEntries(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, "entry-"+strconv.Itoa(i))
	}
	return strings.Join(parts, ", ")
}

// A bulkadd carrying more than maxBulkAddEntries comma-separated entries must be
// rejected up front, before any Discord API call fans out. Discord allows a
// multi-thousand-character string option, so an operator could otherwise submit
// hundreds of names and trigger a serial GuildMembersSearch + per-role
// GuildMemberRoleAdd storm that outruns the rate limiter and the interaction
// token window (#173).
func TestRunFoxhole_BulkAddOverLimitRejectedBeforeAnyAPICall(t *testing.T) {
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
	}
	f := &fakeResponder{}

	overLimit := maxBulkAddEntries + 1
	i := foxholeInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", makeBulkAddEntries(overLimit)),
	)

	runFoxhole(f, gm, nil, i)

	// No GuildManager call may happen: not the role resolution, not the per-entry
	// search, not the role-add. The whole point is to bail before any fan-out.
	if len(gm.Calls()) != 0 {
		t.Fatalf("over-limit bulkadd must not touch the guild at all; got %v", gm.Calls())
	}
	if gm.countCalls("GuildMembersSearch") != 0 {
		t.Fatalf("over-limit bulkadd must not search; got %v", gm.Calls())
	}
	if gm.countCalls("GuildMemberRoleAdd") != 0 {
		t.Fatalf("over-limit bulkadd must not add roles; got %v", gm.Calls())
	}

	// The reply must be actionable: name the limit and the offending count.
	got := lastEditContent(f.Calls())
	assertVerdict(t, got, "", verdictFailed)
	if !strings.Contains(got, strconv.Itoa(maxBulkAddEntries)) {
		t.Fatalf("rejection must name the limit %d, got %q", maxBulkAddEntries, got)
	}
	if !strings.Contains(got, strconv.Itoa(overLimit)) {
		t.Fatalf("rejection must name the submitted count %d, got %q", overLimit, got)
	}
}

// A bulkadd at exactly the limit is within bounds and must behave as today: it
// fans out to the per-entry work. Pins the boundary so a future off-by-one
// (> vs >=) is caught.
func TestRunFoxhole_BulkAddAtLimitStillFansOut(t *testing.T) {
	// Every entry resolves to a distinct member so each one reaches a role-add.
	search := make(map[string][]*discordgo.Member, maxBulkAddEntries)
	for idx := 0; idx < maxBulkAddEntries; idx++ {
		name := "entry-" + strconv.Itoa(idx)
		search[name] = []*discordgo.Member{
			{User: &discordgo.User{ID: fmt.Sprintf("id-%d", idx), Username: name}},
		}
	}
	gm := &fakeGuildManager{
		roles:         []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		searchResults: search,
	}
	f := &fakeResponder{}

	i := foxholeInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", makeBulkAddEntries(maxBulkAddEntries)),
	)

	runFoxhole(f, gm, nil, i)

	if gm.countCalls("GuildMembersSearch") != maxBulkAddEntries {
		t.Fatalf("at-limit bulkadd must search every entry; got %d searches (%v)", gm.countCalls("GuildMembersSearch"), gm.Calls())
	}
	if gm.countCalls("GuildMemberRoleAdd") != maxBulkAddEntries {
		t.Fatalf("at-limit bulkadd must add a role per entry; got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	assertReplyNamesAddedMembers(t, f.Calls(), maxBulkAddEntries)
}

// The cap counts PARSED entries, not raw commas. The parse trims and drops
// blank tokens, so a comma-heavy payload (trailing commas, doubled
// commas, whitespace-only fields) can carry far more than maxBulkAddEntries raw
// tokens yet net to <= the limit of real names. Such a payload must be ACCEPTED
// and fan out for every real entry, never falsely rejected. This locks the
// "count entries, not commas" contract against a regression in the parse's
// blank-dropping.
func TestRunFoxhole_BulkAddCountsParsedEntriesNotRawCommas(t *testing.T) {
	// Interleave each real name with an empty field, then pad with extra trailing
	// commas. Raw comma-separated token count is well above maxBulkAddEntries; the
	// parsed (trim + drop-blank) count is exactly maxBulkAddEntries.
	search := make(map[string][]*discordgo.Member, maxBulkAddEntries)
	rawTokens := make([]string, 0, maxBulkAddEntries*2+10)
	for idx := 0; idx < maxBulkAddEntries; idx++ {
		name := "entry-" + strconv.Itoa(idx)
		search[name] = []*discordgo.Member{
			{User: &discordgo.User{ID: fmt.Sprintf("id-%d", idx), Username: name}},
		}
		rawTokens = append(rawTokens, name, "") // real name followed by a blank field
	}
	// A run of whitespace-only and empty trailing fields, all of which must drop.
	rawTokens = append(rawTokens, "", "   ", "", "", "", "", "", "", "", "")
	payload := strings.Join(rawTokens, ",")

	rawCommaCount := strings.Count(payload, ",") + 1
	if rawCommaCount <= maxBulkAddEntries {
		t.Fatalf("test setup: need > %d raw tokens to prove the comma/entry distinction, got %d", maxBulkAddEntries, rawCommaCount)
	}

	gm := &fakeGuildManager{
		roles:         []*discordgo.Role{guildRole("r-int", foxholeRoleBaseNameDefault+" Internal")},
		searchResults: search,
	}
	f := &fakeResponder{}
	i := foxholeInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", payload),
	)

	runFoxhole(f, gm, nil, i)

	// Must fan out once per real entry, not once per raw comma token, so the
	// cap never refused it.
	if gm.countCalls("GuildMembersSearch") != maxBulkAddEntries {
		t.Fatalf("must search once per real entry (%d), got %d (%v)", maxBulkAddEntries, gm.countCalls("GuildMembersSearch"), gm.Calls())
	}
	if gm.countCalls("GuildMemberRoleAdd") != maxBulkAddEntries {
		t.Fatalf("must add a role per real entry (%d), got %d (%v)", maxBulkAddEntries, gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	assertReplyNamesAddedMembers(t, f.Calls(), maxBulkAddEntries)
}

// assertReplyNamesAddedMembers fails unless the reply's embed mentions each of
// the members id-0 to id-(count-1) the fixtures above resolve.
func assertReplyNamesAddedMembers(t *testing.T, calls []recordedCall, count int) {
	t.Helper()
	embed := lastEditEmbed(calls)
	if embed == nil {
		t.Fatalf("reply carries no embed of added members; content %q", lastEditContent(calls))
	}
	for idx := 0; idx < count; idx++ {
		if mention := fmt.Sprintf("<@id-%d>", idx); !strings.Contains(embed.Description, mention) {
			t.Errorf("reply names no %s among the added members", mention)
		}
	}
}

// A bulkadd's entries are the comma-separated names with each trimmed and
// the blank ones dropped. Entries that are all blank leave nothing to do, so
// the run changes nothing and asks Discord nothing; otherwise exactly the
// named members get the role, and no blank entry fails.
func TestRunFoxhole_BulkAddEntriesAreTheTrimmedNonBlankNames(t *testing.T) {
	cases := []struct {
		name    string
		entries string
		added   []string
	}{
		{"only commas and spaces", " , ,,  ,", nil},
		{"interior blanks", "a,,b, ,c", []string{"a", "b", "c"}},
		{"surrounding whitespace", "  a , b ,c  ", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gm := &fakeGuildManager{
				roles:         []*discordgo.Role{guildRole("r-int", defaultInternalRoleName)},
				searchResults: map[string][]*discordgo.Member{},
			}
			for _, name := range []string{"a", "b", "c"} {
				gm.searchResults[name] = []*discordgo.Member{{User: &discordgo.User{ID: "id-" + name, Username: name}}}
			}
			f := &fakeResponder{}

			runFoxhole(f, gm, nil, bulkAddInteraction("internal", tc.entries))

			var added []string
			for _, add := range gm.roleAddCalls() {
				added = append(added, strings.TrimPrefix(add.userID, "id-"))
			}
			if !slices.Equal(added, tc.added) {
				t.Errorf("the bulkadd added %v, want %v", added, tc.added)
			}
			reply := lastEditContent(f.Calls())
			if tc.added == nil {
				if calls := gm.Calls(); len(calls) != 0 {
					t.Errorf("a bulkadd with nothing to do asked Discord %v", calls)
				}
				assertVerdict(t, reply, "", verdictLeftOver)
				return
			}
			for _, v := range verdicts(reply, "") {
				if v != verdictDone {
					t.Errorf("the reply carries verdict %q, want every line %q; reply %q", v, verdictDone, reply)
				}
			}
		})
	}
}
