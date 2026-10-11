package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The Recordings page (spec #381, #390): the recordings a signed-in forum
// user started. The panel knows them by the Discord ID the user's milpac
// carried at sign-in, found by their forum username.

// recordingsPath is the Recordings page's address.
const recordingsPath = "/recordings"

// milpacTimeout bounds the 7Cav API's milpac lookup at sign-in.
const milpacTimeout = 10 * time.Second

// discordIDOf is the Discord ID the milpac of the forum user with the
// username given carries, found through the 7Cav API. Empty when they have
// no milpac, their milpac carries none, or the lookup failed: a sign-in
// goes on either way, with no recordings of the user's own. A failed lookup
// reaches Sentry. A missing milpac is an INFO line, since any forum user can
// sign in and most have none.
func (p *Panel) discordIDOf(ctx context.Context, username string) string {
	ctx, cancel := panelClock.WithTimeout(ctx, milpacTimeout)
	defer cancel()
	profile, err := utils.GetMilpacByUsername(ctx, username)
	switch {
	case errors.Is(err, utils.ErrNotFound):
		utils.Info("Panel sign-in found no milpac", "username", username)
		return ""
	case err != nil:
		utils.CaptureError("Panel milpac lookup failed at sign-in", err, "username", username)
		return ""
	}
	return profile.DiscordID
}

// The Recordings page's switch: its query parameter, and the value that
// lists every recording.
const (
	paramView = "view"
	viewAll   = "all"
)

// recordingsView is the Recordings page's data: the recordings it lists,
// the newest first.
type recordingsView struct {
	Recordings []recordingItem
	// Switch is set for a panel admin, whose page switches between their
	// own recordings and every recording.
	Switch bool
	// All is set when the page lists every recording, each with its
	// starter.
	All bool
}

// recordingItem is one recording as the page shows it.
type recordingItem struct {
	ID        int64
	Title     string
	Channel   string
	StartedAt time.Time
	// Length is how long it ran, empty while it runs.
	Length   string
	Speakers []string
	// Running is set while the recording runs.
	Running bool
	Mix     store.MixState
	// StarterID is the starter's Discord ID, and Starter their display
	// name, for the list of every recording.
	StarterID string
	Starter   string
}

// recordingsPage is GET /recordings: the recordings the user started. A
// user who started none and isn't a panel admin gets the no-access page. A
// panel admin's switch lists every recording instead, with its starter;
// the switch's address shows anyone else their own.
func (p *Panel) recordingsPage(w http.ResponseWriter, r *http.Request, sess session) {
	ctx, cancel := panelClock.WithTimeout(r.Context(), hubPageBudget)
	defer cancel()
	all := sess.access.panelAdmin && r.URL.Query().Get(paramView) == viewAll
	var recs []store.Recording
	var err error
	if all {
		recs, err = p.recordings.All(ctx)
	} else {
		recs, err = p.ownRecordings(ctx, sess)
	}
	if errors.Is(err, context.Canceled) {
		utils.Info("Panel page abandoned", "step", "recordings page", "username", sess.username, "forum_user_id", sess.userID)
		return
	}
	if err != nil {
		p.pageFailed(w, sess, "recordings page", recordingsPath, err, nil)
		return
	}
	if !sess.access.panelAdmin && len(recs) == 0 {
		// Someone who is neither a panel admin nor the starter of a
		// recording has nothing here.
		p.render(w, http.StatusForbidden, "noaccess", sess.page("No access"))
		return
	}
	data := sess.page("Recordings")
	data.Page = pageRecordings
	data.Nav.Recordings = true
	view := recordingsView{Recordings: recordingItems(recs), Switch: sess.access.panelAdmin, All: all}
	if all {
		members := p.hubs.deps.Manager.MemberList(p.hubs.deps.GuildID)
		for i := range view.Recordings {
			view.Recordings[i].Starter = members.DisplayName(view.Recordings[i].StarterID)
		}
	}
	data.Recordings = view
	p.render(w, http.StatusOK, "recordings", data)
}

// ownRecordings lists the recordings the session's user started, none when
// the panel found no Discord ID for them at sign-in.
func (p *Panel) ownRecordings(ctx context.Context, sess session) ([]store.Recording, error) {
	if sess.discordID == "" {
		return nil, nil
	}
	return p.recordings.StartedBy(ctx, sess.discordID)
}

// recordingItems is each recording as the page shows it, in the order given.
func recordingItems(recs []store.Recording) []recordingItem {
	out := make([]recordingItem, 0, len(recs))
	for _, rec := range recs {
		item := recordingItem{ID: rec.ID, Title: rec.Title, Channel: rec.ChannelName, StartedAt: rec.StartedAt,
			Running: rec.StoppedAt.IsZero(), Mix: rec.Mix, StarterID: rec.StarterID}
		if !item.Running {
			item.Length = recordingLength(rec.StoppedAt.Sub(rec.StartedAt))
		}
		for _, sp := range rec.Speakers {
			item.Speakers = append(item.Speakers, sp.DisplayName)
		}
		out = append(out, item)
	}
	return out
}

// recordingLength says how long a recording ran, to the second under an
// hour and to the minute from an hour up.
func recordingLength(d time.Duration) string {
	switch d = d.Round(time.Second); {
	case d >= time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}
