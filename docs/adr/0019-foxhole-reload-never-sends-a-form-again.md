---
status: accepted
---

# A reload of the Foxhole page never sends a form again

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
  so a reload there sends the form again.
