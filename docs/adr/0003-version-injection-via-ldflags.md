# ADR 0003: Version injected via ldflags from the release tag

## Status

Accepted (0.7.4 / PR #70). Replaces the prior `var Version = "X.Y.Z"`
manual-bump convention.

## Decision

`var Version = "dev"` in `main.go` is overwritten at Docker build via
`-ldflags "-X main.Version=${VERSION}"`. `VERSION` is passed as a
`docker build --build-arg` from `github.ref_name` in `build_and_push.yml`.

The release tag is the single source of truth for the running version.

## Why

The previous flow required a separate `ver bump` PR before each release —
easy to forget, which would ship the wrong version string. Tying `Version` to
the tag removes the human step.

## How to apply

- Cutting a release: `gh release create <tag> --target develop --generate-notes`.
- Local `go build` / `go run` will show `dev` unless you pass the ldflag
  yourself. That's the intended signal: any prod log showing `version=dev`
  means the deploy didn't go through `build_and_push.yml`.
