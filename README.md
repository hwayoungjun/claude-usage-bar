# claude-usage-bar

macOS menu bar widget that displays your Claude Code rate limit usage in real time.

<img src="screenshot.png" alt="claude-usage-bar screenshot" width="300">

## How it works

Claude Code sends rate limit data via the `statusLine` hook on every assistant message. This tool captures that data and displays it in your macOS menu bar.

- **Menu bar** — shows 5h session and 7d weekly usage at a glance
- **Display modes** — toggle between `5h + 7d` (full) and `5h only` (short) from the **Display** submenu; preference is persisted
- **Dropdown** — detailed view with progress bars and reset times
- **Recent sessions** — shows last 5 sessions; click to copy `claude --resume` command
- **Auto-refresh** — updates every time you chat with Claude Code
- **Inactive state** — shows ⏸ when Claude Code hasn't been used for 10+ minutes

## Install

```bash
brew tap hwayoungjun/tap
brew install claude-usage-bar
```

Setup is automatic — `~/.claude/settings.json` is configured on install and every app launch.

Or build from source:

```bash
git clone https://github.com/hwayoungjun/claude-usage-bar.git
cd claude-usage-bar
go build -o claude-usage-bar .
./claude-usage-bar setup
```

### Makefile

Common tasks are available via `make`:

```bash
make build              # build ./bin/claude-usage-bar
make dev                # run in foreground (debugging)
make install            # install to /usr/local/bin (or PREFIX=/opt/homebrew on Apple Silicon)
make uninstall          # remove binary + run app uninstall
make setup              # configure ~/.claude/settings.json
make release            # build darwin arm64 + amd64 binaries
make help               # list all targets
```

## Usage

```bash
claude-usage-bar                # Launch (backgrounds automatically)
```

Auto-start on login (pick one):

```bash
brew services start claude-usage-bar    # via Homebrew service
```

Or enable **"Launch at Login"** from the dropdown menu.

## Uninstall

```bash
brew uninstall claude-usage-bar
```

This automatically removes the LaunchAgent, statusLine config, and app data.

## Requirements

- macOS (Apple Silicon / Intel)
- Claude Code v2.1.80+ (for `rate_limits` in statusLine)
- Claude Pro / Max / Team plan (rate limit data requires a subscription)

## How data flows

```
Claude Code ──stdin──▶ claude-usage-bar statusline ──▶ ~/.config/claude-usage-bar/usage.json
                                                              │
                                                              ▼
                                                     claude-usage-bar (menu bar)
```

1. Claude Code calls `claude-usage-bar statusline` after each assistant message
2. The statusline subcommand parses rate limit data from stdin and writes to `usage.json`
3. The menu bar widget watches `usage.json` via fsnotify and updates instantly

## License

MIT

> This project is not affiliated with Anthropic. Claude and Claude Code are trademarks of Anthropic.
