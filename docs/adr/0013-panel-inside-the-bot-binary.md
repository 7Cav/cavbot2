# ADR 0013: The panel inside the bot binary, forum OAuth2 sign-in, and the service-layer constraint

## Status

Accepted (issue #288, spec #285).

## Context

Hub settings for the temporary voice channel feature are edited live by
staff, not in code or in a config file (`docs/temp-vc-decisions.md`). That
needs a web UI, a sign-in the regiment already has an identity for, and an
access rule that follows the forum without a restart. The panel also has to
rename hub channels in Discord, which only the bot's session can do.

The forum is Xenforo 2.3 with OAuth2 and a custom `user:groups` scope that
puts the user's groups in `/api/me`. The research behind the flow is on the
`research/xenforo-oauth2-go-client` and `research/forum-groups-scope`
branches.

## Decision

- **One binary, one process.** The panel is an HTTP server inside cavbot2,
  started after READY on `PANEL_ADDR`, reached through Nginx Proxy Manager
  over the external `edge` network with no host port. Unset `PANEL_ADDR`
  and nothing listens.
- **Sign-in is Xenforo OAuth2 through `golang.org/x/oauth2`**: confidential
  client, `AuthStyleInParams` (Xenforo reads the credentials from the body),
  PKCE S256 sent anyway, scopes `user:read user:groups`, redirect URI
  `PANEL_BASE_URL` plus `/auth/callback`. The panel owns `state`, the PKCE
  verifier and its session; the library does not.
- **The panel session lives in memory** for two hours from sign-in, the
  forum access token's lifetime. No refresh token, no session table, no
  signing key. A restart ends every session; the user signs in again.
- **The group check runs on every request**: one `GET` to the userinfo URL
  with the session's token. A 401 ends the session as `expired`; a 403 or a
  200 with no allowlisted group ends it as `no-group`; a transport error or
  5xx keeps the session and shows an error page. The allowlist is
  `PANEL_GROUP_IDS`, default Genstaff, S6 HQ and Regimental Technical Aides,
  so a forum change cannot lock the maintainer out.
- **Cookies are `__Host-` prefixed**, Secure, HttpOnly, SameSite Lax, Path
  `/`, no Domain, no Max-Age, holding an opaque 128-bit key. Every
  state change is a POST behind `net/http`'s cross-origin protection; the
  OAuth callback is the one GET that creates state, defended by `state`.
- **Pages are Go templates embedded in the binary.** No Node in the Docker
  build.
- **Handlers call a service layer.** Validation, store calls, Discord calls
  through the bot's manager seam, and runtime updates live in service
  functions the HTML handlers call. A later JS frontend replaces the
  handlers and nothing beneath them. Sign-in has no service layer of its
  own; the constraint binds the hub page and every page after it.

## Why

A separate service would need its own Discord session or an RPC into the
bot to rename a channel, a second deploy, and a second place for the hub
settings to be wrong. The bot already holds the session and the runtime the
settings feed. One writer, one process.

Checking the group on every request costs one forum call per page, which
staff pay in milliseconds, and buys the property the stakeholder asked for:
access follows the forum. A cached check would have to pick a staleness
window and explain it.

`__Host-` cookies over plain HTTP look wrong for local runs, but browsers
treat `localhost` as a secure context, and one cookie contract is simpler
than two.

## How to apply

- A new page goes behind `requireSession`, renders a template from
  `panel/templates`, and calls one service function. It never calls the
  store or the Discord session from a handler.
- A new state change is a POST route. Never a GET.
- Templates carry `data-*` attributes on the values tests read (`data-cause`,
  `data-field`, later `data-hub`). Labels, sentences and element order are
  not contracts.
- Tests drive `Server.Handler()` with `httptest` and a stub forum reached
  through the same `Config` URLs production reads from the environment. No
  seam for the panel.
- A new `PANEL_` variable goes in `.env.example` and in the compose
  `environment:` block, or the container never sees it.
