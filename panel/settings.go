package panel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The Settings page (#680, GLOSSARY.md) is where a panel admin sets the
// guild-wide settings that belong to no hub. Its Rank roles section sets the
// role that counts as each rank on the rank ladder.

// settingsPath is the Settings page's address.
const settingsPath = "/settings"

// settingsView is what the Settings page renders from.
type settingsView struct {
	// Disconnected is the bot's gateway connection down at this load, as on
	// the hub page.
	Disconnected bool
	RankRoles    rankRolesPage
	// RecordingRoles is the recording roles section below it.
	RecordingRoles recordingRolesPage
	Error          *fieldError
	// Refused is the form Error belongs to.
	Refused string
}

// RefusalFor is the refusal to render on a form, or nil when the error
// belongs to another form or there is none, as on the hub page.
func (v settingsView) RefusalFor(form string) *fieldError {
	if v.Refused != form {
		return nil
	}
	return v.Error
}

// rankRoleOptions is the ID of the one list of roles every rank's picker
// offers. The section renders it once rather than once per rank, since the
// live guild is near Discord's cap of 250 roles.
const rankRoleOptions = "rank-role-options"

// rankRolesPage is the Rank roles section as the page renders it: one row
// per rank, most senior first, the roles every row's picker offers, and the
// version the section posts.
type rankRolesPage struct {
	Version string
	Rows    []rankRow
	// Preview is the preview a save waits on, shown above the form as
	// posted, nil for none.
	Preview *rankRolesPreviewView
	// Options are the eligible roles, highest position first, which each
	// rank's picker offers from the list under OptionsID.
	Options   []pickerItem
	OptionsID string
	// Changes are the section's last entries, newest first.
	Changes []changeView
	// Saved is the section showing a save of it just made.
	Saved bool
	// Posted is the form showing the rank roles as a save posted them, or
	// as the preview's address carries them, rather than as stored.
	Posted bool
}

// rankRolesPreviewView is a preview as the section shows it. It holds the
// counts, and the form as the save posted it, which its Confirm posts.
type rankRolesPreviewView struct {
	rankRolesPreview
	Version string
	Fields  []rankRoleFieldValue
	// Refusal is a refused Confirm's, shown on the preview it shows again.
	Refusal *fieldError
}

// rankRoleFieldValue is one rank's role as a preview's Confirm posts it.
type rankRoleFieldValue struct {
	Field  string
	RoleID string
}

// rankRow is one rank's row. It has the rank's abbreviation, a picker
// holding its rank role as the one tag or none, and how many members hold
// that role.
type rankRow struct {
	Abbrev string
	Picker pickerView
	// HasRole is whether the rank has a rank role stored, whose holders
	// Holders counts.
	HasRole bool
	// Holders is the count of the members holding the rank's stored role,
	// from the member list, or unknown while the list has not loaded.
	Holders string
}

// holdersUnknown is a row's holder count while the member list has not
// loaded, never 0.
const holdersUnknown = "unknown"

// HoldersKnown reports whether the row's holder count is a number, which it
// is once the member list has loaded.
func (r rankRow) HoldersKnown() bool { return r.Holders != holdersUnknown }

// roleHolders counts the members of a complete member list holding each
// role, by role ID. It is nil while the list has not loaded.
func roleHolders(list commands.MemberListSnapshot) map[string]int {
	if list.Status != commands.MemberListComplete {
		return nil
	}
	counts := map[string]int{}
	for _, m := range list.Members {
		for _, id := range m.RoleIDs {
			counts[id]++
		}
	}
	return counts
}

// rankRoleSet is a rank's role as a picker's set, the one ID or none.
func rankRoleSet(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

// rankRoleFieldPrefix starts the form field each rank's rank role posts
// under, which a rank roles save's diff keys the rank by.
const rankRoleFieldPrefix = "rank_role_"

// rankRoleField is the form field a rank's rank role posts under.
func rankRoleField(abbrev string) string { return rankRoleFieldPrefix + abbrev }

// The names the Settings page's store reads' errors carry, as the hub
// page's do.
const (
	rankRolesRead       = "read rank roles"
	rankRoleChangesRead = "list rank roles changes"
)

// settingsPage is GET /settings. A save named, with the version it left
// its form's record at, has that form say it saved. The rank roles
// preview's address shows that preview over the form as the address
// carries it.
func (p *Panel) settingsPage(w http.ResponseWriter, r *http.Request, sess session) {
	q := r.URL.Query()
	req := pageRequest{Saved: savedFromQuery(q.Get(paramSaved))}
	// A version that isn't a number matches no record, so the page says
	// nothing saved.
	req.SavedVersion, _ = strconv.ParseInt(q.Get(paramVersion), 10, 64)
	if q.Get(paramPreview) == formRankRoles {
		in := rankRolesInputOf(q)
		req.RankRoles, req.PreviewRankRoles = &in, true
	}
	p.renderSettings(w, r, sess, http.StatusOK, req)
}

// paramPreview is the query parameter naming the form whose preview the
// Settings page's address opens.
const paramPreview = "preview"

// rankRolesPreviewAddress is the address of the preview a rank roles save
// waits on (ADR 0019): the Settings page with the form as the save posted
// it, each rank's role under its form field and the version, opened at the
// preview. A rank with no role posts none, and the address names none for
// it. With every rank filled the address is about a thousand characters,
// well under the request line the proxy in front of the panel allows.
func rankRolesPreviewAddress(in rankRolesInput) string {
	q := url.Values{paramPreview: {formRankRoles}, fieldVersion: {in.Version}}
	for _, rank := range commands.DefaultRankLadder().Ranks() {
		if id := in.Roles[rank.Abbrev]; id != "" {
			q.Set(rankRoleField(rank.Abbrev), id)
		}
	}
	return (&url.URL{Path: settingsPath, RawQuery: q.Encode(), Fragment: formRankRoles}).String()
}

// renderSettings renders the Settings page read now, with the form the
// request names as posted and its refusal, under the hub page's time
// budget and with its failures answered the way the hub page answers its
// own.
func (p *Panel) renderSettings(w http.ResponseWriter, r *http.Request, sess session, status int, req pageRequest) {
	start := panelClock.Now()
	ctx, cancel := panelClock.WithTimeout(r.Context(), hubPageBudget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	reads := newPageReads(start, deadline)
	view, err := p.hubs.forPage(reads).settings(ctx, req)
	switch {
	case errors.Is(err, context.Canceled):
		utils.Info("Panel page abandoned", "step", "settings page", "username", sess.username, "forum_user_id", sess.userID)
		return
	case errors.Is(err, errNoGuildData):
		p.noGuildData(w, r, sess, "settings page", settingsPath, err)
		return
	case err != nil:
		var report *budgetReport
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			report = reads.report(hubPageBudget)
		}
		p.pageFailed(w, r, sess, "settings page", settingsPath, err, report)
		return
	}
	data := sess.page("Settings")
	data.Page, data.Settings, data.Address = pageSettings, view, req.settingsAddress()
	p.render(w, r, status, "settings", data)
}

// settingsAddress is the GET address that shows the Settings page the
// request asks for as it stands then, for a page that answers a form (ADR
// 0019): the page opened at the section whose save was refused, or at the
// rank roles preview the page shows. The refusal and any other form as
// posted came with the POST, so they don't come back.
func (req pageRequest) settingsAddress() string {
	if req.RankRolesPreview != nil && req.RankRoles != nil {
		return rankRolesPreviewAddress(*req.RankRoles)
	}
	return (&url.URL{Path: settingsPath, Fragment: req.Refused}).String()
}

// settings reads everything the Settings page renders from, once. It reads
// the guild from the gateway state, then the rank roles and the recording
// roles from the store.
func (s *hubService) settings(ctx context.Context, req pageRequest) (settingsView, error) {
	guild, err := s.readGuild(ctx)
	if err != nil {
		return settingsView{}, err
	}
	view := settingsView{Disconnected: guild.disconnected, Error: req.Error, Refused: req.Refused}
	if view.RankRoles, err = s.rankRolesSection(ctx, guild.info, req); err != nil {
		return settingsView{}, err
	}
	if view.RecordingRoles, err = s.recordingRolesSection(ctx, guild.info, req); err != nil {
		return settingsView{}, err
	}
	return view, nil
}

// rankRolesSection builds the Rank roles section from the guild read and
// the store. Each rank shows the rank role the stored ladder gives it, the
// role IDs in code while nothing is saved. After a refusal or with a
// preview it shows the form as posted, with the version postedBackVersion
// gives it.
func (s *hubService) rankRolesSection(ctx context.Context, guild guildInfo, req pageRequest) (rankRolesPage, error) {
	stored, err := s.deps.Store.GetRankRoles(ctx, s.deps.GuildID)
	if err != nil {
		return rankRolesPage{}, fmt.Errorf("%s: %w", rankRolesRead, err)
	}
	page := rankRolesPage{Version: strconv.FormatInt(stored.Version, 10), OptionsID: rankRoleOptions,
		Saved: req.savedAt(formRankRoles, stored.Version)}
	posted := req.RankRoles
	if posted != nil {
		page.Version, page.Posted = postedBackVersion(stored.Version, posted.Version, req.staleFor(formRankRoles)), true
	}
	holders := roleHolders(s.deps.Manager.MemberList(s.deps.GuildID))
	for _, rank := range commands.RankLadderOf(stored).Ranks() {
		roles, selected := rankRoleSet(rank.RoleID), rankRoleSet(rank.RoleID)
		if posted != nil {
			selected = rankRoleSet(posted.Roles[rank.Abbrev])
		}
		picker := rolePicker(rankRoleField(rank.Abbrev), "the "+rank.Abbrev+" rank", guild, roles, selected)
		picker.Single, picker.AddLabel, picker.Candidates, picker.OptionsFrom = true, "Choose a role", nil, rankRoleOptions
		row := rankRow{Abbrev: rank.Abbrev, Picker: picker, HasRole: rank.RoleID != "", Holders: holdersUnknown}
		if holders != nil {
			row.Holders = strconv.Itoa(holders[rank.RoleID])
		}
		page.Rows = append(page.Rows, row)
	}
	page.Options = rolePicker("", "", guild, nil, nil).Candidates
	entries, err := s.deps.Store.ListRankRoleChanges(ctx, changeLogLimit)
	if err != nil {
		return rankRolesPage{}, fmt.Errorf("%s: %w", rankRoleChangesRead, err)
	}
	page.Changes = changeViews(entries, guild)
	preview := req.RankRolesPreview
	if req.PreviewRankRoles && posted != nil {
		// The preview's address shows the preview whatever the counts are
		// now, none dropped included, so a reload of it never saves.
		counts := s.rankRolesCounts(commands.RankLadderOf(stored), commands.NewRankLadder(posted.Roles))
		preview = &counts
	}
	if preview != nil && posted != nil {
		view := &rankRolesPreviewView{rankRolesPreview: *preview, Version: posted.Version, Refusal: req.refusalOn(formRankRoles)}
		for _, rank := range commands.DefaultRankLadder().Ranks() {
			if id := posted.Roles[rank.Abbrev]; id != "" {
				view.Fields = append(view.Fields, rankRoleFieldValue{Field: rankRoleField(rank.Abbrev), RoleID: id})
			}
		}
		page.Preview = view
	}
	return page, nil
}

// rankRolesInput is the Rank roles section's form as posted. It carries the
// ladder's version when the section loaded and each rank's role, empty for
// none. Confirmed marks the post of a preview's Confirm, and Shown is the
// counts that preview showed.
type rankRolesInput struct {
	Version   string
	Roles     map[string]string
	Confirmed bool
	Shown     string
}

// errStaleRankRoles is the refusal a save from a stale Rank roles form
// gets, answered the way errStaleRecordingRoles is.
var errStaleRankRoles = &fieldError{kind: refusalStale, Message: "Someone saved the rank roles after you opened this page, " +
	"so your changes were not saved. Their save is at the top of the change log below. " +
	"Your roles are still in the form. Save again to keep them."}

// errRankRolesUnchanged is the refusal a rank roles save gets when it posts
// the ladder as stored. A save that would change nothing says so and writes
// nothing, not even a change log entry.
var errRankRolesUnchanged = &fieldError{kind: refusalUnchanged, Message: "Those are already the rank roles, so nothing was saved."}

// fieldConfirm is the hidden input a rank roles preview's Confirm posts,
// and fieldShown the one carrying the counts that preview showed.
const (
	fieldConfirm = "confirm"
	fieldShown   = "shown"
)

// errRankRolesChanged refuses a rank roles preview's Confirm when the save
// would now stop a different count of members counting as Cav members, or
// change the owner of a different count of channels, than its preview
// showed (CODING_STANDARDS.md, "A Confirm acts on what its preview
// showed"). A handover is final, so the page shows the preview as it
// stands now, to check and confirm again.
var errRankRolesChanged = &fieldError{kind: refusalChanged, Message: "The members or channels this save changes are not what the preview showed, " +
	"so nothing was saved. Check the preview below and confirm again."}

// rankRolesPreview is what a rank roles save that may stop members counting
// as Cav members waits on (GLOSSARY.md, Preview). It counts the members who
// stop and the spawned channels that change owner. A handover is final, so an owner who
// loses a channel to a mistaken save does not get it back when the admin
// saves again.
type rankRolesPreview struct {
	// Dropped is how many members hold a rank role before the save and none
	// after it, counted from the member list.
	Dropped int
	// Counted is false while the member list has not loaded, when Dropped
	// is unknown.
	Counted bool
	// Channels is how many spawned channels change owner.
	Channels int
}

// Shown is the preview's counts as its Confirm posts them back, so the save
// can tell whether they still hold.
func (p rankRolesPreview) Shown() string {
	return p.DroppedValue() + "/" + strconv.Itoa(p.Channels)
}

// DroppedValue is the member count as the preview's data-dropped carries
// it, the number or unknown.
func (p rankRolesPreview) DroppedValue() string {
	if !p.Counted {
		return holdersUnknown
	}
	return strconv.Itoa(p.Dropped)
}

// formRankRoles is the Rank roles section's form, as a refusal and a save
// name it, and the ID of its heading.
const formRankRoles = "rank-roles"

// rankRolesInputOf reads the Rank roles section's form off a parsed post,
// or off the rank roles preview's address, one role or none for each rank
// of the ladder.
func rankRolesInputOf(form url.Values) rankRolesInput {
	in := rankRolesInput{Version: form.Get(fieldVersion), Roles: map[string]string{},
		Confirmed: form.Get(fieldConfirm) != "", Shown: form.Get(fieldShown)}
	for _, rank := range commands.DefaultRankLadder().Ranks() {
		in.Roles[rank.Abbrev] = strings.TrimSpace(form.Get(rankRoleField(rank.Abbrev)))
	}
	return in
}

// saveRankRoles is POST /rank-roles. It makes one service call, then
// redirects to the Settings page, opened at the Rank roles section, where
// the saved roles show and the section says it saved. A refusal answers
// with the page again and the refusal on the section. A save that waits on
// its preview redirects to the preview's address, so a reload of the
// preview never saves, with or without script (ADR 0019).
func (p *Panel) saveRankRoles(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := rankRolesInputOf(r.PostForm)
	ctx, hubs := p.saving(r)
	saved, preview, err := hubs.setRankRoles(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderSettings(w, r, sess, refusalStatus(sess, refusal),
			pageRequest{RankRoles: &in, RankRolesPreview: preview, Error: refusal, Refused: formRankRoles})
		return
	}
	if preview != nil {
		http.Redirect(w, r, rankRolesPreviewAddress(in), http.StatusSeeOther)
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "rank roles save", settingsPath, err)
		return
	}
	utils.Info("Panel rank roles saved", "roles", saved.Roles, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, settingsSavedAddress(formRankRoles, saved.Version), http.StatusSeeOther)
}

// setRankRoles saves the rank roles. It writes the whole ladder's roles
// with a change log entry under no hub naming each rank whose role changed,
// in one store write. Then it applies the ladder to the runtime, so every
// test of whether a member is a Cav member reads it at once. It returns the
// roles as stored. A section loaded at a version the rank roles are no
// longer at is refused first, as errStaleRankRoles, before the guild read.
// A valid ladder that is the stored one is refused as
// errRankRolesUnchanged.
//
// A save that may stop members counting as Cav members writes nothing and
// returns its preview instead, unless it is the preview's Confirm. A Confirm
// whose preview no longer holds is refused as errRankRolesChanged, and
// returns the preview as it stands now with the refusal.
func (s *hubService) setRankRoles(ctx context.Context, in rankRolesInput, by actor) (store.RankRoles, *rankRolesPreview, error) {
	unlock, err := s.lockSave(ctx)
	if err != nil {
		return store.RankRoles{}, nil, err
	}
	defer unlock()
	stored, err := s.deps.Store.GetRankRoles(ctx, s.deps.GuildID)
	if err != nil {
		return store.RankRoles{}, nil, fmt.Errorf("%s: %w", rankRolesRead, err)
	}
	if !formIsCurrent(in.Version, stored.Version) {
		return store.RankRoles{}, nil, errStaleRankRoles
	}
	guild, err := s.readGuild(ctx)
	if err != nil {
		return store.RankRoles{}, nil, err
	}
	before := commands.RankLadderOf(stored)
	after, err := acceptedRankRoles(in.Roles, guild.info, before)
	if err != nil {
		return store.RankRoles{}, nil, err
	}
	if maps.Equal(after.Roles(), before.Roles()) {
		return store.RankRoles{}, nil, errRankRolesUnchanged
	}
	if preview := s.rankRolesPreviewOf(before, after); preview != nil {
		switch {
		case !in.Confirmed:
			return store.RankRoles{}, preview, nil
		case preview.Shown() != in.Shown:
			return store.RankRoles{}, preview, errRankRolesChanged
		}
	}
	entry, err := changeEntry(store.ChangeRankRoles, diffRankRoles(before, after), by)
	if err != nil {
		return store.RankRoles{}, nil, err
	}
	saved := store.RankRoles{Roles: after.Roles(), Version: stored.Version}
	err = s.deps.Store.SaveRankRoles(ctx, s.deps.GuildID, saved, entry)
	if errors.Is(err, store.ErrStale) {
		// Another process saved the rank roles after this save's read.
		return store.RankRoles{}, nil, errStaleRankRoles
	}
	if err != nil {
		return store.RankRoles{}, nil, fmt.Errorf("write rank roles: %w", err)
	}
	s.deps.Runtime.ApplyRankRoles(after)
	saved.Version++
	return saved, nil, nil
}

// rankRolesPreviewOf is the preview a save from the ladder before to the
// ladder after waits on, or nil when the save stops nobody counting as a
// Cav member and needs none. The members are counted from the member list.
// While it has not loaded the count is unknown, and any save that takes a
// role off the ladder may stop someone, so it previews.
func (s *hubService) rankRolesPreviewOf(before, after commands.RankLadder) *rankRolesPreview {
	preview := s.rankRolesCounts(before, after)
	if preview.Counted && preview.Dropped == 0 || !preview.Counted && !before.TakesARoleOff(after) {
		return nil
	}
	return &preview
}

// rankRolesCounts is what a save from the ladder before to the ladder
// after changes, as its preview counts it: the members it stops counting
// as Cav members, from the member list, unknown while the list has not
// loaded, and the spawned channels that change owner.
func (s *hubService) rankRolesCounts(before, after commands.RankLadder) rankRolesPreview {
	preview := rankRolesPreview{Channels: s.deps.Runtime.RankRolesHandovers(after)}
	list := s.deps.Manager.MemberList(s.deps.GuildID)
	if list.Status != commands.MemberListComplete {
		return preview
	}
	preview.Counted = true
	for _, m := range list.Members {
		if before.Drops(after, m.RoleIDs) {
			preview.Dropped++
		}
	}
	return preview
}

// acceptedRankRoles checks each rank's posted role and returns the ladder
// they make. A rank may have no role. A role must be eligible, unless it is
// the role the stored ladder already gives that same rank, the role IDs in
// code included while nothing is saved, so a save keeps an unavailable role
// on its rank and never adds one to another (ADR 0012). And a role may be
// the rank role of one rank only. A refusal is a *fieldError naming the
// rank's field.
func acceptedRankRoles(posted map[string]string, guild guildInfo, stored commands.RankLadder) (commands.RankLadder, error) {
	was := stored.Roles()
	rankOf := map[string]string{}
	for _, rank := range stored.Ranks() {
		id := posted[rank.Abbrev]
		if id == "" {
			continue
		}
		if !guild.isEligible(id) && id != was[rank.Abbrev] {
			return commands.RankLadder{}, &fieldError{Field: rankRoleField(rank.Abbrev),
				Message: "That role can't be " + rank.Abbrev + "'s rank role. Choose another role for " + rank.Abbrev + "."}
		}
		if other, taken := rankOf[id]; taken {
			return commands.RankLadder{}, &fieldError{Field: rankRoleField(rank.Abbrev),
				Message: other + " and " + rank.Abbrev + " have the same role. A role can be the rank role of one rank only."}
		}
		rankOf[id] = rank.Abbrev
	}
	return commands.NewRankLadder(posted), nil
}

// diffRankRoles is a rank roles save's diff. It holds each rank whose role
// changed, under its form field, with its role before and after, null for
// none.
func diffRankRoles(before, after commands.RankLadder) diff {
	d := diff{}
	was, is := before.Roles(), after.Roles()
	for abbrev, id := range is {
		if was[abbrev] != id {
			d[rankRoleField(abbrev)] = change{Before: roleOrNull(was[abbrev]), After: roleOrNull(id)}
		}
	}
	return d
}

// roleOrNull is a rank's role as a diff records it, its ID or null for a
// rank with none.
func roleOrNull(id string) any {
	if id == "" {
		return nil
	}
	return id
}
