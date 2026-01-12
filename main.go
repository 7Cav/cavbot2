package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/7cav/cavbot2/utils"

	"github.com/7cav/cavbot2/commands"
	"github.com/bwmarrin/discordgo"
)

var Version = "0.6.1"

var (
	Token    string
	GuildID  string
	LogLevel string
	BMToken  string
)

func init() {
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

func main() {
	utils.Info("CavBot2 starting", "version", Version)
	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		panic(fmt.Sprintf("Error creating Discord session: %v", err))
	}

	registry := commands.NewRegistry()

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
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

	err = dg.Open()
	if err != nil {
		panic(fmt.Sprintf("Error opening connection: %v", err))
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

	utils.Info("Bot is now running. Press CTRL-C to exit")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
	utils.Info("Shutting down")
}
