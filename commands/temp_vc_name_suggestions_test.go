package commands

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// Name suggestions on the ownership notice (#959). Billet titles are the
// forms the live milpacs carry.

func TestSuggestChannelNames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		billets []string
		scope   string
		want    []string
	}{
		{"a company HQ billet adds a knock HQ",
			[]string{"Commander A/3-7"}, "A/3-7 Voice ▬ A/3-7 ▬",
			[]string{"3-7 Lounge", "A/3-7 Lounge", "🚦 A/3-7 HQ"}},
		{"a battalion HQ billet names the battalion first",
			[]string{"3-7 Executive Officer"}, "3-7 Voice",
			[]string{"3-7 Lounge", "🚦 3-7 HQ"}},
		{"a section leader gets a lounge per level and SP",
			[]string{"Section Leader 1/1/A/3-7"}, "3-7 Voice",
			[]string{"3-7 Lounge", "A/3-7 Lounge", "1/A/3-7 Lounge", "1/1/A/3-7 Lounge", "1/1/A/3-7 SP"}},
		{"an assistant section leader gets SP, on a letter battalion",
			[]string{"Assistant Section Leader 1/2/D/DEVCOM"}, "DEVCOM Voice",
			[]string{"DEVCOM Lounge", "D/DEVCOM Lounge", "2/D/DEVCOM Lounge", "1/2/D/DEVCOM Lounge", "1/2/D/DEVCOM SP"}},
		{"a platoon leader gets lounges only",
			[]string{"Platoon Leader 1/A/1-7"}, "A/1-7 Voice",
			[]string{"1-7 Lounge", "A/1-7 Lounge", "1/A/1-7 Lounge"}},
		{"a billet related to the hub comes before one that is not",
			[]string{"S6 Game Staff", "Trooper 1/2/A/1-7"}, "Voice ▬ A/1-7 ▬",
			[]string{"1-7 Lounge", "A/1-7 Lounge", "2/A/1-7 Lounge", "1/2/A/1-7 Lounge", "S6 Lounge"}},
		{"a department hub puts staff billets first",
			[]string{"Trooper 1/2/A/1-7", "S6 Game Staff"}, "Department Channel [member] Departmental VC",
			[]string{"S6 Lounge", "1-7 Lounge", "A/1-7 Lounge", "2/A/1-7 Lounge", "1/2/A/1-7 Lounge"}},
		{"an HQ hub puts the knock HQ first",
			[]string{"S3 Squad Operations Staff", "3-7 Executive Officer"}, "🚦HQ ▬▬▬ Regimental HQ ▬▬▬",
			[]string{"🚦 3-7 HQ", "S3 Lounge", "3-7 Lounge"}},
		{"a staff word matches a whole word of the scope only",
			[]string{"MP Administrator IT", "Trooper 1/A/3-7"}, "A/3-7 Company",
			[]string{"3-7 Lounge", "A/3-7 Lounge", "1/A/3-7 Lounge", "MP Lounge"}},
		{"a name two billets share shows once, and one row at most",
			[]string{"Commander A/3-7", "Trooper 1/1/B/3-7"}, "3-7 Voice",
			[]string{"3-7 Lounge", "A/3-7 Lounge", "🚦 A/3-7 HQ", "B/3-7 Lounge", "1/B/3-7 Lounge"}},
		{"a billet with no unit and no department word gives nothing",
			[]string{"Reservist", "Regimental Executive Officer", "Aide to the XO", ""}, "Voice",
			[]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := suggestChannelNames(tc.billets, tc.scope); !slices.Equal(got, tc.want) {
				t.Errorf("suggestChannelNames(%q, %q) = %q, want %q", tc.billets, tc.scope, got, tc.want)
			}
		})
	}
}

// stubMilpacs answers the runtime's billet lookup from titles by Discord ID,
// with ErrNotFound for anyone else, or with err for everyone when set. It
// returns the IDs looked up.
func stubMilpacs(tv *TempVC, billets map[string][]string, err error) *[]string {
	var asked []string
	tv.milpacByDiscordID = func(_ context.Context, discordID string) (*utils.ProfileResponse, error) {
		asked = append(asked, discordID)
		if err != nil {
			return nil, err
		}
		titles, ok := billets[discordID]
		if !ok {
			return nil, utils.ErrNotFound
		}
		p := &utils.ProfileResponse{Primary: utils.Position{PositionTitle: titles[0]}}
		for _, title := range titles[1:] {
			p.Secondary = append(p.Secondary, utils.Position{PositionTitle: title})
		}
		return p, nil
	}
	return &asked
}

// suggestionLabels lists a notice's suggestion buttons by label, checking
// each CustomID carries its own label.
func suggestionLabels(t *testing.T, msg fakeMessage) []string {
	t.Helper()
	labels := []string{}
	for _, b := range buttonsOf(msg.data.Components) {
		if _, name, ok := parseRenameSuggestionCustomID(b.CustomID); ok {
			if name != b.Label {
				t.Errorf("button %q renames to %q, want its label", b.Label, name)
			}
			labels = append(labels, b.Label)
		}
	}
	return labels
}

// The notice offers the owner's names, scoped by the hub's category, at
// create and at a handover. Nothing is offered on a hub with renaming off,
// or when the owner's billets cannot be read.
func TestTempVCOwnershipNoticeSuggestsNamesFromTheOwnersBillets(t *testing.T) {
	billets := map[string][]string{
		"sgt-1": {"S6 Game Staff", "Commander A/3-7"},
		"sgt-2": {"Trooper 1/A/3-7"},
	}
	for _, tc := range []struct {
		name         string
		hub          store.Hub
		lookupErr    error
		wantCreate   []string
		wantHandover []string
		wantLookups  bool
	}{
		{"names from the billets", testHub(), nil,
			[]string{"3-7 Lounge", "A/3-7 Lounge", "🚦 A/3-7 HQ", "S6 Lounge"},
			[]string{"3-7 Lounge", "A/3-7 Lounge", "1/A/3-7 Lounge"}, true},
		{"a failed lookup", testHub(), errors.New("milpacs API returned 502 Bad Gateway"),
			[]string{}, []string{}, true},
		{"renaming off", renamingOffHub(), nil, []string{}, []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTempVCManager()
			fake.channels[testTempVCCategory] = &discordgo.Channel{ID: testTempVCCategory, Name: "▬ A/3-7 ▬", Type: discordgo.ChannelTypeGuildCategory}
			fake.channels["chan-a"] = &discordgo.Channel{ID: "chan-a", ParentID: testTempVCCategory, Type: discordgo.ChannelTypeGuildVoice}
			tv := newTestTempVC(t, fake, seedStore(t, tc.hub))
			asked := stubMilpacs(tv, billets, tc.lookupErr)

			spawnInto(tv, fake, "sgt-1", "chan-a", member("Sgt", testRankSGT))
			fake.deliver(tv, voiceEvent("sgt-2", "chan-a", member("Sgt", testRankSGT)))
			fake.deliver(tv, voiceEvent("sgt-1", "", member("Sgt", testRankSGT)))

			notices := noticesIn(t, fake, "chan-a")
			if len(notices) != 2 {
				t.Fatalf("notices in chan-a = %d, want the create and the handover", len(notices))
			}
			if got := suggestionLabels(t, notices[0]); !slices.Equal(got, tc.wantCreate) {
				t.Errorf("create notice offers %q, want %q", got, tc.wantCreate)
			}
			if got := suggestionLabels(t, notices[1]); !slices.Equal(got, tc.wantHandover) {
				t.Errorf("handover notice offers %q, want %q", got, tc.wantHandover)
			}
			if looked := len(*asked) > 0; looked != tc.wantLookups {
				t.Errorf("billet lookups %q, want any: %v", *asked, tc.wantLookups)
			}
		})
	}
}

// A press renames the channel to the button's name for the owner and for a
// hub moderator outside the channel, and for nobody else. A press on a
// channel that is gone changes nothing.
func TestRenameSuggestionAnswersToTheOwnerAndModerators(t *testing.T) {
	for _, tc := range []struct {
		name    string
		presser permMember
		gone    bool
		renames bool
	}{
		{"the owner", lockOwner, false, true},
		{"a hub moderator outside the channel", permMOD, false, true},
		{"a member inside who is neither", permG, false, false},
		{"the owner, once the channel is gone", lockOwner, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeTempVCManager()
			installPermFixture(fake)
			tv := newTestTempVC(t, fake, seedStore(t, permFixtureHub(store.PermissionCategory)))
			stubMilpacs(tv, map[string][]string{lockOwner.id: {"Commander A/3-7"}}, nil)
			spawnInto(tv, fake, lockOwner.id, "chan-1", lockOwner.discordMember())
			enter(tv, fake, permG, "chan-1")

			notices := noticesIn(t, fake, "chan-1")
			if len(notices) != 1 {
				t.Fatalf("notices in chan-1 = %d, want the create notice", len(notices))
			}
			var button string
			for _, b := range buttonsOf(notices[0].data.Components) {
				if b.Label == "A/3-7 Lounge" {
					button = b.CustomID
				}
			}
			if button == "" {
				t.Fatalf("notice carries no A/3-7 Lounge button: %+v", notices[0].data.Components)
			}
			if tc.gone {
				tv.handleChannelDelete(&discordgo.ChannelDelete{Channel: &discordgo.Channel{ID: "chan-1", GuildID: testTempVCGuild}})
			}

			press(t, tv, button, tc.presser)

			var names []string
			for _, e := range fake.recordedEdits() {
				if e.channelID == "chan-1" && e.name != "" {
					names = append(names, e.name)
				}
			}
			if want := []string{"A/3-7 Lounge"}; tc.renames && !slices.Equal(names, want) {
				t.Errorf("renames of chan-1 = %q, want %q", names, want)
			}
			if !tc.renames && len(names) != 0 {
				t.Errorf("renames of chan-1 = %q, want none", names)
			}
		})
	}
}
