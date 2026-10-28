package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/7cav/cavbot2/store"
	"github.com/7cav/cavbot2/utils"
	"github.com/bwmarrin/discordgo"
)

// --- Long /loa and /awol lists page in one message from a saved copy
// (#824, ADR 0020). ---

// listRunner is runLoa or runAwol.
type listRunner func(r utils.InteractionResponder, st store.Store, now time.Time, i *discordgo.InteractionCreate)

// listCommand is one of the two commands that page a long list, with a
// position of each length the tests need.
type listCommand struct {
	name string
	run  listRunner
	// short serves a list that fits one embed, and long one of many pages.
	short, long func(t *testing.T)
}

// loaList and awolList are /loa and /awol, each serving lists as the
// regiment's look.
var (
	loaList = listCommand{
		name:  "loa",
		run:   runLoa,
		short: func(t *testing.T) { serveLOAPosition(t, 1, 1, 1) },
		long:  func(t *testing.T) { serveLOAPosition(t, 60, 50, 40) },
	}
	awolList = listCommand{
		name:  "awol",
		run:   runAwol,
		short: func(t *testing.T) { serveAwolFlagged(t, 2) },
		long:  func(t *testing.T) { serveAwolFlagged(t, 100) },
	}
	listCommands = []listCommand{loaList, awolList}
)

// failingListStore is a store whose saved-list saves fail with saveErr
// and whose page reads fail with readErr, each when set.
type failingListStore struct {
	store.Store
	saveErr, readErr error
}

func (s failingListStore) SavePagedList(ctx context.Context, list store.PagedList) (string, error) {
	if s.saveErr != nil {
		return "", s.saveErr
	}
	return s.Store.SavePagedList(ctx, list)
}

func (s failingListStore) PagedListPage(ctx context.Context, key string, idx int) (store.SavedPage, error) {
	if s.readErr != nil {
		return store.SavedPage{}, s.readErr
	}
	return s.Store.PagedListPage(ctx, key, idx)
}

// listView is what a message of a list shows: its text, its embeds and its
// buttons.
type listView struct {
	content string
	embeds  []*discordgo.MessageEmbed
	buttons []discordgo.Button
}

// runList runs cmd for position 1-7 as member 999 at now over st, and
// returns what its reply shows once the run ends. It fails the test when
// the run sent a follow-up.
func runList(t *testing.T, cmd listCommand, st store.Store, now time.Time) listView {
	t.Helper()
	f := &fakeResponder{}
	cmd.run(f, st, now, fakeAppCommandInteraction(stringOption("position", "1-7")))
	return replyView(t, f)
}

// replyView returns what the run's reply shows once the run ends: the text,
// embeds and buttons its edits left on it. It fails the test when the run
// sent a follow-up or never edited its reply.
func replyView(t *testing.T, f *fakeResponder) listView {
	t.Helper()
	var v listView
	edited := false
	for _, c := range f.Calls() {
		switch {
		case c.Method == "Followup":
			t.Fatalf("the run sent a follow-up: %+v", c.Params)
		case c.Method == "Edit" && c.Edit != nil:
			edited = true
			if c.Edit.Content != nil {
				v.content = *c.Edit.Content
			}
			if c.Edit.Embeds != nil {
				v.embeds = *c.Edit.Embeds
			}
			if c.Edit.Components != nil {
				v.buttons = buttonsOf(*c.Edit.Components)
			}
		}
	}
	if !edited {
		t.Fatalf("the run never edited its reply; it last showed %q", lastReply(t, f.Calls()))
	}
	return v
}

// For each command, a list that fits one embed arrives as one embed with
// no page marker and no buttons, with a store or with one whose every save
// fails, and then with no unsaved-pages line and nothing sent to Sentry.
func TestAListThatFitsOneEmbedArrivesAsOneEmbedWithNoArrows(t *testing.T) {
	for _, cmd := range listCommands {
		stores := map[string]store.Store{
			"store":                  store.NewFake(),
			"store that fails saves": failingListStore{Store: store.NewFake(), saveErr: errors.New("connection refused")},
		}
		for name, st := range stores {
			t.Run(cmd.name+" with a "+name, func(t *testing.T) {
				cmd.short(t)
				rec := recordCaptures(t)
				v := runList(t, cmd, st, loaRefDate)

				if len(v.embeds) != 1 {
					t.Fatalf("want one embed; got %d", len(v.embeds))
				}
				if title := v.embeds[0].Title; strings.Contains(title, "Page") {
					t.Errorf("title = %q, want no page marker", title)
				}
				if len(v.buttons) != 0 {
					t.Errorf("the reply carries %d buttons, want none", len(v.buttons))
				}
				if strings.Contains(v.content, pagedListUnsavedLine) {
					t.Errorf("the reply says %q, want no unsaved-pages line", v.content)
				}
				if rec.count != 0 {
					t.Errorf("captures = %d, want none: %v", rec.count, rec.msgs)
				}
			})
		}
	}
}

// arrow returns the button on v labelled label, ◀ or ▶, failing the test
// when v has none or several.
func (v listView) arrow(t *testing.T, label string) discordgo.Button {
	t.Helper()
	var found []discordgo.Button
	for _, b := range v.buttons {
		if b.Label == label {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the message carries %d %s buttons, want one; buttons %+v", len(found), label, v.buttons)
	}
	return found[0]
}

// page returns the one embed v shows, failing the test when it shows
// another number.
func (v listView) page(t *testing.T) *discordgo.MessageEmbed {
	t.Helper()
	if len(v.embeds) != 1 {
		t.Fatalf("the message shows %d embeds, want one page", len(v.embeds))
	}
	return v.embeds[0]
}

// pageMarker reads the n and m of a title's (Page n/m), failing the test
// when it has none.
func pageMarker(t *testing.T, title string) (n, m int) {
	t.Helper()
	at := strings.LastIndex(title, "(Page ")
	if at < 0 {
		t.Fatalf("title %q carries no page marker", title)
	}
	if _, err := fmt.Sscanf(title[at:], "(Page %d/%d)", &n, &m); err != nil {
		t.Fatalf("title %q: %v", title, err)
	}
	return n, m
}

// /loa on about 150 LOAs across all three states, and /awol on 100 flagged
// troopers, send one message and no follow-up. It shows page 1 of m, with
// m above 1, ◀ disabled and ▶ enabled.
func TestALongListArrivesAsOneMessageShowingPageOneWithArrows(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			v := runList(t, cmd, store.NewFake(), loaRefDate)

			n, m := pageMarker(t, v.page(t).Title)
			if n != 1 || m < 2 {
				t.Fatalf("the reply shows page %d of %d, want page 1 of more than 1", n, m)
			}
			if !v.arrow(t, "◀").Disabled {
				t.Errorf("◀ is enabled on page 1")
			}
			if v.arrow(t, "▶").Disabled {
				t.Errorf("▶ is disabled on page 1 of %d", m)
			}
		})
	}
}

// runner is the member fakeAppCommandInteraction runs every command as.
const runner = "999"

// pressAnswer is how the bot answered a press on a list's arrow.
type pressAnswer struct {
	// updated is set when the answer updates the message pressed, and
	// private when it is a new message only the presser sees.
	updated, private bool
	view             listView
}

// pressList delivers member's press on b to cmd's handler over st at now,
// and returns the answer. It fails the test unless the bot answered with
// one response and nothing else.
func pressList(t *testing.T, cmd listCommand, st store.Store, now time.Time, b discordgo.Button, member string) pressAnswer {
	t.Helper()
	if b.Disabled {
		t.Fatalf("pressed the disabled %s button", b.Label)
	}
	f := &fakeResponder{}
	cmd.run(f, st, now, pressInteraction(b.CustomID, permMember{id: member}))
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Method != "Respond" || calls[0].Response == nil || calls[0].Response.Data == nil {
		t.Fatalf("the press got %+v, want one response", calls)
	}
	resp := calls[0].Response
	a := pressAnswer{
		updated: resp.Type == discordgo.InteractionResponseUpdateMessage,
		private: resp.Type == discordgo.InteractionResponseChannelMessageWithSource &&
			resp.Data.Flags&discordgo.MessageFlagsEphemeral != 0,
		view: listView{content: resp.Data.Content, embeds: resp.Data.Embeds, buttons: buttonsOf(resp.Data.Components)},
	}
	if a.updated == a.private {
		t.Fatalf("the press got a response of type %v with flags %v, want an update or a private message", resp.Type, resp.Data.Flags)
	}
	return a
}

// When the runner presses ▶ on page 1, the public message updates to page
// 2, titled with (Page 2/m). When m is above 2, both arrows on page 2 are
// enabled.
func TestTheRunnersPressOnNextFlipsTheListToPageTwo(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			st := store.NewFake()
			v := runList(t, cmd, st, loaRefDate)
			_, m := pageMarker(t, v.page(t).Title)
			if m < 3 {
				t.Fatalf("the fixture makes %d pages; the test needs more than 2", m)
			}

			a := pressList(t, cmd, st, loaRefDate, v.arrow(t, "▶"), runner)
			if !a.updated {
				t.Fatalf("the runner's press opened a new message, want the list updated")
			}
			if n, got := pageMarker(t, a.view.page(t).Title); n != 2 || got != m {
				t.Errorf("the list shows page %d of %d, want page 2 of %d", n, got, m)
			}
			if a.view.arrow(t, "◀").Disabled || a.view.arrow(t, "▶").Disabled {
				t.Errorf("page 2 of %d has a disabled arrow", m)
			}
		})
	}
}

// pageThrough runs cmd over st at now, then pages its list with the
// runner's ▶ from page 1 to the last, and returns every page in the order
// shown and what the list shows on its last page. It fails the test unless
// each press updates the list.
func pageThrough(t *testing.T, cmd listCommand, st store.Store, now time.Time) ([]*discordgo.MessageEmbed, listView) {
	t.Helper()
	v := runList(t, cmd, st, now)
	pages := []*discordgo.MessageEmbed{v.page(t)}
	for !v.arrow(t, "▶").Disabled {
		a := pressList(t, cmd, st, now, v.arrow(t, "▶"), runner)
		if !a.updated {
			t.Fatalf("the runner's press on page %d opened a new message, want the list updated", len(pages))
		}
		v = a.view
		pages = append(pages, v.page(t))
		if len(pages) > 100 {
			t.Fatalf("▶ never disables")
		}
	}
	return pages, v
}

// When the runner presses ▶ from page 1 to page m, page m has ▶ disabled
// and ◀ enabled. The runner's press on ◀ there goes back to page m-1.
func TestTheRunnersPressOnBackFromTheLastPageGoesBackOnePage(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			st := store.NewFake()
			pages, last := pageThrough(t, cmd, st, loaRefDate)
			m := len(pages)
			if n, got := pageMarker(t, last.page(t).Title); n != m || got != m {
				t.Fatalf("the last page shows page %d of %d, want %d of %d", n, got, m, m)
			}

			a := pressList(t, cmd, st, loaRefDate, last.arrow(t, "◀"), runner)
			if !a.updated {
				t.Fatalf("the runner's press on ◀ opened a new message, want the list updated")
			}
			if n, _ := pageMarker(t, a.view.page(t).Title); n != m-1 {
				t.Errorf("◀ on page %d shows page %d, want %d", m, n, m-1)
			}
		})
	}
}

// Every page a press shows has a description of at most 4096 characters
// and embed text of at most 6000 characters.
func TestEveryPageAPressShowsFitsOneEmbed(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			pages, _ := pageThrough(t, cmd, store.NewFake(), loaRefDate)
			for _, page := range pages {
				assertWithinDiscordLimits(t, [][]*discordgo.MessageEmbed{{page}})
			}
		})
	}
}

// otherMember is a member who didn't run the command.
const otherMember = "4242"

// When another member presses ▶ on page 1 of the public message, they get
// a new ephemeral message showing page 2 with its own arrows, and the
// public message doesn't update.
func TestAnotherMembersPressOpensAPrivateCopyAtTheirPage(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			st := store.NewFake()
			v := runList(t, cmd, st, loaRefDate)

			a := pressList(t, cmd, st, loaRefDate, v.arrow(t, "▶"), otherMember)
			if !a.private {
				t.Fatalf("another member's press updated the public message, want a private copy")
			}
			if n, _ := pageMarker(t, a.view.page(t).Title); n != 2 {
				t.Errorf("the private copy shows page %d, want 2", n)
			}
			a.view.arrow(t, "◀")
			a.view.arrow(t, "▶")
		})
	}
}

// On a list of 3 or more pages, when another member presses ▶ on page 2 of
// their private copy, the copy updates to page 3 and no new message opens.
func TestAPressOnAPrivateCopyFlipsTheCopy(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			st := store.NewFake()
			v := runList(t, cmd, st, loaRefDate)
			if _, m := pageMarker(t, v.page(t).Title); m < 3 {
				t.Fatalf("the fixture makes %d pages; the test needs 3 or more", m)
			}
			copy2 := pressList(t, cmd, st, loaRefDate, v.arrow(t, "▶"), otherMember)

			a := pressList(t, cmd, st, loaRefDate, copy2.view.arrow(t, "▶"), otherMember)
			if !a.updated {
				t.Fatalf("a press on the private copy opened a new message, want the copy updated")
			}
			if n, _ := pageMarker(t, a.view.page(t).Title); n != 3 {
				t.Errorf("the copy shows page %d, want 3", n)
			}
		})
	}
}

// assertPrivateNote fails the test unless a is a new message only the
// presser sees, carrying note and no page, so the message pressed stays as
// it was.
func assertPrivateNote(t *testing.T, a pressAnswer, note string) {
	t.Helper()
	if !a.private {
		t.Fatalf("the press updated the message pressed, want a private note")
	}
	if !strings.Contains(a.view.content, note) {
		t.Errorf("the note says %q, want %q", a.view.content, note)
	}
	if len(a.view.embeds) != 0 || len(a.view.buttons) != 0 {
		t.Errorf("the note carries %d embeds and %d buttons, want neither", len(a.view.embeds), len(a.view.buttons))
	}
}

// A press 30 days after the run gets a private note carrying the
// expired-list constant, and the message pressed doesn't update. A press
// 29 days after the run flips the page.
func TestAPressThirtyDaysAfterTheRunFindsTheListExpired(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			st := store.NewFake()
			v := runList(t, cmd, st, loaRefDate)

			a := pressList(t, cmd, st, loaRefDate.AddDate(0, 0, 30), v.arrow(t, "▶"), runner)
			assertPrivateNote(t, a, pagedListExpiredNote)

			if a := pressList(t, cmd, st, loaRefDate.AddDate(0, 0, 29), v.arrow(t, "▶"), runner); !a.updated {
				t.Errorf("a press 29 days after the run didn't flip the page")
			}
		})
	}
}

// When an arrow from a run that had a store reaches a newly built command
// with no store configured, the presser gets the expired-list note as a
// new ephemeral message, and the message pressed stays as it was.
func TestAPressOnAHostWithNoStoreFindsTheListExpired(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			v := runList(t, cmd, store.NewFake(), loaRefDate)

			assertPrivateNote(t, pressList(t, cmd, nil, loaRefDate, v.arrow(t, "▶"), runner), pagedListExpiredNote)
		})
	}
}

// When a press's store read fails, the presser gets the couldn't-show note
// as a new ephemeral message, Sentry gets one event, and the message
// pressed stays as it was.
func TestAPressWhoseReadFailsGetsTheCouldntShowNote(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			st := store.NewFake()
			v := runList(t, cmd, st, loaRefDate)
			rec := recordCaptures(t)
			down := errors.New("connection refused")

			a := pressList(t, cmd, failingListStore{Store: st, readErr: down}, loaRefDate, v.arrow(t, "▶"), runner)
			assertPrivateNote(t, a, pagedListUnreadableNote)
			assertCaptureNames(t, singleCapture(t, rec, down), map[string]string{"command": cmd.name})
		})
	}
}

// assertUnsavedReply fails the test unless v shows page 1 of a long list
// with both arrows disabled and the unsaved-pages line.
func assertUnsavedReply(t *testing.T, v listView) {
	t.Helper()
	if n, m := pageMarker(t, v.page(t).Title); n != 1 || m < 2 {
		t.Errorf("the reply shows page %d of %d, want page 1 of more than 1", n, m)
	}
	if !v.arrow(t, "◀").Disabled || !v.arrow(t, "▶").Disabled {
		t.Errorf("the reply has an enabled arrow, want both disabled")
	}
	if !strings.Contains(v.content, pagedListUnsavedLine) {
		t.Errorf("the reply says %q, want the unsaved-pages line", v.content)
	}
}

// When the store's save fails, a long list's reply shows page 1 with both
// arrows disabled and the unsaved-pages line. It sends no follow-up, and
// Sentry gets one event whose command is the command's name.
func TestALongListWhoseSaveFailsShowsPageOneAlone(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			rec := recordCaptures(t)
			down := errors.New("connection refused")

			assertUnsavedReply(t, runList(t, cmd, failingListStore{Store: store.NewFake(), saveErr: down}, loaRefDate))
			assertCaptureNames(t, singleCapture(t, rec, down), map[string]string{"command": cmd.name})
		})
	}
}

// With no store configured, a long list's reply shows page 1 with both
// arrows disabled and the unsaved-pages line. It sends no follow-up and
// nothing to Sentry.
func TestALongListOnAHostWithNoStoreShowsPageOneAlone(t *testing.T) {
	for _, cmd := range listCommands {
		t.Run(cmd.name, func(t *testing.T) {
			cmd.long(t)
			rec := recordCaptures(t)

			assertUnsavedReply(t, runList(t, cmd, nil, loaRefDate))
			if rec.count != 0 {
				t.Errorf("captures = %d, want none: %v", rec.count, rec.msgs)
			}
		})
	}
}
