// Package containerd configures containerd for Kubernetes and registry mirrors.
package containerd

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/registry"
	"github.com/makebox/makebox/internal/system"
)

const (
	// ConfigPath is containerd's main configuration file.
	ConfigPath = "/etc/containerd/config.toml"
	// CertsDir is the hosts.toml directory used by the CRI plugin.
	CertsDir = "/etc/containerd/certs.d"
	// DropInDir holds systemd drop-ins for containerd.
	DropInDir = "/etc/systemd/system/containerd.service.d"
	// ProxyConf configures proxies for containerd.
	ProxyConf = DropInDir + "/http-proxy.conf"
)

var (
	sandboxRe = regexp.MustCompile(`(?m)^(\s*)sandbox_image\s*=\s*"[^"]*"`)
	pauseRe   = regexp.MustCompile(`pause:([0-9][0-9A-Za-z.\-]*)`)
)

// Manager configures containerd.
type Manager struct {
	R        *execx.Runner
	FS       *system.FS
	OS       *system.OS
	Path     string
	CertsDir string
}

// NewManager creates a containerd manager.
func NewManager(r *execx.Runner, fs *system.FS, osInfo *system.OS) *Manager {
	return &Manager{R: r, FS: fs, OS: osInfo, Path: ConfigPath, CertsDir: CertsDir}
}

// EnsureConfig makes sure a config.toml exists, generating the default when missing.
func (m *Manager) EnsureConfig() (string, error) {
	if data, err := os.ReadFile(m.Path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		return string(data), nil
	}
	if m.R.DryRun {
		logx.Infof("[dry-run] 将执行 containerd config default > %s", m.Path)
		return "", nil
	}
	if !execx.Has("containerd") {
		return "", fmt.Errorf("未安装 containerd，请先运行: mkbox k8s install")
	}
	out, err := m.R.Output("containerd", "config", "default")
	if err != nil {
		return "", fmt.Errorf("生成默认 containerd 配置失败: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("containerd config default 输出为空")
	}
	if err := m.FS.Write(m.Path, []byte(out+"\n"), 0o644); err != nil {
		return "", err
	}
	return out, nil
}

// Configure applies Kubernetes-friendly settings and Docker Hub mirrors.
func (m *Manager) Configure(mirrors []string, sandboxRepository string) error {
	if !m.OS.IsRoot && !m.R.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if !execx.Has("containerd") && !m.R.DryRun {
		return fmt.Errorf("未安装 containerd，请先运行: mkbox k8s install")
	}
	content, err := m.EnsureConfig()
	if err != nil {
		return err
	}
	if content != "" {
		updated, err := EditConfig(content, sandboxRepository)
		if err != nil {
			return err
		}
		if updated != content {
			if err := m.FS.Write(m.Path, []byte(updated), 0o644); err != nil {
				return err
			}
			logx.Successf("已更新 %s（SystemdCgroup=true）", m.Path)
		}
	}

	if len(mirrors) > 0 {
		if err := m.FS.EnsureDir(m.CertsDir, 0o755); err != nil {
			return err
		}
		hostsPath := m.CertsDir + "/docker.io/hosts.toml"
		content := HostsTOML("https://docker.io", mirrors)
		if err := m.FS.Write(hostsPath, []byte(content), 0o644); err != nil {
			return err
		}
		logx.Successf("已配置 Docker Hub 镜像加速: %s", hostsPath)
	}
	return nil
}

// EditConfig applies the required containerd settings to a config.toml.
// It is a pure function so it can be unit tested.
func EditConfig(content, sandboxRepository string) (string, error) {
	lines := strings.Split(content, "\n")
	foundCgroup := false
	foundSandbox := false

	pauseVersion := "3.10"
	if match := pauseRe.FindStringSubmatch(content); len(match) > 1 {
		pauseVersion = match[1]
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "SystemdCgroup") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + "SystemdCgroup = true"
			foundCgroup = true
			continue
		}
		if sandboxRe.MatchString(line) {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			pause := "pause:" + pauseVersion
			if sandboxRepository != "" {
				lines[i] = fmt.Sprintf(`%ssandbox_image = "%s/%s"`, indent, strings.TrimRight(sandboxRepository, "/"), pause)
			} else {
				lines[i] = fmt.Sprintf(`%ssandbox_image = "registry.k8s.io/%s"`, indent, pause)
			}
			foundSandbox = true
		}
	}

	if !foundCgroup {
		return "", fmt.Errorf("未在 %s 中找到 SystemdCgroup 配置；请备份后删除该文件并重试，或手动设置 SystemdCgroup = true", ConfigPath)
	}
	if !foundSandbox {
		logx.Warnf("未找到 sandbox_image 配置，跳过 pause 镜像替换")
	}

	content = strings.Join(lines, "\n")
	content = EnsureRegistryConfigPath(content, "/etc/containerd/certs.d")
	return content, nil
}

// EnsureRegistryConfigPath sets the CRI registry config_path.
func EnsureRegistryConfigPath(content, path string) string {
	lines := strings.Split(content, "\n")
	inRegistry := false
	foundSection := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inRegistry = strings.HasSuffix(trimmed, ".registry]")
			if inRegistry {
				foundSection = true
			}
			continue
		}
		if inRegistry && strings.HasPrefix(trimmed, "config_path") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%sconfig_path = %q", indent, path)
			return strings.Join(lines, "\n")
		}
	}
	if !foundSection {
		section := `[plugins."io.containerd.grpc.v1.cri".registry]`
		if strings.Contains(content, "io.containerd.cri.v1.images") {
			section = `[plugins.'io.containerd.cri.v1.images'.registry]`
		}
		content = strings.TrimRight(content, "\n") + "\n\n" + section + "\n  config_path = \"" + path + "\"\n"
		return content
	}
	// Registry table exists but has no config_path key: append under it.
	out := make([]string, 0, len(lines))
	injected := false
	for _, line := range lines {
		out = append(out, line)
		if injected {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), ".registry]") {
			out = append(out, fmt.Sprintf(`  config_path = %q`, path))
			injected = true
		}
	}
	return strings.Join(out, "\n")
}

// HostsTOML renders a containerd hosts.toml for a registry with mirrors.
func HostsTOML(server string, mirrors []string) string {
	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox).\n")
	fmt.Fprintf(&b, "server = %q\n\n", server)
	for _, raw := range registry.Dedupe(mirrors) {
		fmt.Fprintf(&b, "[host.%q]\n", raw)
		b.WriteString("  capabilities = [\"pull\", \"resolve\"]\n\n")
	}
	return b.String()
}

// Restart restarts containerd.
func (m *Manager) Restart() error {
	if !m.OS.HasSystemd() {
		if m.R.DryRun {
			logx.Infof("[dry-run] 未检测到 systemd，跳过重启 containerd")
			return nil
		}
		return fmt.Errorf("未检测到 systemd，请手动重启 containerd")
	}
	if err := m.R.Run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := m.R.Run("systemctl", "restart", "containerd"); err != nil {
		return fmt.Errorf("重启 containerd 失败，请执行 journalctl -u containerd 查看原因: %w", err)
	}
	if !m.R.DryRun {
		if _, err := m.R.Output("systemctl", "is-active", "containerd"); err != nil {
			return fmt.Errorf("containerd 未处于运行状态: %w", err)
		}
	}
	logx.Successf("containerd 配置已生效")
	return nil
}

// SetProxy configures containerd's HTTP(S) proxy via systemd.
func (m *Manager) SetProxy(proxyURL string, noProxy []string) error {
	if !m.OS.HasSystemd() && !m.R.DryRun {
		return fmt.Errorf("containerd 代理配置依赖 systemd drop-in，当前系统不支持")
	}
	content := docker.SystemdProxyEnv(proxyURL, noProxy)
	if err := m.FS.Write(ProxyConf, []byte(content), 0o644); err != nil {
		return err
	}
	return m.Restart()
}

// ClearProxy removes the containerd proxy drop-in.
func (m *Manager) ClearProxy() error {
	if err := m.FS.Remove(ProxyConf); err != nil {
		return err
	}
	if m.OS.HasSystemd() {
		if err := m.R.Run("systemctl", "daemon-reload"); err != nil {
			return err
		}
		if err := m.R.Run("systemctl", "restart", "containerd"); err != nil {
			return err
		}
	}
	logx.Successf("已移除 containerd 代理配置")
	return nil
}

// IsInstalled reports whether containerd is available.
func IsInstalled() bool { return execx.Has("containerd") }
