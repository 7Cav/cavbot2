package commands

import (
	"fmt"
	"strings"
)

// positionFormatExamples mirrors the *Position* entry in CONTEXT.md.
var positionFormatExamples = []string{"2/B/1-7", "Reservist", "S1"}

// emptyRosterSearchMessage builds the user-facing reply for /awol and /loa when
// GetRosterByFuzzyPositionSearch returns zero profiles. The upstream API
// returns HTTP 200 {"profiles":{}} for both legitimately-empty positions and
// unparseable input, so the message has to cover both causes — there is no
// signal to distinguish them.
func emptyRosterSearchMessage(position string) string {
	return fmt.Sprintf(
		"❌ No troopers found for \"%s\". The position may have no current members, or the input format may not be recognized. Examples of valid formats: `%s`.",
		position,
		strings.Join(positionFormatExamples, "`, `"),
	)
}
