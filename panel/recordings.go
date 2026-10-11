package panel

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/7cav/cavbot2/commands"
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
	// Single is set on one recording's page, the link a stop gives.
	Single bool
	// ShowStarter is set when the page names each recording's starter: in
	// the list of every recording, and on the page of a recording the
	// viewer didn't start.
	ShowStarter bool
}

// recordingItem is one recording as the page shows it.
type recordingItem struct {
	ID        int64
	Title     string
	Channel   string
	StartedAt time.Time
	// Length is how long it ran, empty while it runs.
	Length string
	// DeletesAt is when it is deleted, zero while it runs.
	DeletesAt time.Time
	Speakers  []string
	// Running is set while the recording runs.
	Running bool
	Mix     store.MixState
	// StarterID is the starter's Discord ID, and Starter their display
	// name, for a page that names the starter.
	StarterID string
	Starter   string
	// MixURL and ZipURL are the downloads' addresses.
	MixURL string
	ZipURL string
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
	p.renderRecordings(w, sess, recordingsView{Recordings: recordingItems(recs), Switch: sess.access.panelAdmin, All: all, ShowStarter: all})
}

// renderRecordings renders the Recordings page with the view given, naming
// each recording's starter by their display name when the view shows them.
func (p *Panel) renderRecordings(w http.ResponseWriter, sess session, view recordingsView) {
	if view.ShowStarter {
		members := p.hubs.deps.Manager.MemberList(p.hubs.deps.GuildID)
		for i := range view.Recordings {
			view.Recordings[i].Starter = members.DisplayName(view.Recordings[i].StarterID)
		}
	}
	data := sess.page("Recordings")
	data.Page = pageRecordings
	data.Nav.Recordings = true
	data.Recordings = view
	p.render(w, http.StatusOK, "recordings", data)
}

// recordingPage is GET /recordings/{id}: one recording, the page the link
// a stop gives leads to. Its starter and panel admins open it.
func (p *Panel) recordingPage(w http.ResponseWriter, r *http.Request, sess session) {
	rec, ok := p.recordingFor(w, r, sess, "recording page")
	if !ok {
		return
	}
	p.renderRecordings(w, sess, recordingsView{Recordings: recordingItems([]store.Recording{rec}), Single: true,
		ShowStarter: rec.StarterID != sess.discordID})
}

// downloadMix is GET /recordings/{id}/mix: the recording's mix, streamed
// from disk with no time budget, once it is ready.
func (p *Panel) downloadMix(w http.ResponseWriter, r *http.Request, sess session) {
	rec, ok := p.recordingFor(w, r, sess, "mix download")
	if !ok {
		return
	}
	f, err := p.recordings.OpenMix(rec)
	if errors.Is(err, commands.ErrMixNotReady) {
		http.Error(w, "this recording's mix is not ready", http.StatusNotFound)
		return
	}
	if err != nil {
		p.serverError(w, "mix download", err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		p.serverError(w, "mix download", err)
		return
	}
	utils.Info("Panel recording downloaded", "recording_id", rec.ID, "file", "mix", "username", sess.username, "forum_user_id", sess.userID)
	w.Header().Set("Content-Type", "audio/ogg")
	w.Header().Set("Content-Disposition", attachment(commands.RecordingFileName(rec, ".opus")))
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// downloadZip is GET /recordings/{id}/zip: a zip of the recording's tracks
// and its info file, written as it streams, with no time budget. A running
// recording has no zip yet.
func (p *Panel) downloadZip(w http.ResponseWriter, r *http.Request, sess session) {
	rec, ok := p.recordingFor(w, r, sess, "zip download")
	if !ok {
		return
	}
	if rec.StoppedAt.IsZero() {
		http.Error(w, "this recording is still running", http.StatusNotFound)
		return
	}
	utils.Info("Panel recording downloaded", "recording_id", rec.ID, "file", "zip", "username", sess.username, "forum_user_id", sess.userID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", attachment(commands.RecordingFileName(rec, " tracks.zip")))
	if err := p.recordings.WriteZip(w, rec); err != nil {
		// The zip's headers have gone, so the browser gets a cut-off file.
		// A browser that left made the write fail, which is no fault.
		if r.Context().Err() != nil {
			utils.Info("Panel zip download abandoned", "recording_id", rec.ID, "username", sess.username, "forum_user_id", sess.userID)
			return
		}
		utils.CaptureError("Panel zip download failed", err, "recording_id", rec.ID)
	}
}

// attachment is a Content-Disposition that saves a download under the name
// given, encoded for a name outside ASCII.
func attachment(name string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return "attachment"
}

// recordingFor reads the recording the route names, for a session that may
// open it: its starter or a panel admin. It answers the request itself and
// returns false when the route names no recording, the read fails, or the
// session may not open it, which gets the no-access page.
func (p *Panel) recordingFor(w http.ResponseWriter, r *http.Request, sess session, step string) (store.Recording, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return store.Recording{}, false
	}
	ctx, cancel := panelClock.WithTimeout(r.Context(), storeTimeout)
	defer cancel()
	rec, err := p.recordings.Recording(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return store.Recording{}, false
	case errors.Is(err, context.Canceled):
		utils.Info("Panel page abandoned", "step", step, "username", sess.username, "forum_user_id", sess.userID)
		return store.Recording{}, false
	case err != nil:
		p.pageFailed(w, sess, step, recordingsPath, err, nil)
		return store.Recording{}, false
	}
	if !sess.access.panelAdmin && (sess.discordID == "" || rec.StarterID != sess.discordID) {
		utils.Info("Panel recording refused", "recording_id", rec.ID, "step", step, "username", sess.username, "forum_user_id", sess.userID)
		p.render(w, http.StatusForbidden, "noaccess", sess.page("No access"))
		return store.Recording{}, false
	}
	return rec, true
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
		page := commands.RecordingPath(rec.ID)
		item := recordingItem{ID: rec.ID, Title: rec.Title, Channel: rec.ChannelName, StartedAt: rec.StartedAt,
			Running: rec.StoppedAt.IsZero(), Mix: rec.Mix, StarterID: rec.StarterID, MixURL: page + "/mix", ZipURL: page + "/zip"}
		if !item.Running {
			item.Length = recordingLength(rec.StoppedAt.Sub(rec.StartedAt))
			item.DeletesAt = commands.RecordingDeletesAt(rec)
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
