# Product

<!-- impeccable:product-schema 1 -->

cavbot2 is the 7th Cavalry Gaming Regiment's Discord bot and its web panel. This record covers the whole product. The panel is the only part with a visual design. GLOSSARY.md owns the terms used here, and `docs/adr/` owns the decisions. This file points to both rather than restating them.

## Platform

web

## Users

The panel at `cavbot2.7cav.us` has four kinds of visitor. Any forum user can sign in. The group check runs on every request and decides which pages each visitor sees.

- **Panel admins** are the forum groups Genstaff (71, Regimental HQ), S6 HQ (47) and Regimental Technical Aides (44), all held as secondary groups (#256). They set up hubs for temporary voice channels, the guild-wide moderator roles and the recording roles. Once recordings ship, they can also see and delete every recording. They open every page.
- **Foxhole managers** are the members of the forum's Foxhole group (323). They work the Foxhole page: notes, approved collaborators, adding and removing members, roster adds, and the purge and re-add when a war ends.
- **Recording starters** are Cav members holding a recording role. They will come to the panel from a link in a recording notice or a DM, sign in with their forum account, and list, download and delete their own recordings (#381). They are the panel's first visitors who aren't staff, and many will be opening it for the first time.
- **Outsiders** are any other forum users. They see only the page saying their roles grant no access.

In Discord, Cav members run the slash commands listed in the README. `/awol`, `/loa`, `/afsm` and `/s6-it-check` answer personnel questions from the milpacs and the forum. Members in temporary voice channels see the ownership, lock and recording notices.

## Product Purpose

The panel replaces three things: MEE6's dashboard for temporary voice channels, Craig for voice recording, and editing Foxhole roles by hand in Discord's role settings. Staff change these settings in one place, signed in with their forum account. Every change records who made it and what changed. The bot never drops a setting or a permission unless a person chose to drop it.

It's working when staff do these jobs without opening Discord's settings screens or a third-party bot's dashboard, when every change can be traced to the forum user who made it, and when a starter gets from the Discord link to their recording without help.

## Positioning

The panel runs inside the bot and signs people in with the regiment's own forum account. It reads the regiment's own records: forum groups, milpacs, unit rosters and the rank ladder. MEE6 and Craig can't know who is in Genstaff, which troopers belong to D/ACD, or which forum user changed a hub. The panel also speaks the regiment's vocabulary (hub, spawned channel, purge, approved collaborator, starter), defined once in GLOSSARY.md.

## Operating Context

- **Sign-in.** Visitors go from 7cav.us to the forum's OAuth page and back to the panel. The panel should read as part of the same site the whole way.
- **From Discord to the panel.** A recording notice links to the recording in the panel when the recording stops. If the channel is gone by then, the starter gets the link by DM instead. That link is often opened on a phone.
- **The war cycle.** A Foxhole war ends with a purge of the Foxhole roles, then a re-add of the approved collaborators. These are the panel's busiest moments for Foxhole managers.
- **Scale.** The live guild is near Discord's caps for roles and channels, so pickers can hold thousands of candidates (ADR 0013).
- **Latency.** At its slowest a page load takes about 20 seconds: a 10-second group check, then the page's 10-second time budget. The reverse proxy cuts requests off at 90 seconds. Foxhole actions run in the background, and their request redirects at once.
- **Release.** A change members or panel users can see gets smoke-tested on a test guild before the release that ships it (ADR 0015, `docs/smoke-test.md`). Panel checks run in the desktop app's built-in browser.

## Capabilities and Constraints

Shipped:
- Forum sign-in and the group check.
- The hub page: list hubs with live counts, create, register, edit and remove a hub, the guild-wide moderator roles, the recording roles, and the change log.
- The Foxhole page: the holder list with flags and notes, approved collaborators, add, remove, roster add, purge, re-add, retry, reports and its own change log, plus a second view of members who have a note but no Foxhole role.

Planned, not built:
- The Recordings page (#390), deleting a recording early (#391), the 30-day retention (#392), and turning recording on in the live guild (#564).

Constraints:
- **Rendering.** The panel is Go HTML templates plus one stylesheet and one plain JavaScript file, all embedded in the bot binary. There is no framework, no build step and no CDN, and no page fetches from a third party, which is why the font is embedded (ADR 0013).
- **Pages without script.** A page stays safe when its script doesn't run. A scripted control posts the stored state rather than an empty or partial one, and there is no second rendering for the no-script case. The Foxhole note is the one exception (ADR 0013).
- **Stored references.** A stored setting whose Discord object is gone or no longer eligible stays stored and is shown as unavailable, with the reason, until a person removes it (ADR 0012).
- **Saves.** A preview comes before every Confirm, and a Confirm acts only on what its preview showed. A preview or save that would change nothing says so and stops. A save ends in a Post/Redirect/Get, and the GET works out the result again (CODING_STANDARDS.md).
- **Error data.** Nothing the panel sends a browser, and no reply in Discord, carries error data except a time (ADR 0016, ADR 0017).
- **Deploy probe.** The release workflow's deploy probe reads the signed-out rail's version line. Its text must stay exactly `cavbot2 <version>`, as the comment in `panel/templates/layout.html` explains.
- **Test hooks.** Templates carry `data-*` attributes (`data-field`, `data-cause`, `data-failure`, `data-nav`) that the panel tests read. Keep them through visual changes.

## Brand Commitments

- **Names.** The product is cavbot2. The regiment is the 7th Cavalry Gaming Regiment, 7Cav for short.
- **Look.** The panel matches 7cav.us as its stylesheet stood on 2026-09-17, the snapshot recorded in the header of `panel/static/panel.css`. The layout is variant D of the approved prototype in #288: forum-style pages inside a left rail. That look is frozen. Checking the forum for later changes is out of scope, and the maintainer starts any redesign. Where a frozen forum colour fails WCAG AA, a lighter tint replaces it. A forum part changes only where the panel needs a state the forum doesn't draw, or where AA requires it. So far that means a red header rule and a filled danger button on a danger confirm, a neutral stripe on a notice that isn't a refusal, and field borders that meet AA's 3:1 for non-text contrast.
- **Rejected looks.** #288 rejected a centred gate over the map with Teko display type, and a split-screen sign-in with Teko titles.
- **Voice.**
  - Copy uses the terms GLOSSARY.md defines and never the ones it lists under "Avoid". For example, it's "Cav member" rather than "trooper", and "panel" rather than "dashboard".
  - A refusal says that nothing changed.
  - Every message is fixed text plus any member input it names, never error details.

## Evidence on Hand

- **Brand assets.** The forum's own files, in `panel/static/`:
  - the logo `logo-m.png`
  - the favicons `icon-16.png`, `icon-32.png` and `icon-48.png`
  - the map background `page-bg2-c1.png`
  - Roboto, as `roboto-latin.woff2` (Apache 2.0)
- **Approved prototype.** Branch `chore/288-panel-prototype`, file `panel/prototype/index.html`. It's throwaway and was never merged.
- **No usage data.** Umami tracks neither 7cav.us nor the panel. The only phone-width check on record is in #463. Don't present usage numbers, device shares or user quotes, because none exist.

## Product Principles

1. **A person decides every removal.** The panel never drops a stored setting, an approval, a note or someone's authority on its own. A refused action changes nothing and says so.
2. **Show it before changing it.** Removing a hub and every Foxhole action go through a preview first, and the Confirm acts on what the preview showed. Re-add is the one exception. It runs at once, because it only gives External back to approved collaborators a person already chose, and its block shows the count before it runs. A form save, such as a hub's settings or a note, has no preview. The form shows what it will store, and the change log keeps the old values. The heaviest confirm and the fullest report belong to the riskiest action. Today that's a purge, which takes a role from every holder at once. Once recordings ship, it's deleting a recording, the one panel action nobody can undo.
3. **Every change names who made it.** Each save and each Foxhole action leaves a change log entry with the forum user, the time, and the old and new values.
4. **The least practised visitor comes first in the parts everyone sees.** The rail, sign-in, the no-access page and shared wording must make sense to a recording starter who has never opened the panel and doesn't know what a hub is. Each page then serves its own audience, so staff pages can stay dense.
5. **Desktop first for staff, phone first for starters.** Staff pages are built for a desk and must still work at phone width, with no sideways scroll and every action reachable. The Recordings page is designed at phone width first, because its link arrives in Discord.

## Accessibility & Inclusion

The bar is WCAG 2.2 AA. A status is never shown by colour alone: every flag, tag and outcome carries words, so a colour-blind manager reads "No rank role" and "Not in the server" as text. Every new colour token gets a contrast check. The current tokens pass where they're used. Body text is 6.9:1 and muted text 5.4:1 on the block background. The red and green tokens (3.7:1 and 4.3:1) appear only as borders, with light text on top.
