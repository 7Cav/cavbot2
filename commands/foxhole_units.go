package commands

import (
	"context"
	"slices"
	"time"

	"github.com/7cav/cavbot2/utils"
)

// rosterFetchTimeout bounds a unit's roster fetch. The roster add command's
// per-trooper role-adds are plain Discord calls inside the interaction's
// 15-minute window, so only the roster lookup needs a deadline. A caller's
// shorter deadline, such as a panel page's time budget, still wins.
const rosterFetchTimeout = 30 * time.Second

// ValidatedInternalUnit is one row of the validated internal unit registry,
// which the /foxhole-bulkadd-internal picker and the Foxhole page's Add a
// unit's roster block both offer. Value is what a pick emits and what
// fingerprints captures; Label is shown in the picker and the summary;
// query is the author-controlled milpac position-group search verified to
// isolate exactly that unit's roster.
//
// The registry is both the extension seam and the safety boundary (ADR 0009):
// adding a unit later is one new row with no logic change, and the operator can
// only ever emit a value the registry already contains. query stays
// unexported, so Roster is the only way a unit's search reaches the API.
type ValidatedInternalUnit struct {
	Value string
	Label string
	query string
}

// validatedInternalUnits is the unit registry. Each query is verified to
// substring-match only its own position group's titles before being added here.
// D/ACD is the one validated internal unit today; the next is one more row.
var validatedInternalUnits = []ValidatedInternalUnit{
	{Value: "D/ACD", Label: "D/ACD", query: "D/ACD"},
}

// ValidatedInternalUnits is the registry's units, in registry order.
func ValidatedInternalUnits() []ValidatedInternalUnit {
	return slices.Clone(validatedInternalUnits)
}

// LookupValidatedInternalUnit resolves a picker value to its registry row. The
// boolean is the safety check: an unregistered value never resolves, so no query
// outside the registry can ever reach the milpac API.
func LookupValidatedInternalUnit(value string) (ValidatedInternalUnit, bool) {
	for _, unit := range validatedInternalUnits {
		if unit.Value == value {
			return unit, true
		}
	}
	return ValidatedInternalUnit{}, false
}

// Roster fetches the unit's roster through the milpac position-group
// search its registry row authorizes, under rosterFetchTimeout.
func (u ValidatedInternalUnit) Roster(ctx context.Context) (*utils.LiteRosterResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, rosterFetchTimeout)
	defer cancel()
	return utils.GetRosterByFuzzyPositionSearch(ctx, u.query)
}
