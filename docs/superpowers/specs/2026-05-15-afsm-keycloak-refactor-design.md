# `/afsm` + `/s6-it-check` — Drop Keycloak lookup, stop nuking reports on per-member errors

**Issue:** [#79](https://github.com/7Cav/cavbot2/issues/79)
**Sentry:** [CAVBOT2-4](https://syniron.sentry.io/issues/CAVBOT2-4)
**Triggering user:** `Stevenson.A` (no `KeycloakID` → 404 from removed Keycloak service)
**Affected commands:** `/afsm`, `/s6-it-check`

## Problem

Two distinct bugs share one cause and one fix shape.

1. **Stale identifier.** `commands/afsm.go:112` and `commands/s6_trackers.go:68` both look up full milpac profiles via `utils.GetMilpacByKeycloakID(ctx, member.KeycloakID)`. Keycloak has been removed as a service; older Cav members still have a populated `KeycloakID` so the lookup happens to succeed for them, but newer members never had one — so `api.7cav.us/api/v1/milpac/keycloak/<id>` returns 404 and `makeAPIRequest` surfaces it as `no milpac found`.

2. **Cascade failure.** Both handlers respond first ("Fetching ... for ...") and then iterate the roster. When **any** intra-loop error fires (milpac fetch, record-date parse, award-date parse, uniform URL regex), the loop hits `return`, the whole report aborts, and the user sees `❌ Failed to fetch milpac: ...` instead of the eligible-members list for everyone else.

## Goals

1. Replace the Keycloak-ID lookup at every call site with a stable identifier.
2. Make per-member errors skip the offending member instead of aborting the whole command.
3. Surface skipped-member counts in the Discord response so the runner knows their report is partial.
4. Keep Sentry capture of internal errors; no new noise sources.

## Non-goals

- Converting either handler to the `runX(responder, i)` shape from PR #74 (tracked under [#76](https://github.com/7Cav/cavbot2/issues/76)).
- Adding unit tests for the refactored handlers (blocked on the `runX` conversion).
- Touching any other command. The Keycloak-ID lookup exists at exactly two call sites; both are in scope.
- Changing the `/s6-it-check` empty-eligibles UX (separate issue if anyone cares).

## Design

### Identifier swap

Use **forum username** as the new stable identifier. The roster API response (`LiteProfileResponse`) exposes it on `member.User.Username`, and `utils.GetMilpacByUsername` already exists in `utils/milpacs.go:147` (`milpacs/profile/username/<username>`).

Rationale vs. alternatives:

- `DiscordID` — not guaranteed to be populated for all roster entries.
- `Gamertag` — free-form string, less stable than the forum account name.
- `Username` — populated on every roster entry; it's the forum account name (note: `User.Username` is the forum username, not the in-game gamertag, which is `LiteProfileResponse.Gamertag`).

### Per-member processing extracted to helpers

Both handlers gain a private helper that encapsulates all the per-member logic. The handler loop becomes a thin caller that handles only the skip-vs-keep decision.

**`commands/afsm.go`:**

```go
// evaluateAFSMMember returns (nil, nil) when the member is ineligible,
// (member, nil) when eligible, and (nil, err) when the member's data
// can't be processed (caller should skip + report).
func evaluateAFSMMember(
    ctx context.Context,
    member utils.LiteProfileResponse,
    dept string,
    currentDate time.Time,
) (*AFSMMember, error)
```

The helper performs: primary/secondary eligibility check, full-profile fetch via `GetMilpacByUsername`, record-date parsing, start-date computation, latest-award lookup, and uniform URL parsing. Any internal error returns `(nil, err)`. Eligible → `(member, nil)`. Not eligible → `(nil, nil)`.

**`commands/s6_trackers.go`:**

```go
// evaluateS6Member returns the zero-to-N ITMember rows yielded by a single
// roster entry. A single member can match both primary and secondary positions,
// each producing its own row (preserving current behavior).
func evaluateS6Member(
    ctx context.Context,
    member utils.LiteProfileResponse,
    currentDate time.Time,
) ([]ITMember, error)
```

Same shape: any internal error → `(nil, err)`; caller skips.

### Handler loop shape

```go
skippedCount := 0
for _, member := range Members.LiteProfiles {
    result, err := evaluateAFSMMember(ctx, member, choice, currentDate)
    if err != nil {
        utils.CaptureError(
            "AFSM member evaluation failed",
            err,
            "username", member.User.Username,
            "department", choice,
        )
        skippedCount++
        continue
    }
    if result != nil {
        eligibleMembers = append(eligibleMembers, *result)
    }
}
```

The S6 loop is the same shape but appends `result...` (slice) when non-nil.

### Skipped-member footer

When `skippedCount > 0`, append one line to the existing response:

```
⚠️ N members skipped due to errors (reported)
```

Single string, no per-member breakdown. Full detail (username, department, underlying error) lives in Sentry via `CaptureError`'s structured fields. Discord output stays clean; on-call gets paged with everything they need.

Footer wording is identical for both commands. Pluralize correctly: `1 member skipped` vs `N members skipped` via a `skippedCount == 1` branch on the noun — cheap one-liner, avoids grating output in the common 1-skip case.

The footer is **appended** to the existing response. For `/afsm`, that means the leading disclaimer (`⚠️ This command cannot be made completely accurate. Please check the output carefully.`) at `commands/afsm.go:230` is preserved verbatim and continues to present to the user. The skipped-count line lands at the end of the response, after the eligibles list.

### Sentry & logging

- The single `CaptureError` at the loop-call site replaces the four (AFSM) / two (S6) per-error-site `CaptureError`/`Error` calls inside the current loop. The helper returns `error` carrying the underlying cause (wrapped via `fmt.Errorf("...: %w", err)`); the loop attaches `username` (+ `department` for AFSM) as structured fields.
- This consolidates Sentry fingerprints: all per-member failures inside a given command now group under a single issue (`"AFSM member evaluation failed"` / `"S6 IT member evaluation failed"`) instead of one issue per error site. The underlying error string (milpac fetch / record date parse / award date parse / uniform URL parse) remains visible in the captured exception, so triage is still fine — it just requires opening the issue's events rather than reading the title. Acceptable churn: existing Sentry issues for these handlers will close and reopen under the new fingerprints; CAVBOT2-4 itself is already going away with the Keycloak removal.
- All `HandleError` calls *inside* the loop are removed — they existed only to communicate the abort to the user, which no longer happens. `HandleError` calls *outside* the loop (initial response, roster fetch failure, empty-roster early return, final response edit) are unchanged.

### Dead-code removal

After the two call sites migrate:

- Delete `GetMilpacByKeycloakID` from `utils/milpacs.go:165-169`.
- Delete `KeycloakID string \`json:"keycloakId"\`` from `ProfileResponse` (line 27) and `LiteProfileResponse` (line 47).

Go's JSON unmarshaller silently ignores unknown fields, so dropping the struct fields is safe even though `api.7cav.us` will continue returning `keycloakId` in payloads.

## File-level change summary

| File | Change |
|---|---|
| `commands/afsm.go` | Extract `evaluateAFSMMember` helper; rewrite handler loop body; add skipped-count footer |
| `commands/s6_trackers.go` | Extract `evaluateS6Member` helper; rewrite handler loop body; add skipped-count footer |
| `utils/milpacs.go` | Delete `GetMilpacByKeycloakID`; remove `KeycloakID` fields from both profile structs |

No changes to `commands/registry.go`, command definitions, CI workflows, `.env.example`, Sentry plumbing, or Docker compose.

## Verification plan

1. `go build -o cavbot2 .` succeeds.
2. `go vet ./...` clean.
3. `golangci-lint run --timeout=5m` clean (matches CI).
4. `go test ./...` passes existing tests; coverage floors hold (utils 20% / commands 1%). No new tests added in this PR.
5. Manual smoke in the test guild: run `/afsm S6` and `/s6-it-check`. Confirm:
   - Eligible-members list renders for a roster known to include at least one Keycloak-less member.
   - Pre-fix, the same invocation returned `❌ Failed to fetch milpac: no milpac found`; post-fix it returns the eligibles list.
   - If we can force a per-member error (e.g., point at a fixture with a malformed record date in a dev setup), the footer renders correctly and the report still shows the rest. If we can't reproduce a real error path before merge, this is acceptable — the control flow change is small and tested by review.

## Follow-ups (not in this PR)

- `commands/afsm.go` and `commands/s6_trackers.go` are good candidates for the next round of `runX(responder, i)` conversion under [#76](https://github.com/7Cav/cavbot2/issues/76). The helper extraction here removes the largest blocker (deep nesting), so the conversion follow-up should be small.
- If empty-eligibles UX for `/s6-it-check` ever becomes a complaint, mirror the AFSM 0.7.8 treatment (graceful message + Sentry capture).
