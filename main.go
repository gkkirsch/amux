package main

import (
	"fmt"
	"os"
)

const usageText = `amux — a thin, reliable tmux CLI for driving agent panes.

Usage:
  amux <command> [flags] [args]

Lifecycle:
  new <session>                   Create a detached session.
  window <session> [-n name] [-- cmd ...]
                                  Create a new window (optionally running cmd).
  split <target> [-h|-v] [-- cmd ...]
                                  Split a pane (-v vertical, -h horizontal).
  kill <target>                   Kill a session, window, or pane.
  list [--json]                   List all sessions/windows/panes.

Input (the hard part — each primitive has one job):
  send <target> <text>            Literal text via 'send-keys -l' — 'Enter' stays as 5 chars.
  key  <target> <key> [<key>...]  Named keys: Enter, Escape, Tab, BSpace, C-c, M-x,
                                  Up, Down, Left, Right, PageUp, PageDown, Home, End ...
  paste <target> [--submit]       Read stdin → load-buffer → paste-buffer (bracketed paste).
                                  Reliable path for multi-line input into TUIs like Claude Code.
                                  --submit appends Enter after the paste.
  type <target> <text> [--delay 30ms]
                                  Character-by-character typing with a delay between chars.

Observation:
  capture <target> [--lines N] [--json]
                                  Capture visible (or last N) pane content.
  wait-idle <target> [--quiet 800ms] [--timeout 60s]
                                  Block until capture-pane output stops changing.

Target format follows tmux: session[:window[.pane]]

Examples:
  amux new agent1
  amux window agent1 -n claude -- claude
  echo "summarize this file" | amux paste agent1:claude --submit
  amux wait-idle agent1:claude
  amux capture agent1:claude --lines 200
`

func usage() { fmt.Fprint(os.Stderr, usageText) }

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

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
