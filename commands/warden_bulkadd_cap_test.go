package commands

import (
	"fmt"
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
func TestRunWarden_BulkAddOverLimitRejectedBeforeAnyAPICall(t *testing.T) {
	gm := &fakeGuildManager{
		roles: []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
	}
	f := &fakeResponder{}

	overLimit := maxBulkAddEntries + 1
	i := wardenInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", makeBulkAddEntries(overLimit)),
	)

	runWarden(f, gm, i)

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
func TestRunWarden_BulkAddAtLimitStillFansOut(t *testing.T) {
	// Every entry resolves to a distinct member so each one reaches a role-add.
	search := make(map[string][]*discordgo.Member, maxBulkAddEntries)
	for idx := 0; idx < maxBulkAddEntries; idx++ {
		name := "entry-" + strconv.Itoa(idx)
		search[name] = []*discordgo.Member{
			{User: &discordgo.User{ID: fmt.Sprintf("id-%d", idx), Username: name}},
		}
	}
	gm := &fakeGuildManager{
		roles:         []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		searchResults: search,
	}
	f := &fakeResponder{}

	i := wardenInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", makeBulkAddEntries(maxBulkAddEntries)),
	)

	runWarden(f, gm, i)

	if gm.countCalls("GuildMembersSearch") != maxBulkAddEntries {
		t.Fatalf("at-limit bulkadd must search every entry; got %d searches (%v)", gm.countCalls("GuildMembersSearch"), gm.Calls())
	}
	if gm.countCalls("GuildMemberRoleAdd") != maxBulkAddEntries {
		t.Fatalf("at-limit bulkadd must add a role per entry; got %d (%v)", gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	got := lastEditContent(f.Calls())
	if !strings.Contains(got, fmt.Sprintf("Added warden role(s) to %d user(s)", maxBulkAddEntries)) {
		t.Fatalf("expected all %d adds summarised, got %q", maxBulkAddEntries, got)
	}
}

// The cap counts PARSED entries, not raw commas. splitCommaSeparated trims and
// drops blank tokens, so a comma-heavy payload (trailing commas, doubled
// commas, whitespace-only fields) can carry far more than maxBulkAddEntries raw
// tokens yet net to <= the limit of real names. Such a payload must be ACCEPTED
// and fan out for every real entry, never falsely rejected. This locks the
// "count entries, not commas" contract against a regression in
// splitCommaSeparated's blank-dropping.
func TestRunWarden_BulkAddCountsParsedEntriesNotRawCommas(t *testing.T) {
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
	if parsed := len(splitCommaSeparated(payload)); parsed != maxBulkAddEntries {
		t.Fatalf("test setup: payload must parse to exactly %d real entries, got %d", maxBulkAddEntries, parsed)
	}

	gm := &fakeGuildManager{
		roles:         []*discordgo.Role{wardenRole("r-int", wardenRoleBaseName+" Internal")},
		searchResults: search,
	}
	f := &fakeResponder{}
	i := wardenInteraction("guild-1",
		stringOption("command", "bulkadd"),
		stringOption("flag", "internal"),
		stringOption("discordname", payload),
	)

	runWarden(f, gm, i)

	got := lastEditContent(f.Calls())
	// Must NOT be rejected as over-limit despite the raw comma count exceeding it.
	if strings.Contains(got, "Too many entries") {
		t.Fatalf("comma-heavy payload netting to %d real entries must not be rejected; got %q", maxBulkAddEntries, got)
	}
	// Must fan out once per real entry, not once per raw comma token.
	if gm.countCalls("GuildMembersSearch") != maxBulkAddEntries {
		t.Fatalf("must search once per real entry (%d), got %d (%v)", maxBulkAddEntries, gm.countCalls("GuildMembersSearch"), gm.Calls())
	}
	if gm.countCalls("GuildMemberRoleAdd") != maxBulkAddEntries {
		t.Fatalf("must add a role per real entry (%d), got %d (%v)", maxBulkAddEntries, gm.countCalls("GuildMemberRoleAdd"), gm.Calls())
	}
	if !strings.Contains(got, fmt.Sprintf("Added warden role(s) to %d user(s)", maxBulkAddEntries)) {
		t.Fatalf("expected all %d real entries added, got %q", maxBulkAddEntries, got)
	}
}

// splitCommaSeparated's trim-and-drop-blank contract is load-bearing for the
// bulkadd cap (the cap counts what this returns). Pin it directly.
func TestSplitCommaSeparated_TrimsAndDropsBlanks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty string", "", nil},
		{"only commas and spaces", " , ,,  ,", nil},
		{"interior and trailing blanks", "a,,b, ,c", []string{"a", "b", "c"}},
		{"surrounding whitespace trimmed", "  a , b ,c  ", []string{"a", "b", "c"}},
		{"single entry no commas", "solo", []string{"solo"}},
		{"leading and trailing commas", ",a,b,", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitCommaSeparated(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitCommaSeparated(%q) = %v (len %d), want %v (len %d)", tc.in, got, len(got), tc.want, len(tc.want))
			}
			for idx := range tc.want {
				if got[idx] != tc.want[idx] {
					t.Fatalf("splitCommaSeparated(%q)[%d] = %q, want %q", tc.in, idx, got[idx], tc.want[idx])
				}
			}
		})
	}
}
