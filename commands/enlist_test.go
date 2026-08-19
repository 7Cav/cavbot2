package commands

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// Registration is what makes the command exist: unregistered, Discord never
// creates it and no interaction ever routes to the handler.
func TestRegistry_RegistersEnlist(t *testing.T) {
	reg := NewRegistry()

	for _, def := range reg.GetCommands() {
		if def.Name == "enlist" {
			return
		}
	}
	t.Fatal("enlist must be registered in the command registry")
}

// The infographic exists to be shown to a recruit in the channel. An ephemeral
// reply reaches only whoever typed the command, leaving the person being
// onboarded unable to see the one thing this command exists to show them.
func TestRunEnlist_RespondsPublicly(t *testing.T) {
	f := &fakeResponder{}

	runEnlist(f, fakeAppCommandInteraction())

	delivered := 0
	for _, c := range f.Calls() {
		if c.Response == nil {
			continue
		}
		delivered++
		if c.Response.Data.Flags&discordgo.MessageFlagsEphemeral != 0 {
			t.Errorf("response must not be ephemeral; flags = %d", c.Response.Data.Flags)
		}
	}
	// Guards the loop: with nothing delivered the body never runs and the
	// assertion above would pass by never executing.
	if delivered == 0 {
		t.Fatal("no response was delivered; Discord shows the invoker a failure")
	}
}

// deliveredEmbeds gathers every embed the invoker would see, off whichever call
// shape carried it. Reading shape-agnostically keeps the assertions on what
// reaches the member rather than on whether the command answers immediately,
// defers and edits, or follows up — all of which deliver the same card.
func deliveredEmbeds(calls []recordedCall) []*discordgo.MessageEmbed {
	var embeds []*discordgo.MessageEmbed
	for _, c := range calls {
		switch {
		case c.Response != nil && c.Response.Data != nil:
			embeds = append(embeds, c.Response.Data.Embeds...)
		case c.Edit != nil && c.Edit.Embeds != nil:
			embeds = append(embeds, *c.Edit.Embeds...)
		case c.Params != nil:
			embeds = append(embeds, c.Params.Embeds...)
		}
	}
	return embeds
}

// The infographic is the entire payload. The embed's own text only says to
// follow the steps below — the steps themselves exist nowhere but inside the
// image, so a card delivered without it tells the recruit nothing.
func TestRunEnlist_DeliversTheInfographic(t *testing.T) {
	f := &fakeResponder{}

	runEnlist(f, fakeAppCommandInteraction())

	for _, e := range deliveredEmbeds(f.Calls()) {
		if e.Image != nil && e.Image.URL != "" {
			return
		}
	}
	t.Fatal("no delivered embed carries an image; the infographic is the whole command")
}

// deliveredText returns the user-facing text a call carried, off whichever
// shape carried it. It is empty for a delivery that carried only embeds.
func deliveredText(c recordedCall) string {
	switch {
	case c.Response != nil && c.Response.Data != nil:
		return c.Response.Data.Content
	case c.Edit != nil && c.Edit.Content != nil:
		return *c.Edit.Content
	case c.Params != nil:
		return c.Params.Content
	}
	return ""
}

// A failed response must not leave the invoker looking at Discord's own "the
// application did not respond". This is the defect the command carried through
// review: the failure was logged server-side and the member was told nothing.
//
// The failed attempt is itself recorded — carrying the infographic embed and no
// text — so text is what tells a real reply apart from the attempt that failed.
// That holds without pinning the reply's position, its shape, or its wording.
func TestRunEnlist_FailedResponseStillTellsTheInvoker(t *testing.T) {
	f := &fakeResponder{RespondErrs: []error{errors.New("discord unavailable")}}

	runEnlist(f, fakeAppCommandInteraction())

	for _, c := range f.Calls() {
		if deliveredText(c) != "" {
			return
		}
	}
	t.Fatal("the invoker was told nothing after the response failed")
}

// deliveredCardText joins everything the invoker actually reads: message
// content plus every embed's title and description, across whichever call
// shape carried them.
func deliveredCardText(calls []recordedCall) string {
	var sb strings.Builder
	for _, c := range calls {
		sb.WriteString(deliveredText(c))
		sb.WriteString("\n")
	}
	for _, e := range deliveredEmbeds(calls) {
		sb.WriteString(e.Title)
		sb.WriteString("\n")
		sb.WriteString(e.Description)
		sb.WriteString("\n")
	}
	return sb.String()
}

// enlistLinkPattern matches the enlistment URL only where it ends at a
// boundary. A plain substring check also matches a longer path that merely
// starts with it (/enlistment, /enlist-now), so a divergent hardcoded URL
// slips through. That is the same trap a bare "988" literal falls into in
// helpline_test.go, and it was caught here by mutating the code and watching
// the substring form stay green.
var enlistLinkPattern = regexp.MustCompile(regexp.QuoteMeta(enlistFormURL) + `($|[^\w/-])`)

// The infographic tells a recruit to head to the enlistment page, but that
// address is pixels inside the image and cannot be clicked. The card has to
// carry the URL as text for the client to make it reachable.
//
// What this holds: the URL reaches the rendered card. What it does not hold:
// that Discord renders it as a clickable link, which is a live-client property
// the test-guild smoke test covers, or that the URL is correct, which needs a
// network fetch this does not make.
func TestRunEnlist_CardCarriesTheEnlistmentLink(t *testing.T) {
	f := &fakeResponder{}

	runEnlist(f, fakeAppCommandInteraction())

	card := deliveredCardText(f.Calls())
	if !enlistLinkPattern.MatchString(card) {
		t.Fatalf("card does not carry %s, so every route to enlisting exists only inside the image\ncard: %s", enlistFormURL, card)
	}
}
