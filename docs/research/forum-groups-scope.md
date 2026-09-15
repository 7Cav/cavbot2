# Forum group IDs for panel access, and what `user:groups` puts in `/api/me`

**Ticket:** [#256](https://github.com/7Cav/cavbot2/issues/256), under map #255.
**Sources:** the forum mirror at `~/srv/xenforo-dev` on the maintainer's machine. `db.sql.gz` is a MariaDB dump completed 2026-08-04 (its last line says so). `app/src` is the forum's PHP tree, XenForo 2.3 plus the 7Cav add-ons. Nothing was started; the dump was read with `gzip -dc` and `grep`, the code with a text editor. No user rows, hashes, or secrets were copied, and the aggregate membership counts below are the only thing derived from user data.

## Answer

The three groups for the panel allowlist:

| Team in the decisions doc | `xf_user_group.user_group_id` | `title` | Banner text |
|---|---|---|---|
| Genstaff | 71 | `Position - Regimental HQ` | Regimental HQ |
| S6 HQ | 47 | `Position - S6 HQ` | S6 HQ |
| Regimental Technical Aides | 44 | `Position - IMO Staff` | IMO Staff |

So the env var would be `PANEL_GROUP_IDS=71,47,44` or whatever the spec names it. Group 44 is the one to double-check with a human, because its title does not say "Technical Aide": the milpac position "Regimental Technical Aide" is the only position that grants it, which is why I picked it. If the intent is every Regimental HQ aide rather than the technical ones, use 277 (`Position - Regimental HQ Aide`) instead or as well. Details under "The three groups".

The JSON: with `user:groups` on the token, `GET /api/me` adds two fields to the `me` object, `user_group_id` (integer, the primary group) and `secondary_group_ids` (array of integers). IDs only, never titles. Both are present; secondary groups are included. All three allowlist groups are secondary groups on every member who holds them, so the check has to read `secondary_group_ids`. Every member's primary group is something else (in this dump nobody has 44, 47, or 71 as primary).

```json
{
    "me": {
        "user_id": 1234,
        "username": "Doe.J",
        "user_group_id": 2,
        "secondary_group_ids": [35, 47, 72],
        "...": "stock fields continue"
    }
}
```

Without the scope those two fields are absent for a normal user and everything else is unchanged.

No second scope is required to read `user_id` and `username`. `GET /api/me` asserts no scope at all, and `user_id` and `username` are in the response at every verbosity. `user:read` only switches the response from the short stub to the full profile. The add-on README recommends `user:read user:groups` for a full profile, and that is fine, but `user:groups` alone gets the panel its ID, username, and groups.

## The three groups

### Where the mapping comes from

Forum membership in a `Position - ...` group is not assigned by hand. The NF Rosters add-on (the milpacs) gives each position an `extra_group_ids` list, and when a member's position changes the add-on adds and removes those forum groups through XenForo's `UserGroupChange` service:

- `app/src/addons/NF/Rosters/Service/Profile/Editor.php` lines 140 to 151 loop over the member's new positions and call `addUserGroupChange` with `$position->extra_group_ids`.
- `app/src/XF/Service/User/UserGroupChangeService.php` lines 212 to 232 merge those IDs into `secondary_group_ids` and save. It never touches `user_group_id`.

So the authoritative question is "which milpac positions grant which forum group", and the table `xf_nf_rosters_position` answers it. Its `extra_group_ids` column is a hex-encoded comma list of `xf_user_group` IDs.

### Genstaff is 71, `Position - Regimental HQ`

Rows in `xf_nf_rosters_position` whose `extra_group_ids` include 71, all in milpac position group 1 "Regimental HQ":

| `position_id` | `position_title` | Grants |
|---|---|---|
| 1 | Regimental Commanding Officer | 71, 322 |
| 2 | Regimental Executive Officer | 71, 322 |
| 3 | Regimental Chief of Staff | 65, 71, 322 |
| 4 | Regimental Recruiting Oversight Officer | 55, 71, 322 |
| 5 | Regimental Information Management Officer | 43, 71, 322 |
| 6 | Regimental Command Sergeant Major | 65, 71, 322 |
| 743 | Regimental Adjutant General | 71, 322 |

(322 is `Wiki Admin`.) That list is the general staff. Group 71 has 9 members in the dump, all Active Duty, eight of them in a general-officer rank group.

The other candidate is 148, `General's Area`. It has 7 members, six of whom are also in 71, and its `username_css` is the red glow. No milpac position grants it, so it is hand-assigned, which is a bad property for an allowlist. I would not use it. `xf_user_group` row 148 for the styling, and the absence of 148 in any `xf_nf_rosters_position.extra_group_ids` for the rest.

### S6 HQ is 47, `Position - S6 HQ`

Rows granting 47, all in milpac position group 2 "Support Attachment":

| `position_id` | `position_title` | Grants |
|---|---|---|
| 50 | S6 1IC | 47 |
| 51 | S6 2IC | 47, 72 |
| 52 | S6 DevOps Lead | 47 |
| 53 | S6 Senior Development Staff | 47 |
| 56 | S6 Game/Forum Lead | 47 |
| 57 | S6 Senior Game Staff | 47 |
| 778 | S6 Senior Forums Staff | 47 |

Rank-and-file S6 positions grant 48 (`Position - S6 Staff`) instead, so 47 is the leads and seniors only. 4 members in the dump. `xf_user_group` row 47 has `sv_mentionable = 1`, the only group in this set that does.

### Regimental Technical Aides is 44, `Position - IMO Staff`, with 277 as the wider option

`xf_nf_rosters_position` row 773, `Regimental Technical Aide`, position group 2 "Support Attachment", grants 44 and 277.

Group 44 is granted by no other position. It has 7 members and all 7 are also in 277. The title `Position - IMO Staff` is a leftover name (IMO is the Regimental Information Management Officer, whose own position grants 43 `Position - IMO HQ`); the group is in practice the technical aides' group.

Group 277, `Position - Regimental HQ Aide`, banner text "Regimental Aide", is granted by every aide position under Regimental HQ:

| `position_id` | `position_title` | Grants |
|---|---|---|
| 10 | Aide to the CoS | 65, 277 |
| 60 | Aide to the ROO | 55, 236, 238, 239, 277 |
| 62 | Aide to the SecOps | 49, 277 |
| 676 | Aide to the CSM | 45, 67, 216, 237, 277 |
| 772 | Aide to the AG | 240, 277 |
| 773 | Regimental Technical Aide | 44, 277 |
| 1127 | Aide to the XO | 277 |

277 has 8 members, 7 of them the technical aides. So today the two groups differ by one person. They will drift apart as aides come and go, which is the whole reason to pick deliberately: 44 means "technical aides", 277 means "anyone aiding Regimental HQ". The decisions doc says "Regimental Technical Aides", so 44 is my reading. A moderator log row from 2023-10-24 titled "Create Milpac Position: Regimental Technical Aide" confirms the position is a deliberate, named thing rather than a nickname (`xf_moderator_log`, ticket 4469).

Groups I ruled out for this slot: 176 `S6 Clerk` (one member, granted by "S6 Game Staff IT") and 178 `Position - MPIT` (one member, no position grants it).

### Membership shape

Every member of 44, 47, 71, and 277 holds it as a secondary group; `xf_user_group_relation` has zero rows with `is_primary = 1` for any of them. This follows from the mechanism above and it means the allowlist check is `any(id in secondary_group_ids)`. Checking `user_group_id` too costs nothing and guards against a hand-set primary group, but it will not match anyone today.

## The `user:groups` scope

### It is an add-on, installed and active

- `app/src/addons/Cav7/UserGroupsScope/addon.json`: "7Cav - User Groups Scope", version 1.0.0, requires XenForo 2.3.0+. The README line 88 says it was imported from `github.com/7Cav/UserGroupsScope` at commit `15d267b`.
- `xf_addon` has the row `Cav7/UserGroupsScope`, version 1.0.0, `active = 1`.
- `xf_class_extension` row 625 extends `XF\Entity\User` with `Cav7\UserGroupsScope\XF\Entity\User`, execute order 10, `active = 1`.
- `xf_api_scope` has the row `user:groups`, `usable_with_oauth_clients = 1`, `addon_id = Cav7/UserGroupsScope`.

### What the code does

The whole add-on is one method, `setupApiResultData` in `app/src/addons/Cav7/UserGroupsScope/XF/Entity/User.php`:

- Line 19 calls the stock method first, so everything stock is untouched.
- Lines 21 to 24 bail unless this is an API result (not a webhook).
- Lines 27 to 31 bail unless the user being rendered is the visitor. Another user's groups are never exposed, whatever the scopes.
- Lines 33 to 37 bail unless the request's OAuth token or API key `hasScope('user:groups')`.
- Lines 44 to 47 bail if an OAuth token's `user_id` is not the rendered user.
- Line 49: `$result->includeColumn(['user_group_id', 'secondary_group_ids'])`.

That is the entire contract. Field names and types come from stock XenForo, `app/src/XF/Entity/User.php` lines 2336 to 2339: `user_group_id` is `UINT`, `secondary_group_ids` is `LIST_COMMA` with a `posint` list, unique, sorted numerically. The API renderer (`app/src/XF/Api/Result/EntityResult.php` lines 136 to 148) emits each column's entity value, so the list becomes a JSON array of integers.

Nothing in the add-on adds titles. If the panel ever wants to show a group name it has to carry its own ID-to-name table, which for an allowlist it does not need.

### Why stock XenForo hides these fields

`app/src/XF/Entity/User.php` line 2099 is the only stock place that includes `user_group_id` and `secondary_group_ids`, inside the `includeInternalProfile` branch. Line 1972 sets that flag from `$visitor->hasAdminPermission('user')`, and line 1961 also sets it when the request bypasses permissions (a super-user key). So an admin testing with their own account sees the group fields under `user:read` alone and will draw the wrong conclusion. The add-on's README line 79 to 84 and CONTEXT.md line 36 to 40 both flag this. Test with a non-admin account.

### `/api/me` needs no scope for GET

`app/src/XF/Api/Controller/MeController.php`:

- Lines 18 to 25: `preDispatchController` asserts `user:write` only when the method is not GET.
- Lines 32 to 40: `actionGet` picks `VERBOSITY_VERBOSE` if the token has `user:read`, else `VERBOSITY_QUIET`, and returns `\XF::visitor()->toApiResult($verbosity)` under the key `me`.

Bearer handling in `app/src/XF/Api/App.php` lines 221 to 240 looks the token up, rejects it if `isValid()` is false, calls `\XF::setAccessToken($token)`, and returns the token's user as the visitor. No scope check happens at that layer.

What the QUIET stub contains, from `EntityResult::isColumnIncluded` (`app/src/XF/Api/Result/EntityResult.php` line 263: included if named by `includeColumn`, flagged `'api' => true`, or auto-increment):

- `user_id` (auto-increment, `User.php` line 2323)
- `username` (`'api' => true`, line 2325)
- `message_count`, `question_solution_count`, `register_date`, `trophy_points`, `is_staff`, `reaction_score`, `vote_score` (all `'api' => true`, lines 2342 to 2374)
- plus what `setupApiResultData` adds unconditionally for a logged-in user: `is_ignored`, `is_followed` (line 1989), the `can_*` booleans (lines 2106 to 2114), `user_title`, `signature`, `location`, `avatar_urls`, `profile_banner_urls`, `view_url` (lines 2148 to 2168), and `last_activity` when online status is visible (line 1984).
- with `user:groups`: `user_group_id`, `secondary_group_ids`.

So a `user:groups`-only token returns ID, username, and groups. The README's "verified behavior" table (lines 65 to 72) reports the same from an acceptance run on 2.3.10 in June 2026, including the row "OAuth token, `user:groups` alone: present (stub identity)". The code agrees with the table.

### The scope registry is a DB table seeded from add-on XML

- Stock scopes are declared in `app/src/addons/XF/_data/api_scopes.xml` lines 3 to 25 (`alert:read` through `user:write`, 23 scopes).
- The add-on declares its one scope in `app/src/addons/Cav7/UserGroupsScope/_data/api_scopes.xml` line 3.
- Install imports both into `xf_api_scope`, and that table is what runs: `ApiRepository::getApiScopesByIds` (`app/src/XF/Repository/ApiRepository.php` lines 82 to 87) is a finder over `xf_api_scope`, and the ACP OAuth client form lists scopes with `usableForOAuth()` (`app/src/XF/Admin/Controller/OAuth2Controller.php` line 39, `app/src/XF/Finder/ApiScopeFinder.php` lines 17 to 20).

The dump's `xf_api_scope` has 27 rows: the 23 stock ones plus `donation:read` and `donation:write` (Siropu/Donations), `nf_discord:read` (NF/Discord), and `user:groups`.

`hasScope` itself is a key lookup in the token's `scopes` JSON column (`app/src/XF/Entity/TokenScopeTrait.php` lines 11 to 14), shared by `OAuthToken` and `ApiKey`. An API key with `allow_all_scopes` passes every check (`app/src/XF/Entity/ApiKey.php` lines 51 to 59).

### A gotcha for the panel's OAuth client

The authorize endpoint filters the requested scopes against the client's `allowed_scopes` and silently drops the ones the client is not allowed (`app/src/XF/Pub/Controller/OAuth2Controller.php` lines 67 to 96). It only errors when nothing survives. So if the panel's OAuth client is created in the ACP without `user:groups` ticked, a request for `user:read user:groups` succeeds, the consent screen shows only `user:read`, and `/api/me` comes back with no group fields and no error. The panel should treat a missing `secondary_group_ids` as a misconfiguration, not as "no groups", and the runbook for creating the client should say to tick `user:groups`.

## What I could not determine

- Which of 44 and 277 the maintainer means by "Regimental Technical Aides". The data supports 44 as the literal reading. A human should confirm before the env var is set.
- Whether the token's `scopes` column can be inspected from the panel side. There is no `/api/me` field for it; the add-on does not add one. If the panel wants to know what it was granted, the OAuth token response's `scope` value is the place, and that was not in scope for this ticket.
- Anything about the live production forum. The dump is a staging mirror completed 2026-08-04; positions and memberships move, so the counts above are a snapshot. The IDs themselves are stable (auto-increment primary keys, `AUTO_INCREMENT=323` on the table).
