package panel

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"slices"
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
	case errors.Is(err, context.Canceled):
		// The browser left the callback: expected, so no Sentry event.
		utils.Info("Panel sign-in abandoned during the milpac lookup", "username", username)
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

// paramDeleted is the Recordings page's query parameter naming the
// recording a delete's redirect says has gone.
const paramDeleted = "deleted"

// The Recordings page's sentences for a list with nothing in it: after a
// delete, and otherwise.
const (
	noRecordingsLeft = "No recordings left."
	noRecordingsYet  = "No recordings yet."
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
	// Gone is set on the page of a recording the panel doesn't hold, such
	// as one deleted since its link was sent.
	Gone bool
	// DeletePreview is set on a recording's delete preview.
	DeletePreview bool
	// Deleted is set when the page says a recording has just been deleted.
	Deleted bool
	// Outsider is set for an outsider's list, which, when it lists
	// nothing, says their forum groups open no other page.
	Outsider bool
}

// EmptyNote is what the page says when it lists no recordings: that none
// are left after a delete, and that there are none yet otherwise.
func (v recordingsView) EmptyNote() string {
	if v.Deleted {
		return noRecordingsLeft
	}
	return noRecordingsYet
}

// recordingItem is one recording as the page shows it.
type recordingItem struct {
	ID        int64
	Title     string
	Channel   string
	StartedAt time.Time
	// Length is how long it ran, such as 1h2m3s, empty while it runs.
	Length string
	// DeletesAt is when it is deleted, zero while it runs.
	DeletesAt time.Time
	Speakers  []recordingSpeaker
	// Running is set while the recording runs.
	Running bool
	// HasTrack is set when a stopped recording has a speaker's track on
	// disk, so its zip holds a track. One without offers its info file in
	// the zip's place.
	HasTrack bool
	// CutShort is set for a recording a restart or a crash cut short.
	CutShort bool
	Mix      store.MixState
	// StarterID is the starter's Discord ID, and Starter their display
	// name, for a page that names the starter, which sets ShowStarter.
	StarterID   string
	Starter     string
	ShowStarter bool
	// PageURL is the recording's page's address, MixURL, ZipURL and InfoURL
	// the downloads', and DeleteURL its delete preview's.
	PageURL   string
	MixURL    string
	ZipURL    string
	InfoURL   string
	DeleteURL string
}

// recordingSpeaker is one of a recording's speakers as the page names
// them: their display name, and Marker, the lost track marker when their
// track was lost (GLOSSARY.md, Lost track), else empty.
type recordingSpeaker struct {
	Name   string
	Marker string
}

// recordingsPage is GET /recordings: the recordings the user started, the
// page every signed-in visitor opens. A panel admin's switch lists every
// recording instead, with its starter; the switch's address shows anyone
// else their own.
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
	var deleted bool
	if err == nil {
		deleted, err = p.wasDeleted(ctx, r.URL.Query().Get(paramDeleted))
	}
	if errors.Is(err, context.Canceled) {
		utils.Info("Panel page abandoned", "step", "recordings page", "username", sess.username, "forum_user_id", sess.userID)
		return
	}
	if err != nil {
		p.pageFailed(w, sess, "recordings page", recordingsPath, err, nil)
		return
	}
	p.renderRecordings(w, sess, http.StatusOK, recordingsView{Recordings: p.recordingItems(recs), Switch: sess.access.panelAdmin, All: all,
		ShowStarter: all, Deleted: deleted, Outsider: sess.access.outsider()})
}

// wasDeleted reports whether the recording the Recordings page's deleted
// parameter names is gone: the result a delete's redirect shows, worked out
// again at each load. A parameter that names no recording ID shows nothing.
func (p *Panel) wasDeleted(ctx context.Context, raw string) (bool, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return false, nil
	}
	_, err = p.recordings.Recording(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	return false, err
}

// renderRecordings renders the Recordings page with the status and view
// given, naming each recording's starter by their display name when the
// view shows them.
func (p *Panel) renderRecordings(w http.ResponseWriter, sess session, status int, view recordingsView) {
	if view.ShowStarter {
		members := p.hubs.deps.Manager.MemberList(p.hubs.deps.GuildID)
		for i := range view.Recordings {
			view.Recordings[i].Starter = members.DisplayName(view.Recordings[i].StarterID)
			view.Recordings[i].ShowStarter = true
		}
	}
	data := sess.page("Recordings")
	data.Page = pageRecordings
	data.Recordings = view
	p.render(w, status, "recordings", data)
}

// recordingPage is GET /recordings/{id}: one recording, the page the link
// a stop gives leads to. Its starter and panel admins open it.
func (p *Panel) recordingPage(w http.ResponseWriter, r *http.Request, sess session) {
	rec, ok := p.recordingFor(w, r, sess, "recording page")
	if !ok {
		return
	}
	p.renderRecordings(w, sess, http.StatusOK, recordingsView{Recordings: p.recordingItems([]store.Recording{rec}), Single: true,
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
		// Only an address typed by hand gets here: the page offers the mix
		// once it is ready. The recording's page says how far it has got.
		http.Redirect(w, r, commands.RecordingPath(rec.ID), http.StatusSeeOther)
		return
	}
	if err != nil {
		p.serverError(w, "mix download", err)
		return
	}
	defer func() { _ = f.Close() }()
	utils.Info("Panel recording downloaded", "recording_id", rec.ID, "file", "mix", "username", sess.username, "forum_user_id", sess.userID)
	w.Header().Set("Content-Type", "audio/ogg")
	w.Header().Set("Content-Disposition", attachment(commands.RecordingFileName(rec, ".opus")))
	// ServeContent answers a range request too, so a download cut off
	// partway resumes. The mix never changes once built, so no time goes
	// with it.
	http.ServeContent(w, r, "", time.Time{}, f)
}

// downloadZip is GET /recordings/{id}/zip: a zip of the recording's tracks
// and its info file, written as it streams, with no time budget. A running
// recording has no zip yet.
func (p *Panel) downloadZip(w http.ResponseWriter, r *http.Request, sess session) {
	rec, ok := p.stoppedRecordingFor(w, r, sess, "zip download")
	if !ok {
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

// downloadInfo is GET /recordings/{id}/info: the recording's info file
// alone, which the page offers in the zip's place for a recording with no
// track on disk (#648). A running recording has no info file yet.
func (p *Panel) downloadInfo(w http.ResponseWriter, r *http.Request, sess session) {
	rec, ok := p.stoppedRecordingFor(w, r, sess, "info download")
	if !ok {
		return
	}
	info, err := p.recordings.InfoFile(rec)
	if err != nil {
		p.serverError(w, "info download", err)
		return
	}
	utils.Info("Panel recording downloaded", "recording_id", rec.ID, "file", "info", "username", sess.username, "forum_user_id", sess.userID)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", attachment(commands.RecordingFileName(rec, " info.txt")))
	_, _ = w.Write(info)
}

// recordingDeletePath is the address of a recording's delete preview, and
// of its Confirm.
func recordingDeletePath(id int64) string {
	return commands.RecordingPath(id) + "/delete"
}

// deletePreview is GET /recordings/{id}/delete: the danger confirm a
// recording's delete waits on, for its starter and panel admins.
func (p *Panel) deletePreview(w http.ResponseWriter, r *http.Request, sess session) {
	rec, ok := p.recordingFor(w, r, sess, "delete preview")
	if !ok {
		return
	}
	p.renderRecordings(w, sess, http.StatusOK, recordingsView{Recordings: p.recordingItems([]store.Recording{rec}), Single: true, DeletePreview: true,
		ShowStarter: rec.StarterID != sess.discordID})
}

// deleteRecording is POST /recordings/{id}/delete, the delete preview's
// Confirm: the recording's files and its row go, then a redirect to the
// Recordings page, which says so. The delete outlives the request, so a
// browser that leaves partway doesn't stop it between the files and the
// row.
func (p *Panel) deleteRecording(w http.ResponseWriter, r *http.Request, sess session) {
	rec, ok := p.recordingFor(w, r, sess, "recording delete")
	if !ok {
		return
	}
	ctx, cancel := panelClock.WithTimeout(context.WithoutCancel(r.Context()), storeTimeout)
	defer cancel()
	err := p.recordings.Delete(ctx, rec)
	if errors.Is(err, commands.ErrRecordingRunning) {
		// Only an address typed by hand gets here: the page offers no
		// Delete while the recording runs, and says it is still recording.
		http.Redirect(w, r, commands.RecordingPath(rec.ID), http.StatusSeeOther)
		return
	}
	if err != nil {
		// The files may have gone before the failure, so the event names
		// who deleted them too.
		utils.CaptureError("Panel recording delete failed", err, "recording_id", rec.ID, "username", sess.username, "forum_user_id", sess.userID)
		http.Error(w, serverErrorText, http.StatusInternalServerError)
		return
	}
	// The trail a questioned deletion is traced by. A deletion has no
	// change log entry (spec #381): the recording it would describe is
	// gone, and no page could show it.
	utils.Info("Panel recording deleted", "recording_id", rec.ID, "username", sess.username, "forum_user_id", sess.userID)
	q := url.Values{paramDeleted: {strconv.FormatInt(rec.ID, 10)}}
	if rec.StarterID != sess.discordID {
		// Only a panel admin deletes a recording someone else started, such
		// as for an S6 deletion ticket: back to every recording, where the
		// next one is.
		q.Set(paramView, viewAll)
	}
	http.Redirect(w, r, recordingsPath+"?"+q.Encode(), http.StatusSeeOther)
}

// recordingStatusPath is the recording status endpoint's address, which the
// deploy workflow polls (.github/scripts/wait-for-recordings.sh).
const recordingStatusPath = "/recording-status"

// recordingStatus is GET /recording-status, open to anyone with no panel
// session: whether a recording is running, as {"recording": true|false},
// and nothing else (spec #381, #394). The deploy workflow polls it and
// waits while one runs, so a deploy doesn't cut a recording. Anyone can
// read it, so it names no channel and no member. A recording a restart cut
// short counts as running until startup recovery closes it.
func (p *Panel) recordingStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := panelClock.WithTimeout(r.Context(), storeTimeout)
	defer cancel()
	recs, err := p.recordings.All(ctx)
	if errors.Is(err, context.Canceled) {
		// The poll left before the answer: expected, so no Sentry event.
		utils.Info("Panel recording status abandoned")
		return
	}
	if err != nil {
		p.serverError(w, "recording status", err)
		return
	}
	running := slices.ContainsFunc(recs, func(rec store.Recording) bool { return rec.StoppedAt.IsZero() })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Recording bool `json:"recording"`
	}{running})
}

// attachment is a Content-Disposition that saves a download under the name
// given, encoded for a name outside ASCII.
func attachment(name string) string {
	return mime.FormatMediaType("attachment", map[string]string{"filename": name})
}

// recordingFor reads the recording the route names, for a session that may
// open it: its starter or a panel admin. It answers the request itself and
// returns false in three cases. A route that names no recording, or an ID
// that isn't one, gets a page saying the recording isn't in the panel. A
// read that fails gets the error page. A session that may not open it gets
// the no-access page, saying the recording is open only to its starter and
// panel admins.
func (p *Panel) recordingFor(w http.ResponseWriter, r *http.Request, sess session, step string) (store.Recording, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		p.renderRecordings(w, sess, http.StatusNotFound, recordingsView{Single: true, Gone: true})
		return store.Recording{}, false
	}
	ctx, cancel := panelClock.WithTimeout(r.Context(), storeTimeout)
	defer cancel()
	rec, err := p.recordings.Recording(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		p.renderRecordings(w, sess, http.StatusNotFound, recordingsView{Single: true, Gone: true})
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
		p.noAccess(w, sess, noAccessRecording)
		return store.Recording{}, false
	}
	return rec, true
}

// stoppedRecordingFor is recordingFor for a download that a running
// recording doesn't have yet. A running recording's download redirects to
// its page, which offers no download while it runs and says it is still
// recording.
func (p *Panel) stoppedRecordingFor(w http.ResponseWriter, r *http.Request, sess session, step string) (store.Recording, bool) {
	rec, ok := p.recordingFor(w, r, sess, step)
	if ok && rec.StoppedAt.IsZero() {
		http.Redirect(w, r, commands.RecordingPath(rec.ID), http.StatusSeeOther)
		return store.Recording{}, false
	}
	return rec, ok
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
func (p *Panel) recordingItems(recs []store.Recording) []recordingItem {
	out := make([]recordingItem, 0, len(recs))
	for _, rec := range recs {
		page := commands.RecordingPath(rec.ID)
		item := recordingItem{ID: rec.ID, Title: rec.Title, Channel: rec.ChannelName, StartedAt: rec.StartedAt,
			Running: rec.StoppedAt.IsZero(), CutShort: rec.Ended == store.RecordingEndCutShort, Mix: rec.Mix, StarterID: rec.StarterID,
			PageURL: page, MixURL: page + "/mix", ZipURL: page + "/zip", InfoURL: page + "/info",
			DeleteURL: recordingDeletePath(rec.ID)}
		if !item.Running {
			item.Length = rec.StoppedAt.Sub(rec.StartedAt).Round(time.Second).String()
			item.DeletesAt = commands.RecordingDeletesAt(rec)
			item.HasTrack = p.recordings.HasTrack(rec)
		}
		for _, sp := range rec.Speakers {
			speaker := recordingSpeaker{Name: sp.DisplayName}
			if sp.TrackLost {
				speaker.Marker = commands.TrackLostMarker
			}
			item.Speakers = append(item.Speakers, speaker)
		}
		out = append(out, item)
	}
	return out
}
