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

// The AO roles (#231, GLOSSARY.md): the roles that mark a member as playing
// one of the regiment's games, set in their section on the Settings page
// the way the recording roles are. The Joiners page reads them.

const (
	// fieldAORoles is the form field the section's tags post under, and the
	// field of its saves' diffs.
	fieldAORoles = "ao_roles"
	// formAORoles is the section's form, as a refusal and a save name it,
	// and the ID of its heading.
	formAORoles = "ao-roles"
	// setAORoles names the set the section's picker edits.
	setAORoles = "the AO roles"
)

// The names the section's store reads' errors carry, as the hub page's do.
const (
	aoRolesRead       = "read AO roles"
	aoRoleChangesRead = "list AO roles changes"
)

// aoRolesInput is the AO roles section's form as posted.
type aoRolesInput struct {
	// Version is the set's version when the section loaded, from a hidden
	// input. A save whose version the set is no longer at is a stale form.
	Version string
	RoleIDs []string
}

// errStaleAORoles is the refusal a save from a stale AO roles section gets,
// answered the way errStaleRecordingRoles is.
var errStaleAORoles = &fieldError{kind: refusalStale, Message: "Someone saved the AO roles after you opened this page, " +
	"so your changes were not saved. Their save is at the top of the change log below. " +
	"Your roles are still in the picker. Save again to keep them."}

// errIneligibleAORole is the refusal an AO roles save gets when it posts a
// role it may not add.
var errIneligibleAORole = &fieldError{Field: fieldAORoles, Message: "One of those roles cannot be an AO role. Choose again."}

// errAORolesUnchanged is the refusal an AO roles save gets when it posts
// the stored set.
var errAORolesUnchanged = &fieldError{kind: refusalUnchanged, Message: "Those are already the AO roles, so nothing was saved."}

// aoRolesPage is the AO roles section as the page renders it, as
// recordingRolesPage is the recording roles section.
type aoRolesPage struct {
	Version string
	Picker  pickerView
	Changes []changeView
	// Saved is the section showing a save of it just made.
	Saved bool
}

// aoRolesSection builds the AO roles section the way recordingRolesSection
// builds the recording roles section.
func (s *hubService) aoRolesSection(ctx context.Context, guild guildInfo, req pageRequest) (aoRolesPage, error) {
	stored, err := s.deps.Store.GetAORoles(ctx, s.deps.GuildID)
	if err != nil {
		return aoRolesPage{}, fmt.Errorf("%s: %w", aoRolesRead, err)
	}
	selected, version := stored.RoleIDs, strconv.FormatInt(stored.Version, 10)
	if posted := req.AORoles; posted != nil {
		selected = posted.RoleIDs
		version = postedBackVersion(stored.Version, posted.Version, req.staleFor(formAORoles))
	}
	entries, err := s.deps.Store.ListAORoleChanges(ctx, changeLogLimit)
	if err != nil {
		return aoRolesPage{}, fmt.Errorf("%s: %w", aoRoleChangesRead, err)
	}
	return aoRolesPage{Version: version, Picker: rolePicker(fieldAORoles, setAORoles, guild, stored.RoleIDs, selected),
		Changes: changeViews(entries, guild), Saved: req.savedAt(formAORoles, stored.Version)}, nil
}

// setAORoles saves the AO roles under setRecordingRoles' rules: stale
// first, then the posted IDs validated against the guild and the stored
// set, then unchanged, each a refusal that writes nothing. It returns the
// set as stored, at the version the save left it at.
func (s *hubService) setAORoles(ctx context.Context, in aoRolesInput, by actor) (store.AORoles, error) {
	unlock, err := s.lockSave(ctx)
	if err != nil {
		return store.AORoles{}, err
	}
	defer unlock()
	before, err := s.deps.Store.GetAORoles(ctx, s.deps.GuildID)
	if err != nil {
		return store.AORoles{}, fmt.Errorf("%s: %w", aoRolesRead, err)
	}
	if !formIsCurrent(in.Version, before.Version) {
		return store.AORoles{}, errStaleAORoles
	}
	guild, err := s.readGuild(ctx)
	if err != nil {
		return store.AORoles{}, err
	}
	roles, err := acceptedRoles(in.RoleIDs, guild.info, before.RoleIDs, errIneligibleAORole)
	if err != nil {
		return store.AORoles{}, err
	}
	if slices.Equal(sortedRoles(roles), sortedRoles(before.RoleIDs)) {
		return store.AORoles{}, errAORolesUnchanged
	}
	d := diff{fieldAORoles: {Before: sortedRoles(before.RoleIDs), After: sortedRoles(roles)}}
	entry, err := changeEntry(store.ChangeAORoles, d, by)
	if err != nil {
		return store.AORoles{}, err
	}
	saved := store.AORoles{RoleIDs: roles, Version: before.Version}
	err = s.deps.Store.SaveAORoles(ctx, s.deps.GuildID, saved, entry)
	if errors.Is(err, store.ErrStale) {
		// Another process saved the set after this save's read.
		return store.AORoles{}, errStaleAORoles
	}
	if err != nil {
		return store.AORoles{}, fmt.Errorf("write AO roles: %w", err)
	}
	saved.Version++
	return saved, nil
}

// saveAORoles is POST /ao-roles, answered the way saveRecordingRoles
// answers POST /recording-roles.
func (p *Panel) saveAORoles(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := aoRolesInput{Version: r.PostForm.Get(fieldVersion), RoleIDs: r.PostForm[fieldAORoles]}
	ctx, hubs := p.saving(r)
	saved, err := hubs.setAORoles(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderSettings(w, r, sess, refusalStatus(sess, refusal), pageRequest{AORoles: &in, Error: refusal, Refused: formAORoles})
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "AO roles save", settingsPath, err)
		return
	}
	utils.Info("Panel AO roles saved", "role_ids", saved.RoleIDs, "username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, settingsSavedAddress(formAORoles, saved.Version), http.StatusSeeOther)
}
