package panel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The Foxhole page's Add a unit's roster block (spec #434, "Add a unit's
// roster"): a Foxhole manager picks a validated internal unit, never types
// one, and previews adding its roster to Internal. The units come from the
// registry the roster add command's picker offers (ADR 0009), and a unit's
// roster comes from the 7Cav API through the unit's registry row alone.

// The roster block's addresses: the Preview roster its form posts to, and
// the roster add the roster preview's Confirm starts.
const (
	foxholeRosterPreviewPath = "/foxhole/roster/preview"
	foxholeRosterPath        = "/foxhole/roster"
)

// fieldUnit is the roster block's field: the value of the validated
// internal unit picked. The roster preview's Confirm posts it beside who
// the preview listed, as fieldRoster.
const (
	fieldUnit   = "unit"
	fieldRoster = "roster"
)

// rosterResult is what a roster add does with one trooper on the roster,
// its data-result marker. Each but rosterGets is a reason the add skips
// the trooper, which its report gives under the same code.
type rosterResult string

const (
	// rosterGets is a trooper in the server who gets Internal.
	rosterGets rosterResult = "gets"
	// rosterHolding is a trooper who already holds Internal.
	rosterHolding = rosterResult(commands.SkipHolding)
	// rosterNotInServer is a trooper whose milpac names a Discord ID no
	// member of the server has.
	rosterNotInServer = rosterResult(commands.SkipNotInServer)
	// rosterNoDiscord is a trooper whose milpac names no Discord account.
	rosterNoDiscord = rosterResult(commands.SkipNoDiscord)
)

// rosterPreview is the preview a roster add waits on: the unit, and a row
// per trooper on its roster, in forum username order.
type rosterPreview struct {
	Unit commands.ValidatedInternalUnit
	Rows []rosterRow
	// Notes counts the members the preview names who have a note.
	Notes int
	// Holding counts the troopers who already hold Internal.
	Holding int
}

// rosterRow is one trooper on the roster as the roster preview shows them:
// the trooper, what the add does with them, the member, nil for a trooper
// not in the server, and the member's note, whether or not they are in the
// server.
type rosterRow struct {
	commands.RosterTrooper
	Result rosterResult
	Member *holderRow
	Note   string
}

// ResultLabel is the row's result as the preview says it, in the report's
// words for a trooper the add skips.
func (r rosterRow) ResultLabel() string {
	if r.Result == rosterGets {
		return "gets Internal"
	}
	return skipWords(commands.SkipReason(r.Result), foxholeRoleLabels[commands.FoxholeInternal])
}

// rosterRequest is the roster preview a page opens: the unit, and its
// roster when the request has fetched it already, nil for the page to
// fetch it.
type rosterRequest struct {
	Unit     commands.ValidatedInternalUnit
	Troopers []commands.RosterTrooper
}

// errRosterListPartial refuses a Preview roster pressed while the member
// list is partial: the preview can't tell who holds Internal or is in the
// server. It fetches no roster.
var errRosterListPartial = &saveRefusal{Kind: "member-list", status: http.StatusServiceUnavailable,
	log:     "Panel action refused: member list partial",
	Message: "Cavbot2 doesn't have the whole member list from Discord yet, so it can't preview the roster. Nothing changed. Preview again once the list has arrived."}

// rosterFetchRefusal refuses a roster preview, or the roster add its
// Confirm would start, when the unit's roster fetch fails. The fault has
// reached Sentry.
func rosterFetchRefusal(unit commands.ValidatedInternalUnit) *saveRefusal {
	return &saveRefusal{Kind: "roster-failed", status: http.StatusBadGateway,
		log:     "Panel action refused: roster fetch failed",
		Message: fmt.Sprintf("Cavbot2 couldn't fetch the %s roster from the 7Cav API, so nothing changed. The bot has reported it. Try again shortly.", unit.Label)}
}

// rosterEmptyRefusal refuses a roster preview, or the roster add its
// Confirm would start, when the unit's roster comes back empty. A
// validated unit's roster is fixed input, so empty is a fault (ADR 0002),
// and it has reached Sentry.
func rosterEmptyRefusal(unit commands.ValidatedInternalUnit) *saveRefusal {
	return &saveRefusal{Kind: "roster-empty", status: http.StatusBadGateway,
		log:     "Panel action refused: roster empty",
		Message: fmt.Sprintf("The %s roster came back empty. That shouldn't happen for a validated unit, so nothing changed, and the bot has reported it.", unit.Label)}
}

// fetchRoster fetches the unit's roster for a preview or a Confirm. A fetch
// that fails, or an empty roster, reaches Sentry and refuses, unless the
// request ended first: a browser that went away is no fault.
func fetchRoster(ctx context.Context, unit commands.ValidatedInternalUnit) ([]commands.RosterTrooper, error) {
	troopers, err := rosterTroopers(ctx, unit)
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return nil, ctx.Err()
	case err != nil:
		utils.CaptureError("Panel Foxhole roster fetch failed", err, "unit", unit.Value)
		return nil, rosterFetchRefusal(unit)
	case len(troopers) == 0:
		utils.CaptureError("Panel Foxhole roster came back empty", fmt.Errorf("empty roster for unit %q", unit.Value), "unit", unit.Value)
		return nil, rosterEmptyRefusal(unit)
	}
	return troopers, nil
}

// rosterTroopers fetches the unit's roster through its registry row, and
// returns its troopers in forum username order, each Discord ID trimmed.
func rosterTroopers(ctx context.Context, unit commands.ValidatedInternalUnit) ([]commands.RosterTrooper, error) {
	roster, err := unit.Roster(ctx)
	if err != nil {
		return nil, err
	}
	troopers := make([]commands.RosterTrooper, 0, len(roster.LiteProfiles))
	for _, p := range roster.LiteProfiles {
		troopers = append(troopers, commands.RosterTrooper{Username: p.User.Username, DiscordID: strings.TrimSpace(p.DiscordID)})
	}
	slices.SortFunc(troopers, func(a, b commands.RosterTrooper) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Username), strings.ToLower(b.Username)), cmp.Compare(a.DiscordID, b.DiscordID))
	})
	return troopers, nil
}

// rosterPreviewOf is the preview of an add of the troopers given to
// Internal, read from a complete member list and the guild's Foxhole
// records.
func (s foxholeService) rosterPreviewOf(list commands.MemberListSnapshot, records map[string]store.FoxholeRecord, unit commands.ValidatedInternalUnit, troopers []commands.RosterTrooper) *rosterPreview {
	guild := s.foxholeGuildOf()
	preview := &rosterPreview{Unit: unit}
	for _, t := range troopers {
		row := rosterRow{RosterTrooper: t}
		if t.DiscordID != "" {
			row.Note = records[t.DiscordID].Note
		}
		if row.Note != "" {
			preview.Notes++
		}
		mem, inServer := list.Member(t.DiscordID)
		switch {
		case t.DiscordID == "":
			row.Result = rosterNoDiscord
		case !inServer:
			row.Result = rosterNotInServer
		default:
			holder := guild.rowOf(mem, records[t.DiscordID])
			row.Member, row.Result = &holder, rosterGets
			if holder.Internal {
				row.Result = rosterHolding
				preview.Holding++
			}
		}
		preview.Rows = append(preview.Rows, row)
	}
	return preview
}

// Listed is who the preview lists, for its Confirm to post back: each
// trooper's forum username and the Discord ID their milpac names, in the
// preview's order. Who holds Internal or is in the server doesn't change
// it.
func (p *rosterPreview) Listed() string {
	troopers := make([]commands.RosterTrooper, 0, len(p.Rows))
	for _, row := range p.Rows {
		troopers = append(troopers, row.RosterTrooper)
	}
	return listedRoster(troopers)
}

// listedRoster is the troopers as a roster preview's Listed gives them.
func listedRoster(troopers []commands.RosterTrooper) string {
	raw, _ := json.Marshal(troopers)
	return string(raw)
}

// errRosterChanged refuses a roster add's Confirm when the unit's roster
// has changed since its preview, so the add never gives Internal to a
// trooper the manager didn't see. The page shows the preview as it stands
// now, to check and confirm again.
var errRosterChanged = &saveRefusal{Kind: "roster-changed", status: http.StatusConflict,
	log:     "Panel action refused: roster preview out of date",
	Message: "The roster changed since you previewed it, so the roster add didn't start. Nothing changed. Check the preview below and confirm again."}

// confirmedRoster is the roster a roster add's Confirm starts on: the
// unit's roster fetched again, as fetchRoster says, which must list the
// troopers listed, as the preview's Listed, else errRosterChanged with the
// roster as it stands now. It fetches nothing while the add couldn't start:
// while the member list is partial, commands.ErrMemberListPartial, and
// while another Foxhole action runs, commands.ErrActionRunning.
func (s foxholeService) confirmedRoster(ctx context.Context, unit commands.ValidatedInternalUnit, listed string) ([]commands.RosterTrooper, error) {
	if s.manager.MemberList(s.guildID).Status != commands.MemberListComplete {
		return nil, commands.ErrMemberListPartial
	}
	if _, busy := s.actions.Running(); busy {
		return nil, commands.ErrActionRunning
	}
	troopers, err := fetchRoster(ctx, unit)
	if err != nil {
		return nil, err
	}
	if listedRoster(troopers) != listed {
		return troopers, errRosterChanged
	}
	return troopers, nil
}

// CanAdd reports whether confirming the preview could give Internal to
// anyone: a trooper in the server who doesn't hold it. A preview that
// can't offers no Confirm.
func (p *rosterPreview) CanAdd() bool {
	return slices.ContainsFunc(p.Rows, func(r rosterRow) bool { return r.Result == rosterGets })
}

// startRosterAdd is POST /foxhole/roster, the roster preview's Confirm: it
// starts the roster add of the roster confirmedRoster gives, as
// startAction says. A roster that lists other troopers than the preview
// did starts nothing, and the page shows the preview as it stands now. A
// unit outside the registry is refused before any roster fetch.
func (p *Panel) startRosterAdd(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	unit, ok := commands.LookupValidatedInternalUnit(r.PostForm.Get(fieldUnit))
	if !ok {
		http.Error(w, "the form names no validated internal unit, so nothing changed", http.StatusBadRequest)
		return
	}
	kv := []any{"unit", unit.Value}
	// The fetch runs to its end whether or not the browser waits, as the
	// action it starts does, under the page's time budget, as the preview's
	// fetch does.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), p.pageBudget)
	defer cancel()
	troopers, err := p.foxhole.confirmedRoster(ctx, unit, r.PostForm.Get(fieldRoster))
	if errors.Is(err, errRosterChanged) {
		p.refuseAction(w, r, sess, foxholeRequest{Filter: filterAll, Roster: &rosterRequest{Unit: unit, Troopers: troopers}}, errRosterChanged, kv...)
		return
	}
	p.startAction(w, r, sess, actionPage, "the roster add", func(ctx context.Context, by commands.ForumUser) error {
		if err != nil {
			return err
		}
		return p.foxhole.actions.RosterAdd(ctx, unit, troopers, by)
	}, kv...)
}

// previewRoster is POST /foxhole/roster/preview, the roster block's Preview
// roster: the Foxhole page with the roster preview of the unit picked. A
// value outside the registry is refused before any roster fetch.
func (p *Panel) previewRoster(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	unit, ok := commands.LookupValidatedInternalUnit(r.PostForm.Get(fieldUnit))
	if !ok {
		http.Error(w, "the form names no validated internal unit, so nothing changed", http.StatusBadRequest)
		return
	}
	p.renderFoxhole(w, r, sess, http.StatusOK, foxholeRequest{Filter: filterAll, AwaitList: true, Roster: &rosterRequest{Unit: unit}})
}
