package docker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/registry"
	"github.com/makebox/makebox/internal/system"
)

const (
	// APTKeyringDir is the apt keyring directory managed by mkbox.
	APTKeyringDir = "/etc/apt/keyrings"
	// DropInDir holds systemd drop-ins for Docker.
	DropInDir = "/etc/systemd/system/docker.service.d"
	// ProxyConf is the systemd drop-in configuring Docker daemon proxies.
	ProxyConf = DropInDir + "/http-proxy.conf"
)

// DaemonConfig represents /etc/docker/daemon.json while preserving unknown keys.
type DaemonConfig struct {
	values map[string]any
}

// NewDaemonConfig returns an empty daemon configuration.
func NewDaemonConfig() *DaemonConfig {
	return &DaemonConfig{values: map[string]any{}}
}

// ReadDaemon parses daemon.json. A missing or empty file yields an empty config.
func ReadDaemon(path string) (*DaemonConfig, error) {
	if path == "" {
		path = DaemonJSON
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewDaemonConfig(), nil
		}
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	return ReadDaemonFromBytes(data)
}

// ReadDaemonFromBytes parses daemon.json content without reading the host.
func ReadDaemonFromBytes(data []byte) (*DaemonConfig, error) {
	if strings.TrimSpace(string(data)) == "" {
		return NewDaemonConfig(), nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析 daemon.json 失败: %w（请先修复或备份后删除该文件）", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return &DaemonConfig{values: m}, nil
}

// Set sets a key.
func (d *DaemonConfig) Set(key string, value any) { d.values[key] = value }

// Get returns a key value.
func (d *DaemonConfig) Get(key string) (any, bool) {
	v, ok := d.values[key]
	return v, ok
}

// Mirrors returns the configured registry mirrors.
func (d *DaemonConfig) Mirrors() []string {
	raw, ok := d.values["registry-mirrors"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// SetMirrors sets registry mirrors, normalising and de-duplicating entries.
func (d *DaemonConfig) SetMirrors(mirrors []string) error {
	normalized := registry.Dedupe(mirrors)
	if len(normalized) == 0 {
		delete(d.values, "registry-mirrors")
		return nil
	}
	d.values["registry-mirrors"] = normalized
	return nil
}

// SetLogging configures log rotation.
func (d *DaemonConfig) SetLogging(driver, maxSize, maxFile string) {
	if driver != "" {
		d.values["log-driver"] = driver
	}
	opts := map[string]any{}
	if existing, ok := d.values["log-opts"].(map[string]any); ok {
		for k, v := range existing {
			opts[k] = v
		}
	}
	if maxSize != "" {
		opts["max-size"] = maxSize
	}
	if maxFile != "" {
		opts["max-file"] = maxFile
	}
	if len(opts) > 0 {
		d.values["log-opts"] = opts
	}
}

// SetDataRoot sets Docker's data-root.
func (d *DaemonConfig) SetDataRoot(path string) {
	if path == "" {
		delete(d.values, "data-root")
		return
	}
	d.values["data-root"] = path
}

// Marshal renders indented JSON with deterministic key order.
func (d *DaemonConfig) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(d.values, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// SortedKeys returns top-level keys in sorted order (for display).
func (d *DaemonConfig) SortedKeys() []string {
	keys := make([]string, 0, len(d.values))
	for k := range d.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// JSON renders the full configuration for display.
func (d *DaemonConfig) JSON() string {
	data, err := json.MarshalIndent(d.values, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// Manager writes Docker configuration and reloads services.
type Manager struct {
	R    *execx.Runner
	FS   *system.FS
	OS   *system.OS
	Path string
}

// NewManager creates a Docker configuration manager.
func NewManager(r *execx.Runner, fs *system.FS, osInfo *system.OS) *Manager {
	return &Manager{R: r, FS: fs, OS: osInfo, Path: DaemonJSON}
}

// Load reads the daemon configuration.
func (m *Manager) Load() (*DaemonConfig, error) {
	return ReadDaemon(m.Path)
}

// Save writes the daemon configuration.
func (m *Manager) Save(cfg *DaemonConfig) error {
	data, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := m.FS.Write(m.Path, data, 0o644); err != nil {
		return err
	}
	logx.Successf("已写入 %s", m.Path)
	return nil
}

// ApplyMirrors updates registry-mirrors in daemon.json and restarts Docker.
func (m *Manager) ApplyMirrors(mirrors []string) error {
	cfg, err := m.Load()
	if err != nil {
		return err
	}
	if err := cfg.SetMirrors(mirrors); err != nil {
		return err
	}
	if err := m.Save(cfg); err != nil {
		return err
	}
	return m.Restart()
}

// ApplyLogging updates log rotation settings.
func (m *Manager) ApplyLogging(driver, maxSize, maxFile string) error {
	cfg, err := m.Load()
	if err != nil {
		return err
	}
	cfg.SetLogging(driver, maxSize, maxFile)
	return m.Save(cfg)
}

// ValidateDataRoot checks that a Docker data-root is absolute and not /.
func ValidateDataRoot(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || path == "/" {
		return "", fmt.Errorf("Docker 数据目录必须是绝对路径且不能是 /: %s", path)
	}
	return path, nil
}

// ApplyDataRoot sets data-root in daemon.json.
func (m *Manager) ApplyDataRoot(path string) error {
	clean, err := ValidateDataRoot(path)
	if err != nil {
		return err
	}
	path = clean
	cfg, err := m.Load()
	if err != nil {
		return err
	}
	cfg.SetDataRoot(path)
	if err := m.Save(cfg); err != nil {
		return err
	}
	if path != "" {
		logx.Warnf("Docker 数据目录已切换为 %s，但原有镜像不会自动迁移", path)
		logx.Warnf("如需迁移，请先停止 Docker 并手动同步 /var/lib/docker 到新目录")
	}
	return nil
}

// Restart reloads systemd and restarts Docker.
func (m *Manager) Restart() error {
	if !m.OS.HasSystemd() {
		if m.R.DryRun {
			logx.Infof("[dry-run] 未检测到 systemd，跳过重启 Docker")
			return nil
		}
		return fmt.Errorf("未检测到 systemd，请手动重启 Docker 使配置生效")
	}
	if err := m.R.Run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := m.R.Run("systemctl", "restart", "docker"); err != nil {
		return fmt.Errorf("重启 Docker 失败，请执行 journalctl -u docker 查看原因: %w", err)
	}
	if !m.R.DryRun {
		if _, err := m.R.Output("docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
			return fmt.Errorf("Docker 重启后未就绪，请执行 journalctl -u docker 查看原因: %w", err)
		}
	}
	logx.Successf("Docker 配置已生效")
	return nil
}

// SetProxy configures HTTP(S)/NO_PROXY for the Docker daemon via a systemd drop-in.
func (m *Manager) SetProxy(proxyURL string, noProxy []string) error {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return fmt.Errorf("代理地址不能为空")
	}
	if !m.OS.HasSystemd() && !m.R.DryRun {
		return fmt.Errorf("Docker 代理配置依赖 systemd drop-in，当前系统不支持")
	}
	content := SystemdProxyEnv(proxyURL, noProxy)
	if err := m.FS.Write(ProxyConf, []byte(content), 0o644); err != nil {
		return err
	}
	return m.Restart()
}

// ClearProxy removes the Docker proxy drop-in and restarts Docker.
func (m *Manager) ClearProxy() error {
	if err := m.FS.Remove(ProxyConf); err != nil {
		return err
	}
	if m.OS.HasSystemd() {
		if err := m.R.Run("systemctl", "daemon-reload"); err != nil {
			return err
		}
		if err := m.R.Run("systemctl", "restart", "docker"); err != nil {
			return err
		}
	}
	logx.Successf("已移除 Docker 代理配置")
	return nil
}

// ProxyStatus returns the current systemd proxy drop-in, if any.
func (m *Manager) ProxyStatus() (string, bool) {
	data, err := os.ReadFile(ProxyConf)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// SystemdProxyEnv renders a systemd drop-in with upper and lower case proxy
// variables so that both Go and libcurl based clients honour it.
func SystemdProxyEnv(proxyURL string, noProxy []string) string {
	nps := CleanNoProxy(noProxy)
	var b strings.Builder
	b.WriteString("# Managed by makebox (mkbox). Re-run `mkbox docker proxy set` to update.\n")
	b.WriteString("[Service]\n")
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		fmt.Fprintf(&b, "Environment=%q\n", key+"="+proxyURL)
	}
	if len(nps) > 0 {
		joined := strings.Join(nps, ",")
		for _, key := range []string{"NO_PROXY", "no_proxy"} {
			fmt.Fprintf(&b, "Environment=%q\n", key+"="+joined)
		}
	}
	return b.String()
}

// CleanNoProxy splits, trims and de-duplicates a no-proxy list.
func CleanNoProxy(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, group := range in {
		for _, item := range strings.Split(group, ",") {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}
