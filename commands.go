package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
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
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cwd := fs.String("c", "", "working directory for the session's first window")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("usage: amux new [-c <dir>] <session>")
	}
	tmuxArgs := []string{"new-session", "-d", "-s", rest[0]}
	if *cwd != "" {
		tmuxArgs = append(tmuxArgs, "-c", *cwd)
	}
	_, err := tmux(tmuxArgs...)
	return err
}

func cmdWindow(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: amux window <session> [-n name] [-- cmd ...]")
	}
	session := args[0]
	if err := mustExist(session); err != nil {
		return err
	}
	fs := flag.NewFlagSet("window", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("n", "", "window name")
	cwd := fs.String("c", "", "working directory for the new window")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tmuxArgs := []string{"new-window", "-t", session}
	if *name != "" {
		tmuxArgs = append(tmuxArgs, "-n", *name)
	}
	if *cwd != "" {
		tmuxArgs = append(tmuxArgs, "-c", *cwd)
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
	if err := mustExist(target); err != nil {
		return err
	}
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
	if err := mustExist(target); err != nil {
		return err
	}
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
	if err := mustExist(target); err != nil {
		return err
	}
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
	if err := mustExist(target); err != nil {
		return err
	}
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
	if err := mustExist(target); err != nil {
		return err
	}
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
		// Bracketed-paste end sentinels (\e[201~) need a beat to land
		// in the receiving TUI before the Enter that submits. Without
		// this sleep, multi-line pastes against Claude's TUI show
		// "[Pasted text #N]" and the Enter gets eaten as part of the
		// paste instead of submitting. 250ms is empirically reliable
		// across a wider range of message sizes while still feeling
		// instant.
		time.Sleep(250 * time.Millisecond)
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
	if err := mustExist(target); err != nil {
		return err
	}
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
	if err := mustExist(target); err != nil {
		return err
	}
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

// --- existence / waiting / one-shot ----------------------------------------

// cmdExists is a silent "does target exist?" check. Zero exit = exists,
// non-zero = doesn't. Nothing printed, to keep the output parseable in
// scripts. Uses display-message because it resolves any target shape
// (session, window, pane) in a single call.
func cmdExists(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: amux exists <target>")
	}
	ok, err := targetExists(args[0])
	if err != nil {
		return err
	}
	if !ok {
		// Silent non-zero: no stderr either, to match common "test"-style
		// probes.
		os.Exit(1)
	}
	return nil
}

// targetExists does an EXACT-name check. tmux's own `-t` resolution does
// prefix matching ("foo" matches "foo-bar"), which makes it useless for
// existence checks — so we enumerate and compare.
//
// Fallback: in some environments (e.g. when spawned as a subprocess from an
// app without a tmux client session) list-sessions or list-windows can return
// empty even when sessions exist, due to how tmux resolves its socket path.
// When the enumeration finds nothing, we fall back to direct probes
// (has-session / display-message) which use tmux's own -t resolution and are
// unaffected by the enumeration issue.
func targetExists(target string) (bool, error) {
	sess, win, pane := parseTarget(target)

	sessions, err := listValues("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if isNoServer(err) {
			return false, nil
		}
		return false, err
	}
	sessFound := contains(sessions, sess)
	if !sessFound {
		// Fallback: list-sessions returned nothing or didn't include this
		// session. Try has-session as a direct probe. "=" prefix forces tmux
		// to do an exact-name match rather than prefix matching.
		if _, ferr := tmux("has-session", "-t", "="+sess); ferr == nil {
			sessFound = true
		}
	}
	if !sessFound {
		return false, nil
	}
	if win == "" {
		return true, nil
	}

	// win can be a numeric index ("1") or a name ("cc"). We match either.
	winRows, err := listValues("list-windows", "-t", sess,
		"-F", "#{window_index}\t#{window_name}")
	if err != nil {
		return false, err
	}
	winMatched := ""
	for _, row := range winRows {
		parts := strings.SplitN(row, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == win || parts[1] == win {
			winMatched = parts[0]
			break
		}
	}
	if winMatched == "" {
		// Fallback: list-windows returned nothing. Try display-message as a
		// direct probe — it exits 0 and prints the window name if the target
		// exists, non-zero otherwise.
		if out, ferr := tmux("display-message", "-t", sess+":"+win, "-p", "#{window_name}"); ferr == nil {
			winMatched = strings.TrimSpace(out)
		}
	}
	if winMatched == "" {
		return false, nil
	}
	if pane == "" {
		return true, nil
	}

	panes, err := listValues("list-panes", "-t", sess+":"+winMatched,
		"-F", "#{pane_index}")
	if err != nil {
		return false, err
	}
	return contains(panes, pane), nil
}

// parseTarget splits session[:window[.pane]] into its pieces.
func parseTarget(t string) (sess, win, pane string) {
	if i := strings.Index(t, ":"); i >= 0 {
		sess = t[:i]
		rest := t[i+1:]
		if j := strings.Index(rest, "."); j >= 0 {
			win = rest[:j]
			pane = rest[j+1:]
		} else {
			win = rest
		}
	} else {
		sess = t
	}
	return
}

func listValues(tmuxArgs ...string) ([]string, error) {
	out, err := tmux(tmuxArgs...)
	if err != nil {
		return nil, err
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// mustExist returns an error if target doesn't resolve to a real session,
// window, or pane via EXACT matching. tmux's own `-t` does prefix
// matching ("foo" matches "foo-bar"), which quietly misroutes commands
// after a rename or when multiple sessions share a prefix — a real
// orchestrator hazard. Every command that operates on an existing
// target routes through mustExist first.
//
// Set AMUX_LOOSE_TARGETS=1 to opt out and fall back to tmux's default
// resolution.
func mustExist(target string) error {
	if os.Getenv("AMUX_LOOSE_TARGETS") == "1" {
		return nil
	}
	ok, err := targetExists(target)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no such target %q (set AMUX_LOOSE_TARGETS=1 to skip this check)", target)
	}
	return nil
}

// cmdWaitFor polls capture-pane until a regex matches, or --timeout
// elapses. This is the deterministic complement to wait-idle: use
// wait-idle when you don't know what marker to look for; use wait-for
// when you do.
//
// --new-only guards against the classic orchestrator bug: you pass the
// orchestrator a prompt that mentions your completion marker, the
// prompt gets echoed into the pane, and wait-for matches on that echo
// rather than on the actual completion.
func cmdWaitFor(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: amux wait-for <target> <pattern> [--timeout 60s] [--interval 200ms] [--lines 0] [--new-only]")
	}
	target := args[0]
	pattern := args[1]
	if err := mustExist(target); err != nil {
		return err
	}
	fs := flag.NewFlagSet("wait-for", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	timeout := fs.Duration("timeout", 60*time.Second, "overall timeout")
	interval := fs.Duration("interval", 200*time.Millisecond, "poll interval")
	lines := fs.Int("lines", 0, "include last N history lines in each capture (0 = visible only)")
	newOnly := fs.Bool("new-only", false, "only match content added AFTER this command starts (ignore existing pane content)")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid regex %q: %w", pattern, err)
	}

	var anchorOffset int
	if *newOnly {
		anchorOffset, err = paneLineOffset(target)
		if err != nil {
			return err
		}
	}

	deadline := time.Now().Add(*timeout)
	for {
		var searchIn string
		if *newOnly {
			searchIn, err = captureSince(target, anchorOffset)
		} else {
			searchIn, err = captureText(target, *lines)
		}
		if err != nil {
			return err
		}
		if re.MatchString(searchIn) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait-for: pattern %q did not match in %s on %s", pattern, *timeout, target)
		}
		time.Sleep(*interval)
	}
}

// captureSince captures pane content from the given absolute line anchor
// (history_size + cursor_y when the anchor was taken) down to the
// current bottom. Converts the absolute anchor to tmux's -S coordinate.
func captureSince(target string, anchorOffset int) (string, error) {
	_, hs, err := paneOffsetAndHistory(target)
	if err != nil {
		return "", err
	}
	rel := anchorOffset - hs
	return tmux("capture-pane", "-p", "-t", target, "-S", strconv.Itoa(rel))
}

// cmdRun is the agent one-shot: reads stdin, pastes+submits, waits until
// the pane goes quiet, then emits ONLY the new content added since the
// submit. That's the primitive agents actually want for "ask and read the
// reply". If the pane's scrollback has rolled past our pre-submit snapshot
// we fall back to emitting the full post-submit capture.
func cmdRun(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: amux run <target> [--quiet 2s] [--timeout 120s] [--lines 2000] < prompt")
	}
	target := args[0]
	if err := mustExist(target); err != nil {
		return err
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	quiet := fs.Duration("quiet", 2*time.Second, "wait-idle quiescence threshold")
	timeout := fs.Duration("timeout", 120*time.Second, "overall timeout (applies to wait-idle or --wait-for)")
	interval := fs.Duration("interval", 300*time.Millisecond, "wait-idle poll interval")
	bracketed := fs.Bool("bracketed", true, "use bracketed paste")
	waitFor := fs.String("wait-for", "", "regex to wait for in new content (instead of wait-idle)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("run: stdin was empty (did you pipe a prompt in?)")
	}
	data = sanitizeBracketedPaste(data)

	// Stabilize the pane before snapshotting. Otherwise the target might
	// still be mid-redraw (e.g. shell finishing a new prompt after the
	// previous command), and the cursor_y we read would be off — the
	// delta would include content that was "in flight" at sample time.
	if err := waitIdleFunc(target, 300*time.Millisecond, 5*time.Second, 100*time.Millisecond); err != nil {
		return err
	}

	// Snapshot: use tmux's own history_size + visible-line count as our
	// "where are we in the transcript" anchor. Line-based slicing is far
	// more robust than string-prefix matching across redraws.
	beforeOffset, err := paneLineOffset(target)
	if err != nil {
		return err
	}

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
	if _, err := tmux("send-keys", "-t", target, "Enter"); err != nil {
		return err
	}

	// Wait for the end condition. If --wait-for is given, match a regex
	// in the content added since submit (deterministic). Otherwise fall
	// back to wait-idle (heuristic).
	if *waitFor != "" {
		re, err := regexp.Compile(*waitFor)
		if err != nil {
			return fmt.Errorf("invalid --wait-for regex %q: %w", *waitFor, err)
		}
		deadline := time.Now().Add(*timeout)
		for {
			searchIn, err := captureSince(target, beforeOffset)
			if err != nil {
				return err
			}
			if re.MatchString(searchIn) {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("run: --wait-for %q did not match in %s on %s", *waitFor, *timeout, target)
			}
			time.Sleep(*interval)
		}
	} else {
		if err := waitIdleFunc(target, *quiet, *timeout, *interval); err != nil {
			return err
		}
	}

	afterOffset, afterHS, err := paneOffsetAndHistory(target)
	if err != nil {
		return err
	}
	// Convert our absolute "lines-elapsed" marker into tmux's -S coordinate:
	//   relStart = beforeOffset - afterHistorySize
	// Positive means "line N of visible pane from top". Negative means
	// "N lines up from visible top (into scrollback)". Either way,
	// tmux captures from that point down to the current bottom.
	relStart := beforeOffset - afterHS
	if relStart > afterOffset-afterHS {
		// No movement — snap to current cursor line so we still emit at
		// least one line (Claude's ⏺ reply on an unchanged screen).
		relStart = afterOffset - afterHS
	}
	out, err := tmux("capture-pane", "-p", "-t", target,
		"-S", strconv.Itoa(relStart))
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// paneOffsetAndHistory returns (absolute_cursor_line, history_size) for
// the target pane. The absolute line is `history_size + cursor_y` —
// history_size grows as content scrolls out of the visible screen, so
// the sum increases monotonically whenever the pane emits output.
func paneOffsetAndHistory(target string) (int, int, error) {
	out, err := tmux("display-message", "-p", "-t", target,
		"#{history_size} #{cursor_y}")
	if err != nil {
		return 0, 0, err
	}
	parts := strings.Fields(strings.TrimSpace(out))
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected display-message output: %q", out)
	}
	hs, err1 := strconv.Atoi(parts[0])
	cy, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("parse display-message: %v %v (%q)", err1, err2, out)
	}
	return hs + cy, hs, nil
}

func paneLineOffset(target string) (int, error) {
	off, _, err := paneOffsetAndHistory(target)
	return off, err
}

// captureText is the Go-side version of `amux capture`, used internally.
func captureText(target string, lines int) (string, error) {
	args := []string{"capture-pane", "-p", "-t", target}
	if lines > 0 {
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	}
	return tmux(args...)
}

// waitIdleFunc is the Go-side version of `amux wait-idle`, used internally
// by `run` so we don't shell out to ourselves.
func waitIdleFunc(target string, quiet, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
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
		} else if time.Since(lastChange) >= quiet {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("wait-idle: timed out after %s on %s", timeout, target)
		}
		time.Sleep(interval)
	}
}


// --- log (pipe-pane → file) ------------------------------------------------

// cmdLog turns on/off tmux's pipe-pane, which streams everything a pane
// displays to a command. We use `cat >> FILE` so the file is an
// append-only transcript. `amux log TARGET off` detaches the pipe.
//
// Orchestrators can use this to persist full agent transcripts without
// polling capture-pane. One `log` call at spawn time, one `off` at
// teardown — the file has the whole history.
func cmdLog(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: amux log <target> <file-or-off>")
	}
	target, arg := args[0], args[1]
	if err := mustExist(target); err != nil {
		return err
	}
	if arg == "off" {
		// pipe-pane with no shell-command toggles the pipe off for target.
		_, err := tmux("pipe-pane", "-t", target)
		return err
	}
	path := arg
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "~") {
		// Resolve to absolute — tmux runs the pipe command in its own
		// cwd, which may not be the user's, and relative paths are a
		// footgun.
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		path = wd + "/" + path
	}
	// `-o` opens the pipe if not already; with a command arg, replaces
	// any existing pipe. `-O` appends to existing pipe output (tmux 3.4+).
	// We use plain pipe-pane with the command — it replaces.
	shellCmd := "cat >> " + shellQuote(path)
	_, err := tmux("pipe-pane", "-t", target, shellCmd)
	return err
}

// --- rename / color --------------------------------------------------------

func cmdRename(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: amux rename <target> <new-name>")
	}
	target, name := args[0], args[1]
	if err := mustExist(target); err != nil {
		return err
	}
	switch {
	case strings.Contains(target, "."):
		// Pane: use pane title via select-pane -T.
		_, err := tmux("select-pane", "-t", target, "-T", name)
		return err
	case strings.Contains(target, ":"):
		_, err := tmux("rename-window", "-t", target, name)
		return err
	default:
		_, err := tmux("rename-session", "-t", target, name)
		return err
	}
}

// cmdColor tints the window-status entry (for window targets) or pane
// border (for pane targets) so a human watching the session can tell
// multiple agents apart at a glance. Accepts tmux color names (red,
// green, yellow, blue, magenta, cyan, white, black, chartreuse, ...)
// or hex like "#80ff00". Session targets are not supported — color
// applies per-window and per-pane.
func cmdColor(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: amux color <target> <color>  (window or pane target)")
	}
	target, color := args[0], args[1]
	if err := mustExist(target); err != nil {
		return err
	}
	switch {
	case strings.Contains(target, "."):
		// Pane: set pane-border-style. User also needs `pane-border-status`
		// set to show borders — we enable it per-pane if possible.
		_, err := tmux("set-option", "-p", "-t", target, "pane-border-style", "fg="+color+",bold")
		if err != nil {
			return err
		}
		// Make borders visible (top border shows titles too).
		_, _ = tmux("set-option", "-t", target, "pane-border-status", "top")
		return nil
	case strings.Contains(target, ":"):
		// Window: color the status bar entry.
		_, err := tmux("set-window-option", "-t", target, "window-status-style", "fg="+color+",bold")
		if err != nil {
			return err
		}
		_, err = tmux("set-window-option", "-t", target, "window-status-current-style", "fg="+color+",bold,reverse")
		return err
	default:
		return fmt.Errorf("color: target must be a window or pane (got session %q)", target)
	}
}

func cmdWaitIdle(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: amux wait-idle <target> [--quiet 800ms] [--timeout 60s] [--interval 200ms]")
	}
	target := args[0]
	if err := mustExist(target); err != nil {
		return err
	}
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
