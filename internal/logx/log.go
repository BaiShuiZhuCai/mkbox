// Package logx provides small, consistent terminal output helpers.
package logx

import (
	"fmt"
	"os"
	"strings"

	"github.com/makebox/makebox/internal/i18n"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	cyan   = "\033[36m"
)

var noColor = detectNoColor()

func detectNoColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return true
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// SetNoColor disables or enables ANSI colors.
func SetNoColor(v bool) { noColor = v }

func paint(c, s string) string {
	if noColor {
		return s
	}
	return c + s + reset
}

func printf(w *os.File, format string, args ...any) {
	fmt.Fprintf(w, format, args...)
}

// Infof prints a neutral message.
func Infof(format string, args ...any) {
	printf(os.Stdout, paint(cyan, "→ ")+i18n.Translate(format)+"\n", args...)
}

// Successf prints a success message.
func Successf(format string, args ...any) {
	printf(os.Stdout, paint(green, "✓ ")+i18n.Translate(format)+"\n", args...)
}

// Warnf prints a warning.
func Warnf(format string, args ...any) {
	printf(os.Stderr, paint(yellow, "! ")+i18n.Translate(format)+"\n", args...)
}

// Errorf prints an error.
func Errorf(format string, args ...any) {
	printf(os.Stderr, paint(red, "✗ ")+i18n.Translate(format)+"\n", args...)
}

// Debugf prints only when verbose is enabled.
func Debugf(verbose bool, format string, args ...any) {
	if verbose {
		printf(os.Stdout, paint(dim, "· ")+i18n.Translate(format)+"\n", args...)
	}
}

// Stepf prints a numbered step header.
func Stepf(n, total int, format string, args ...any) {
	prefix := paint(bold, fmt.Sprintf("[%d/%d]", n, total))
	printf(os.Stdout, "%s %s\n", prefix, fmt.Sprintf(i18n.Translate(format), args...))
}

// Header prints a section title.
func Header(format string, args ...any) {
	printf(os.Stdout, "\n%s\n", paint(bold, fmt.Sprintf(i18n.Translate(format), args...)))
}

// Item prints an aligned key/value bullet.
func Item(key, value string) {
	printf(os.Stdout, "  %s %-22s %s\n", paint(cyan, "•"), i18n.Translate(key), i18n.Translate(value))
}

// Raw prints text without decoration.
func Raw(format string, args ...any) {
	printf(os.Stdout, i18n.Translate(format)+"\n", args...)
}

// Command prints a command that is about to be executed.
func Command(dryRun bool, cmdline string) {
	prefix := paint(dim, "  $ ")
	if dryRun {
		prefix = paint(yellow, "  [dry-run] $ ")
	}
	printf(os.Stdout, "%s%s\n", prefix, cmdline)
}

// Banner prints the application banner.
func Banner(version string) {
	art := `        __  __          _        ____            
       |  \/  |        | |      |  _ \           
       | \  / |  __ _  | | ___  | |_) |  _____  __
       | |\/| | / _  | | |/ _ \ |  _ <  / _ \ \/ /
       | |  | || (_| | | |  __/ | |_) || (_) >  < 
       |_|  |_| \__,_| |_|\___| |____/  \___/_/\_\`
	if !noColor {
		fmt.Fprintln(os.Stdout, paint(cyan, art))
	} else {
		fmt.Fprintln(os.Stdout, art)
	}
	fmt.Fprintf(os.Stdout, "\n  makebox %s — Docker & Kubernetes one-command bootstrap for Linux\n\n", version)
}

// IsTerminal reports whether f is attached to a character device.
func IsTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// JoinList renders a short bullet list on one line.
func JoinList(items []string) string {
	return strings.Join(items, ", ")
}
