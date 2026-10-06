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

// holderFilter is one of the holder list's filters: which holders it
// lists.
type holderFilter struct {
	name  string
	label string
	keeps func(holderRow) bool
}

// filterAll is the filter that lists every holder, the list's default.
const filterAll = "all"

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
	// Refusal is why the save the form shows was refused, nil for a form
	// the Edit link opened.
	Refusal *noteRefusal
}

// noteRefusal is a refused note save: its reason, which the page names as
// the data-error marker, the sentence the form shows, and the status the
// page answers with. A refused save writes nothing.
type noteRefusal struct {
	Kind    string
	Message string
	status  int
}

func (e *noteRefusal) Error() string { return e.Message }

// errNotHolder refuses a save that would start a note on a member who holds
// no Foxhole role: a note starts only on a holder.
var errNotHolder = &noteRefusal{Kind: "not-holder", status: http.StatusUnprocessableEntity,
	Message: "This member holds no Foxhole role, so a note can't start on them. Nothing was saved."}

// errStaleNote refuses a save from a note form another save got to first,
// so a save never writes over a note its manager didn't see. The form comes
// back with the note as it stands now loaded, and saving it again writes
// over that.
var errStaleNote = &noteRefusal{Kind: "stale", status: http.StatusConflict,
	Message: "Someone changed this note after you opened it, so yours wasn't saved. Save again to replace their note with the text in the box."}

// errNoteListPartial refuses a save that would start a note while the
// member list is partial: the save can't see whether the member holds a
// Foxhole role. An edit of a note a member has reads no list and stays
// open.
var errNoteListPartial = &noteRefusal{Kind: "member-list", status: http.StatusServiceUnavailable,
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
// the filter, from the filter links, and the member whose note form the
// page opens, from a row's Edit link.
const (
	paramQuery  = "q"
	paramFilter = "filter"
	paramNote   = "note"
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
// filter, and the member whose note form opens, empty for none. A refused
// note save's page also carries the refusal and the note as typed, which
// the form shows.
type foxholeRequest struct {
	Query   string
	Filter  string
	Note    string
	Refusal *noteRefusal
	Typed   string
}

// foxholePage is GET /foxhole, the Foxhole page. It reads the guild's roles
// and the member list from the gateway state and makes no Discord call. The
// search is a plain GET form, so it works without script.
func (p *Panel) foxholePage(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	p.renderFoxhole(w, r, sess, http.StatusOK, foxholeRequest{Query: q.Get(paramQuery), Filter: q.Get(paramFilter), Note: q.Get(paramNote)})
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
		p.pageFailed(w, sess, "foxhole page", foxholeAddress(req.Query, knownFilter(req.Filter)), err, nil)
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
	// The save runs to its end whether or not the browser waits, as a hub
	// save does.
	err := p.foxhole.forSave(storeTimeout).saveNote(context.WithoutCancel(r.Context()), in, sess.actor())
	var refusal *noteRefusal
	if errors.As(err, &refusal) {
		utils.Info("Panel Foxhole note save refused", "reason", refusal.Kind, "member_id", in.MemberID,
			"username", sess.username, "forum_user_id", sess.userID)
		p.renderFoxhole(w, r, sess, refusal.status, foxholeRequest{Query: in.Query, Filter: in.Filter, Note: in.MemberID, Refusal: refusal, Typed: in.Note})
		return
	}
	if err != nil {
		p.serverError(w, "note save", err)
		return
	}
	utils.Info("Panel Foxhole note saved", "member_id", in.MemberID, "username", sess.username, "forum_user_id", sess.userID)
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
		if err := s.refreshNames(ctx, list, records); err != nil {
			return foxholeView{}, err
		}
		view = s.holderList(list, records, req)
	}
	view.Query, view.Filter = req.Query, knownFilter(req.Filter)
	view.NoteTemplate = noteForm{Query: view.Query, Filter: view.Filter, Back: foxholeAddress(view.Query, view.Filter)}
	view.Changes = noteChangeViews(entries)
	if req.Note != "" {
		view.NoteForm = noteFormFor(req, list, s.foxholeGuildOf(), records)
	}
	return view, nil
}

// refreshNames stores the names a complete member list shows for each
// member with a record whose display name or username changed since the
// panel last saw them, so a member who leaves shows under their last
// names. A load that finds none changed writes nothing.
func (s foxholeService) refreshNames(ctx context.Context, list commands.MemberListSnapshot, records map[string]store.FoxholeMember) error {
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
	if err := s.store.SetFoxholeMemberNames(ctx, s.guildID, changed); err != nil {
		return fmt.Errorf("set Foxhole member names: %w", err)
	}
	return nil
}

// records reads the guild's Foxhole records keyed by member ID.
func (s foxholeService) records(ctx context.Context) (map[string]store.FoxholeMember, error) {
	members, err := s.store.ListFoxholeMembers(ctx, s.guildID)
	if err != nil {
		return nil, fmt.Errorf("list Foxhole members: %w", err)
	}
	out := make(map[string]store.FoxholeMember, len(members))
	for _, m := range members {
		out[m.MemberID] = m
	}
	return out, nil
}

// holderList builds the holder list from a complete member list: every
// holder, with their note and their Edit link, narrowed to the search and
// the filter.
func (s foxholeService) holderList(list commands.MemberListSnapshot, records map[string]store.FoxholeMember, req foxholeRequest) foxholeView {
	search := strings.ToLower(strings.TrimSpace(req.Query))
	filter := knownFilter(req.Filter)
	guild := s.foxholeGuildOf()
	var matched []holderRow
	holders := 0
	for _, mem := range list.Members {
		row := guild.rowOf(mem, records[mem.ID].Note)
		if !row.holds() {
			continue
		}
		holders++
		row.EditHref = noteAddress(req.Query, filter, mem.ID)
		if row.matches(search) {
			matched = append(matched, row)
		}
	}
	slices.SortFunc(matched, func(a, b holderRow) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.DisplayName), strings.ToLower(b.DisplayName)), cmp.Compare(a.ID, b.ID))
	})
	view := listView(matched, req.Query, filter)
	view.NoHolders = holders == 0
	return view
}

// noteFormFor is the note form the request opens: the member's note as the
// store holds it, under the names the member list shows, or the record's
// when the list doesn't hold them.
func noteFormFor(req foxholeRequest, list commands.MemberListSnapshot, guild foxholeGuild, records map[string]store.FoxholeMember) *noteForm {
	rec := records[req.Note]
	filter := knownFilter(req.Filter)
	form := &noteForm{MemberID: req.Note, DisplayName: rec.DisplayName, Username: rec.Username, Loaded: rec.Note, Note: rec.Note,
		Query: req.Query, Filter: filter, Back: foxholeAddress(req.Query, filter)}
	for _, mem := range list.Members {
		if mem.ID == req.Note {
			form.DisplayName, form.Username = displayName(mem), mem.Username
		}
	}
	if req.Refusal != nil {
		form.Note, form.Refusal = req.Typed, req.Refusal
	}
	return form
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
	var member *commands.ListedMember
	for _, mem := range list.Members {
		if mem.ID == in.MemberID {
			member = &mem
			names.DisplayName, names.Username = displayName(mem), mem.Username
		}
	}
	if !hasRecord && in.Loaded == "" {
		switch {
		case list.Status != commands.MemberListComplete:
			return errNoteListPartial
		case member == nil || !s.foxholeGuildOf().rowOf(*member, "").holds():
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

// knownFilter is the filter named, or filterAll for one the page doesn't
// know.
func knownFilter(filter string) string {
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
	return foxholeURL(query, filter, "")
}

// noteAddress is foxholeAddress with the member's note form open.
func noteAddress(query, filter, memberID string) string {
	return foxholeURL(query, filter, memberID)
}

// foxholeURL is the Foxhole page's address with the search, the filter and
// the member whose note form opens, leaving out each that is the default.
func foxholeURL(query, filter, note string) string {
	v := url.Values{}
	if filter != filterAll {
		v.Set(paramFilter, filter)
	}
	if query != "" {
		v.Set(paramQuery, query)
	}
	if note != "" {
		v.Set(paramNote, note)
	}
	if len(v) == 0 {
		return foxholePath
	}
	return foxholePath + "?" + v.Encode()
}
