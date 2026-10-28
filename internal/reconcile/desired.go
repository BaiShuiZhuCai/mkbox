package reconcile

import (
	"fmt"
	"strings"

	"github.com/makebox/makebox/internal/containerd"
	"github.com/makebox/makebox/internal/docker"
)

// DesiredDaemonJSON merges the desired Docker settings into the current
// daemon.json, preserving unknown keys.
func DesiredDaemonJSON(current string, p DockerProfile) (string, error) {
	var cfg *docker.DaemonConfig
	var err error
	if strings.TrimSpace(current) == "" {
		cfg = docker.NewDaemonConfig()
	} else {
		cfg, err = docker.ReadDaemonFromBytes([]byte(current))
		if err != nil {
			return "", err
		}
	}
	if err := cfg.SetMirrors(p.Mirrors); err != nil {
		return "", err
	}
	cfg.SetLogging(p.LogDriver, p.LogMaxSize, p.LogMaxFile)
	if p.DataRoot != "" {
		cfg.SetDataRoot(p.DataRoot)
	}
	data, err := cfg.Marshal()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DesiredDockerProxy renders the systemd drop-in for Docker's daemon proxy.
func DesiredDockerProxy(p ProxyProfile) string {
	if strings.TrimSpace(p.URL) == "" {
		return ""
	}
	return docker.SystemdProxyEnv(p.URL, p.NoProxy)
}

// DesiredContainerdConfig applies the Kubernetes-friendly containerd settings
// to an existing config.
func DesiredContainerdConfig(current string, p K8sProfile) (string, error) {
	if strings.TrimSpace(current) == "" {
		return "", nil
	}
	return containerd.EditConfig(current, p.ImageRepository)
}

// DesiredContainerdHosts renders the Docker Hub mirror hosts.toml.
func DesiredContainerdHosts(p DockerProfile) string {
	return containerd.HostsTOML("https://docker.io", p.Mirrors)
}

// RedactProxyText removes password material from proxy URLs before rendering.
func RedactProxyText(s string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		b.WriteString(redactLine(line))
	}
	return b.String()
}

func redactLine(line string) string {
	// Proxy URLs in systemd drop-ins look like Environment="HTTP_PROXY=http://u:p@host".
	start := strings.Index(line, "://")
	if start < 0 {
		return line
	}
	rest := line[start+3:]
	at := strings.Index(rest, "@")
	slash := strings.IndexAny(rest, "/\"' \t")
	if at <= 0 || (slash >= 0 && slash < at) {
		return line
	}
	cred := rest[:at]
	if !strings.Contains(cred, ":") {
		return line
	}
	user := strings.SplitN(cred, ":", 2)[0]
	return line[:start+3] + user + ":REDACTED@" + rest[at+1:]
}

// DiffUnified renders a compact unified diff. It is intentionally bounded so
// a pathological config cannot exhaust memory.
func DiffUnified(path, oldText, newText string) string {
	const maxLines = 4000
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)
	if len(oldLines) > maxLines || len(newLines) > maxLines {
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ 文件过大，省略逐行 diff @@\n", path, path)
	}
	if oldText == newText {
		return ""
	}
	ops := diffOps(oldLines, newLines)
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n", path)
	fmt.Fprintf(&b, "+++ b/%s\n", path)
	for _, op := range ops {
		switch op.kind {
		case ' ':
			b.WriteString(" " + op.line + "\n")
		case '-':
			b.WriteString("-" + op.line + "\n")
		case '+':
			b.WriteString("+" + op.line + "\n")
		}
	}
	return b.String()
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

type diffOp struct {
	kind byte
	line string
}

// diffOps computes a line-based LCS diff. Config files are small; the
// quadratic table is bounded by maxLines above.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}
