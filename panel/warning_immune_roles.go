package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The warning immune roles (#971): the roles whose holders /warn refuses to
// warn, set in the Warnings section of the Settings page the way the
// recording roles are. /warn reads them from the store at each use.

const (
	// fieldWarningImmuneRoles is the form field the section's tags post
	// under, and the field of its saves' diffs.
	fieldWarningImmuneRoles = "warning_immune_roles"
	// formWarnings is the Warnings section's form, as a refusal and a save
	// name it, and the ID of its heading.
	formWarnings = "warnings"
	// setWarningImmuneRoles names the set the section's picker edits.
	setWarningImmuneRoles = "the immune roles"
)

// The names the section's store reads' errors carry, as the hub page's do.
const (
	warningImmuneRolesRead       = "read warning immune roles"
	warningImmuneRoleChangesRead = "list warning immune roles changes"
)

type warningImmuneRolesInput struct {
	// Version is the set's version when the section loaded, from a hidden
	// input. A save whose version the set is no longer at is a stale form.
	Version string
	RoleIDs []string
}

// errStaleWarningImmuneRoles is the refusal a save from a stale Warnings
// section gets, answered the way errStaleRecordingRoles is.
var errStaleWarningImmuneRoles = &fieldError{kind: refusalStale, Message: "Someone saved the immune roles after you opened this page, " +
	"so your changes were not saved. Their save is at the top of the change log below. " +
	"Your roles are still in the picker. Save again to keep them."}

// errIneligibleWarningImmuneRole is the refusal an immune roles save gets
// when it posts a role it may not add.
var errIneligibleWarningImmuneRole = &fieldError{Field: fieldWarningImmuneRoles, Message: "One of those roles cannot be an immune role. Choose again."}

// errWarningImmuneRolesUnchanged is the refusal an immune roles save gets
// when it posts the stored set.
var errWarningImmuneRolesUnchanged = &fieldError{kind: refusalUnchanged, Message: "Those are already the immune roles, so nothing was saved."}

// warningImmuneRolesPage is the Warnings section as the page renders it, as
// recordingRolesPage is the recording roles section.
type warningImmuneRolesPage struct {
	Version string
	Picker  pickerView
	Changes []changeView
	// Saved is the section showing a save of it just made.
	Saved bool
}

// warningImmuneRolesSection builds the Warnings section the way
// recordingRolesSection builds the recording roles section.
func (s *hubService) warningImmuneRolesSection(ctx context.Context, guild guildInfo, req pageRequest) (warningImmuneRolesPage, error) {
	stored, err := s.deps.Store.GetWarningImmuneRoles(ctx, s.deps.GuildID)
	if err != nil {
		return warningImmuneRolesPage{}, fmt.Errorf("%s: %w", warningImmuneRolesRead, err)
	}
	selected, version := stored.RoleIDs, strconv.FormatInt(stored.Version, 10)
	if posted := req.WarningImmuneRoles; posted != nil {
		selected = posted.RoleIDs
		version = postedBackVersion(stored.Version, posted.Version, req.staleFor(formWarnings))
	}
	entries, err := s.deps.Store.ListWarningImmuneRoleChanges(ctx, changeLogLimit)
	if err != nil {
		return warningImmuneRolesPage{}, fmt.Errorf("%s: %w", warningImmuneRoleChangesRead, err)
	}
	return warningImmuneRolesPage{Version: version, Picker: rolePicker(fieldWarningImmuneRoles, setWarningImmuneRoles, guild, stored.RoleIDs, selected),
		Changes: changeViews(entries, guild), Saved: req.savedAt(formWarnings, stored.Version)}, nil
}

// setWarningImmuneRoles saves the warning immune roles under
// setRecordingRoles' rules: stale first, then the posted IDs validated
// against the guild and the stored set, then unchanged, each a refusal that
// writes nothing. It returns the set as stored, at the version the save
// left it at.
func (s *hubService) setWarningImmuneRoles(ctx context.Context, in warningImmuneRolesInput, by actor) (store.WarningImmuneRoles, error) {
	unlock, err := s.lockSave(ctx)
	if err != nil {
		return store.WarningImmuneRoles{}, err
	}
	defer unlock()
	before, err := s.deps.Store.GetWarningImmuneRoles(ctx, s.deps.GuildID)
	if err != nil {
		return store.WarningImmuneRoles{}, fmt.Errorf("%s: %w", warningImmuneRolesRead, err)
	}
	if !formIsCurrent(in.Version, before.Version) {
		return store.WarningImmuneRoles{}, errStaleWarningImmuneRoles
	}
	guild, err := s.readGuild(ctx)
	if err != nil {
		return store.WarningImmuneRoles{}, err
	}
	roles, err := acceptedRoles(in.RoleIDs, guild.info, before.RoleIDs, errIneligibleWarningImmuneRole)
	if err != nil {
		return store.WarningImmuneRoles{}, err
	}
	if slices.Equal(sortedRoles(roles), sortedRoles(before.RoleIDs)) {
		return store.WarningImmuneRoles{}, errWarningImmuneRolesUnchanged
	}
	d := diff{fieldWarningImmuneRoles: {Before: sortedRoles(before.RoleIDs), After: sortedRoles(roles)}}
	entry, err := changeEntry(store.ChangeWarningImmuneRoles, d, by)
	if err != nil {
		return store.WarningImmuneRoles{}, err
	}
	saved := store.WarningImmuneRoles{RoleIDs: roles, Version: before.Version}
	err = s.deps.Store.SaveWarningImmuneRoles(ctx, s.deps.GuildID, saved, entry)
	if errors.Is(err, store.ErrStale) {
		// Another process saved the set after this save's read.
		return store.WarningImmuneRoles{}, errStaleWarningImmuneRoles
	}
	if err != nil {
		return store.WarningImmuneRoles{}, fmt.Errorf("write warning immune roles: %w", err)
	}
	saved.Version++
	return saved, nil
}

// saveWarningImmuneRoles is POST /warning-immune-roles, answered the way
// saveRecordingRoles answers POST /recording-roles.
func (p *Panel) saveWarningImmuneRoles(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := warningImmuneRolesInput{Version: r.PostForm.Get(fieldVersion), RoleIDs: r.PostForm[fieldWarningImmuneRoles]}
	ctx, hubs := p.saving(r)
	saved, err := hubs.setWarningImmuneRoles(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderSettings(w, r, sess, refusalStatus(sess, refusal), pageRequest{WarningImmuneRoles: &in, Error: refusal, Refused: formWarnings})
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "warning immune roles save", settingsPath, err)
		return
	}
	utils.Info("Panel warning immune roles saved", "role_ids", saved.RoleIDs, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, settingsSavedAddress(formWarnings, saved.Version), http.StatusSeeOther)
}
