// Package config loads and validates mkbox's persistent configuration.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/makebox/makebox/internal/i18n"
	"github.com/makebox/makebox/internal/system"
)

// Config is the persisted mkbox configuration.
type Config struct {
	Version  int          `json:"version"`
	Language string       `json:"language"`
	Registry RegistryConf `json:"registry"`
	Docker   DockerConf   `json:"docker"`
	K8s      K8sConf      `json:"k8s"`
	Proxy    ProxyConf    `json:"proxy"`
	Mirror   MirrorConf   `json:"mirror"`
	Network  NetworkConf  `json:"network"`
}

// RegistryConf holds container registry mirror settings.
type RegistryConf struct {
	Mirrors        []string `json:"mirrors"`
	MaxMirrors     int      `json:"maxMirrors"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

// DockerConf holds Docker Engine settings.
type DockerConf struct {
	InstallMirror string `json:"installMirror"`
	LogDriver     string `json:"logDriver"`
	LogMaxSize    string `json:"logMaxSize"`
	LogMaxFile    string `json:"logMaxFile"`
	DataRoot      string `json:"dataRoot"`
}

// K8sConf holds Kubernetes bootstrap settings.
type K8sConf struct {
	Distro          string `json:"distro"`
	Version         string `json:"version"`
	ImageRepository string `json:"imageRepository"`
	RepoMirror      string `json:"repoMirror"`
	PodCIDR         string `json:"podCIDR"`
	ServiceCIDR     string `json:"serviceCIDR"`
	CNI             string `json:"cni"`
}

// ProxyConf holds system proxy settings.
type ProxyConf struct {
	HTTP    string   `json:"http"`
	HTTPS   string   `json:"https"`
	NoProxy []string `json:"noProxy"`
}

// MirrorConf holds OS and developer tool mirror settings.
type MirrorConf struct {
	OS string `json:"os"`
}

// NetworkConf holds network-related preferences.
type NetworkConf struct {
	GHProxy        string `json:"ghProxy"`
	CheckTimeoutMS int    `json:"checkTimeoutMs"`
}

// DefaultNoProxy is a safe no-proxy list covering loopback, private networks
// and Kubernetes service/pod ranges.
var DefaultNoProxy = []string{
	"localhost", "127.0.0.1", "::1",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10",
	".local", ".internal", ".svc", ".cluster.local",
	"10.96.0.0/12", "10.244.0.0/16",
}

// DefaultMirrors is the built-in list of Docker Hub mirrors. Availability of
// public mirrors changes frequently; use `mkbox registry test` to verify and
// prefer your own mirror in production.
var DefaultMirrors = []string{
	"https://docker.m.daocloud.io",
	"https://docker.1ms.run",
	"https://dockerproxy.net",
	"https://docker.1panel.live",
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Version:  1,
		Language: i18n.Chinese,
		Registry: RegistryConf{
			Mirrors:        append([]string(nil), DefaultMirrors...),
			MaxMirrors:     3,
			TimeoutSeconds: 4,
		},
		Docker: DockerConf{
			InstallMirror: "aliyun",
			LogDriver:     "json-file",
			LogMaxSize:    "100m",
			LogMaxFile:    "3",
		},
		K8s: K8sConf{
			Distro:          "kubeadm",
			Version:         "1.35",
			ImageRepository: "registry.aliyuncs.com/google_containers",
			RepoMirror:      "aliyun",
			PodCIDR:         "10.244.0.0/16",
			ServiceCIDR:     "10.96.0.0/12",
			CNI:             "flannel",
		},
		Proxy: ProxyConf{
			NoProxy: append([]string(nil), DefaultNoProxy...),
		},
		Mirror: MirrorConf{OS: "aliyun"},
		Network: NetworkConf{
			CheckTimeoutMS: 5000,
		},
	}
}

// Path returns the config file path, honouring MKBOX_CONFIG and XDG_CONFIG_HOME.
func Path() string {
	if p := os.Getenv("MKBOX_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(system.UserHome(), ".config")
	}
	return filepath.Join(base, "makebox", "config.json")
}

// Load reads the configuration from path. A missing file yields defaults.
func Load(path string) (*Config, error) {
	if path == "" {
		path = Path()
	}
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置 %s: %w", path, err)
		}
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置 %s 无效: %w", path, err)
	}
	return cfg, nil
}

// ApplyDefaults fills in empty fields with default values.
func (c *Config) ApplyDefaults() {
	def := Default()
	if c.Version == 0 {
		c.Version = def.Version
	}
	if c.Language == "" {
		c.Language = def.Language
	}
	if c.Registry.Mirrors == nil {
		c.Registry.Mirrors = def.Registry.Mirrors
	}
	if c.Registry.MaxMirrors <= 0 {
		c.Registry.MaxMirrors = def.Registry.MaxMirrors
	}
	if c.Registry.TimeoutSeconds <= 0 {
		c.Registry.TimeoutSeconds = def.Registry.TimeoutSeconds
	}
	if c.Docker.InstallMirror == "" {
		c.Docker.InstallMirror = def.Docker.InstallMirror
	}
	if c.Docker.LogDriver == "" {
		c.Docker.LogDriver = def.Docker.LogDriver
	}
	if c.Docker.LogMaxSize == "" {
		c.Docker.LogMaxSize = def.Docker.LogMaxSize
	}
	if c.Docker.LogMaxFile == "" {
		c.Docker.LogMaxFile = def.Docker.LogMaxFile
	}
	if c.K8s.Distro == "" {
		c.K8s.Distro = def.K8s.Distro
	}
	if c.K8s.Version == "" {
		c.K8s.Version = def.K8s.Version
	}
	if c.K8s.ImageRepository == "" {
		c.K8s.ImageRepository = def.K8s.ImageRepository
	}
	if c.K8s.RepoMirror == "" {
		c.K8s.RepoMirror = def.K8s.RepoMirror
	}
	if c.K8s.PodCIDR == "" {
		c.K8s.PodCIDR = def.K8s.PodCIDR
	}
	if c.K8s.ServiceCIDR == "" {
		c.K8s.ServiceCIDR = def.K8s.ServiceCIDR
	}
	if c.K8s.CNI == "" {
		c.K8s.CNI = def.K8s.CNI
	}
	if c.Mirror.OS == "" {
		c.Mirror.OS = def.Mirror.OS
	}
	if c.Network.CheckTimeoutMS <= 0 {
		c.Network.CheckTimeoutMS = def.Network.CheckTimeoutMS
	}
}

// Save writes the configuration atomically with restrictive permissions.
// Marshal validates and renders the configuration as indented JSON.
func (c *Config) Marshal() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Save writes the configuration atomically with 0600 permissions.
func (c *Config) Save(path string) error {
	if path == "" {
		path = Path()
	}
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

var versionRe = regexp.MustCompile(`^v?1\.\d+(\.\d+)?(\+k3s\d+)?$`)

// Validate checks all configuration values.
func (c *Config) Validate() error {
	if _, ok := i18n.Normalize(c.Language); !ok {
		return fmt.Errorf("language 必须是 zh-CN 或 en")
	}
	for _, m := range c.Registry.Mirrors {
		if err := ValidateURL(m); err != nil {
			return fmt.Errorf("registry.mirrors: %w", err)
		}
	}
	if c.Registry.MaxMirrors <= 0 || c.Registry.MaxMirrors > 10 {
		return fmt.Errorf("registry.maxMirrors 必须在 1-10 之间")
	}
	if c.Registry.TimeoutSeconds <= 0 || c.Registry.TimeoutSeconds > 60 {
		return fmt.Errorf("registry.timeoutSeconds 必须在 1-60 之间")
	}
	switch c.Docker.InstallMirror {
	case "aliyun", "tuna", "ustc", "huawei", "official":
	default:
		return fmt.Errorf("docker.installMirror 必须是 aliyun/tuna/ustc/huawei/official 之一")
	}
	if c.Docker.DataRoot != "" {
		clean := filepath.Clean(c.Docker.DataRoot)
		if !filepath.IsAbs(clean) || clean == "/" {
			return fmt.Errorf("docker.dataRoot 必须是绝对路径且不能是 /")
		}
	}
	switch c.K8s.Distro {
	case "kubeadm", "k3s":
	default:
		return fmt.Errorf("k8s.distro 必须是 kubeadm 或 k3s")
	}
	if !versionRe.MatchString(c.K8s.Version) {
		return fmt.Errorf("k8s.version 必须形如 1.35 或 v1.35.2")
	}
	switch c.K8s.CNI {
	case "flannel", "calico", "none":
	default:
		return fmt.Errorf("k8s.cni 必须是 flannel/calico/none 之一")
	}
	if c.K8s.ImageRepository == "" {
		return fmt.Errorf("k8s.imageRepository 不能为空")
	}
	switch c.K8s.RepoMirror {
	case "official", "aliyun":
	default:
		return fmt.Errorf("k8s.repoMirror 必须是 official 或 aliyun")
	}
	for _, cidr := range []struct{ name, val string }{
		{"k8s.podCIDR", c.K8s.PodCIDR},
		{"k8s.serviceCIDR", c.K8s.ServiceCIDR},
	} {
		if _, _, err := net.ParseCIDR(cidr.val); err != nil {
			return fmt.Errorf("%s 无效: %v", cidr.name, err)
		}
	}
	switch c.Mirror.OS {
	case "aliyun", "tuna", "ustc", "huawei":
	default:
		return fmt.Errorf("mirror.os 必须是 aliyun/tuna/ustc/huawei 之一")
	}
	if c.Proxy.HTTP != "" {
		if err := ValidateProxyURL(c.Proxy.HTTP); err != nil {
			return fmt.Errorf("proxy.http: %w", err)
		}
	}
	if c.Proxy.HTTPS != "" {
		if err := ValidateProxyURL(c.Proxy.HTTPS); err != nil {
			return fmt.Errorf("proxy.https: %w", err)
		}
	}
	if c.Network.GHProxy != "" {
		if err := ValidateURL(c.Network.GHProxy); err != nil {
			return fmt.Errorf("network.ghProxy: %w", err)
		}
	}
	return nil
}

// ValidateProxyURL accepts http, https, socks5 and socks5h proxy URLs.
func ValidateProxyURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("无效代理地址 %q: %v", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("%q 必须以 http://、https://、socks5:// 或 socks5h:// 开头", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%q 缺少主机名", raw)
	}
	return nil
}

// ValidateURL requires an absolute http(s) URL.
func ValidateURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("无效 URL %q: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%q 必须以 http:// 或 https:// 开头", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%q 缺少主机名", raw)
	}
	return nil
}

// EffectiveProxy returns the proxy URL to use for HTTPS (falling back to HTTP).
func (c *Config) EffectiveProxy() string {
	if c.Proxy.HTTPS != "" {
		return c.Proxy.HTTPS
	}
	return c.Proxy.HTTP
}
