package main

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// tmux runs `tmux <args...>` and returns stdout. On a non-zero exit, the
// returned error embeds stderr verbatim so callers see tmux's own diagnostic.
func tmux(args ...string) (string, error) {
	return tmuxStdin(nil, args...)
}

// tmuxStdin is tmux() but pipes stdin to the subprocess — used for
// `load-buffer -`, which is the reliable path for multi-line pastes.
func tmuxStdin(stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	stdout := out.String()
	if err != nil {
		return stdout, &tmuxError{
			args:   args,
			stderr: strings.TrimSpace(errb.String()),
			err:    err,
		}
	}
	return stdout, nil
}

type tmuxError struct {
	args   []string
	stderr string
	err    error
}

func (e *tmuxError) Error() string {
	if e.stderr != "" {
		return fmt.Sprintf("tmux %s: %s", strings.Join(e.args, " "), e.stderr)
	}
	return fmt.Sprintf("tmux %s: %v", strings.Join(e.args, " "), e.err)
}

func (e *tmuxError) Unwrap() error { return e.err }

// isNoServer reports whether err is the "no server running" case. tmux emits
// this from list-* calls before any session exists; we treat it as empty.
func isNoServer(err error) bool {
	var te *tmuxError
	if !errors.As(err, &te) {
		return false
	}
	s := strings.ToLower(te.stderr)
	return strings.Contains(s, "no server running") || strings.Contains(s, "error connecting")
}
