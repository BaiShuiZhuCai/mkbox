// Package containerd configures containerd for Kubernetes and registry mirrors.
package containerd

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
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
	// systemdCgroupLine is the runc option Kubernetes requires.
	systemdCgroupLine = "SystemdCgroup = true"
	// defaultPauseVersion is used when config.toml has no sandbox image yet.
	defaultPauseVersion = "3.10"
)

var (
	// sandboxRe matches both the double- and single-quoted TOML styles that
	// containerd 1.x and 2.x generate for sandbox_image.
	sandboxRe = regexp.MustCompile(`(?m)^(\s*)sandbox_image\s*=\s*(?:"[^"]*"|'[^']*')`)
	pauseRe   = regexp.MustCompile(`pause:([0-9][0-9A-Za-z.\-]*)`)
	versionRe = regexp.MustCompile(`v?(\d+)\.\d+`)
	// pluginEntryRe extracts the quoted items of a TOML string array.
	pluginEntryRe = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)
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
		updated, err := EditConfigForVersion(content, sandboxRepository, m.MajorVersion())
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

// MajorVersion reports the installed containerd major version, or 0 when it
// cannot be determined (missing binary, unparsable output or --dry-run).
func (m *Manager) MajorVersion() int {
	if m.R == nil || m.R.DryRun {
		return 0
	}
	out, err := m.R.Output("containerd", "--version")
	if err != nil {
		return 0
	}
	return ParseMajorVersion(out)
}

// criLayout lists the CRI plugin tables that differ between containerd
// generations. containerd 1.x keeps everything under io.containerd.grpc.v1.cri;
// containerd 2.x splits it into io.containerd.cri.v1.runtime and
// io.containerd.cri.v1.images.
type criLayout struct {
	sandbox        string // table that owns sandbox_image
	runtimeOptions string // table that owns the runc options (SystemdCgroup)
}

var (
	layoutGRPCv1 = criLayout{
		sandbox:        `[plugins."io.containerd.grpc.v1.cri"]`,
		runtimeOptions: `[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]`,
	}
	layoutCRIv1 = criLayout{
		sandbox:        `[plugins.'io.containerd.cri.v1.runtime']`,
		runtimeOptions: `[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]`,
	}
)

// ParseMajorVersion extracts the containerd major version from
// `containerd --version` output and returns 0 when no version is found.
func ParseMajorVersion(versionText string) int {
	match := versionRe.FindStringSubmatch(versionText)
	if match == nil {
		return 0
	}
	major, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return major
}

// selectLayout prefers the generation already present in config.toml so an
// existing file is never migrated, and falls back to the installed containerd
// major version (0 = unknown) when the file has no CRI plugin yet.
func selectLayout(content string, major int) criLayout {
	switch {
	case strings.Contains(content, "io.containerd.cri.v1.runtime"):
		return layoutCRIv1
	case strings.Contains(content, "io.containerd.grpc.v1.cri"):
		return layoutGRPCv1
	case major >= 2:
		return layoutCRIv1
	}
	return layoutGRPCv1
}

// tableKey normalises a TOML table header so that quote style and spacing do
// not matter when comparing it against a generated path. Array tables return
// an empty key because they never match a plain table.
func tableKey(header string) string {
	header = strings.TrimSpace(header)
	if strings.HasPrefix(header, "[[") || !strings.HasPrefix(header, "[") || !strings.HasSuffix(header, "]") {
		return ""
	}
	replacer := strings.NewReplacer(`"`, "", `'`, "", " ", "", "\t", "")
	return replacer.Replace(header[1 : len(header)-1])
}

// indentOf returns the leading whitespace of a line.
func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// sandboxImageLine renders the sandbox_image assignment for a repository.
func sandboxImageLine(repository, pauseVersion string) string {
	pause := "pause:" + pauseVersion
	if repo := strings.TrimRight(repository, "/"); repo != "" {
		return fmt.Sprintf(`sandbox_image = "%s/%s"`, repo, pause)
	}
	return fmt.Sprintf(`sandbox_image = "registry.k8s.io/%s"`, pause)
}

// splice inserts extra lines at the given "before" positions.
func splice(lines []string, before map[int][]string) []string {
	if len(before) == 0 {
		return lines
	}
	out := make([]string, 0, len(lines)+len(before)*2)
	for i := 0; i <= len(lines); i++ {
		out = append(out, before[i]...)
		if i < len(lines) {
			out = append(out, lines[i])
		}
	}
	return out
}

// disablesCRIEntry reports whether a disabled_plugins entry switches a CRI
// plugin off. Packages (deb/rpm and vendor images) commonly ship
// `disabled_plugins = ["cri"]`, and kubeadm then fails to create the CRI
// service because the socket has no runtime.v1.RuntimeService.
func disablesCRIEntry(entry string) bool {
	entry = strings.TrimSpace(entry)
	return entry == "cri" || entry == "io.containerd.grpc.v1.cri" ||
		strings.HasPrefix(entry, "io.containerd.cri.v1.")
}

// DisablesCRI reports whether config.toml keeps a CRI plugin in
// disabled_plugins, i.e. whether the socket cannot serve CRI clients.
func DisablesCRI(content string) bool {
	_, changed := EnableCRIPlugin(content)
	return changed
}

// EnableCRIPlugin drops CRI entries from the top-level disabled_plugins array
// so containerd serves the CRI API again. Unrelated disabled plugins are kept
// and content is returned unchanged when CRI is not disabled.
func EnableCRIPlugin(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			// disabled_plugins is a top-level key: stop at the first table.
			break
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "disabled_plugins"))
		if rest != trimmed && strings.HasPrefix(rest, "=") {
			start = i
			break
		}
	}
	if start < 0 {
		return content, false
	}
	// The array may span several lines up to the closing bracket.
	end := start
	for end < len(lines) && !strings.Contains(lines[end], "]") {
		end++
	}
	if end >= len(lines) {
		return content, false
	}

	kept := []string{}
	changed := false
	statement := strings.Join(lines[start:end+1], "\n")
	for _, match := range pluginEntryRe.FindAllStringSubmatch(statement, -1) {
		entry := match[1]
		if entry == "" {
			entry = match[2]
		}
		if disablesCRIEntry(entry) {
			changed = true
			continue
		}
		kept = append(kept, entry)
	}
	if !changed {
		return content, false
	}

	quoted := make([]string, len(kept))
	for i, entry := range kept {
		quoted[i] = strconv.Quote(entry)
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[:start]...)
	out = append(out, "disabled_plugins = ["+strings.Join(quoted, ", ")+"]")
	out = append(out, lines[end+1:]...)
	return strings.Join(out, "\n"), true
}

// EditConfig applies the required containerd settings to a config.toml.
// It is a pure function so it can be unit tested.
func EditConfig(content, sandboxRepository string) (string, error) {
	return EditConfigForVersion(content, sandboxRepository, 0)
}

// EditConfigForVersion is EditConfig with the containerd major version known.
// The version decides the CRI table layout when config.toml has no CRI plugin
// yet; pass 0 to infer the layout from the content only.
//
// Settings that are absent (no SystemdCgroup line, no runc options table at
// all) are appended instead of failing, because configs shipped by the
// containerd.io packages only carry `version` and no CRI plugin tables.
func EditConfigForVersion(content, sandboxRepository string, major int) (string, error) {
	if strings.TrimSpace(content) == "" {
		return content, nil
	}
	// A packaged `disabled_plugins = ["cri"]` keeps the plugin off no matter
	// which CRI tables the file carries, so drop it before anything else.
	content, _ = EnableCRIPlugin(content)
	layout := selectLayout(content, major)
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

	pauseVersion := defaultPauseVersion
	if match := pauseRe.FindStringSubmatch(content); len(match) > 1 {
		pauseVersion = match[1]
	}
	sandboxImage := sandboxImageLine(sandboxRepository, pauseVersion)

	sandboxKey := tableKey(layout.sandbox)
	optionsKey := tableKey(layout.runtimeOptions)

	foundCgroup, foundSandbox := false, false
	table := ""
	sandboxIdx, optionsIdx, firstCRIIdx := -1, -1, -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if key := tableKey(trimmed); key != "" {
			table = key
			if firstCRIIdx < 0 && (key == sandboxKey || strings.HasPrefix(key, sandboxKey+".")) {
				firstCRIIdx = i
			}
			if key == sandboxKey && sandboxIdx < 0 {
				sandboxIdx = i
			}
			if key == optionsKey && optionsIdx < 0 {
				optionsIdx = i
			}
			continue
		}
		if table == optionsKey && strings.HasPrefix(trimmed, "SystemdCgroup") {
			lines[i] = indentOf(line) + systemdCgroupLine
			foundCgroup = true
			continue
		}
		if table == sandboxKey && sandboxRe.MatchString(line) {
			lines[i] = indentOf(line) + sandboxImage
			foundSandbox = true
		}
	}

	// Existing tables take the missing keys; no header is duplicated.
	inserts := map[int][]string{}
	if !foundSandbox {
		switch {
		case sandboxIdx >= 0:
			inserts[sandboxIdx+1] = append(inserts[sandboxIdx+1], "  "+sandboxImage)
		case firstCRIIdx >= 0:
			inserts[firstCRIIdx] = append(inserts[firstCRIIdx], layout.sandbox, "  "+sandboxImage)
		}
	}
	if !foundCgroup && optionsIdx >= 0 {
		inserts[optionsIdx+1] = append(inserts[optionsIdx+1], "  "+systemdCgroupLine)
	}
	out := splice(lines, inserts)

	// Missing tables are appended parent-first: appending after the last line
	// keeps every existing key inside its own table.
	var tail []string
	addTail := func(block ...string) {
		if len(tail) > 0 {
			tail = append(tail, "")
		}
		tail = append(tail, block...)
	}
	if !foundSandbox && sandboxIdx < 0 && firstCRIIdx < 0 {
		addTail(layout.sandbox, "  "+sandboxImage)
	}
	if !foundCgroup && optionsIdx < 0 {
		addTail(layout.runtimeOptions, "  "+systemdCgroupLine)
	}
	if len(tail) > 0 {
		out = append(out, "")
		out = append(out, tail...)
	}

	return EnsureRegistryConfigPath(strings.Join(out, "\n")+"\n", CertsDir), nil
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
		// containerd 2.x moved the registry config next to the split CRI
		// plugins; any io.containerd.cri.v1.* table implies that layout.
		if strings.Contains(content, "io.containerd.cri.v1.") {
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

// HasCRIPlugin reports whether `ctr plugins ls` output lists a CRI plugin.
// containerd 1.x registers io.containerd.grpc.v1.cri, 2.x splits the CRI
// plugin into io.containerd.cri.v1.runtime and io.containerd.cri.v1.images.
func HasCRIPlugin(pluginsOutput string) bool {
	return strings.Contains(pluginsOutput, "io.containerd.cri.v1.runtime") ||
		strings.Contains(pluginsOutput, "io.containerd.grpc.v1.cri")
}

// VerifyCRI checks that the running containerd actually serves the CRI API.
// kubeadm fails with "unknown service runtime.v1.RuntimeService" when the
// plugin is disabled, so the check turns a late, cryptic failure into an
// actionable error. It is best-effort: without ctr or a reachable socket the
// check is skipped.
func (m *Manager) VerifyCRI() error {
	if m.R == nil || m.R.DryRun || !execx.Has("ctr") {
		return nil
	}
	out, err := m.R.Output("ctr", "plugins", "ls")
	if err != nil {
		logx.Warnf("无法读取 containerd 插件列表，跳过 CRI 校验: %v", err)
		return nil
	}
	if HasCRIPlugin(out) {
		return nil
	}
	return fmt.Errorf("containerd 已启动但未启用 CRI 插件，kubeadm 会报 unknown service runtime.v1.RuntimeService；请从 %s 的 disabled_plugins 中移除 cri 后重启 containerd", m.Path)
}

// Restart restarts containerd and verifies that the CRI plugin is served.
func (m *Manager) Restart() error {
	return m.restart(true)
}

func (m *Manager) restart(verifyCRI bool) error {
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
		if verifyCRI {
			if err := m.VerifyCRI(); err != nil {
				return err
			}
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
	// Proxy changes must not fail on an unrelated CRI problem.
	return m.restart(false)
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
