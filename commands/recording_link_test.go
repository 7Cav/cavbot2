package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The link at stop (#390). A stop edits the recording notice with a link to
// the recording's page in the panel. Only when the notice is gone, its
// channel or the message itself, does the starter get the link by DM
// instead. The stop runs on the command's goroutine, the notice edit and
// any DM included, before the command replies, so a test reads the fake
// Discord manager once the stop's ✅ has arrived. A link is recognised by
// the panel's base URL the runtime was built with.

// testPanelURL is the panel's base URL every recording scene's runtime
// links to.
const testPanelURL = "https://panel.test"

// stopAsStarter stops the scene's recording with /record stop as the
// starter, and checks the stop's ✅.
func (sc *recordScene) stopAsStarter(t *testing.T) {
	t.Helper()
	if reply := recordAs(t, sc.rt, recStarter, "stop"); !strings.HasPrefix(reply, "✅") {
		t.Fatalf("stop reply = %q, want a ✅ verdict", reply)
	}
}

// deref is the string s points at, empty for nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// A stop edits the notice with a link to the recording in the panel, and
// with the edit through, nobody gets a DM.
func TestRecordingNoticeCarriesTheLinkAtStop(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)

	sc.stopAsStarter(t)

	edit := noticeEdit(t, sc.fake, recordingNotice(t, sc.fake, recordChannel))
	if edit.Content == nil || !strings.Contains(*edit.Content, testPanelURL) {
		t.Errorf("notice edit content = %q, want a link into the panel at %s", deref(edit.Content), testPanelURL)
	}
	if dms := sc.fake.sentDMs(); len(dms) != 0 {
		t.Errorf("DMs after the notice edit went through = %+v, want none", dms)
	}
}

// A notice that is gone by the stop, its channel deleted (10003) or the
// message itself (10008), sends the link to the starter by DM instead: one
// DM, to the starter, carrying it.
func TestRecordingLinkGoesToTheStarterByDMWhenTheNoticeIsGone(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{
		{"unknown channel", discordgo.ErrCodeUnknownChannel},
		{"unknown message", discordgo.ErrCodeUnknownMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, testRecorder)
			sc.startIn(t, recStarter, recordChannel)
			sc.fake.messageEditErr = restError(http.StatusNotFound, tc.code, rawBodyMarker)

			sc.stopAsStarter(t)

			dms := sc.fake.sentDMs()
			if len(dms) != 1 {
				t.Fatalf("DMs after the notice edit failed with %d = %+v, want one", tc.code, dms)
			}
			if dms[0].to != recStarter.id || !strings.Contains(dms[0].data.Content, testPanelURL) {
				t.Errorf("DM to %s = %q, want one to the starter %s with a link into the panel at %s",
					dms[0].to, dms[0].data.Content, recStarter.id, testPanelURL)
			}
		})
	}
}

// A notice edit Discord refuses for any other reason, such as the bot
// lacking a permission in the channel, sends no DM: the notice is still
// there for the channel to read.
func TestRecordingLinkSendsNoDMWhenTheNoticeEditFailsOtherwise(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.fake.messageEditErr = restError(http.StatusForbidden, discordgo.ErrCodeMissingPermissions, rawBodyMarker)

	sc.stopAsStarter(t)

	if dms := sc.fake.sentDMs(); len(dms) != 0 {
		t.Errorf("DMs after the notice edit failed with %d = %+v, want none", discordgo.ErrCodeMissingPermissions, dms)
	}
}
