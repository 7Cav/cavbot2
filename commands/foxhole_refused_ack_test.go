package commands

import (
	"net/http"
	"testing"
)

// When Discord refuses a /foxhole bulkadd's or purge's acknowledgement with
// anything but 10062 Unknown interaction, the run stops at the one reply
// that tells the member it couldn't start: no guild read, no role change,
// and nothing left running, so the Foxhole page isn't kept busy by a
// command its member was told never started.
func TestRefusedFoxholeAcknowledgementStopsTheRun(t *testing.T) {
	for _, subcommand := range []string{"bulkadd", "purge"} {
		t.Run(subcommand, func(t *testing.T) {
			fx, _ := newPageRuntime(t)
			gm := trooperGuild()
			f := &fakeResponder{RespondErrs: []error{restError(http.StatusInternalServerError, 0, rawBodyMarker)}}

			runFoxhole(f, gm, fx, slashNamed("foxhole", stringOption("command", subcommand),
				stringOption("flag", "internal"), stringOption("discordname", "good")))

			// A purge that carried on would hold the one-action-at-a-time rule
			// until its background run had made its last call, so once nothing
			// is busy the calls below are all the run will make.
			if fx.Busy() {
				t.Errorf("the Foxhole page is busy after the refused /foxhole %s returned", subcommand)
			}
			if calls := gm.Calls(); len(calls) != 0 {
				t.Errorf("guild calls = %v, want none", calls)
			}
			if calls := f.Calls(); len(calls) != 2 || calls[1].Method != "Respond" {
				t.Errorf("responder calls = %+v, want the refused deferral, then one reply", calls)
			}
		})
	}
}
