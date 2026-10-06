package panel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// foxholeView is what the Foxhole page renders from: the holder list, read
// from the gateway state at this load, narrowed to the search and the
// filter, the note form the page opens, and the page's change log.
type foxholeView struct {
	// ListReady is the member list complete at this load. Until it is, a
	// notice takes the holder list's place, so the page never shows a
	// partial list.
	ListReady bool
	// Query is the search as typed, rendered back into the search box.
	Query string
	// Filter names the filter the list shows: filterAll when the address
	// names none, or one the page doesn't know.
	Filter string
	// Filters are the filter links, each with how many holders matching the
	// search it lists.
	Filters []filterLink
	Holders []holderRow
	// NoHolders is nobody holding a Foxhole role at this load, whatever the
	// search. The empty list then says so, instead of saying no holder
	// matches.
	NoHolders bool
	// NoRoleLink is the link to the no-role view after the filter links,
	// with how many members matching the search the view lists.
	NoRoleLink filterLink
	// NoRoleView is the page showing the no-role view in the holder list's
	// place, and NoRoleRows its rows: every member with a note who isn't on
	// the holder list, matching the search.
	NoRoleView bool
	NoRoleRows []holderRow
	// NoNotes is nobody in the no-role view at this load, whatever the
	// search.
	NoNotes bool
	// AlsoMatches is the line under a search's results counting the other
	// view's matches, with a link there. Nil with no search or no match
	// there.
	AlsoMatches *alsoMatches
	// NoteForm is the note form the page opens above the holder list: the
	// one a holder row's Edit link names, which is how a browser with no
	// script edits a note. Nil when the page opens none.
	NoteForm *noteForm
	// NoteTemplate is the same form with no member, for the page's script
	// to open in a row's note cell.
	NoteTemplate noteForm
	// Changes is the Foxhole page's change log, newest first. It reads no
	// member list, so it shows whatever the list's state.
	Changes []noteChangeView
	// Cleared is the result of the save before this load, when it cleared a
	// member's note and the page lists them no more: the change log's newest
	// entry about them, which names them, since their record is gone. Nil
	// for none.
	Cleared *noteChangeView
}

// filterLink is one filter link above the holder list. Each keeps the
// search.
type filterLink struct {
	Name  string
	Label string
	Count int
	Href  string
	On    bool
}

// alsoMatches is the line under a search's results counting the members
// the other view lists for the same search, linking there with the search
// kept.
type alsoMatches struct {
	Count int
	// ToNoRoleView is a line in the holder list, pointing at the no-role
	// view. False is one in the no-role view, pointing back at the holder
	// list.
	ToNoRoleView bool
	Href         string
}

// holderFilter is one of the holder list's filters: which holders it
// lists.
type holderFilter struct {
	name  string
	label string
	keeps func(holderRow) bool
}

// filterAll is the filter that lists every holder, the list's default.
const filterAll = "all"

// filterNoRole names the no-role view: the members with a note who aren't
// on the holder list. It rides the filter parameter, so the search form,
// the Edit links and a note save keep the view, but it filters nothing:
// its rows are members the holder list never shows.
const filterNoRole = "no-role"

// holderFilters are the holder list's filters, in the order their links
// show.
var holderFilters = []holderFilter{
	{filterAll, "All", func(holderRow) bool { return true }},
	{"internal", "Internal", func(h holderRow) bool { return h.Internal }},
	{"external", "External", func(h holderRow) bool { return h.External }},
	{"flagged", "Flagged", holderRow.flagged},
}

// holderRow is one Foxhole role holder as the holder list shows them.
type holderRow struct {
	ID string
	// DisplayName is the name the member shows in the server: their server
	// nickname, else their global name, else their username.
	DisplayName string
	Username    string
	Internal    bool
	External    bool
	// RankRole is the name of the member's rank role, the most senior on
	// the rank ladder they hold. Empty when they hold none.
	RankRole string
	// NoRankRole is the "no rank role" flag: an Internal holder with no
	// rank role. Display only; the bot acts on no flag.
	NoRankRole bool
	// NotInServer is the "not in the server" flag: a member the member list
	// doesn't hold, shown under the names the panel last saw.
	NotInServer bool
	// Note is the member's note, empty when they have none.
	Note string
	// EditHref is the row's Edit link: the page, in the view it shows, with
	// the member's note form open.
	EditHref string
}

// noteForm is the form that saves one member's note, as the page opens it.
type noteForm struct {
	MemberID    string
	DisplayName string
	Username    string
	// Loaded is the note as the store holds it at this load, which the save
	// writes over. Posted back, it lets the store refuse a save that another
	// save got to first.
	Loaded string
	// Note is what the note box holds.
	Note string
	// Query and Filter are the view the form was opened from, which the
	// save returns to.
	Query  string
	Filter string
	// Back is the address of that view, where Cancel leads.
	Back string
	// NoRoleRow is the form opened from the no-role view, whose member an
	// empty note takes off the page. The form then says so.
	NoRoleRow bool
	// Refusal is why the save the form shows was refused, nil for a form
	// the Edit link opened.
	Refusal *noteRefusal
}

// noteRefusal is a refused note save: its reason, which the page names as
// the data-error marker, the sentence the form shows, the status the page
// answers with, and the INFO line it logs, in the "Panel save refused"
// family the hub saves log under. A refused save writes nothing.
type noteRefusal struct {
	Kind    string
	Message string
	status  int
	log     string
}

func (e *noteRefusal) Error() string { return e.Message }

// errNotHolder refuses a save that would start a note on a member who holds
// no Foxhole role: a note starts only on a holder.
var errNotHolder = &noteRefusal{Kind: "not-holder", status: http.StatusUnprocessableEntity,
	log:     "Panel save refused: no Foxhole role",
	Message: "This member holds no Foxhole role, so a note can't start on them. Nothing was saved."}

// errStaleNote refuses a save from a note form another save got to first,
// so a save never writes over a note its manager didn't see. The form comes
// back with the note as it stands now loaded, and saving it again writes
// over that.
var errStaleNote = &noteRefusal{Kind: "stale", status: http.StatusConflict,
	log:     "Panel save refused: stale form",
	Message: "Someone changed this note after you opened it, so yours wasn't saved. Save again to replace their note with the text in the box."}

// errNoteListPartial refuses a save that would start a note while the
// member list is partial: the save can't see whether the member holds a
// Foxhole role. An edit of a note a member has reads no list and stays
// open.
var errNoteListPartial = &noteRefusal{Kind: "member-list", status: http.StatusServiceUnavailable,
	log:     "Panel save refused: member list partial",
	Message: "Cavbot2 doesn't have the whole member list from Discord yet, so it can't check this member holds a Foxhole role. Nothing was saved. Save again in a minute."}

// noteChangeView is one entry of the Foxhole page's change log as the page
// shows it: who saved, the member the save touched, and the note's old and
// new text.
type noteChangeView struct {
	ID       int64
	Username string
	At       time.Time
	Action   store.ChangeAction
	// MemberID, MemberName and MemberUsername name the member the save
	// touched, under the names the panel saw at the save.
	MemberID       string
	MemberName     string
	MemberUsername string
	Before         string
	After          string
}

// The Foxhole page's addresses: the page, and the note save its note form
// posts to.
const (
	foxholePath      = "/foxhole"
	foxholeNotesPath = "/foxhole/notes"
)

// The Foxhole page's query parameters: the search, from the search form,
// the filter, from the filter links, the ID of the member whose note form
// the page opens, from a row's Edit link, and the ID of the member a note
// save cleared off the page, from the save's redirect.
const (
	paramQuery      = "q"
	paramFilter     = "filter"
	paramNoteMember = "note"
	paramCleared    = "cleared"
)

// The note form's fields beside the view's query and filter: the member,
// the note as the form loaded it, and the note as saved.
const (
	fieldMember = "member"
	fieldLoaded = "loaded"
	fieldNote   = "note"
)

// foxholeService reads the Foxhole page from the gateway state, through the
// manager seam the hub page reads the guild through, and from the store,
// and saves notes.
type foxholeService struct {
	manager commands.TempVCManager
	store   store.Store
	guildID string
}

// forSave is the service as a save runs it: every store call under its
// own deadline of timeout, as a hub save's.
func (s foxholeService) forSave(timeout time.Duration) foxholeService {
	s.store = boundedStore{store: s.store, timeout: timeout}
	return s
}

// foxholeRequest is what a Foxhole page load asks for: the search, the
// filter, one the page knows, the ID of the member whose note form opens,
// empty for none, and the result of the save before it. A refused note
// save's page also carries the refusal and the note as typed, which the
// form shows.
type foxholeRequest struct {
	Query      string
	Filter     string
	NoteMember string
	Refusal    *noteRefusal
	Typed      string
	// Cleared is the ID of the member whose note the save before this load
	// cleared, taking them off the page, empty for none.
	Cleared string
}

// foxholePage is GET /foxhole, the Foxhole page. It reads the guild's roles
// and the member list from the gateway state and makes no Discord call. The
// search is a plain GET form, so it works without script.
func (p *Panel) foxholePage(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	p.renderFoxhole(w, r, sess, http.StatusOK,
		foxholeRequest{Query: q.Get(paramQuery), Filter: knownFilter(q.Get(paramFilter)), NoteMember: q.Get(paramNoteMember), Cleared: q.Get(paramCleared)})
}

// renderFoxhole renders the Foxhole page read now, under the page's time
// budget. A store read that fails gets the could-not-load page, as the hub
// page's does, and an abandoned load answers nobody.
func (p *Panel) renderFoxhole(w http.ResponseWriter, r *http.Request, sess session, status int, req foxholeRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), p.pageBudget)
	defer cancel()
	view, err := p.foxhole.view(ctx, req)
	if errors.Is(err, context.Canceled) {
		utils.Info("Panel page abandoned", "step", "foxhole page", "username", sess.username, "forum_user_id", sess.userID)
		return
	}
	if err != nil {
		p.pageFailed(w, sess, "foxhole page", foxholeAddress(req.Query, req.Filter), err, nil)
		return
	}
	data := sess.page("Foxhole")
	data.Page, data.Foxhole = pageFoxhole, view
	p.render(w, status, "foxhole", data)
}

// saveNote is POST /foxhole/notes: one service call, then a redirect to the
// view the note form was opened from. A note save changes no Discord role
// and makes no Discord call.
func (p *Panel) saveNote(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := noteInput{
		MemberID: r.PostForm.Get(fieldMember),
		Loaded:   r.PostForm.Get(fieldLoaded),
		Note:     strings.TrimSpace(r.PostForm.Get(fieldNote)),
		Query:    r.PostForm.Get(paramQuery),
		Filter:   knownFilter(r.PostForm.Get(paramFilter)),
	}
	if in.MemberID == "" {
		http.Error(w, "the form names no member", http.StatusBadRequest)
		return
	}
	if in.Note == in.Loaded {
		// The form posts the note it loaded: nothing changed, so nothing is
		// saved and the change log gains no entry.
		http.Redirect(w, r, foxholeAddress(in.Query, in.Filter), http.StatusSeeOther)
		return
	}
	// The save runs to its end whether or not the browser waits, as a hub
	// save does.
	err := p.foxhole.forSave(storeTimeout).saveNote(context.WithoutCancel(r.Context()), in, sess.actor())
	var refusal *noteRefusal
	if errors.As(err, &refusal) {
		utils.Info(refusal.log, "member_id", in.MemberID, "username", sess.username, "forum_user_id", sess.userID)
		p.renderFoxhole(w, r, sess, refusal.status, foxholeRequest{Query: in.Query, Filter: in.Filter, NoteMember: in.MemberID, Refusal: refusal, Typed: in.Note})
		return
	}
	if err != nil {
		p.serverError(w, "note save", err)
		return
	}
	utils.Info("Panel Foxhole note saved", "member_id", in.MemberID, "username", sess.username, "forum_user_id", sess.userID)
	if in.Note == "" {
		// No confirmation step. When the clear took the member off the page,
		// the page the save lands on says so.
		http.Redirect(w, r, clearedAddress(in.Query, in.Filter, in.MemberID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, foxholeAddress(in.Query, in.Filter), http.StatusSeeOther)
}

// noteInput is a note save as posted.
type noteInput struct {
	MemberID string
	Loaded   string
	Note     string
	Query    string
	Filter   string
}

// flagged reports whether the holder carries a flag.
func (h holderRow) flagged() bool { return h.NoRankRole }

// matches reports whether the holder's display name, username, Discord ID
// or note holds the search, ignoring case. An empty search matches
// everyone.
func (h holderRow) matches(search string) bool {
	return strings.Contains(strings.ToLower(h.DisplayName), search) ||
		strings.Contains(strings.ToLower(h.Username), search) ||
		strings.Contains(h.ID, search) ||
		strings.Contains(strings.ToLower(h.Note), search)
}

// foxholeGuild is what the page reads from one snapshot of the guild's
// roles: the Foxhole roles' IDs, found by exact name as the commands find
// them, and every role's name by ID.
type foxholeGuild struct {
	internalID string
	externalID string
	roleNames  map[string]string
}

// foxholeGuildOf reads the Foxhole roles from the guild's roles, through
// the manager seam, never Discord's API.
func (s foxholeService) foxholeGuildOf() foxholeGuild {
	guild := s.manager.GuildData(s.guildID)
	internalName, externalName := commands.FoxholeRoleNames()
	g := foxholeGuild{roleNames: make(map[string]string, len(guild.Roles))}
	for _, r := range guild.Roles {
		g.roleNames[r.ID] = r.Name
		switch {
		case r.Name == internalName && g.internalID == "":
			g.internalID = r.ID
		case r.Name == externalName && g.externalID == "":
			g.externalID = r.ID
		}
	}
	return g
}

// rowOf is a member as the holder list shows them, with their note.
func (g foxholeGuild) rowOf(mem commands.ListedMember, note string) holderRow {
	row := holderRow{
		ID:          mem.ID,
		DisplayName: displayName(mem),
		Username:    mem.Username,
		Internal:    g.internalID != "" && slices.Contains(mem.RoleIDs, g.internalID),
		External:    g.externalID != "" && slices.Contains(mem.RoleIDs, g.externalID),
		Note:        note,
	}
	if rank, ok := commands.SeniorRankRole(mem.RoleIDs); ok {
		row.RankRole = cmp.Or(g.roleNames[rank], rank)
	}
	row.NoRankRole = row.Internal && row.RankRole == ""
	return row
}

// holds reports whether the row's member holds a Foxhole role.
func (h holderRow) holds() bool { return h.Internal || h.External }

// memberByID finds a member in the member list. A list that isn't complete
// holds no members, so it finds nobody.
func memberByID(list commands.MemberListSnapshot, id string) (commands.ListedMember, bool) {
	for _, mem := range list.Members {
		if mem.ID == id {
			return mem, true
		}
	}
	return commands.ListedMember{}, false
}

// displayName is the name a member shows in the server: their server
// nickname, else their global name, else their username.
func displayName(mem commands.ListedMember) string {
	return cmp.Or(mem.Nick, mem.GlobalName, mem.Username)
}

// view reads the Foxhole page from one snapshot of the guild's roles and
// one of its member list, through the manager seam, never Discord's API,
// and from the store's Foxhole records and change log. The holder list
// keeps the holders who match the search and the filter named. A member
// list that isn't complete holds no members, and the page has no holder
// list.
func (s foxholeService) view(ctx context.Context, req foxholeRequest) (foxholeView, error) {
	records, err := s.records(ctx)
	if err != nil {
		return foxholeView{}, err
	}
	entries, err := s.store.ListFoxholeChanges(ctx, changeLogLimit)
	if err != nil {
		return foxholeView{}, fmt.Errorf("list Foxhole changes: %w", err)
	}
	list := s.manager.MemberList(s.guildID)
	view := foxholeView{}
	if list.Status == commands.MemberListComplete {
		// The names are for a later load, once a member has left: a write
		// that fails is reported and the page shows anyway.
		if err := s.refreshNames(ctx, list, records); err != nil && !errors.Is(err, context.Canceled) {
			utils.CaptureError("Panel Foxhole name refresh failed", err)
		}
		view = s.lists(list, records, req)
	}
	view.Query, view.Filter = req.Query, req.Filter
	view.NoteTemplate = req.blankNoteForm()
	view.Changes = noteChangeViews(entries)
	if req.Cleared != "" && s.offPage(list, records, req.Cleared) {
		view.Cleared = newestAbout(view.Changes, req.Cleared)
	}
	if req.NoteMember != "" {
		view.NoteForm = noteFormFor(req, list, records)
	}
	return view, nil
}

// refreshNames stores the names a complete member list shows for each
// member with a record whose display name or username changed since the
// panel last saw them, so a member who leaves shows under their last
// names. A load that finds none changed writes nothing.
func (s foxholeService) refreshNames(ctx context.Context, list commands.MemberListSnapshot, records map[string]store.FoxholeRecord) error {
	var changed []store.MemberNames
	for _, mem := range list.Members {
		rec, ok := records[mem.ID]
		if !ok {
			continue
		}
		if name := displayName(mem); name != rec.DisplayName || mem.Username != rec.Username {
			changed = append(changed, store.MemberNames{MemberID: mem.ID, DisplayName: name, Username: mem.Username})
		}
	}
	if len(changed) == 0 {
		return nil
	}
	if err := s.store.SetFoxholeRecordNames(ctx, s.guildID, changed); err != nil {
		return fmt.Errorf("set Foxhole record names: %w", err)
	}
	return nil
}

// records reads the guild's Foxhole records keyed by member ID.
func (s foxholeService) records(ctx context.Context) (map[string]store.FoxholeRecord, error) {
	members, err := s.store.ListFoxholeRecords(ctx, s.guildID)
	if err != nil {
		return nil, fmt.Errorf("list Foxhole records: %w", err)
	}
	out := make(map[string]store.FoxholeRecord, len(members))
	for _, m := range members {
		out[m.MemberID] = m
	}
	return out, nil
}

// lists builds the page's two lists from a complete member list: the holder
// list, every holder, and the no-role view, every member with a note who
// isn't on the holder list, the members who left the server among them
// under the names the panel last saw. Each row carries its note and its
// Edit link. Both lists are narrowed to the search, the holder list to the
// filter too, and the page shows the one the request names.
func (s foxholeService) lists(list commands.MemberListSnapshot, records map[string]store.FoxholeRecord, req foxholeRequest) foxholeView {
	guild := s.foxholeGuildOf()
	var holders, noRole []holderRow
	inServer := make(map[string]commands.ListedMember, len(list.Members))
	listed := map[string]bool{}
	for _, mem := range list.Members {
		inServer[mem.ID] = mem
		if row := guild.rowOf(mem, records[mem.ID].Note); row.holds() {
			holders = append(holders, row)
			listed[mem.ID] = true
		}
	}
	for id, rec := range records {
		if rec.Note == "" || listed[id] {
			continue
		}
		row := holderRow{ID: id, DisplayName: rec.DisplayName, Username: rec.Username, Note: rec.Note, NotInServer: true}
		if mem, ok := inServer[id]; ok {
			row = guild.rowOf(mem, rec.Note)
		}
		noRole = append(noRole, row)
	}
	matchedHolders, matchedNoRole := matching(holders, req), matching(noRole, req)
	noRoleView := req.Filter == filterNoRole
	view := listView(matchedHolders, req.Query, req.Filter)
	view.NoHolders = len(holders) == 0
	view.NoRoleLink = filterLink{Name: filterNoRole, Label: "No role, with a note", Count: len(matchedNoRole),
		Href: foxholeAddress(req.Query, filterNoRole), On: noRoleView}
	if noRoleView {
		view.NoRoleView, view.NoRoleRows, view.NoNotes = true, matchedNoRole, len(noRole) == 0
	}
	// A search runs in the view the manager is on, and says under its
	// results when the other view matches too, whether or not this one did.
	switch {
	case strings.TrimSpace(req.Query) == "":
	case noRoleView && len(matchedHolders) > 0:
		view.AlsoMatches = &alsoMatches{Count: len(matchedHolders), Href: foxholeAddress(req.Query, filterAll)}
	case !noRoleView && len(matchedNoRole) > 0:
		view.AlsoMatches = &alsoMatches{Count: len(matchedNoRole), ToNoRoleView: true, Href: foxholeAddress(req.Query, filterNoRole)}
	}
	return view
}

// matching keeps the rows that hold the request's search, each with its
// Edit link, in display name order.
func matching(rows []holderRow, req foxholeRequest) []holderRow {
	search := strings.ToLower(strings.TrimSpace(req.Query))
	var matched []holderRow
	for _, row := range rows {
		if row.matches(search) {
			row.EditHref = foxholeURL(req.Query, req.Filter, row.ID)
			matched = append(matched, row)
		}
	}
	slices.SortFunc(matched, func(a, b holderRow) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)), cmp.Compare(a.ID, b.ID))
	})
	return matched
}

// noteFormFor is the note form the request opens: the member's note as the
// store holds it, under the names the member list shows, or the record's
// when the list doesn't hold them.
func noteFormFor(req foxholeRequest, list commands.MemberListSnapshot, records map[string]store.FoxholeRecord) *noteForm {
	rec := records[req.NoteMember]
	form := req.blankNoteForm()
	form.MemberID, form.DisplayName, form.Username, form.Loaded, form.Note = req.NoteMember, rec.DisplayName, rec.Username, rec.Note, rec.Note
	if mem, ok := memberByID(list, req.NoteMember); ok {
		form.DisplayName, form.Username = displayName(mem), mem.Username
	}
	if req.Refusal != nil {
		form.Note, form.Refusal = req.Typed, req.Refusal
	}
	return &form
}

// blankNoteForm is the note form for the request's view with no member:
// saved, it returns to that view, and opened from the no-role view it says
// an empty note takes the member off the page.
func (req foxholeRequest) blankNoteForm() noteForm {
	return noteForm{Query: req.Query, Filter: req.Filter, Back: foxholeAddress(req.Query, req.Filter), NoRoleRow: req.Filter == filterNoRole}
}

// saveNote saves a member's note over the note the form loaded, and its
// change log entry with it, under the names the member list shows now, or
// the record's when the list doesn't hold the member. A save that starts a
// note, on a member with no record, must find them holding a Foxhole role.
func (s foxholeService) saveNote(ctx context.Context, in noteInput, by actor) error {
	records, err := s.records(ctx)
	if err != nil {
		return err
	}
	rec, hasRecord := records[in.MemberID]
	names := store.MemberNames{MemberID: in.MemberID, DisplayName: rec.DisplayName, Username: rec.Username}
	list := s.manager.MemberList(s.guildID)
	member, inList := memberByID(list, in.MemberID)
	if inList {
		names.DisplayName, names.Username = displayName(member), member.Username
	}
	if !hasRecord && in.Loaded == "" {
		switch {
		case list.Status != commands.MemberListComplete:
			return errNoteListPartial
		case !inList || !s.foxholeGuildOf().rowOf(member, "").holds():
			return errNotHolder
		}
	}
	raw, err := json.Marshal(noteDiff{
		Member: noteMember{ID: in.MemberID, DisplayName: names.DisplayName, Username: names.Username},
		Note:   noteText{Before: in.Loaded, After: in.Note},
	})
	if err != nil {
		return fmt.Errorf("encode note change: %w", err)
	}
	entry := store.ChangeLogEntry{ForumUserID: by.userID, ForumUsername: by.username, Action: store.ChangeNote, Diff: raw}
	save := store.NoteSave{MemberID: in.MemberID, Before: in.Loaded, Note: in.Note, DisplayName: names.DisplayName, Username: names.Username}
	err = s.store.SaveFoxholeNote(ctx, s.guildID, save, entry)
	if errors.Is(err, store.ErrStale) {
		return errStaleNote
	}
	return err
}

// noteDiff is a note save's change log diff: the member the save touched,
// under the names the panel saw then, and the note's old and new text. An
// empty note is no note.
type noteDiff struct {
	Member noteMember `json:"member"`
	Note   noteText   `json:"note"`
}

type noteMember struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
}

type noteText struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// noteChangeViews decodes the Foxhole change log's entries for the page. An
// entry whose diff does not decode is shown with no member and no text
// rather than dropped: the save happened.
func noteChangeViews(entries []store.ChangeLogEntry) []noteChangeView {
	views := make([]noteChangeView, 0, len(entries))
	for _, e := range entries {
		v := noteChangeView{ID: e.ID, Username: e.ForumUsername, At: e.At, Action: e.Action}
		var d noteDiff
		if err := json.Unmarshal(e.Diff, &d); err == nil {
			v.MemberID, v.MemberName, v.MemberUsername = d.Member.ID, d.Member.DisplayName, d.Member.Username
			v.Before, v.After = d.Note.Before, d.Note.After
		}
		views = append(views, v)
	}
	return views
}

// offPage reports whether the page lists the member nowhere at this load:
// they have no record, so no note for the no-role view, and a complete
// member list shows them holding no Foxhole role. A list that isn't
// complete can't tell.
func (s foxholeService) offPage(list commands.MemberListSnapshot, records map[string]store.FoxholeRecord, memberID string) bool {
	if _, ok := records[memberID]; ok || list.Status != commands.MemberListComplete {
		return false
	}
	mem, inList := memberByID(list, memberID)
	return !inList || !s.foxholeGuildOf().rowOf(mem, "").holds()
}

// newestAbout is the newest entry of the change log about the member, nil
// when the log shows none.
func newestAbout(changes []noteChangeView, memberID string) *noteChangeView {
	for i := range changes {
		if changes[i].MemberID == memberID {
			return &changes[i]
		}
	}
	return nil
}

// knownFilter is the filter named, the no-role view included, or filterAll
// for one the page doesn't know.
func knownFilter(filter string) string {
	if filter == filterNoRole {
		return filterNoRole
	}
	for _, f := range holderFilters {
		if f.name == filter {
			return f.name
		}
	}
	return filterAll
}

// listView builds the page from the holders matching the search: a link for
// each filter with its count, and the holders the named filter keeps.
func listView(matched []holderRow, query, filter string) foxholeView {
	page := foxholeView{ListReady: true, Query: query, Filter: filter}
	for _, f := range holderFilters {
		link := filterLink{Name: f.name, Label: f.label, Href: foxholeAddress(query, f.name), On: f.name == page.Filter}
		for _, h := range matched {
			if f.keeps(h) {
				link.Count++
				if link.On {
					page.Holders = append(page.Holders, h)
				}
			}
		}
		page.Filters = append(page.Filters, link)
	}
	return page
}

// foxholeAddress is the Foxhole page's address with the search and the
// filter, leaving out each that is the default.
func foxholeAddress(query, filter string) string {
	return foxholeLink(query, filter, "", "")
}

// foxholeURL is foxholeAddress with the note form of the member whose ID
// is noteMember open, none when it is empty.
func foxholeURL(query, filter, noteMember string) string {
	return foxholeLink(query, filter, paramNoteMember, noteMember)
}

// clearedAddress is foxholeAddress after a save that cleared the note of
// the member whose ID is member. The page says so when that took them off
// it.
func clearedAddress(query, filter, member string) string {
	return foxholeLink(query, filter, paramCleared, member)
}

// foxholeLink is foxholeAddress with the parameter key set to value, left
// out when value is empty.
func foxholeLink(query, filter, key, value string) string {
	v := url.Values{}
	if filter != filterAll {
		v.Set(paramFilter, filter)
	}
	if query != "" {
		v.Set(paramQuery, query)
	}
	if value != "" {
		v.Set(key, value)
	}
	if len(v) == 0 {
		return foxholePath
	}
	return foxholePath + "?" + v.Encode()
}
