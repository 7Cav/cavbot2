[![Build Test](https://github.com/7Cav/cavbot2/actions/workflows/build_test.yml/badge.svg)](https://github.com/7Cav/cavbot2/actions/workflows/build_test.yml)
[![Deploy](https://github.com/7Cav/cavbot2/actions/workflows/build_and_push.yml/badge.svg)](https://github.com/7Cav/cavbot2/actions/workflows/build_and_push.yml)

# CavBot2 Readme

A Discord bot built for the 7th Cavalry Gaming Regiment using Go and DiscordGo, enabling various functions for 7Cav members.

## Prerequisites

- Go 1.23 or higher
- DiscordGo library
- A working Go development environment

## Setup

Under Construction due to rewrite of bot
TODO: Add setup steps and list out functionality

## Testing

Run the tests:

```bash
go test ./...
```

Run tests with the coverage floor check (matches CI):

```bash
go test ./... -race -cover | .github/scripts/check-coverage-floors.sh
```

CI enforces per-package coverage floors via `.github/scripts/check-coverage-floors.sh`. When a PR raises a package's coverage by more than a point or two, raise its floor in the same PR — that's how the suite ratchets up without the team having to think about it.

## Contributing

Contributions are welcome through issues and pull requests on our GitHub repository.

## License

Licensed under the [MIT License](https://opensource.org/licenses/MIT).
