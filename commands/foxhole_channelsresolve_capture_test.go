package commands

import (
	"slices"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// --- runFoxholePurge: GuildChannels fault capture split (#195) ---

// purgeChannelsGM builds a guild manager whose Foxhole role resolves cleanly so
// the purge reaches the GuildChannels lookup, then fails that lookup with err.
func purgeChannelsGM(err error) *fakeGuildManager {
	return &fakeGuildManager{
		roles:        []*discordgo.Role{guildRole("old-int", foxholeRoleBaseNameDefault+" Internal")},
		ChannelsErrs: []error{err},
	}
}

// A purge whose channel lookup Discord fails recreates nothing, and its reply
// gives the advice for that kind of failure without Discord's raw body. A
// system fault pages Sentry once, naming the command and the guild (#195); a
// client fault pages nothing.
func TestRunFoxholePurge_ChannelLookupFailureAdvisesAndRecreatesNothing(t *testing.T) {
	for _, failure := range []discordFailure{serverError, transport, unknownGuild, badRequest} {
		t.Run(failure.name, func(t *testing.T) {
			noOverwriteDelay(t)
			rec := &captureRecorder{}
			rec.install(t)
			gm := purgeChannelsGM(failure.err)
			f := &fakeResponder{}

			runFoxholePurge(f, gm, foxholeInteraction("guild-1"), "guild-1", "internal")

			if gm.countCalls("GuildRoleCreate") != 0 {
				t.Fatalf("must not recreate roles when channels are inaccessible; got %v", gm.Calls())
			}
			reply := lastEditContent(f.Calls())
			if got := verdicts(reply, ""); !slices.Equal(got, []string{verdictFailed}) {
				t.Errorf("the reply's verdicts are %q, want one %q", got, verdictFailed)
			}
			assertFailureReply(t, reply, failure, rec)
			if !failure.captured {
				return
			}
			for key, want := range map[string]string{"command": "foxhole", "guild": "guild-1"} {
				if got, ok := kvValue(rec.lastKV, key); !ok || got != want {
					t.Errorf("capture context %q = %v, want %q; kv %v", key, got, want, rec.lastKV)
				}
			}
		})
	}
}
