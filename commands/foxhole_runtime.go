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

// foxholeNow is the Foxhole runtime's clock, a package var beside
// tempVCNow.
var foxholeNow = time.Now

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
// member-role rate, about one a second.
type FoxholeRoleWriter interface {
	GuildMemberRoleRemove(guildID, userID, roleID, auditReason string) error
}

// ForumUser is the forum user who started a Foxhole action on the page.
type ForumUser struct {
	ID       int
	Username string
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
)

// ActionReport is a Foxhole action's report as its change log entry's diff
// holds it. The entry itself holds who started the action and when.
type ActionReport struct {
	// Scope is a purge's scope.
	Scope PurgeScope `json:"scope,omitempty"`
	// Outcome is how the action ended, empty while it runs.
	Outcome ReportOutcome `json:"outcome,omitempty"`
	// EndedAt is when the action ended, zero while it runs.
	EndedAt time.Time `json:"ended_at,omitzero"`
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
}

// ReportMember is one member a report names, under the names the member
// list showed when the action reached them, with the role the action
// changed or meant to.
type ReportMember struct {
	ID          string      `json:"id"`
	DisplayName string      `json:"display_name"`
	Username    string      `json:"username"`
	Role        FoxholeRole `json:"role"`
	// Skip is why a skipped member was skipped.
	Skip SkipReason `json:"skip,omitempty"`
	// Failure is why Discord refused a failed member's change, in plain
	// words.
	Failure string `json:"failure,omitempty"`
}

// SkipReason is why a Foxhole action skipped a member it reached.
type SkipReason string

const (
	// SkipNotHolding is a member who no longer held the role the action was
	// taking.
	SkipNotHolding SkipReason = "not-holding"
	// SkipLeft is a member who had left the server.
	SkipLeft SkipReason = "left"
)

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

	mu sync.Mutex
	// busy is an action running, which holds the one-action-at-a-time
	// rule.
	busy bool
}

// Busy reports whether a Foxhole action is running.
func (r *FoxholeRuntime) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

// claim takes the one-action-at-a-time rule, and reports false when an
// action already holds it.
func (r *FoxholeRuntime) claim() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy {
		return false
	}
	r.busy = true
	return true
}

// release gives the rule back.
func (r *FoxholeRuntime) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.busy = false
}

// NewFoxholeRuntime builds the runtime over the guild's gateway state, the
// role calls and the bot's store.
func NewFoxholeRuntime(guild FoxholeGuildReader, roles FoxholeRoleWriter, st store.Store, guildID string) *FoxholeRuntime {
	return &FoxholeRuntime{guild: guild, roles: roles, store: st, guildID: guildID}
}

// plannedChange is one role change an action sets out to make: the member
// as the member list showed them when it started, and the role's ID.
type plannedChange struct {
	member ReportMember
	roleID string
}

// actionRun is what an action's run carries besides its plan: the action,
// its report's ID, and the audit log reason each of its changes sends.
type actionRun struct {
	action   store.ChangeAction
	reportID int64
	reason   string
}

// Purge starts a purge of the scope given, started by the forum user
// given, and returns once its report is written. It starts only while no
// other Foxhole action runs, else ErrActionRunning, only with a complete
// member list, else ErrMemberListPartial, and only when the guild holds
// each role it names, else a *MissingRoleError. The purge then runs in
// the background, with no deadline of ctx's, to its end: one member at a
// time, External holders first, it takes the role off each holder the
// member list showed at the start. It never deletes or recreates a role,
// so role IDs and channel overwrites stay.
func (r *FoxholeRuntime) Purge(ctx context.Context, scope PurgeScope, by ForumUser) (err error) {
	if !r.claim() {
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
	if err := missingRole(roleIDs, scope); err != nil {
		captureError("Foxhole role not found for a purge", err, "role", err.Role, "scope", scope)
		return err
	}
	plan := purgePlan(list, roleIDs, scope)
	report := ActionReport{Scope: scope, Changed: []ReportMember{}, Skipped: []ReportMember{}, Failed: []ReportMember{},
		NotAttempted: make([]ReportMember, 0, len(plan))}
	for _, p := range plan {
		report.NotAttempted = append(report.NotAttempted, p.member)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode the purge's report: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, foxholeStoreTimeout)
	defer cancel()
	names, err := r.recordNames(ctx)
	if err != nil {
		return err
	}
	entry, err := r.store.StartFoxholeReport(ctx, store.ChangeLogEntry{
		ForumUserID: by.ID, ForumUsername: by.Username, Action: store.ChangePurge, Diff: raw,
	})
	if err != nil {
		return fmt.Errorf("start the purge's report: %w", err)
	}
	utils.Info("Foxhole action started", "action", store.ChangePurge, "scope", scope, "members", len(plan),
		"username", by.Username, "forum_user_id", by.ID)
	run := actionRun{action: store.ChangePurge, reportID: entry.ID, reason: "Panel: Foxhole purge by " + by.auditName()}
	go r.run(run, plan, report, names)
	return nil
}

// recordNames reads the last-seen names of each member with a Foxhole
// record, by member ID.
func (r *FoxholeRuntime) recordNames(ctx context.Context) (map[string]store.MemberNames, error) {
	records, err := r.store.ListFoxholeRecords(ctx, r.guildID)
	if err != nil {
		return nil, fmt.Errorf("list Foxhole records: %w", err)
	}
	names := make(map[string]store.MemberNames, len(records))
	for _, rec := range records {
		names[rec.MemberID] = store.MemberNames{MemberID: rec.MemberID, DisplayName: rec.DisplayName, Username: rec.Username}
	}
	return names, nil
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

// missingRole is the first role the scope names that the guild lacks, nil
// when it holds them all.
func missingRole(roleIDs map[FoxholeRole]string, scope PurgeScope) *MissingRoleError {
	internalName, externalName := FoxholeRoleNames()
	names := map[FoxholeRole]string{FoxholeInternal: internalName, FoxholeExternal: externalName}
	for _, role := range scope.Roles() {
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
		slices.SortFunc(holders, func(a, b plannedChange) int {
			return cmp.Or(cmp.Compare(strings.ToLower(a.member.DisplayName), strings.ToLower(b.member.DisplayName)),
				cmp.Compare(a.member.ID, b.member.ID))
		})
		plan = append(plan, holders...)
	}
	return plan
}

// run makes an action's role changes one at a time, writing the report
// after each, and ends the report once it has been through them all. Just
// before it changes a member it reads them from the member list: one who
// no longer holds the role, or who left the server, is skipped with that
// reason, and one still in the server is named in the report under the
// names the list shows now, which also refresh their record's names.
func (r *FoxholeRuntime) run(run actionRun, plan []plannedChange, report ActionReport, names map[string]store.MemberNames) {
	defer utils.RecoverPanic("foxhole-action")
	// A panic gives the rule back too; release on a free rule does nothing.
	defer r.release()
	faults := newFaultCollector()
	outcome := ReportDone
	for _, p := range plan {
		member := p.member
		list := r.guild.MemberList(r.guildID)
		if list.Status != MemberListComplete {
			outcome = ReportMemberListGone
			break
		}
		mem, ok := list.Member(member.ID)
		if ok {
			r.refreshNames(names, mem)
			member.DisplayName, member.Username = mem.DisplayName(), mem.Username
		}
		switch {
		case !ok:
			member.Skip = SkipLeft
			report.Skipped = append(report.Skipped, member)
		case !slices.Contains(mem.RoleIDs, p.roleID):
			member.Skip = SkipNotHolding
			report.Skipped = append(report.Skipped, member)
		default:
			if err := r.roles.GuildMemberRoleRemove(r.guildID, member.ID, p.roleID, run.reason); err != nil {
				if classifyDiscordError(err).SystemFault {
					faults.recordSystemFault(err, member.ID)
				}
				member.Failure = failureReason(err)
				report.Failed = append(report.Failed, member)
			} else {
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
