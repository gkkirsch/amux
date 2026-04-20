package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestClaudeIntegration is the proof-of-concept: drive a real Claude Code
// TUI end-to-end through amux. Spawns `claude` in a pane, pastes a prompt
// that asks for a specific marker word in the reply, and asserts the
// marker appears in the captured response.
//
// Skipped automatically if `claude` isn't on PATH. Set AMUX_SKIP_CLAUDE=1
// to skip even when it is available (for faster local iteration).
func TestClaudeIntegration(t *testing.T) {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not on PATH; skipping integration test")
	}
	if os.Getenv("AMUX_SKIP_CLAUDE") == "1" {
		t.Skip("AMUX_SKIP_CLAUDE=1")
	}

	sess := uniqueSession(t)
	target := sess + ":claude"

	// Launch claude with --dangerously-skip-permissions so no interactive
	// tool-approval prompt blocks us. Requires the user to already be
	// authenticated (persisted in ~/.claude).
	mustAmux(t, "", "window", sess, "-n", "claude", "--",
		claudePath, "--dangerously-skip-permissions")

	// Poll captures until the TUI has actually drawn its prompt (❯) and
	// the welcome banner. We can't rely on wait-idle alone because the
	// pane can be momentarily "quiet" before claude paints its first
	// frame (the binary takes a beat to start).
	var initial string
	waitFor(t, 60*time.Second, "claude welcome screen + prompt", func() bool {
		initial = mustAmux(t, "", "capture", target, "--lines", "200")
		low := strings.ToLower(initial)
		return strings.Contains(low, "claude code") && strings.Contains(initial, "❯")
	})

	// Brief settle so any remaining first-render churn is done.
	mustAmux(t, "", "wait-idle", target,
		"--quiet", "1500ms", "--timeout", "15s", "--interval", "300ms")
	t.Logf("claude TUI ready:\n%s", trimTail(initial, 20))

	// Prompt with a unique reply marker — something Claude would only emit
	// if it actually processed our prompt.
	marker := "AMUXPROOFOK"
	prompt := "Please reply with exactly the single word " + marker + " and nothing else."

	mustAmux(t, prompt, "paste", target, "--submit")

	// Wait for the reply stream to finish.
	mustAmux(t, "", "wait-idle", target,
		"--quiet", "4s", "--timeout", "120s", "--interval", "500ms")

	// Poll captures for up to 30s in case tmux's scrollback lags a beat.
	deadline := time.Now().Add(30 * time.Second)
	var lastCap string
	for time.Now().Before(deadline) {
		lastCap = mustAmux(t, "", "capture", target, "--lines", "500")
		if strings.Contains(lastCap, marker) {
			t.Logf("captured reply containing marker %q:\n%s", marker, trimTail(lastCap, 60))
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("did not see marker %q in claude response. Last capture:\n%s", marker, lastCap)
}

func trimTail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) <= lines {
		return s
	}
	return "... (" + itoa(len(parts)-lines) + " earlier lines elided) ...\n" +
		strings.Join(parts[len(parts)-lines:], "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
