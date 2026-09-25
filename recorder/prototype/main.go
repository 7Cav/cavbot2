// Command prototype is a THROWAWAY prototype for issue #10. It is not part of
// the bot and nothing imports it. See README.md for the question it answers,
// how to run it and how to read what it prints.
//
// It keeps bwmarrin/discordgo for the main gateway, hands the voice events to
// disgo's voice package, decrypts DAVE with thomas-vilte/dave-go, and writes
// one Ogg Opus track per speaker.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/joho/godotenv"
	"github.com/thomas-vilte/dave-go/session"
)

func main() {
	envFile := flag.String("env", "../../.env", "dotenv file holding DISCORD_TOKEN and GUILD_ID; variables already set in the environment win")
	channelFlag := flag.String("channel", "", "ID of the voice channel to record (required)")
	outDir := flag.String("out", "out", "where the PROTOTYPE recordings go")
	keepalive := flag.Duration("keepalive", 5*time.Second, "UDP keepalive interval; 0 turns it off")
	statusEvery := flag.Duration("status", 10*time.Second, "how often to print the state table")
	duration := flag.Duration("duration", 0, "stop after this long; 0 runs until Ctrl-C")
	prime := flag.Bool("prime", false, "send five silence frames after joining, in case Discord withholds audio from a bot that never sent any")
	debug := flag.Bool("debug", false, "log disgo and dave-go at debug level")
	flag.Parse()

	level := slog.LevelInfo
	daveLevel := slog.LevelWarn
	if *debug {
		level, daveLevel = slog.LevelDebug, slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	daveLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: daveLevel}))

	if err := godotenv.Load(*envFile); err != nil && !os.IsNotExist(err) {
		fail("reading %s: %v", *envFile, err)
	}
	token, guildStr := os.Getenv("DISCORD_TOKEN"), os.Getenv("GUILD_ID")
	if token == "" || guildStr == "" {
		fail("DISCORD_TOKEN and GUILD_ID must be set (looked in the environment and %s)", *envFile)
	}
	guildID, err := snowflake.Parse(guildStr)
	if err != nil {
		fail("GUILD_ID: %v", err)
	}
	channelID, err := snowflake.Parse(*channelFlag)
	if err != nil {
		fail("-channel must be a voice channel ID: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	st := newStats()
	rec, err := newRecorder(*outDir, st)
	if err != nil {
		fail("creating recording directory: %v", err)
	}
	st.printf("recording to %s", rec.dir)

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		fail("discordgo: %v", err)
	}
	dg.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildVoiceStates
	ready := make(chan *discordgo.Ready, 1)
	dg.AddHandlerOnce(func(_ *discordgo.Session, r *discordgo.Ready) { ready <- r })
	if err := dg.Open(); err != nil {
		fail("opening the Discord gateway: %v", err)
	}
	defer func() { _ = dg.Close() }()

	var botID snowflake.ID
	select {
	case r := <-ready:
		botID = snowflake.MustParse(r.User.ID)
		st.printf("gateway ready as %s", r.User.Username)
	case <-time.After(30 * time.Second):
		fail("no READY from the gateway within 30s")
	}
	st.names = newNameBook(dg, guildStr)

	// disgo's voice package never touches a gateway itself. It asks for a
	// voice state update through this function, and discordgo's
	// ChannelVoiceJoinManual sends exactly that op 4 without creating a
	// discordgo VoiceConnection, so discordgo's own voice code stays idle.
	stateUpdate := func(_ context.Context, g snowflake.ID, ch *snowflake.ID, selfMute, selfDeaf bool) error {
		cid := ""
		if ch != nil {
			cid = ch.String()
		}
		return dg.ChannelVoiceJoinManual(g.String(), cid, selfMute, selfDeaf)
	}

	mgr := voice.NewManager(stateUpdate, botID,
		voice.WithDaveSessionCreateFunc(countingDaveSessions(
			session.CreateFunc(session.WithSessionHook(st.setDave)), st)),
		voice.WithDaveSessionLogger(daveLogger),
		voice.WithConnConfigOpts(
			voice.WithUDPConnCreateFunc(keepaliveUDPConns(*keepalive, st)),
			voice.WithConnAudioReceiverCreateFunc(rec.newReceiver),
			voice.WithConnEventHandlerFunc(st.onVoiceEvent),
		),
	)

	// Hand discordgo's voice events to disgo, translated to disgo's types.
	left := make(chan struct{}, 1)
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.VoiceStateUpdate) {
		st.names.learn(e.Member)
		ev, ok := toDisgoVoiceState(e)
		if !ok {
			return
		}
		if ev.UserID == botID && ev.GuildID == guildID && ev.ChannelID == nil {
			select {
			case left <- struct{}{}:
			default:
			}
		}
		mgr.HandleVoiceStateUpdate(ev)
	})
	dg.AddHandler(func(_ *discordgo.Session, e *discordgo.VoiceServerUpdate) {
		ev, ok := toDisgoVoiceServer(e)
		if ok {
			mgr.HandleVoiceServerUpdate(ev)
		}
	})

	conn := mgr.CreateConn(guildID)
	openCtx, cancelOpen := context.WithTimeout(ctx, 30*time.Second)
	// Self-mute unless priming: the recorder never speaks. Never self-deafen,
	// or Discord sends no audio at all.
	err = conn.Open(openCtx, channelID, !*prime, false)
	cancelOpen()
	if err != nil {
		fail("joining voice channel %s: %v", channelID, err)
	}
	st.printf("joined voice channel %s", channelID)

	// Only after Open: disgo's UDP socket does not exist before it, and a
	// receiver started earlier panics on a nil conn (disgo #595).
	conn.SetOpusFrameReceiver(rec)
	if *prime {
		conn.SetOpusFrameProvider(&silenceProvider{left: 5})
	}

	ticker := time.NewTicker(*statusEvery)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-left:
			st.printf("the bot is no longer in the channel, stopping")
			break loop
		case <-ticker.C:
			rec.flush()
			st.printStatus(os.Stdout)
		}
	}

	st.printf("stopping")
	rec.stopReceiving()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	conn.Close(closeCtx)
	cancelClose()
	if err := rec.finish(); err != nil {
		st.printf("closing tracks: %v", err)
	}
	if err := st.writeSummary(rec.dir); err != nil {
		st.printf("writing summary: %v", err)
	}
	st.printStatus(os.Stdout)
	st.printBursts(os.Stdout)
	st.printf("recording and summary.txt are in %s", rec.dir)
}

func toDisgoVoiceState(e *discordgo.VoiceStateUpdate) (gateway.EventVoiceStateUpdate, bool) {
	guildID, err1 := snowflake.Parse(e.GuildID)
	userID, err2 := snowflake.Parse(e.UserID)
	if err1 != nil || err2 != nil {
		return gateway.EventVoiceStateUpdate{}, false
	}
	var channelID *snowflake.ID
	if e.ChannelID != "" {
		id, err := snowflake.Parse(e.ChannelID)
		if err != nil {
			return gateway.EventVoiceStateUpdate{}, false
		}
		channelID = &id
	}
	return gateway.EventVoiceStateUpdate{VoiceState: discord.VoiceState{
		GuildID:   guildID,
		ChannelID: channelID,
		UserID:    userID,
		SessionID: e.SessionID,
		GuildDeaf: e.Deaf,
		GuildMute: e.Mute,
		SelfDeaf:  e.SelfDeaf,
		SelfMute:  e.SelfMute,
	}}, true
}

func toDisgoVoiceServer(e *discordgo.VoiceServerUpdate) (gateway.EventVoiceServerUpdate, bool) {
	guildID, err := snowflake.Parse(e.GuildID)
	if err != nil {
		return gateway.EventVoiceServerUpdate{}, false
	}
	var endpoint *string
	if e.Endpoint != "" {
		endpoint = &e.Endpoint
	}
	return gateway.EventVoiceServerUpdate{Token: e.Token, GuildID: guildID, Endpoint: endpoint}, true
}

// silenceProvider feeds disgo's audio sender a few silence frames, then
// nothing. It exists only for the -prime flag.
type silenceProvider struct{ left int }

func (p *silenceProvider) ProvideOpusFrame() ([]byte, error) {
	if p.left <= 0 {
		return nil, nil
	}
	p.left--
	return voice.SilenceAudioFrame, nil
}

func (p *silenceProvider) Close() {}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "prototype: "+format+"\n", args...)
	os.Exit(1)
}
