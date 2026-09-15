# Xenforo OAuth2 flow as a Go client sees it

Research for issue #257. The question: what does a Go program have to do to
sign a forum user in through Xenforo 2.3 OAuth2 and then call `/api/me`?

Sources, in order of weight:

- The forum's own app code, Xenforo 2.3.11 (`src/XF.php:46`), read from the
  staging mirror at `~/srv/xenforo-dev/app`. File paths below are relative to
  that directory. Nothing was modified and no container was started.
- Xenforo's published docs at docs.xenforo.com (the `xenforo.com/docs` URLs
  redirect there).
- `golang.org/x/oauth2` v0.37.0, read from the module zip served by
  proxy.golang.org, plus its pkg.go.dev page.

## Answer

Endpoints, with `<board>` being the forum's base URL:

| Purpose | Method and path | Where |
| --- | --- | --- |
| Authorization (consent page) | `GET <board>/oauth2/authorize` | `src/XF/Pub/Controller/OAuth2Controller.php:19` |
| Token (code exchange and refresh) | `POST <board>/api/oauth2/token` | `src/XF/Api/Controller/OAuth2Controller.php:44` |
| Introspection (RFC 7662) | `POST <board>/api/oauth2/introspect` | same file, line 215 |
| Revocation | `POST <board>/api/oauth2/revoke` | same file, line 385 |
| Current user | `GET <board>/api/me` | `src/XF/Api/Controller/MeController.php:32` |

The published API reference declares the same two URLs as its OAuth2 security
scheme (`https://example.com/oauth2/authorize` and
`https://example.com/api/oauth2/token`, with refresh at the token URL):
<https://docs.xenforo.com/api>. Those paths assume friendly URLs; with them
off the forum builds `index.php?oauth2/authorize` and `index.php?api/oauth2/token`
instead (`src/XF/App.php:633-649`). The admin form for a client prints the
three endpoint URLs for the install, so read them off the form rather than
guessing (`src/addons/XF/_data/templates.xml:14278-14280`).

Client type and PKCE. Register the panel as a confidential client. It is a
server-side process that can hold a secret, and the introspect and revoke
endpoints only work with the secret. Send PKCE (S256) anyway. The server only
forces PKCE on public clients (`src/XF/Pub/Controller/OAuth2Controller.php:181-191`,
`src/XF/Api/Controller/OAuth2Controller.php:72-77`); for a confidential client
it verifies `code_verifier` when one is sent and skips the check when one is
not (`src/XF/Api/Controller/OAuth2Controller.php:132-141`). The only accepted
challenge method is `S256` (`src/XF/Pub/Controller/OAuth2Controller.php:193-203`).
The only grant types are `authorization_code` and `refresh_token`
(`src/XF/Api/Controller/OAuth2Controller.php:84-96`). Client credentials go in
the POST body, not in an HTTP Basic header (see the token endpoint section).

Callback URI rules. Registered URIs must be `http` or `https` with a host
(`src/XF/Validator/Url.php:10-75`). For a non-loopback host the URI sent as
`redirect_uri` has to equal a registered one character for character after a
parse and rebuild: scheme, host, explicit port, path, query, fragment. No
wildcards, no prefix match, and a trailing slash counts
(`src/XF/Repository/OAuthRepository.php:24-56`,
`src/vendor/lusitanian/oauth/src/OAuth/Common/Http/Uri/Uri.php:75-118,242-260`).
Loopback and private-use hosts (`127.x.x.x`, `[::1]`, or a TLD of `example`,
`internal`, `invalid`, `local`, `localhost`, `test`) match on scheme, host and
path only, so the port is free for local development
(`src/XF/Http/Request.php:1982-1995`, `src/XF/Util/Url.php:215-230`). For
production register exactly `https://cavbot2.7cav.us/<callback path>`.

Token lifetimes. Authorization code 5 minutes
(`src/XF/Entity/OAuthCode.php:19`). Access token 2 hours
(`src/XF/Entity/OAuthToken.php:30`). Refresh token 90 days
(`src/XF/Entity/OAuthRefreshToken.php:24`). A refresh issues a new access
token and a new refresh token and revokes the old access token, which takes
the old refresh token with it, so refresh tokens rotate and the client must
store the new one every time
(`src/XF/Api/Controller/OAuth2Controller.php:159-195`,
`src/XF/Service/OAuth/AuthToken/RevokerService.php:23-49`). The token
response is `access_token`, `refresh_token`, `token_type: "bearer"`,
`expires_in: 7200`, `scope`, `issue_date`
(`src/XF/Api/Controller/OAuth2Controller.php:149-156`).

Expired or revoked token on `/api/me`. HTTP 401 with body
`{"errors":[{"code":"unauthorized","message":"api_error.unauthorized","params":[]}]}`.
Expired, revoked and never-issued tokens are indistinguishable there
(`src/XF/Api/App.php:221-235`, `src/XF/Entity/OAuthToken.php:45-58`,
`src/XF/Api/Mvc/Renderer/Api.php:29-70`). Introspection returns
`{"active": false}` for all three as well
(`src/XF/Api/Controller/OAuth2Controller.php:259-269`).

Session cookie. The forum's `xf_session` and `xf_user` cookies matter only on
the forum origin, during the consent step: the user must already be signed in
to the forum, or the authorize URL shows the login form and returns them to
the same URL afterwards (`src/XF/Pub/Controller/OAuth2Controller.php:21`,
`src/XF/ControllerPlugin/ErrorPlugin.php:73-82`). The panel never sees those
cookies, and after the callback it holds only a code, then tokens. It keeps
its own session. Signing out of the forum does not touch OAuth tokens;
revoking the panel under the user's account page does, and the next `/api/me`
then fails with the 401 above.

Go library. `golang.org/x/oauth2` on its own covers the whole flow: consent
URL with PKCE, code exchange, refresh with rotation, and a bearer
`http.Client` for `/api/me`. Set `Endpoint.AuthStyle: oauth2.AuthStyleInParams`
so it never tries HTTP Basic. `go mod tidy` adds only
`golang.org/x/oauth2 v0.37.0` (go 1.26.0, same as this repo), nothing outside
the standard library compiles in, and it builds with `CGO_ENABLED=0`. The
panel still writes its own `state` cookie, stores the PKCE verifier between
redirect and callback, and owns its session; the library does none of that.
Xenforo is not an OpenID Connect provider (no discovery document, no
`id_token`), so an OIDC library is the wrong tool.

## Evidence

### Client registration

Admin control panel, Setup > Service providers > OAuth2 clients, at
`admin.php?oauth2/clients`. The entry needs the `oauth2` admin permission
(`src/addons/XF/_data/admin_navigation.xml:81,103,104`). The form takes title,
description, client type (`confidential` or `public`, default
`confidential`), a list of redirect URIs, homepage URL, image, the allowed
scopes as checkboxes, and an active flag
(`src/addons/XF/_data/templates.xml:14226-14330`,
`src/XF/Admin/Controller/OAuth2Controller.php:60-80`,
`src/XF/Entity/OAuthClient.php:136-164`). The server generates the client ID
(16 decimal digits) and secret (32 characters from the URL-safe base64
alphabet) on insert; the form can show and regenerate the secret
(`src/XF/Entity/OAuthClient.php:49-79,101-113`, `src/XF/Util/Random.php:55-73`).

Scopes are per client. The authorize step drops any requested scope the
client is not allowed, silently, and errors only when nothing is left
(`src/XF/Pub/Controller/OAuth2Controller.php:67-96`). So a scope the admin
forgot to tick does not fail sign-in; it comes back missing from the token's
`scope` string. Worth checking at the client after exchange.

Stock scopes are listed at <https://docs.xenforo.com/api> and in
`src/addons/XF/_data/api_scopes.xml`. The forum adds a custom `user:groups`
scope through the `Cav7/UserGroupsScope` add-on
(`src/addons/Cav7/UserGroupsScope/_data/api_scopes.xml`, public source at
<https://github.com/7Cav/UserGroupsScope>). See the `/api/me` section for what
it does and a caveat about where it is installed.

### Authorization endpoint

`GET /oauth2/authorize` reads `client_id`, `redirect_uri`, `response_type`,
`state`, `scope`, `code_challenge`, `code_challenge_method`
(`src/XF/Pub/Controller/OAuth2Controller.php:148-157`). Checks, in order:

1. The visitor must be a signed-in forum user. A guest gets the login
   template with HTTP 403; its form carries `_xfRedirect` so a successful
   login lands back on the authorize URL
   (`src/XF/Pub/Controller/AbstractController.php:164-172`,
   `src/XF/ControllerPlugin/ErrorPlugin.php:73-82`,
   `src/addons/XF/_data/templates.xml:61341`, `src/XF/App.php:2608-2628`).
2. The client must exist and be active, else a 404 page (lines 25-29).
3. `redirect_uri` is required and must match a registered URI, else an error
   page with HTTP 400 (lines 159-169). Errors after this point go back to the
   redirect URI as `error=...` query parameters.
4. `response_type` must be `code`, else `error=unsupported_response_type`
   (lines 171-179).
5. A public client must send `code_challenge` and `code_challenge_method`,
   else `error=invalid_request` (lines 181-191).
6. If `code_challenge` is present, the method must be `S256` (lines 193-203).
7. Scopes are split on spaces and intersected with the client's allowed list
   (lines 67-98).

The request is saved as an `xf_oauth_request` row and the consent page
renders (lines 105-120, template `oauth_authorize` at
`src/addons/XF/_data/templates.xml:67650`). Consent is not remembered: there
is no table for it and the GET path always renders the page, so every sign-in
shows the "Authorize" button. On POST the server mints a code and redirects to
`redirect_uri?code=...&state=...`; `state` is echoed only when non-empty
(lines 34-54, 208-231). "Deny" redirects with `error=access_denied` (lines
126-143).

### Token endpoint

`POST /api/oauth2/token`, form encoded
(<https://docs.xenforo.com/api/post-oauth-2-token>). It needs no API key: the
controller allows unauthenticated requests
(`src/XF/Api/Controller/OAuth2Controller.php:448-451`).

Inputs are read from the request body: `client_id`, `client_secret`,
`grant_type`, `code`, `refresh_token`, `code_verifier`, `redirect_uri`
(lines 48-56). `client_id` and `grant_type` are always required; a
confidential client must also send `client_secret`; a public client doing
`authorization_code` must send `code_verifier` (lines 46, 68-77). A wrong
secret is `invalid_client` (lines 79-82).

HTTP Basic is not a substitute for the body fields. The API app does accept
an `Authorization: Basic` header and, when the client ID and secret in it
match, treats the request as a guest (`src/XF/Api/App.php:205-219`), but the
controller then fails `assertRequiredApiInput(['client_id', 'grant_type'])`
because `client_id` is not in the body
(`src/XF/Api/Controller/AbstractController.php:177-225`). The result is a 400
`required_input_missing`.

`authorization_code` grant (lines 86-91, 99-157):

- `redirect_uri` is required and must be one of the registered URIs. It is
  not compared with the URI used at the authorize step, only with the
  registered list (lines 87-90, 103-106).
- The code must exist, be unexpired, and belong to this client (lines
  109-130). Failures are `invalid_grant`.
- If `code_verifier` is sent, `base64url(sha256(verifier))` without padding
  must equal the stored `code_challenge` (lines 132-141). This is the same
  computation `x/oauth2` uses (`pkce.go:49-52`).
- The new token copies the user and scopes from the authorize request
  (`src/XF/Service/OAuth/AuthToken/CreatorService.php:60-66`).

The code is not consumed on exchange. Nothing deletes or marks it; the only
cleanup is the daily cron pruning codes 14 days after expiry
(`src/XF/Repository/OAuthRepository.php:152-160`,
`src/XF/Cron/CleanUp.php:111-113`). So within its 5 minute window the same
code can be exchanged again and each exchange mints a fresh token pair. A Go
client should not rely on this (RFC 6749 says a code is single use), but it
means a retried callback does not fail the way it would elsewhere.

`refresh_token` grant (lines 92-93, 159-195): the refresh token must belong
to this client and be valid, which also requires its parent access token to
be unrevoked (`src/XF/Entity/OAuthRefreshToken.php:26-44`). A new access and
refresh token are issued, then the old access token is revoked, and that
revocation cascades to every refresh token hanging off it
(`src/XF/Service/OAuth/AuthToken/RevokerService.php:23-49`). Net effect: one
live pair per sign-in, rotated on every refresh. A client that loses the new
refresh token after a refresh is signed out.

### Redirect URI matching

Both endpoints call `OAuthRepository::isValidRedirectUri`
(`src/XF/Repository/OAuthRepository.php:24-56`). It parses the input and each
registered URI with the `lusitanian/oauth` `Uri` class. When the request
object says both hosts are local, it compares scheme, host and path and
ignores the port, citing RFC 8252 section 7.3. Otherwise it compares
`getAbsoluteUri()` of both, which rebuilds `scheme://[userinfo@]host[:port]path[?query][#fragment]`
with the port only when it was written explicitly and a bare `/` path kept
only when it was written
(`src/vendor/lusitanian/oauth/src/OAuth/Common/Http/Uri/Uri.php:75-118,227-260`).
Nothing is lowercased, so the host has to match the registered spelling.

"Local" means `[::1]`, `127.` followed by three octets, or a TLD in
`example`, `internal`, `invalid`, `local`, `localhost`, `test`
(`src/XF/Http/Request.php:1982-1995`, `src/XF/Util/Url.php:215-230`). A
plain `http://localhost:8090/auth/callback` therefore matches a registered
`http://localhost/auth/callback` on any port. The admin form validates each
saved URI with `XF\Validator\Url`: scheme `http` or `https`, host required,
and PHP's `FILTER_VALIDATE_URL` in strict mode
(`src/XF/Entity/OAuthClient.php:81-99`, `src/XF/Validator/Url.php:9-76`).
The form's help text says the same
(`src/addons/XF/_data/phrases.xml:6585`).

### Token lifetimes, format, and revocation

| Thing | Lifetime | Source |
| --- | --- | --- |
| Authorization code | 300 s | `src/XF/Entity/OAuthCode.php:19,52-63` |
| Access token | 7200 s | `src/XF/Entity/OAuthToken.php:30,65-76` |
| Refresh token | 90 days | `src/XF/Entity/OAuthRefreshToken.php:24,59-70` |
| Authorize request row | pruned after 30 days | `src/XF/Repository/OAuthRepository.php:162-170` |

Access and refresh tokens are 32 characters from `A-Za-z0-9-_`
(`src/XF/Entity/OAuthToken.php:60-63`, `src/XF.php:1446-1456`,
`src/XF/Util/Random.php:55-73`); the `token` column is `varchar(64)`
(`src/XF/Entity/OAuthToken.php:85`). Useful for sizing a session store.

An access token is valid when its client and user rows exist, `revoked_date`
is zero, and `expiry_date` is in the future
(`src/XF/Entity/OAuthToken.php:45-58`). Ways it stops being valid:

- The client calls `POST /api/oauth2/revoke` with `client_id`,
  `client_secret`, `token`, and optional `token_type_hint`
  (`src/XF/Api/Controller/OAuth2Controller.php:385-446`,
  <https://docs.xenforo.com/api/post-oauth-2-revoke>). Revoking an access
  token also revokes its refresh tokens.
- The user revokes the application at `account/applications`, which revokes
  every token that client holds for them
  (`src/XF/Pub/Controller/AccountController.php:1901-1925`,
  `src/XF/Repository/OAuthRepository.php:112-131`).
- An admin does the same from the user edit page
  (`src/XF/Admin/Controller/UserController.php:637-655`).
- A refresh revokes the previous access token (above).
- Deleting the client deletes its tokens (`src/XF/Entity/OAuthClient.php:120-126`).
  Deactivating the client is softer: `isValid()` does not look at `active`,
  so existing access tokens keep working until they expire, while the token
  endpoint filters on `active = 1` and refuses to refresh them
  (`src/XF/Api/Controller/OAuth2Controller.php:59-62`).
- Deleting the user removes their tokens
  (`src/XF/Service/User/DeleteCleanUpService.php:57-58,151-161`).

Forum logout is not on that list. Neither `LogoutController` nor the session
code references OAuth tokens.

### What `/api/me` returns

Authentication. The API app reads the `Authorization` header; for
`Bearer <token>` it looks the token up by value and requires `isValid()`. On
failure it sets error `api_error.unauthorized` with HTTP 401
(`src/XF/Api/App.php:193-235`). The renderer turns that into
`{"errors":[{"code":"unauthorized","message":"api_error.unauthorized","params":[]}]}`:
the code is the phrase name with `api_error.` stripped
(`src/XF/Api/Mvc/Renderer/Api.php:29-70`), and the message is the phrase name
itself because no `api_error.unauthorized` phrase ships in
`src/addons/XF/_data/phrases.xml` and a missing phrase renders as its name
(`src/XF/Language.php:134-167`). The error envelope matches the published
format (<https://docs.xenforo.com/manual/reference/rest-api>). Match on
`code`, not `message`, as the docs say. I could not run this against a live
forum; the body above is derived from the code path, not observed.

No `Authorization` header and no `XF-Api-Key` gives 400
`no_api_key_in_request` instead, because `MeController` does not allow
unauthenticated requests (`src/XF/Api/App.php:144-159`).

Banned, rejected, or security-locked users are refused with 403 before the
controller runs, even with a valid token
(`src/XF/Api/Controller/AbstractController.php:282-345`). A valid token can
also fail on the forum's board-inactive switch, which returns the configured
service-unavailable code to non-admins (same file, lines 326-333).

Scopes. `GET /api/me` asserts no scope; only the write methods require
`user:write` (`src/XF/Api/Controller/MeController.php:18-25`). `user:read`
raises verbosity from quiet to verbose (line 35), which for the user's own
record adds `email`, `timezone`, `gravatar`, the notification options, and
the privacy options (`src/XF/Entity/User.php:2057-2093`). The quiet stub
still carries `user_id`, `username`, the columns flagged `api` (message and
trophy counts, `register_date`, `is_staff`, reaction and vote scores), the
`can_*` booleans, and `last_activity` where visible
(`src/XF/Entity/User.php:1936-2110,2324-2374`). The published page for the
endpoint: <https://docs.xenforo.com/api/get-me>.

Groups. Stock Xenforo includes `user_group_id` and `secondary_group_ids` only
when the caller holds the `user` admin permission or bypasses permissions
(`src/XF/Entity/User.php:1961-1972,2097-2102`). The forum's
`Cav7/UserGroupsScope` add-on extends the entity so that a token carrying
`user:groups` gets both fields for the token's own user, and never for anyone
else (`src/addons/Cav7/UserGroupsScope/XF/Entity/User.php:13-50`). Its README
records acceptance tests on 2.3.10 with a non-admin user and warns not to
test with an admin account, since admins see the fields under `user:read`
alone (`src/addons/Cav7/UserGroupsScope/README.md`). For the panel that means
requesting `user:read user:groups` and ticking both on the client.

Caveat on that add-on. The staging database dump in the mirror (dated 4 Aug)
has no `user:groups` row in `xf_api_scope` and no `Cav7/UserGroupsScope` row
in `xf_addon`, while the add-on files in `src/addons/Cav7/UserGroupsScope`
date from June. The mirror carries the code but the dump predates its
install, or it was never installed on staging. Whether production has it
installed I could not determine from this machine; the decisions doc for the
temp VC work assumes it does. Check the production ACP scope list before the
group check is built on it.

### The forum session cookie

Xenforo's cookie prefix is `xf_` and the session and remember cookies are
named `session` and `user`, so `xf_session` and `xf_user`
(`src/XF/App.php:197,1095,2056-2060`). They are set for the forum's origin and
the browser sends them only there. In the flow they do one job: satisfy
`assertRegistrationRequired` on the authorize page. The panel receives the
redirect with `code` and `state`, exchanges the code server to server, and
from then on authenticates to the API with the bearer token. There is no
endpoint that turns a forum session into an identity for a normal client; the
`auth/from-session` endpoint that does so is restricted to super-user API keys
(<https://docs.xenforo.com/api/auth>).

So the panel keeps its own session. Two consequences:

- Forum sign-out does not sign the panel out. If that matters, the panel's
  session lifetime is the lever, since access tokens live 2 hours and refresh
  tokens 90 days.
- Revocation on the forum takes effect the next time the panel touches the
  API. A panel that only calls `/api/me` at sign-in and then trusts its own
  session for hours will not notice. Re-checking `/api/me` on a timer, or on
  every request, is how revocation becomes prompt. Introspection needs the
  client secret and returns `active: false` for the same cases, so it adds
  nothing over `/api/me` here.

### `golang.org/x/oauth2`

Version and dependencies. v0.37.0, published 2026-08-25, `go 1.26.0`
(proxy.golang.org `@latest` and `.mod`). The module's only `require` is
`cloud.google.com/go/compute/metadata`, used by the `google` subpackage. A
throwaway module importing only `golang.org/x/oauth2` tidied to a single
`require golang.org/x/oauth2 v0.37.0`, `go list -deps` showed nothing outside
the standard library and `golang.org/x/oauth2/internal`, and
`CGO_ENABLED=0 go build` succeeded. The root package imports only standard
library packages plus `internal` (`oauth2.go`, `token.go`, `transport.go`,
`pkce.go`, `deviceauth.go`).

What it covers, against Xenforo's requirements:

- Consent URL. `Config.AuthCodeURL(state, opts...)` writes `response_type=code`,
  `client_id`, `redirect_uri`, `scope` (space joined) and `state`
  (`oauth2.go:159-184`). `oauth2.S256ChallengeOption(verifier)` adds
  `code_challenge` and `code_challenge_method=S256` (`pkce.go:54-69`);
  `oauth2.GenerateVerifier()` makes the verifier (`pkce.go:20-38`).
- Exchange. `Config.Exchange(ctx, code, oauth2.VerifierOption(verifier))`
  posts `grant_type=authorization_code`, `code`, `redirect_uri`,
  `code_verifier` (`oauth2.go:222-234`, `pkce.go:40-44`).
- Client credentials placement. With `AuthStyleInParams`, `client_id` and
  `client_secret` go in the form body (`internal/token.go:186-204`), which is
  what Xenforo reads. Leaving `AuthStyle` at zero makes the library try HTTP
  Basic first and fall back to the body on error, caching the result per
  process (`internal/token.go:215-256`); against Xenforo that costs one
  failed request after every restart. Set it explicitly.
- Refresh. `Config.TokenSource` or `Config.Client` refreshes when the token is
  within 10 seconds of expiry (`token.go:22,138-153`), posting
  `grant_type=refresh_token` and `refresh_token`, and adopts a rotated
  refresh token from the response (`oauth2.go:274-291`). That matches
  Xenforo's rotation. The panel must persist the new token from the source
  after each refresh, or it will hold a dead one after restart.
- Response parsing. JSON with `access_token`, `token_type`, `refresh_token`,
  `expires_in` is the default branch (`internal/token.go:258-330`); Xenforo's
  extra `scope` and `issue_date` fields land in `Token.Raw`. Error bodies
  with a non-2xx status become `*oauth2.RetrieveError` carrying the body, so
  the panel can read Xenforo's `errors[0].code`.
- The `/api/me` call. `Config.Client(ctx, token)` returns an `http.Client`
  that adds `Authorization: Bearer <token>` (`oauth2.go:236-242`,
  `transport.go`). A plain `net/http` request with the header set by hand
  works equally well.

What it leaves to the panel, per its own docs: generating and verifying
`state` ("be sure to validate ... state", `oauth2.go:216-221`), keeping the
PKCE verifier between redirect and callback, storing tokens, and the panel's
session. None of that needs a library.

Why not something else. `coreos/go-oidc` and similar expect an OpenID
Connect issuer with a discovery document and `id_token`; Xenforo's OAuth2
server has neither (no `.well-known` route, no `id_token` in the token
response). Heavier OAuth2 client frameworks add nothing here because the
flow is the plain authorization code grant. The repo already has `resty`,
but that is an HTTP client, not an OAuth2 client, and reimplementing PKCE
and refresh rotation on top of it is work `x/oauth2` has already done.

### Not determined

- The exact bytes of the 401 body on `/api/me` and of the token endpoint's
  error bodies. Derived from the code, not observed; the dev container was
  not running and the task said not to start it when the code answers.
- Whether `Cav7/UserGroupsScope` is installed on production (see the groups
  caveat above).
- The production forum's friendly URL setting, which decides between
  `/oauth2/authorize` and `/index.php?oauth2/authorize`. The admin client form
  shows the right one.
- Xenforo's community announcement thread for OAuth2 sits behind a Cloudflare
  bot check and was not read. The published docs and the code covered
  everything the ticket asked.
