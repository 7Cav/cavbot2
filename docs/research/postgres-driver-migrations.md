# Postgres driver and migrations under CGO_ENABLED=0

Research for [#258](https://github.com/7Cav/cavbot2/issues/258), part of the temp voice channel map ([#255](https://github.com/7Cav/cavbot2/issues/255)). Written 2026-09-15 against `develop` at `f8f04ac`. The store this is for is fixed by `docs/temp-vc-decisions.md`: "Postgres in its own container in cavbot2's compose file, with its own volume", shared later with analytics.

Every library named below was compiled into one static binary with `CGO_ENABLED=0 GOOS=linux` from a scratch module before being recommended. The commands and results are in the verification section at the end.

## Answer

Driver. `github.com/jackc/pgx/v5`, used through `database/sql` via `pgx/stdlib` (`sql.Open("pgx", dsn)`). It is pure Go, so the Dockerfile's `CGO_ENABLED=0` build stays as it is. The `database/sql` route keeps the store on the same `*sql.DB` shape as the forum path in `utils/loa.go`, which means the fetcher-seam test pattern and the `go-sqlmock` dependency already in `go.mod` carry over unchanged, and the migration tool below takes a `*sql.DB` directly. Switch to pgx's native interface only if the store ever needs `LISTEN`/`NOTIFY` or `COPY`; nothing in the decisions doc does.

Migrations. `github.com/pressly/goose/v3` as a library, SQL files in an `embed.FS`, run at process startup through `goose.NewProvider` with `WithSessionLocker(lock.NewPostgresSessionLocker())`, and the bot refuses to start if `Up` returns an error. Run them at startup rather than as a separate step because the deploy is a Watchtower image pull: Watchtower has no hook that can gate a rollout on a migration result, and a startup migration that fails rolls back (goose wraps each file in a transaction) and is retried on the next start. On a redeploy against an existing volume with nothing pending, goose reads `goose_db_version`, finds nothing to apply, and returns an empty result. golang-migrate is ruled out for this deploy shape because a failed migration sets a dirty flag that a human has to clear by hand before the bot will start again. tern is the runner-up if the store goes pgx-native.

Tests. A repository seam with an in-memory fake for the command layer, which is the `loaPostFetcher` pattern the repo already uses, plus a real Postgres for the store package: a `services: postgres:` block in the `build` job of `build_test.yml`, with store tests reading a DSN from an env var and calling `t.Skip` when it is unset. That is the only combination where every test asserts on something a caller can observe (Discord output for commands, rows and errors for the store) and the store package still earns a real coverage floor in CI. pgxmock is out: it mocks pgx's native interfaces, which the stdlib route does not expose, and a driver mock asserts on SQL text, which `CLAUDE.md` defines as a change detector. testcontainers-go is the fallback if the team wants `go test ./...` to be self-sufficient on a laptop, at the price of roughly seventy lines in `go.sum` and a second container per run.

The env var the bot reads is `BOT_DB_DSN`, named to sit next to `FORUM_DB_DSN`. It has to be added to both `.env.example` and the `environment:` block in `docker-compose.yml`, or the container never sees it (the allowlist quirk in `CLAUDE.md`). The compose sketch is at the end.

## What the repo already fixes

The Dockerfile builds with `CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.Version=${VERSION}"` in a `golang:latest` stage and copies only the binary into `alpine:latest` ([Dockerfile](../../Dockerfile)). Two consequences: no C toolchain and no libpq exist at runtime, and anything the binary needs at startup, migration SQL included, has to be inside it or the Dockerfile grows a second `COPY`.

`go.mod` is at `go 1.26.0` and already carries `github.com/go-sql-driver/mysql v1.10.1` and `github.com/DATA-DOG/go-sqlmock v1.5.2` ([go.mod](../../go.mod)). `go.sum` is 56 lines today.

`main.go` opens the forum DB with `sql.Open("mysql", dsn)`, caps it at `SetMaxOpenConns(2)`, and runs the first `Refresh` inline before starting a 15 minute ticker goroutine ([main.go](../../main.go), `initLOACache`). `sql.Open` defers the connection, so a bad DSN only surfaces on first use, which `CLAUDE.md` calls out.

`utils/loa.go` puts the SQL behind a one-method interface, `loaPostFetcher`, with `sqlLOAFetcher` wrapping `*sql.DB` in production and `refresh` taking the interface ([utils/loa.go](../../utils/loa.go)). The file's own comment names it "a narrow test seam only". `utils/loa_test.go` uses a `fakeLOAFetcher` for every cache-logic test and reaches for `go-sqlmock` only where the fake cannot go: a `rows.Err()` failure mid-stream, a connection error on one node out of three, a row that fails to scan ([utils/loa_test.go](../../utils/loa_test.go)). The sqlmock helper pins the query by a regex on the column list line and the comment above it explains the trade: "robust against later changes to FROM/WHERE/ORDER BY formatting while still rejecting an entirely different query". That is the repo's existing verdict on driver mocks. Use them for driver error semantics, not for behaviour.

CI runs on `ubuntu-latest`, executes `go test ./... -race -cover -covermode=atomic | tee /tmp/cover.log`, then `check-coverage-floors.sh` reads that log ([build_test.yml](../../.github/workflows/build_test.yml)). The floor script tracks `utils` at 87 and `commands` at 81, excludes `main`, and errors if a tracked package is missing from the output ([check-coverage-floors.sh](../../.github/scripts/check-coverage-floors.sh)). ADR 0005 adds the rule that matters for a new `store` package: "Don't add new packages without a starter floor" ([ADR 0005](../adr/0005-per-package-coverage-floors.md)).

The compose file has one service with an explicit `environment:` allowlist, one external network `xenforo_internal`, and `restart` commented out ([docker-compose.yml](../../docker-compose.yml)). A GitHub Release builds the image, pushes it to Docker Hub, sleeps ten seconds, and `curl`s `https://watcher.7cav.us/v1/update` ([build_and_push.yml](../../.github/workflows/build_and_push.yml)). Watchtower's HTTP API `/v1/update` "triggers an update for all of the containers monitored by this Watchtower instance" ([Watchtower HTTP API mode](https://containrrr.dev/watchtower/http-api-mode/)), and Watchtower watches every container unless told otherwise ([Watchtower container selection](https://containrrr.dev/watchtower/container-selection/)).

## Driver

### pgx is pure Go and says so

The pgx README opens with "pgx is a pure Go driver and toolkit for PostgreSQL" and "It also includes an adapter for the standard `database/sql` interface" ([pgx README](https://github.com/jackc/pgx/blob/master/README.md)). Its `go.mod` requires `pgpassfile`, `pgservicefile`, `puddle/v2`, `x/sync`, `x/text`, and `testify`; none is a C binding ([pgx go.mod](https://github.com/jackc/pgx/blob/master/go.mod)). A grep of the v5.11.0 source tarball finds no `import "C"` and no cgo build constraint in its 310 Go files; the one `.c` file is a server-side test module. pgx's own CI runs its Windows job with `CGO_ENABLED: 0`, which only works if the library builds without cgo ([pgx ci.yml](https://raw.githubusercontent.com/jackc/pgx/v5.11.0/.github/workflows/ci.yml)).

The scratch build below confirms it: `go list -deps` under `CGO_ENABLED=0` reports zero packages with `CgoFiles` in the whole graph.

Latest is v5.11.0, released 2026-09-07 ([pkg.go.dev pgx/v5](https://pkg.go.dev/github.com/jackc/pgx/v5)). It "supports Go 1.25 and higher and PostgreSQL 14 and higher" ([pgx README](https://github.com/jackc/pgx/blob/master/README.md)); the repo is on Go 1.26.

### database/sql through pgx/stdlib, not the native interface

"Package stdlib is the compatibility layer from pgx to database/sql" ([pkg.go.dev pgx/v5/stdlib](https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib)). A blank import registers the driver names `pgx` and `pgx/v5`, after which `sql.Open("pgx", "postgres://...")` works with either a URL or a `key=value` string. Queries use positional `$1` parameters; named parameters are not supported. If a caller ever needs the native connection, `(*sql.Conn).Raw()` yields the `*stdlib.Conn` and `.Conn()` on it returns the `*pgx.Conn`.

The pgx README's own guidance on choosing: the native interface "is faster" and is recommended when "The application only targets PostgreSQL" and "No other libraries that require `database/sql` are in use" ([pgx README](https://github.com/jackc/pgx/blob/master/README.md)). The second condition fails here on purpose. The forum path is `*sql.DB`, `go-sqlmock` is `*sql.DB`, and goose takes `*sql.DB`. A Discord bot writing a handful of hub rows does not need the native speed, and a second connection abstraction in a codebase this size is a cost with nothing to show for it.

Two things carry over from `initLOACache`. Keep `SetMaxOpenConns` small (the forum path uses 2). And note that pgx's `Conn.Begin` differs from `database/sql`: "there is no auto-rollback on context cancellation" ([pkg.go.dev pgx/v5](https://pkg.go.dev/github.com/jackc/pgx/v5)). That difference vanishes on the stdlib route, which is one more reason to stay on it.

Go 1.27 adds `driver.RowsColumnScanner`, which pgx 5.11.0 implements so Postgres arrays scan straight into Go slices through `database/sql` ([pkg.go.dev pgx/v5/stdlib](https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib)). Not needed today, but array scanning used to be a reason to go native and now is not.

### lib/pq

lib/pq is still maintained: v1.12.3 shipped 2026-04-03 and its README no longer carries the "maintenance mode" notice ([pkg.go.dev lib/pq](https://pkg.go.dev/github.com/lib/pq)). That notice, with the words "we recommend using pgx which is under active development", is what the v1.10.9 README said ([lib/pq v1.10.9 README](https://raw.githubusercontent.com/lib/pq/v1.10.9/README.md)). It is `database/sql` only, has no pool of its own, and none of the three migration tools' pgx paths or pgxmock target it. There is no reason to pick it over pgx for a new store.

## Migrations

### The three tools side by side

| | goose v3.28.0 | golang-migrate v4.20.1 | tern v2.4.3 |
|---|---|---|---|
| Released | 2026-09-02 | 2026-09-09 | 2026-08-23 |
| Go directive | 1.26.0 | 1.25.11 | 1.25.0 |
| Connection type | `*sql.DB` | `*sql.DB` (pgx/v5 driver blank-imports `pgx/stdlib`) | `*pgx.Conn` only |
| Embedded SQL | `goose.SetBaseFS(embed.FS)` or `NewProvider(..., fsys fs.FS)` | `source/iofs`: `iofs.New(fsys, "migrations")` | `Migrator.LoadMigrations(fsys fs.FS)` |
| Version table | `goose_db_version`, one row per applied version | `schema_migrations`, one row: `version`, `dirty` | caller-named, one row holding the current version |
| Already at target | `Provider.Up` "returns empty list and nil error" | `Up` returns the sentinel error `ErrNoChange` | `MigrateTo` returns `nil` before taking the lock |
| Lock | Opt-in: `WithSessionLocker(lock.NewPostgresSessionLocker())`, `pg_try_advisory_lock` with retry | Always: `pg_advisory_lock` | Always: `pg_advisory_lock` |
| Transaction per file | Yes by default; `-- +goose NO TRANSACTION` opts out | No; the tool runs `ExecContext` with no `BeginTx` | Yes by default; `---- tern: disable-tx ----` opts out |
| Failure state | Rolled back; next `Up` retries | `dirty = true`; `Up` refuses until `Force(version)` | Rolled back; next `Migrate` retries |
| Down migrations | Separate `-- +goose Down` section | `*.down.sql` pair, "strongly recommended" | Same file, split by a magic comment |

Sources, row by row. goose: [pkg.go.dev goose/v3](https://pkg.go.dev/github.com/pressly/goose/v3), [goose provider docs](https://pressly.github.io/goose/documentation/provider/), [pkg.go.dev goose/v3/lock](https://pkg.go.dev/github.com/pressly/goose/v3/lock), [goose lock/postgres.go](https://github.com/pressly/goose/blob/main/lock/postgres.go), [goose README](https://github.com/pressly/goose/blob/main/README.md), [goose go.mod](https://github.com/pressly/goose/blob/main/go.mod). golang-migrate: [pkg.go.dev migrate/v4](https://pkg.go.dev/github.com/golang-migrate/migrate/v4), [pkg.go.dev source/iofs](https://pkg.go.dev/github.com/golang-migrate/migrate/v4/source/iofs), [database/pgx/v5/pgx.go](https://github.com/golang-migrate/migrate/blob/master/database/pgx/v5/pgx.go), [migrate FAQ](https://github.com/golang-migrate/migrate/blob/master/FAQ.md), [migrate MIGRATIONS.md](https://github.com/golang-migrate/migrate/blob/master/MIGRATIONS.md), [migrate go.mod](https://github.com/golang-migrate/migrate/blob/master/go.mod). tern: [pkg.go.dev tern/v2/migrate](https://pkg.go.dev/github.com/jackc/tern/v2/migrate), [tern migrate/migrate.go](https://raw.githubusercontent.com/jackc/tern/master/migrate/migrate.go), [tern README](https://raw.githubusercontent.com/jackc/tern/master/README.markdown), [tern go.mod](https://github.com/jackc/tern/blob/master/go.mod).

None of the three says "pure Go" in prose. The evidence is that all three release their own binaries with `CGO_ENABLED=0` ([goose .goreleaser.yaml](https://github.com/pressly/goose/blob/main/.goreleaser.yaml), [tern .goreleaser.yaml](https://raw.githubusercontent.com/jackc/tern/master/.goreleaser.yaml), [migrate Makefile](https://github.com/golang-migrate/migrate/blob/master/Makefile)), and that the scratch build below links each one statically with no cgo package in the graph. Two caveats on dependencies. goose's `go.mod` requires `modernc.org/sqlite` (the cgo-free SQLite); it lands in `go.sum` but `go list -deps` shows it is not linked when only the Postgres dialect is used. golang-migrate's `go.mod` requires `mattn/go-sqlite3`, which is cgo, but it sits behind the `sqlite3` build tag and the pgx/v5 path does not pull it in.

### Why goose

The deploy is unattended. A release pushes an image and Watchtower restarts the container; nobody is watching a terminal. The question that decides the tool is what happens when a migration fails at 03:00.

With golang-migrate the answer is "the bot is down until a human runs `force`". The FAQ: "Execution stops if a migration fails and the dirty state persists, which prevents attempts to run more migrations on top of a failed migration. You need to manually fix the error and then 'force' the expected version" ([migrate FAQ](https://github.com/golang-migrate/migrate/blob/master/FAQ.md)). And because the pgx/v5 driver runs each file as one `ExecContext` with no transaction of its own ([database/pgx/v5/pgx.go](https://github.com/golang-migrate/migrate/blob/master/database/pgx/v5/pgx.go)), a multi-statement file can half-apply unless the author wraps it in `BEGIN`/`COMMIT` by hand, which the driver README asks for ([database/postgres README](https://github.com/golang-migrate/migrate/blob/master/database/postgres/README.md)). Both are reasonable defaults for a CLI a person runs. Neither is what you want at the top of `main()`. It also carries the heaviest dependency file of the three (its `go.mod` is 219 lines against goose's 88 and tern's 23 requirement lines), most of it for databases the bot will never touch.

With goose the answer is "the transaction rolled back, the bot exited non-zero, the next start retries". "By default, all migrations are run within a transaction" ([goose README](https://github.com/pressly/goose/blob/main/README.md)). The Provider API has "no global state" and takes an `fs.FS` directly ([goose provider docs](https://pressly.github.io/goose/documentation/provider/)). Locking is opt-in, and the Postgres session locker "utilizes PostgreSQL's exclusive session-level advisory lock mechanism" with `pg_try_advisory_lock` retried for up to five minutes ([pkg.go.dev goose/v3/lock](https://pkg.go.dev/github.com/pressly/goose/v3/lock)). Use `NewProvider`, not the package-level `goose.Up`; the legacy path has no locking at all (there is no lock call in `up.go`).

tern would be my pick if the store used native pgx. It is small, its `Migrate` fast-paths to `nil` when already at the target version without touching the lock ([tern migrate/migrate.go](https://raw.githubusercontent.com/jackc/tern/master/migrate/migrate.go)), and it comes from the pgx author. But `NewMigrator` takes a `*pgx.Conn`, so on the stdlib route you would have to unwrap a raw connection just to migrate. It also pulls in `Masterminds/sprig` for its SQL templating, which the bot does not need.

One more goose behaviour to know. By default it errors if it finds an unapplied file with a version lower than the database's highest applied version, which the README calls "missing (out-of-order) migrations" ([goose README](https://github.com/pressly/goose/blob/main/README.md)). Two PRs that each add a migration and merge in the wrong order will trip it. Timestamp-named files (`20260915120000_hubs.sql`) plus a rule that the second PR renumbers on rebase is enough for a one-team repo.

### Startup versus a separate step

A "separate step" means something runs the migration before the new bot binary starts serving. With this deploy there is nothing to run it.

Watchtower's model is to "gracefully shut down your existing container and restart it with the same options that were used when it was deployed initially" ([Watchtower](https://containrrr.dev/watchtower/)). It works per container through the Docker API, and by default it only looks at running ones: `--include-stopped` "Will also include created and exited containers" and defaults to false ([Watchtower arguments](https://containrrr.dev/watchtower/arguments/)). So a one-shot `migrate` service with `depends_on: condition: service_completed_successfully` would run on a manual `compose up`, then sit exited and invisible to every release after that.

Watchtower does have lifecycle hooks, but they do not fit either. They are off unless `--enable-lifecycle-hooks` is set, they run with `sh` inside the container being updated, and "The failure of a command to execute, identified by an exit code different than 0 or 75 (EX_TEMPFAIL), will not prevent watchtower from updating the container" ([Watchtower lifecycle hooks](https://containrrr.dev/watchtower/lifecycle-hooks/)). The pre-update hook runs in the old container, so it would execute the old binary's migrations. The post-update hook runs after the new container is up, which is after the bot has already connected to Discord. A step that cannot block the rollout and cannot run the right code is not a gate.

Running migrations at startup is the honest option because it makes the process itself the gate: no migration, no bot. The cost is that a failing migration takes the bot down. That is the right failure. A bot running against a schema it does not understand fails later and less clearly.

### What a Watchtower redeploy looks like against an existing volume

The Postgres container is separate from the bot container and keeps its named volume. Watchtower stops `cavbot2`, pulls, and starts the new image with the old options. The new binary then:

1. Opens `BOT_DB_DSN`. `sql.Open` does not connect, so add a `PingContext` with a short retry before migrating; otherwise a Postgres that is mid-restart fails the migration step for a transient reason.
2. Calls `Provider.Up`. goose takes the session lock, lists `goose_db_version`, and diffs it against the embedded files. If everything is applied, "this method returns empty list and nil error" ([pkg.go.dev goose/v3](https://pkg.go.dev/github.com/pressly/goose/v3#Provider.Up)) and the bot carries on. If files are pending, each runs in its own transaction. On failure, that file's transaction rolls back, `Up` returns the error, and the bot should exit non-zero.
3. Connects to Discord only after step 2 succeeds.

Rolling back a release is the one case to think about. If a bad image is replaced with the previous one, the older binary's embedded FS lacks the newest migration file. goose only walks the versions it has on disk (`UpVersions` in `internal/gooseutil/resolve.go` iterates `fsysVersions`; a version that exists only in the database is never considered), so the older binary starts cleanly against the newer schema. Whether it then works depends on the migration. Additive migrations (new table, new nullable column) are safe to roll over. A rename or drop is not, and would need the `Down` section run by hand before the old image goes back. The practical rule: every migration must be compatible with the release before it.

The `restart:` line in `docker-compose.yml` is commented out today. With startup migrations that choice becomes visible: no restart policy means a failed migration leaves the container stopped, which is loud; `unless-stopped` means it retries on a loop, each attempt rolling back cleanly. Either is defensible. I would take `unless-stopped` so that a transient DB outage at boot heals itself.

## Test strategy

The standard to hold each option to comes from `CLAUDE.md`: a test should go red when "an externally meaningful contract is broken" and stay green when "implementation details, wording, structure, formatting" change. For a store, the externally meaningful contract is "given these calls, these rows come back and these errors are returned". The SQL text is an implementation detail.

### pgxmock

pgxmock "is a mock library implementing pgx interfaces" and "is based on the well-known sqlmock library" ([pgxmock README](https://github.com/pashagolub/pgxmock/blob/master/README.md)). It mocks `pgx.Conn`, `pgxpool.Pool`, and `pgx.Tx`, so it only applies if the store uses pgx's native interface. On the stdlib route the equivalent is `go-sqlmock`, already in `go.mod`.

Either way the test asserts on SQL text, and the repo has already met that cost once: the regex-pinning comment in `loa_test.go` is there to stop a full-query match from breaking on formatting changes. That is a change detector by the repo's own definition.

The scratch build also turned up a coupling cost. pgxmock's `v4` module path resolves to v4.9.0 (2025-10-06), which declares `pgx/v5 v5.7.4` and does not compile against pgx v5.11.0: pgx added `TypeMap()` to the `pgx.Rows` interface, and its changelog says "Custom implementations of `Rows`, including mocks, must add this method" ([pgx CHANGELOG, 5.11.0](https://github.com/jackc/pgx/blob/master/CHANGELOG.md)). The maintainer had already moved to a `v5` module path; v5.2.0 shipped 2026-09-08, one day after pgx, and builds cleanly ([pkg.go.dev pgxmock/v5](https://pkg.go.dev/github.com/pashagolub/pgxmock/v5)). So the project is well kept, but a driver mock is by nature coupled to the driver's interface, and a pgx minor release can require a mock major bump. Dependabot would bump pgx and pgxmock in separate PRs unless they land in the same group.

It costs CI nothing. Coverage comes out high and cheap, which is the trap: it covers lines without testing the store.

### Repository seam with an in-memory fake

This is `loaPostFetcher` again, one level up. The command layer talks to a `HubStore` interface (or whatever the spec names it); production wires the Postgres implementation, tests wire a map-backed fake. Command tests then assert on what Discord sees: the channel that got created, the ephemeral reply, the error path. `utils/loa_test.go` shows the pattern working: a dozen or so tests build a `fakeLOAFetcher`, four build a sqlmock DB.

What it does not do is test the store. The fake proves the commands behave given a store that behaves; nothing proves the SQL returns the rows the fake pretends to. With this option alone, the `store` package would have only whatever coverage its constructors and DSN parsing give, and the floor would be a token number.

It costs CI nothing, and it keeps `-race` meaningful, since the fake can be exercised concurrently the way `concurrentLOAFetcher` is.

### A Postgres service container in GitHub Actions

GitHub runs service containers as "Docker containers" alongside the job; "If you are using GitHub-hosted runners, you must use an Ubuntu runner", which the `build` job already is ([About service containers](https://docs.github.com/en/actions/using-containerized-services/about-service-containers)). When the job runs on the runner machine rather than in a container, "You can access service containers from the Docker host using `localhost` and the Docker host port number", so the `ports: 5432:5432` mapping is required ([Creating PostgreSQL service containers](https://docs.github.com/en/actions/using-containerized-services/creating-postgresql-service-containers)). GitHub's own example is:

```yaml
services:
  postgres:
    image: postgres
    env:
      POSTGRES_PASSWORD: postgres
    options: >-
      --health-cmd pg_isready
      --health-interval 10s
      --health-timeout 5s
      --health-retries 5
    ports:
      - 5432:5432
```

Pin the image to the same major as production (`postgres:18-alpine`) instead of bare `postgres`. The store tests read `BOT_DB_DSN_TEST` (or reuse `BOT_DB_DSN`; the spec can decide) and `t.Skip` when it is empty, so `go test ./...` still passes on a laptop with no database. In CI the variable is set on the `Run tests with coverage` step and the store tests run for real.

This is the only option where store tests assert on caller-observable behaviour: call `CreateHub`, call `ListHubs`, compare. The SQL can be rewritten freely and the tests stay green as long as the rows come back the same.

Coverage: `go test ./... -race -cover` reports the `store` package with its integration tests included, so the floor script sees a real number and ADR 0005's starter floor can be honest from day one. The catch is that `check-coverage-floors.sh` run locally without a database would report `store` below its floor, because the tests skipped. ADR 0005 says "Local check matches CI", and this breaks that for one package. The fix is one line in the README: `docker run --rm -e POSTGRES_PASSWORD=x -p 5432:5432 postgres:18-alpine` and export the DSN.

Isolation inside the package: the tests share one database, so each test needs a clean slate. `TRUNCATE ... RESTART IDENTITY CASCADE` in a `t.Cleanup` is enough for a schema this size, and it keeps the tests sequential within the package, which they would be anyway (Go runs packages in parallel, not tests within a package unless they call `t.Parallel`).

The CI cost is the image pull plus the health wait, once per job. Measured locally on this machine (Docker 29.5.3, arm64): `docker pull postgres:17-alpine` took 6.6 s and a cold start to the second "ready to accept connections" line took 1.2 to 1.5 s across two runs. A GitHub runner's pull will differ and I did not measure it. The health check as written polls every 10 s, so the first success can land up to 10 s after the server is actually ready; `--health-interval 2s` shortens that.

### testcontainers-go

Same real Postgres, started from Go instead of YAML. `postgres.Run(ctx, "postgres:16-alpine", postgres.WithDatabase(...), postgres.WithUsername(...), postgres.WithPassword(...), postgres.BasicWaitStrategies())` returns a container whose `ConnectionString(ctx, "sslmode=disable")` is the DSN ([testcontainers-go postgres module](https://golang.testcontainers.org/modules/postgres/)). It "requires a Docker-API compatible container runtime" ([testcontainers-go Docker requirements](https://golang.testcontainers.org/system_requirements/docker/)). The docs have no GitHub Actions page (the URL returns 404), but the project's own CI runs its full test matrix on `ubuntu-latest` with no Docker setup step ([testcontainers-go ci.yml](https://github.com/testcontainers/testcontainers-go/blob/main/.github/workflows/ci.yml)), and it runs that matrix with `-race` ([commons-test.mk](https://github.com/testcontainers/testcontainers-go/blob/main/commons-test.mk)), so both hold on GitHub-hosted runners.

Two things it offers that the service container does not. `go test ./...` becomes self-sufficient on any machine with Docker, no manual `docker run`. And the module's `Snapshot()`/`Restore()` resets the database to a post-migration template between tests, so the per-test cleanup code goes away ([testcontainers-go postgres module](https://golang.testcontainers.org/modules/postgres/)).

Two costs. Its `go.mod` has 16 direct and 35 indirect requirements, including the moby client and OpenTelemetry ([testcontainers-go go.mod](https://raw.githubusercontent.com/testcontainers/testcontainers-go/main/go.mod)); in the scratch module it took `go.sum` from 141 to 209 lines. That is test-only code, but `go.sum` and Dependabot do not know that. And each test run starts a reaper container, Ryuk, alongside the database, which "removes containers/networks/volumes created by Testcontainers for Go after a specified delay" ([testcontainers-go garbage collector](https://golang.testcontainers.org/features/garbage_collector/)). Latest is v0.44.0, 2026-08-07, on Go 1.25 ([pkg.go.dev testcontainers-go](https://pkg.go.dev/github.com/testcontainers/testcontainers-go)). The scratch build confirms its test graph is cgo-free.

The CI cost is the same pull and start as the service container, plus Ryuk, and it happens inside `go test` rather than before it, so the `-race` run gets longer by that amount.

### Recommendation

Seam plus service container. The command layer gets the fake it already knows how to use. The store gets tests that only know its public methods. CI gets a real Postgres for the cost of a YAML block and an env var. Keep `go-sqlmock` for the one or two driver-error tests the fake cannot express, following the `loa_test.go` precedent, and do not add pgxmock.

Revisit testcontainers-go if the store's test suite grows past what `TRUNCATE` handles comfortably, or if contributors keep tripping over the missing local database. It is a swap of the setup code, not of the tests.

## Compose sketch

Matches the decisions doc: Postgres in its own container in this compose file, with its own volume. Values that belong in `.env` are shown as interpolations.

```yaml
services:
  cavbot2:
    image: 7cav/cavbot2:latest
    container_name: cavbot2
    restart: unless-stopped
    depends_on:
      postgres:
        condition: service_healthy
    environment:
      BEARER: ${BEARER}
      DISCORD_TOKEN: ${DISCORD_TOKEN}
      GUILD_ID: ${GUILD_ID}
      BM_TOKEN: ${BM_TOKEN}
      LOG_LEVEL: ${LOG_LEVEL}
      DISCORDGO_LOG_LEVEL: ${DISCORDGO_LOG_LEVEL}
      FORUM_DB_DSN: ${FORUM_DB_DSN}
      LOA_NODE_IDS: ${LOA_NODE_IDS}
      WARDEN_ROLE_BASE_NAME: ${WARDEN_ROLE_BASE_NAME}
      SENTRY_DSN: ${SENTRY_DSN}
      APP_ENV: ${APP_ENV}
      BOT_DB_DSN: ${BOT_DB_DSN}
    networks:
      - xenforo_internal
      - cavbot_internal

  postgres:
    image: postgres:18-alpine
    container_name: cavbot2-db
    restart: unless-stopped
    environment:
      POSTGRES_USER: cavbot
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: cavbot
    volumes:
      - cavbot2_pgdata:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U cavbot -d cavbot"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 10s
    labels:
      com.centurylinklabs.watchtower.enable: "false"
    networks:
      - cavbot_internal

volumes:
  cavbot2_pgdata:

networks:
  xenforo_internal:
    external: true
  cavbot_internal:
```

And in `.env.example`:

```
POSTGRES_PASSWORD=password
BOT_DB_DSN=postgres://cavbot:password@postgres:5432/cavbot?sslmode=disable
```

Notes on each choice.

`BOT_DB_DSN` appears in both `.env.example` and the `environment:` block. `CLAUDE.md` records what happens otherwise: the value reaches compose's interpolator and never reaches the process. `POSTGRES_PASSWORD` only needs `.env`, because compose interpolates it into the `postgres` service directly; it is not read by the bot. The password is typed twice, once for the image and once inside the DSN, and the two have to agree. That is the same shape as `FORUM_DB_DSN`, which also carries its password inline.

`image:` is `7cav/cavbot2:latest` here because Watchtower can only pull a registry tag, and that is the name `build_and_push.yml` pushes ([CLAUDE.md](../../CLAUDE.md), "Versioning & deploy"). The repo's compose file says `cavbot2:latest`, the tag the README's local `docker build` step produces. Whatever the host's copy says today, it has to name the Hub image for Watchtower to have anything to pull.

`postgres:18-alpine` pins the major. PostgreSQL's own upgrade page: "For major releases of PostgreSQL, the internal data storage format is subject to change", while minor releases "never change the internal storage format" ([PostgreSQL upgrading](https://www.postgresql.org/docs/current/upgrading.html)). A floating `latest` would one day pull 19 against an 18 data directory, which by that rule it cannot read. pgx supports PostgreSQL 14 and up, so 17 is equally fine; 18 avoids one major upgrade later.

The volume mounts at `/var/lib/postgresql`, not `/var/lib/postgresql/data`, because of 18. The image docs: "The defined `VOLUME` was changed in 18 and above to `/var/lib/postgresql`" and "Mounts and volumes should be targeted at the updated location" ([postgres image docs](https://github.com/docker-library/docs/blob/master/postgres/README.md)). For 17 and below the same docs say to mount at `/var/lib/postgresql/data`. Get this wrong on 18 and the data lands outside the volume.

`POSTGRES_PASSWORD` "is required for you to use the PostgreSQL image. It must not be empty or undefined" ([postgres image docs](https://github.com/docker-library/docs/blob/master/postgres/README.md)). `POSTGRES_USER` and `POSTGRES_DB` only take effect on an empty data directory; "any pre-existing database will be left untouched on container startup". So they create the role and database once, on first boot, and are inert on every redeploy after that.

`pg_isready` "is a utility for checking the connection status of a PostgreSQL database server" and exits 0 when the server is accepting connections ([pg_isready](https://www.postgresql.org/docs/current/app-pg-isready.html)). `depends_on` with `condition: service_healthy` makes `docker compose up` wait for it, which covers a host reboot. It does not cover a Watchtower recreate, since that goes through the Docker API and only touches the bot container; Postgres is already up in that case, so nothing is lost. The ping-with-retry at startup is there for the remaining gap, a Postgres that is restarting at the moment the bot comes up.

The Watchtower label keeps a bot release from restarting the database. `/v1/update` updates every monitored container, and `com.centurylinklabs.watchtower.enable="false"` removes one from the set ([Watchtower container selection](https://containrrr.dev/watchtower/container-selection/)). The trade is that Postgres minor updates then need a manual `docker compose pull postgres && docker compose up -d postgres`. I think that is the right side of the trade: minor updates are safe for data but a restart mid-command is not something a bot release should cause.

`cavbot_internal` is a new default-driver network so the bot and its database talk without touching the forum's `xenforo_internal`. The DSN's host is the service name, `postgres`, which compose resolves on that network.

`sslmode=disable` is fine on a private compose network between two containers on one host. Drop it, or set `sslmode=require`, the day the database moves to another machine.

## Verification

A scratch module outside the repo, `go 1.26.0`, importing `pgx/v5/stdlib`, `pgx/v5/pgxpool`, `pressly/goose/v3` with `SetBaseFS` on an `embed.FS`, `jackc/tern/v2/migrate`, `golang-migrate/migrate/v4` with `database/pgx/v5` and `source/iofs`, and in a test file `testcontainers-go/modules/postgres`. Versions resolved by `go get @latest` on 2026-09-15:

```
github.com/jackc/pgx/v5 v5.11.0
github.com/pressly/goose/v3 v3.28.0
github.com/jackc/tern/v2 v2.4.3
github.com/golang-migrate/migrate/v4 v4.20.1
github.com/testcontainers/testcontainers-go v0.44.0
github.com/pashagolub/pgxmock/v5 v5.2.0
```

```
$ CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o main .
$ file main
main: ELF 64-bit LSB executable, ARM aarch64, version 1 (SYSV), statically linked, Go BuildID=..., stripped
$ CGO_ENABLED=0 GOOS=linux go list -deps -test -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' . | sort -u
(no output)
$ CGO_ENABLED=0 GOOS=linux go list -deps . | grep -E 'modernc|sqlite|mattn'
(no output)
```

The first `go list` is the check that matters: no package in the build or test graph has cgo files. The second shows goose's SQLite dependency stays out of the binary on the Postgres path.

`pashagolub/pgxmock/v4@latest` (v4.9.0) in the same module fails to type-check against pgx v5.11.0 with `*rowSets does not implement pgx.Rows (missing method TypeMap)`. `pgxmock/v5@latest` (v5.2.0) builds.

The Postgres timing came from `docker run -d postgres:17-alpine` with `POSTGRES_PASSWORD` set, polling `pg_isready` and then waiting for the second "database system is ready to accept connections" log line (the image starts a temporary server for init scripts first). 1.5 s and 1.2 s on two runs; pull 6.6 s. Local numbers on a laptop, not a GitHub runner.

## What I could not determine

How long the service container adds on a GitHub-hosted runner. The local figures above bound the start; the runner's pull time depends on its network and image cache and I did not measure it.

Whether the host's Watchtower is configured with lifecycle hooks, label filters, or a scope. That is host configuration, not in this repo. The sketch assumes defaults, which is what the `/v1/update` call implies.

Whether the store will ever want `LISTEN`/`NOTIFY` or `COPY`. Nothing in `docs/temp-vc-decisions.md` suggests it. If the analytics work later does, the stdlib route can hand out a raw `*pgx.Conn` for that one use without changing the rest.

The wayfinding rules in `docs/agents/issue-tracker.md` say a resolved ticket also gets one line appended to the map's "Decisions so far". This ticket's instructions said not to edit #255, so that line is left for the map's owner.
