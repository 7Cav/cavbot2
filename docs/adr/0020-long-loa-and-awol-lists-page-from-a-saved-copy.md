---
status: accepted
---

# A long /loa or /awol list pages in one message from a saved copy

A /loa or /awol list too long for one embed goes out as one public message
that shows one page, with ◀ and ▶ buttons that switch the page in place. It
replaces the follow-up messages /loa has sent since #826 and /awol gets from
#823. Those spread a battalion's list over several public messages, and
anyone's chat line can land between them. ◀ is greyed out on the first page
and ▶ on the last. /awol pages a list of any length, and sends its text file
only when staff set force_file_output.

When the command runs, the bot saves the run's pages in its Postgres, and each
press reads its page from that saved copy. Every page then comes from the run
the title's timestamp names. A press answers without waiting on the 7Cav API,
and the arrows keep working through a deploy or restart. The buttons name the
saved copy, so a free-text position never has to fit in the 100 characters
ADR 0007 allows a CustomID.

Only the member who ran the command flips the public message. Anyone else's
press opens a private copy at the page they asked for. That copy is an
ephemeral message with its own arrows, read from the same saved pages. Two
leaders can read one list at their own pace, and nobody posts a second copy to
the channel to reach page 4.

A list's saved pages last 30 days from the run, the window recordings already
use, and a sweep deletes them after that. A press on an expired list gets a
private note that the list has expired and to run the command again, and the
message stays as it was. A month-old /awol or /loa has little left to tell
anyone, since LOAs open and close every day.

The bot always runs with its Postgres, even though an unset BOT_DB_DSN turns
the store off. So a list whose pages can't be saved gets a short reply, not a
second way to send the list. Whether the save fails or no database is
configured, the reply shows page 1 with both arrows greyed out and a line
saying the other pages couldn't be kept and to run the command again. A failed
save goes to Sentry. The change that adds the arrows removes the follow-up
messages from both commands. /awol's force_file_output still sends the whole
list, because the file needs no database.

Decided in the triage of #824, before its implementation. Until #824 ships,
/loa sends a long list over its reply and public follow-ups, /awol does the
same once #823 ships, and /awol sends its file for a list over 10 pages.

## Considered options

- **Run the lookups again on every press.** The bot keeps nothing, but each press
  waits on the roster and LOA lookups, and the list can shift between presses.
  A trooper who posts while a leader reads page 2 drops off the list, and page
  3 skips someone. /awol's day figures also change at UTC midnight while the
  title's timestamp stays put.
- **Keep the pages in the bot's memory.** The cheapest option, but every reply
  posted before a deploy or restart ends up with dead arrows.
- **Any press flips the public message.** Two readers would keep moving the
  page under each other.
- **Only the member who ran it can press, and anyone else is refused.** A
  second reader runs the command again to reach page 4, which brings back the
  channel clutter paging removes.
- **Fall back to #823's follow-up messages when a save fails.** A leader would
  still read every row during a Postgres outage. But the bot always runs its
  database, and the fallback would keep a second way to send a long list, with
  its own tests, for a case nobody expects.
- **Keep saved pages forever.** The bot needs no sweep, but the table grows
  with every long run and keeps pages for messages deleted long ago.
- **Arrows that wrap around.** The last page's ▶ would go to page 1, and a
  reader could no longer tell from the arrows that the list had ended.
