// Package metrics is the Discord activity the Metrics page shows (#949): the
// rows the bot store keeps, the sections they count for, and the report
// built from them. It makes no Discord or database call.
package metrics

import "time"

const (
	RawRetention = 180 * 24 * time.Hour
	BackfillSpan = 12 * 7 * 24 * time.Hour
)

// WeekStart is Monday 00:00 UTC of the week t falls in.
func WeekStart(t time.Time) time.Time {
	u := t.UTC()
	day := time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
}

type Message struct {
	ID         string
	UserID     string
	CategoryID string // "" when the channel has no category
	SentAt     time.Time
	Addressed  []string
}

// VoicePresence is a member counted in voice. ChannelID "" means not counted
// (left, AFK channel, self-deafened).
type VoicePresence struct {
	UserID     string
	ChannelID  string
	CategoryID string
	RoleIDs    []string
}

type MemberEventKind string

const (
	MemberJoined MemberEventKind = "join"
	MemberLeft   MemberEventKind = "leave"
)

type MemberEvent struct {
	UserID string
	Kind   MemberEventKind
	At     time.Time
}

// State is when collection started and when the backfill finished, zero for not yet.
type State struct{ StartedAt, BackfilledAt time.Time }

type Section struct {
	CategoryID string
	Name       string
	RoleIDs    []string // empty: voice counts in the category's own channels
}

// WeekTotal is one section's week; CategoryID "" is the whole server.
type WeekTotal struct {
	Week         time.Time
	CategoryID   string
	Messages     int
	VoiceSeconds int64
	VoicePeople  int
	Active       int
}

type ActiveDay struct {
	UserID string
	Day    time.Time // 00:00 UTC
}

type Span struct{ Start, End time.Time }

// FirstReply is one join with its first message and first reply, zero for none.
type FirstReply struct {
	UserID                              string
	JoinedAt, FirstMessageAt, RepliedAt time.Time
}
