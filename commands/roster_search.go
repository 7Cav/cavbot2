package commands

import "fmt"

// positionFormatExamples mirrors the canonical *Position* entry in CONTEXT.md.
// Keep these in sync if CONTEXT.md ever broadens the example set.
var positionFormatExamples = []string{"2/B/1-7", "Reservist", "S1"}

// emptyRosterSearchMessage builds the user-facing reply for /awol and /loa when
// GetRosterByFuzzyPositionSearch returns zero profiles. The API returns
// HTTP 200 {"profiles":{}} for both legitimately-empty positions AND
// unparseable input (probed 2026-05-27), so the message acknowledges both
// causes rather than asserting the user got it wrong.
func emptyRosterSearchMessage(position string) string {
	return fmt.Sprintf(
		"❌ No troopers found for \"%s\". The position may have no current members, or the input format may not be recognized. Examples of valid formats: `%s`, `%s`, `%s`.",
		position,
		positionFormatExamples[0],
		positionFormatExamples[1],
		positionFormatExamples[2],
	)
}
