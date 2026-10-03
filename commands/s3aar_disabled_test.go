package commands

import (
	"regexp"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// regimentDate matches a date written the regiment's way, DDMMMYY (01DEC26).
var regimentDate = regexp.MustCompile(`\b\d{2}(JAN|FEB|MAR|APR|MAY|JUN|JUL|AUG|SEP|OCT|NOV|DEC)\d{2}\b`)

// A member who still reaches for /s3aar learns, in a reply only they see,
// where to say they need it and the date it goes away, so they know how long
// they have to speak up.
func TestDisabledS3aarTellsTheInvokerWhereToGoAndByWhen(t *testing.T) {
	f := &fakeResponder{}

	runS3aarDisabled(f, fakeAppCommandInteraction())

	calls := f.Calls()
	if len(calls) == 0 {
		t.Fatal("the member got no reply")
	}
	if first := calls[0]; first.Method != "Respond" || first.Response == nil || first.Response.Data == nil ||
		first.Response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("first call = %+v, want an ephemeral response", first)
	}
	var shown string
	switch last := calls[len(calls)-1]; {
	case last.Edit != nil && last.Edit.Content != nil:
		shown = *last.Edit.Content
	case last.Response != nil && last.Response.Data != nil:
		shown = last.Response.Data.Content
	}
	if !strings.Contains(shown, "S6") {
		t.Errorf("reply %q does not send the member to S6", shown)
	}
	if !regimentDate.MatchString(shown) {
		t.Errorf("reply %q names no removal date", shown)
	}
}

// /s3aar stays declared under its name, so it keeps its command ID and with it
// any Server Settings restriction (see S3AARDisabled). It asks for nothing,
// because all a member can get from it is that it is off.
func TestRegistryDeclaresS3aarWithNoOptions(t *testing.T) {
	var def *discordgo.ApplicationCommand
	for _, d := range NewRegistry(nil).GetCommands() {
		if d.Name == "s3aar" {
			def = d
		}
	}
	if def == nil {
		t.Fatal("s3aar is not registered")
	}
	if len(def.Options) != 0 {
		t.Errorf("s3aar declares %d options, want none", len(def.Options))
	}
}
