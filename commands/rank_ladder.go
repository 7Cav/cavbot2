package commands

import (
	"errors"
	"slices"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The rank ladder and its rank roles (GLOSSARY.md, #680). The ranks and
// their order live in code, tempVCRankRoles. The role that counts as each
// rank is a panel admin's to set on the Settings page, and a guild with
// nothing saved uses the role IDs in code. Everything that asks whether a
// member is a Cav member, or how senior, reads one RankLadder. The
// handover rule, /record start and the panel's Foxhole page all do.

// errRankRoleDeleted is the identity of a deleted rank role's event in
// Sentry. Every member holding it stops counting as a Cav member, and only a
// panel admin's save of the rank roles brings the rank back (ADR 0012).
var errRankRoleDeleted = errors.New("a rank role was deleted in Discord")

// errDischargedRoleDeleted is the identity of a deleted Discharged role's
// event in Sentry, filed in an issue of its own (dischargedRoleDeletedIssue).
// Every discharged trooper counts as a Cav member again, until a panel
// admin saves another Discharged role (ADR 0012, amended by #893).
var errDischargedRoleDeleted = errors.New("the Discharged role was deleted in Discord")

// dischargedRoleDeletedIssue files a deleted Discharged role apart from a
// deleted rank role, which the same handler reports.
var dischargedRoleDeletedIssue = utils.SentryIssue{Fingerprint: []string{"discharged-role-deleted"}}

// defaultDischargedRole is the forum's Discharged role (GLOSSARY.md,
// Discharged role, #893), the one a guild has until a panel admin saves
// the Discharged role. The rank roles use the role IDs in code the same
// way.
const defaultDischargedRole = "437749895785480193"

// Rank is one rank on the rank ladder and its rank role, by ID, empty when
// the rank has none.
type Rank struct {
	Abbrev string
	RoleID string
}

// RankLadder is the rank ladder with the rank role each rank has, most
// senior first. It never changes once built. A save builds another, so a
// reader may keep one and share it.
type RankLadder struct {
	ranks []Rank
	// position maps each rank role's ID to its rank's place on the ladder.
	position map[string]int
	// discharged is the Discharged role's ID, empty for none. A member
	// holding it is no Cav member, whatever rank role they hold.
	discharged string
}

// newRankLadder builds a ladder of the code's ranks, in their order, each
// with the role roleOf gives it. A role given to two ranks counts for the
// more senior one.
func newRankLadder(roleOf func(rankRole) string) RankLadder {
	l := RankLadder{ranks: make([]Rank, len(tempVCRankRoles)), position: make(map[string]int, len(tempVCRankRoles))}
	for i, rr := range tempVCRankRoles {
		id := roleOf(rr)
		l.ranks[i] = Rank{Abbrev: rr.abbrev, RoleID: id}
		if _, taken := l.position[id]; id != "" && !taken {
			l.position[id] = i
		}
	}
	return l
}

// DefaultRankLadder is the ladder with the role IDs in code, the one a
// guild uses until a panel admin saves the rank roles.
func DefaultRankLadder() RankLadder {
	l := newRankLadder(func(rr rankRole) string { return rr.roleID })
	l.discharged = defaultDischargedRole
	return l
}

// NewRankLadder is the ladder with the rank roles given, keyed by rank
// abbreviation, and the Discharged role given, empty for none. A rank the
// map leaves out, or maps to empty, has none.
func NewRankLadder(roles map[string]string, discharged string) RankLadder {
	l := newRankLadder(func(rr rankRole) string { return roles[rr.abbrev] })
	l.discharged = discharged
	return l
}

// RankLadderOf is the ladder a guild's stored rank roles give. While
// nothing is saved, at version 0, that is the role IDs in code. From the
// first save on it is the saved roles, and the code's IDs never apply to
// that guild again. The Discharged role is the forum's until a save
// writes it, which rank roles saved before #893 never did.
func RankLadderOf(saved store.RankRoles) RankLadder {
	if saved.Version == 0 {
		return DefaultRankLadder()
	}
	discharged := defaultDischargedRole
	if saved.DischargedRoleID != nil {
		discharged = *saved.DischargedRoleID
	}
	return NewRankLadder(saved.Roles, discharged)
}

// Ranks returns the ladder's ranks, most senior first.
func (l RankLadder) Ranks() []Rank {
	return slices.Clone(l.ranks)
}

// Roles returns each rank's rank role by abbreviation, empty for a rank
// with none. A save of the ladder stores this map.
func (l RankLadder) Roles() map[string]string {
	roles := make(map[string]string, len(l.ranks))
	for _, r := range l.ranks {
		roles[r.Abbrev] = r.RoleID
	}
	return roles
}

// DischargedRole returns the Discharged role's ID, empty for none.
func (l RankLadder) DischargedRole() string {
	return l.discharged
}

// seniority is the member's place on the ladder: the place of the most
// senior rank role among the role IDs, or noRankIndex when they are no Cav
// member, holding no rank role or holding the Discharged role.
func (l RankLadder) seniority(roleIDs []string) int {
	if l.Discharged(roleIDs) {
		return noRankIndex
	}
	return l.place(roleIDs)
}

// place is the ladder place of the most senior rank role among the role
// IDs, or noRankIndex when none of them is a rank role.
func (l RankLadder) place(roleIDs []string) int {
	best := noRankIndex
	for _, id := range roleIDs {
		if i, ok := l.position[id]; ok && i < best {
			best = i
		}
	}
	return best
}

// CavMember reports whether a member holding the role IDs is a Cav member:
// they hold a rank role and not the Discharged role.
func (l RankLadder) CavMember(roleIDs []string) bool {
	return l.seniority(roleIDs) < noRankIndex
}

// Discharged reports whether the role IDs hold the Discharged role, so
// their holder is no Cav member whatever rank role they hold.
func (l RankLadder) Discharged(roleIDs []string) bool {
	return l.discharged != "" && slices.Contains(roleIDs, l.discharged)
}

// SeniorRankRole returns the most senior rank role among the role IDs, and
// false when none of them is a rank role. A discharged member keeps
// theirs, though they are no Cav member.
func (l RankLadder) SeniorRankRole(roleIDs []string) (string, bool) {
	i := l.place(roleIDs)
	if i == noRankIndex {
		return "", false
	}
	return l.ranks[i].RoleID, true
}

// Drops reports whether a member holding the role IDs counts as a Cav
// member on this ladder and as none on the ladder next, so a save from this
// ladder to next stops them counting.
func (l RankLadder) Drops(next RankLadder, roleIDs []string) bool {
	return l.CavMember(roleIDs) && !next.CavMember(roleIDs)
}

// MayDrop reports whether a save from this ladder to next may stop some
// member counting as a Cav member: a role that counts for a rank on this
// ladder counts for none on next, or next sets a Discharged role this
// ladder doesn't have.
func (l RankLadder) MayDrop(next RankLadder) bool {
	if next.discharged != "" && next.discharged != l.discharged {
		return true
	}
	for _, r := range l.ranks {
		if _, still := next.position[r.RoleID]; r.RoleID != "" && !still {
			return true
		}
	}
	return false
}

// RankOf returns the abbreviation of the rank whose rank role the role is,
// and false when it is no rank's.
func (l RankLadder) RankOf(roleID string) (string, bool) {
	i, ok := l.position[roleID]
	if !ok {
		return "", false
	}
	return l.ranks[i].Abbrev, true
}
