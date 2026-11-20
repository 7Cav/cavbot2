package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
)

// The forum groups (GLOSSARY.md, Foxhole group and Joiners group): the
// forum groups the group check reads besides the admin groups, set in their
// section on the Settings page. The forum names no group to the panel, so
// the section takes IDs.

const (
	// fieldFoxholeGroup and fieldJoinersGroups are the section's form
	// fields, and the fields of its saves' diffs.
	fieldFoxholeGroup  = "foxhole_group"
	fieldJoinersGroups = "joiners_groups"
	// formForumGroups is the section's form, as a refusal and a save name
	// it, and the ID of its heading.
	formForumGroups = "forum-groups"
)

// The names the section's store reads' errors carry, as the hub page's do.
const (
	forumGroupsRead       = "read forum groups"
	forumGroupChangesRead = "list forum groups changes"
)

// forumGroupsInput is the section's form as posted, its two fields as typed.
type forumGroupsInput struct {
	Version       string
	FoxholeGroup  string
	JoinersGroups string
}

// errStaleForumGroups is the refusal a save from a stale forum groups
// section gets, answered the way errStaleAORoles is.
var errStaleForumGroups = &fieldError{kind: refusalStale, Message: "Someone saved the forum groups after you opened this page, " +
	"so your changes were not saved. Their save is at the top of the change log below. " +
	"Your groups are still in the form. Save again to keep them."}

// errForumGroupsUnchanged is the refusal a forum groups save gets when it
// posts the stored groups.
var errForumGroupsUnchanged = &fieldError{kind: refusalUnchanged, Message: "Those are already the forum groups, so nothing was saved."}

// The refusals of a field that does not hold group IDs.
var (
	errFoxholeGroupID  = &fieldError{Field: fieldFoxholeGroup, Message: "Enter the Foxhole group as one forum group ID, a whole number above 0."}
	errJoinersGroupIDs = &fieldError{Field: fieldJoinersGroups, Message: "Enter the Joiners groups as forum group IDs, whole numbers above 0, separated by commas or spaces."}
)

// forumGroupsPage is the forum groups section as the page renders it.
type forumGroupsPage struct {
	Version       string
	FoxholeGroup  string
	JoinersGroups string
	Changes       []changeView
	// Saved is the section showing a save of it just made.
	Saved bool
}

// forumGroupsSection builds the forum groups section from the store, or
// from the form as posted after a refusal.
func (s *hubService) forumGroupsSection(ctx context.Context, guild guildInfo, req pageRequest) (forumGroupsPage, error) {
	stored, err := s.deps.Store.GetForumGroups(ctx, s.deps.GuildID)
	if err != nil {
		return forumGroupsPage{}, fmt.Errorf("%s: %w", forumGroupsRead, err)
	}
	page := forumGroupsPage{Version: strconv.FormatInt(stored.Version, 10), FoxholeGroup: strconv.Itoa(stored.FoxholeGroupID),
		JoinersGroups: groupIDsText(stored.JoinersGroupIDs), Saved: req.savedAt(formForumGroups, stored.Version)}
	if posted := req.ForumGroups; posted != nil {
		page.FoxholeGroup, page.JoinersGroups = posted.FoxholeGroup, posted.JoinersGroups
		page.Version = postedBackVersion(stored.Version, posted.Version, req.staleFor(formForumGroups))
	}
	entries, err := s.deps.Store.ListForumGroupChanges(ctx, changeLogLimit)
	if err != nil {
		return forumGroupsPage{}, fmt.Errorf("%s: %w", forumGroupChangesRead, err)
	}
	page.Changes = changeViews(entries, guild)
	return page, nil
}

// setForumGroups saves the forum groups under setAORoles' rules: stale
// first, then each field parsed, then unchanged, each a refusal that writes
// nothing. It returns the groups as stored, at the version the save left
// them at.
func (s *hubService) setForumGroups(ctx context.Context, in forumGroupsInput, by actor) (store.ForumGroups, error) {
	before, err := s.deps.Store.GetForumGroups(ctx, s.deps.GuildID)
	if err != nil {
		return store.ForumGroups{}, fmt.Errorf("%s: %w", forumGroupsRead, err)
	}
	if !formIsCurrent(in.Version, before.Version) {
		return store.ForumGroups{}, errStaleForumGroups
	}
	foxhole, ok := parseGroupID(strings.TrimSpace(in.FoxholeGroup))
	if !ok {
		return store.ForumGroups{}, errFoxholeGroupID
	}
	joiners, ok := parseGroupIDList(in.JoinersGroups)
	if !ok {
		return store.ForumGroups{}, errJoinersGroupIDs
	}
	d := diff{}
	if foxhole != before.FoxholeGroupID {
		d[fieldFoxholeGroup] = change{Before: before.FoxholeGroupID, After: foxhole}
	}
	if was := sortedGroupIDs(before.JoinersGroupIDs); !slices.Equal(was, joiners) {
		d[fieldJoinersGroups] = change{Before: was, After: joiners}
	}
	if len(d) == 0 {
		return store.ForumGroups{}, errForumGroupsUnchanged
	}
	entry, err := changeEntry(store.ChangeForumGroups, d, by)
	if err != nil {
		return store.ForumGroups{}, err
	}
	saved := store.ForumGroups{FoxholeGroupID: foxhole, JoinersGroupIDs: joiners, Version: before.Version}
	err = s.deps.Store.SaveForumGroups(ctx, s.deps.GuildID, saved, entry)
	if errors.Is(err, store.ErrStale) {
		// Another process saved the groups after this save's read.
		return store.ForumGroups{}, errStaleForumGroups
	}
	if err != nil {
		return store.ForumGroups{}, fmt.Errorf("write forum groups: %w", err)
	}
	saved.Version++
	return saved, nil
}

// parseGroupIDList reads forum group IDs separated by commas or spaces as a
// sorted set, never nil. ok is false when one is not a forum group ID.
func parseGroupIDList(raw string) (ids []int, ok bool) {
	ids = []int{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		id, ok := parseGroupID(part)
		if !ok {
			return nil, false
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), true
}

// parseGroupID reads one forum group ID: a whole number above 0 that fits
// the store's INTEGER column.
func parseGroupID(s string) (int, bool) {
	id, err := strconv.ParseInt(s, 10, 32)
	return int(id), err == nil && id > 0
}

// sortedGroupIDs is a set of group IDs as a diff records it: sorted, and
// never null.
func sortedGroupIDs(ids []int) []int {
	out := slices.Sorted(slices.Values(ids))
	if out == nil {
		out = []int{}
	}
	return out
}

// groupIDsText is a set of group IDs as the form shows it.
func groupIDsText(ids []int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range sortedGroupIDs(ids) {
		parts = append(parts, strconv.Itoa(id))
	}
	return strings.Join(parts, ", ")
}

// saveForumGroups is POST /forum-groups, answered the way saveAORoles
// answers POST /ao-roles.
func (p *Panel) saveForumGroups(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	in := forumGroupsInput{Version: r.PostForm.Get(fieldVersion), FoxholeGroup: r.PostForm.Get(fieldFoxholeGroup),
		JoinersGroups: r.PostForm.Get(fieldJoinersGroups)}
	ctx, hubs := p.saving(r)
	saved, err := hubs.setForumGroups(ctx, in, sess.actor())
	if refusal, ok := asFieldError(err); ok {
		p.renderSettings(w, r, sess, refusalStatus(sess, refusal), pageRequest{ForumGroups: &in, Error: refusal, Refused: formForumGroups})
		return
	}
	if err != nil {
		p.saveFailed(w, r, sess, "forum groups save", settingsPath, err)
		return
	}
	utils.Info("Panel forum groups saved", "foxhole_group_id", saved.FoxholeGroupID, "joiners_group_ids", saved.JoinersGroupIDs,
		"username", sess.username, "forum_user_id", sess.userID)
	http.Redirect(w, r, settingsSavedAddress(formForumGroups, saved.Version), http.StatusSeeOther)
}
