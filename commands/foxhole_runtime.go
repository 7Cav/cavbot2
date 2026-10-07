package commands

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// Foxhole actions started on the Foxhole page (spec #434, GLOSSARY.md). The
// runtime here owns them, as the temp VC runtime owns spawned channels: it
// starts each in the background, so a page's request never waits on one
// and one outlives its browser, and it writes the action's report to the
// store as the action goes. The report is the action's change log entry.

// FoxholeRole is one of the two Foxhole roles, as a Foxhole action names
// it.
type FoxholeRole string

const (
	FoxholeInternal FoxholeRole = "internal"
	FoxholeExternal FoxholeRole = "external"
)

// ParseFoxholeRole reads a Foxhole role as a form posts it, and reports
// false for one that names neither.
func ParseFoxholeRole(raw string) (FoxholeRole, bool) {
	switch role := FoxholeRole(raw); role {
	case FoxholeInternal, FoxholeExternal:
		return role, true
	}
	return "", false
}

// RemovalClearsApproval reports whether a removal of the role on the
// Foxhole page clears the approval of each member it takes the role from.
// Only External's does: approved collaborators are External only. A purge
// never clears one.
func (r FoxholeRole) RemovalClearsApproval() bool { return r == FoxholeExternal }

// PurgeScope is which Foxhole roles a purge takes off their holders: both,
// or Internal or External alone.
type PurgeScope string

const (
	PurgeBoth     PurgeScope = "both"
	PurgeInternal PurgeScope = "internal"
	PurgeExternal PurgeScope = "external"
)

// ParsePurgeScope reads a scope as a form posts it, and reports false for
// one that names no scope.
func ParsePurgeScope(raw string) (PurgeScope, bool) {
	switch scope := PurgeScope(raw); scope {
	case PurgeBoth, PurgeInternal, PurgeExternal:
		return scope, true
	}
	return "", false
}

// Roles are the Foxhole roles the scope names, in the order a purge takes
// them: External first, so a purge that stops partway leaves the Cav
// members' role half done, never the collaborators'.
func (s PurgeScope) Roles() []FoxholeRole {
	switch s {
	case PurgeInternal:
		return []FoxholeRole{FoxholeInternal}
	case PurgeExternal:
		return []FoxholeRole{FoxholeExternal}
	}
	return []FoxholeRole{FoxholeExternal, FoxholeInternal}
}

// foxholeStoreTimeout bounds each store call a Foxhole action makes, so a
// stalled database never hangs the action.
const foxholeStoreTimeout = 5 * time.Second

// foxholeEndAttempts is how many times an action tries the write that ends
// its report. A report left unended shows a running action to every
// manager until the next one, so one store blip mustn't leave it so.
const foxholeEndAttempts = 3

// FoxholePauseLimit is how long a Foxhole action stays paused before its
// next member, waiting for a complete member list with the bot connected,
// before it stops. A paused action holds the one-action-at-a-time rule,
// which blocks the commands, and they don't need the list.
const FoxholePauseLimit = 60 * time.Second

// FoxholePausePoll is how often a paused action reads the member list
// again. It matches the rate of one role change a second, so a pause reads
// the list no more often than a running action does.
const FoxholePausePoll = time.Second

// foxholeNow is the Foxhole runtime's clock, a package var beside
// tempVCNow.
var foxholeNow = time.Now

// FoxholeAfterFunc runs f on a goroutine of its own once d has passed, and
// returns a stop that keeps f from running if it has not started, as
// time.Timer.Stop does. A Foxhole action's pause is timed through it: its
// limit and each read of the list. A package var beside foxholeNow,
// exported so the panel's tests can run a pause on a fake clock. Each
// runtime takes it when it is built, so a runtime already running keeps the
// clock it was built with.
var FoxholeAfterFunc = func(d time.Duration, f func()) (stop func()) {
	timer := time.AfterFunc(d, f)
	return func() { timer.Stop() }
}

// FoxholeGuildReader is the subset of the manager seam a Foxhole action
// reads the guild through, from the gateway state and never Discord's API:
// the member list, and the guild's roles. TempVCManager has both.
type FoxholeGuildReader interface {
	MemberList(guildID string) MemberListSnapshot
	GuildData(guildID string) GuildSnapshot
}

// FoxholeRoleWriter is the subset of GuildManager a Foxhole action changes
// roles through: one member's role at a time, with the audit log reason,
// which the production adapter URL-encodes. The production adapter waits
// out a rate limit and retries, so a member never fails on a bucket that
// resets in seconds, and discordgo paces the calls to Discord's per-guild
// member-role rate, about one a second. Adds and removals share that rate.
type FoxholeRoleWriter interface {
	GuildMemberRoleAdd(guildID, userID, roleID, auditReason string) error
	GuildMemberRoleRemove(guildID, userID, roleID, auditReason string) error
}

// ForumUser is a forum user who started or stopped a Foxhole action on the
// page.
type ForumUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

// auditName names the forum user in an audit log reason, the way the
// panel's hub changes do.
func (u ForumUser) auditName() string {
	return fmt.Sprintf("%s (forum user %d)", u.Username, u.ID)
}

// ErrMemberListPartial refuses a Foxhole action started while the member
// list isn't complete: the action could not see who holds a role. Nothing
// changed.
var ErrMemberListPartial = errors.New("the member list isn't complete")

// MissingRoleError refuses a Foxhole action naming a Foxhole role the
// guild doesn't hold by the name the commands use. The name comes from
// configuration, not the manager, so the fault reaches Sentry (ADR 0002).
// Nothing changed.
type MissingRoleError struct {
	// Role is the missing role's name.
	Role string
}

func (e *MissingRoleError) Error() string {
	return fmt.Sprintf("the %q role isn't in the server", e.Role)
}

// ErrActionRunning refuses a Foxhole action started while another runs:
// one action at a time, so two never fight over the same roles. Nothing
// changed.
var ErrActionRunning = errors.New("another Foxhole action is running")

// ReportOutcome is how a Foxhole action ended.
type ReportOutcome string

const (
	// ReportDone is an action that went through every member it set out to.
	ReportDone ReportOutcome = "done"
	// ReportMemberListGone is an action that stopped when the member list
	// went partial under it: it could no longer check a member before
	// changing them. What it hadn't reached is not attempted.
	ReportMemberListGone ReportOutcome = "member-list"
	// ReportStopped is an action a Foxhole manager or panel admin stopped.
	// The change in flight finished; what it hadn't reached is not
	// attempted. The report names who stopped it.
	ReportStopped ReportOutcome = "stopped"
	// ReportRestart is an action the bot's restart cut off: a deploy or a
	// crash. Its report holds what it had written before the restart, and
	// the action never resumes.
	ReportRestart ReportOutcome = "restart"
)

// ActionReport is a Foxhole action's report as its change log entry's diff
// holds it. The entry itself holds who started the action and when.
type ActionReport struct {
	// Scope is a purge's scope.
	Scope PurgeScope `json:"scope,omitempty"`
	// Role is a removal's role, or an add's.
	Role FoxholeRole `json:"role,omitempty"`
	// Unit is a roster add's validated internal unit, by its label.
	Unit string `json:"unit,omitempty"`
	// Outcome is how the action ended, empty while it runs.
	Outcome ReportOutcome `json:"outcome,omitempty"`
	// EndedAt is when the action ended, zero while it runs.
	EndedAt time.Time `json:"ended_at,omitzero"`
	// StoppedBy is who stopped an action that ended ReportStopped.
	StoppedBy *ForumUser `json:"stopped_by,omitempty"`
	// Changed are the members whose role the action changed.
	Changed []ReportMember `json:"changed"`
	// Skipped are the members the action reached and left as they were,
	// each with the reason.
	Skipped []ReportMember `json:"skipped"`
	// Failed are the members whose change Discord refused, each with
	// Discord's reason in plain words.
	Failed []ReportMember `json:"failed"`
	// NotAttempted are the members the action set out to change and has
	// not reached: while it runs, the members still to come.
	NotAttempted []ReportMember `json:"not_attempted"`
	// AddedNobody are an add's pasted lines that named no member to add
	// when it started, in the order pasted.
	AddedNobody []PastedLine `json:"added_nobody,omitempty"`
}

// PastedLine is a line pasted for an add that named no member to add when
// the add started, and why.
type PastedLine struct {
	// Line is the line's number in the paste box, counting blank lines, and
	// Text the line as pasted.
	Line int    `json:"line"`
	Text string `json:"text"`
	// Reason is why the line added nobody.
	Reason LineReason `json:"reason"`
	// Matches counts the members a LineNonePicked line matched.
	Matches int `json:"matches,omitempty"`
	// SameAs is the line whose member a LineSameMember line named too.
	SameAs int `json:"same_as,omitempty"`
}

// LineReason is why a pasted line added nobody.
type LineReason string

const (
	// LineNoMatch is a line no member's name matches.
	LineNoMatch LineReason = "no-match"
	// LineNoSuchID is a line naming a Discord ID no member of the server
	// has.
	LineNoSuchID LineReason = "no-such-id"
	// LineNonePicked is a line matching several members, none of whom was
	// picked.
	LineNonePicked LineReason = "none-picked"
	// LineSameMember is a line naming a member another line named, whom the
	// add gives the role once.
	LineSameMember LineReason = "same-member"
)

// ReportMember is one member a report names, under the names the member
// list showed when the action reached them, with the role the action
// changed or meant to.
type ReportMember struct {
	ID          string      `json:"id"`
	DisplayName string      `json:"display_name"`
	Username    string      `json:"username"`
	Role        FoxholeRole `json:"role"`
	// Trooper is a roster add's member's forum username on the roster. A
	// trooper with no Discord account on the milpac has no ID.
	Trooper string `json:"trooper,omitempty"`
	// Skip is why a skipped member was skipped.
	Skip SkipReason `json:"skip,omitempty"`
	// Failure is why Discord refused a failed member's change, in plain
	// words.
	Failure string `json:"failure,omitempty"`
	// ApprovalCleared is a member a removal took External from whose
	// approval it cleared.
	ApprovalCleared bool `json:"approval_cleared,omitempty"`
}

// SkipReason is why a Foxhole action skipped a member it reached.
type SkipReason string

const (
	// SkipNotHolding is a member who no longer held the role the action was
	// taking.
	SkipNotHolding SkipReason = "not-holding"
	// SkipHolding is a member who already held the role the action was
	// giving.
	SkipHolding SkipReason = "holding"
	// SkipLeft is a member who had left the server.
	SkipLeft SkipReason = "left"
	// SkipNotInServer is a roster add's trooper whose Discord ID no member
	// of the server has. A trooper may never have joined, so a roster add
	// says so rather than SkipLeft.
	SkipNotInServer SkipReason = "not-in-server"
	// SkipNoDiscord is a roster add's trooper whose milpac names no Discord
	// account, whom it skips before it starts.
	SkipNoDiscord SkipReason = "no-discord"
)

// RosterTrooper is one trooper on a validated internal unit's roster, as a
// roster add takes them: their forum username, and the Discord ID their
// milpac names, empty for none.
type RosterTrooper struct {
	Username  string
	DiscordID string
}

// FoxholeRoleIDs finds the Foxhole roles among the guild's roles by the
// exact names the commands use, and returns each one's ID, empty for a role
// the guild lacks.
func FoxholeRoleIDs(guild GuildSnapshot) map[FoxholeRole]string {
	internalName, externalName := FoxholeRoleNames()
	ids := map[FoxholeRole]string{}
	for _, r := range guild.Roles {
		switch {
		case r.Name == internalName && ids[FoxholeInternal] == "":
			ids[FoxholeInternal] = r.ID
		case r.Name == externalName && ids[FoxholeExternal] == "":
			ids[FoxholeExternal] = r.ID
		}
	}
	return ids
}

// FoxholeRuntime owns the Foxhole actions started on the Foxhole page, and
// runs one at a time.
type FoxholeRuntime struct {
	guild   FoxholeGuildReader
	roles   FoxholeRoleWriter
	store   store.Store
	guildID string
	// afterFunc times a pause: FoxholeAfterFunc when the runtime was built.
	afterFunc func(d time.Duration, f func()) (stop func())

	mu sync.Mutex
	// running is the action running, nil when none is. An action running
	// holds the one-action-at-a-time rule.
	running *actionState
}

// actionState is what the runtime knows of the action running, beside its
// report in the store.
type actionState struct {
	// reportID is the action's report, 0 until it is written.
	reportID int64
	// stopPressedBy is who pressed Stop, nil until someone does, and stop
	// closes when someone does.
	stopPressedBy *ForumUser
	stop          chan struct{}
	// paused is the action paused before its next member.
	paused bool
}

// RunningAction is the Foxhole action running now, as the page shows it
// beside its report.
type RunningAction struct {
	// StopPressedBy is the username of the forum user who pressed Stop,
	// empty until someone does. The action stops after the change in
	// flight.
	StopPressedBy string
	// Paused is the action paused before its next member, waiting for a
	// complete member list with the bot connected.
	Paused bool
}

// Running reports the Foxhole action running, and false when none is.
func (r *FoxholeRuntime) Running() (RunningAction, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running == nil {
		return RunningAction{}, false
	}
	action := RunningAction{Paused: r.running.paused}
	if by := r.running.stopPressedBy; by != nil {
		action.StopPressedBy = by.Username
	}
	return action, true
}

// claim takes the one-action-at-a-time rule, and reports false when an
// action already holds it. The channel it returns closes when someone
// presses the action's Stop.
func (r *FoxholeRuntime) claim() (<-chan struct{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running != nil {
		return nil, false
	}
	r.running = &actionState{stop: make(chan struct{})}
	return r.running.stop, true
}

// started records the running action's report.
func (r *FoxholeRuntime) started(reportID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running.reportID = reportID
}

// Stop asks the running Foxhole action whose report has the ID given to
// stop, on behalf of the forum user given, and reports whether one was
// running. It stops after the change in flight, and its report names them.
// A Stop from a page that showed an action since ended stops nothing,
// whatever runs now.
func (r *FoxholeRuntime) Stop(reportID int64, by ForumUser) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	action := r.running
	if action == nil || action.reportID == 0 || action.reportID != reportID {
		return false
	}
	if action.stopPressedBy == nil {
		action.stopPressedBy = &by
		close(action.stop)
	}
	return true
}

// stopPressedBy is who pressed Stop on the running action, nil for nobody.
func (r *FoxholeRuntime) stopPressedBy() *ForumUser {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running.stopPressedBy
}

// setPaused records whether the running action is paused.
func (r *FoxholeRuntime) setPaused(paused bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running.paused = paused
}

// release gives the rule back. On a free rule it does nothing.
func (r *FoxholeRuntime) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = nil
}

// NewFoxholeRuntime builds the runtime over the guild's gateway state, the
// role calls and the bot's store. It ends every report the store still
// holds running as stopped by a restart, since the action that wrote it
// stopped with the process before it ended, and it never resumes one. An
// error is the store failing that work.
func NewFoxholeRuntime(guild FoxholeGuildReader, roles FoxholeRoleWriter, st store.Store, guildID string) (*FoxholeRuntime, error) {
	r := &FoxholeRuntime{guild: guild, roles: roles, store: st, guildID: guildID, afterFunc: FoxholeAfterFunc}
	ctx, cancel := context.WithTimeout(context.Background(), foxholeStoreTimeout)
	defer cancel()
	if err := r.endCutOff(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// endCutOff ends each report still running with the outcome
// ReportRestart. Each keeps the members its action had written as changed,
// skipped and failed, and lists the rest as not attempted.
func (r *FoxholeRuntime) endCutOff(ctx context.Context) error {
	running, err := r.store.RunningFoxholeReports(ctx)
	if err != nil {
		return fmt.Errorf("list running Foxhole reports: %w", err)
	}
	for _, entry := range running {
		var report ActionReport
		if err := json.Unmarshal(entry.Diff, &report); err != nil {
			// Only the runtime writes a report, so this is a fault. The report
			// still ends, with the members it could read, so no page shows its
			// action running for good.
			captureError("Foxhole report unreadable at startup", err, "report_id", entry.ID)
		}
		report.Outcome, report.EndedAt = ReportRestart, foxholeNow().UTC()
		raw, err := json.Marshal(report)
		if err != nil {
			return fmt.Errorf("encode Foxhole report %d: %w", entry.ID, err)
		}
		if err := r.store.EndFoxholeReport(ctx, entry.ID, raw); err != nil {
			return fmt.Errorf("end Foxhole report %d: %w", entry.ID, err)
		}
		utils.Info("Foxhole action stopped by a restart", "action", entry.Action, "report_id", entry.ID,
			"changed", len(report.Changed), "not_attempted", len(report.NotAttempted))
	}
	return nil
}

// plannedChange is one role change an action sets out to make: the member
// as the member list showed them when it started, and the role's ID.
type plannedChange struct {
	member ReportMember
	roleID string
}

// actionRun is what an action's run carries besides its plan: the action,
// whether it gives the role or takes it, its report's ID, and the audit log
// reason each of its changes sends.
type actionRun struct {
	action   store.ChangeAction
	grant    bool
	reportID int64
	reason   string
	// clearsApproval is a removal of External, which clears the approval of
	// each member it takes the role from.
	clearsApproval bool
	// absent is the reason a member not in the server is skipped with.
	absent SkipReason
	// stop closes when someone presses the action's Stop.
	stop <-chan struct{}
}

// actionSpec is a Foxhole action as start starts it.
type actionSpec struct {
	action store.ChangeAction
	// scope is a purge's scope, and role a removal's or an add's role, for
	// its report.
	scope PurgeScope
	role  FoxholeRole
	// roles are the Foxhole roles the action changes. The guild must hold
	// each.
	roles []FoxholeRole
	// grant is an action giving the role. One that doesn't takes it.
	grant bool
	// clearsApproval is an action that clears the approval of each member it
	// takes External from.
	clearsApproval bool
	// reason is the audit log reason's wording before the forum user who
	// started the action.
	reason string
	// addedNobody are an add's pasted lines that named no member to add.
	addedNobody []PastedLine
	// unit is a roster add's unit, by its label, for its report.
	unit string
	// skipped are the members the action skips before it starts: a roster
	// add's troopers with no Discord account on the milpac.
	skipped []ReportMember
	// absent is the reason a member not in the server is skipped with:
	// SkipLeft when empty.
	absent SkipReason
	// plan is the action's role changes, over the complete member list as it
	// stands, the guild's Foxhole role IDs and the guild's Foxhole records.
	plan func(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord) []plannedChange
}

// Purge starts a purge of the scope given, started by the forum user
// given, and returns once its report is written. It starts as start says.
// The purge then runs in the background, with no deadline of ctx's, to its
// end: one member at a time, External holders first, it takes the role off
// each holder the member list showed at the start. It never deletes or
// recreates a role, so role IDs and channel overwrites stay.
func (r *FoxholeRuntime) Purge(ctx context.Context, scope PurgeScope, by ForumUser) error {
	return r.start(ctx, actionSpec{
		action: store.ChangePurge, scope: scope, roles: scope.Roles(), reason: "Panel: Foxhole purge by ",
		plan: func(list MemberListSnapshot, roleIDs map[FoxholeRole]string, _ []store.FoxholeRecord) []plannedChange {
			return purgePlan(list, roleIDs, scope)
		},
	}, by)
}

// ReAdd starts a re-add of the approved collaborators, started by the forum
// user given, and returns once its report is written. It starts as start
// says. The re-add then runs in the background, with no deadline of ctx's,
// to its end: one member at a time, it gives External to each approved
// collaborator the store held at the start. One who left the server, or
// who holds External already, is skipped with that reason.
func (r *FoxholeRuntime) ReAdd(ctx context.Context, by ForumUser) error {
	return r.start(ctx, actionSpec{
		action: store.ChangeReAdd, roles: []FoxholeRole{FoxholeExternal}, grant: true,
		reason: "Panel: Foxhole re-add of approved collaborators by ", plan: reAddPlan,
	}, by)
}

// Remove starts a removal of the role given from the members given,
// started by the forum user given, and returns once its report is written.
// It starts as start says. The removal then runs in the background, with no
// deadline of ctx's, to its end: one member at a time, it takes the role
// off each. Taking External off an approved collaborator clears their
// approval too, which a purge never does.
func (r *FoxholeRuntime) Remove(ctx context.Context, role FoxholeRole, memberIDs []string, by ForumUser) error {
	return r.start(ctx, actionSpec{
		action: store.ChangeRemoval, role: role, roles: []FoxholeRole{role}, clearsApproval: role.RemovalClearsApproval(),
		reason: "Panel: Foxhole removal by ",
		plan: func(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord) []plannedChange {
			return removalPlan(list, roleIDs, records, role, memberIDs)
		},
	}, by)
}

// Add starts an add of the role to the members given, started by the
// forum user given, and returns once its report is written. It starts as
// start says. The report lists the pasted lines given that named no member
// to add. The add then runs in the background, with no deadline of ctx's,
// to its end: one member at a time, in the order given, it gives the role
// to each. One who holds it already, or who left the server, is skipped
// with that reason.
func (r *FoxholeRuntime) Add(ctx context.Context, role FoxholeRole, memberIDs []string, addedNobody []PastedLine, by ForumUser) error {
	return r.start(ctx, actionSpec{
		action: store.ChangeAdd, role: role, roles: []FoxholeRole{role}, grant: true, reason: "Panel: Foxhole add by ",
		addedNobody: addedNobody,
		plan: func(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord) []plannedChange {
			return addPlan(list, roleIDs, records, role, memberIDs)
		},
	}, by)
}

// RosterAdd starts an add of a validated internal unit's roster to
// Internal, the troopers given, started by the forum user given, and
// returns once its report is written. It starts as start says. The report
// lists each trooper whose milpac names no Discord account as skipped. The
// add then runs in the background, with no deadline of ctx's, to its end:
// one member at a time, in the order given, it gives Internal to each
// trooper's Discord ID. One who holds it already is skipped with that
// reason, and one who isn't in the server with SkipNotInServer.
func (r *FoxholeRuntime) RosterAdd(ctx context.Context, unit ValidatedInternalUnit, troopers []RosterTrooper, by ForumUser) error {
	var noDiscord []ReportMember
	for _, t := range troopers {
		if t.DiscordID == "" {
			noDiscord = append(noDiscord, ReportMember{Trooper: t.Username, Role: FoxholeInternal, Skip: SkipNoDiscord})
		}
	}
	return r.start(ctx, actionSpec{
		action: store.ChangeRosterAdd, unit: unit.Label, roles: []FoxholeRole{FoxholeInternal}, grant: true,
		reason: "Panel: Foxhole " + unit.Label + " roster add by ", skipped: noDiscord, absent: SkipNotInServer,
		plan: func(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord) []plannedChange {
			return rosterAddPlan(list, roleIDs, records, troopers)
		},
	}, by)
}

// start starts the Foxhole action spec names, started by the forum user
// given, and returns once its report is written. It starts only while no
// other Foxhole action runs, else ErrActionRunning, only with a complete
// member list, else ErrMemberListPartial, and only when the guild holds
// each role the action changes, else a *MissingRoleError. The action then
// runs in the background, with no deadline of ctx's.
func (r *FoxholeRuntime) start(ctx context.Context, spec actionSpec, by ForumUser) (err error) {
	stop, ok := r.claim()
	if !ok {
		return ErrActionRunning
	}
	defer func() {
		if err != nil {
			r.release()
		}
	}()
	list := r.guild.MemberList(r.guildID)
	if list.Status != MemberListComplete {
		return ErrMemberListPartial
	}
	roleIDs := FoxholeRoleIDs(r.guild.GuildData(r.guildID))
	if err := missingRole(roleIDs, spec.roles); err != nil {
		captureError("Foxhole role not found for an action", err, "role", err.Role, "action", spec.action, "scope", spec.scope)
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, foxholeStoreTimeout)
	defer cancel()
	records, err := r.store.ListFoxholeRecords(ctx, r.guildID)
	if err != nil {
		return fmt.Errorf("list Foxhole records: %w", err)
	}
	plan := spec.plan(list, roleIDs, records)
	report := ActionReport{Scope: spec.scope, Role: spec.role, Unit: spec.unit, Changed: []ReportMember{},
		Skipped: append([]ReportMember{}, spec.skipped...), Failed: []ReportMember{},
		NotAttempted: make([]ReportMember, 0, len(plan)), AddedNobody: spec.addedNobody}
	for _, p := range plan {
		report.NotAttempted = append(report.NotAttempted, p.member)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode the %s report: %w", spec.action, err)
	}
	entry, err := r.store.StartFoxholeReport(ctx, store.ChangeLogEntry{
		ForumUserID: by.ID, ForumUsername: by.Username, Action: spec.action, Diff: raw,
	})
	if err != nil {
		return fmt.Errorf("start the %s report: %w", spec.action, err)
	}
	utils.Info("Foxhole action started", "action", spec.action, "scope", spec.scope, "role", spec.role, "unit", spec.unit, "members", len(plan),
		"username", by.Username, "forum_user_id", by.ID)
	r.started(entry.ID)
	run := actionRun{action: spec.action, grant: spec.grant, reportID: entry.ID, reason: spec.reason + by.auditName(),
		clearsApproval: spec.clearsApproval, absent: cmp.Or(spec.absent, SkipLeft), stop: stop}
	go r.run(run, plan, report, namesOf(records))
	return nil
}

// namesOf is the last-seen names of each member with a Foxhole record, by
// member ID.
func namesOf(records []store.FoxholeRecord) map[string]store.MemberNames {
	names := make(map[string]store.MemberNames, len(records))
	for _, rec := range records {
		names[rec.MemberID] = store.MemberNames{MemberID: rec.MemberID, DisplayName: rec.DisplayName, Username: rec.Username}
	}
	return names
}

// refreshNames stores the names the member list shows for a member with a
// record whose names changed since the panel last saw them, and keeps
// names in step. A write that fails reaches Sentry and the action goes on:
// the next page load writes the names.
func (r *FoxholeRuntime) refreshNames(names map[string]store.MemberNames, mem ListedMember) {
	seen, ok := names[mem.ID]
	if !ok || (seen.DisplayName == mem.DisplayName() && seen.Username == mem.Username) {
		return
	}
	now := store.MemberNames{MemberID: mem.ID, DisplayName: mem.DisplayName(), Username: mem.Username}
	ctx, cancel := context.WithTimeout(context.Background(), foxholeStoreTimeout)
	defer cancel()
	if err := r.store.SetFoxholeRecordNames(ctx, r.guildID, []store.MemberNames{now}); err != nil {
		captureError("Foxhole action name refresh failed", err, "member_id", mem.ID)
		return
	}
	names[mem.ID] = now
}

// missingRole is the first of the roles that the guild lacks, nil when it
// holds them all.
func missingRole(roleIDs map[FoxholeRole]string, roles []FoxholeRole) *MissingRoleError {
	internalName, externalName := FoxholeRoleNames()
	names := map[FoxholeRole]string{FoxholeInternal: internalName, FoxholeExternal: externalName}
	for _, role := range roles {
		if roleIDs[role] == "" {
			return &MissingRoleError{Role: names[role]}
		}
	}
	return nil
}

// purgePlan is a purge's role changes over the member list as it stands:
// for each role the scope names, in order, every member holding it, in
// display name order.
func purgePlan(list MemberListSnapshot, roleIDs map[FoxholeRole]string, scope PurgeScope) []plannedChange {
	var plan []plannedChange
	for _, role := range scope.Roles() {
		id := roleIDs[role]
		var holders []plannedChange
		for _, m := range list.Members {
			if slices.Contains(m.RoleIDs, id) {
				holders = append(holders, plannedChange{
					member: ReportMember{ID: m.ID, DisplayName: m.DisplayName(), Username: m.Username, Role: role},
					roleID: id,
				})
			}
		}
		plan = append(plan, byDisplayName(holders)...)
	}
	return plan
}

// reAddPlan is a re-add's role changes: External for every approved
// collaborator, in display name order, whether or not they are in the
// server. The member list names those in it, and the records' last-seen
// names those who left.
func reAddPlan(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord) []plannedChange {
	var plan []plannedChange
	for _, rec := range records {
		if !rec.Approved {
			continue
		}
		seen := store.MemberNames{MemberID: rec.MemberID, DisplayName: rec.DisplayName, Username: rec.Username}
		plan = append(plan, plannedChange{member: plannedMember(list, seen, FoxholeExternal), roleID: roleIDs[FoxholeExternal]})
	}
	return byDisplayName(plan)
}

// removalPlan is a removal's role changes: the role for each member given,
// once, in display name order, whether or not they hold it or are in the
// server. The member list names those in it, and the records' last-seen
// names those who left.
func removalPlan(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord, role FoxholeRole, memberIDs []string) []plannedChange {
	return byDisplayName(plannedChanges(list, roleIDs, records, role, slices.Compact(slices.Sorted(slices.Values(memberIDs)))))
}

// addPlan is an add's role changes: the role for each member given, once,
// in the order given, whether or not they hold it or are in the server.
// The member list names those in it, and the records' last-seen names those
// who left.
func addPlan(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord, role FoxholeRole, memberIDs []string) []plannedChange {
	seen := map[string]bool{}
	once := slices.DeleteFunc(slices.Clone(memberIDs), func(id string) bool {
		repeat := seen[id]
		seen[id] = true
		return repeat
	})
	return plannedChanges(list, roleIDs, records, role, once)
}

// rosterAddPlan is a roster add's role changes: Internal for each trooper
// given with a Discord ID, once, in the order given, whether or not they
// hold it or are in the server, each named by their forum username too.
// The member list names those in it, and the records' last-seen names those
// who left.
func rosterAddPlan(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord, troopers []RosterTrooper) []plannedChange {
	names := namesOf(records)
	seen := map[string]bool{}
	var plan []plannedChange
	for _, t := range troopers {
		if t.DiscordID == "" || seen[t.DiscordID] {
			continue
		}
		seen[t.DiscordID] = true
		known := names[t.DiscordID]
		known.MemberID = t.DiscordID
		member := plannedMember(list, known, FoxholeInternal)
		member.Trooper = t.Username
		plan = append(plan, plannedChange{member: member, roleID: roleIDs[FoxholeInternal]})
	}
	return plan
}

// plannedChanges are the changes of the role for each member given, in the
// order given, under the names the member list shows, or the records'
// last-seen names for a member it doesn't hold.
func plannedChanges(list MemberListSnapshot, roleIDs map[FoxholeRole]string, records []store.FoxholeRecord, role FoxholeRole, memberIDs []string) []plannedChange {
	names := namesOf(records)
	plan := make([]plannedChange, 0, len(memberIDs))
	for _, id := range memberIDs {
		seen := names[id]
		seen.MemberID = id
		plan = append(plan, plannedChange{member: plannedMember(list, seen, role), roleID: roleIDs[role]})
	}
	return plan
}

// plannedMember is a member an action sets out to change the role given
// for, under the names the member list shows, or the last-seen names given
// when it doesn't hold them.
func plannedMember(list MemberListSnapshot, seen store.MemberNames, role FoxholeRole) ReportMember {
	member := ReportMember{ID: seen.MemberID, DisplayName: seen.DisplayName, Username: seen.Username, Role: role}
	if m, ok := list.Member(seen.MemberID); ok {
		member.DisplayName, member.Username = m.DisplayName(), m.Username
	}
	return member
}

// byDisplayName sorts changes by their member's display name, ignoring
// case, then by member ID, and returns them.
func byDisplayName(changes []plannedChange) []plannedChange {
	slices.SortFunc(changes, func(a, b plannedChange) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.member.DisplayName), strings.ToLower(b.member.DisplayName)),
			cmp.Compare(a.member.ID, b.member.ID))
	})
	return changes
}

// run makes an action's role changes one at a time, writing the report
// after each, and ends the report once it has been through them all, or
// sooner when someone presses Stop or a pause outlasts FoxholePauseLimit
// (awaitList). Just before it changes a member it reads them from the
// member list: one who no longer holds the role it takes, or who holds the
// role it gives already, is skipped with that reason, one not in the server
// with the run's absent reason, and one still in the server is named in the
// report under the names the list shows now, which also refresh their
// record's names.
func (r *FoxholeRuntime) run(run actionRun, plan []plannedChange, report ActionReport, names map[string]store.MemberNames) {
	defer utils.RecoverPanic("foxhole-action")
	// A panic gives the rule back too; release on a free rule does nothing.
	defer r.release()
	faults := newFaultCollector()
	outcome := ReportDone
	for _, p := range plan {
		list, end := r.awaitList(run.stop)
		if end != "" {
			outcome = end
			if end == ReportStopped {
				report.StoppedBy = r.stopPressedBy()
			}
			break
		}
		member := p.member
		mem, ok := list.Member(member.ID)
		if ok {
			r.refreshNames(names, mem)
			member.DisplayName, member.Username = mem.DisplayName(), mem.Username
		}
		holds := slices.Contains(mem.RoleIDs, p.roleID)
		switch {
		case !ok:
			member.Skip = run.absent
			report.Skipped = append(report.Skipped, member)
		case run.grant && holds:
			member.Skip = SkipHolding
			report.Skipped = append(report.Skipped, member)
		case !run.grant && !holds:
			member.Skip = SkipNotHolding
			report.Skipped = append(report.Skipped, member)
		default:
			if err := r.change(run, member.ID, p.roleID); err != nil {
				if classifyDiscordError(err).SystemFault {
					faults.recordSystemFault(err, member.ID)
				}
				member.Failure = failureReason(err)
				report.Failed = append(report.Failed, member)
			} else {
				if run.clearsApproval {
					member.ApprovalCleared = r.clearApproval(member.ID)
				}
				report.Changed = append(report.Changed, member)
			}
		}
		report.NotAttempted = report.NotAttempted[1:]
		r.writeReport(run.reportID, report, false)
	}
	report.Outcome, report.EndedAt = outcome, foxholeNow().UTC()
	// The action's last Discord change is made, so it gives the rule back
	// before it writes its end: whoever sees the report ended can start the
	// next.
	r.release()
	r.writeReport(run.reportID, report, true)
	faults.flush("Foxhole action role change failed", "action", run.action, "guild", r.guildID)
	utils.Info("Foxhole action ended", "action", run.action, "outcome", report.Outcome, "changed", len(report.Changed),
		"skipped", len(report.Skipped), "failed", len(report.Failed), "not_attempted", len(report.NotAttempted))
}

// clearApproval clears the approval of a member whose External a removal
// took, and reports whether they had one. A clear that fails reaches Sentry
// and the action goes on: the member keeps the approval, their row flags
// them "approved, doesn't hold External", and the report says nothing
// cleared.
func (r *FoxholeRuntime) clearApproval(memberID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), foxholeStoreTimeout)
	defer cancel()
	cleared, err := r.store.ClearFoxholeApprovalForRemoval(ctx, r.guildID, memberID)
	if err != nil {
		captureError("Foxhole removal approval clear failed", err, "member_id", memberID)
		return false
	}
	return cleared
}

// change gives the member the role, or takes it, as the run does, with the
// run's audit log reason.
func (r *FoxholeRuntime) change(run actionRun, memberID, roleID string) error {
	if run.grant {
		return r.roles.GuildMemberRoleAdd(r.guildID, memberID, roleID, run.reason)
	}
	return r.roles.GuildMemberRoleRemove(r.guildID, memberID, roleID, run.reason)
}

// awaitList reads the member list before an action's next member, and
// ends the action instead with the outcome it returns. A Stop pressed ends
// it ReportStopped. A list complete with the bot connected comes back at
// once. Otherwise the action pauses: it reads the list again every
// FoxholePausePoll until it is complete with the bot connected, while Stop
// still ends it, and once FoxholePauseLimit passes it ends it
// ReportMemberListGone.
func (r *FoxholeRuntime) awaitList(stop <-chan struct{}) (MemberListSnapshot, ReportOutcome) {
	select {
	case <-stop:
		return MemberListSnapshot{}, ReportStopped
	default:
	}
	list := r.guild.MemberList(r.guildID)
	if listReady(list) {
		return list, ""
	}
	r.setPaused(true)
	defer r.setPaused(false)
	for expired := r.after(FoxholePauseLimit); ; {
		poll := r.after(FoxholePausePoll)
		select {
		case <-stop:
			return list, ReportStopped
		case <-expired:
			return list, ReportMemberListGone
		case <-poll:
		}
		if list = r.guild.MemberList(r.guildID); listReady(list) {
			return list, ""
		}
	}
}

// after is a channel that closes once d has passed on the runtime's clock.
// A timer whose channel nobody waits on any more runs out with nothing to
// wake.
func (r *FoxholeRuntime) after(d time.Duration) <-chan struct{} {
	done := make(chan struct{})
	r.afterFunc(d, func() { close(done) })
	return done
}

// listReady reports whether an action can check a member against the list:
// it is complete, and the bot's connection is up, so it is current.
func listReady(list MemberListSnapshot) bool {
	return list.Status == MemberListComplete && list.Connected
}

// failureReason is why Discord refused a member's role change, in plain
// words a manager can act on. It never carries Discord's raw answer.
func failureReason(err error) string {
	class := classifyDiscordError(err)
	switch {
	case class.MissingPermissions:
		return "Discord refused: the bot needs Manage Roles, and its own role above the Foxhole role"
	case class.NotFound:
		return "Discord has no such member in the server"
	case class.ConfigFault:
		return "Discord doesn't know the role or the server, so the role may have been deleted"
	case class.SystemFault:
		return class.UserDetail
	}
	return "Discord rejected the change"
}

// writeReport writes the report as it stands, ending it when end is set.
// The end write is tried foxholeEndAttempts times. A write that fails for
// good reaches Sentry and the action goes on: the role changes matter
// more, and the next write carries everything.
func (r *FoxholeRuntime) writeReport(reportID int64, report ActionReport, end bool) {
	raw, err := json.Marshal(report)
	if err != nil {
		captureError("Foxhole report write failed", err, "report_id", reportID, "end", end)
		return
	}
	write, attempts := r.store.UpdateFoxholeReport, 1
	if end {
		write, attempts = r.store.EndFoxholeReport, foxholeEndAttempts
	}
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), foxholeStoreTimeout)
		err = write(ctx, reportID, raw)
		cancel()
		if err == nil {
			return
		}
		if attempt == attempts {
			captureError("Foxhole report write failed", err, "report_id", reportID, "end", end, "attempts", attempts)
			return
		}
		utils.Warn("Foxhole report write failed, retrying", "report_id", reportID, "attempt", attempt, "error", err)
	}
}
