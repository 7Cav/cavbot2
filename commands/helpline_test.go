package commands

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// A targeted invocation addresses the member: their mention has to reach the
// message content, because a mention inside an embed never notifies.
func TestRunHelpline_TargetedMentionsTheMember(t *testing.T) {
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runHelpline(f, i)

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected a single Respond call, got %d: %+v", len(calls), calls)
	}
	if got := calls[0].Response.Data.Content; !strings.Contains(got, "<@111>") {
		t.Fatalf("content = %q, want it to mention the targeted member", got)
	}
}

// With no target there is nobody to address, so no mention may be emitted. The
// bug this guards is building the content unconditionally: an empty ID renders
// the literal "<@>" — a broken mention chip posted to the channel.
func TestRunHelpline_UntargetedEmitsNoMention(t *testing.T) {
	f := &fakeResponder{}
	i := fakeAppCommandInteraction()

	runHelpline(f, i)

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected a single Respond call, got %d: %+v", len(calls), calls)
	}
	if got := calls[0].Response.Data.Content; strings.Contains(got, "<@") {
		t.Fatalf("content = %q, want no mention when no member was targeted", got)
	}
}

// The card is posted publicly. An ephemeral reply is visible only to the
// invoker, which would leave the addressed member unable to read a message
// addressed to them — the one failure that defeats the whole command.
func TestRunHelpline_RespondsPublicly(t *testing.T) {
	f := &fakeResponder{}
	i := fakeAppCommandInteraction(userOption("user", "111"))

	runHelpline(f, i)

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected a single Respond call, got %d: %+v", len(calls), calls)
	}
	if calls[0].Response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("response type = %v, want ChannelMessageWithSource", calls[0].Response.Type)
	}
	if calls[0].Response.Data.Flags&discordgo.MessageFlagsEphemeral != 0 {
		t.Fatalf("response must not be ephemeral; flags = %d", calls[0].Response.Data.Flags)
	}
}

// renderedHelplineCard joins every embed description in the response, so
// assertions read the card as a member sees it rather than reaching into the
// resource table. How the card is split across embeds is layout, not contract.
func renderedHelplineCard(t *testing.T) string {
	t.Helper()

	f := &fakeResponder{}
	runHelpline(f, fakeAppCommandInteraction())

	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected a single Respond call, got %d: %+v", len(calls), calls)
	}
	var sb strings.Builder
	for _, embed := range calls[0].Response.Data.Embeds {
		sb.WriteString(embed.Description)
		sb.WriteString("\n")
	}
	return sb.String()
}

// Every resource the bot carries must actually reach the rendered card. This
// iterates the production table rather than pinning a URL list, so retiring a
// resource is a content edit and not a test failure; what it catches is a
// resource that exists but never renders — a section the template skips, or a
// dropped link.
func TestRunHelpline_EveryResourceLinkReachesTheCard(t *testing.T) {
	card := renderedHelplineCard(t)

	for _, section := range helplineSections {
		for _, resource := range section.Resources {
			if resource.URL == "" {
				t.Errorf("resource %q carries no URL", resource.Name)
				continue
			}
			if !strings.Contains(card, resource.URL) {
				t.Errorf("resource %q has URL %s, which does not appear on the rendered card", resource.Name, resource.URL)
			}
		}
	}
}

// Dial and text routes render from a different field than the links, so a
// template change can drop every number while all five URLs still render and
// the card still looks complete. A member in an acute moment should not have to
// open a browser to find a number.
//
// This iterates the table rather than pinning the digits: a bare "988" literal
// would be vacuous, since it also occurs inside 988lifeline.org and so could
// never fail while that resource exists at all.
func TestRunHelpline_ContactRoutesReachTheCard(t *testing.T) {
	card := renderedHelplineCard(t)

	checked := 0
	for _, section := range helplineSections {
		for _, resource := range section.Resources {
			if resource.Contact == "" {
				continue
			}
			checked++
			if !strings.Contains(card, resource.Contact) {
				t.Errorf("resource %q carries contact routes %q, which do not appear on the rendered card",
					resource.Name, resource.Contact)
			}
		}
	}
	// Guards the loop itself: if every Contact were emptied, the body above
	// would pass by never running.
	if checked == 0 {
		t.Fatal("no resource carries contact routes; the crisis lines must render a way to reach them")
	}
}

// The command is wired into the registry so Discord registers it and routes its
// interactions to the handler.
func TestRegistry_RegistersHelpline(t *testing.T) {
	reg := NewRegistry()

	registered := false
	for _, def := range reg.GetCommands() {
		if def.Name == "helpline" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("helpline must be registered in the command registry")
	}
	if _, ok := reg.GetHandler("helpline"); !ok {
		t.Fatal("helpline must resolve a handler from the registry")
	}
}
