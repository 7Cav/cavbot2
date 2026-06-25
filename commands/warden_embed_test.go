package commands

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// makeMembersWithIDLen builds n members whose user IDs all have the given
// number of digits, so the rendered line length (`<@ID>\n`) is identical for
// every member. That fixed width lets a test compute the exact truncation
// boundary, and therefore the exact "and N more" count, by hand.
func makeMembersWithIDLen(n, idLen int) []*discordgo.Member {
	members := make([]*discordgo.Member, n)
	for i := range members {
		id := fmt.Sprintf("%0*d", idLen, i)
		members[i] = &discordgo.Member{User: &discordgo.User{ID: id}}
	}
	return members
}

// TestBuildAddedMembersEmbedTruncationCount pins the exact "and N more" count
// when the description overflows Discord's 4096-char limit. The line format is
// `<@ID>\n`; with 18-digit IDs every line is 22 chars, so 186 lines fit
// (186*22 = 4092 <= 4096) and the 187th would overflow (4092+22 = 4114). With
// 200 members, exactly 186 render and 14 remain. The assertion is intentionally
// exact so a count derived from anything other than the true rendered count
// (e.g. newline counting that diverges from the per-line format) would fail.
func TestBuildAddedMembersEmbedTruncationCount(t *testing.T) {
	const idLen = 18 // line = "<@" + 18 + ">\n" = 22 chars
	const total = 200

	embed := buildAddedMembersEmbed(makeMembersWithIDLen(total, idLen))

	const lineLen = idLen + 4 // "<@" + id + ">\n"
	const maxDescLen = 4096
	wantRendered := maxDescLen / lineLen // 186
	wantRemaining := total - wantRendered // 14

	wantSuffix := fmt.Sprintf("... and %d more.", wantRemaining)
	if !strings.HasSuffix(embed.Description, wantSuffix) {
		t.Fatalf("description should end with %q\ngot tail: %q",
			wantSuffix, tail(embed.Description, 60))
	}

	// Cross-check the accounting: the number of rendered mention lines plus the
	// reported remaining count must equal the total, with no off-by-one.
	gotRendered := strings.Count(embed.Description, "<@")
	if gotRendered != wantRendered {
		t.Fatalf("rendered mention count = %d, want %d", gotRendered, wantRendered)
	}
	if gotRendered+wantRemaining != total {
		t.Fatalf("rendered (%d) + remaining (%d) = %d, want total %d",
			gotRendered, wantRemaining, gotRendered+wantRemaining, total)
	}
}

// TestBuildAddedMembersEmbedTruncationBoundary checks the exact truncation
// boundary at a second, different line width. With 10-digit IDs each line is
// 14 chars, so 292 lines fit (292*14 = 4088 <= 4096) and the 293rd overflows
// (4088+14 = 4102). With 300 members, exactly 292 render and 8 remain. Using a
// different width than the primary test guards against a count formula that
// happens to be right only at one specific line length.
func TestBuildAddedMembersEmbedTruncationBoundary(t *testing.T) {
	const idLen = 10 // line = "<@" + 10 + ">\n" = 14 chars
	const total = 300

	embed := buildAddedMembersEmbed(makeMembersWithIDLen(total, idLen))

	const lineLen = idLen + 4
	const maxDescLen = 4096
	wantRendered := maxDescLen / lineLen  // 292
	wantRemaining := total - wantRendered // 8

	wantSuffix := fmt.Sprintf("... and %d more.", wantRemaining)
	if !strings.HasSuffix(embed.Description, wantSuffix) {
		t.Fatalf("description should end with %q\ngot tail: %q",
			wantSuffix, tail(embed.Description, 60))
	}
	if got := strings.Count(embed.Description, "<@"); got != wantRendered {
		t.Fatalf("rendered mention count = %d, want %d", got, wantRendered)
	}
}

// TestBuildAddedMembersEmbedExactlyAtLimit checks the case where the rendered
// lines exactly fill the limit with no member left over: there must be no
// "and N more" line. With 18-digit IDs (22-char lines) and exactly 186 members,
// 186*22 = 4092 chars are used and nothing overflows.
func TestBuildAddedMembersEmbedExactlyAtLimit(t *testing.T) {
	members := makeMembersWithIDLen(186, 18)
	embed := buildAddedMembersEmbed(members)

	if strings.Contains(embed.Description, "more.") {
		t.Fatalf("a list that exactly fits should not be truncated, got tail: %q",
			tail(embed.Description, 60))
	}
	if got := strings.Count(embed.Description, "<@"); got != len(members) {
		t.Fatalf("rendered mention count = %d, want %d", got, len(members))
	}
}

// TestBuildAddedMembersEmbedSingleOverflow checks a single member over the
// boundary: 187 members of width 22 means the 187th overflows, so 186 render
// and the count is exactly 1.
func TestBuildAddedMembersEmbedSingleOverflow(t *testing.T) {
	members := makeMembersWithIDLen(187, 18)
	embed := buildAddedMembersEmbed(members)

	if want := "... and 1 more."; !strings.HasSuffix(embed.Description, want) {
		t.Fatalf("description should end with %q\ngot tail: %q",
			want, tail(embed.Description, 60))
	}
	if got := strings.Count(embed.Description, "<@"); got != 186 {
		t.Fatalf("rendered mention count = %d, want 186", got)
	}
}

// TestBuildAddedMembersEmbedNoTruncation confirms the happy path: when every
// member fits, there is no "and N more" line and all mentions are present.
func TestBuildAddedMembersEmbedNoTruncation(t *testing.T) {
	members := makeMembersWithIDLen(5, 18)
	embed := buildAddedMembersEmbed(members)

	if strings.Contains(embed.Description, "more.") {
		t.Fatalf("short list should not be truncated, got: %q", embed.Description)
	}
	if got := strings.Count(embed.Description, "<@"); got != len(members) {
		t.Fatalf("rendered mention count = %d, want %d", got, len(members))
	}
	if want := "Added 5 user(s)"; embed.Title != want {
		t.Fatalf("title = %q, want %q", embed.Title, want)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
