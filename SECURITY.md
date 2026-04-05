# Security

shellwire gives anyone with access to your bot the ability to run arbitrary shell commands on your machine with the same privileges as the user running shellwire. Read this before deploying.

## Access Control

- Only the Telegram user ID set as `owner` in `config.json` has access by default.
- The owner can grant access to other chat IDs using `/grant` and revoke it with `/revoke`.
- One-time access tokens (via `/token`) expire after a single use.
- **Never share your bot token or grant access to untrusted users.**

## Bot Token

- Your bot token is equivalent to full control over the bot. Keep it secret.
- If your token is compromised, revoke it immediately via [@BotFather](https://t.me/BotFather) and update `config.json`.
- Do not commit `config.json` to version control. Add it to `.gitignore`.

## Deployment Recommendations

- Run shellwire as a dedicated low-privilege user, not as root.
- Use a firewall to restrict outbound connections if possible.
- Run on a machine you trust and control — not a shared server.
- Consider running inside a container or VM to limit blast radius.
- Keep the host OS and Go runtime up to date.

## What shellwire Can Access

shellwire inherits the full environment of the user running it:

- Filesystem read/write access
- Network access
- Ability to spawn processes
- Access to environment variables (which may contain secrets)

There is no sandboxing. Commands run directly on the host.

## Reporting Vulnerabilities

If you find a security issue, please open a GitHub issue or contact the maintainer directly. Do not disclose publicly until a fix is available.
