---
status: accepted
---

# Discord activity is kept in the bot store as IDs and times, and shown to panel admins as group totals

**Amended 2026-11-20** (#972): the Joiners group sees cohort totals too. See
*Amendment: the Joiners group sees cohort totals*.

The Metrics page (#949) answers questions like "2-7 doesn't use Discord much"
and "new members go quiet at this rate". To do that the bot records who sent a
message, who was in voice and who joined or left, in its own Postgres. Each
row holds user, message, channel category and role IDs with times, never
message text, so the bot needs no Message Content intent. The page shows only
totals per section, per join cohort and for the whole server, and only panel
admins open it.

ADR 0011 sends usage to the Grafana stack. This is a different kind of usage.
Counting distinct people per category, per role held at a voice session's
start and per join cohort needs member identities, which Loki labels can't
carry without unbounded cardinality. The panel's group check already decides
who may see regiment data, so the page sits behind it.

Raw rows are kept 180 days, which covers 12 weeks of trends, 30-day retention
of new members and the 60-day lookback for "returned". Weekly totals per
section are kept with no limit, so long trends survive the pruning.

## Considered options

- **Grafana and Loki.** Fits ADR 0011, but distinct-people counts by category,
  role and cohort need identities as labels.
- **A third-party stats bot.** Member data would leave the regiment, and it
  can't read the regiment's sections or roles.
- **The Message Content intent.** Not needed. IDs, mentions and reply targets
  arrive without it.
- **Per-member views.** Rejected. The page is for group trends and must not
  feed requirements or promotions.

## Amendment: the Joiners group sees cohort totals (2026-11-20)

ADR 0022 shows the Joiners group the Joiners page's pipeline, which holds
activity totals for a whole join cohort. Panel admins are no longer the only
audience for those totals. They stay per cohort, never per member.
