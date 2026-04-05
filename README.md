# shellwire

A fast, modern Telegram bot that gives you a full interactive terminal from your phone. Written in pure Go.

> Inspired by the idea of a shell-over-Telegram bot, shellwire is a ground-up Go rewrite — faster, leaner, and actively maintained.

## Features

- Run any shell command via `/run`
- Full interactive terminal support — htop, vim, nano, and more
- Arrow keys, Ctrl+C, Ctrl+D, and signals via inline keypad or commands
- File view and edit via `/file`
- File upload and download
- Per-chat context: shell, cwd, env, terminal size
- Multi-user access control with one-time tokens

## Setup

```bash
cp config.example.json config.json
# Edit config.json with your bot token and Telegram user ID
go build -o shellwire .
./shellwire
```

## Configuration

```json
{
  "authToken": "YOUR_TELEGRAM_BOT_TOKEN",
  "owner": YOUR_TELEGRAM_USER_ID
}
```

Get a bot token from [@BotFather](https://t.me/BotFather). Your Telegram user ID can be found via [@userinfobot](https://t.me/userinfobot).

## Commands

| Command | Description |
|---|---|
| `/run <cmd>` | Execute a command |
| `/r` | Shortcut: run or enter depending on state |
| `/enter [text]` | Send input to running command |
| `/type [text]` | Send text without newline |
| `/control <letter>` | Send Ctrl+letter |
| `/meta [text]` | Send Alt+key |
| `/end` | Send EOF (Ctrl+D) |
| `/cancel [signal]` | Send SIGINT to process group |
| `/kill [signal]` | Send SIGTERM to process |
| `/keypad` | Toggle interactive arrow-key keypad |
| `/redraw` | Force screen repaint |
| `/status` | Show current status |
| `/shell [shell]` | View or change shell |
| `/cd [dir]` | Change working directory |
| `/env [name[=value]]` | View or set environment variable |
| `/resize <cols> <rows>` | Resize terminal |
| `/setsilent [yes\|no]` | Toggle silent output |
| `/setinteractive [yes\|no]` | Toggle interactive shell flag |
| `/setlinkpreviews [yes\|no]` | Toggle link previews |
| `/file <path>` | View/edit a text file |
| `/upload <path>` | Send a file to Telegram |
| `/grant <id>` | Grant access to a chat (owner only) |
| `/revoke <id>` | Revoke access (owner only) |
| `/token` | Generate one-time access token (owner only) |

## Security

shellwire executes real shell commands on your machine. Read [SECURITY.md](SECURITY.md) before deploying.

## Built With

- [go-telegram-bot-api/telegram-bot-api v5](https://github.com/go-telegram-bot-api/telegram-bot-api) — Telegram Bot API client for Go
- [creack/pty](https://github.com/creack/pty) — PTY (pseudo-terminal) support for Go
- [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) — Low-level OS primitives
- [golang.org/x/term](https://pkg.go.dev/golang.org/x/term) — Terminal utilities

## License

MIT
