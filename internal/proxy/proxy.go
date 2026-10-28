// Package proxy manages system-wide HTTP(S) proxy configuration.
package proxy

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

const (
	// EnvFile is the profile.d script exporting proxy variables.
	EnvFile = "/etc/profile.d/mkbox-proxy.sh"
	// EnvironmentFile is the system-wide environment file.
	EnvironmentFile = "/etc/environment"
	// AptConf configures apt proxies.
	AptConf = "/etc/apt/apt.conf.d/95mkbox-proxy"
)

var managedKeys = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
}

// Validate checks a proxy URL.
func Validate(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("代理地址不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("无效的代理地址: %v", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("代理协议仅支持 http/https/socks5，收到 %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("代理地址缺少主机名")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("代理地址不应包含路径")
	}
	return nil
}

// ParseScopes parses a comma separated scope list.
func ParseScopes(raw string) (map[string]bool, error) {
	allowed := map[string]bool{
		"system": true, "shell": true, "apt": true, "docker": true, "containerd": true,
	}
	out := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if item == "all" {
			for k := range allowed {
				out[k] = true
			}
			continue
		}
		if !allowed[item] {
			return nil, fmt.Errorf("未知 scope %q（可选 system,shell,apt,docker,containerd,all）", item)
		}
		out[item] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("scope 不能为空")
	}
	return out, nil
}

// Manager applies system/shell/apt proxy settings.
type Manager struct {
	R  *execx.Runner
	FS *system.FS
	OS *system.OS
}

// NewManager creates a proxy manager.
func NewManager(r *execx.Runner, fs *system.FS, osInfo *system.OS) *Manager {
	return &Manager{R: r, FS: fs, OS: osInfo}
}

// Apply writes the requested scopes. Docker/containerd scopes are ignored here
// and handled by their own managers.
func (m *Manager) Apply(proxyURL string, noProxy []string, scopes map[string]bool) error {
	if err := Validate(proxyURL); err != nil {
		return err
	}
	if !m.OS.IsRoot && !m.R.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	np := docker.CleanNoProxy(noProxy)

	if scopes["system"] {
		existing, _ := os.ReadFile(EnvironmentFile)
		updated := UpdateEnvironment(string(existing), proxyURL, np)
		if err := m.FS.Write(EnvironmentFile, []byte(updated), 0o644); err != nil {
			return err
		}
		logx.Successf("已更新 %s", EnvironmentFile)
	}
	if scopes["shell"] {
		if err := m.FS.Write(EnvFile, []byte(ShellEnv(proxyURL, np)), 0o644); err != nil {
			return err
		}
		logx.Successf("已写入 %s（重新登录后生效）", EnvFile)
	}
	if scopes["apt"] {
		if err := m.FS.Write(AptConf, []byte(AptConfContent(proxyURL)), 0o644); err != nil {
			return err
		}
		logx.Successf("已配置 apt 代理")
	}
	return nil
}

// Clear removes the requested system/shell/apt proxy settings.
func (m *Manager) Clear(scopes map[string]bool) error {
	if !m.OS.IsRoot && !m.R.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if scopes["system"] {
		existing, err := os.ReadFile(EnvironmentFile)
		if err == nil {
			updated := UpdateEnvironment(string(existing), "", nil)
			if err := m.FS.Write(EnvironmentFile, []byte(updated), 0o644); err != nil {
				return err
			}
		}
	}
	if scopes["shell"] {
		if err := m.FS.Remove(EnvFile); err != nil {
			return err
		}
	}
	if scopes["apt"] {
		if err := m.FS.Remove(AptConf); err != nil {
			return err
		}
	}
	logx.Successf("已清理代理配置")
	return nil
}

func isManagedKey(line string) bool {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "export ")
	for _, key := range managedKeys {
		if strings.HasPrefix(trimmed, key+"=") {
			return true
		}
	}
	return false
}

// UpdateEnvironment rewrites managed proxy keys in /etc/environment content.
// Passing an empty proxy removes the keys.
func UpdateEnvironment(content, proxyURL string, noProxy []string) string {
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines)+8)
	for _, line := range lines {
		if isManagedKey(line) {
			continue
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if proxyURL == "" {
		return strings.Join(kept, "\n") + "\n"
	}
	np := docker.CleanNoProxy(noProxy)
	kept = append(kept, "")
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY"} {
		kept = append(kept, fmt.Sprintf(`%s="%s"`, key, proxyURL))
	}
	if len(np) > 0 {
		kept = append(kept, fmt.Sprintf(`NO_PROXY="%s"`, strings.Join(np, ",")))
	}
	for _, key := range []string{"http_proxy", "https_proxy"} {
		kept = append(kept, fmt.Sprintf(`%s="%s"`, key, proxyURL))
	}
	if len(np) > 0 {
		kept = append(kept, fmt.Sprintf(`no_proxy="%s"`, strings.Join(np, ",")))
	}
	return strings.Join(kept, "\n") + "\n"
}

// ShellEnv renders a profile.d script with proxy exports.
func ShellEnv(proxyURL string, noProxy []string) string {
	np := docker.CleanNoProxy(noProxy)
	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox).\n")
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		fmt.Fprintf(&b, "export %s='%s'\n", key, proxyURL)
	}
	if len(np) > 0 {
		joined := strings.Join(np, ",")
		b.WriteString(fmt.Sprintf("export NO_PROXY='%s'\n", joined))
		b.WriteString(fmt.Sprintf("export no_proxy='%s'\n", joined))
	}
	return b.String()
}

// AptConfContent renders apt's proxy configuration.
func AptConfContent(proxyURL string) string {
	var b strings.Builder
	b.WriteString("// Managed by makebox (mkbox)\n")
	fmt.Fprintf(&b, "Acquire::http::Proxy %q;\n", proxyURL)
	fmt.Fprintf(&b, "Acquire::https::Proxy %q;\n", proxyURL)
	return b.String()
}
