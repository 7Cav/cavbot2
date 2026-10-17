---
status: accepted
---

# A reload of the Foxhole page never sends a form again

**Amended 2026-10-17**: the rule covers every panel page; see *Amendment:
every panel page follows the rule*.

A Foxhole form that saves or starts something answered a refusal with the
page itself, and the add and roster previews answered their POST the same
way. The tab's history entry was then the form, so a reload sent it again. The
reload undid every box the manager had ticked or unticked since. Once whatever
refused the form had cleared, the reload saved or started what the form named.
In the triage of #770, a reload of a refused remove Confirm took External from
a member the manager had unticked.

On the Foxhole page a reload now never sends a form again. Each page a Foxhole
form answers with has an address that shows it. When the page loads, the page
script puts that address in the tab in place of the form's history entry, and
keeps it current as boxes change. A reload shows the page as it stands then,
without the refusal note. The roster preview is a GET of the Foxhole page with
the unit in its address.

The add preview's paste stays out of its address. The paste box has no line
cap, so a whole allied regiment goes in one add, and nginx in front of the
panel turns away a request line over 8KB by default. The tab's session storage
keeps the paste, the way it keeps a typed note through a sign-in, and the
script loads the preview again in the background. A refused note save keeps
its typed text the same way.

Without script, a reload sends the form again after the browser asks, as
before. ADR 0013 accepts that, since the page posts what the manager pressed.
Decided in the triage of #770.

## Considered options

- **Redirect after a refusal, on the server.** It covers pages without script.
  But every other refused save in the panel answers with its page and a 4xx
  status, and a redirect needs a way to show the refusal note once. A note
  carried in the query comes back on every reload.
- **Change the address only after a box changes.** A reload of an untouched
  refused remove Confirm would still remove the role once the action that
  blocked it ended.
- **Put the paste in the add preview's address.** A long paste would get a
  414 from the proxy instead of a preview.
- **Keep the paste on the server as a draft.** It works without script, but
  it is new server state, which a restart loses unless it goes in Postgres.

## Consequences

- A new Foxhole form follows this rule. The page it answers with needs an
  address that shows it, and the tab keeps whatever an address can't carry.
  A form that copies the rest of the panel's refusal answer brings the bug
  back.
- The panel's other pages still answer a refused save at the form's address,
  so a reload there sends the form again. *Superseded: see the amendment
  below.*

## Amendment: every panel page follows the rule (2026-10-17)

The triage of #775 found the Hubs and Settings pages answering a refused save
the way the Foxhole page did. In a headless run, Discord refused a hub's
rename. The admin put the old name back on the page that came back, Discord
recovered, and a reload saved the form as first sent. The hub's channel took
the name the admin had dropped. A reload of the rank roles preview sent its
save again too. The code answers a failed save, and a 403, 404 or 502 to a
form, the same way.

### Decision

On every panel page, a reload never sends a form again. Each page the panel
renders in answer to a form names an address that shows it, and the page
script puts that address in the tab, as on the Foxhole page. A failed save's
error page names the address its Try again opens. A save that fails with a
500 gets the panel's error page and its Try again, not a line of plain text.
A 400 and a cross-origin refusal stay plain text, since the panel turns a
resend down the same way. A panic and a failed render stay plain text too,
since the error page might not render either.

The rank roles preview is a GET of the Settings page, with each rank's role
and the form's version in the address. With every rank filled, that is about
a thousand characters, well under the 8KB the proxy allows. A rank roles save
that needs a preview redirects there, so a reload of the preview never saves,
with or without script. That address always shows a preview with its Confirm,
whatever the counts are by then. A preview has no refusal note, so this
redirect avoids what ruled out redirecting after a refusal.

What a panel user typed into a form and has not saved is an unsaved edit. The
page script keeps a form's unsaved edit in the tab's session storage, under
the signed-in forum user, from the moment the form is sent. It comes back
marked not saved yet on a reload, Back or Forward, on a failed save's Try
again, and on the page a sign-in returns to. It keeps the version the page
carried, so the panel still refuses a save over a newer save as stale. Any
other link opens the form as stored. The marker links to the form as stored,
and following that link drops the unsaved edit. An unsaved edit goes once its
form saves, once its page shows the form as stored, or when the tab closes. A
reload shows no refusal note. One mechanism keeps every unsaved edit, the
Foxhole page's typed notes and add paste among them.

Without script, a reload of any page but the rank roles preview sends the
form again after the browser asks. ADR 0013 accepts that. Decided in the
triage of #775.

### Considered options

- **A reload that shows the form as stored.** A stale refusal tells the admin
  their values are still in the form. A reload to look at the other admin's
  save would wipe them.
- **Keep the rank roles in the tab, as the add preview keeps its paste.** The
  ladder fits an address, and an address works without script and in a new
  tab.

### Consequences

- Every new panel form follows the rule. Each page it answers with names an
  address that shows it. A Go test goes through every route a form posts to
  and fails on a page that names none, so a form that renders its answer the
  old way fails CI.
