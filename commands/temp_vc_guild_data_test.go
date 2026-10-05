package commands

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The panel's hub page and saves read the guild's channels, roles and boost
// tier from discordgo's state cache, never Discord's API (#400). These tests
// drive the production adapter over a real session whose state is fed the
// gateway events discordgo's reader would apply.

// refusingAPI stands in for Discord's API where a read must send nothing:
// any request fails the test.
func refusingAPI(t *testing.T) *fakeDiscordAPI {
	t.Helper()
	return &fakeDiscordAPI{answer: func(r *http.Request, _ []byte) (int, []byte) {
		t.Errorf("sent %s %s to Discord's API, want no request", r.Method, r.URL.Path)
		return http.StatusInternalServerError, []byte(`{"message":"no request expected"}`)
	}}
}

// stateSession is a real session whose REST calls reach api.
func stateSession(t *testing.T, api *fakeDiscordAPI) *discordgo.Session {
	t.Helper()
	dg, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	dg.Client = &http.Client{Transport: api}
	return dg
}

// guildDataGuild is the test guild as its GUILD_CREATE carries it: a
// category with a voice channel under it, two roles, one of them managed,
// and boost tier 2.
func guildDataGuild() *discordgo.Guild {
	return &discordgo.Guild{
		ID:          testTempVCGuild,
		PremiumTier: discordgo.PremiumTier2,
		Channels: []*discordgo.Channel{
			{ID: "cat-1", GuildID: testTempVCGuild, Name: "Arma Reforger", Type: discordgo.ChannelTypeGuildCategory},
			{ID: "hub-1", GuildID: testTempVCGuild, Name: "Join to create", Type: discordgo.ChannelTypeGuildVoice, ParentID: "cat-1"},
		},
		Roles: []*discordgo.Role{
			{ID: "role-mp", Name: "Military Police", Color: 0xebc729, Position: 3},
			{ID: "role-bot", Name: "CavBot", Managed: true, Position: 1},
		},
	}
}

// channelIn returns the channel with the ID from a snapshot, or nil.
func channelIn(data GuildSnapshot, id string) *discordgo.Channel {
	for _, ch := range data.Channels {
		if ch.ID == id {
			return ch
		}
	}
	return nil
}

// roleIn returns the role with the ID from a snapshot, or nil.
func roleIn(data GuildSnapshot, id string) *discordgo.Role {
	for _, r := range data.Roles {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func TestSessionTempVCManagerGuildDataReadsTheStateCache(t *testing.T) {
	dg := stateSession(t, refusingAPI(t))
	feed(t, dg, &discordgo.GuildCreate{Guild: guildDataGuild()})

	data := NewSessionTempVCManager(dg, testTempVCGuild).GuildData(testTempVCGuild)

	if data.Status != GuildDataPresent {
		t.Fatalf("status = %v, want present", data.Status)
	}
	if len(data.Channels) != 2 {
		t.Errorf("read %d channels, want 2", len(data.Channels))
	}
	hub := channelIn(data, "hub-1")
	if hub == nil || hub.Name != "Join to create" || hub.Type != discordgo.ChannelTypeGuildVoice || hub.ParentID != "cat-1" {
		t.Errorf("hub-1 = %+v, want the voice channel Join to create under cat-1", hub)
	}
	if cat := channelIn(data, "cat-1"); cat == nil || cat.Name != "Arma Reforger" || cat.Type != discordgo.ChannelTypeGuildCategory {
		t.Errorf("cat-1 = %+v, want the category Arma Reforger", cat)
	}
	if len(data.Roles) != 2 {
		t.Errorf("read %d roles, want 2", len(data.Roles))
	}
	if mp := roleIn(data, "role-mp"); mp == nil || mp.Name != "Military Police" || mp.Color != 0xebc729 || mp.Position != 3 || mp.Managed {
		t.Errorf("role-mp = %+v, want Military Police, colour 0xebc729, position 3, not managed", mp)
	}
	if bot := roleIn(data, "role-bot"); bot == nil || !bot.Managed {
		t.Errorf("role-bot = %+v, want the managed role", bot)
	}
	if data.PremiumTier != discordgo.PremiumTier2 {
		t.Errorf("boost tier = %v, want tier 2", data.PremiumTier)
	}
}

// After a READY the cache holds a placeholder for the guild until its
// GUILD_CREATE lands: the data is on its way. An outage's GUILD_DELETE
// takes the guild out of the cache: it is absent. Each read follows the
// cache as it is now.
func TestSessionTempVCManagerGuildDataFollowsTheGuildThroughTheGateway(t *testing.T) {
	dg := stateSession(t, refusingAPI(t))
	mgr := NewSessionTempVCManager(dg, testTempVCGuild)
	feed(t, dg, readyWith(&discordgo.Guild{ID: testTempVCGuild, Unavailable: true}))

	if data := mgr.GuildData(testTempVCGuild); data.Status != GuildDataArriving {
		t.Errorf("after READY, status = %v, want on its way", data.Status)
	}

	feed(t, dg, &discordgo.GuildCreate{Guild: guildDataGuild()})

	if data := mgr.GuildData(testTempVCGuild); data.Status != GuildDataPresent || channelIn(data, "hub-1") == nil {
		t.Errorf("after GUILD_CREATE, status = %v with hub-1 %v, want present with hub-1", data.Status, channelIn(data, "hub-1"))
	}

	feed(t, dg, &discordgo.GuildDelete{Guild: &discordgo.Guild{ID: testTempVCGuild, Unavailable: true}})

	if data := mgr.GuildData(testTempVCGuild); data.Status != GuildDataAbsent {
		t.Errorf("after GUILD_DELETE, status = %v, want absent", data.Status)
	}
}

// discordgo's reader applies gateway events on its goroutine while page
// loads read on theirs, and it writes the cached channels and roles in
// place: a CHANNEL_UPDATE copies over the cached channel, and a delete
// shifts the slice. The readers here use every field of what they were
// given after the read returned, outside any lock, so -race is the
// assertion that the snapshot shares nothing with the cache.
func TestSessionTempVCManagerGuildDataRacesGatewayEventsCleanly(t *testing.T) {
	dg := stateSession(t, refusingAPI(t))
	feed(t, dg, &discordgo.GuildCreate{Guild: guildDataGuild()})
	mgr := NewSessionTempVCManager(dg, testTempVCGuild)

	events := func(i int) []any {
		spare := fmt.Sprintf("vc-%d", i)
		role := fmt.Sprintf("role-%d", i)
		return []any{
			&discordgo.ChannelUpdate{Channel: &discordgo.Channel{ID: "hub-1", GuildID: testTempVCGuild,
				Name: fmt.Sprintf("Join %d", i), Type: discordgo.ChannelTypeGuildVoice, ParentID: "cat-1"}},
			&discordgo.ChannelCreate{Channel: &discordgo.Channel{ID: spare, GuildID: testTempVCGuild,
				Name: spare, Type: discordgo.ChannelTypeGuildVoice, ParentID: "cat-1"}},
			&discordgo.GuildRoleCreate{GuildRole: &discordgo.GuildRole{GuildID: testTempVCGuild,
				Role: &discordgo.Role{ID: role, Name: role}}},
			&discordgo.GuildRoleUpdate{GuildRole: &discordgo.GuildRole{GuildID: testTempVCGuild,
				Role: &discordgo.Role{ID: "role-mp", Name: fmt.Sprintf("Military Police %d", i), Position: i}}},
			&discordgo.ChannelDelete{Channel: &discordgo.Channel{ID: spare, GuildID: testTempVCGuild, Type: discordgo.ChannelTypeGuildVoice}},
			&discordgo.GuildRoleDelete{GuildID: testTempVCGuild, RoleID: role},
		}
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Go(func() {
		defer close(done)
		// t.Fatalf may not be called off the test goroutine, so the writer
		// reports through t.Errorf.
		for i := range 200 {
			for _, e := range events(i) {
				if err := dg.State.OnInterface(dg, e); err != nil {
					t.Errorf("State.OnInterface(%T): %v", e, err)
					return
				}
			}
		}
	})
	for range 4 {
		wg.Go(func() {
			for {
				data := mgr.GuildData(testTempVCGuild)
				for _, ch := range data.Channels {
					if ch.GuildID != testTempVCGuild || ch.Name == "" || ch.Position < 0 ||
						(ch.Type == discordgo.ChannelTypeGuildVoice && ch.ParentID != "cat-1") {
						t.Errorf("read channel %+v, which no event sent", ch)
						return
					}
				}
				for _, r := range data.Roles {
					if r.ID == "" || r.Name == "" || r.Position < 0 || r.Color < 0 {
						t.Errorf("read role %+v, which no event sent", r)
						return
					}
				}
				select {
				case <-done:
					return
				default:
				}
			}
		})
	}
	wg.Wait()
}

// A create or a rename puts the channel Discord answered with into the
// cache before it returns, so a read made next finds it without waiting for
// the gateway event. A create whose guild leaves the cache while the
// request is out has still happened, and returns the new channel.
func TestSessionTempVCManagerCreateAndRenamePutDiscordsReplyInTheState(t *testing.T) {
	answering := func(reply string, during func()) *fakeDiscordAPI {
		return &fakeDiscordAPI{answer: func(*http.Request, []byte) (int, []byte) {
			if during != nil {
				during()
			}
			return http.StatusOK, []byte(reply)
		}}
	}
	created := `{"id":"hub-2","guild_id":"` + testTempVCGuild + `","name":"Squad Join","type":2,"parent_id":"cat-1"}`
	create := func(mgr TempVCManager) (*discordgo.Channel, error) {
		return mgr.GuildChannelCreateComplex(testTempVCGuild, discordgo.GuildChannelCreateData{
			Name: "Squad Join", Type: discordgo.ChannelTypeGuildVoice, ParentID: "cat-1",
		}, "created by user-o")
	}

	t.Run("a create", func(t *testing.T) {
		dg := stateSession(t, answering(created, nil))
		feed(t, dg, &discordgo.GuildCreate{Guild: guildDataGuild()})
		mgr := NewSessionTempVCManager(dg, testTempVCGuild)

		if _, err := create(mgr); err != nil {
			t.Fatalf("GuildChannelCreateComplex: %v", err)
		}

		ch := channelIn(mgr.GuildData(testTempVCGuild), "hub-2")
		if ch == nil || ch.Name != "Squad Join" || ch.ParentID != "cat-1" {
			t.Errorf("hub-2 after the create = %+v, want Squad Join under cat-1", ch)
		}
	})

	t.Run("a rename", func(t *testing.T) {
		renamed := `{"id":"hub-1","guild_id":"` + testTempVCGuild + `","name":"Join here","type":2,"parent_id":"cat-1"}`
		dg := stateSession(t, answering(renamed, nil))
		feed(t, dg, &discordgo.GuildCreate{Guild: guildDataGuild()})
		mgr := NewSessionTempVCManager(dg, testTempVCGuild)

		if _, err := mgr.ChannelEdit("hub-1", &discordgo.ChannelEdit{Name: "Join here"}, "renamed by user-o"); err != nil {
			t.Fatalf("ChannelEdit: %v", err)
		}

		if ch := channelIn(mgr.GuildData(testTempVCGuild), "hub-1"); ch == nil || ch.Name != "Join here" {
			t.Errorf("hub-1 after the rename = %+v, want Join here", ch)
		}
	})

	t.Run("a create whose guild leaves the state meanwhile", func(t *testing.T) {
		var dg *discordgo.Session
		outage := func() {
			if err := dg.State.OnInterface(dg, &discordgo.GuildDelete{Guild: &discordgo.Guild{ID: testTempVCGuild, Unavailable: true}}); err != nil {
				t.Errorf("State.OnInterface(GuildDelete): %v", err)
			}
		}
		dg = stateSession(t, answering(created, outage))
		feed(t, dg, &discordgo.GuildCreate{Guild: guildDataGuild()})
		mgr := NewSessionTempVCManager(dg, testTempVCGuild)

		ch, err := create(mgr)

		if err != nil {
			t.Fatalf("GuildChannelCreateComplex = %v, want the create Discord made", err)
		}
		if ch == nil || ch.ID != "hub-2" {
			t.Errorf("created channel = %+v, want hub-2", ch)
		}
	})
}
