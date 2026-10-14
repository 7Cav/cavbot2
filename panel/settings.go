package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The Settings page (#680, GLOSSARY.md): where a panel admin sets the
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
	Error        *fieldError
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
// offers, rendered once for the section rather than once per rank: the
// live guild is near Discord's cap of 250 roles.
const rankRoleOptions = "rank-role-options"

// rankRolesPage is the Rank roles section as the page renders it: one row
// per rank, most senior first, the roles every row's picker offers, and the
// version the section posts.
type rankRolesPage struct {
	Version string
	Rows    []rankRow
	// Options are the eligible roles, highest position first, which each
	// rank's picker offers from the list under OptionsID.
	Options   []pickerItem
	OptionsID string
}

// rankRow is one rank's row: its abbreviation and its picker, holding its
// rank role as the one tag, or none.
type rankRow struct {
	Abbrev string
	Picker pickerView
}

// rankRoleSet is a rank's role as a picker's set: the one ID, or none.
func rankRoleSet(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

// rankRoleField is the form field a rank's rank role posts under.
func rankRoleField(abbrev string) string { return "rank_role_" + abbrev }

// The names the Settings page's store reads' errors carry, as the hub
// page's do.
const rankRolesRead = "read rank roles"

// settingsPage is GET /settings.
func (p *Panel) settingsPage(w http.ResponseWriter, r *http.Request, sess session) {
	p.renderSettings(w, r, sess, http.StatusOK, pageRequest{})
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
		p.noGuildData(w, sess, "settings page", settingsPath, err)
		return
	case err != nil:
		var report *budgetReport
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			report = reads.report(hubPageBudget)
		}
		p.pageFailed(w, sess, "settings page", settingsPath, err, report)
		return
	}
	data := sess.page("Settings")
	data.Page, data.Settings = pageSettings, view
	p.render(w, status, "settings", data)
}

// renderSettingsRefused answers a refused save of a Settings page form with
// the page again, the form as posted and the refusal's note on it, the way
// renderRefused answers one on the hub page.
func (p *Panel) renderSettingsRefused(w http.ResponseWriter, r *http.Request, sess session, req pageRequest) {
	status := http.StatusUnprocessableEntity
	if req.Error.stale() {
		status = http.StatusConflict
		utils.Info("Panel save refused: stale form", "username", sess.username, "forum_user_id", sess.userID)
	}
	p.renderSettings(w, r, sess, status, req)
}

// settings reads everything the Settings page renders from, once: the
// guild from the gateway state, then the rank roles from the store.
func (s *hubService) settings(ctx context.Context, req pageRequest) (settingsView, error) {
	guild, err := s.readGuild(ctx)
	if err != nil {
		return settingsView{}, err
	}
	view := settingsView{Disconnected: guild.disconnected, Error: req.Error, Refused: req.Refused}
	if view.RankRoles, err = s.rankRolesSection(ctx, guild.info, req); err != nil {
		return settingsView{}, err
	}
	return view, nil
}

// rankRolesSection builds the Rank roles section from the guild read and
// the store: each rank with the rank role the stored ladder gives it, the
// role IDs in code while nothing is saved, or the form as posted back after
// a refusal with the version postedBackVersion gives it.
func (s *hubService) rankRolesSection(ctx context.Context, guild guildInfo, req pageRequest) (rankRolesPage, error) {
	stored, err := s.deps.Store.GetRankRoles(ctx, s.deps.GuildID)
	if err != nil {
		return rankRolesPage{}, fmt.Errorf("%s: %w", rankRolesRead, err)
	}
	page := rankRolesPage{Version: strconv.FormatInt(stored.Version, 10), OptionsID: rankRoleOptions}
	posted := req.RankRoles
	if posted != nil {
		page.Version = postedBackVersion(stored.Version, posted.Version, req.staleFor(formRankRoles))
	}
	for _, rank := range commands.RankLadderOf(stored).Ranks() {
		roles, selected := rankRoleSet(rank.RoleID), rankRoleSet(rank.RoleID)
		if posted != nil {
			selected = rankRoleSet(posted.Roles[rank.Abbrev])
		}
		picker := rolePicker(rankRoleField(rank.Abbrev), "the "+rank.Abbrev+" rank", guild, roles, selected)
		picker.Single, picker.AddLabel, picker.Candidates, picker.OptionsFrom = true, "Choose a role", nil, rankRoleOptions
		page.Rows = append(page.Rows, rankRow{Abbrev: rank.Abbrev, Picker: picker})
	}
	page.Options = rolePicker("", "", guild, nil, nil).Candidates
	return page, nil
}

// rankRolesInput is the Rank roles section's form as posted: the ladder's
// version when the section loaded, and each rank's role, empty for none.
type rankRolesInput struct {
	Version string
	Roles   map[string]string
}

// formRankRoles is the Rank roles section's form, as a refusal and a save
// name it, and the ID of its heading.
const formRankRoles = "rank-roles"

// rankRolesInputOf reads the Rank roles section's form off a parsed post:
// one role, or none, for each rank of the ladder.
func rankRolesInputOf(r *http.Request) rankRolesInput {
	in := rankRolesInput{Version: r.PostForm.Get(fieldVersion), Roles: map[string]string{}}
	for _, rank := range commands.DefaultRankLadder().Ranks() {
		in.Roles[rank.Abbrev] = strings.TrimSpace(r.PostForm.Get(rankRoleField(rank.Abbrev)))
	}
	return in
}

// saveRankRoles is POST /rank-roles: one service call, then a redirect to
// the Settings page, opened at the Rank roles section.
func (p *Panel) saveRankRoles(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := rankRolesInputOf(r)
	ctx, hubs := p.saving(r)
	saved, err := hubs.setRankRoles(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderSettingsRefused(w, r, sess, pageRequest{RankRoles: &in, Error: refusal, Refused: formRankRoles})
		return
	}
	if err != nil {
		p.saveFailed(w, sess, "rank roles save", settingsPath, err)
		return
	}
	utils.Info("Panel rank roles saved", "roles", saved.Roles, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, settingsPath+"#"+formRankRoles, http.StatusSeeOther)
}

// setRankRoles saves the rank roles: it writes the whole ladder's roles
// with a change log entry under no hub naming each rank whose role changed,
// in one store write, then applies the ladder to the runtime, so every test
// of whether a member is a Cav member reads it at once. It returns the
// roles as stored.
func (s *hubService) setRankRoles(ctx context.Context, in rankRolesInput, by actor) (store.RankRoles, error) {
	unlock, err := s.lockSave(ctx)
	if err != nil {
		return store.RankRoles{}, err
	}
	defer unlock()
	stored, err := s.deps.Store.GetRankRoles(ctx, s.deps.GuildID)
	if err != nil {
		return store.RankRoles{}, fmt.Errorf("%s: %w", rankRolesRead, err)
	}
	guild, err := s.readGuild(ctx)
	if err != nil {
		return store.RankRoles{}, err
	}
	before := commands.RankLadderOf(stored)
	after, err := acceptedRankRoles(in.Roles, guild.info, before)
	if err != nil {
		return store.RankRoles{}, err
	}
	entry, err := changeEntry(store.ChangeRankRoles, diffRankRoles(before, after), by)
	if err != nil {
		return store.RankRoles{}, err
	}
	saved := store.RankRoles{Roles: after.Roles(), Version: stored.Version}
	if err := s.deps.Store.SaveRankRoles(ctx, s.deps.GuildID, saved, entry); err != nil {
		return store.RankRoles{}, fmt.Errorf("write rank roles: %w", err)
	}
	s.deps.Runtime.ApplyRankRoles(after)
	saved.Version++
	return saved, nil
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

// diffRankRoles is a rank roles save's diff: each rank whose role changed,
// under its form field, with its role before and after, null for none.
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

// roleOrNull is a rank's role as a diff records it: its ID, or null for a
// rank with none.
func roleOrNull(id string) any {
	if id == "" {
		return nil
	}
	return id
}
