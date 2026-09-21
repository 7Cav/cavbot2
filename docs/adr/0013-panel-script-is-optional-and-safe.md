# ADR 0013: The panel serves its own script, and a page without it is safe, not editable

## Status

Accepted. Decided in the triage of issue #343, before its implementation.

## Context

The hub page's pickers list every candidate as one line. The live guild is
near Discord's caps for roles and channels, so a picker runs to thousands of
pixels. The fix, a picker that shows only the selected set and opens a
search list to add one, needs script. The panel had shipped none.

## Decision

The panel ships one plain JavaScript file, served by the binary from its
static path the way it serves the font and the stylesheet. No framework, no
CDN, no build step.

A page must be safe when the script does not run. A picker then renders its
tags and carries the stored IDs; its add and remove controls do nothing; a
save leaves the selected set as stored. Every other field stays editable.
There is no second rendering for the no-script case.

## Why

The panel is a staff tool used on current browsers. A dead picker that
cannot change the stored set is an acceptable failure mode, and it is safe by
construction: the form posts what the page loaded. A checkbox fallback that
script upgrades would mean two renderings to build, test and keep aligned,
for a case nobody hits.

Serving the script from the binary keeps the panel's promise that no page
fetches from a third party, the same reason the font is embedded.

## How to apply

- Put page script in the panel's static directory and reference it from the
  layout. Never add a script tag that points off-host.
- A control that script drives must post the stored state when script has
  not run. Never render a control that, dead, would post an empty or partial
  set.
- Do not add a no-script editing path for a scripted control. If a control
  needs one, that is a new decision.
