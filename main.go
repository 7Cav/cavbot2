package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/7cav/cavbot2/commands"
	"github.com/bwmarrin/discordgo"
)

var Version = "0.3.2"

var (
	Token   string
	GuildID string
)

func init() {
	Token = os.Getenv("DISCORD_TOKEN")
	GuildID = os.Getenv("GUILD_ID")

	if Token == "" {
		log.Fatal("No token provided. Please set DISCORD_TOKEN environment variable")
	}
	if GuildID == "" {
		log.Fatal("No GuildID provided. Please set GUILD_ID environment variable")
	}
}

func main() {
	log.Printf("CavBot2 v%s starting...", Version)
	dg, err := discordgo.New("Bot " + Token)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
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
		log.Fatalf("Error opening connection: %v", err)
	}
	defer func() {
		err := dg.Close()
		if err != nil {
			log.Printf("Error closing Discord connection: %v", err)
		}
	}()
	registeredCommandNames := make(map[string]struct{}, len(registry.GetCommands()))
	for _, cmd := range registry.GetCommands() {
		registeredCommandNames[cmd.Name] = struct{}{}
	}
	log.Println("Removing deprecated commands...")
	existingCommands, err := dg.ApplicationCommands(dg.State.User.ID, GuildID)
	if err != nil {
		log.Printf("Warning: Could not fetch existing commands: %v", err)
	} else {
		for _, cmd := range existingCommands {
			if _, exists := registeredCommandNames[cmd.Name]; !exists {
				err := dg.ApplicationCommandDelete(dg.State.User.ID, GuildID, cmd.ID)
				if err != nil {
					log.Printf("Warning: Could not delete deprecated command %s: %v", cmd.Name, err)
				} else {
					log.Printf("Removed deprecated command: %s", cmd.Name)
				}
			}
		}
	}

	log.Println("Registering commands...")
	registeredCommands := make([]*discordgo.ApplicationCommand, len(registry.GetCommands()))

	for i, cmd := range registry.GetCommands() {
		rcmd, err := dg.ApplicationCommandCreate(dg.State.User.ID, GuildID, cmd)
		if err != nil {
			log.Panicf("Cannot create command %v: %v", cmd.Name, err)
		}
		registeredCommands[i] = rcmd
	}

	fmt.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
	log.Println("Shutting down...")
}
