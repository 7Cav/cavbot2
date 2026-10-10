# CavBot2

The language of cavbot2, the 7th Cavalry Gaming Regiment's Discord bot and
web panel. Define a new term here rather than in a code comment.

## Organization

**7Cav**:
The 7th Cavalry Gaming Regiment, the gaming community this bot serves. Its
wiki at https://wiki.7cav.us/ is the member-facing reference for the
regiment's structure and terms.

**Cav member**:
A Discord member who holds a rank role, one of the 29 roles on the rank
ladder. The two descriptions name one set: every Cav member holds a rank
role, and every rank-role holder is a Cav member. Member-facing copy says
"Cav member". The handover rule and the code say "rank role", the test the
bot applies.
_Avoid_: rank holder (in copy), trooper (in copy), verified member, Cav.

**Department**:
One of the twelve parts of the regiment /afsm checks: S1 (Personnel), S2
(Intelligence), S3 (Operations), S5 (Public Affairs), S6 (Information
Systems), S7 (Training), WAG (Wiki Admin Group), RTC (Recruit Training
Command), RRD (Regimental Recruiting Department), MP (Military Police), ODS
(Officer Development School) and NCOA (Non-Commissioned Officer Academy).

**Position**:
A trooper's assignment as their milpac records it, in free text such as
2/B/1-7, Reservist or a department code like S1.

**Position group**:
A unit as the milpacs name it, grouping its positions, such as ACD at
battalion level or D/ACD, a company within it.

**Zulu**:
UTC, the regiment's standard time, written with a z suffix as in 2300z.
Wherever the bot decides which day something falls on, as in counting
accountable days, a day is a UTC calendar date.
_Avoid_: GMT.

## Member records

**Milpac**:
One trooper's record in MILPACS, the regiment's personnel record system.

**Forum username**:
A member's handle on the forum, also stored on their milpac. The bot finds a
member's milpac by it.

**Roster**:
The troopers the milpacs list under a department or position group, such as
everyone in S1 or in D/ACD.

## Forum + LOA

**Forum**:
The regiment's Xenforo web forum. Members file PAFs there, and a post there
is what keeps a trooper from going AWOL.

**LOA**:
Leave of absence: a trooper's notice, filed as a PAF in a forum thread of
its own, that they will be away for a date range. It belongs to its Subject,
and a second LOA is always a new thread.

**PAF**:
Personnel Action Form: the forum form a personnel action is filed on, such
as an LOA request, which names a Submitter and a Subject. The bot reads a
PAF by its field labels, so renaming one on the forum changes what the bot
can read.

**Subject**:
The trooper an LOA is for, the one going on leave. The bot attributes the
LOA to its Subject and nobody else.
_Avoid_: username (bare), because PAFs have carried the Submitter and the
Subject under the same Username label.

**Submitter**:
The account that files a PAF. For an LOA, the Subject themselves or someone
filing for them, such as their squad lead.
_Avoid_: author, poster.

**LOA node**:
A forum section that holds LOA threads.

**LOA cache**:
The bot's copy of the forum's LOAs, each kept until a year after it ends and
refreshed every 15 minutes. When it is unavailable, /awol counts days with
no LOA subtracted and says so.

## Eligibility / tracker concepts

**AFSM**:
The Armed Forces Service Medal, given for service in a department. /afsm
lists the members of one department who have served in it for a year and
have no AFSM for it from the past year.

**S6 IT full status**:
Full membership of the S6 IT team, which a probationary member is promoted
to. /s6-it-check lists the members who have held an S6 IT position for six
months.

**AWOL**:
A trooper with more than 7 accountable days. The regiment expects a forum
post at least once a week, and any post counts. AWOL comes from forum posts
alone, never from the milpac.

**Accountable day**:
A UTC calendar date after a trooper's last forum post, up to and including
today, that none of their LOAs covers.

**Accuracy disclaimer**:
The warning at the top of every /afsm reply, with or without names, that the
list may be wrong because milpac records are typed by hand.

## Member welfare

**Helpline card**:
The crisis and mental-health services /helpline posts, optionally addressed
to a member. Every service on it is an outside organisation. The regiment
names no internal crisis contact, since no volunteer roster is staffed,
trained and always available the way those services are. A changed phone
number or dial sequence on it is a correctness fix, not a copy edit.

## Foxhole roles

The Discord roles the regiment gives members for the game Foxhole, and the
panel page and `/foxhole` commands that manage who holds them. What a Foxhole
role unlocks is set in Discord, outside the bot.

**Foxhole role**:
A Discord role the `/foxhole` commands and the Foxhole page give and take by
its exact name, Internal or External. The bot answers for who holds it,
never for what it unlocks.
_Avoid_: Warden role (a faction the regiment no longer plays).

**Internal / External**:
The two Foxhole roles, Internal for Cav members and External for outside
collaborators from allied groups. The bot enforces neither meaning and checks
no rank role before an Internal grant.
_Avoid_: access tier.

**War**:
One Foxhole war, the game's campaign from its start to a victory. Foxhole
role membership lasts one war.
_Avoid_: season, round.

**Purge**:
Taking a Foxhole role off every holder in one Foxhole action when a war ends.
Re-adding the approved collaborators afterwards is a separate Foxhole action.
_Avoid_: reset, wipe, clear.

**Approved collaborator**:
An outside collaborator marked on the panel to get External back after every
purge, such as an allied group's leader. The mark outlives a purge and the
member leaving the server, and only a person clears it, by hand or by
removing their External on the panel.
_Avoid_: locked (a spawned channel's term), whitelisted, pinned, preset.

**Note**:
A Foxhole manager's free text about one Discord member, one per member,
started only on a Foxhole role holder or an approved collaborator. It
outlives a purge, the member losing the role and the member leaving the
server, and only a person changes or clears it.
_Avoid_: comment, remark, reason (Discord's audit log word for why a change
was made).

**Validated internal unit**:
A position group whose current roster the regiment treats as belonging in
Internal, such as D/ACD. Not every unit is one.

**Foxhole group**:
The one forum group whose members are Foxhole managers. Who is in it is
decided on the forum.
_Avoid_: warden group, warden allowlist.

**Foxhole manager**:
A signed-in forum user in the Foxhole group, such as a Foxhole leader, who
opens the Foxhole page. A panel admin can do everything a Foxhole manager
can.
_Avoid_: warden manager, warden admin, Foxhole leader (as the panel's name
for the role).

**Foxhole page**:
The panel page that lists every Foxhole role holder and approved
collaborator with their notes, and starts Foxhole actions. A second view
lists the members with a note who hold no Foxhole role and aren't approved
collaborators.
_Avoid_: warden page, warden dashboard.

**Holder list**:
The Foxhole page's list of every Foxhole role holder and every approved
collaborator, read from the member list as it stands when the page loads.
_Avoid_: roster (a unit's roster), member list (the bot's copy of the whole
guild).

**Flag**:
A display-only mark on a holder list row that points a Foxhole manager at a
member worth a second look. The marks are "no rank role" on an Internal
holder, "not in the server", and "approved, doesn't hold External".
_Avoid_: warning, alert, issue.

**Member list**:
The bot's copy of every member of the guild, which the holder list is read
from, partial after each connect until Discord's last part arrives. A
resumed session keeps it.
_Avoid_: member cache, roster (a unit's roster is a different list).

**Foxhole action**:
One change to who holds the Foxhole roles, made on the Foxhole page or by a
`/foxhole` command: an add, a removal, a roster add, a purge or a re-add of
the approved collaborators. One started on the page never runs alongside
another from either path, though two commands may still run together.
_Avoid_: bulk action, job, task, operation, lock (a spawned channel's
state, not the rule that keeps two actions apart), starter for who started
one (a recording's).

**Report**:
The record of one Foxhole action started on the Foxhole page, and that
action's change log entry. It names who started the action and how it
ended, and lists the members it changed, skipped, failed on and never
attempted.
_Avoid_: summary, results, receipt.

## Temporary voice channels

The bot replaces MEE6's "Temporary Channels" plugin. A member joins a hub, the
bot creates a spawned channel for them, and the spawned channel is deleted when
it empties: at once, or after its hub's delete delay. Hub settings are edited
in the panel, never in code.

**Hub**:
A voice channel that, when a member joins it, causes the bot to create a
spawned channel and move the member into it. Each hub carries its own
settings. MEE6 calls this "join to create".
_Avoid_: join-to-create channel, creation trigger, hub voice channel.

**Spawned channel**:
The voice channel a hub creates for the member who joined it. Named from the
hub's base string plus a per-hub number.
_Avoid_: temp channel, temp VC, temporary channel, personal channel.

**Owner**:
The occupant a spawned channel belongs to, or nobody. Owning is what lets a
member rename or lock it, as far as its hub allows each; a moderator role
does both without owning. The owner always holds a rank role. The creator at
first, if they hold one; after a handover, whoever the handover named. A
bot-internal marker that grants no Discord permission.
_Avoid_: interim controller, controller, creator (for the current owner).

**Handover**:
The bot giving ownership of a spawned channel to an occupant. When the owner
leaves, the highest-ranked occupant with a rank role takes over, ties broken
by lowest user ID; with no such occupant the channel has no owner. When a
rank-role holder joins a channel with no owner, they take over. A handover
is final: a returning creator is an ordinary occupant.
_Avoid_: hand off, hand back, succession, transfer, loan.

**Restart sweep**:
The bot's check, when it connects, of every spawned channel it holds a
stored record of against the guild. A recorded channel that is gone loses
its record. An empty one is deleted with its record once its hub's delete
delay passes with nobody joining, counted from when it emptied if the bot
saw that happen, and otherwise from the sweep. An occupied one is tracked
again, its owner restored or elected by the handover rule. A channel with no
record is never touched.
_Avoid_: adoption, orphan sweep, recovery, reap, resync.

**Spawn in flight**:
A spawned channel from the moment Discord confirms its create until its row
write or compensating delete finishes. A restart sweep that overlaps this
interval leaves the channel alone until that sweep also finishes.
_Avoid_: pending spawn, unsettled channel, protected channel, marked
channel.

**Stale voice state**:
The bot's record of which channel a member is in, or of who is in a spawned
channel, at a moment when Discord has already reported a change the bot has
not yet applied. Discord reports changes in order; the bot applies them in
no fixed order, so the record can lag. Before it creates, moves or deletes,
the bot checks Discord's current state and goes ahead only if the record it
decided on still holds at that check.
_Avoid_: stale event, out-of-order event, late event, race.

**Ownership notice**:
The bot message in a spawned channel's text chat that names the current
owner, or says there is none. Posted when the channel is created and at
every handover. It pings nobody.
_Avoid_: announcement, banner, status line, welcome message.

**Moderator role**:
A Discord role that may rename, lock and unlock any spawned channel it
covers without owning it, as far as the hub allows each, and that no lock
keeps out. Set per hub, or once for every hub; a hub's moderator roles are
the union of the two. Grants no Discord permission beyond getting past a
lock.
_Avoid_: staff role, admin role, global moderator, default moderator.

**Lock**:
A spawned channel's state in which only its guests and its hub's moderator
roles may join. Everyone else still sees the channel, with Discord's
padlock, and cannot read its text chat. The owner or a moderator locks it,
on a hub that allows locking. It belongs to the channel, not to whoever set
it, and lasts until someone unlocks it or the channel is deleted.
_Avoid_: private channel, closed channel, hide (a hidden channel is out of
sight; a locked one is not).

**Guest list**:
The members a locked channel admits: everyone who has been inside it since
it locked, however they got in, and everyone let in. Each lock starts a new
one; unlock clears it. A guest is one member on it.
_Avoid_: allowlist, whitelist, permit list, invite list.

**Let in**:
To put a member who is outside a locked channel on its guest list. Done by a
moderator or by whoever locked the channel. It never shows a member a
channel they could not already see.
_Avoid_: admit, invite, permit, allow.

**Lock notice**:
The bot message in a locked channel's text chat that names who locked it and
carries the controls to unlock it and to let someone in. Posted at each
lock. At unlock it is edited to name who unlocked the channel, and loses its
controls.
_Avoid_: lock panel (the panel is the web UI), control panel, tool, widget.

**Knock channel**:
A spawned channel whose name starts with 🚦, asking members outside to knock
before they join. A courtesy the bot does not enforce: a knock channel keeps
nobody out, where a lock does. The name is its only record.
_Avoid_: soft lock, do not disturb, knocked channel, knock (for the channel
or its 🚦).

**Knock**:
A member outside a knock channel asking the people inside whether they may
join. Members do it among themselves; the bot plays no part.
_Avoid_: request to join, let in (a lock's term, and done by the bot).

**Eligible role**:
A live Discord role that is not managed and is not `@everyone`. The only
kind a moderator picker or the recording roles picker offers, and the only
kind a save may add.
_Avoid_: offered role, valid role, pickable role, selectable role.

**Unavailable moderator role**:
A stored moderator role that is no longer eligible, kept with its authority
until a person removes it in the panel. Two reasons: deleted, when the role
is gone from Discord, and managed. A stored recording role that is no
longer eligible is kept the same way.
_Avoid_: legacy role, stale role, orphaned role, ghost role.

**Permission source**:
The per-hub setting that chooses what a spawned channel inherits its
permissions from, the hub's category or the hub channel itself.
_Avoid_: sync, category sync, synchronize permissions.

**Delete delay**:
How long a spawned channel must stay empty before the bot deletes it. Set
per hub, and read as it stands now, so a change reaches the channels already
waiting; none means the moment the channel empties. A join cancels the wait,
and the next empty starts it again.
_Avoid_: keep alive (MEE6's term), grace period, linger, timeout, cooldown.

**Register**:
To make an existing voice channel a hub through the panel. The other way a
hub comes to exist is the panel creating the channel itself.
_Avoid_: adopt (for a hub), import, link, attach.

**Disabled hub**:
A hub that keeps its channel and its settings but spawns nothing. A join to
it does nothing. Its spawned channels live on until empty.
_Avoid_: paused hub, inactive hub, archived hub, hub off.

**Broken hub**:
A hub whose channel is gone from Discord or has no category. Discord's state
makes it so, never a panel setting, and the panel works it out each time it
shows the hub and never stores it. A join can reach only the no-category
kind, and it spawns nothing.
_Avoid_: orphaned hub, stale hub, dead hub, unhealthy hub.

**Spawn failure**:
A join to an enabled hub that ends with no spawned channel for the member.
Two kinds: a failure Discord returned on the create or the move-into, and a
refusal the bot decided itself, which only a broken hub causes.
_Avoid_: create failure, failed create, failed join, refused join.

**Save**:
One submitted panel form that changes what the panel stores: a hub's create,
register, update or remove, the guild-wide moderator roles, the recording
roles, a note, or an approval given or cleared. It begins once the group
check allows it, runs to its end whether or not the browser waits, and takes
effect when the store holds the change. A refused save changes nothing. A
save that fails leaves the store as it was, though a Discord change it
already made may stay.
_Avoid_: submit, submission, write, commit.

**Change log**:
The panel's record of each change made through it: who made it, when, and
what changed. Every save that takes effect has one entry, holding the old
and new values. Every Foxhole action started on the Foxhole page has one
too, its report. Each hub's form shows the hub's own entries, the guild-wide
moderator section and the recording roles section each the entries of its
own saves, which reference no hub, and the Foxhole page its own. A change
made by a command or by hand in Discord has none.
_Avoid_: audit trail, audit log (that is Discord's), history.

**Stale form**:
A hub's edit form, the guild-wide moderator section or the recording roles
section, loaded before another save of the same settings took effect. A save
from it is refused. A rename made in Discord does not make a form stale.
_Avoid_: conflict, collision, race, outdated form, edit conflict.

**Rank ladder**:
The rank roles in seniority order, most senior first, that the handover rule
ranks occupants by.
_Avoid_: rank list, rank table, seniority list, role ladder.

**Panel**:
cavbot2's web UI at `cavbot2.7cav.us`. Any forum user can sign in through
the forum, and the group check decides which pages each one sees.
_Avoid_: dashboard, admin UI, settings screen, config.

**Panel session**:
The panel's record that a browser is signed in as one forum user. Created at
the OAuth callback, for any forum user. Ended by sign-out, or by the forum
refusing the token or refusing to say who the user is. A group check that
grants nothing leaves it running.
_Avoid_: login, token session, forum session, auth cookie.

**Pending sign-in**:
The panel's record of one sign-in between the redirect to the forum and the
callback. Consumed by the callback, whatever its outcome, or dropped after
five minutes.
_Avoid_: auth request, login attempt, OAuth state, flow.

**Group check**:
The panel's test of the signed-in forum user's forum groups, primary or
secondary, on every request. The groups decide what the panel session can
see. They never end it. One of the panel's admin groups makes them a panel
admin. The Foxhole group makes them a Foxhole manager. An outsider sees
only a page saying their roles grant no access.
_Avoid_: allowlist check, permission check, authorisation, role check.

**Panel admin**:
A signed-in forum user the group check finds in one of the panel's admin
groups. Opens every page and takes every action on the panel.
_Avoid_: admin (bare), staff, allowlisted user, moderator (a spawned
channel's), Foxhole manager (a narrower role).

**Outsider**:
A signed-in forum user the group check finds in none of the panel's groups,
neither a panel admin nor a Foxhole manager.
_Avoid_: user outside the admin groups (a Foxhole manager is one too), user
in neither group, user in no group, guest, unprivileged user.

**Block**:
One bordered unit of a panel page, with a gold header rule. The unit a
page's layout rules bound.
_Avoid_: card and section (for a page unit; the helpline card is a Discord
message), widget.

**Tag**:
One selected item as a picker shows it, with its remove control. A role tag
is a moderator picker's or the recording roles picker's, and shows a stored
role that is no longer eligible with its reason.
_Avoid_: chip, pill, badge.

**Search list**:
The list of candidates a picker's add control opens, filtered as the person
types. A moderator picker's or the recording roles picker's role search
offers the eligible roles not yet selected. The register picker's channel
search offers the voice channels that are not hubs.
_Avoid_: dropdown, popover, menu, combobox.

## Voice recording

Designed in #10, not built yet. The bot replaces Craig. A member starts a
recording of the voice channel they are in, a recorder joins it, and the bot
keeps what each speaker said as a separate track.

**Recording**:
The audio of one voice channel captured by a recorder from the moment its
starter starts it until it stops, kept as one track per speaker and a mix.
Deleted 30 days after it stops, or sooner, whole, by its starter or a panel
admin.
_Avoid_: session, capture, craig (as a verb).

**Starter**:
The Cav member holding a recording role who started a recording. Apart from
panel admins, the only person who can pull it from the panel.
_Avoid_: owner (a spawned channel's), host, requester, recorder.

**Recorder**:
A Discord account, separate from the bot's own, that joins a channel to
record it and is in voice only while it records. Each records one channel at
a time, so a recording starts only when a recorder is free. Temp VC never
counts a recorder as an occupant. With none configured, nobody can record.
_Avoid_: recording bot, Craig, the bot (for this account).

**Recording role**:
A Discord role whose holders, if Cav members, may start a recording, and may
stop one in a channel they are in. Set once for the guild in the panel. With
none set, nobody can record.
_Avoid_: recorder role, record permission, recording permission.

**Track**:
One speaker's audio for the whole of a recording, silent wherever they were
not talking, so every track in a recording lines up from its start. A
speaker who leaves and rejoins keeps one track.
_Avoid_: stream, channel (that is Discord's), file.

**Speaker**:
Anyone whose voice a recording captured. Not necessarily a Cav member: an
applicant in an S2 interview is a speaker. A recruit holds the `RCT` rank
role, so is a Cav member; an applicant holds none.
_Avoid_: participant, attendee, member (for this role).

**Mix**:
One file of every track in a recording played together, built when the
recording stops.
_Avoid_: mixdown, combined track, merged audio.

**Info file**:
The text file kept with a recording, and in its zip, that names its title,
channel, starter, start and stop times in Zulu, how it ended, and each
speaker's display name and Discord ID.
_Avoid_: metadata, manifest, readme.

**Cut-short recording**:
A recording that ended because the bot shut down or crashed, not by a stop,
the time cap, or the last person leaving. What it captured up to then is
kept.
_Avoid_: failed recording, aborted recording, broken recording.

**Recording notice**:
The bot message in the recorded channel's text chat that says a recording is
running, names its starter, and carries the control to stop it. Posted at
start, and edited at stop to say it stopped, or that it was cut short, with
a link to the recording in the panel. If the channel is gone by then, the
starter gets that link by DM instead.
_Avoid_: recording panel (the panel is the web UI), banner, announcement.

## Slash commands

**Refusal**:
A command's reply that turns a run down before the command acts on it: bad
input, a run outside a server, or a Foxhole action running on the Foxhole
page. Only the member who ran it sees it, and nothing changed.
_Avoid_: rejection, error reply (which also covers a run that failed after
it started).

## External systems

**7Cav API**:
The regiment's web API at api.7cav.us, where the bot reads milpacs and
rosters.

**Bot Postgres**:
The bot's own database, and the only one it writes to.

## Observability

Usage goes to the regiment's Grafana stack at metrics.7cav.us, and failures
go to Sentry. Neither one carries the other's signal (ADR 0011).

**Command telemetry**:
The usage record of one slash command run: which command, how long it took
and who ran it. It never records a failure.

**Missed acknowledgement**:
An interaction Discord dropped because the bot's acknowledgement did not
reach Discord within 3 seconds of the interaction's creation. The member
sees "The application did not respond".
_Avoid_: expired interaction (the 15-minute limit on editing a reply is a
different limit), timeout.

**Abandoned page load**:
A panel page load whose connection closed before the panel answered and
before the page's time budget ran out. Usually the browser left, by
navigating away, reloading or closing the tab. It is expected, not a
failure, so it leaves an INFO line and no Sentry event. A page still loading
when its budget runs out has failed, and is not an abandoned page load.
Neither is a load whose read failed for its own reason as the connection
closed.
_Avoid_: browser leaving, client disconnect, cancelled request, timeout.

**Time budget**:
The one deadline shared by every read a panel page load makes, and by any
wait for Discord to send the bot the guild's data or the member list,
starting when the page begins reading. A load still reading when it runs out
fails at once, whatever read it is waiting on, and the page says it took too
long. A load still waiting on Discord fails at once too, and the page says
Discord has not sent the data. The Foxhole page is the exception. It shows
everything that doesn't need the member list, with a notice in place of the
rest.
_Avoid_: page timeout, deadline.

**Failed start**:
A start of the bot that ends before the bot is running, because a startup
step failed. A stop during startup is not a failed start, but a step that
fails for its own reason while a stop is waiting still is. A failure while
the bot shuts down is not one.
_Avoid_: crash, boot failure, startup panic.
