package commands

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/voice"
	"github.com/bwmarrin/discordgo"
)

// /record (#386). Every case puts members in voice through the fake cache,
// runs the command through the fake responder, and judges the runtime by
// where the fake voice adapter's recorders are and by the store's
// recording rows. No reply wording is asserted: a reply is read for its
// verdict marker, the names it carries, and the production constants that
// tell refusals apart.

// The fixture's channels and recording role. Neither channel is a hub.
const (
	recordChannel      = "vc-briefing"
	recordChannelName  = "Briefing Room"
	recordOtherChannel = "vc-other"
	testRecordingRole  = "role-recording"
)

// The fixture's members. The starter and the holder each hold the recording
// role and a rank role, so either may start or stop a recording. The
// outsider is a Cav member with no recording role.
var (
	recStarter  = permMember{id: "user-s", roles: []string{testRecordingRole, testRankSGT}}
	recHolder   = permMember{id: "user-h", roles: []string{testRecordingRole, testRankSGT}}
	recOutsider = permMember{id: "user-n", roles: []string{testRankSGT}}
)

// fakeRecorderVoice stands in for the voice adapter: the recorders
// configured, and every voice connection they joined. joinErr, when set, is
// what every join returns instead.
type fakeRecorderVoice struct {
	mu        sync.Mutex
	recorders []string
	conns     []*fakeVoiceConn
	joinErr   error
}

// fakeVoiceConn is one recorder in one channel, until it leaves. handle is
// what the runtime passed to Receive, nil before it does.
type fakeVoiceConn struct {
	v                     *fakeRecorderVoice
	recorderID, channelID string
	left                  bool
	handle                func(voice.Frame)
}

func (v *fakeRecorderVoice) UserIDs() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return slices.Clone(v.recorders)
}

func (v *fakeRecorderVoice) Join(_ context.Context, recorderID, guildID, channelID string) (voice.Conn, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.joinErr != nil {
		return nil, v.joinErr
	}
	c := &fakeVoiceConn{v: v, recorderID: recorderID, channelID: channelID}
	v.conns = append(v.conns, c)
	return c, nil
}

func (c *fakeVoiceConn) Leave(context.Context) {
	c.v.mu.Lock()
	defer c.v.mu.Unlock()
	c.left = true
}

func (c *fakeVoiceConn) Receive(handle func(voice.Frame)) {
	c.v.mu.Lock()
	defer c.v.mu.Unlock()
	c.handle = handle
}

// joinCount is how many joins went out.
func (v *fakeRecorderVoice) joinCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.conns)
}

// inChannel lists the recorders in a channel now: joined and not left.
func (v *fakeRecorderVoice) inChannel(channelID string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for _, c := range v.conns {
		if c.channelID == channelID && !c.left {
			out = append(out, c.recorderID)
		}
	}
	return out
}

// recordScene is the fixture: the fake cache holding the two channels, a
// store holding the test hub and the recording role, temp VC over them, and
// the runtime over a fake voice adapter with the recorders given.
type recordScene struct {
	// dir is the recordings directory.
	dir string
	// freeDisk is the free space the runtime reads on the recordings
	// volume, plenty unless a test sets it.
	freeDisk *atomic.Uint64
	clock    *fakeClock
	fake     *fakeTempVCManager
	st       *store.Fake
	tv       *TempVC
	voice    *fakeRecorderVoice
	// mixer builds every mix, writing "fake mix" until a test sets
	// otherwise.
	mixer *fakeMixer
	rt    *RecordingRuntime
	// lib reads the recordings back, as the panel does.
	lib *RecordingLibrary
}

func newRecordScene(t *testing.T, recorders ...string) *recordScene {
	t.Helper()
	sc := &recordScene{dir: t.TempDir(), clock: installFakeClock(t), fake: newFakeTempVCManager(), freeDisk: &atomic.Uint64{}}
	sc.freeDisk.Store(1 << 40)
	prevFreeDisk := recordingFreeDisk
	recordingFreeDisk = func(string) (uint64, error) { return sc.freeDisk.Load(), nil }
	t.Cleanup(func() { recordingFreeDisk = prevFreeDisk })
	sc.fake.channels[recordChannel] = &discordgo.Channel{ID: recordChannel, Name: recordChannelName, Type: discordgo.ChannelTypeGuildVoice}
	sc.fake.channels[recordOtherChannel] = &discordgo.Channel{ID: recordOtherChannel, Name: "Other", Type: discordgo.ChannelTypeGuildVoice}
	sc.st = seedStore(t, testHub())
	saveRecordingRoles(t, sc.st, testRecordingRole)
	sc.voice = &fakeRecorderVoice{recorders: recorders}
	sc.tv = newTestTempVC(t, sc.fake, sc.st)
	sc.mixer = &fakeMixer{out: []byte("fake mix")}
	sc.rt = NewRecordingRuntime(sc.fake, sc.st, sc.tv, testTempVCGuild, sc.voice, sc.mixer, sc.dir)
	sc.lib = NewRecordingLibrary(sc.st, testTempVCGuild, sc.dir)
	return sc
}

// saveRecordingRoles saves the guild's recording roles through the store's
// save, the one call the panel's recording roles save makes.
func saveRecordingRoles(t *testing.T, st store.Store, roleIDs ...string) {
	t.Helper()
	ctx := context.Background()
	current, err := st.GetRecordingRoles(ctx, testTempVCGuild)
	if err != nil {
		t.Fatalf("GetRecordingRoles: %v", err)
	}
	entry := store.ChangeLogEntry{ForumUserID: 1, ForumUsername: "admin", Action: store.ChangeRecordingRoles, Diff: json.RawMessage(`{}`)}
	if err := st.SaveRecordingRoles(ctx, testTempVCGuild, store.RecordingRoles{RoleIDs: roleIDs, Version: current.Version}, entry); err != nil {
		t.Fatalf("SaveRecordingRoles: %v", err)
	}
}

// recordings lists the store's recording rows.
func (sc *recordScene) recordings(t *testing.T) []store.Recording {
	t.Helper()
	recs, err := sc.st.ListRecordings(context.Background(), testTempVCGuild)
	if err != nil {
		t.Fatalf("ListRecordings: %v", err)
	}
	return recs
}

// recordInteraction builds the interaction /record receives from m, with
// the subcommand and its options.
func recordInteraction(m permMember, subcommand string, opts ...*discordgo.ApplicationCommandInteractionDataOption) *discordgo.InteractionCreate {
	member := m.discordMember()
	member.User.Username = "tester"
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type:    discordgo.InteractionApplicationCommand,
		GuildID: testTempVCGuild,
		Member:  member,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: recordCommandName,
			Options: []*discordgo.ApplicationCommandInteractionDataOption{{
				Name: subcommand, Type: discordgo.ApplicationCommandOptionSubCommand, Options: opts,
			}},
		},
	}}
}

// recordAs runs /record as m, answered the way Discord answers: the deferral
// is accepted and a later response refused as already acknowledged, so a
// reply sent as a response lands as an edit of the deferred one. It fails
// the test unless every response was ephemeral, and returns the reply.
func recordAs(t *testing.T, rt *RecordingRuntime, m permMember, subcommand string, opts ...*discordgo.ApplicationCommandInteractionDataOption) string {
	t.Helper()
	f := &fakeResponder{RespondErrs: []error{nil, errAlreadyAcked}}
	runRecord(f, rt, recordInteraction(m, subcommand, opts...))
	return ephemeralRecordReply(t, f)
}

// ephemeralRecordReply checks that the first response was a deferral only
// the member sees, and that nothing after it was public, and returns the
// text of the last edit.
func ephemeralRecordReply(t *testing.T, f *fakeResponder) string {
	t.Helper()
	calls := f.Calls()
	if len(calls) == 0 || calls[0].Method != "Respond" || calls[0].Response == nil ||
		calls[0].Response.Type != discordgo.InteractionResponseDeferredChannelMessageWithSource ||
		calls[0].Response.Data == nil || calls[0].Response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
		t.Fatalf("responder calls = %+v, want an ephemeral deferral first", calls)
	}
	reply, replied := "", false
	for _, c := range calls[1:] {
		switch c.Method {
		case "Respond":
			if c.Response.Data == nil || c.Response.Data.Flags&discordgo.MessageFlagsEphemeral == 0 {
				t.Errorf("response %+v is not ephemeral", c.Response)
			}
		case "Edit":
			if c.Edit.Content != nil {
				reply, replied = *c.Edit.Content, true
			}
		case "Followup":
			t.Errorf("followup %+v, want every reply in the deferred one", c.Params)
		}
	}
	if !replied {
		t.Fatalf("responder calls = %+v, want the deferred reply edited", calls)
	}
	return reply
}

// A start with everything in place joins a recorder to the caller's channel
// and writes a row with the starter, the channel, the title, the recorder
// and the start time.
func TestRecordStartJoinsARecorderAndWritesItsRow(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.fake.setVoice(recStarter.id, recordChannel)

	reply := recordAs(t, sc.rt, recStarter, "start", stringOption("title", "S2 interview"))

	if !strings.HasPrefix(reply, "✅") {
		t.Errorf("reply = %q, want a ✅ verdict", reply)
	}
	if got := sc.voice.inChannel(recordChannel); !slices.Equal(got, []string{testRecorder}) {
		t.Errorf("recorders in %s = %v, want [%s]", recordChannel, got, testRecorder)
	}
	recs := sc.recordings(t)
	if len(recs) != 1 {
		t.Fatalf("recordings = %+v, want one", recs)
	}
	got := recs[0]
	if got.ChannelID != recordChannel || got.ChannelName != recordChannelName || got.StarterID != recStarter.id ||
		got.Title != "S2 interview" || got.RecorderID != testRecorder {
		t.Errorf("row = %+v, want %s (%s) started by %s, titled S2 interview, recorded by %s",
			got, recordChannel, recordChannelName, recStarter.id, testRecorder)
	}
	if !got.StartedAt.Equal(sc.clock.read()) || !got.StoppedAt.IsZero() {
		t.Errorf("row started at %v, stopped at %v, want started at %v and running", got.StartedAt, got.StoppedAt, sc.clock.read())
	}
}

// recordRefusal is one /record start refusal: a scene with the recorders
// given, a setup that leaves exactly one precondition unmet for the caller,
// and what the reply must and must not carry.
type recordRefusal struct {
	name      string
	recorders []string
	// setup arranges the scene and returns the member who runs /record
	// start.
	setup          func(t *testing.T, sc *recordScene) permMember
	carries, lacks []string
}

var recordRefusals = []recordRefusal{
	{
		name:      "not in voice",
		recorders: []string{testRecorder},
		setup:     func(*testing.T, *recordScene) permMember { return recStarter },
		carries:   []string{recordNotInVoiceRefusal},
	},
	{
		name:      "in a hub",
		recorders: []string{testRecorder},
		setup: func(_ *testing.T, sc *recordScene) permMember {
			sc.fake.setVoice(recStarter.id, testTempVCHub)
			return recStarter
		},
		carries: []string{recordInHubRefusal},
	},
	{
		name:      "no recording role",
		recorders: []string{testRecorder},
		setup: func(_ *testing.T, sc *recordScene) permMember {
			sc.fake.setVoice(recOutsider.id, recordChannel)
			return recOutsider
		},
		carries: []string{noRecordingRoleRefusal},
	},
	{
		name:      "recording role but no rank role",
		recorders: []string{testRecorder},
		setup: func(_ *testing.T, sc *recordScene) permMember {
			applicant := permMember{id: "user-a", roles: []string{testRecordingRole}}
			sc.fake.setVoice(applicant.id, recordChannel)
			return applicant
		},
		carries: []string{notCavMemberRefusal},
	},
	{
		name:      "no recorder free",
		recorders: []string{testRecorder},
		setup: func(t *testing.T, sc *recordScene) permMember {
			sc.fake.setVoice(recHolder.id, recordChannel)
			recordAs(t, sc.rt, recHolder, "start")
			sc.fake.setVoice(recStarter.id, recordOtherChannel)
			return recStarter
		},
		carries: []string{noFreeRecorderRefusal, "<#" + recordChannel + ">"},
	},
	{
		name: "recording off",
		setup: func(_ *testing.T, sc *recordScene) permMember {
			sc.fake.setVoice(recStarter.id, recordChannel)
			return recStarter
		},
		carries: []string{recordingOffRefusal},
		lacks:   []string{noFreeRecorderRefusal},
	},
}

// A start is refused when any one precondition fails: no recorder joins,
// no row is written, and the reply is an ephemeral ❌ carrying that
// precondition's refusal. Each fixture meets every other precondition. No
// wording is asserted beyond the refusal constants that tell the cases
// apart, and the channel the no-free-recorder refusal names.
func TestRecordStartRefusals(t *testing.T) {
	for _, tc := range recordRefusals {
		t.Run(tc.name, func(t *testing.T) {
			sc := newRecordScene(t, tc.recorders...)
			caller := tc.setup(t, sc)
			joins, rows := sc.voice.joinCount(), len(sc.recordings(t))

			reply := recordAs(t, sc.rt, caller, "start")

			if !strings.HasPrefix(reply, "❌") {
				t.Errorf("reply = %q, want a ❌ verdict", reply)
			}
			if got := sc.voice.joinCount(); got != joins {
				t.Errorf("joins = %d, want %d, none from the refused start", got, joins)
			}
			if got := len(sc.recordings(t)); got != rows {
				t.Errorf("recordings = %d, want %d, none from the refused start", got, rows)
			}
			for _, s := range tc.carries {
				if !strings.Contains(reply, s) {
					t.Errorf("reply = %q, want it to carry %q", reply, s)
				}
			}
			for _, s := range tc.lacks {
				if strings.Contains(reply, s) {
					t.Errorf("reply = %q, want it without %q", reply, s)
				}
			}
		})
	}
}

// startIn has m start a recording of a channel, and fails the test unless a
// recorder joined it.
func (sc *recordScene) startIn(t *testing.T, m permMember, channelID string) {
	t.Helper()
	sc.fake.setVoice(m.id, channelID)
	recordAs(t, sc.rt, m, "start")
	if len(sc.voice.inChannel(channelID)) != 1 {
		t.Fatalf("no recorder joined %s at the start", channelID)
	}
}

// assertStopped checks that the recorder has left the channel, the
// recording's one row has its stop time, the clock's now, and records that
// it ended as end, and the recording notice was edited to lose its button.
func (sc *recordScene) assertStopped(t *testing.T, channelID string, end store.RecordingEnd) {
	t.Helper()
	if got := sc.voice.inChannel(channelID); len(got) != 0 {
		t.Errorf("recorders in %s after the stop = %v, want none", channelID, got)
	}
	recs := sc.recordings(t)
	if len(recs) != 1 || !recs[0].StoppedAt.Equal(sc.clock.read()) || recs[0].Ended != end {
		t.Errorf("recordings after the stop = %+v, want one stopped at %v, ended %q", recs, sc.clock.read(), end)
	}
	// A nil list leaves the button in place; an empty one removes it.
	edit := noticeEdit(t, sc.fake, recordingNotice(t, sc.fake, channelID))
	if edit.Components == nil || len(*edit.Components) != 0 {
		t.Errorf("notice edit components = %v, want an empty list", edit.Components)
	}
}

// assertRunning checks that the recorder is still in the channel and the
// recording's one row is still open.
func (sc *recordScene) assertRunning(t *testing.T, channelID string) {
	t.Helper()
	if got := sc.voice.inChannel(channelID); len(got) != 1 {
		t.Errorf("recorders in %s = %v, want the one recording it", channelID, got)
	}
	if recs := sc.recordings(t); len(recs) != 1 || !recs[0].StoppedAt.IsZero() {
		t.Errorf("recordings = %+v, want one still running", recs)
	}
}

// The starter stops their recording from another channel: the recorder
// leaves the channel it records, and the row records the stop time.
func TestRecordStopByTheStarterFromAnotherChannel(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.fake.setVoice(recStarter.id, recordOtherChannel)
	sc.clock.advance(17 * time.Minute)

	reply := recordAs(t, sc.rt, recStarter, "stop")

	if !strings.HasPrefix(reply, "✅") {
		t.Errorf("reply = %q, want a ✅ verdict", reply)
	}
	sc.assertStopped(t, recordChannel, store.RecordingEndStopped)
}

// The recording stays with its channel when the starter moves elsewhere: a
// recording-role holder still in that channel stops it, and the recorder
// leaves it.
func TestRecordStaysWithItsChannelWhenTheStarterMoves(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.fake.setVoice(recStarter.id, recordOtherChannel)
	sc.fake.setVoice(recHolder.id, recordChannel)

	reply := recordAs(t, sc.rt, recHolder, "stop")

	if !strings.HasPrefix(reply, "✅") {
		t.Errorf("reply = %q, want a ✅ verdict", reply)
	}
	sc.assertStopped(t, recordChannel, store.RecordingEndStopped)
}

// A member in the channel who neither started the recording nor holds a
// recording role is refused, and the recording continues: the recorder
// stays and the row stays open.
func TestRecordStopByAnyoneElseIsRefused(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	sc.startIn(t, recStarter, recordChannel)
	sc.fake.setVoice(recOutsider.id, recordChannel)

	reply := recordAs(t, sc.rt, recOutsider, "stop")

	if !strings.HasPrefix(reply, notAllowedToStopRefusal) {
		t.Errorf("reply = %q, want the refusal %q", reply, notAllowedToStopRefusal)
	}
	if got := sc.voice.inChannel(recordChannel); !slices.Equal(got, []string{testRecorder}) {
		t.Errorf("recorders in %s after the refused stop = %v, want [%s]", recordChannel, got, testRecorder)
	}
	if recs := sc.recordings(t); len(recs) != 1 || !recs[0].StoppedAt.IsZero() {
		t.Errorf("recordings after the refused stop = %+v, want one still running", recs)
	}
}

// A recording roles save on the panel decides the next start, with no
// restart: a member refused for holding no recording role starts once their
// role is saved as one, on the same runtime.
func TestRecordStartFollowsARecordingRolesSave(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	interviewer := permMember{id: "user-i", roles: []string{"role-s2", testRankSGT}}
	sc.fake.setVoice(interviewer.id, recordChannel)
	recordAs(t, sc.rt, interviewer, "start")
	if sc.voice.joinCount() != 0 {
		t.Fatal("a recorder joined before the member's role was a recording role")
	}

	saveRecordingRoles(t, sc.st, testRecordingRole, "role-s2")
	recordAs(t, sc.rt, interviewer, "start")

	if got := sc.voice.inChannel(recordChannel); !slices.Equal(got, []string{testRecorder}) {
		t.Errorf("recorders in %s after the save = %v, want [%s]", recordChannel, got, testRecorder)
	}
}

// A recorder that fails to join is reported to Sentry and writes no row,
// and it is free again: the next start joins it.
func TestRecordStartJoinFailureIsReportedAndFreesTheRecorder(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	captures := countCaptures(t)
	sc.voice.joinErr = errors.New("voice gateway closed 4017")
	sc.fake.setVoice(recStarter.id, recordChannel)

	recordAs(t, sc.rt, recStarter, "start")

	if *captures != 1 {
		t.Errorf("Sentry captures = %d, want 1", *captures)
	}
	if recs := sc.recordings(t); len(recs) != 0 {
		t.Errorf("recordings after the failed join = %+v, want none", recs)
	}
	sc.voice.joinErr = nil
	recordAs(t, sc.rt, recStarter, "start")
	if got := sc.voice.inChannel(recordChannel); !slices.Equal(got, []string{testRecorder}) {
		t.Errorf("recorders in %s after the next start = %v, want [%s]", recordChannel, got, testRecorder)
	}
}

// A start whose row can't be written is reported to Sentry, and no
// recorder is left in the channel recording with no row.
func TestRecordStartRowWriteFailureLeavesNoRecorderInTheChannel(t *testing.T) {
	sc := newRecordScene(t, testRecorder)
	captures := countCaptures(t)
	failing := &failingStore{Fake: sc.st, startRecordingErr: errors.New("connection refused")}
	rt := NewRecordingRuntime(sc.fake, failing, sc.tv, testTempVCGuild, sc.voice, sc.mixer, sc.dir)
	sc.fake.setVoice(recStarter.id, recordChannel)

	recordAs(t, rt, recStarter, "start")

	if *captures != 1 {
		t.Errorf("Sentry captures = %d, want 1", *captures)
	}
	if got := sc.voice.inChannel(recordChannel); len(got) != 0 {
		t.Errorf("recorders in %s after the failed write = %v, want none", recordChannel, got)
	}
}

// /record stays in Discord's command list with no recorder token, or with no
// bot store, so it keeps the Server Settings roles Discord keys by its ID:
// the registry declares it with no runtime, and with a runtime that has no
// recorder.
func TestRegistryDeclaresRecordWhetherRecordingIsOnOrOff(t *testing.T) {
	noRecorder := newRecordScene(t).rt
	for name, rt := range map[string]*RecordingRuntime{"no bot store": nil, "no recorder token": noRecorder} {
		t.Run(name, func(t *testing.T) {
			if _, ok := NewRegistry(nil, nil, rt).GetHandler(recordCommandName); !ok {
				t.Errorf("%s is not registered", recordCommandName)
			}
		})
	}
}

// On a host with no bot store there is no runtime. /record still exists, so
// a start is refused with the ephemeral shape instead of dereferencing a
// nil runtime.
func TestRecordNoRuntimeRefused(t *testing.T) {
	if reply := recordAs(t, nil, recStarter, "start"); reply != recordingOffRefusal {
		t.Errorf("reply = %q, want the refusal %q", reply, recordingOffRefusal)
	}
}
