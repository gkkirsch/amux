# amux for agents

This is the practical guide. If you're an AI agent (or writing code that
drives one), start here. The README explains *what* amux is; this
document explains *how to use it well*.

All snippets assume `amux` is on `$PATH` and `tmux` is installed.

---

## Mental model

- **A session** is a named tmux session. One per "workspace". Cheap.
- **A window** inside a session is one agent (or one tool). Cheap.
- **A pane** is a split within a window. Useful for logs next to an agent.
- **A target** is `session`, `session:window`, or `session:window.pane`.

Everything you do in amux is: pick a target, then send input or observe
output. Inputs and observations are separate commands so you can compose
them however you want.

## The 11 commands, at a glance

| Category    | Command     | Use when… |
|-------------|-------------|-----------|
| Lifecycle   | `new`       | creating a new session |
|             | `window`    | adding a window (spawn an agent) |
|             | `split`     | adding a side pane (logs, monitor) |
|             | `rename`    | giving a session/window a better name |
|             | `color`     | visually tagging parallel agents |
|             | `kill`      | tearing down |
|             | `list`      | enumerating state (use `--json` to parse) |
|             | `exists`    | idempotent setup (`amux exists X \|\| amux new X`) |
| Input       | `send`      | literal text into a prompt buffer (no submit) |
|             | `key`       | named keys (Enter, arrows, Escape, C-c …) |
|             | `paste`     | multi-line content into a TUI (Claude, vim, etc.) |
|             | `type`      | char-by-char — needed for TUI search fields |
| Observation | `capture`   | read what's on the pane right now |
|             | `wait-idle` | heuristic: wait until output stops changing |
|             | `wait-for`  | deterministic: wait for a regex to appear |
|             | `run`       | one-shot: paste → submit → wait → emit delta |

For every command, `amux <cmd> -h` prints a command-specific help page
with examples. Do that first if you're unsure.

## Exit-code contract

amux is a well-behaved unix tool. In scripts:

- `0` success
- `1` runtime failure (tmux error, timeout, assertion miss)
- `2` usage error (bad args or unknown command)

`exists` deliberately prints *nothing* on failure — it's meant to be
used in `||` chains.

## Target resolution is EXACT in amux

tmux's own target syntax does **prefix matching** on session names —
`tmux -t foo` matches a session called `foo-bar`. amux's own `exists`
command does **exact matching** (it enumerates via `list-sessions` and
compares). If you need exact resolution in your own scripts, use
`amux exists <target>` rather than `tmux has-session`.

---

## Recipe: spin up Claude and ask one question

```bash
amux new workspace
amux window workspace -n cc -- claude --dangerously-skip-permissions

# Wait for the TUI to draw its prompt — doesn't matter how fast the
# machine is, this polls until the ❯ marker is visible.
amux wait-for workspace:cc '❯' --timeout 45s

# Ask, wait for idle, emit the delta.
echo "what is 17 * 23? reply with just the number" | amux run workspace:cc

amux kill workspace
```

The `run` command is the batteries-included primitive:
1. Read stdin as the prompt
2. Sanitize any embedded bracketed-paste sentinels
3. Paste it with `--submit`
4. `wait-idle` for the pane to stop changing
5. Emit ONLY the new content added since the submit

If you need the full pane capture, use `paste --submit` + `wait-idle` +
`capture` yourself.

## Recipe: back-and-forth over many turns

```bash
amux new sess
amux window sess -n cc -- claude --dangerously-skip-permissions
amux wait-for sess:cc '❯' --timeout 45s

for prompt in "summarize /tmp/report.md in 2 sentences" \
              "translate that summary into French" \
              "now a haiku"; do
  echo "$prompt" | amux run sess:cc --timeout 120s
  echo "--- turn done ---"
done

amux kill sess
```

## Recipe: parallel fleet of agents

```bash
amux new fleet
for name in researcher writer editor; do
  amux window fleet -n "$name" -- claude --dangerously-skip-permissions
done
for name in researcher writer editor; do
  amux wait-for "fleet:$name" '❯' --timeout 45s
done

# Visual tags for a human watching (colors show in tmux status bar).
amux color fleet:researcher cyan
amux color fleet:writer     green
amux color fleet:editor     magenta

echo "find 3 facts about tmux" | amux run fleet:researcher &
echo "draft a paragraph about Linux ergonomics" | amux run fleet:writer &
echo "here's a sentence. Shorten it." | amux run fleet:editor &
wait
```

## Recipe: idempotent setup (safe to re-run)

```bash
amux exists projectA            || amux new projectA
amux exists projectA:cc         || amux window projectA -n cc -- \
                                      claude --dangerously-skip-permissions
amux wait-for projectA:cc '❯' --timeout 45s
```

Since `exists` is silent and returns the exit code, you can sprinkle it
through bootstrap scripts without needing to scrape output.

## Recipe: drive a long-running build in a side pane, watch from another

```bash
amux new work
amux window work -n build -- bash -c 'go build ./... && go test ./...'
amux split work:build -h -- bash -c 'tail -f /tmp/buildlog'

# Block until the build exits (the window closes, then wait-for fails).
amux wait-for work:build 'PASS|FAIL' --timeout 600s --lines 5000
amux capture work:build --lines 5000 > /tmp/buildlog
```

## Recipe: stream transcripts to disk

```bash
while amux exists sess:cc; do
  amux capture sess:cc --lines 200 > "/tmp/cc-$(date +%s).txt"
  sleep 5
done
```

---

## Gotchas (read once, avoid forever)

### 1. `send` is literal; `key` is named

- `amux send t "Enter"` sends 5 characters: E, n, t, e, r
- `amux key t Enter` sends the Enter keypress

These are different operations. Use `send` for the text of a prompt;
follow with `key Enter` to submit, or use `paste --submit`/`run`.

### 2. Bulk `send` of text may be treated as a "paste" by rich TUIs

Claude Code's `/plugin` picker has a search filter that routes
individual keystrokes to its search box — but a bulk `send-keys -l`
write looks like a paste to the TUI and goes somewhere else (often the
main ❯ input, or nowhere).

**Fix:** use `amux type <target> <text> --delay 80ms` for TUI fields
whose handlers only react to per-character events.

```bash
# WRONG — text silently goes to main input, not the picker
amux send sess:cc "search term"

# RIGHT — each char is an independent keystroke
amux type sess:cc "search term" --delay 80ms
```

### 3. Burst arrow keys get debounced

Several named keys in a single `amux key ... Down Down Down` call
arrive together — Claude's TUI debounces and registers only one or two.

**Fix:** use `--repeat` with `--delay`:

```bash
amux key sess:cc Down --repeat 5 --delay 80ms
```

### 4. `paste` strips `\e[200~` / `\e[201~` from your content

If your prompt contains those byte sequences (bracketed-paste markers),
amux silently strips them before handing to tmux. Without the strip,
the receiving TUI sees the end-sentinel mid-content, closes paste mode,
drops the rest of your prompt, and treats it as keystrokes — usually
silently. This was a real regression against Claude Code.

The strip is safe: those sequences have no legitimate in-content
meaning. If you need them (you don't), you'd use `amux send` instead.

### 5. Enter vs. Escape in Claude Code

Claude's TUI uses **double-tap Esc** to clear the input. A single Esc
usually dismisses overlays (and interrupts a streaming reply). Know
what layer you're in before sending Escape.

```bash
amux key sess:cc Escape Escape          # clear input
amux key sess:cc Escape                 # interrupt reply / dismiss overlay
```

BSpace erases one char at a time. Up recalls the previous submitted
prompt (which then doesn't always want to clear cleanly — easier to
kill the session if you need a reset).

### 6. `wait-idle` vs. `wait-for`

- `wait-idle` polls `capture-pane` and returns when output hasn't
  changed for `--quiet` duration. **Heuristic**. Fails when a TUI has
  constant background animation. Fails when there's a brief pause
  before the reply starts streaming (it'll false-positive).
- `wait-for` polls for a regex match. **Deterministic**. Use this
  whenever you know a marker the target will emit.

Rule of thumb: prefer `wait-for` for known state transitions, fall
back to `wait-idle` for "I don't know what to wait for, just settle".

For Claude specifically, `wait-for target '⏺'` after a submit is a
reliable "reply has started". Combine with `wait-idle` to also wait
for the reply to finish.

### 7. `run` adds a brief pre-stabilize `wait-idle`

Before sampling the pre-submit cursor position, `run` does a short
`wait-idle` (300ms quiet, 5s timeout) to make sure the pane isn't
mid-redraw. That means `run` has ≥300ms of latency floor. For
time-critical scripts, use `paste --submit` + `wait-for` directly.

### 8. Sending to a non-focused pane works fine

amux always uses `tmux send-keys -t <target>` which delivers regardless
of which pane the user is currently viewing in a tmux session. You
don't need to `select-pane` first.

### 9. `color` requires a window or pane target

Color is per-window (status-bar entry) or per-pane (pane border).
Session-wide color has no single tmux primitive.

```bash
amux color sess:cc green                 # OK — tints window status
amux color sess:cc.1 green               # OK — tints pane border
amux color sess green                    # ERROR — tells you why
```

### 10. Shell quoting of launch commands

`amux window sess -n w -- <cmd args...>` joins everything after `--`
back into one POSIX-shell string with proper single-quote quoting.
You can pass complex commands safely:

```bash
amux window sess -n dev -- \
  sh -c 'cd /tmp/app && source .envrc && exec npm run dev'
```

No need to double-quote or escape from your side.

---

## JSON & scripting

```bash
# Every pane in every session
amux list --json

# Just the Claude panes
amux list --json | jq '.[] | select(.window_name | startswith("cc"))'

# PIDs of everything in a session
amux list --json | jq -r '.[] | select(.session=="fleet") | .pid'

# Structured capture
amux capture sess:cc --json | jq -r .content
```

## How the plumbing works (if you care)

- `send` → `tmux send-keys -t TARGET -l -- TEXT` (literal mode, `--`
  guard so leading dashes don't confuse tmux).
- `key`  → `tmux send-keys -t TARGET -- KEY [KEY…]` (no `-l`, tmux
  interprets names). Optional `--repeat` loops with `--delay`.
- `paste` → `tmux load-buffer -b BUF -` (stdin) + `tmux paste-buffer -b
  BUF -t TARGET -p -d` (bracketed paste, delete buffer). `--submit`
  follows with `tmux send-keys Enter`. Content is pre-sanitized to
  strip `\e[200~` and `\e[201~`.
- `type` → one `send-keys -l --` per rune, with `--delay` between.
- `capture` → `tmux capture-pane -p -t TARGET` (+ `-S -N` for
  scrollback, `-e` for ANSI escapes).
- `wait-idle` → poll capture, remember last content + when it last
  changed; return when (now − lastChange) ≥ `--quiet`.
- `wait-for` → compile Go regex; poll capture; return on first match.
- `run` → stabilize (brief `wait-idle`) → record `history_size +
  cursor_y` → paste-buffer+Enter → `wait-idle` → capture from the
  remembered offset.
- `exists` → enumerate via `list-sessions`/`list-windows`/`list-panes`
  and exact-match (tmux's own `-t` does prefix matching).

## When to reach past amux and use raw tmux

- Attaching to a session interactively: `tmux attach -t sess`.
- Adjusting global tmux options or keybindings.
- Complex layouts (`select-layout`, `swap-pane`, `break-pane`…).

amux doesn't try to hide tmux — it just makes the agent-driving subset
reliable.
