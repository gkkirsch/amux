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
	mustAmux(t, payload, "paste", target)
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
