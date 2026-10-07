package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
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
	Running bool
	Outcome commands.ReportOutcome
	// StoppedBy is the forum user who stopped an action that ended stopped,
	// empty otherwise.
	StoppedBy    string
	Changed      []reportMember
	Skipped      []reportMember
	Failed       []reportMember
	NotAttempted []reportMember
	// AddedNobody are an add's pasted lines that added nobody.
	AddedNobody []reportLine
	// StopPressedBy is the forum user who pressed Stop on the running
	// action, which stops after the change in flight. Empty until someone
	// does.
	StopPressedBy string
	// Paused is the running action paused before its next member, waiting
	// for the member list.
	Paused bool
	// Done counts the members the action has been through, of Total, and
	// Left is the rough time the rest take.
	Done, Total int
	Left        estimate
}

// reportLine is a line pasted for an add that added nobody, as its report
// lists it.
type reportLine struct {
	commands.PastedLine
}

// Why is why the line added nobody, as the report says it. A line matching
// more than a pick row offers had nothing to pick from.
func (l reportLine) Why() string {
	switch {
	case l.Reason == commands.LineNonePicked && l.Matches > maxChoices:
		return fmt.Sprintf("matched %d members, too many to pick from", l.Matches)
	case l.Reason == commands.LineNonePicked:
		return fmt.Sprintf("matched %d members, none picked", l.Matches)
	}
	return lineReasonWords(l.Reason, l.SameAs)
}

// reportMember is one member a report names, with the role's name as the
// page shows it.
type reportMember struct {
	commands.ReportMember
	RoleName string
}

// OutcomeLabel is how the action ended, as the page says it. The page's
// "outcome" template names who stopped a stopped action itself, around its
// stopped-by marker.
func (r reportView) OutcomeLabel() string {
	switch {
	case r.Running:
		return "Running"
	case r.Outcome == commands.ReportDone:
		return "Done"
	case r.Outcome == commands.ReportMemberListGone:
		return "Stopped: Discord's member list didn't arrive"
	case r.Outcome == commands.ReportRestart:
		return "Stopped by a restart"
	}
	return string(r.Outcome)
}

// PauseLimit is how long a paused action waits for the member list before
// it stops, as the progress block says it.
func (reportView) PauseLimit() string {
	return fmt.Sprintf("%d s", int(commands.FoxholePauseLimit/time.Second))
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
	case commands.SkipHolding:
		return "already holds " + m.RoleName
	case commands.SkipLeft:
		if m.Trooper != "" {
			// A roster's trooper may never have been in the server.
			return "isn't in the server"
		}
		return "left the server"
	case commands.SkipNoDiscord:
		return "no Discord account on the milpac"
	}
	return m.Failure
}

// reportViewOf decodes a report the store holds for the page. Its name is
// the action's, with a purge's scope or a removal's role.
func reportViewOf(stored store.FoxholeReport) (*reportView, error) {
	var report commands.ActionReport
	if err := json.Unmarshal(stored.Entry.Diff, &report); err != nil {
		return nil, fmt.Errorf("decode Foxhole report %d: %w", stored.Entry.ID, err)
	}
	name := actionNames[stored.Entry.Action]
	if scope := scopeLabels[report.Scope]; scope != "" {
		name += " " + scope
	}
	if role := foxholeRoleLabels[report.Role]; role != "" {
		name += " " + role
	}
	if report.Unit != "" {
		name = report.Unit + " " + name
	}
	view := &reportView{ID: stored.Entry.ID, Action: stored.Entry.Action, Name: name,
		StartedBy: stored.Entry.ForumUsername, StartedAt: stored.Entry.At, EndedAt: report.EndedAt,
		Running: stored.Running, Outcome: report.Outcome,
		Changed: reportMembers(report.Changed), Skipped: reportMembers(report.Skipped), Failed: reportMembers(report.Failed),
		NotAttempted: reportMembers(report.NotAttempted)}
	for _, line := range report.AddedNobody {
		view.AddedNobody = append(view.AddedNobody, reportLine{PastedLine: line})
	}
	if report.StoppedBy != nil {
		view.StoppedBy = report.StoppedBy.Username
	}
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

// actionNames are the Foxhole actions as the page names them. An entry of
// the Foxhole change log whose action is among them is that action's
// report.
var actionNames = map[store.ChangeAction]string{
	store.ChangePurge:   "Purge",
	store.ChangeRemoval: "Remove",
	store.ChangeReAdd:   "Re-add approved collaborators",
	store.ChangeAdd:     "Add",
	// A roster add's name follows its unit, as in "D/ACD roster add".
	store.ChangeRosterAdd: "roster add",
}

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

// removePreview is the preview a removal waits on: the role, the members
// selected who lose it, those it skips, for External the approved
// collaborators among the losers, whose approval it clears, how many of
// its members have a note, and the rough time at one change a second.
type removePreview struct {
	Role    commands.FoxholeRole
	Name    string
	Loses   []previewMember
	Skipped []previewMember
	Cleared []previewMember
	// Notes counts the members the preview names who have a note.
	Notes    int
	Estimate estimate
	// Back is the address of the view the selection was made in, where
	// Cancel leads.
	Back string
}

// previewMember is one member a preview names, under the names the member
// list shows, or the record's when it doesn't hold them, with their note.
type previewMember struct {
	ID          string
	DisplayName string
	Username    string
	Note        string
	// Skip is why the removal skips the member: they don't hold the role,
	// or they left the server. RoleName is the role as the page names it.
	Skip     commands.SkipReason
	RoleName string
}

// Why is why the removal skips the member, as the preview says it. Empty
// for a member who loses the role.
func (m previewMember) Why() string {
	switch m.Skip {
	case commands.SkipNotHolding:
		return "doesn't hold " + m.RoleName
	case commands.SkipLeft:
		return "not in the server"
	}
	return ""
}

// removePreviewOf is the preview of a removal of the request's role from
// the members it selected, in display name order, read from a complete
// member list and the guild's Foxhole records.
func (s foxholeService) removePreviewOf(list commands.MemberListSnapshot, records map[string]store.FoxholeRecord, req foxholeRequest) *removePreview {
	guild := s.foxholeGuildOf()
	name := foxholeRoleLabels[req.Remove]
	preview := &removePreview{Role: req.Remove, Name: name, Back: foxholeAddress(req.Query, req.Filter)}
	for _, id := range distinct(req.RemoveMembers) {
		rec := records[id]
		member := previewMember{ID: id, DisplayName: rec.DisplayName, Username: rec.Username, Note: rec.Note, RoleName: name}
		mem, inServer := list.Member(id)
		if inServer {
			member.DisplayName, member.Username = mem.DisplayName(), mem.Username
		}
		if member.Note != "" {
			preview.Notes++
		}
		switch {
		case !inServer:
			member.Skip = commands.SkipLeft
			preview.Skipped = append(preview.Skipped, member)
		case !guild.rowOf(mem, rec).holdsRole(req.Remove):
			member.Skip = commands.SkipNotHolding
			preview.Skipped = append(preview.Skipped, member)
		default:
			preview.Loses = append(preview.Loses, member)
			if req.Remove.RemovalClearsApproval() && rec.Approved {
				preview.Cleared = append(preview.Cleared, member)
			}
		}
	}
	for _, members := range [][]previewMember{preview.Loses, preview.Skipped, preview.Cleared} {
		slices.SortFunc(members, func(a, b previewMember) int { return byName(a.DisplayName, a.ID, b.DisplayName, b.ID) })
	}
	preview.Estimate = estimateFor(len(preview.Loses))
	return preview
}

// reAddCounts is what the After a war block says a re-add would do: how
// many approved collaborators hold External already, how many in the server
// don't, and how many left the server.
type reAddCounts struct {
	Holding, NotHolding, NotInServer int
}

// Approved is how many approved collaborators there are.
func (c reAddCounts) Approved() int { return c.Holding + c.NotHolding + c.NotInServer }

// reAddCountsOf counts the approved collaborators among the holder list's
// rows, by the flags the rows show.
func reAddCountsOf(holders []holderRow) *reAddCounts {
	c := &reAddCounts{}
	for _, h := range holders {
		switch {
		case !h.Approved:
		case h.NotInServer:
			c.NotInServer++
		case h.External:
			c.Holding++
		default:
			c.NotHolding++
		}
	}
	return c
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

// actionRunningRefusal refuses a Foxhole action, named by what, started
// while another runs. The page shows the running action's progress block,
// which names it and who started it.
func actionRunningRefusal(what string) *saveRefusal {
	return &saveRefusal{Kind: "action-running", status: http.StatusConflict,
		log:     "Panel action refused: another action running",
		Message: fmt.Sprintf("Another Foxhole action is running, so %s didn't start. Nothing changed. Try again when it ends.", what)}
}

// actionListPartialRefusal refuses a Foxhole action, named by what, started
// while the member list is partial: it could not see who holds a role.
func actionListPartialRefusal(what string) *saveRefusal {
	return &saveRefusal{Kind: "member-list", status: http.StatusServiceUnavailable,
		log:     "Panel action refused: member list partial",
		Message: fmt.Sprintf("Cavbot2 doesn't have the whole member list from Discord yet, so %s didn't start. Nothing changed. Try again once the list has arrived.", what)}
}

// errAddListPartial refuses an add's Preview pressed while the member list
// is partial: no line can be matched. The page keeps the lines pasted, to
// preview again once the list has arrived.
var errAddListPartial = &saveRefusal{Kind: "member-list", status: http.StatusServiceUnavailable,
	log:     "Panel action refused: member list partial",
	Message: "Cavbot2 doesn't have the whole member list from Discord yet, so it can't match the pasted lines. Nothing changed. Preview again once the list has arrived."}

// removeListPartialRefusal refuses a removal of the role named, its preview
// opened or its Confirm pressed while the member list is partial: it can't
// tell who holds the role. The page keeps no selection, so the manager
// selects again once the holder list is back.
func removeListPartialRefusal(role string) *saveRefusal {
	return &saveRefusal{Kind: "member-list", status: http.StatusServiceUnavailable,
		log:     "Panel action refused: member list partial",
		Message: fmt.Sprintf("Cavbot2 doesn't have the whole member list from Discord yet, so it can't tell who holds %s. Nothing changed. Select the members again once the list has arrived.", role)}
}

// roleMissingRefusal refuses a Foxhole action, named by what, that changes
// a role the guild doesn't hold by its configured name. The runtime has
// reported it to Sentry.
func roleMissingRefusal(what, role string) *saveRefusal {
	return &saveRefusal{Kind: "role-missing", status: http.StatusUnprocessableEntity,
		log:     "Panel action refused: Foxhole role not found",
		Message: fmt.Sprintf("The server has no role named %s, so %s didn't start. Nothing changed. The bot has reported it.", role, what)}
}

// startPurge is POST /foxhole/purge, the purge confirmation's Confirm: it
// starts the purge of the scope confirmed, as startAction says.
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
	p.startAction(w, r, sess, actionPage, "the purge", func(ctx context.Context, by commands.ForumUser) error {
		return p.foxhole.actions.Purge(ctx, scope, by)
	}, "scope", scope)
}

// startRemoval is POST /foxhole/remove, the remove preview's Confirm: it
// starts the removal of the role confirmed from the members the preview
// listed as losing it, as startAction says.
func (p *Panel) startRemoval(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	role, ok := commands.ParseFoxholeRole(r.PostForm.Get(fieldRole))
	if !ok {
		http.Error(w, "the form names no Foxhole role, so nothing changed", http.StatusBadRequest)
		return
	}
	members := r.PostForm[fieldMember]
	if len(members) == 0 {
		http.Error(w, "the form names no member, so nothing changed", http.StatusBadRequest)
		return
	}
	p.startAction(w, r, sess, actionPage, "the removal", func(ctx context.Context, by commands.ForumUser) error {
		err := p.foxhole.actions.Remove(ctx, role, members, by)
		if errors.Is(err, commands.ErrMemberListPartial) {
			// The page the refusal answers with keeps no selection: it says
			// to select again, as the preview's refusal does.
			return removeListPartialRefusal(foxholeRoleLabels[role])
		}
		return err
	}, "role", role, "members", len(members))
}

// startReAdd is POST /foxhole/re-add, the After a war block's Re-add
// approved collaborators: one button with no preview, which starts the
// re-add as startAction says.
func (p *Panel) startReAdd(w http.ResponseWriter, r *http.Request, sess session) {
	p.startAction(w, r, sess, actionPage, "the re-add", p.foxhole.actions.ReAdd)
}

// startAction starts a Foxhole action, named by what, through start and
// redirects straight back to the Foxhole page. The action runs in the
// background to its end, whether or not the browser waits, so the request
// never waits on it. A refused action answers with the page back asks for
// and the refusal, and logs it with kv. A start that returns a *saveRefusal
// refuses in its own words.
func (p *Panel) startAction(w http.ResponseWriter, r *http.Request, sess session, back foxholeRequest, what string,
	start func(context.Context, commands.ForumUser) error, kv ...any) {
	err := start(context.WithoutCancel(r.Context()), sess.forumUser())
	var (
		missing *commands.MissingRoleError
		refusal *saveRefusal
	)
	switch {
	case errors.As(err, &refusal):
		p.refuseAction(w, r, sess, back, refusal, kv...)
		return
	case errors.Is(err, commands.ErrActionRunning):
		p.refuseAction(w, r, sess, back, actionRunningRefusal(what), kv...)
		return
	case errors.Is(err, commands.ErrMemberListPartial):
		p.refuseAction(w, r, sess, back, actionListPartialRefusal(what), kv...)
		return
	case errors.As(err, &missing):
		p.refuseAction(w, r, sess, back, roleMissingRefusal(what, missing.Role), append(kv, "role", missing.Role)...)
		return
	}
	if err != nil {
		p.serverError(w, "Foxhole action start", fmt.Errorf("start %s: %w", what, err))
		return
	}
	http.Redirect(w, r, foxholePath, http.StatusSeeOther)
}

// stopAction is POST /foxhole/stop, the progress block's Stop: it asks the
// running Foxhole action to stop after the change in flight, and redirects
// straight back to the Foxhole page. There is no confirmation step.
func (p *Panel) stopAction(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	reportID, err := strconv.ParseInt(r.PostForm.Get(fieldReport), 10, 64)
	if err != nil {
		http.Error(w, "the form names no action, so nothing stopped", http.StatusBadRequest)
		return
	}
	stopped := p.foxhole.actions.Stop(reportID, sess.forumUser())
	utils.Info("Panel Foxhole Stop pressed", "report_id", reportID, "action_stopping", stopped,
		"username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, foxholePath, http.StatusSeeOther)
}

// forumUser is the forum user this session's Foxhole actions name.
func (s session) forumUser() commands.ForumUser {
	return commands.ForumUser{ID: s.userID, Username: s.username}
}

// refuseAction answers a refused Foxhole action with the page back asks
// for, as it stands, and the refusal on it, and logs the refusal's INFO
// line with kv.
func (p *Panel) refuseAction(w http.ResponseWriter, r *http.Request, sess session, back foxholeRequest, refusal *saveRefusal, kv ...any) {
	utils.Info(refusal.log, append(kv, "username", sess.username, "forum_user_id", sess.userID)...)
	back.ActionRefusal = refusal
	p.renderFoxhole(w, r, sess, refusal.status, back)
}

// actionPage is the page a refused purge, removal or re-add answers with:
// the holder list unfiltered, with nothing open.
var actionPage = foxholeRequest{Filter: filterAll}
