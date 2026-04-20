package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var amuxBin string

// TestMain builds the amux binary once into a tempdir so every test below
// exercises the real CLI end-to-end (not the cmd* funcs directly).
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintln(os.Stderr, "SKIP: tmux not on PATH")
		os.Exit(0)
	}
	tmp, err := os.MkdirTemp("", "amux-build-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(tmp)
	amuxBin = filepath.Join(tmp, "amux")
	build := exec.Command("go", "build", "-o", amuxBin, ".")
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build failed:", err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

// --- helpers ---------------------------------------------------------------

type result struct {
	stdout, stderr string
	exit           int
}

func runAmux(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(amuxBin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("run amux %v: %v", args, err)
		}
	}
	return result{out.String(), errb.String(), exit}
}

func mustAmux(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	r := runAmux(t, stdin, args...)
	if r.exit != 0 {
		t.Fatalf("amux %v exited %d\nstdout:\n%s\nstderr:\n%s", args, r.exit, r.stdout, r.stderr)
	}
	return r.stdout
}

func uniqueSession(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("amuxt-%d-%d", os.Getpid(), time.Now().UnixNano())
	mustAmux(t, "", "new", name)
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	return name
}

// waitFor polls cond until true or timeout. Returns the last captured value
// from cond so failing tests can print what they last saw.
func waitFor(t *testing.T, d time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", d, desc)
}

// --- lifecycle -------------------------------------------------------------

func TestNewAndList(t *testing.T) {
	sess := uniqueSession(t)
	out := mustAmux(t, "", "list")
	if !strings.Contains(out, sess) {
		t.Fatalf("list did not contain %q:\n%s", sess, out)
	}
}

func TestListJSON(t *testing.T) {
	sess := uniqueSession(t)
	out := mustAmux(t, "", "list", "--json")
	var panes []paneInfo
	if err := json.Unmarshal([]byte(out), &panes); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	found := false
	for _, p := range panes {
		if p.Session == sess {
			found = true
			if p.Window != 0 || p.Pane != 0 {
				t.Errorf("expected window=0 pane=0, got %d.%d", p.Window, p.Pane)
			}
			if p.PID <= 0 {
				t.Errorf("expected positive pid, got %d", p.PID)
			}
		}
	}
	if !found {
		t.Fatalf("session %s not in json output:\n%s", sess, out)
	}
}

func TestListEmpty_NoServer_ReturnsEmpty(t *testing.T) {
	// Run list against a guaranteed-empty tmux server namespace. We need
	// to (a) clear TMUX so an existing session we're inside doesn't leak,
	// and (b) point TMUX_TMPDIR at a fresh directory so no server exists.
	tmp := t.TempDir()
	cmd := exec.Command(amuxBin, "list", "--json")
	env := []string{}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_TMPDIR=") {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "TMUX_TMPDIR="+tmp)
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("list on empty server should succeed, got err=%v stderr=%s", err, errb.String())
	}
	var panes []paneInfo
	if err := json.Unmarshal(out.Bytes(), &panes); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out.String())
	}
	if len(panes) != 0 {
		t.Fatalf("expected empty panes list, got %d", len(panes))
	}
}

func TestWindow(t *testing.T) {
	sess := uniqueSession(t)
	mustAmux(t, "", "window", sess, "-n", "work")
	out := mustAmux(t, "", "list", "--json")
	var panes []paneInfo
	_ = json.Unmarshal([]byte(out), &panes)
	found := false
	for _, p := range panes {
		if p.Session == sess && p.WinName == "work" {
			found = true
		}
	}
	if !found {
		t.Fatalf("window 'work' not found:\n%s", out)
	}
}

func TestSplit(t *testing.T) {
	sess := uniqueSession(t)
	target := sess + ":0"
	mustAmux(t, "", "split", target)
	mustAmux(t, "", "split", target, "-h")
	out := mustAmux(t, "", "list", "--json")
	var panes []paneInfo
	_ = json.Unmarshal([]byte(out), &panes)
	count := 0
	for _, p := range panes {
		if p.Session == sess {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("expected 3 panes after 2 splits, got %d:\n%s", count, out)
	}
}

func TestKillSession(t *testing.T) {
	sess := uniqueSession(t)
	mustAmux(t, "", "kill", sess)
	out := mustAmux(t, "", "list")
	if strings.Contains(out, sess) {
		t.Fatalf("kill did not remove %s from list:\n%s", sess, out)
	}
}

// --- input: send / key -----------------------------------------------------

// TestSendIsLiteral proves `send` passes text through tmux -l, so the word
// "Enter" inside the text arrives as 5 literal characters rather than being
// interpreted as a keypress. We verify by writing into `cat > FILE` and then
// reading the file.
func TestSendIsLiteral(t *testing.T) {
	sess := uniqueSession(t)
	outFile := filepath.Join(t.TempDir(), "out")
	target := sess + ":0"
	// Run the reader inside the pane.
	mustAmux(t, "", "send", target, "cat > "+outFile)
	mustAmux(t, "", "key", target, "Enter")

	// Small pause to ensure cat is ready for input.
	time.Sleep(200 * time.Millisecond)

	// Send text containing the literal word "Enter" — must NOT be treated
	// as a keypress.
	mustAmux(t, "", "send", target, "hello Enter world")
	mustAmux(t, "", "key", target, "Enter") // real newline
	mustAmux(t, "", "key", target, "C-d")   // EOF to cat

	waitFor(t, 3*time.Second, "outfile to exist and be non-empty", func() bool {
		b, err := os.ReadFile(outFile)
		return err == nil && len(b) > 0
	})

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read outfile: %v", err)
	}
	want := "hello Enter world\n"
	if string(got) != want {
		t.Fatalf("send wasn't literal.\n  want: %q\n  got:  %q", want, string(got))
	}
}

// TestKeyMultiple proves `key` accepts several named keys in one call and
// interprets them as keypresses (not literal text).
func TestKeyMultiple(t *testing.T) {
	sess := uniqueSession(t)
	outFile := filepath.Join(t.TempDir(), "lines")
	target := sess + ":0"
	mustAmux(t, "", "send", target, "cat > "+outFile)
	mustAmux(t, "", "key", target, "Enter")
	time.Sleep(200 * time.Millisecond)

	mustAmux(t, "", "send", target, "first")
	mustAmux(t, "", "key", target, "Enter", "Enter") // first ends, empty line
	mustAmux(t, "", "send", target, "third")
	mustAmux(t, "", "key", target, "Enter")
	mustAmux(t, "", "key", target, "C-d")

	waitFor(t, 3*time.Second, "output file", func() bool {
		b, _ := os.ReadFile(outFile)
		return strings.Contains(string(b), "third\n")
	})

	got, _ := os.ReadFile(outFile)
	want := "first\n\nthird\n"
	if string(got) != want {
		t.Fatalf("key didn't insert keypresses correctly.\n  want: %q\n  got:  %q", want, string(got))
	}
}

// --- input: paste (multi-line, the Claude Code use case) --------------------

// TestPasteMultiline proves `paste` delivers multi-line content reliably to
// a consumer, and `--submit` sends Enter after the paste.
func TestPasteMultiline(t *testing.T) {
	sess := uniqueSession(t)
	outFile := filepath.Join(t.TempDir(), "pasted")
	target := sess + ":0"
	mustAmux(t, "", "send", target, "cat > "+outFile)
	mustAmux(t, "", "key", target, "Enter")
	time.Sleep(200 * time.Millisecond)

	content := "line one\nline two with spaces\nline three: special $chars \"and quotes\"\n"
	// --bracketed=false: don't emit \e[200~/\e[201~ markers. Those are
	// only meaningful to TUIs that enabled bracketed paste; our consumer
	// is plain `cat` and emitting them here makes the test order-sensitive
	// on tmux versions that still deliver the markers into a non-bracketed
	// application.
	mustAmux(t, content, "paste", target, "--bracketed=false")
	// paste doesn't append Enter; we need one more Enter to flush the last
	// (unterminated) line if content didn't already end in newline.
	// Our content already ends in \n, so no extra Enter needed — just EOF.
	mustAmux(t, "", "key", target, "C-d")

	waitFor(t, 3*time.Second, "pasted file non-empty", func() bool {
		b, _ := os.ReadFile(outFile)
		return len(b) > 0 && strings.Contains(string(b), "line three")
	})

	got, _ := os.ReadFile(outFile)
	if string(got) != content {
		t.Fatalf("paste lost content.\n  want: %q\n  got:  %q", content, string(got))
	}
}

// TestPasteSubmit proves `--submit` sends Enter after the paste — exercised
// via `sh -c 'read LINE; echo GOT:$LINE > FILE'`.
func TestPasteSubmit(t *testing.T) {
	sess := uniqueSession(t)
	outFile := filepath.Join(t.TempDir(), "submitted")
	// Fresh window running a reader that writes what it got to a file.
	mustAmux(t, "", "window", sess, "-n", "reader", "--",
		"sh", "-c", fmt.Sprintf("read LINE; printf 'GOT:%%s' \"$LINE\" > %s", outFile))
	time.Sleep(200 * time.Millisecond)

	mustAmux(t, "the quick brown fox", "paste", sess+":reader", "--submit")

	waitFor(t, 3*time.Second, "submit file", func() bool {
		_, err := os.Stat(outFile)
		return err == nil
	})

	got, _ := os.ReadFile(outFile)
	want := "GOT:the quick brown fox"
	if string(got) != want {
		t.Fatalf("paste --submit failed.\n  want: %q\n  got:  %q", want, string(got))
	}
}

// --- input: type (char-by-char) --------------------------------------------

func TestType(t *testing.T) {
	sess := uniqueSession(t)
	outFile := filepath.Join(t.TempDir(), "typed")
	target := sess + ":0"
	mustAmux(t, "", "send", target, "cat > "+outFile)
	mustAmux(t, "", "key", target, "Enter")
	time.Sleep(200 * time.Millisecond)

	mustAmux(t, "", "type", target, "abc123", "--delay", "5ms")
	mustAmux(t, "", "key", target, "Enter")
	mustAmux(t, "", "key", target, "C-d")

	waitFor(t, 3*time.Second, "typed file", func() bool {
		b, _ := os.ReadFile(outFile)
		return strings.Contains(string(b), "abc123")
	})
	got, _ := os.ReadFile(outFile)
	if strings.TrimSpace(string(got)) != "abc123" {
		t.Fatalf("type delivered wrong content: %q", string(got))
	}
}

// --- observation: capture / wait-idle ---------------------------------------

func TestCaptureShowsOutput(t *testing.T) {
	sess := uniqueSession(t)
	target := sess + ":0"
	marker := fmt.Sprintf("amux-marker-%d", time.Now().UnixNano())
	mustAmux(t, "", "send", target, "echo "+marker)
	mustAmux(t, "", "key", target, "Enter")

	// Poll capture until we see the marker echoed back by the shell.
	waitFor(t, 3*time.Second, "marker in capture", func() bool {
		out := mustAmux(t, "", "capture", target)
		return strings.Count(out, marker) >= 2 // once typed, once from echo output
	})
}

func TestCaptureJSON(t *testing.T) {
	sess := uniqueSession(t)
	target := sess + ":0"
	out := mustAmux(t, "", "capture", target, "--json")
	var v struct {
		Target, Content string
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if v.Target != target {
		t.Fatalf("target mismatch: want %s, got %s", target, v.Target)
	}
}

// TestWaitIdle proves wait-idle blocks while the pane is churning and returns
// after the pane stops changing for the quiet window.
func TestWaitIdle(t *testing.T) {
	sess := uniqueSession(t)
	// Spin up a window that prints for ~1.5s then stays alive (so we can
	// observe the idle transition without the pane disappearing on exit).
	mustAmux(t, "", "window", sess, "-n", "busy", "--",
		"sh", "-c", "for i in 1 2 3; do echo tick$i; sleep 0.5; done; sleep 30")

	start := time.Now()
	mustAmux(t, "", "wait-idle", sess+":busy",
		"--quiet", "500ms", "--timeout", "10s", "--interval", "100ms")
	elapsed := time.Since(start)

	// Loop runs ~1.5s; wait-idle should return after that + ~500ms quiet.
	// Accept a wide window to keep the test robust on slow machines.
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("wait-idle returned too fast: %s", elapsed)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("wait-idle took too long: %s", elapsed)
	}

	cap := mustAmux(t, "", "capture", sess+":busy")
	for _, want := range []string{"tick1", "tick2", "tick3"} {
		if !strings.Contains(cap, want) {
			t.Fatalf("expected %s in capture:\n%s", want, cap)
		}
	}
}

func TestWaitIdleTimeout(t *testing.T) {
	sess := uniqueSession(t)
	// Infinite loop — wait-idle must time out.
	mustAmux(t, "", "window", sess, "-n", "forever", "--",
		"sh", "-c", "while :; do echo busy; sleep 0.1; done")

	r := runAmux(t, "", "wait-idle", sess+":forever",
		"--quiet", "500ms", "--timeout", "1500ms", "--interval", "100ms")
	if r.exit == 0 {
		t.Fatalf("expected non-zero exit on timeout, got 0\nstderr: %s", r.stderr)
	}
	if !strings.Contains(r.stderr, "timed out") {
		t.Fatalf("expected 'timed out' in stderr, got: %s", r.stderr)
	}
}

// --- usage / error paths ---------------------------------------------------

func TestUsageOnUnknownCommand(t *testing.T) {
	r := runAmux(t, "", "nonsense")
	if r.exit != 2 {
		t.Fatalf("expected exit 2, got %d", r.exit)
	}
	if !strings.Contains(r.stderr, "unknown command") {
		t.Fatalf("expected 'unknown command' in stderr, got: %s", r.stderr)
	}
}

// TestPasteStripsBracketedPasteSentinels proves amux removes embedded
// \e[200~ / \e[201~ markers from user content — without the strip, a
// payload containing \e[201~ prematurely closes bracketed paste on the
// receiving TUI and silently truncates the prompt (observed against
// Claude Code's TUI).
func TestPasteStripsBracketedPasteSentinels(t *testing.T) {
	sess := uniqueSession(t)
	outFile := filepath.Join(t.TempDir(), "stripped")
	target := sess + ":0"
	mustAmux(t, "", "send", target, "cat > "+outFile)
	mustAmux(t, "", "key", target, "Enter")
	time.Sleep(200 * time.Millisecond)

	// Content embeds the end sentinel in the middle.
	payload := "before\n\x1b[201~middle\n\x1b[200~after\n"
	// --bracketed=false: isolate the sanitizer from tmux's own outer
	// wrapping, which varies across tmux versions for non-TUI consumers.
	mustAmux(t, payload, "paste", target, "--bracketed=false")
	mustAmux(t, "", "key", target, "C-d")

	waitFor(t, 3*time.Second, "stripped file", func() bool {
		b, _ := os.ReadFile(outFile)
		return strings.Contains(string(b), "after")
	})
	got, _ := os.ReadFile(outFile)
	want := "before\nmiddle\nafter\n"
	if string(got) != want {
		t.Fatalf("sentinels weren't stripped.\n  want: %q\n  got:  %q", want, string(got))
	}
}

// --- new commands: exists / wait-for / run / rename / color --------------

func TestExists(t *testing.T) {
	sess := uniqueSession(t)
	// Session exists → exit 0.
	if r := runAmux(t, "", "exists", sess); r.exit != 0 {
		t.Fatalf("exists on real session: exit=%d stderr=%s", r.exit, r.stderr)
	}
	// Nonexistent → non-zero.
	if r := runAmux(t, "", "exists", "definitely-not-a-session-xyz"); r.exit == 0 {
		t.Fatalf("exists on fake session should fail")
	}
	// Silent: no stdout on success.
	r := runAmux(t, "", "exists", sess)
	if r.stdout != "" {
		t.Fatalf("exists should be silent, got stdout: %q", r.stdout)
	}
}

func TestWaitForMatches(t *testing.T) {
	sess := uniqueSession(t)
	// Trigger delayed output.
	mustAmux(t, "", "window", sess, "-n", "emit", "--",
		"sh", "-c", "sleep 0.4; echo READY-MARKER; sleep 30")
	start := time.Now()
	mustAmux(t, "", "wait-for", sess+":emit", "READY-MARKER",
		"--timeout", "5s", "--interval", "100ms")
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("wait-for should match quickly after the line appears, took %s", elapsed)
	}
}

func TestWaitForTimeout(t *testing.T) {
	sess := uniqueSession(t)
	r := runAmux(t, "", "wait-for", sess+":0", "NEVER_APPEARS",
		"--timeout", "600ms", "--interval", "100ms")
	if r.exit == 0 {
		t.Fatalf("wait-for should exit non-zero on timeout")
	}
	if !strings.Contains(r.stderr, "did not match") {
		t.Fatalf("expected 'did not match' in stderr, got: %s", r.stderr)
	}
}

func TestWaitForInvalidRegex(t *testing.T) {
	sess := uniqueSession(t)
	r := runAmux(t, "", "wait-for", sess+":0", "[bad", "--timeout", "1s")
	if r.exit == 0 {
		t.Fatalf("expected non-zero on invalid regex")
	}
	if !strings.Contains(r.stderr, "invalid regex") {
		t.Fatalf("expected 'invalid regex' in stderr, got: %s", r.stderr)
	}
}

// TestRun proves the paste+submit+wait-idle+delta flow works against a
// real shell. The delta emitted should contain our echoed marker and
// NOT the earlier "prompt" we wrote before submitting.
func TestRun(t *testing.T) {
	sess := uniqueSession(t)
	target := sess + ":0"
	// Shell needs to be ready — print a marker and sync on it first.
	mustAmux(t, "", "send", target, "echo PRE-MARKER")
	mustAmux(t, "", "key", target, "Enter")
	mustAmux(t, "", "wait-for", target, "PRE-MARKER", "--timeout", "3s")

	// `run` pastes "echo RUN-REPLY" + Enter, waits for idle, emits
	// the new content.
	out := mustAmux(t, "echo RUN-REPLY-42\n", "run", target,
		"--quiet", "600ms", "--timeout", "10s", "--interval", "100ms", "--bracketed=false")
	if !strings.Contains(out, "RUN-REPLY-42") {
		t.Fatalf("run didn't return the reply:\n%s", out)
	}
	if strings.Contains(out, "PRE-MARKER") {
		t.Fatalf("run delta should not include pre-submit PRE-MARKER:\n%s", out)
	}
}

func TestRunEmptyStdin(t *testing.T) {
	sess := uniqueSession(t)
	r := runAmux(t, "", "run", sess+":0", "--timeout", "2s")
	if r.exit == 0 {
		t.Fatalf("run with empty stdin should fail")
	}
	if !strings.Contains(r.stderr, "empty") {
		t.Fatalf("expected 'empty' in stderr, got: %s", r.stderr)
	}
}

func TestRenameSession(t *testing.T) {
	sess := uniqueSession(t)
	newName := sess + "-renamed"
	mustAmux(t, "", "rename", sess, newName)
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", newName).Run() })
	// Old name gone, new name exists.
	if r := runAmux(t, "", "exists", sess); r.exit == 0 {
		t.Fatalf("old session name %s should be gone", sess)
	}
	if r := runAmux(t, "", "exists", newName); r.exit != 0 {
		t.Fatalf("new session name %s should exist: %s", newName, r.stderr)
	}
}

func TestRenameWindow(t *testing.T) {
	sess := uniqueSession(t)
	mustAmux(t, "", "window", sess, "-n", "before")
	mustAmux(t, "", "rename", sess+":before", "after")
	out := mustAmux(t, "", "list", "--json")
	if !strings.Contains(out, `"after"`) {
		t.Fatalf("renamed window 'after' not in list:\n%s", out)
	}
	if strings.Contains(out, `"before"`) {
		t.Fatalf("old name 'before' should be gone:\n%s", out)
	}
}

func TestColorWindow(t *testing.T) {
	sess := uniqueSession(t)
	mustAmux(t, "", "window", sess, "-n", "tagged")
	// Setting a color should succeed. The effect is visual only; we
	// verify by reading the option back via tmux directly.
	mustAmux(t, "", "color", sess+":tagged", "red")
	out, err := exec.Command("tmux", "show-window-options", "-t",
		sess+":tagged", "window-status-style").Output()
	if err != nil {
		t.Fatalf("show-window-options: %v", err)
	}
	if !strings.Contains(string(out), "red") {
		t.Fatalf("expected 'red' in window-status-style, got: %s", string(out))
	}
}

func TestColorSessionErrors(t *testing.T) {
	sess := uniqueSession(t)
	r := runAmux(t, "", "color", sess, "red")
	if r.exit == 0 {
		t.Fatalf("color on session should fail")
	}
	if !strings.Contains(r.stderr, "must be a window or pane") {
		t.Fatalf("expected helpful error, got: %s", r.stderr)
	}
}

// TestHelpPerCommand proves `amux <cmd> -h` prints the command-specific
// help text, not the top-level usage.
func TestHelpPerCommand(t *testing.T) {
	r := runAmux(t, "", "paste", "-h")
	if r.exit != 0 {
		t.Fatalf("amux paste -h should exit 0, got %d", r.exit)
	}
	if !strings.Contains(r.stdout, "bracketed paste") {
		t.Fatalf("expected paste help to mention 'bracketed paste':\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "amux <command> [flags]") {
		t.Fatalf("paste -h printed the top-level usage instead of the command help")
	}
}

// TestArrowKeys proves Left arrow moves the cursor inside a readline-enabled
// consumer. Uses `bash -c 'read -e LINE; echo GOT:$LINE > FILE'`: with -e,
// bash invokes readline so cursor movement is a measurable operation.
func TestArrowKeys(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	sess := uniqueSession(t)
	outFile := filepath.Join(t.TempDir(), "arrows")
	mustAmux(t, "", "window", sess, "-n", "rl", "--",
		"bash", "-c", fmt.Sprintf("read -e LINE; printf 'GOT:%%s' \"$LINE\" > %s", outFile))
	time.Sleep(250 * time.Millisecond) // let bash reach 'read'

	target := sess + ":rl"
	// Type "hello", move cursor 5 left (to the start), insert "X",
	// submit. Expected: GOT:Xhello.
	mustAmux(t, "", "send", target, "hello")
	mustAmux(t, "", "key", target, "Left", "Left", "Left", "Left", "Left")
	mustAmux(t, "", "send", target, "X")
	mustAmux(t, "", "key", target, "Enter")

	waitFor(t, 3*time.Second, "arrows output file", func() bool {
		b, _ := os.ReadFile(outFile)
		return strings.Contains(string(b), "GOT:")
	})
	got, _ := os.ReadFile(outFile)
	if string(got) != "GOT:Xhello" {
		t.Fatalf("arrow keys didn't move cursor correctly.\n  want: %q\n  got:  %q", "GOT:Xhello", string(got))
	}
}

func TestSendRequiresTwoArgs(t *testing.T) {
	r := runAmux(t, "", "send", "only-target")
	if r.exit == 0 {
		t.Fatalf("expected error on missing text")
	}
}
