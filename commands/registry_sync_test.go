package commands

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// fakeGuildCommands is Discord's side of the startup sync for one guild: the
// command names it holds, with a bulk overwrite leaving exactly the names it
// was sent, as Discord does. With hold set, an overwrite closes entered and
// then waits for hold to close, as Discord's rate limit can hold one.
type fakeGuildCommands struct {
	mu           sync.Mutex
	held         []string
	listErr      error
	overwriteErr error
	hold         chan struct{}
	entered      chan struct{}
}

func (f *fakeGuildCommands) ApplicationCommands(_, _ string, _ ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*discordgo.ApplicationCommand, len(f.held))
	for i, n := range f.held {
		out[i] = &discordgo.ApplicationCommand{Name: n}
	}
	return out, nil
}

func (f *fakeGuildCommands) ApplicationCommandBulkOverwrite(_, _ string, cmds []*discordgo.ApplicationCommand, _ ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	if f.hold != nil {
		close(f.entered)
		<-f.hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.overwriteErr != nil {
		return nil, f.overwriteErr
	}
	f.held = f.held[:0]
	for _, c := range cmds {
		f.held = append(f.held, c.Name)
	}
	return cmds, nil
}

func (f *fakeGuildCommands) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.held)
	sort.Strings(out)
	return out
}

func registryOf(names ...string) *Registry {
	r := &Registry{}
	for _, n := range names {
		r.RegisterCommands(Command{
			Definition: &discordgo.ApplicationCommand{Name: n, Description: n},
			Handler:    func(*discordgo.Session, *discordgo.InteractionCreate) {},
		})
	}
	return r
}

func TestRegistrySync_GuildEndsHoldingExactlyTheRegistrysCommands(t *testing.T) {
	guild := &fakeGuildCommands{held: []string{"alpha", "stale"}}

	if err := registryOf("alpha", "beta").Sync(context.Background(), guild, "app", "guild"); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got, want := guild.names(), []string{"alpha", "beta"}; !slices.Equal(got, want) {
		t.Fatalf("guild holds %v, want %v", got, want)
	}
}

func TestRegistrySync_RejectedCommandSetIsAnError(t *testing.T) {
	guild := &fakeGuildCommands{overwriteErr: errors.New("HTTP 400 Bad Request: Invalid Form Body")}

	if err := registryOf("alpha").Sync(context.Background(), guild, "app", "guild"); err == nil {
		t.Fatal("Sync returned nil after Discord rejected the command set")
	}
}

func TestRegistrySync_UnlistableGuildStillGetsTheRegistrysCommands(t *testing.T) {
	guild := &fakeGuildCommands{held: []string{"stale"}, listErr: errors.New("HTTP 503 Service Unavailable")}

	if err := registryOf("alpha").Sync(context.Background(), guild, "app", "guild"); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got, want := guild.names(), []string{"alpha"}; !slices.Equal(got, want) {
		t.Fatalf("guild holds %v, want %v", got, want)
	}
}

func TestRegistrySync_StopWhileDiscordHoldsTheOverwriteReturnsAtOnce(t *testing.T) {
	guild := &fakeGuildCommands{hold: make(chan struct{}), entered: make(chan struct{})}
	t.Cleanup(func() { close(guild.hold) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	returned := make(chan error, 1)
	go func() { returned <- registryOf("alpha").Sync(ctx, guild, "app", "guild") }()
	select {
	case <-guild.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Sync never sent the overwrite")
	}
	cancel()

	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("Sync returned nil after the stop, so startup would carry on")
		}
	case <-time.After(time.Second):
		t.Fatal("Sync was still waiting on Discord 1 s after the stop")
	}
}
