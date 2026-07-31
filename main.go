package main

import (
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/7cav/cavbot2/utils"

	_ "github.com/go-sql-driver/mysql"

	"github.com/7cav/cavbot2/commands"
	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)


var Version = "dev"



var (
	Token    string
	GuildID  string
	LogLevel string
	BMToken  string
)

func init() {
	// Load .env before the reads below so `go run .` works from a filled-in
	// .env alone. godotenv.Load never overwrites a variable already in the
	// environment, so Docker and CI — which inject env directly and ship no
	// .env — behave exactly as they did before. A missing file is the normal
	// case there and not an error; anything else means the file exists but
	// could not be read, which is worth failing on rather than starting with
	// half the configuration silently missing.
	//
	// This must stay in init(), not main(): the panics below run first.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		panic(fmt.Sprintf("Found .env but could not load it: %v", err))
	}

	Token = os.Getenv("DISCORD_TOKEN")
	GuildID = os.Getenv("GUILD_ID")
	LogLevel = os.Getenv("LOG_LEVEL")
	BMToken = os.Getenv("BM_TOKEN")

	if Token == "" {
		panic("No token provided. Please set DISCORD_TOKEN environment variable")
	}
	if GuildID == "" {
		panic("No GuildID provided. Please set GUILD_ID environment variable")
	}
	if BMToken == "" {
		panic("No BM_TOKEN provided. Please set BM_TOKEN environment variable")
	}
	if LogLevel == "" {
		LogLevel = "default"
	}

	utils.InitLogger(LogLevel)
}

func initLOACache() {
	dsn := os.Getenv("FORUM_DB_DSN")
	if dsn == "" {
		utils.Warn("FORUM_DB_DSN not set, LOA cache disabled")
		return
	}

	nodeIDs := []int{180}
	if s := os.Getenv("LOA_NODE_IDS"); s != "" {
		nodeIDs = nil
		for _, part := range strings.Split(s, ",") {
			if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				nodeIDs = append(nodeIDs, id)
			}
		}
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		utils.Warn("Failed to open forum DB connection, LOA cache disabled", "error", err)
		return
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxIdleTime(30 * time.Second)

	utils.GlobalLOACache.Refresh(db, nodeIDs)

	go func() {
		defer utils.RecoverPanic("loa-refresh")
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			utils.GlobalLOACache.Refresh(db, nodeIDs)
		}
	}()
}

func main() {
	defer utils.InitSentry(Version)()

	utils.Info("CavBot2 starting", "version", Version)
	initLOACache()

	// Route discordgo's own logging through slog before the session exists, so
	// nothing it emits escapes to the stdlib logger.
	dgLogLevel := utils.InitDiscordgoLogging()

	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		panic(fmt.Sprintf("Error creating Discord session: %v", err))
	}
	dg.LogLevel = dgLogLevel
	// IntentsGuildMembers is a Privileged Gateway Intent — must be toggled on
	// in the Discord Developer Portal for this bot application, otherwise
	// dg.Open() fails at runtime with no compile-time signal.
	dg.Identify.Intents = discordgo.IntentsAllWithoutPrivileged | discordgo.IntentsGuildMembers

	registry := commands.NewRegistry()

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		defer utils.RecoverPanic("interaction-handler")
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if h, ok := registry.GetHandler(i.ApplicationCommandData().Name); ok {
				h(s, i)
			}
		case discordgo.InteractionMessageComponent:
			customID := i.MessageComponentData().CustomID
			parts := strings.Split(customID, "::")
			if len(parts) > 0 {
				if h, ok := registry.GetHandler(parts[0]); ok {
					h(s, i)
				}
			}
		}
	})

	// Not dg.Open() directly: a session can open without ever reaching READY,
	// which leaves dg.State.User nil for the command registration below.
	if err := utils.OpenSession(dg); err != nil {
		panic(fmt.Sprintf("Discord session unavailable: %v", err))
	}
	defer func() {
		err := dg.Close()
		if err != nil {
			panic(fmt.Sprintf("Error closing Discord connection: %v", err))
		}
	}()
	registeredCommandNames := make(map[string]struct{}, len(registry.GetCommands()))
	for _, cmd := range registry.GetCommands() {
		registeredCommandNames[cmd.Name] = struct{}{}
	}
	utils.Info("Removing deprecated commands")
	existingCommands, err := dg.ApplicationCommands(dg.State.User.ID, GuildID)
	if err != nil {
		utils.Warn("Warning: Could not fetch existing commands:", "error", err)
	} else {
		for _, cmd := range existingCommands {
			if _, exists := registeredCommandNames[cmd.Name]; !exists {
				err := dg.ApplicationCommandDelete(dg.State.User.ID, GuildID, cmd.ID)
				if err != nil {
					utils.Warn("Warning: Could not delete deprecated command", "command", cmd.Name, "error", err)
				} else {
					utils.Info("Removed deprecated command", "command", cmd.Name)
				}
			}
		}
	}

	utils.Info("Registering commands")
	registeredCommands := make([]*discordgo.ApplicationCommand, len(registry.GetCommands()))

	for i, cmd := range registry.GetCommands() {
		rcmd, err := dg.ApplicationCommandCreate(dg.State.User.ID, GuildID, cmd)
		if err != nil {
			panic(fmt.Sprintf("Cannot create command %v: %v", cmd.Name, err))
		}
		registeredCommands[i] = rcmd
	}

	commands.StartJoinerReportScheduler(dg, GuildID)

	utils.Info("Bot is now running. Press CTRL-C to exit")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
	utils.Info("Shutting down")
}
