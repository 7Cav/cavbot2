package metrics

import (
	"slices"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func category(id, name string, position int) *discordgo.Channel {
	return &discordgo.Channel{ID: id, Name: name, Position: position, Type: discordgo.ChannelTypeGuildCategory}
}

func textIn(id, parentID string) *discordgo.Channel {
	return &discordgo.Channel{ID: id, ParentID: parentID, Type: discordgo.ChannelTypeGuildText}
}

func role(id, name string) *discordgo.Role {
	return &discordgo.Role{ID: id, Name: name}
}

func TestSections(t *testing.T) {
	tests := []struct {
		name     string
		channels []*discordgo.Channel
		roles    []*discordgo.Role
		want     []Section
	}{
		{
			name: "category order is position, then numeric ID",
			channels: []*discordgo.Channel{
				category("300", "Third", 2), textIn("t3", "300"),
				category("1000", "Second", 1), textIn("t2", "1000"),
				category("200", "First", 1), textIn("t1", "200"),
			},
			want: []Section{{CategoryID: "200", Name: "First"}, {CategoryID: "1000", Name: "Second"}, {CategoryID: "300", Name: "Third"}},
		},
		{
			name: "a category holding no channel or only a thread is no section",
			channels: []*discordgo.Channel{
				category("1", "Empty", 0),
				category("2", "Threads", 1),
				{ID: "th", ParentID: "2", Type: discordgo.ChannelTypeGuildPublicThread},
				category("3", "Voice", 2),
				{ID: "vc", ParentID: "3", Type: discordgo.ChannelTypeGuildVoice},
			},
			want: []Section{{CategoryID: "3", Name: "Voice"}},
		},
		{
			name:     "a company takes the role named exactly as it, not platoon or HQ roles",
			channels: []*discordgo.Channel{category("1", "▬ A/1-7 ▬", 0), textIn("t", "1")},
			roles:    []*discordgo.Role{role("plt", "1/A/1-7"), role("hq", "A/1-7 HQ"), role("co", "A/1-7")},
			want:     []Section{{CategoryID: "1", Name: "A/1-7", RoleIDs: []string{"co"}}},
		},
		{
			name: "a category sharing a role's name outside the company pattern keeps its own channels",
			channels: []*discordgo.Channel{
				category("1", "Squad", 0), textIn("t1", "1"),
				category("2", "▬ 🎮 Operations/Training ▬", 1), textIn("t2", "2"),
			},
			roles: []*discordgo.Role{role("game", "Squad"), role("ops", "🎮 Operations/Training")},
			want:  []Section{{CategoryID: "1", Name: "Squad"}, {CategoryID: "2", Name: "🎮 Operations/Training"}},
		},
		{
			name: "a department takes every role named with its code prefix",
			channels: []*discordgo.Channel{
				category("1", "▬ S1 Department ▬", 0), textIn("t1", "1"),
				category("2", "▬ Recruit Training Command ▬", 1), textIn("t2", "2"),
			},
			roles: []*discordgo.Role{
				role("s1d", "S1 - Department"), role("s1hq", "S1 - HQ"), role("s1x", "S1 Clerk"),
				role("s3", "S3 - HQ"), role("rtc", "RTC - Leads/Seniors/HQ"),
			},
			want: []Section{
				{CategoryID: "1", Name: "S1 Department", RoleIDs: []string{"s1d", "s1hq"}},
				{CategoryID: "2", Name: "Recruit Training Command", RoleIDs: []string{"rtc"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Sections(tt.channels, tt.roles)
			same := slices.EqualFunc(got, tt.want, func(a, b Section) bool {
				return a.CategoryID == b.CategoryID && a.Name == b.Name && slices.Equal(a.RoleIDs, b.RoleIDs)
			})
			if !same {
				t.Errorf("Sections = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestWeekStart(t *testing.T) {
	monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"Monday midnight starts its own week", monday, monday},
		{"Sunday's last second belongs to the week before", monday.Add(-time.Second), monday.AddDate(0, 0, -7)},
		{"a time east of UTC counts by its UTC day", time.Date(2026, 10, 5, 1, 0, 0, 0, time.FixedZone("CEST", 2*3600)), monday.AddDate(0, 0, -7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WeekStart(tt.at); !got.Equal(tt.want) {
				t.Errorf("WeekStart(%v) = %v, want %v", tt.at, got, tt.want)
			}
		})
	}
}
