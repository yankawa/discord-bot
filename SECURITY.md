# Security Policy

## Supported versions

Security fixes are applied to the current `main` branch. Tagged releases may receive security updates at the maintainers' discretion. Older builds are not guaranteed to be supported.

## Reporting vulnerabilities

Do not disclose vulnerabilities or credentials in public issues.

Use [GitHub private vulnerability reporting](https://github.com/astrapath-dev/discord-bot/security/advisories/new) when the repository has it enabled. If it is unavailable, contact a repository maintainer privately through their published GitHub contact channel. Include the affected commit or version, a reproducible example, expected impact, and any proposed mitigation. Do not include live tokens or unrelated user content.

No public response or resolution deadline is guaranteed. Maintainers will investigate reports and coordinate disclosure when possible.

## Deployment precautions

- Keep Discord bot tokens in environment files or a secrets manager, never in Git.
- Grant each bot only the permissions required by its README instructions.
- Use separate applications and tokens for `times-bot` and `utils-bot`.
- Restrict filesystem access to the state files and maintain backups before upgrades.
- Review dependency alerts and updates regularly.
- Test permission changes with a non-administrator account before production use.
