package commands

import (
    "fmt"
    "strings"
    "github.com/7cav/cavbot2/utils"
    "github.com/bwmarrin/discordgo"
)

const ROLE_NAME string = "Warden Verified"
const ROLE_NAME_ADMIN string = "Warden Admin"

func Warden() Command {
    var required int64 = discordgo.PermissionManageRoles
    return Command{
        Definition: &discordgo.ApplicationCommand{
            Name:        "warden",
            Description: "Warden role management",
            DefaultMemberPermissions: &required,
            Options: []*discordgo.ApplicationCommandOption{
                {
                    Type: discordgo.ApplicationCommandOptionSubCommand,
                    Name: "add",
                    Description: "Add '" + ROLE_NAME + "' role to a user",
                    Options: []*discordgo.ApplicationCommandOption{
                        {
                            Type:        discordgo.ApplicationCommandOptionString,
                            Name:        "discordname",
                            Description: "Discord username or nickname to match (partial allowed)",
                            Required:    true,
                        },
                    },
                },
                {
                    Type: discordgo.ApplicationCommandOptionSubCommand,
                    Name: "remove",
                    Description: "Remove '" + ROLE_NAME + "' role from a user",
                    Options: []*discordgo.ApplicationCommandOption{
                        {
                            Type:        discordgo.ApplicationCommandOptionString,
                            Name:        "discordname",
                            Description: "Discord username or nickname to match (partial allowed)",
                            Required:    true,
                        },
                    },
                },
                {
                    Type: discordgo.ApplicationCommandOptionSubCommand,
                    Name: "bulkadd",
                    Description: "Add '" + ROLE_NAME + "' role to a list of users",
                    Options: []*discordgo.ApplicationCommandOption{
                        {
                            Type:        discordgo.ApplicationCommandOptionString,
                            Name:        "userlist",
                            Description: "Comma-separated list of Discord usernames or nicknames to match (partial allowed)",
                            Required:    true,
                        },
                    },
                },
            },
        },
        Handler: handleWarden,
    }
}

func handleWarden(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
    data := interaction.ApplicationCommandData()
    if len(data.Options) == 0 {
        utils.HandleError(session, interaction, "❌ Invalid warden command")
        return
    }

    sub := data.Options[0]

    if len(sub.Options) == 0 {
        utils.HandleError(session, interaction, "❌ Missing discordname argument")
        return
    }

    query := sub.Options[0].StringValue()
    guildID := interaction.GuildID

    if guildID == "" {
        utils.HandleError(session, interaction, "❌ GUILD_ID not configured")
        return
    }

    if !checkIfRequestedUserHasPermission(session, interaction, guildID) {
        return
    }

    switch sub.Name {
        case "add":
            handleAddCommand(session, interaction, sub, guildID, query)
        case "bulkadd":
            handleBulkAddCommand(session, interaction, sub, guildID, query)
        case "remove":
            handleRemoveCommand(session, interaction, sub, guildID, query)
        default:
            utils.HandleError(session, interaction, "❌ Unknown subcommand")
    }
}

func handleAddCommand(
    session *discordgo.Session,
    interaction *discordgo.InteractionCreate,
    sub *discordgo.ApplicationCommandInteractionDataOption,
    guildID string,
    query string,
) {
    member := retrieveMemberByName(session, guildID, query)
    if member == nil {
        return
    }

    roleID := findRoleIDByName(session, interaction, guildID, ROLE_NAME)
    if roleID == "" {
        utils.HandleError(session, interaction, "❌ 'Warden Verified' role not found in guild")
        return
    }

    addRoleForQuery(session, interaction, guildID, query, roleID)

    err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseChannelMessageWithSource,
        Data: &discordgo.InteractionResponseData{
            Content: fmt.Sprintf("✅ Added '%s' role to %s#%s", ROLE_NAME,member.User.Username, member.User.Discriminator),
            Flags:   discordgo.MessageFlagsEphemeral,
        },
    })
    if err != nil {
        utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to send confirmation: %v", err))
        return
    }
    utils.Info("Warden role assigned", "user", member.User.ID)
}

func handleRemoveCommand(
    session *discordgo.Session,
    interaction *discordgo.InteractionCreate,
    sub *discordgo.ApplicationCommandInteractionDataOption,
    guildID string,
    query string,
) {
    member := retrieveMemberByName(session, guildID, query)
    if member == nil {
        return
    }

    roleID := findRoleIDByName(session, interaction, guildID, ROLE_NAME)
    if roleID == "" {
        utils.HandleError(session, interaction, "❌ 'Warden Verified' role not found in guild")
        return
    }

    err := session.GuildMemberRoleRemove(guildID, member.User.ID, roleID)
    if err != nil {
        utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to remove role: %v", err))
        return
    }

    err = session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseChannelMessageWithSource,
        Data: &discordgo.InteractionResponseData{
            Content: fmt.Sprintf("✅ Removed '%s' role from %s#%s", ROLE_NAME, member.User.Username, member.User.Discriminator),
            Flags:   discordgo.MessageFlagsEphemeral,
        },
    })
    if err != nil {
        utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to send confirmation: %v", err))
        return
    }
    utils.Info("Warden role removed", "user", member.User.ID)
}

func handleBulkAddCommand(
    session *discordgo.Session,
    interaction *discordgo.InteractionCreate,
    sub *discordgo.ApplicationCommandInteractionDataOption,
    guildID string,
    query string,
) {
    queries := strings.Split(query, ",")

    roleID := findRoleIDByName(session, interaction, guildID, ROLE_NAME)
    if roleID == "" {
        return
    }

    err := session.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{
        Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
    })

    if err != nil {
        utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to acknowledge bulk add: %v", err))
        return
    }

    var results []string
    for _, q := range queries {
        q = strings.TrimSpace(q)
        utils.Info("Processing bulk add", "query", q)
        if q == "" {
            continue
        }
        results = append(results, addRoleForQuery(session, interaction, guildID, q, roleID))
    }

    content := strings.Join(results, "\n")
    _, err = session.InteractionResponseEdit(interaction.Interaction, &discordgo.WebhookEdit{
        Content: &content,
    })
    if err != nil {
        utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to send bulk add summary: %v", err))
        return
    }
}

func addRoleForQuery(
    session *discordgo.Session,
    interaction *discordgo.InteractionCreate,
    guildID string,
    query string,
    roleID string,
) string {
    member := retrieveMemberByName(session, guildID, query)
    if member == nil {
        return fmt.Sprintf("❌ No member found matching '%s'", query)
    }

    err := session.GuildMemberRoleAdd(guildID, member.User.ID, roleID)
    if err != nil {
        utils.Error("Failed to add role in bulk", "user", member.User.ID, "error", err)
        return fmt.Sprintf("❌ Failed to add role to %s#%s: %v", member.User.Username, member.User.Discriminator, err)
    }

    utils.Info("Warden role assigned (bulk)", "user", member.User.ID)
    return fmt.Sprintf("✅ Added '%s' role to %s#%s", ROLE_NAME, member.User.Username, member.User.Discriminator)
}

func retrieveMemberByName(
    session *discordgo.Session,
    guildID string,
    query string,
) (*discordgo.Member) {
    if strings.HasPrefix(query, "<@") && strings.HasSuffix(query, ">") {
        userID := strings.TrimSuffix(strings.TrimPrefix(query, "<@"), ">")
        member, _ := session.GuildMember(guildID, userID)

        if member != nil {
            return member
        }
    }

    members, err := session.GuildMembersSearch(guildID, query, 10)

    if err != nil {
        utils.Error("Failed to search members (silent)", "error", err)
        return nil
    }

    if len(members) != 1 {
        return nil
    }

    return members[0]
}

func findRoleIDByName(
    session *discordgo.Session,
    interaction *discordgo.InteractionCreate,
    guildID string,
    roleName string,
) (string) {
    roles, err := session.GuildRoles(guildID)

    if err != nil {
        utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to retrieve guild roles: %v", err))
        return ""
    }

    for _, role := range roles {
        if role.Name == roleName {
            return role.ID
        }
    }

    return ""
}

func checkIfRequestedUserHasPermission(
    session *discordgo.Session,
    interaction *discordgo.InteractionCreate,
    guildID string,
) bool {
    adminRoleID := findRoleIDByName(session, interaction, guildID, ROLE_NAME_ADMIN)
    if adminRoleID == "" {
        utils.HandleError(session, interaction, "❌ Admin role not found")
        return false
    }

    member, err := session.GuildMember(guildID, interaction.Member.User.ID)
    if err != nil {
        utils.HandleError(session, interaction, fmt.Sprintf("❌ Failed to retrieve your member info: %v", err))
        return false
    }

    for _, rid := range member.Roles {
        if rid == adminRoleID {
            return true
        }
    }

    utils.HandleError(session, interaction, "❌ You do not have permission to use this command")
    return false
}
