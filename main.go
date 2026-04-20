package main

import (
	"fmt"
	"os"
)

const usageText = `amux — a thin, reliable tmux CLI for driving agent panes.

Usage:
  amux <command> [flags] [args]
  amux <command> -h               Detailed help for one command.

Lifecycle:
  new     <session>                             Create a detached session.
  window  <session> [-n name] [-- cmd ...]      Create a new window.
  split   <target>  [-h|-v]   [-- cmd ...]      Split a pane (-v default, -h left/right).
  rename  <target>  <new-name>                  Rename session/window (pane: set title).
  color   <target>  <color>                     Tint window-status or pane border.
  kill    <target>                              Kill session/window/pane.
  list    [--json]                              List everything.
  exists  <target>                              Silent existence check (exit 0/1).

Input (each primitive does ONE thing):
  send  <target> <text>                         Literal text (-l --). 'Enter' stays as 5 chars.
  key   <target> <key> [<key>...] [--repeat N] [--delay 80ms]
                                                Named keys. Use --repeat for reliable navigation.
  paste <target> [--submit] [--bracketed=false]
                                                Read stdin → bracketed paste. Strips \e[201~
                                                sentinels from content. --submit appends Enter.
  type  <target> <text> [--delay 20ms]          Char-by-char typing. Use this for TUI search
                                                fields that treat bulk sends as pastes.

Observation:
  capture   <target> [--lines N] [--json] [--escapes]
                                                Capture pane content. --escapes keeps ANSI colors.
  wait-idle <target> [--quiet 800ms] [--timeout 60s] [--interval 200ms]
                                                Heuristic: block until output stops changing.
  wait-for  <target> <pattern> [--timeout 60s] [--interval 200ms] [--lines 0]
                                                Deterministic: block until regex matches capture.

Agent one-shot:
  run <target> [--quiet 2s] [--timeout 120s] [--lines 2000] < prompt
                                                Paste stdin → submit → wait-idle → emit the
                                                NEW content added since submit. The "ask and
                                                read reply" primitive.

Target format follows tmux: session[:window[.pane]]

Exit codes:
  0   success
  1   runtime error (tmux error, timeout, assertion miss, etc.)
  2   usage error (bad args or unknown command)

Examples:
  amux new agent1
  amux window agent1 -n cc -- claude --dangerously-skip-permissions
  amux wait-for agent1:cc '❯'                  # TUI is ready
  echo "say hi" | amux run agent1:cc            # ask & get reply in one call
  amux color agent1:cc green                    # tag it green
  amux list --json | jq
`

func usage() { fmt.Fprint(os.Stderr, usageText) }

// commandHelp returns a long-form help string for each subcommand. Shown
// when the user runs `amux <cmd> -h` or `amux help <cmd>`.
var commandHelp = map[string]string{
	"new": `amux new <session>

Create a new detached tmux session. It will have a single default
window. Errors if the session name already exists.

Example:
  amux new agent1`,

	"window": `amux window <session> [-n name] [-- cmd ...]

Create a new window inside <session>. If -n is given, the window gets
that name (otherwise tmux names it after the running command). Anything
after '--' is run as the initial shell command in the new window; it's
reassembled with POSIX shell quoting, so 'sh -c "read LINE"' survives.

Examples:
  amux window agent1 -n cc -- claude --dangerously-skip-permissions
  amux window agent1 -n work                       # opens $SHELL
  amux window agent1 -- python3 -i                 # unnamed, running python`,

	"split": `amux split <target> [-h|-v] [-- cmd ...]

Split the target pane. -v (default) creates a top/bottom split, -h
creates a left/right split. The new pane does NOT take focus
(split-window -d). Command after '--' runs in the new pane.

Examples:
  amux split agent1:cc                             # vertical, default shell
  amux split agent1:cc -h -- htop
  amux split agent1:cc.0 -- tail -f /var/log/foo`,

	"kill": `amux kill <target>

Kill a session, window, or pane. The action is chosen by the target
shape: "s" → session, "s:w" → window, "s:w.p" → pane.

Examples:
  amux kill agent1                                 # kill whole session
  amux kill agent1:cc                              # kill one window
  amux kill agent1:cc.1                            # kill one pane`,

	"list": `amux list [--json]

List every session/window/pane across every running tmux server the
user can see. Returns cleanly (empty list) when no server is running.

Columns (non-JSON): active-marker, target, (window-name), pid, command.
JSON: array of {session, window, window_name, pane, pid, active, command}.

Examples:
  amux list
  amux list --json | jq '.[] | select(.session=="agent1")'`,

	"rename": `amux rename <target> <new-name>

Rename a session or window. For a pane target, sets the pane title
(tmux's select-pane -T) — the title is visible in pane borders if
'pane-border-status' is enabled (amux color on a pane enables it).

Examples:
  amux rename agent1 fleet                         # session
  amux rename agent1:cc claude-1                   # window
  amux rename agent1:cc.1 "logs"                   # pane title`,

	"color": `amux color <target> <color>

Tint a window's status-bar entry (window target) or a pane's border
(pane target). Session targets are rejected. <color> accepts tmux's
color names (red, green, yellow, blue, magenta, cyan, white, black,
and 'colour0'-'colour255') or 6-digit hex like #80ff00.

Useful for giving parallel agent panes distinct visual tags when a
human is watching.

Examples:
  amux color agent1:cc green
  amux color agent1:cc.1 "#80ff00"                 # chartreuse`,

	"send": `amux send <target> <text>

Send LITERAL text to the target. Uses 'tmux send-keys -l --', so
tokens like "Enter" inside <text> are sent as 5 characters, not as a
keypress. Leading dashes in <text> are handled correctly.

'send' does NOT submit. If you want to submit the text, follow up with
'amux key <target> Enter'. If your text is multi-line or large, prefer
'paste' (which uses bracketed paste into TUIs).

Examples:
  amux send agent1:cc "ls -la"
  amux send agent1:cc "--verbose --no-color"       # leading dashes OK`,

	"key": `amux key <target> <key> [<key>...] [--repeat N] [--delay 80ms]

Send named keys (not literal text). Keys use tmux's own key names:
  Enter Escape Tab BSpace Space
  Up Down Left Right PageUp PageDown Home End
  C-a C-c C-d C-l C-u ...                          (Control-modified)
  M-a M-x ...                                      (Meta-modified)
  S-Tab                                            (Shift-modified)

For holding a navigation key (e.g. scrolling a picker), use --repeat
with a small --delay — many TUIs debounce and drop keys that arrive
in a single burst.

Examples:
  amux key agent1:cc Enter
  amux key agent1:cc Escape Escape                 # Claude's "clear input"
  amux key agent1:cc Down --repeat 5 --delay 80ms  # scroll a picker`,

	"paste": `amux paste <target> [--submit] [--bracketed=false] < stdin

Paste stdin into the target pane. Uses tmux's 'load-buffer | paste-
buffer -p', which is bracketed paste — the receiving TUI treats it
as a single atomic paste, so embedded newlines don't submit early.

SECURITY: paste strips embedded \e[200~ and \e[201~ byte sequences
from stdin first. Without this, a payload containing the end-sentinel
prematurely closes paste mode on the receiving TUI and silently
truncates the delivered prompt (observed against Claude Code's TUI).

Flags:
  --submit            Append Enter after the paste (submit in TUIs).
  --bracketed=false   Don't emit bracketed-paste markers. Use for
                      non-TUI consumers (cat, shell) that would echo
                      the markers as literal bytes.

Examples:
  echo "what is 17*23?" | amux paste agent1:cc --submit
  cat big-prompt.md | amux paste agent1:cc --submit
  cat file.txt | amux paste agent1:shell --bracketed=false`,

	"type": `amux type <target> <text> [--delay 20ms]

Send text character-by-character with a delay between each char. Use
this for TUI fields that treat a bulk 'send' as a paste and route it
elsewhere — e.g. Claude Code's /plugin picker search filter only
accepts chars that arrive as individual keystrokes.

Examples:
  amux type agent1:cc "superch" --delay 80ms       # filter /plugin picker`,

	"capture": `amux capture <target> [--lines N] [--json] [--escapes]

Print the contents of the target pane. By default: visible screen
only, escape codes stripped.

Flags:
  --lines N    Include the last N history (scrollback) lines.
  --json       Emit {"target": "...", "content": "..."} as JSON.
  --escapes    Preserve ANSI escape codes (color, cursor, highlight).
               Useful for checking which menu item is selected.

Examples:
  amux capture agent1:cc
  amux capture agent1:cc --lines 500
  amux capture agent1:cc --json | jq -r .content
  amux capture agent1:cc --escapes | grep $'\e\\[7m'  # find reverse video`,

	"wait-idle": `amux wait-idle <target> [--quiet 800ms] [--timeout 60s] [--interval 200ms]

Heuristic wait: block until capture-pane output hasn't changed for
<quiet> duration, or fail with exit 1 after <timeout>.

Use this when you don't know what to wait for. Use 'wait-for' instead
when you DO know a marker — wait-idle can false-positive in brief
silences, and can false-negative on TUIs with constant animation.

Examples:
  amux wait-idle agent1:cc
  amux wait-idle agent1:cc --quiet 2s --timeout 60s`,

	"wait-for": `amux wait-for <target> <pattern> [--timeout 60s] [--interval 200ms] [--lines 0]

Deterministic wait: block until a Go regex <pattern> matches the
capture-pane output of <target>. Exit 0 on first match, exit 1 on
timeout. Nothing printed.

Flags:
  --lines N    Include last N scrollback lines in each search (helps
               when the marker has scrolled off the visible screen).

Examples:
  amux wait-for agent1:cc '❯'                      # claude prompt visible
  amux wait-for agent1:cc 'DONE[0-9]+' --timeout 120s
  amux wait-for agent1:cc 'exited' --lines 500`,

	"exists": `amux exists <target>

Silent existence check. Exit 0 if the target (session/window/pane)
exists, exit 1 otherwise. Nothing printed.

Useful for idempotent setup:
  amux exists agent1 || amux new agent1
  amux exists agent1:cc || amux window agent1 -n cc -- claude ...`,

	"run": `amux run <target> [--quiet 2s] [--timeout 120s] [--lines 2000] [--interval 300ms] [--bracketed=false] < prompt

The agent one-shot. Reads stdin as a prompt, pastes it with submit,
waits for the pane to go idle, and emits ONLY the content added since
the submit. This is the primitive you want for "ask Claude X and read
the reply" without a bash pipeline of paste+wait-idle+capture.

If the pane's scrollback has rolled past our pre-submit snapshot the
fallback is to emit the full post-submit capture.

Examples:
  echo "what is 17*23?"       | amux run agent1:cc
  cat task.md                  | amux run agent1:cc --timeout 300s
  printf 'reply ONLY: %s\n' $X | amux run agent1:cc | grep "$X"`,
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	// `amux help <cmd>` and `amux <cmd> -h|--help` both print long help.
	if cmd == "help" {
		if len(args) == 0 {
			fmt.Print(usageText)
			return
		}
		if h, ok := commandHelp[args[0]]; ok {
			fmt.Println(h)
			return
		}
		fmt.Fprintf(os.Stderr, "amux: no help for %q\n", args[0])
		os.Exit(2)
	}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		if h, ok := commandHelp[cmd]; ok {
			fmt.Println(h)
			return
		}
	}

	var err error
	switch cmd {
	case "new":
		err = cmdNew(args)
	case "window":
		err = cmdWindow(args)
	case "split":
		err = cmdSplit(args)
	case "kill":
		err = cmdKill(args)
	case "list":
		err = cmdList(args)
	case "send":
		err = cmdSend(args)
	case "key":
		err = cmdKey(args)
	case "paste":
		err = cmdPaste(args)
	case "type":
		err = cmdType(args)
	case "capture":
		err = cmdCapture(args)
	case "wait-idle":
		err = cmdWaitIdle(args)
	case "wait-for":
		err = cmdWaitFor(args)
	case "exists":
		err = cmdExists(args)
	case "run":
		err = cmdRun(args)
	case "rename":
		err = cmdRename(args)
	case "color":
		err = cmdColor(args)
	case "-h", "--help", "help":
		fmt.Print(usageText)
		return
	default:
		fmt.Fprintf(os.Stderr, "amux: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "amux:", err)
		os.Exit(1)
	}
}
