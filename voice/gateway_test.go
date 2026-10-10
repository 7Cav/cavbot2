package voice

import (
	"context"
	"errors"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgo/gateway"
	disgovoice "github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// The production recorder session, over a fake disgo voice manager. Only
// OpenGateway needs Discord itself, and the smoke test covers it.

// fakeManager stands in for disgo's voice manager: it records the events
// handed to it and whether it was closed, and creates conn for every join.
// The methods it doesn't override belong to the nil Manager it embeds, so a
// call to one fails the test.
type fakeManager struct {
	disgovoice.Manager
	states  []gateway.EventVoiceStateUpdate
	servers []gateway.EventVoiceServerUpdate
	conn    *fakeDisgoConn
	closed  bool
}

func (m *fakeManager) HandleVoiceStateUpdate(e gateway.EventVoiceStateUpdate) {
	m.states = append(m.states, e)
}

func (m *fakeManager) HandleVoiceServerUpdate(e gateway.EventVoiceServerUpdate) {
	m.servers = append(m.servers, e)
}

func (m *fakeManager) CreateConn(snowflake.ID) disgovoice.Conn { return m.conn }
func (m *fakeManager) Close(context.Context)                   { m.closed = true }

// fakeDisgoConn stands in for one disgo voice connection. openErr, when set,
// is what Open returns: Discord not completing the join in time.
type fakeDisgoConn struct {
	disgovoice.Conn
	openErr  error
	opened   bool
	channel  snowflake.ID
	selfDeaf bool
	closed   bool
}

func (c *fakeDisgoConn) Open(_ context.Context, channelID snowflake.ID, _, selfDeaf bool) error {
	c.opened, c.channel, c.selfDeaf = true, channelID, selfDeaf
	return c.openErr
}

func (c *fakeDisgoConn) Close(context.Context) { c.closed = true }

// recorderSession is the production session over a fake manager, its
// discordgo session built and never opened.
func recorderSession(t *testing.T, m *fakeManager) *discordgoSession {
	t.Helper()
	dg, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatalf("discordgo.New: %v", err)
	}
	return &discordgoSession{dg: dg, userID: "300", voice: m}
}

// The recorder's voice state and voice server, as discordgo delivers them,
// reach disgo, which needs both to connect.
func TestRecorderVoiceEventsReachDisgo(t *testing.T) {
	m := &fakeManager{}
	s := recorderSession(t, m)

	s.onVoiceStateUpdate(&discordgo.VoiceStateUpdate{VoiceState: &discordgo.VoiceState{
		GuildID: "100", ChannelID: "200", UserID: "300", SessionID: "sess-1",
	}})
	s.onVoiceServerUpdate(&discordgo.VoiceServerUpdate{Token: "tok", GuildID: "100", Endpoint: "c-dfw.discord.media:443"})

	if len(m.states) != 1 || m.states[0].UserID != snowflake.ID(300) || m.states[0].SessionID != "sess-1" {
		t.Errorf("voice states handed to disgo = %+v, want the recorder's", m.states)
	}
	if len(m.servers) != 1 || m.servers[0].Token != "tok" {
		t.Errorf("voice servers handed to disgo = %+v, want the one Discord sent", m.servers)
	}
}

// A join opens the connection to the channel given without deafening the
// recorder, which Discord would then send no audio, and leaving closes it.
func TestRecorderJoinOpensUndeafenedAndLeaveCloses(t *testing.T) {
	m := &fakeManager{conn: &fakeDisgoConn{}}
	s := recorderSession(t, m)

	conn, err := s.Join(context.Background(), "100", "200")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}

	if !m.conn.opened || m.conn.channel != snowflake.ID(200) {
		t.Errorf("connection opened = %v to %d, want opened to 200", m.conn.opened, m.conn.channel)
	}
	if m.conn.selfDeaf {
		t.Error("the recorder joined deafened")
	}
	conn.Leave(context.Background())
	if !m.conn.closed {
		t.Error("the connection is still open after Leave")
	}
}

// A join Discord doesn't complete in time returns the error, and the
// connection is closed, since disgo leaves the join it sent standing.
func TestRecorderJoinThatTimesOutLeavesAgain(t *testing.T) {
	m := &fakeManager{conn: &fakeDisgoConn{openErr: context.DeadlineExceeded}}
	s := recorderSession(t, m)

	_, err := s.Join(context.Background(), "100", "200")

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Join error = %v, want the deadline", err)
	}
	if !m.conn.closed {
		t.Error("the connection is still open after the failed join")
	}
}

// A voice state update sent while the recorder's gateway connection is down
// comes back as an error. disgo sends one from its own goroutine when it
// reconnects, where a panic would end the process.
func TestRecorderVoiceStateUpdateWithTheGatewayDownIsAnError(t *testing.T) {
	s := recorderSession(t, &fakeManager{})
	channelID := snowflake.ID(200)

	if err := s.updateVoiceState(context.Background(), snowflake.ID(100), &channelID, true, false); err == nil {
		t.Error("updateVoiceState with no gateway connection = nil, want an error")
	}
}

// Closing the session at shutdown takes the recorder out of its voice
// channels.
func TestRecorderCloseLeavesVoice(t *testing.T) {
	m := &fakeManager{}
	s := recorderSession(t, m)

	_ = s.Close()

	if !m.closed {
		t.Error("the recorder's voice connections are still open after Close")
	}
}
