# Contributing

Contributions to Discord Bots are welcome. The project contains two independent services: **times-bot** (channel ownership) and **utils-bot** (link previews).

## Before opening a pull request

- Search existing issues and pull requests. Open an issue first for changes to permissions, persistence, or message handling.
- Make focused changes and describe their user-visible effects.
- Never commit Discord bot tokens, server data, production state files, or screenshots containing private messages.
- Do not add gateway intents or permissions without explaining why they are required.

## Development

Install Go 1.26 or newer. Go 1.27.4 is used in CI.

```sh
go mod download
go test ./...
go test -race ./...
go vet ./...
make build
```

Go source must be formatted with `gofmt`; check that `go mod tidy` does not change `go.mod` or `go.sum` unexpectedly.

Each service can be run independently:

```sh
cp .env.times.example .env.times
docker compose -f compose.times.yaml up -d --build
```

```sh
cp .env.utils.example .env.utils
docker compose -f compose.utils.yaml up -d --build
```

Never use real production credentials in automated tests. Tests that exercise Discord REST should use mocks. Changes to permission enforcement must cover non-admin users, missing audit-log entries, and recovery after a restart.

## Pull requests

Include a concise summary, tests run, related issue links (if any), and any effect on permissions, environment variables, storage formats, or deployment. Documentation changes are required for changes to user-visible behavior.

By submitting a contribution, you agree that it may be distributed under the repository's MIT License, unless the contribution explicitly carries a different compatible license that is clearly disclosed and approved.
