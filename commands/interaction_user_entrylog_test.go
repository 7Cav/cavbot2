package commands

import (
	"errors"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// errStub forces InteractionRespond to fail so each command short-circuits via
// HandleError right after its entry log, keeping the member-less entry-log test
// free of any real Discord/network I/O.
var errStub = errors.New("stub respond error")

// dmShapedInteraction builds a member-less ("DM-shaped") slash-command
// interaction: Discord leaves Member nil for a DM/forwarded interaction and
// populates interaction.User instead. The migrated entry logs must read the
// invoking user through interactionUser/interactionUsernameAndID so this shape
// does not panic the way a raw interaction.Member.User.Username deref would.
func dmShapedInteraction(opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	i := fakeAppCommandInteraction(opts...)
	i.Member = nil
	i.User = &discordgo.User{ID: "dm-555", Username: "dmuser"}
	return i
}

// memberlessNoMember additionally nils interaction.User, the fully-malformed
// case where neither identity source is present. The helper returns empty
// identity fields; the entry log must still not panic.
func memberlessNoMember(opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	i := dmShapedInteraction(opts...)
	i.User = nil
	return i
}

// TestEntryLogsSurviveMemberlessInteraction drives a representative subset of
// the migrated commands with a member-less interaction and asserts the entry
// log does not panic. Each command's entry log is its first identity read; to
// keep the test free of network/Discord I/O it injects a RespondErr so the
// command short-circuits via HandleError immediately after the entry log,
// proving only that the entry-log identity read survived the nil Member.
func TestEntryLogsSurviveMemberlessInteraction(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, i *discordgo.InteractionCreate)
	}{
		{
			name: "milpac",
			run: func(t *testing.T, i *discordgo.InteractionCreate) {
				f := &fakeResponder{RespondErrs: []error{errStub}}
				runMilpac(f, i)
			},
		},
		{
			name: "afsm",
			run: func(t *testing.T, i *discordgo.InteractionCreate) {
				f := &fakeResponder{RespondErrs: []error{errStub}}
				runAFSM(f, i)
			},
		},
		{
			name: "s3aar",
			run: func(t *testing.T, i *discordgo.InteractionCreate) {
				f := &fakeResponder{RespondErrs: []error{errStub}}
				runS3aar(f, i)
			},
		},
		{
			name: "s6_trackers",
			run: func(t *testing.T, i *discordgo.InteractionCreate) {
				f := &fakeResponder{RespondErrs: []error{errStub}}
				runS6ITCheck(f, i)
			},
		},
		{
			name: "loa",
			run: func(t *testing.T, i *discordgo.InteractionCreate) {
				f := &fakeResponder{RespondErrs: []error{errStub}}
				runLoa(f, healthyView(nil), time.Now(), i)
			},
		},
		{
			name: "awol",
			run: func(t *testing.T, i *discordgo.InteractionCreate) {
				f := &fakeResponder{RespondErrs: []error{errStub}}
				runAwol(f, healthyCache(nil), time.Now(), i)
			},
		},
	}

	// A single string option satisfies the Options[0] reads in loa/awol/afsm and
	// the option map in s3aar/milpac without exercising any real lookup, because
	// the injected RespondErr forces an early return.
	opt := stringOption("position", "S6")

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s entry log panicked on member-less interaction: %v", tc.name, r)
				}
			}()
			tc.run(t, dmShapedInteraction(opt))
		})
		t.Run(tc.name+"_no_user", func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s entry log panicked on nil Member+User: %v", tc.name, r)
				}
			}()
			tc.run(t, memberlessNoMember(opt))
		})
	}
}
