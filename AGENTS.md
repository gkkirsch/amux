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
|             | `log`       | pipe everything a pane prints to a file (append) |
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

## Marker-echo gotcha (any interactive target)

If your prompt contains the completion marker you plan to wait for,
the marker is already on the pane the moment the prompt echoes —
`amux wait-for TARGET MARKER` matches that echo, not the real
completion.

Three ways out, pick the one that fits:

- **Unique marker** — have the sub-agent emit a completion string
  that does NOT appear in your prompt. Describe it abstractly rather
  than literally: "when complete, print the letters D, O, N, E".
- **`wait-for --new-only`** — snapshots the cursor on invocation,
  only matches content added afterwards. Stand-alone waits.
- **`run --wait-for REGEX`** — anchors at pre-submit cursor, so the
  prompt echo is inside the search window (still susceptible to this
  gotcha if the marker is in the prompt — use a unique marker).
- **File signal (best)** — sub-agent writes `/tmp/done-X`, orchestrator
  waits for the file. Completely decouples you from TUI text.

## Recipe: spin up an interactive agent and ask one question

```bash
amux new workspace
amux window workspace -n agent -- <your-interactive-command>

# Wait for the TUI to draw a stable prompt/ready state.
amux wait-for workspace:agent '<ready-marker>' --timeout 45s

# Ask, wait for idle, emit the delta.
echo "<prompt>" | amux run workspace:agent

amux kill workspace
```

Choosing `<ready-marker>` depends on the TUI. Make sure it's something
that appears ONLY in the ready state — not also in any startup dialog,
welcome screen, or menu the TUI draws along the way.

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
amux window sess -n agent -- <interactive-cmd>
amux wait-for sess:agent '<ready-marker>' --timeout 45s

for prompt in "...turn 1..." "...turn 2..." "...turn 3..."; do
  echo "$prompt" | amux run sess:agent --timeout 120s
  echo "--- turn done ---"
done

amux kill sess
```

## Recipe: parallel fleet of agents

```bash
amux new fleet
for name in researcher writer editor; do
  amux window fleet -n "$name" -- <interactive-cmd>
done
for name in researcher writer editor; do
  amux wait-for "fleet:$name" '<ready-marker>' --timeout 45s
done

# Visual tags for a human watching (colors show in tmux status bar).
amux color fleet:researcher cyan
amux color fleet:writer     green
amux color fleet:editor     magenta

# Persistent per-agent transcripts.
for w in researcher writer editor; do
  amux log "fleet:$w" "/tmp/fleet-$w.log"
done

echo "...task for researcher..." | amux run fleet:researcher &
echo "...task for writer..."     | amux run fleet:writer     &
echo "...task for editor..."     | amux run fleet:editor     &
wait
```

## Recipe: idempotent setup (safe to re-run)

```bash
amux exists projectA            || amux new projectA
amux exists projectA:agent      || amux window projectA -n agent -- <cmd>
amux wait-for projectA:agent '<ready-marker>' --timeout 45s
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

Some TUIs have input fields that only accept per-character keystrokes
(search-as-you-type filters, inline pickers). A bulk `send-keys -l`
write arrives as one chunk and the TUI's paste-detection heuristic
routes it somewhere else (often the main input area, or nowhere).

**Fix:** use `amux type <target> <text> --delay 80ms` for fields whose
handlers only react to per-character events.

```bash
# WRONG — text silently goes to main input, not the picker
amux send sess:cc "search term"

# RIGHT — each char is an independent keystroke
amux type sess:cc "search term" --delay 80ms
```

### 3. Burst arrow keys get debounced

Several named keys in a single `amux key ... Down Down Down` call
arrive together — many TUIs debounce and only register one or two
of the presses.

**Fix:** use `--repeat` with `--delay`:

```bash
amux key sess:cc Down --repeat 5 --delay 80ms
```

### 4. `paste` strips `\e[200~` / `\e[201~` from your content

If your prompt contains those byte sequences (bracketed-paste
markers), amux silently strips them before handing to tmux. Without
the strip, the receiving TUI sees the end-sentinel mid-content,
closes paste mode, drops the rest of your prompt, and treats it as
keystrokes — usually silently.

The strip is safe: those sequences have no legitimate in-content
meaning. If you truly need to deliver them raw, use `amux send`
instead.

### 5. `wait-idle` vs. `wait-for`

- `wait-idle` polls `capture-pane` and returns when output hasn't
  changed for `--quiet` duration. **Heuristic**. Fails when a TUI has
  constant background animation. Fails when there's a brief pause
  before the reply starts streaming (it'll false-positive).
- `wait-for` polls for a regex match. **Deterministic**. Use this
  whenever you know a marker the target will emit.

Rule of thumb: prefer `wait-for` for known state transitions, fall
back to `wait-idle` for "I don't know what to wait for, just settle".
`wait-for --new-only` ignores any existing match — use it when your
waiting condition might accidentally match pre-existing content.

### 6. `run` adds a brief pre-stabilize `wait-idle`

Before sampling the pre-submit cursor position, `run` does a short
`wait-idle` (300ms quiet, 5s timeout) to make sure the pane isn't
mid-redraw. That means `run` has ≥300ms of latency floor. For
time-critical scripts, use `paste --submit` + `wait-for` directly.

### 7. Sending to a non-focused pane works fine

amux always uses `tmux send-keys -t <target>` which delivers regardless
of which pane the user is currently viewing in a tmux session. You
don't need to `select-pane` first.

### 8. `color` requires a window or pane target

Color is per-window (status-bar entry) or per-pane (pane border).
Session-wide color has no single tmux primitive.

```bash
amux color sess:cc green                 # OK — tints window status
amux color sess:cc.1 green               # OK — tints pane border
amux color sess green                    # ERROR — tells you why
```

### 9. Shell quoting of launch commands

`amux window sess -n w -- <cmd args...>` joins everything after `--`
back into one POSIX-shell string with proper single-quote quoting.
You can pass complex commands safely:

```bash
amux window sess -n dev -- \
  sh -c 'cd /tmp/app && source .envrc && exec npm run dev'
```

No need to double-quote or escape from your side.

---

### 10. Strict target matching (tmux does prefix matching!)

tmux's own `-t foo` matches `foo-bar` — a silent misroute hazard after
renames or when multiple sessions share a prefix. amux does EXACT
matching by default on every target-consuming command, via
enumeration. If you really want tmux's prefix behavior, set
`AMUX_LOOSE_TARGETS=1` in the environment.

## JSON & scripting

```bash
# Every pane in every session
amux list --json

# Filter by window name
amux list --json | jq '.[] | select(.window_name | startswith("agent-"))'

# PIDs of everything in a session
amux list --json | jq -r '.[] | select(.session=="fleet") | .pid'

# Structured capture
amux capture sess:agent --json | jq -r .content
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
- `log`  → `tmux pipe-pane -t TARGET 'cat >> FILE'`; `off` detaches.
- All target-consuming commands gate on `exists` first (strict matching).

## When to reach past amux and use raw tmux

- Attaching to a session interactively: `tmux attach -t sess`.
- Adjusting global tmux options or keybindings.
- Complex layouts (`select-layout`, `swap-pane`, `break-pane`…).

amux doesn't try to hide tmux — it just makes the agent-driving subset
reliable.
