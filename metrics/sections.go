package metrics

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// companyName is a company role's name, such as A/1-7. Platoon and HQ roles
// (1/A/1-7, A/1-7 HQ) don't match.
var companyName = regexp.MustCompile(`^[A-Z]+/[A-Z0-9]+-7$`)

// departments maps a role code to the fragment of its category's name. A
// department's roles are named "<code> - ...". A new department category
// needs a row here.
var departments = []struct{ code, fragment string }{
	{"S1", "S1 Department"},
	{"S3", "S3 Department"},
	{"S5", "S5 Department"},
	{"S6", "S6 Department"},
	{"S7", "S7 Department"},
	{"RRD", "Recruiting Department"},
	{"RTC", "Recruit Training Command"},
}

// Sections returns the sections in Discord's category order.
func Sections(channels []*discordgo.Channel, roles []*discordgo.Role) []Section {
	filled := map[string]bool{}
	for _, ch := range channels {
		if !ch.IsThread() && ch.ParentID != "" {
			filled[ch.ParentID] = true
		}
	}
	var categories []*discordgo.Channel
	for _, ch := range channels {
		if ch.Type == discordgo.ChannelTypeGuildCategory && filled[ch.ID] {
			categories = append(categories, ch)
		}
	}
	slices.SortFunc(categories, func(a, b *discordgo.Channel) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(snowflake(a.ID), snowflake(b.ID)))
	})
	sections := make([]Section, 0, len(categories))
	for _, cat := range categories {
		name := strings.TrimSpace(strings.ReplaceAll(cat.Name, "▬", ""))
		sections = append(sections, Section{CategoryID: cat.ID, Name: name, RoleIDs: sectionRoles(name, roles)})
	}
	return sections
}

// sectionRoles is the company role or the department roles a section's voice
// counts by, or nil when it counts its own channels.
func sectionRoles(name string, roles []*discordgo.Role) []string {
	if companyName.MatchString(name) {
		for _, r := range roles {
			if r.Name == name {
				return []string{r.ID}
			}
		}
		return nil
	}
	for _, d := range departments {
		if !strings.Contains(name, d.fragment) {
			continue
		}
		var ids []string
		for _, r := range roles {
			if strings.HasPrefix(r.Name, d.code+" - ") {
				ids = append(ids, r.ID)
			}
		}
		return ids
	}
	return nil
}

// snowflake is a Discord ID as a number, so IDs of different lengths sort
// by age.
func snowflake(id string) uint64 {
	n, _ := strconv.ParseUint(id, 10, 64)
	return n
}
