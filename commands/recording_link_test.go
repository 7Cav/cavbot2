package commands

import (
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

// The link at stop (#390). A stop edits the recording notice with a link to
// the recording's page in the panel. The starter gets the link by DM as
// well in a spawned channel (#620), and instead in any other channel whose
// notice is gone, the channel or the message itself. The stop runs on the command's goroutine, the notice edit and
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

// recordSpawned is the spawned channel the starter takes from the test hub.
const recordSpawned = "vc-spawned"

// startInSpawned gives the test hub a delete delay, has the starter take a
// spawned channel from it the way temp VC spawns one, and starts a
// recording of that channel. It returns the recording notice, read before
// temp VC's ownership notices can follow it into the channel's chat.
func (sc *recordScene) startInSpawned(t *testing.T) fakeMessage {
	t.Helper()
	hub := storedTestHub(t, sc.st)
	hub.DeleteDelayMinutes = 10
	sc.tv.ApplyHub(hub)
	spawnInto(sc.tv, sc.fake, recStarter.id, recordSpawned, member("Starter", recStarter.roles...))
	sc.startIn(t, recStarter, recordSpawned)
	return recordingNotice(t, sc.fake, recordSpawned)
}

// starterLeavesSpawned has the starter, the last human in the spawned
// channel, leave it. Temp VC and the recording runtime are each handed the
// leave, after the fake cache, as discordgo hands them a voice event. The
// channel then waits out its hub's delete delay, notice and all.
func (sc *recordScene) starterLeavesSpawned() {
	vs := voiceEvent(recStarter.id, "", member("Starter", recStarter.roles...))
	sc.fake.deliver(sc.tv, vs)
	sc.rt.HandleVoiceStateUpdate(vs)
}

// assertOneLinkDM checks that dms, the DMs a stop or a recovery sent, are
// one DM, to the starter, carrying the link into the panel.
func assertOneLinkDM(t *testing.T, dms []fakeDM) {
	t.Helper()
	if len(dms) != 1 {
		t.Fatalf("DMs = %+v, want one", dms)
	}
	if dms[0].to != recStarter.id || !strings.Contains(dms[0].data.Content, testPanelURL) {
		t.Errorf("DM to %s = %q, want one to the starter %s with a link into the panel at %s",
			dms[0].to, dms[0].data.Content, recStarter.id, testPanelURL)
	}
}

// In a spawned channel the starter gets the link by DM as well as in the
// notice (#620): once everyone has left, the hub's delete delay takes the
// channel and its notice away, and the starter, gone already, would never
// read it.
func TestRecordingInASpawnedChannelSendsTheStarterTheLinkByDM(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	notice := sc.startInSpawned(t)
	before := len(sc.fake.sentDMs())

	sc.starterLeavesSpawned()

	edit := noticeEdit(t, sc.fake, notice)
	if edit.Content == nil || !strings.Contains(*edit.Content, testPanelURL) {
		t.Errorf("notice edit content = %q, want a link into the panel at %s", deref(edit.Content), testPanelURL)
	}
	assertOneLinkDM(t, sc.fake.sentDMs()[before:])
}

// A spawned channel already gone by the stop, deleted while temp VC still
// tracks it, is both a spawned channel and a notice gone (10003). The
// starter gets one DM, not one for each.
func TestRecordingInAGoneSpawnedChannelSendsTheStarterOneDM(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startInSpawned(t)
	sc.fake.messageEditErr = restError(http.StatusNotFound, discordgo.ErrCodeUnknownChannel, rawBodyMarker)
	before := len(sc.fake.sentDMs())

	sc.starterLeavesSpawned()

	assertOneLinkDM(t, sc.fake.sentDMs()[before:])
}

// With the panel off there is no link, so a stop in a spawned channel DMs
// nobody. The notice is still edited to say the recording stopped.
func TestRecordingInASpawnedChannelSendsNoDMWithThePanelOff(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.rt = NewRecordingRuntime(sc.fake, sc.faults, sc.tv, testTempVCGuild, sc.voice, sc.mixer, sc.dir, "")
	notice := sc.startInSpawned(t)
	before := len(sc.fake.sentDMs())

	sc.starterLeavesSpawned()

	noticeEdit(t, sc.fake, notice)
	if dms := sc.fake.sentDMs()[before:]; len(dms) != 0 {
		t.Errorf("DMs with the panel off = %+v, want none", dms)
	}
}
