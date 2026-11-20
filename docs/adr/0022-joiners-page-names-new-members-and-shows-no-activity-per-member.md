---
status: accepted
---

# The Joiners page names new members and shows no activity per member

AO leaders want to welcome the people who join the Discord server for their
AO. The Joiners page (#231) lists each week's joiners by AO role, with their
names and join times, and only panel admins and the Joiners group open it.
It replaces a weekly DM that went to one person. That person left the
regiment, and the DM now fails (CAVBOT2-7).

A name and a join time are what a welcome needs, and Discord already shows
both on a member's server profile. Activity is different. ADR 0021 keeps it
as group totals so it can't feed requirements or promotions. The page shows
activity only in the pipeline, as totals for a whole join cohort, which stays
inside that rule.

## Considered options

- **Keep the DM.** One recipient, and when they left it reached no one.
- **A slash command.** A reply holds a week's AO table, its trends and the
  pipeline worse than a page does.
- **A channel post.** Discord roles, not the panel's group check, would
  decide who reads it, and it pushes to people who didn't ask.
- **Per-AO scoping.** Deferred. Every viewer sees every AO.
- **A "talked yet" column per member.** Rejected. It is activity per member,
  which ADR 0021 rules out.
