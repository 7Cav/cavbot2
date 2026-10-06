package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The Foxhole page's Foxhole actions (spec #434): the purge confirmation,
// the purge's start through the Foxhole runtime, and the running action's
// progress block or the last action's report, which also renders as the
// action's change log entry.

// reportView is a Foxhole action's report as the page shows it: its
// outcome, and the members it changed, skipped and never attempted.
type reportView struct {
	ID int64
	// Action is the action the report is of, and Name the action with its
	// scope as the page names it.
	Action store.ChangeAction
	Name   string
	// StartedBy is the forum user who started the action, StartedAt when,
	// and EndedAt when it ended, zero while it runs.
	StartedBy          string
	StartedAt, EndedAt time.Time
	// Running is the action still running: the page shows its progress
	// block in the report's place.
	Running      bool
	Outcome      commands.ReportOutcome
	Changed      []reportMember
	Skipped      []reportMember
	Failed       []reportMember
	NotAttempted []reportMember
	// Done counts the members the action has been through, of Total, and
	// Left is the rough time the rest take.
	Done, Total int
	Left        estimate
}

// reportMember is one member a report names, with the role's name as the
// page shows it.
type reportMember struct {
	commands.ReportMember
	RoleName string
}

// OutcomeLabel is how the action ended, as the page says it.
func (r reportView) OutcomeLabel() string {
	switch {
	case r.Running:
		return "Running"
	case r.Outcome == commands.ReportDone:
		return "Done"
	case r.Outcome == commands.ReportMemberListGone:
		return "Stopped: Discord's member list didn't arrive"
	}
	return string(r.Outcome)
}

// OutcomeCode is the report's data-outcome marker: how the action ended,
// or running while it runs.
func (r reportView) OutcomeCode() string {
	if r.Running {
		return "running"
	}
	return string(r.Outcome)
}

// Why is why the action skipped the member, or why Discord refused their
// change, as the page says it. Empty for a member it changed or never
// attempted.
func (m reportMember) Why() string {
	switch m.Skip {
	case commands.SkipNotHolding:
		return "no longer holds " + m.RoleName
	case commands.SkipLeft:
		return "left the server"
	}
	return m.Failure
}

// reportViewOf decodes a report the store holds for the page.
func reportViewOf(stored store.FoxholeReport) (*reportView, error) {
	var report commands.ActionReport
	if err := json.Unmarshal(stored.Entry.Diff, &report); err != nil {
		return nil, fmt.Errorf("decode Foxhole report %d: %w", stored.Entry.ID, err)
	}
	view := &reportView{ID: stored.Entry.ID, Action: stored.Entry.Action, Name: actionNames[stored.Entry.Action] + " " + scopeLabels[report.Scope],
		StartedBy: stored.Entry.ForumUsername, StartedAt: stored.Entry.At, EndedAt: report.EndedAt,
		Running: stored.Running, Outcome: report.Outcome,
		Changed: reportMembers(report.Changed), Skipped: reportMembers(report.Skipped), Failed: reportMembers(report.Failed),
		NotAttempted: reportMembers(report.NotAttempted)}
	view.Done = len(view.Changed) + len(view.Skipped) + len(view.Failed)
	view.Total = view.Done + len(view.NotAttempted)
	view.Left = estimateFor(len(view.NotAttempted))
	return view, nil
}

// reportMembers are a report's members as the page shows them.
func reportMembers(members []commands.ReportMember) []reportMember {
	out := make([]reportMember, 0, len(members))
	for _, m := range members {
		out = append(out, reportMember{ReportMember: m, RoleName: foxholeRoleLabels[m.Role]})
	}
	return out
}

// purgeConfirm is the confirmation a purge waits on: its scope, how many
// holders lose each role it names, in the order the purge takes them, and
// the rough time at one change a second.
type purgeConfirm struct {
	Scope    commands.PurgeScope
	Label    string
	Roles    []purgeRole
	Estimate estimate
}

// purgeRole is one role a purge takes, and how many hold it at this load.
type purgeRole struct {
	Role    commands.FoxholeRole
	Name    string
	Holders int
}

// changeEstimate is the time one role change takes in the page's
// estimates: Discord's per-guild member-role rate is about one change a
// second.
const changeEstimate = time.Second

// estimate is a rough time for a number of role changes: the whole seconds,
// for the data-seconds marker, and the words the page shows.
type estimate struct {
	Seconds int
	Text    string
}

// estimateFor is the rough time of changes role changes at changeEstimate
// each. Under a minute it counts seconds, under ten minutes minutes and
// seconds to the nearest 5, and beyond that whole minutes.
func estimateFor(changes int) estimate {
	d := time.Duration(changes) * changeEstimate
	secs := int(d.Round(time.Second) / time.Second)
	switch {
	case secs < 60:
		return estimate{Seconds: secs, Text: fmt.Sprintf("%d s", secs)}
	case secs < 600:
		rounded := (secs + 2) / 5 * 5
		if rounded%60 == 0 {
			return estimate{Seconds: secs, Text: fmt.Sprintf("%d min", rounded/60)}
		}
		return estimate{Seconds: secs, Text: fmt.Sprintf("%d min %d s", rounded/60, rounded%60)}
	}
	return estimate{Seconds: secs, Text: fmt.Sprintf("%d min", (secs+30)/60)}
}

// foxholeRoleLabels are the Foxhole roles as the page names them.
var foxholeRoleLabels = map[commands.FoxholeRole]string{commands.FoxholeInternal: "Internal", commands.FoxholeExternal: "External"}

// actionNames are the Foxhole actions as the page names them.
var actionNames = map[store.ChangeAction]string{store.ChangePurge: "Purge"}

// scopeLabels are the purge scopes as the page names them.
var scopeLabels = map[commands.PurgeScope]string{
	commands.PurgeBoth:     "Internal and External",
	commands.PurgeInternal: "Internal",
	commands.PurgeExternal: "External",
}

// purgeConfirmOf is the confirmation of a purge of the scope given, its
// counts read from a complete member list through the role lookup the purge
// itself makes, so the count confirmed is the count the purge sets out on.
func (s foxholeService) purgeConfirmOf(list commands.MemberListSnapshot, scope commands.PurgeScope) *purgeConfirm {
	roleIDs := commands.FoxholeRoleIDs(s.manager.GuildData(s.guildID))
	confirm := &purgeConfirm{Scope: scope, Label: scopeLabels[scope]}
	changes := 0
	for _, role := range scope.Roles() {
		holders := 0
		for _, mem := range list.Members {
			if roleIDs[role] != "" && slices.Contains(mem.RoleIDs, roleIDs[role]) {
				holders++
			}
		}
		confirm.Roles = append(confirm.Roles, purgeRole{Role: role, Name: foxholeRoleLabels[role], Holders: holders})
		changes += holders
	}
	confirm.Estimate = estimateFor(changes)
	return confirm
}

// lastReport reads the last Foxhole action's report, nil before the first
// action.
func (s foxholeService) lastReport(ctx context.Context) (*reportView, error) {
	stored, err := s.store.LastFoxholeReport(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the last Foxhole report: %w", err)
	}
	return reportViewOf(stored)
}

// errActionRunning refuses a Foxhole action started while another runs.
// The page shows the running action's progress block, which names it and
// who started it.
var errActionRunning = &saveRefusal{Kind: "action-running", status: http.StatusConflict,
	log:     "Panel action refused: another action running",
	Message: "Another Foxhole action is running, so the purge didn't start. Nothing changed. Try again when it ends."}

// errActionListPartial refuses a Foxhole action started while the member
// list is partial: it could not see who holds a role.
var errActionListPartial = &saveRefusal{Kind: "member-list", status: http.StatusServiceUnavailable,
	log:     "Panel action refused: member list partial",
	Message: "Cavbot2 doesn't have the whole member list from Discord yet, so the purge didn't start. Nothing changed. Try again once the list has arrived."}

// roleMissingRefusal refuses a Foxhole action naming a role the guild
// doesn't hold by its configured name. The runtime has reported it to
// Sentry.
func roleMissingRefusal(role string) *saveRefusal {
	return &saveRefusal{Kind: "role-missing", status: http.StatusUnprocessableEntity,
		log:     "Panel action refused: Foxhole role not found",
		Message: fmt.Sprintf("The server has no role named %s, so the purge didn't start. Nothing changed. The bot has reported it.", role)}
}

// startPurge is POST /foxhole/purge, the purge confirmation's Confirm: it
// starts the purge through the Foxhole runtime and redirects straight back
// to the Foxhole page. The purge runs in the background to its end,
// whether or not the browser waits, so the request never waits on it.
func (p *Panel) startPurge(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	scope, ok := commands.ParsePurgeScope(r.PostForm.Get(fieldScope))
	if !ok {
		http.Error(w, "the form names no purge scope, so nothing changed", http.StatusBadRequest)
		return
	}
	by := commands.ForumUser{ID: sess.userID, Username: sess.username}
	err := p.foxhole.actions.Purge(context.WithoutCancel(r.Context()), scope, by)
	var missing *commands.MissingRoleError
	switch {
	case errors.Is(err, commands.ErrActionRunning):
		p.refuseAction(w, r, sess, errActionRunning, "scope", scope)
		return
	case errors.Is(err, commands.ErrMemberListPartial):
		p.refuseAction(w, r, sess, errActionListPartial, "scope", scope)
		return
	case errors.As(err, &missing):
		p.refuseAction(w, r, sess, roleMissingRefusal(missing.Role), "scope", scope, "role", missing.Role)
		return
	}
	if err != nil {
		p.serverError(w, "purge start", err)
		return
	}
	http.Redirect(w, r, foxholePath, http.StatusSeeOther)
}

// refuseAction answers a refused Foxhole action with the page as it stands
// and the refusal on it, and logs the refusal's INFO line with kv.
func (p *Panel) refuseAction(w http.ResponseWriter, r *http.Request, sess session, refusal *saveRefusal, kv ...any) {
	utils.Info(refusal.log, append(kv, "username", sess.username, "forum_user_id", sess.userID)...)
	p.renderFoxhole(w, r, sess, refusal.status, foxholeRequest{Filter: filterAll, ActionRefusal: refusal})
}
