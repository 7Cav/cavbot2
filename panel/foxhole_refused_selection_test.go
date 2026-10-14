package panel

import (
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/7cav/cavbot2/commands"
	"golang.org/x/net/html"
)

// The selection lasts until an Approve, a Clear approval or a removal made
// from it goes ahead. A refused Approve or Clear approval answers with the
// members the selection bar posted still ticked, under the search and the
// filter they were selected under: an Approve of Marsh, approved already
// for both roles he holds, and a Clear approval of Ash, who has no
// approval.
func TestARefusedApprovalsSaveKeepsTheSelection(t *testing.T) {
	cases := []struct {
		name   string
		op     string
		member string
	}{
		{"an Approve on a member approved for each Foxhole role they hold", opApprove, memberMarsh.ID},
		{"a Clear approval on a member with no approval", opClear, memberAsh.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newFoxholeWorld(t)
			setMembers(w, memberDoe, memberAsh, memberKestrel, memberMarsh, vanceWithInternal())
			approveThroughThePage(t, w, memberMarsh.ID)

			res := submitSelection(t, w.b, listUnderASearchAndAFilter(t, w), tc.op, tc.member)

			if res.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusConflict)
			}
			doc := parseHTML(t, res)
			if got, want := tickedRows(t, doc), []string{tc.member}; !slices.Equal(got, want) {
				t.Errorf("the refused page ticks %v, want the selection %v", got, want)
			}
			if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
				t.Errorf("the refused page lists %v, want %v under the search and the filter", got, listedUnderSearchAndFilter)
			}
		})
	}
}

// noticeReload returns the address of the member list notice's Reload link,
// and fails the test when the page has no notice or the notice no link.
func noticeReload(t *testing.T, doc *html.Node) string {
	t.Helper()
	reload := findElement(memberListNotice(t, doc), "a", "data-field", "reload")
	if reload == nil {
		t.Fatal("the member list notice has no Reload link")
	}
	return attrOf(reload, "href")
}

// An Approve pressed while the member list is partial is refused, and the
// member list notice stands in the holder list's place. Once the list is
// complete, the notice's Reload opens the holder list at its heading under
// the same search and filter, with the members the bar posted ticked.
func TestApproveRefusedWhileTheMemberListIsPartialReloadsToTheSelection(t *testing.T) {
	w := newFoxholeWorld(t)
	members := []commands.ListedMember{memberDoe, memberAsh, memberKestrel, memberMarsh, vanceWithInternal()}
	setMembers(w, members...)
	listed := listUnderASearchAndAFilter(t, w)
	w.discord.setMemberList(arrivingList)
	refused := parseHTML(t, submitSelection(t, w.b, listed, opApprove, memberAsh.ID, memberMarsh.ID))
	reload := noticeReload(t, refused)
	setMembers(w, members...)

	doc, fragment := landing(t, w.b, reload, w.b.get(reload))

	landedOnBlock(t, doc, fragment, "holders")
	if got, want := tickedRows(t, doc), sorted([]string{memberAsh.ID, memberMarsh.ID}); !slices.Equal(got, want) {
		t.Errorf("the notice's Reload %s ticks %v, want the selection %v", reload, got, want)
	}
	if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
		t.Errorf("the notice's Reload %s lists %v, want %v under the search and the filter", reload, got, listedUnderSearchAndFilter)
	}
}

// A page load that shows the member list notice answers no refusal, so its
// Reload stays the page in the view it shows, with no member and no
// fragment, even when its address names a selection, as the address Cancel
// on a remove preview opens does.
func TestMemberListNoticeOfAPageLoadReloadsNoSelection(t *testing.T) {
	w := newFoxholeWorld(t)
	w.discord.setMemberList(arrivingList)
	selection := url.Values{paramQuery: {"r"}, paramFilter: {"internal"}, fieldMember: {memberAsh.ID, memberMarsh.ID}}

	reload := noticeReload(t, parseHTML(t, w.b.get(foxholePath+"?"+selection.Encode()+"#"+holderListAnchor)))

	at, err := url.Parse(reload)
	if err != nil {
		t.Fatalf("the notice's Reload %q: %v", reload, err)
	}
	if got := at.Query()[fieldMember]; len(got) != 0 {
		t.Errorf("the notice's Reload %s names the members %v, want none", reload, got)
	}
	if at.Fragment != "" {
		t.Errorf("the notice's Reload %s opens at %q, want the top of the page", reload, at.Fragment)
	}
}

// previewMembers returns every member the remove preview lists, under Loses
// or Skipped, sorted.
func previewMembers(preview *html.Node) []string {
	var out []string
	for _, list := range []string{"loses", "skipped"} {
		out = append(out, keys(previewList(preview, list))...)
	}
	return sorted(out)
}

// A Remove pressed while the member list is partial is refused, with the
// member list notice in the holder list's place. Once the list is complete,
// the notice's Reload opens that removal's preview: the same role and
// members, under the same search and filter, with the members ticked in its
// holder list.
func TestRemoveRefusedWhileTheMemberListIsPartialReloadsToItsPreview(t *testing.T) {
	w := newFoxholeWorld(t)
	members := []commands.ListedMember{memberDoe, memberAsh, memberKestrel, memberMarsh, vanceWithInternal()}
	setMembers(w, members...)
	listed := listUnderASearchAndAFilter(t, w)
	w.discord.setMemberList(arrivingList)
	refused := parseHTML(t, submitSelection(t, w.b, listed, "external", memberAsh.ID, memberMarsh.ID))
	if findLive(refused, "", "data-error", "member-list") == nil {
		t.Fatal("the page doesn't say the removal was refused for the member list")
	}
	reload := noticeReload(t, refused)
	setMembers(w, members...)

	doc, fragment := landing(t, w.b, reload, w.b.get(reload))

	_, preview := landedOnBlock(t, doc, fragment, "remove-preview")
	if got := attrOf(preview, "data-role"); got != "external" {
		t.Errorf("the notice's Reload %s opens the preview of removing %q, want external", reload, got)
	}
	selection := sorted([]string{memberAsh.ID, memberMarsh.ID})
	if got := previewMembers(preview); !slices.Equal(got, selection) {
		t.Errorf("the preview lists %v, want the selection %v", got, selection)
	}
	if got := tickedRows(t, doc); !slices.Equal(got, selection) {
		t.Errorf("the preview page ticks %v, want the selection %v", got, selection)
	}
	if got := keys(holderRows(t, doc)); !slices.Equal(got, listedUnderSearchAndFilter) {
		t.Errorf("the preview page lists %v, want %v under the search and the filter", got, listedUnderSearchAndFilter)
	}
}
