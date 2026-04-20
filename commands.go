package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// sanitizeBracketedPaste removes ESC[200~ and ESC[201~ byte sequences
// from content, so user data containing them can't prematurely close
// (or confusingly nest) bracketed-paste mode on the receiving TUI.
// See paste-buffer -p in tmux(1) and xterm's bracketed paste mode.
func sanitizeBracketedPaste(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\x1b[200~"), nil)
	b = bytes.ReplaceAll(b, []byte("\x1b[201~"), nil)
	return b
}

// shellQuote produces a POSIX-shell-safe single-quoted form of s. We need
// this because `tmux new-window [cmd]` and `tmux split-window [cmd]` take a
// SINGLE shell string (tmux runs it via /bin/sh -c), so when callers pass
// multiple argv tokens like `-- sh -c 'read LINE'` we must reassemble them
// with quoting that survives one layer of shell parsing.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = shellQuote(a)
	}
	return strings.Join(out, " ")
}

// --- lifecycle -------------------------------------------------------------

func cmdNew(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: amux new <session>")
	}
	_, err := tmux("new-session", "-d", "-s", args[0])
	return err
}

func cmdWindow(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: amux window <session> [-n name] [-- cmd ...]")
	}
	session := args[0]
	fs := flag.NewFlagSet("window", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("n", "", "window name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tmuxArgs := []string{"new-window", "-t", session}
	if *name != "" {
		tmuxArgs = append(tmuxArgs, "-n", *name)
	}
	if rem := fs.Args(); len(rem) > 0 {
		// tmux new-window takes a single shell command string as a positional.
		tmuxArgs = append(tmuxArgs, shellJoin(rem))
	}
	_, err := tmux(tmuxArgs...)
	return err
}

func cmdSplit(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: amux split <target> [-h|-v] [-- cmd ...]")
	}
	target := args[0]
	fs := flag.NewFlagSet("split", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	horiz := fs.Bool("h", false, "horizontal split (left/right)")
	_ = fs.Bool("v", false, "vertical split (top/bottom; default)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tmuxArgs := []string{"split-window", "-d", "-t", target}
	if *horiz {
		tmuxArgs = append(tmuxArgs, "-h")
	}
	if rem := fs.Args(); len(rem) > 0 {
		tmuxArgs = append(tmuxArgs, shellJoin(rem))
	}
	_, err := tmux(tmuxArgs...)
	return err
}

func cmdKill(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: amux kill <target>")
	}
	target := args[0]
	// Dispatch by target shape: "sess" → session, "sess:win" → window,
	// "sess:win.pane" → pane. tmux itself would also accept the plain
	// target for kill-pane, but being explicit gives clearer errors.
	switch {
	case strings.Contains(target, "."):
		_, err := tmux("kill-pane", "-t", target)
		return err
	case strings.Contains(target, ":"):
		_, err := tmux("kill-window", "-t", target)
		return err
	default:
		_, err := tmux("kill-session", "-t", target)
		return err
	}
}

type paneInfo struct {
	Session string `json:"session"`
	Window  int    `json:"window"`
	WinName string `json:"window_name"`
	Pane    int    `json:"pane"`
	PID     int    `json:"pid"`
	Active  bool   `json:"active"`
	Command string `json:"command"`
}

func (p paneInfo) Target() string {
	return fmt.Sprintf("%s:%d.%d", p.Session, p.Window, p.Pane)
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	format := "#{session_name}\t#{window_index}\t#{window_name}\t#{pane_index}\t#{pane_pid}\t#{pane_active}\t#{pane_current_command}"
	out, err := tmux("list-panes", "-a", "-F", format)
	if err != nil {
		if !isNoServer(err) {
			return err
		}
		out = ""
	}
	var panes []paneInfo
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			continue
		}
		wi, _ := strconv.Atoi(parts[1])
		pi, _ := strconv.Atoi(parts[3])
		pid, _ := strconv.Atoi(parts[4])
		panes = append(panes, paneInfo{
			Session: parts[0], Window: wi, WinName: parts[2],
			Pane: pi, PID: pid, Active: parts[5] == "1", Command: parts[6],
		})
	}
	if *asJSON {
		if panes == nil {
			panes = []paneInfo{}
		}
		return json.NewEncoder(os.Stdout).Encode(panes)
	}
	if len(panes) == 0 {
		fmt.Println("(no sessions)")
		return nil
	}
	for _, p := range panes {
		marker := " "
		if p.Active {
			marker = "*"
		}
		fmt.Printf("%s %-30s  pid=%-7d  %s\n", marker, p.Target()+"  ("+p.WinName+")", p.PID, p.Command)
	}
	return nil
}

// --- input -----------------------------------------------------------------

func cmdSend(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: amux send <target> <text>  (literal — 'Enter' stays as 5 chars)")
	}
	target, text := args[0], args[1]
	// -l = literal: tmux won't interpret tokens like "Enter" as key names.
	// The trailing "--" tells tmux "no more flags" — without it, user text
	// beginning with "-" (e.g. "--verbose") fails tmux's flag parser.
	_, err := tmux("send-keys", "-t", target, "-l", "--", text)
	return err
}

func cmdKey(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: amux key <target> <key> [<key>...] [--repeat N] [--delay 80ms]")
	}
	target := args[0]
	// Separate positional keys from flags so --repeat / --delay can appear
	// anywhere after the target (e.g. `amux key t Down --repeat 5`).
	var keys []string
	flagArgs := []string{}
	for i := 1; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			flagArgs = append(flagArgs, args[i:]...)
			break
		}
		keys = append(keys, a)
	}
	fs := flag.NewFlagSet("key", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repeat := fs.Int("repeat", 1, "send the key sequence N times")
	delay := fs.Duration("delay", 80*time.Millisecond, "delay between repeats / between keys when repeating")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("key: at least one key required")
	}
	if *repeat < 1 {
		return fmt.Errorf("key: --repeat must be >= 1")
	}
	// No -l: tokens like "Enter" are interpreted as key names. "--" guards
	// against keys that start with a dash.
	base := append([]string{"send-keys", "-t", target, "--"}, keys...)
	for i := 0; i < *repeat; i++ {
		if i > 0 {
			time.Sleep(*delay)
		}
		if _, err := tmux(base...); err != nil {
			return err
		}
	}
	return nil
}

func cmdPaste(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: amux paste <target> [--submit]  (reads content from stdin)")
	}
	target := args[0]
	fs := flag.NewFlagSet("paste", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	submit := fs.Bool("submit", false, "send Enter after paste")
	bracketed := fs.Bool("bracketed", true, "use bracketed paste (-p)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("paste: stdin was empty (did you pipe content in?)")
	}
	// Strip bracketed-paste sentinels from user content. If present, tmux
	// or the receiving TUI would interpret the embedded \e[201~ as "end
	// of paste", drop the remainder of our buffer, and treat it as stray
	// keypresses. We observed this silently breaking submits against
	// Claude's TUI. Stripping both markers is the safe default.
	data = sanitizeBracketedPaste(data)
	buf := fmt.Sprintf("amux-%d-%d", os.Getpid(), time.Now().UnixNano())
	if _, err := tmuxStdin(data, "load-buffer", "-b", buf, "-"); err != nil {
		return err
	}
	pasteArgs := []string{"paste-buffer", "-b", buf, "-t", target, "-d"}
	if *bracketed {
		pasteArgs = append(pasteArgs, "-p")
	}
	if _, err := tmux(pasteArgs...); err != nil {
		return err
	}
	if *submit {
		if _, err := tmux("send-keys", "-t", target, "Enter"); err != nil {
			return err
		}
	}
	return nil
}

func cmdType(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: amux type <target> <text> [--delay 20ms]")
	}
	target, text := args[0], args[1]
	fs := flag.NewFlagSet("type", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	delay := fs.Duration("delay", 20*time.Millisecond, "delay between chars")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	for _, r := range text {
		if _, err := tmux("send-keys", "-t", target, "-l", "--", string(r)); err != nil {
			return err
		}
		time.Sleep(*delay)
	}
	return nil
}

// --- observation -----------------------------------------------------------

func cmdCapture(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: amux capture <target> [--lines N] [--json]")
	}
	target := args[0]
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	lines := fs.Int("lines", 0, "include last N history lines (0 = visible screen only)")
	asJSON := fs.Bool("json", false, "emit JSON")
	escapes := fs.Bool("escapes", false, "preserve ANSI escape sequences (color, highlight)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tmuxArgs := []string{"capture-pane", "-p", "-t", target}
	if *escapes {
		tmuxArgs = append(tmuxArgs, "-e")
	}
	if *lines > 0 {
		// -S -<N> starts the capture <N> lines up from the bottom of history.
		tmuxArgs = append(tmuxArgs, "-S", fmt.Sprintf("-%d", *lines))
	}
	out, err := tmux(tmuxArgs...)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"target":  target,
			"content": out,
		})
	}
	fmt.Print(out)
	return nil
}

func cmdWaitIdle(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: amux wait-idle <target> [--quiet 800ms] [--timeout 60s] [--interval 200ms]")
	}
	target := args[0]
	fs := flag.NewFlagSet("wait-idle", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	quiet := fs.Duration("quiet", 800*time.Millisecond, "quiescence threshold")
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")
	interval := fs.Duration("interval", 200*time.Millisecond, "poll interval")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	deadline := time.Now().Add(*timeout)
	var lastCap string
	lastChange := time.Now()
	first := true
	for {
		out, err := tmux("capture-pane", "-p", "-t", target)
		if err != nil {
			return err
		}
		if first || out != lastCap {
			lastCap = out
			lastChange = time.Now()
			first = false
		} else if time.Since(lastChange) >= *quiet {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait-idle: timed out after %s on %s", *timeout, target)
		}
		time.Sleep(*interval)
	}
}
