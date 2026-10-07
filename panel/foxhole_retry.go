package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/7cav/cavbot2/commands"
	"github.com/7cav/cavbot2/store"
)

// The Foxhole page's Retry (spec #434, "The progress block, the report and
// Retry"): a report that ended with members the action failed on or never
// attempted offers Retry, which opens the action's preview holding only
// those members. The manager confirms against Discord as it is then, and
// the Retry runs as a new action, named for whoever confirmed it.

// foxholeRetryPath is the Confirm of a Retry opened for a purge, a re-add
// or a roster add, which posts the report as fieldReport.
const foxholeRetryPath = "/foxhole/retry"

// retryReport reads the report with the ID given for its Retry: nil when no
// report has the ID, or its action still runs, or it missed nobody.
func (s foxholeService) retryReport(ctx context.Context, id int64) (*reportView, error) {
	stored, err := s.store.FoxholeReport(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Foxhole report %d: %w", id, err)
	}
	report, err := reportViewOf(stored)
	if err != nil || !report.CanRetry() {
		return nil, err
	}
	return report, nil
}

// openRetry opens on the page the Retry of the report the request names:
// the report, whose failures the page shows again, and the action's
// preview, holding only the members it missed, read from a complete member
// list and the guild's Foxhole records. A report with nothing to retry
// opens nothing.
func (s foxholeService) openRetry(ctx context.Context, view *foxholeView, list commands.MemberListSnapshot, records map[string]store.FoxholeRecord, req foxholeRequest) error {
	report, err := s.retryReport(ctx, req.Retry)
	if err != nil || report == nil {
		return err
	}
	var ids []string
	for _, m := range report.Missed {
		ids = append(ids, m.ID)
	}
	switch report.Action {
	case store.ChangeAdd:
		// A line naming a member's ID matches them alone, so the preview
		// names each member the add missed, whatever their names now.
		view.AddPreview = s.addPreviewOf(list, records, pasteBox{Role: report.Role, Text: strings.Join(ids, "\n")}, nil)
	case store.ChangeRemoval:
		view.RemovePreview = s.removePreviewOf(list, records,
			foxholeRequest{Query: req.Query, Filter: req.Filter, Remove: report.Role, RemoveMembers: ids})
	case store.ChangeRosterAdd:
		unit, ok := commands.LookupValidatedInternalUnitLabelled(report.Unit)
		if !ok {
			// The unit has left the registry since, so no roster add of it
			// starts again (ADR 0009).
			return nil
		}
		var troopers []commands.RosterTrooper
		for _, m := range report.Missed {
			troopers = append(troopers, commands.RosterTrooper{Username: m.Trooper, DiscordID: m.ID})
		}
		view.RosterPreview = s.rosterPreviewOf(list, records, unit, troopers)
		view.RosterPreview.Retry = report.ID
	case store.ChangeReAdd, store.ChangePurge:
		view.RetryConfirm = s.retryConfirmOf(report, list)
	}
	view.Retry = report
	return nil
}

// retryConfirm is the confirmation the Retry of a purge or a re-add waits
// on, actions with no preview of their own to open again: the members it
// missed, each with the role they were to lose or get, split by what
// confirming does with each as the member list stands. Changes are those
// who lose or get it, and Skipped those the run would skip, with why. Each
// shows under the names the member list shows, or the report's when it
// doesn't hold them.
type retryConfirm struct {
	// Report is the report whose Retry it is.
	Report   *reportView
	Changes  []previewMember
	Skipped  []previewMember
	Estimate estimate
}

// Purge reports whether the Retry is a purge's, which takes each member's
// role. A re-add's gives it.
func (c retryConfirm) Purge() bool { return c.Report.Action == store.ChangePurge }

// retryConfirmOf is the confirmation of the report's Retry, read from a
// complete member list.
func (s foxholeService) retryConfirmOf(report *reportView, list commands.MemberListSnapshot) *retryConfirm {
	guild := s.foxholeGuildOf()
	confirm := &retryConfirm{Report: report}
	grant := !confirm.Purge()
	for _, m := range report.Missed {
		member := previewMember{ID: m.ID, DisplayName: m.DisplayName, Username: m.Username, RoleName: m.RoleName, Role: m.Role}
		mem, inServer := list.Member(m.ID)
		holds := inServer && guild.rowOf(mem, store.FoxholeRecord{}).holdsRole(m.Role)
		if inServer {
			member.DisplayName, member.Username = mem.DisplayName(), mem.Username
		}
		switch {
		case !inServer:
			member.Skip = commands.SkipLeft
		case grant && holds:
			member.Skip = commands.SkipHolding
		case !grant && !holds:
			member.Skip = commands.SkipNotHolding
		default:
			confirm.Changes = append(confirm.Changes, member)
			continue
		}
		confirm.Skipped = append(confirm.Skipped, member)
	}
	confirm.Estimate = estimateFor(len(confirm.Changes))
	return confirm
}

// startRetry is POST /foxhole/retry, the Confirm of the Retry of a purge, a
// re-add or a roster add: it starts the action of the report the form names
// again, over the members the report missed, as startAction says.
func (p *Panel) startRetry(w http.ResponseWriter, r *http.Request, sess session) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "the form could not be read", http.StatusBadRequest)
		return
	}
	reportID, err := strconv.ParseInt(r.PostForm.Get(fieldReport), 10, 64)
	if err != nil {
		http.Error(w, "the form names no report, so nothing changed", http.StatusBadRequest)
		return
	}
	p.startAction(w, r, sess, actionPage, "the retry", func(ctx context.Context, by commands.ForumUser) error {
		err := p.foxhole.actions.Retry(ctx, reportID, by)
		if errors.Is(err, commands.ErrNothingToRetry) {
			return errNothingToRetry
		}
		return err
	}, "report_id", reportID)
}

// errNothingToRetry refuses a Retry's Confirm posted for a report with
// nothing to retry, as from a page loaded before the report went, or one
// made by hand. The page offers Retry only on a report that has something
// to retry.
var errNothingToRetry = &saveRefusal{Kind: "nothing-to-retry", status: http.StatusConflict,
	log:     "Panel action refused: nothing to retry",
	Message: "That report has no members left to retry, so nothing changed."}
