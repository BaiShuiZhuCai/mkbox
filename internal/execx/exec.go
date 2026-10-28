// Package execx wraps command execution with dry-run, logging and
// consistent error reporting.
package execx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/makebox/makebox/internal/logx"
)

// Runner executes external commands. When DryRun is set, commands are only
// printed, never executed.
type Runner struct {
	DryRun  bool
	Verbose bool
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

// New returns a Runner wired to the current process streams.
func New(dryRun, verbose bool) *Runner {
	return &Runner{
		DryRun:  dryRun,
		Verbose: verbose,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
}

// CommandError describes a failed command.
type CommandError struct {
	Cmd      string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *CommandError) Error() string {
	msg := fmt.Sprintf("command failed: %s", e.Cmd)
	if e.ExitCode >= 0 {
		msg += fmt.Sprintf(" (exit %d)", e.ExitCode)
	}
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + lastLines(s, 6)
	}
	return msg
}

func (e *CommandError) Unwrap() error { return e.Err }

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// Quote renders an argument for display.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\'' || r == '$' || r == '\\' || r == '&' || r == '|' || r == ';' || r == '(' || r == ')'
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func cmdline(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, Quote(name))
	for _, a := range args {
		parts = append(parts, Quote(a))
	}
	return strings.Join(parts, " ")
}

func (r *Runner) show(line string) {
	if r.DryRun || r.Verbose {
		logx.Command(r.DryRun, line)
	}
}

// Run executes a command with its output streamed to the runner streams.
func (r *Runner) Run(name string, args ...string) error {
	return r.RunEnv(nil, name, args...)
}

// RunEnv is like Run with extra environment variables.
func (r *Runner) RunEnv(env []string, name string, args ...string) error {
	r.show(cmdline(name, args))
	if r.DryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = r.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		code := -1
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return &CommandError{Cmd: cmdline(name, args), ExitCode: code, Err: err}
	}
	return nil
}

// RunInput executes a command with the provided standard input.
func (r *Runner) RunInput(input string, name string, args ...string) error {
	r.show(cmdline(name, args) + " <<< stdin")
	if r.DryRun {
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		code := -1
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return &CommandError{Cmd: cmdline(name, args), ExitCode: code, Err: err}
	}
	return nil
}

// Output runs a command and returns trimmed stdout. Stderr is captured and
// included in the error when the command fails.
func (r *Runner) Output(name string, args ...string) (string, error) {
	return r.OutputEnv(nil, name, args...)
}

// OutputEnv is like Output with extra environment variables.
func (r *Runner) OutputEnv(env []string, name string, args ...string) (string, error) {
	r.show(cmdline(name, args) + "  (capture)")
	if r.DryRun {
		return "", nil
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		code := -1
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return stdout.String(), &CommandError{
			Cmd:      cmdline(name, args),
			ExitCode: code,
			Stderr:   stderr.String(),
			Err:      err,
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Shell runs a script through /bin/sh.
func (r *Runner) Shell(script string) error {
	r.show("sh -c " + Quote(script))
	if r.DryRun {
		return nil
	}
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = os.Environ()
	cmd.Stdin = r.Stdin
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		code := -1
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return &CommandError{Cmd: "sh -c " + Quote(script), ExitCode: code, Err: err}
	}
	return nil
}

// Has reports whether a command exists in PATH.
func Has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// HasAny reports whether at least one of the commands exists.
func HasAny(names ...string) bool {
	for _, n := range names {
		if Has(n) {
			return true
		}
	}
	return false
}
