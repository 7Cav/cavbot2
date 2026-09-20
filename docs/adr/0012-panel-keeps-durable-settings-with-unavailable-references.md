# ADR 0012: The panel does not silently discard durable settings with unavailable Discord references

## Status

Accepted. Decided in the triage of issue #315, before its implementation.

## Scope

Durable panel configuration: hub rows and moderator role assignments, per hub
and guild-wide. Spawned channel rows are outside it. The runtime deletes them
when the channel empties. That is a lifecycle, not configuration.

## Decision

A stored setting whose Discord object is gone, or is no longer eligible, stays
stored. The panel renders it as unavailable, with the reason, and a person
removes it. The panel never removes it on its own, whether at a page load,
a save or startup.

A save's validator accepts an ineligible role ID on one condition only: the
exact record being saved already stores it. An ID another hub stores, or the
guild-wide set stores, is refused for this record.

## Why

Once an operator has seen an unavailable role and kept it, the store cannot
tell deliberate retention from an overlooked record. Any later automated
cleanup would revoke authority after the panel had promised that only a
person removes it. That promise is what makes the decision hard to reverse.

A stored managed role is not inert. `isModeratorLocked` compares role IDs, so
the role keeps rename authority while it is stored. That matters most for a
managed role people hold, such as the booster role. The trade-off is:

- Preserve recorded operator intent, and never revoke authority silently.
- Preserve, for now, authority for a role an operator could not choose today.
- Carry a validator exception and a second kind of picker control until a
  person reviews the record.

Considered and not chosen:

- Drop the ID on the next save. This is what the role pickers did before
  #315, and it is the defect.
- Drop every stored ineligible ID once at startup, with a system change log
  entry. This decides for the operator.

A startup warning that names the records is a reporting decision, separable
from this one, and not decided here.

Broken hubs share the principle and differ in consequence. A broken hub can
only be removed, and it spawns nothing meanwhile. An unavailable moderator
role can be kept through a save, and it grants authority meanwhile.

## How to apply

- A stored reference the guild read no longer resolves, or that is no longer
  eligible, renders as unavailable with its reason, enabled and postable, so
  a person decides.
- A validator accepts an ineligible ID only from the stored set of the exact
  record being saved, read in the same request before validation.
- Never add a cleanup that removes such a reference without a person's save.
- To add operational reporting of unavailable references, write a separate
  decision.
