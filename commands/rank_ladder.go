package commands

import "github.com/7cav/cavbot2/store"

// The rank ladder and its rank roles (GLOSSARY.md, #680). The ranks and
// their order live in code, tempVCRankRoles. The role that counts as each
// rank is a panel admin's to set on the Settings page, and a guild with
// nothing saved uses the role IDs in code. Everything that asks whether a
// member is a Cav member, or how senior, reads one RankLadder: the handover
// rule, /record start and the panel's Foxhole page.

// Rank is one rank on the rank ladder and its rank role, by ID, empty when
// the rank has none.
type Rank struct {
	Abbrev string
	RoleID string
}

// RankLadder is the rank ladder with the rank role each rank has, most
// senior first. It never changes once built: a save builds another, so a
// reader may keep one and share it.
type RankLadder struct {
	ranks []Rank
	// position maps each rank role's ID to its rank's place on the ladder.
	position map[string]int
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
	return newRankLadder(func(rr rankRole) string { return rr.roleID })
}

// NewRankLadder is the ladder with the rank roles given, keyed by rank
// abbreviation. A rank the map leaves out, or maps to empty, has none.
func NewRankLadder(roles map[string]string) RankLadder {
	return newRankLadder(func(rr rankRole) string { return roles[rr.abbrev] })
}

// RankLadderOf is the ladder a guild's stored rank roles give: the role IDs
// in code while nothing is saved, at version 0, and the saved roles from
// the first save on, so the code's IDs never apply to that guild again.
func RankLadderOf(saved store.RankRoles) RankLadder {
	if saved.Version == 0 {
		return DefaultRankLadder()
	}
	return NewRankLadder(saved.Roles)
}

// seniority is the ladder place of the most senior rank role among the role
// IDs, or noRankIndex when none of them is a rank role.
func (l RankLadder) seniority(roleIDs []string) int {
	best := noRankIndex
	for _, id := range roleIDs {
		if i, ok := l.position[id]; ok && i < best {
			best = i
		}
	}
	return best
}

// SeniorRankRole returns the most senior rank role among the role IDs, and
// false when none of them is a rank role: the member is no Cav member.
func (l RankLadder) SeniorRankRole(roleIDs []string) (string, bool) {
	i := l.seniority(roleIDs)
	if i == noRankIndex {
		return "", false
	}
	return l.ranks[i].RoleID, true
}
