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
	// Reason is why a skipped member was skipped, one of the Skip codes, or
	// why Discord refused a failed member's change, in plain words.
	Reason string `json:"reason,omitempty"`
}

// Why a Foxhole action skipped a member it reached: they no longer held
// the role it was taking, or they had left the server.
const (
	SkipNotHolding = "not-holding"
	SkipLeft       = "left"
)

// DisplayName is the name a member shows in the server: their server
// nickname, else their global name, else their username.
func (m ListedMember) DisplayName() string {
	return cmp.Or(m.Nick, m.GlobalName, m.Username)
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

	mu sync.Mutex
	// running is the action running, nil while none runs.
	running *FoxholeRunning
}

// FoxholeRunning is a Foxhole action that is running: what it is, and the
// forum user who started it.
type FoxholeRunning struct {
	Action store.ChangeAction
	By     ForumUser
}

// Running reports the Foxhole action running, if one is.
func (r *FoxholeRuntime) Running() (FoxholeRunning, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running == nil {
		return FoxholeRunning{}, false
	}
	return *r.running, true
}

// claim takes the one-action-at-a-time rule for an action, and reports
// false when another action holds it.
func (r *FoxholeRuntime) claim(action FoxholeRunning) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running != nil {
		return false
	}
	r.running = &action
	return true
}

// release gives the rule back.
func (r *FoxholeRuntime) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = nil
}

// NewFoxholeRuntime builds the runtime over the guild's gateway state, the
// role calls and the bot's store.
func NewFoxholeRuntime(guild FoxholeGuildReader, roles FoxholeRoleWriter, st store.Store, guildID string) *FoxholeRuntime {
	return &FoxholeRuntime{guild: guild, roles: roles, store: st, guildID: guildID}
}

// planned is one role change an action sets out to make: the member as
// the member list showed them when it started, and the role's ID.
type planned struct {
	member ReportMember
	roleID string
}

// Purge starts a purge of the scope given, started by the forum user
// given, and returns once its report is written. It starts only while no
// other Foxhole action runs, else ErrActionRunning, and only with a
// complete member list, else ErrMemberListPartial. The purge then runs in
// the background, with no deadline of ctx's, to its end: one member at a
// time, External holders first, it takes the role off each holder the
// member list showed at the start. It never deletes or recreates a role,
// so role IDs and channel overwrites stay.
func (r *FoxholeRuntime) Purge(ctx context.Context, scope PurgeScope, by ForumUser) (err error) {
	if !r.claim(FoxholeRunning{Action: store.ChangePurge, By: by}) {
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
	plan := purgePlan(list, FoxholeRoleIDs(r.guild.GuildData(r.guildID)), scope)
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
	go r.run(entry.ID, plan, report, names, "Panel: Foxhole purge by "+by.auditName())
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
		utils.CaptureError("Foxhole action name refresh failed", err, "member_id", mem.ID)
		return
	}
	names[mem.ID] = now
}

// purgePlan is a purge's role changes over the member list as it stands:
// for each role the scope names, in order, every member holding it, in
// display name order.
func purgePlan(list MemberListSnapshot, roleIDs map[FoxholeRole]string, scope PurgeScope) []planned {
	var plan []planned
	for _, role := range scope.Roles() {
		id := roleIDs[role]
		if id == "" {
			continue
		}
		var holders []planned
		for _, m := range list.Members {
			if slices.Contains(m.RoleIDs, id) {
				holders = append(holders, planned{
					member: ReportMember{ID: m.ID, DisplayName: m.DisplayName(), Username: m.Username, Role: role},
					roleID: id,
				})
			}
		}
		slices.SortFunc(holders, func(a, b planned) int {
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
func (r *FoxholeRuntime) run(reportID int64, plan []planned, report ActionReport, names map[string]store.MemberNames, reason string) {
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
		mem, ok := memberIn(list, member.ID)
		if ok {
			r.refreshNames(names, mem)
		}
		switch {
		case !ok:
			member.Reason = SkipLeft
			report.Skipped = append(report.Skipped, member)
		case !slices.Contains(mem.RoleIDs, p.roleID):
			member.DisplayName, member.Username, member.Reason = mem.DisplayName(), mem.Username, SkipNotHolding
			report.Skipped = append(report.Skipped, member)
		default:
			member.DisplayName, member.Username = mem.DisplayName(), mem.Username
			if err := r.roles.GuildMemberRoleRemove(r.guildID, member.ID, p.roleID, reason); err != nil {
				if classifyDiscordError(err).SystemFault {
					faults.recordSystemFault(err, member.ID)
				}
				member.Reason = failureReason(err)
				report.Failed = append(report.Failed, member)
			} else {
				report.Changed = append(report.Changed, member)
			}
		}
		report.NotAttempted = report.NotAttempted[1:]
		r.writeReport(reportID, report, false)
	}
	report.Outcome, report.EndedAt = outcome, foxholeNow().UTC()
	// The action's last Discord change is made, so it gives the rule back
	// before it writes its end: whoever sees the report ended can start the
	// next.
	r.release()
	r.writeReport(reportID, report, true)
	faults.flush("Foxhole action role change failed", "action", store.ChangePurge, "guild", r.guildID)
	utils.Info("Foxhole action ended", "action", store.ChangePurge, "outcome", report.Outcome, "changed", len(report.Changed),
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
		return "the role is gone from Discord"
	case class.SystemFault:
		return class.UserDetail
	}
	return "Discord rejected the change"
}

// memberIn finds a member in the member list.
func memberIn(list MemberListSnapshot, id string) (ListedMember, bool) {
	for _, m := range list.Members {
		if m.ID == id {
			return m, true
		}
	}
	return ListedMember{}, false
}

// writeReport writes the report as it stands, ending it when end is set. A
// write that fails reaches Sentry and the action goes on: the role changes
// matter more, and the next write carries everything.
func (r *FoxholeRuntime) writeReport(reportID int64, report ActionReport, end bool) {
	raw, err := json.Marshal(report)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), foxholeStoreTimeout)
		defer cancel()
		if end {
			err = r.store.EndFoxholeReport(ctx, reportID, raw)
		} else {
			err = r.store.UpdateFoxholeReport(ctx, reportID, raw)
		}
	}
	if err != nil {
		utils.CaptureError("Foxhole report write failed", err, "report_id", reportID, "end", end)
	}
}
