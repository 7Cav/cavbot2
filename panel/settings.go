package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/7cav/cavbot2/commands"
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

// rankRoleField is the form field a rank's rank role posts under.
func rankRoleField(abbrev string) string { return "rank_role_" + abbrev }

// The names the Settings page's store reads' errors carry, as the hub
// page's do.
const rankRolesRead = "read rank roles"

// settingsPage is GET /settings.
func (p *Panel) settingsPage(w http.ResponseWriter, r *http.Request, sess session) {
	p.renderSettings(w, r, sess, http.StatusOK)
}

// renderSettings renders the Settings page read now, under the hub page's
// time budget and with its failures answered the way the hub page answers
// its own.
func (p *Panel) renderSettings(w http.ResponseWriter, r *http.Request, sess session, status int) {
	start := panelClock.Now()
	ctx, cancel := panelClock.WithTimeout(r.Context(), hubPageBudget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	reads := newPageReads(start, deadline)
	view, err := p.hubs.forPage(reads).settings(ctx)
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

// settings reads everything the Settings page renders from, once: the
// guild from the gateway state, then the rank roles from the store.
func (s *hubService) settings(ctx context.Context) (settingsView, error) {
	guild, err := s.readGuild(ctx)
	if err != nil {
		return settingsView{}, err
	}
	view := settingsView{Disconnected: guild.disconnected}
	if view.RankRoles, err = s.rankRolesSection(ctx, guild.info); err != nil {
		return settingsView{}, err
	}
	return view, nil
}

// rankRolesSection builds the Rank roles section from the guild read and
// the store: each rank with the rank role the stored ladder gives it, the
// role IDs in code while nothing is saved.
func (s *hubService) rankRolesSection(ctx context.Context, guild guildInfo) (rankRolesPage, error) {
	stored, err := s.deps.Store.GetRankRoles(ctx, s.deps.GuildID)
	if err != nil {
		return rankRolesPage{}, fmt.Errorf("%s: %w", rankRolesRead, err)
	}
	page := rankRolesPage{Version: strconv.FormatInt(stored.Version, 10), OptionsID: rankRoleOptions}
	for _, rank := range commands.RankLadderOf(stored).Ranks() {
		var roles []string
		if rank.RoleID != "" {
			roles = []string{rank.RoleID}
		}
		picker := rolePicker(rankRoleField(rank.Abbrev), "the "+rank.Abbrev+" rank", guild, roles, roles)
		picker.Single, picker.AddLabel, picker.Candidates, picker.OptionsFrom = true, "Choose a role", nil, rankRoleOptions
		page.Rows = append(page.Rows, rankRow{Abbrev: rank.Abbrev, Picker: picker})
	}
	page.Options = rolePicker("", "", guild, nil, nil).Candidates
	return page, nil
}
