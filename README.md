# amux

A thin, reliable tmux CLI for driving agent panes — especially Claude Code.

`tmux` is already the right tool for running agents in isolated panes. But
the standard `tmux send-keys` interface is fiddly: flags conflict with
content, `Enter` can be a key *or* five literal characters, bracketed-paste
markers in user content silently break submissions. `amux` is a tight Go
CLI that exposes each useful tmux primitive as its own small command with
one job — plus the observation tools (`capture`, `wait-idle`) you need to
programmatically talk to an agent.

## Install

```
git clone https://github.com/gkkirsch/amux.git ~/dev/amux
cd ~/dev/amux
make install      # builds + copies to ~/.local/bin/amux
```

Requires tmux 3.x and Go 1.21+ to build.

**See [AGENTS.md](./AGENTS.md) for the cookbook** — recipes, gotchas, and
the pragmatic guide for driving Claude Code (and other TUIs) from a
script or an agent.

Every command has detailed help: `amux <cmd> -h`.

## Commands

### Lifecycle
| Command | What it does |
|---|---|
| `amux new <session>` | Create a detached session |
| `amux window <session> [-n name] [-- cmd ...]` | New window, optionally running a command |
| `amux split <target> [-h\|-v] [-- cmd ...]` | Split a pane (`-v` top/bottom default, `-h` left/right) |
| `amux rename <target> <new-name>` | Rename session/window (pane: set title) |
| `amux color <target> <color>` | Tint window-status or pane border |
| `amux kill <target>` | Kill a session / window / pane (dispatched by target shape) |
| `amux list [--json]` | List all sessions/windows/panes |
| `amux exists <target>` | Silent exact-match existence check (exit 0/1) |
| `amux log <target> <file\|off>` | Pipe everything the pane prints to `<file>` (append), or `off` to detach |

### Input
Each input primitive has one job — no overloaded "send" that sometimes
submits and sometimes doesn't.

| Command | What it does |
|---|---|
| `amux send <target> <text>` | Literal text (`send-keys -l --`). `Enter` in your string stays as 5 chars. |
| `amux key <target> <key> [<key>...]` | Named keys (`Enter`, `Escape`, `Tab`, `BSpace`, `Up`, `Down`, `Left`, `Right`, `PageUp`, `PageDown`, `Home`, `End`, `C-c`, `M-x`, …) |
| `amux paste <target> [--submit] [--bracketed=false]` | Read stdin → `load-buffer` → `paste-buffer -p`. The reliable path for multi-line input into TUIs. `--submit` appends `Enter`. |
| `amux type <target> <text> [--delay 20ms]` | Character-by-character, for apps that choke on fast input |

**Paste sanitization.** `paste` strips embedded `\e[200~`/`\e[201~` byte
sequences from stdin before delivery. Without this, a user payload
containing the bracketed-paste-end sentinel prematurely closes paste mode
on the receiving TUI and silently truncates the prompt — observed and
fixed against Claude Code.

### Observation
| Command | What it does |
|---|---|
| `amux capture <target> [--lines N] [--json] [--escapes]` | `capture-pane -p`. `--lines` pulls scrollback; `--escapes` preserves ANSI color/highlight |
| `amux wait-idle <target> [--quiet 800ms] [--timeout 60s] [--interval 200ms]` | Heuristic: block until capture output hasn't changed for `--quiet`. Non-zero exit on timeout. |
| `amux wait-for <target> <pattern> [--timeout 60s] [--interval 200ms] [--lines 0]` | Deterministic: block until a Go regex matches. |

### Agent one-shot
| Command | What it does |
|---|---|
| `amux run <target> [--quiet 2s] [--timeout 120s] < prompt` | Read stdin → paste+submit → wait-idle → emit the NEW content added since submit. The "ask and read reply" primitive. |

## The hard part: reliable input into Claude Code

Three decisions matter:

1. **Never conflate text with keypresses.** `send` is always literal
   (`send-keys -l --`). `key` is always named. `paste` is always a buffer
   paste. You pick which mechanism; amux never guesses.
2. **Use bracketed paste for multi-line.** `paste` goes through
   `load-buffer`/`paste-buffer -p`, which Claude Code treats as a single
   atomic paste — embedded newlines don't trigger premature submission.
3. **Strip paste sentinels from user content.** Otherwise a malicious (or
   just unlucky) payload containing `\e[201~` silently truncates the
   delivered prompt. We learned this the hard way and it now has a
   regression test.

## Proof

Everything is tested end-to-end against real tmux — no mocks anywhere. The
suite builds the binary and drives it as a subprocess against live `tmux`
and a real `claude` TUI.

```
$ go test ./... -v -count=1
--- PASS: TestNewAndList                           (0.28s)
--- PASS: TestListJSON                             (0.03s)
--- PASS: TestListEmpty_NoServer_ReturnsEmpty      (0.01s)
--- PASS: TestWindow                               (0.04s)
--- PASS: TestSplit                                (0.05s)
--- PASS: TestKillSession                          (0.03s)
--- PASS: TestSendIsLiteral                        (0.33s)
--- PASS: TestKeyMultiple                          (0.28s)
--- PASS: TestPasteMultiline                       (0.26s)
--- PASS: TestPasteSubmit                          (0.26s)
--- PASS: TestType                                 (0.33s)
--- PASS: TestCaptureShowsOutput                   (0.28s)
--- PASS: TestCaptureJSON                          (0.03s)
--- PASS: TestWaitIdle                             (1.67s)
--- PASS: TestWaitIdleTimeout                      (1.56s)
--- PASS: TestUsageOnUnknownCommand                (0.00s)
--- PASS: TestPasteStripsBracketedPasteSentinels   (0.26s)
--- PASS: TestArrowKeys                            (0.33s)
--- PASS: TestSendRequiresTwoArgs                  (0.00s)
--- PASS: TestClaudeIntegration                    (8.53s)
PASS
ok  	github.com/gkkirsch/amux	15.802s
```

`TestClaudeIntegration` spawns a real Claude Code TUI, pastes a prompt
asking for a unique marker, waits for the response to settle, and asserts
the marker in the captured reply.

### Navigating rich TUIs (`/plugin`, pickers, menus)

Two subtleties observed while driving Claude Code's `/plugin` picker:

1. **Arrow-key repeats need spacing.** Several `Down` keys in a single
   `amux key ... Down Down Down` call arrive as a burst and the TUI only
   registers one or two. Use `--repeat N --delay 80ms` to space them:

   ```
   amux key demo:cc Down --repeat 5 --delay 80ms
   ```

2. **Search-as-you-type needs `type`, not `send`.** A single bulk
   `send-keys -l` write gets treated like a paste by the TUI's heuristic
   and is routed to the wrong target (or dropped). Character-by-character
   `amux type ... --delay 80ms` reaches the search input:

   ```
   amux type demo:cc "superch" --delay 80ms
   ```

`send` is still the right tool for filling the main prompt with a short
snippet. `paste` is still the right tool for multi-line content into the
main prompt. `type` is specifically the escape hatch for "this TUI field
only accepts keystrokes, not pastes".

### Manually verified against Claude Code

Beyond the automated suite, the following scenarios were driven by hand:

- Simple prompt via `paste --submit` → marker response
- `send` + `key Enter` submission (literal text, separate submit)
- `type --delay` char-by-char submission
- `wait-idle` on a real Claude response (returns ~2s after stream ends)
- `key Up` / `key Down` navigation in the slash-command menu
- `key Escape Escape` to dismiss overlays (double-tap is Claude's own shortcut)
- Mid-stream interrupt via `key Escape` (Claude shows "Interrupted · …")
- External kill of the Claude process — tmux cleanly collapses the window
- 3 parallel Claudes in one session, independent prompts, zero crosstalk
- Back-and-forth landing-page build across two turns (4.1kB HTML written,
  then a follow-up edit added a fourth feature card)
- Stress paste: a 101-line payload with unicode, emoji, RTL, code blocks,
  shell metachars, trailing whitespace, consecutive newlines — delivered
  as one prompt
- 1MB paste into `cat` — no truncation
- Injection: payload containing `\e[201~` mid-content. Without the
  sanitizer, Claude silently drops the tail. With it, the full prompt
  arrives.
- 5 concurrent amux invocations from parallel shells targeting the same
  pane — all 5 chunks delivered
- Clean `/exit` from Claude — window tears down
- `amux kill` on nonexistent session/window/pane returns the tmux error
  verbatim and exits 1
- `/plugin` picker: opened, filtered with `type`, navigated with
  `key --repeat`, toggled selection with `Space`, drilled in with `Enter`,
  backed out with `Escape`, cycled top-tabs with `Tab`

## Example: pick up a fresh Claude, make it answer, read the reply

```
amux new demo
amux window demo -n cc -- claude --dangerously-skip-permissions
amux wait-for demo:cc '❯' --timeout 45s      # TUI is ready

echo "what is 17*23? reply with just the number" | amux run demo:cc
# → ⏺ 391

amux kill demo
```

The underlying primitives (`paste --submit` + `wait-idle` + `capture`)
are also available if you want direct control:

```
echo "what is 17*23?" | amux paste demo:cc --submit
amux wait-idle demo:cc --quiet 2s --timeout 30s
amux capture demo:cc | grep '⏺'
```

## Target format

`session` or `session:window` or `session:window.pane`. Windows resolve by
index or name. Whatever tmux accepts, amux accepts.
