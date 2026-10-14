# pingme

A small Go CLI for coding agents to send Telegram notifications to one private chat. Supports Linux and macOS. Credentials stay in a local `.env`; they are never compiled into the executable.

## Build and setup

Requires Go 1.26 or newer and Make.

```sh
make build
./pingme setup
```

Setup guides you through creating a dedicated bot with [BotFather](https://t.me/BotFather), reads the token with terminal echo disabled, and asks you to send a unique pairing message from your phone. It only pairs private chats. You confirm before it writes credentials and sends a test notification. Pairing times out after three minutes. Bots with an existing webhook are rejected; use a dedicated bot so setup doesn't interfere with another application. Setup uses Telegram's update queue, so don't run another polling client for this bot.

The default `.env` is beside the **resolved executable**, regardless of your working directory. Symlinking the binary into your PATH works; the `.env` remains beside the original binary. If you move the binary, move the `.env` with it. For example:

```sh
./pingme setup --env /your/private/config/.env
./pingme send --env /your/private/config/.env 'Hello'
```

The parent directory must exist. Setup writes with permissions `600` and preserves an existing configuration as `.env.bak.<timestamp>`. Backups are also private and ignored by Git. The parser accepts blank lines, comments, and optionally quoted values for these two settings only:

```dotenv
TELEGRAM_BOT_TOKEN=your_bot_token
TELEGRAM_CHAT_ID=your_private_chat_id
```

Do not paste your token into a prompt, shell command, issue, or commit. If configuring manually, use a local editor and `chmod 600 .env`. This file is data, not a shell script; do not source it. Environment variables are not used to override credentials.

## Send

```sh
./pingme send 'Build finished; all tests passed.'
./pingme send --title 'Needs your input' 'Which deployment target should I use?'
printf 'Build complete\nTests passed\n' | ./pingme send -
./pingme send --silent 'Background task finished'
```

Flags go before the message. Messages are plain text with link previews disabled. Empty messages and messages over 4096 UTF-16 units, including the optional title, are rejected rather than split. This is a conservative limit for emoji. Stdin input is bounded at 64 KiB. Telegram requests time out after 35 seconds. Rate limits are reported with their wait time. The CLI does not retry automatically because a timed-out send might already have succeeded.

Exit `0` means success; exit `1` means failure. A successful send means Telegram accepted the message, not that the phone displayed it. Enable notifications for the bot chat on your phone. `--silent` suppresses sound.

## Coding agents

```sh
./pingme agents
```

Give the output to your agent, or add an instruction to your agent's configuration to run `pingme agents` before sending notifications. This command prints examples using the executable's absolute path and never opens `.env` or contacts Telegram. Decide when notifications are wanted in your agent instructions, such as when a task finishes or requires your input.

The `.env` keeps credentials out of prompts and source control, but it does not isolate them from an agent running as your OS user. Such an agent can still read the file. A separate service or OS user would be needed for that boundary.

## Development

```sh
make test
make release
```

Tests use local HTTP servers and fake tokens. `make release` cross-compiles Linux and macOS binaries for amd64 and arm64 into `dist/`. macOS builds are unsigned; native runtime testing is still required. Build artifacts, `.env`, its temporary files, and credential backups are ignored. Only `.env.example` is tracked. Build commands never read `.env`.

See the [Telegram Bot API](https://core.telegram.org/bots/api) for `getMe`, `getWebhookInfo`, `getUpdates`, and `sendMessage`.
