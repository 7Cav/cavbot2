// Package commands stands in for cavbot2's commands, with the shapes of the
// values it hands the panel.
package commands

import (
	"errors"
	"time"

	"github.com/bwmarrin/discordgo"
)

// ReportMember is a member a Foxhole action reached. The Foxhole page lists
// a failed member with their failure reason.
type ReportMember struct {
	ID      string `json:"id"`
	Failure string `json:"failure,omitempty"`
}

// failureReason is why Discord refused a member's role change, chosen by
// classifying the error.
func failureReason(err error) string {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == 403 {
		return "Discord refused: the bot needs Manage Roles"
	}
	return "Discord rejected the change"
}

func recordsAClassifiedFailure(member ReportMember, err error) ReportMember {
	member.Failure = failureReason(err)
	return member
}

func recordsTheErrorAsTheFailure(member ReportMember, err error) ReportMember {
	member.Failure = "Discord refused: " + err.Error() // want "panel"
	return member
}

// SpawnFailureCause says why a hub's last spawn failed.
type SpawnFailureCause string

const SpawnFailureRateLimited SpawnFailureCause = "rate limited"

// SpawnFailure is a hub's last failed spawn. The hubs page shows it.
type SpawnFailure struct {
	At    time.Time
	Cause SpawnFailureCause
}

func recordsAClassifiedCause() SpawnFailure {
	return SpawnFailure{At: time.Now(), Cause: SpawnFailureRateLimited}
}

func recordsTheErrorAsTheCause(err error) SpawnFailure {
	return SpawnFailure{At: time.Now(), Cause: SpawnFailureCause(err.Error())} // want "panel"
}
