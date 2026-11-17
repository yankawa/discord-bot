# Discord Bots

Two independent Discord applications managed in one repository.

| Service | Purpose |
| --- | --- |
| `times-bot` | Assigns channel management to the creator of a new channel in configured categories |
| `utils-bot` | Replies to X/Twitter post links using FxTwitter-compatible preview URLs |

Each application has its own token, configuration, process, and persistent state. You can install either service without installing the other.

**Language:** [日本語](README.md) | English

## Requirements

- Two Discord applications when running both services, or just one application for a single service.
- A Discord server where you can configure bot permissions and intents.
- Docker with the Compose plugin, or Go 1.26+ for local builds (CI uses Go 1.27.4).

Create bot tokens in the [Discord Developer Portal](https://discord.com/developers/applications). Never commit credentials.

## Install one service

**Times Bot only** — requires only `.env.times`:

```sh
git clone https://github.com/astrapath-dev/discord-bot.git
cd discord-bot
cp .env.times.example .env.times
# Set DISCORD_TOKEN, GUILD_ID, CATEGORY_IDS in .env.times
docker compose -f compose.times.yaml up -d --build
```

**Utils Bot only** — requires only `.env.utils`:

```sh
git clone https://github.com/astrapath-dev/discord-bot.git
cd discord-bot
cp .env.utils.example .env.utils
# Set DISCORD_TOKEN and GUILD_ID in .env.utils
docker compose -f compose.utils.yaml up -d --build
```

Use `docker compose -f compose.times.yaml logs -f` or `docker compose -f compose.utils.yaml logs -f` to inspect logs. The Compose files have independent services and state volumes; neither service requires the other service's environment file.

## Install both services

```sh
cp .env.times.example .env.times
cp .env.utils.example .env.utils
# Configure both files with different Discord tokens
docker compose up -d --build
```

The default `compose.yaml` runs both applications. Each service may also be started independently using a dedicated Compose file.

Alternatively, build the binaries without Docker:

```sh
make build           # both
make build-times     # only Times Bot
make build-utils     # only Utils Bot
./bin/times-bot -env .env.times
./bin/utils-bot -env .env.utils
```

The `-check-config` flag validates local configuration without connecting to Discord. Separate Linux amd64 archives are available as GitHub Actions artifacts for each bot. Tagged releases (`v*`) publish per-bot archives and SHA-256 checksums to GitHub Releases.

## Permissions and behavior

### Times Bot

Required permissions include **View Audit Log** at the server level, and **View Channel**, **Manage Channels**, **Manage Permissions**, and **Connect** in the target categories. It needs the `Guilds` and `GuildModeration` gateway intents.

For channels created in selected categories, it identifies the creator by the channel ID in the Discord audit log, denies general channel-management permissions, and grants management of that channel to the creator. It periodically reconciles ownership and saves state to `owner.json`.

**Important:** Discord's `Manage Channels` permission also permits editing or deleting the category itself. Discord has no independent "create channel only" permission. Granting category-level permission to `@everyone` carries that risk, including periods when the bot is offline. Guild owners and administrators can override channel permission denies. Newly created channels are secured after creation, not atomically.

Pre-existing channels are not automatically adopted on the bot's first run.

### Utils Bot

Enable **Message Content Intent** in the Discord Developer Portal. Provide **View Channel**, **Send Messages**, **Embed Links**, **Read Message History**, and **Send Messages in Threads** where applicable.

It identifies supported X/Twitter post URLs and replies with `fxtwitter.com` URLs. It does not edit or delete the original message. It tracks edits and deletions of processed posts and reconciles persisted state from `links.json`.

Code blocks, inline code, spoiler text, and angle-bracketed URLs are intentionally ignored. Preview rendering depends on Discord and the external FxEmbed service. This application does not perform content moderation.

## State and upgrades

The Compose configurations use the same `owner-data` and `links-data` volume names across combined and individual deployments. Do not use `docker compose down -v` unless deleting stored state is intentional. Keep only one running instance of each service per state file.

Never run both services with the same token. Review permissions and test with non-administrator accounts before production use.

## Development and community

Run `go test ./...`, `go test -race ./...`, and `go vet ./...` before submitting changes. Read [Contributing](CONTRIBUTING.md), [Security Policy](SECURITY.md), and [Code of Conduct](CODE_OF_CONDUCT.md).

This project is licensed under the [MIT License](LICENSE). Third-party license texts are retained in [licenses/](licenses/README.md). GitHub Dependabot monitors Go modules, GitHub Actions, and Docker dependencies. Govulncheck runs on dependency changes and on a weekly schedule.
