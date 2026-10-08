package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
)

// The Foxhole page's paste box (spec #434, "Add members: the paste box"): a
// Foxhole manager pastes one member a line, as Discord IDs, mentions,
// usernames, server nicknames or global names, and previews who each line
// matched before anything reaches Discord. Matching reads the member list
// alone, never Discord's member search, which is a capped prefix match and
// would answer differently from the list.

// The paste box's addresses: the Preview its form posts to, and the add
// the add preview's Confirm starts. Both POSTs, since a whole allied
// regiment's paste can outgrow an address.
const (
	foxholeAddPreviewPath = "/foxhole/add/preview"
	foxholeAddPath        = "/foxhole/add"
)

// fieldLines is the paste box's field beside fieldRole: the lines pasted.
// The add preview's Confirm posts the lines it previewed under it, beside
// who each line named, as fieldNamed, and a pick row's choice picked, as
// fieldPick and the row's line number.
const (
	fieldLines = "lines"
	fieldNamed = "named"
	fieldPick  = "pick-"
)

// pasteBox is the paste box as the page shows it: the role picked and the
// lines pasted. AbovePreview is the box above an add preview, whose button
// previews again.
type pasteBox struct {
	Role         commands.FoxholeRole
	Text         string
	AbovePreview bool
}

// addPreview is the preview an add waits on: the role it gives, the paste
// box above it, and a row per pasted line, in the order pasted.
type addPreview struct {
	Role  commands.FoxholeRole
	Name  string
	Paste pasteBox
	Rows  []addRow
	// Notes counts the members the preview names who have a note, each once
	// however many lines name them.
	Notes int
}

// lineResult is what an add does with one pasted line, its data-result
// marker.
type lineResult string

const (
	// lineGets is a line whose member gets the role.
	lineGets lineResult = "gets"
	// lineHolding is a line whose member already holds the role.
	lineHolding lineResult = "holding"
	// lineNoMatch is a line no member's name matches. It and the two
	// results after it are reasons a line adds nobody, which the add's report
	// gives under the same codes.
	lineNoMatch = lineResult(commands.LineNoMatch)
	// lineNoSuchID is a line naming a Discord ID no member of the server
	// has.
	lineNoSuchID = lineResult(commands.LineNoSuchID)
	// lineSameMember is a line naming the member an earlier line named.
	lineSameMember = lineResult(commands.LineSameMember)
	// linePick is a line matching two to maxChoices members, offered as
	// choices with none chosen. It adds nobody unless the manager picks one.
	linePick lineResult = "pick"
	// lineTooMany is a line matching more than maxChoices members. It offers
	// no choices and adds nobody.
	lineTooMany lineResult = "too-many"
)

// maxChoices is the most members a pick row offers. A line matching more
// asks for an @username or ID instead, so the page never shows a short list
// that hides the right member.
const maxChoices = 5

// lineOutcome is what an add does with a pasted line, or with a pick row's
// choice: the result, the line whose member a lineSameMember result names
// too, the members a linePick or lineTooMany line matched, and the role as
// the page names it.
type lineOutcome struct {
	Result   lineResult
	SameAs   int
	Matches  int
	RoleName string
}

// ResultLabel is the outcome as the preview says it.
func (o lineOutcome) ResultLabel() string {
	switch o.Result {
	case lineGets:
		return "gets " + o.RoleName
	case lineHolding:
		return "already holds " + o.RoleName
	case lineNoMatch, lineNoSuchID, lineSameMember:
		return lineReasonWords(commands.LineReason(o.Result), o.SameAs)
	case linePick:
		return fmt.Sprintf("matches %d members, pick one", o.Matches)
	case lineTooMany:
		return fmt.Sprintf("matches %d members, paste their @username or ID", o.Matches)
	}
	return string(o.Result)
}

// lineReasonWords is why a pasted line adds nobody, as both the preview and
// the add's report say it, for the reasons they word alike: no member
// matches, no member has the ID, or the line names the member line sameAs
// named.
func lineReasonWords(reason commands.LineReason, sameAs int) string {
	switch reason {
	case commands.LineNoMatch:
		return "no member matches"
	case commands.LineNoSuchID:
		return "no member with this ID in the server"
	case commands.LineSameMember:
		return fmt.Sprintf("same member as line %d", sameAs)
	}
	return string(reason)
}

// addRow is one pasted line as the add preview shows it: its number in the
// paste box, counting blank lines, its text, what the add does with it, and
// the member it matched, nil for none, or a pick row's choices.
type addRow struct {
	lineOutcome
	Line    int
	Text    string
	Member  *addMember
	Choices []addChoice
	// matches are the members the line matched.
	matches []lineMatch
}

// addChoice is one member a pick row offers, and what picking them does.
type addChoice struct {
	addMember
	lineOutcome
}

// addMember is a member a pasted line matched, as the holder list shows
// them, and what the line matched them by.
type addMember struct {
	holderRow
	Matched matchKind
}

// MatchedLabel is what the line matched the member by, as the preview says
// it.
func (m addMember) MatchedLabel() string {
	return matchLabels[m.Matched]
}

// matchKind is what a pasted line matched a member by, its data-matched
// marker.
type matchKind string

const (
	// matchedID is a line naming the member's Discord ID, bare or as a
	// mention.
	matchedID matchKind = "id"
	// matchedUsername is a line equal to the member's username.
	matchedUsername matchKind = "username"
	// matchedNickname is a line equal to the member's server nickname.
	matchedNickname matchKind = "nickname"
	// matchedGlobalName is a line equal to the member's global name.
	matchedGlobalName matchKind = "global-name"
)

// matchLabels are the kinds of match as the preview names them.
var matchLabels = map[matchKind]string{
	matchedID: "ID", matchedUsername: "username", matchedNickname: "nickname", matchedGlobalName: "global name",
}

// lineMatch is one member a pasted line matched, and by what.
type lineMatch struct {
	member commands.ListedMember
	by     matchKind
}

// matchedLine is one non-blank line of the paste box: its number in the
// box, counting blank lines, its text with the spaces around it trimmed,
// and the members it matched.
type matchedLine struct {
	number  int
	text    string
	matches []lineMatch
	// byID is a line naming a Discord ID, bare or as a mention.
	byID bool
}

// matchLines reads the paste box's lines against a complete member list.
// A blank line is no member, though it counts in the numbering. There is no
// cap on how many lines.
func matchLines(list commands.MemberListSnapshot, text string) []matchedLine {
	var lines []matchedLine
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		matches, byID := matchLine(list, line)
		lines = append(lines, matchedLine{number: i + 1, text: line, matches: matches, byID: byID})
	}
	return lines
}

// matchLine finds the members of a complete member list that a trimmed
// pasted line names, and reports whether it names a Discord ID. A mention
// names its member's ID. Otherwise the line, with one leading @ dropped,
// matches a member's ID, or the whole of their username, server nickname or
// global name, ignoring case, and never part of one. Every member it
// matches counts, whatever kind of name each matched by.
func matchLine(list commands.MemberListSnapshot, line string) ([]lineMatch, bool) {
	if id, ok := commands.MentionUserID(line); ok {
		if m, ok := list.Member(id); ok {
			return []lineMatch{{member: m, by: matchedID}}, true
		}
		return nil, true
	}
	name := strings.TrimPrefix(line, "@")
	var matches []lineMatch
	for _, m := range list.Members {
		if by, ok := matchedBy(m, name); ok {
			matches = append(matches, lineMatch{member: m, by: by})
		}
	}
	return matches, commands.IsSnowflakeID(name)
}

// matchedBy is what name matches the member by, the first in the order ID,
// username, nickname, global name, and false when it matches none.
func matchedBy(m commands.ListedMember, name string) (matchKind, bool) {
	switch {
	case m.ID == name:
		return matchedID, true
	case sameName(m.Username, name):
		return matchedUsername, true
	case sameName(m.Nick, name):
		return matchedNickname, true
	case sameName(m.GlobalName, name):
		return matchedGlobalName, true
	}
	return "", false
}

// sameName reports whether a member's name, set, is the pasted name,
// ignoring case. A member with no nickname or no global name matches no
// line by it.
func sameName(have, pasted string) bool {
	return have != "" && strings.EqualFold(have, pasted)
}

// addPreviewOf is the preview of an add of the role to the members the
// pasted lines name, read from a complete member list and the guild's
// Foxhole records. Each line keeps its place. A member who holds the role
// already is named as holding it, and a line naming a member an earlier
// line named says so. A pick row's choices are weighed against the members
// the lines matching one member name, then against the choices picked in
// the rows above it. picks holds the member picked in a pick row, by its
// line number: a pick that names none of the row's choices picks nothing.
func (s foxholeService) addPreviewOf(list commands.MemberListSnapshot, records map[string]store.FoxholeRecord, paste pasteBox, picks map[int]string) *addPreview {
	guild := s.foxholeGuildOf()
	name := paste.Role.Label()
	paste.AbovePreview = true
	preview := &addPreview{Role: paste.Role, Name: name, Paste: paste}
	// claimed is the line that named each member first.
	claimed := map[string]int{}
	for _, line := range matchLines(list, paste.Text) {
		row := addRow{lineOutcome: lineOutcome{RoleName: name, Matches: len(line.matches)}, Line: line.number, Text: line.text, matches: line.matches}
		switch n := len(line.matches); {
		case n == 0 && line.byID:
			row.Result = lineNoSuchID
		case n == 0:
			row.Result = lineNoMatch
		case n == 1:
			match := line.matches[0]
			holder := guild.rowOf(match.member, records[match.member.ID])
			row.Member = &addMember{holderRow: holder, Matched: match.by}
			row.lineOutcome = memberOutcome(holder, paste.Role, claimed)
			if row.Result != lineSameMember {
				claimed[holder.ID] = line.number
			}
		case n <= maxChoices:
			row.Result = linePick
		default:
			row.Result = lineTooMany
		}
		preview.Rows = append(preview.Rows, row)
	}
	for i := range preview.Rows {
		row := &preview.Rows[i]
		if row.Result != linePick {
			continue
		}
		for _, match := range row.matches {
			holder := guild.rowOf(match.member, records[match.member.ID])
			row.Choices = append(row.Choices, addChoice{addMember: addMember{holderRow: holder, Matched: match.by},
				lineOutcome: memberOutcome(holder, paste.Role, claimed)})
		}
		for _, choice := range row.Choices {
			if choice.ID != picks[row.Line] {
				continue
			}
			row.Member, row.lineOutcome = &choice.addMember, choice.lineOutcome
			if row.Result != lineSameMember {
				claimed[choice.ID] = row.Line
			}
		}
	}
	preview.Notes = preview.notes()
	return preview
}

// memberOutcome is what an add of the role does with a member a line names,
// given the line that named each member first: a member named before is
// the same member as that line, one holding the role already holds it, and
// anyone else gets it.
func memberOutcome(holder holderRow, role commands.FoxholeRole, claimed map[string]int) lineOutcome {
	o := lineOutcome{Result: lineGets, RoleName: role.Label()}
	if first, seen := claimed[holder.ID]; seen {
		o.Result, o.SameAs = lineSameMember, first
	} else if holder.holdsRole(role) {
		o.Result = lineHolding
	}
	return o
}

// notes counts the members the preview's rows and choices name who have a
// note, each once.
func (p *addPreview) notes() int {
	noted := map[string]bool{}
	for _, row := range p.Rows {
		if m := row.Member; m != nil && m.Note != "" {
			noted[m.ID] = true
		}
		for _, c := range row.Choices {
			if c.Note != "" {
				noted[c.ID] = true
			}
		}
	}
	return len(noted)
}

// CanAdd reports whether confirming the preview could give the role to
// anyone: a line whose member gets it, or a pick row with a choice who
// would. A preview that can't offers no Confirm.
func (p *addPreview) CanAdd() bool {
	for _, row := range p.Rows {
		if row.Result == lineGets {
			return true
		}
		for _, c := range row.Choices {
			if c.Result == lineGets {
				return true
			}
		}
	}
	return false
}

// members are the IDs of the members the add gives the role, in paste
// order, each once: those of the rows matching one member, or with a choice
// picked, who get the role or hold it already. The runner skips each who
// holds it when it reaches them.
func (p *addPreview) members() []string {
	var ids []string
	for _, row := range p.Rows {
		if row.Member != nil && (row.Result == lineGets || row.Result == lineHolding) {
			ids = append(ids, row.Member.ID)
		}
	}
	return ids
}

// addedNobody are the pasted lines that name no member the add gives the
// role, in paste order, each with why: a line no member matches, one naming
// an ID no member has, one naming a member another line named, and one
// matching several with none picked.
func (p *addPreview) addedNobody() []commands.PastedLine {
	var lines []commands.PastedLine
	for _, row := range p.Rows {
		line := commands.PastedLine{Line: row.Line, Text: row.Text}
		switch row.Result {
		case lineNoMatch, lineNoSuchID, lineSameMember:
			line.Reason, line.SameAs = commands.LineReason(row.Result), row.SameAs
		case linePick, lineTooMany:
			line.Reason, line.Matches = commands.LineNonePicked, row.Matches
		default:
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// picksOf reads the choices a posted add picked, by their pick row's line
// number.
func picksOf(form url.Values) map[int]string {
	picks := map[int]string{}
	for key, values := range form {
		line, err := strconv.Atoi(strings.TrimPrefix(key, fieldPick))
		if strings.HasPrefix(key, fieldPick) && err == nil && len(values) > 0 {
			picks[line] = values[0]
		}
	}
	return picks
}

// errAddChanged refuses an add's Confirm when a line names other members
// than the preview showed, as when a username changed hands since, so an
// add never gives the role to someone the manager didn't see. The page
// shows the preview as it stands now, to check and confirm again.
var errAddChanged = &saveRefusal{Kind: "changed", status: http.StatusConflict,
	log:     "Panel action refused: add preview out of date",
	Message: "Some lines name different members than when you previewed them, so the add didn't start. Nothing changed. Check the preview below and confirm again."}

// Named is who each line of the preview names, for the Confirm to post
// back: per line, the IDs of the members it matched, sorted, or "many" for
// a line matching more than a pick row offers. The picks don't change it.
func (p *addPreview) Named() string {
	var b strings.Builder
	for _, row := range p.Rows {
		ids := make([]string, 0, len(row.matches))
		for _, m := range row.matches {
			ids = append(ids, m.member.ID)
		}
		slices.Sort(ids)
		named := strings.Join(ids, ",")
		if row.Result == lineTooMany {
			named = "many"
		}
		fmt.Fprintf(&b, "%d=%s;", row.Line, named)
	}
	return b.String()
}

// startAdd is POST /foxhole/add, the add preview's Confirm: it matches the
// lines the preview showed again, against the member list as it stands,
// with the choices picked, and starts the add of the role to the members
// they name, as startAction says. A line that names other members than
// the preview showed starts nothing, and nor does an add that would give
// the role to nobody: each answers with the preview, the lines kept.
func (p *Panel) startAdd(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	role, ok := commands.ParseFoxholeRole(r.PostForm.Get(fieldRole))
	if !ok {
		http.Error(w, "the form names no Foxhole role, so nothing changed", http.StatusBadRequest)
		return
	}
	paste, picks := pasteBox{Role: role, Text: r.PostForm.Get(fieldLines)}, picksOf(r.PostForm)
	named := r.PostForm.Get(fieldNamed)
	// A refused add answers with its preview again, or with the paste box
	// alone while the member list is partial, so the lines pasted are kept.
	back := foxholeRequest{Filter: filterAll, Paste: &paste}
	p.startAction(w, r, sess, back, "the add", func(ctx context.Context, by commands.ForumUser) error {
		preview, err := p.foxhole.forSave(storeTimeout).confirmedAdd(ctx, paste, picks, named)
		if err != nil {
			return err
		}
		return refuseNobody(p.foxhole.actions.Add(ctx, role, preview.members(), preview.addedNobody(), by),
			fmt.Sprintf("No line gives %s to anyone, so there's nothing to add.", role.Label()))
	}, "role", role)
}

// confirmedAdd is the add the paste and the choices picked name, matched
// against the member list as it stands, which must be complete, else
// commands.ErrMemberListPartial. Its lines must name the members named
// records, as the preview's Named, else errAddChanged.
func (s foxholeService) confirmedAdd(ctx context.Context, paste pasteBox, picks map[int]string, named string) (*addPreview, error) {
	records, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	list := s.manager.MemberList(s.guildID)
	if list.Status != commands.MemberListComplete {
		return nil, commands.ErrMemberListPartial
	}
	preview := s.addPreviewOf(list, records, paste, picks)
	if preview.Named() != named {
		return nil, errAddChanged
	}
	return preview, nil
}

// previewAdd is POST /foxhole/add/preview, the paste box's Preview: the
// Foxhole page with the add preview of the lines pasted, for the role
// picked. It reads the member list and makes no Discord call.
func (p *Panel) previewAdd(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	role, ok := commands.ParseFoxholeRole(r.PostForm.Get(fieldRole))
	if !ok {
		http.Error(w, "the form names no Foxhole role, so nothing changed", http.StatusBadRequest)
		return
	}
	p.renderFoxhole(w, r, sess, http.StatusOK, foxholeRequest{Filter: filterAll, AwaitList: true,
		Paste: &pasteBox{Role: role, Text: r.PostForm.Get(fieldLines)}})
}
