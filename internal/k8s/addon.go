package k8s

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
)

// Addon describes a Kubernetes addon manifest.
type Addon struct {
	Name         string
	Description  string
	URL          string
	Namespace    string
	Workload     string
	PatchMetrics bool
}

var addonDefs = []Addon{
	{
		Name:        "flannel",
		Description: "轻量级 CNI（默认，podSubnet 10.244.0.0/16）",
		URL:         "https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml",
		Namespace:   "kube-flannel",
		Workload:    "daemonset/kube-flannel-ds",
	},
	{
		Name:        "calico",
		Description: "功能完整的 CNI（网络策略、BGP）",
		URL:         "https://raw.githubusercontent.com/projectcalico/calico/v3.29.0/manifests/calico.yaml",
		Namespace:   "kube-system",
		Workload:    "daemonset/calico-node",
	},
	{
		Name:         "metrics-server",
		Description:  "资源指标服务，供 HPA 与 kubectl top 使用",
		URL:          "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml",
		Namespace:    "kube-system",
		Workload:     "deployment/metrics-server",
		PatchMetrics: true,
	},
	{
		Name:        "ingress-nginx",
		Description: "NGINX Ingress Controller（baremetal/NodePort）",
		URL:         "https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.11.3/deploy/static/provider/baremetal/deploy.yaml",
		Namespace:   "ingress-nginx",
		Workload:    "deployment/ingress-nginx-controller",
	},
	{
		Name:        "dashboard",
		Description: "Kubernetes 官方 Web 控制台",
		URL:         "https://raw.githubusercontent.com/kubernetes/dashboard/v2.7.0/aio/deploy/recommended.yaml",
		Namespace:   "kubernetes-dashboard",
		Workload:    "deployment/kubernetes-dashboard",
	},
	{
		Name:        "local-path",
		Description: "本地路径动态存储（Rancher local-path-provisioner）",
		URL:         "https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.30/deploy/local-path-storage.yaml",
		Namespace:   "local-path-storage",
		Workload:    "deployment/local-path-provisioner",
	},
}

// Addons returns the supported addon definitions.
func Addons() []Addon { return append([]Addon(nil), addonDefs...) }

// FindAddon looks up an addon by name.
func FindAddon(name string) (Addon, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, a := range addonDefs {
		if a.Name == name {
			return a, true
		}
	}
	return Addon{}, false
}

// AddonInstaller applies addon manifests to a cluster.
type AddonInstaller struct {
	R          *execx.Runner
	Kubeconfig string
	GHProxy    string
	PodCIDR    string
	Timeout    time.Duration
}

// NewAddonInstaller creates an addon installer.
func NewAddonInstaller(r *execx.Runner, kubeconfig, ghProxy string) *AddonInstaller {
	return &AddonInstaller{R: r, Kubeconfig: kubeconfig, GHProxy: ghProxy, Timeout: 3 * time.Minute}
}

// Install downloads and applies a supported addon.
func (a *AddonInstaller) Install(name string) error {
	def, ok := FindAddon(name)
	if !ok {
		return fmt.Errorf("未知插件 %q，可用插件: %s", name, addonNames())
	}
	return a.InstallSource(def.Name, def.URL)
}

// InstallSource applies a manifest from a URL or local file.
func (a *AddonInstaller) InstallSource(name, source string) error {
	if !execx.Has("kubectl") && !a.R.DryRun {
		return fmt.Errorf("未找到 kubectl")
	}
	def, known := FindAddon(name)
	display := name
	if known {
		display = def.Name
	}
	logx.Header("安装插件: %s", display)

	localPath, isLocal := localManifestPath(source)
	var manifest string
	if isLocal {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return fmt.Errorf("读取清单 %s: %w", localPath, err)
		}
		manifest = string(data)
		if strings.TrimSpace(manifest) == "" {
			return fmt.Errorf("清单 %s 为空", localPath)
		}
		if known {
			manifest, err = a.patch(def, manifest)
			if err != nil {
				return err
			}
		}
	} else if err := validateRemoteSource(source); err != nil {
		return err
	}

	if a.R.DryRun {
		if isLocal {
			logx.Infof("[dry-run] 读取本地清单 %s（%d 字节）并通过 kubectl apply 应用", localPath, len(manifest))
		} else {
			logx.Infof("[dry-run] 下载清单 %s 并通过 kubectl apply 应用", strings.TrimSpace(source))
		}
		if known && def.PatchMetrics {
			logx.Infof("[dry-run] 将为 metrics-server 添加 --kubelet-insecure-tls")
		}
		if !execx.Has("kubectl") {
			logx.Warnf("未找到 kubectl，实际执行时会失败")
		}
		return nil
	}

	if !isLocal {
		var err error
		manifest, err = a.fetch(source)
		if err != nil {
			return err
		}
		if known {
			manifest, err = a.patch(def, manifest)
			if err != nil {
				return err
			}
		}
	}
	args := []string{"apply", "-f", "-"}
	if a.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", a.Kubeconfig}, args...)
	}
	if err := a.R.RunInput(manifest, "kubectl", args...); err != nil {
		return fmt.Errorf("应用 %s 清单失败: %w", display, err)
	}
	logx.Successf("%s 清单已应用", display)

	if known && def.Workload != "" {
		if err := a.waitReady(def); err != nil {
			return fmt.Errorf("%s 已应用但尚未就绪: %w", display, err)
		}
		logx.Successf("%s 已就绪", display)
	}
	return nil
}

func (a *AddonInstaller) patch(def Addon, manifest string) (string, error) {
	if def.PatchMetrics {
		manifest = PatchMetricsServer(manifest)
	}
	if def.Name == "flannel" && a.PodCIDR != "" {
		patched, ok := PatchFlannelCIDR(manifest, a.PodCIDR)
		if !ok && a.PodCIDR != "10.244.0.0/16" {
			return "", fmt.Errorf("无法在 flannel 清单中设置 podCIDR=%s，请参考文档手动修改 Network", a.PodCIDR)
		}
		manifest = patched
	}
	if def.Name == "calico" && a.PodCIDR != "" && a.PodCIDR != "192.168.0.0/16" {
		patched, ok := PatchCalicoCIDR(manifest, a.PodCIDR)
		if !ok {
			return "", fmt.Errorf("无法在 Calico 清单中设置 podCIDR=%s，请参考文档手动修改 CALICO_IPV4POOL_CIDR", a.PodCIDR)
		}
		manifest = patched
	}
	return manifest, nil
}

func localManifestPath(source string) (string, bool) {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "file://") {
		return strings.TrimPrefix(source, "file://"), true
	}
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return source, true
	}
	if st, err := os.Stat(source); err == nil && !st.IsDir() {
		return source, true
	}
	return "", false
}

func validateRemoteSource(source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("清单地址为空")
	}
	raw := source
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("无效的清单地址 %q: %w", source, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("清单地址 %q 必须使用 http 或 https", source)
	}
	if u.Host == "" {
		return fmt.Errorf("清单地址 %q 缺少主机名", source)
	}
	return nil
}

func (a *AddonInstaller) fetch(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("清单地址为空")
	}
	if !strings.Contains(source, "://") {
		if st, err := os.Stat(source); err == nil && !st.IsDir() {
			data, err := os.ReadFile(filepath.Clean(source))
			if err != nil {
				return "", err
			}
			return string(data), nil
		}
		source = "https://" + source
	}
	if strings.HasPrefix(source, "file://") {
		data, err := os.ReadFile(strings.TrimPrefix(source, "file://"))
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	manifest, err := a.download(source)
	if err == nil {
		return manifest, nil
	}
	if a.GHProxy != "" && isGitHubURL(source) {
		proxied := strings.TrimRight(a.GHProxy, "/") + "/" + source
		logx.Warnf("直连下载失败，尝试 GitHub 代理: %s", proxied)
		if m, perr := a.download(proxied); perr == nil {
			return m, nil
		}
	}
	return "", fmt.Errorf("下载清单 %s 失败: %w", source, err)
}

func isGitHubURL(raw string) bool {
	return strings.Contains(raw, "github.com/") || strings.Contains(raw, "raw.githubusercontent.com/")
}

func (a *AddonInstaller) download(url string) (string, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "makebox/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a *AddonInstaller) waitReady(def Addon) error {
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	args := []string{"rollout", "status", def.Workload, "-n", def.Namespace,
		fmt.Sprintf("--timeout=%ds", int(timeout.Seconds()))}
	if a.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", a.Kubeconfig}, args...)
	}
	if err := a.R.Run("kubectl", args...); err != nil {
		return err
	}
	return nil
}

// PatchMetricsServer adds --kubelet-insecure-tls for self-signed kubelets.
func PatchMetricsServer(manifest string) string {
	if strings.Contains(manifest, "--kubelet-insecure-tls") {
		return manifest
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^(\s*)-\s+--kubelet-preferred-address-types=.*$`),
		regexp.MustCompile(`(?m)^(\s*)-\s+--cert-dir=.*$`),
	}
	for _, re := range patterns {
		if re.MatchString(manifest) {
			return re.ReplaceAllString(manifest, "$1- --kubelet-insecure-tls\n$0")
		}
	}
	return manifest
}

// PatchFlannelCIDR replaces the Network value in flannel's net-conf.json.
func PatchFlannelCIDR(manifest, podCIDR string) (string, bool) {
	re := regexp.MustCompile(`(?m)("Network"\s*:\s*")[^"]*(")`)
	if !re.MatchString(manifest) {
		return manifest, false
	}
	return re.ReplaceAllString(manifest, "${1}"+podCIDR+"${2}"), true
}

// PatchCalicoCIDR sets CALICO_IPV4POOL_CIDR in a Calico manifest.
func PatchCalicoCIDR(manifest, podCIDR string) (string, bool) {
	commented := regexp.MustCompile(`(?m)^(\s*)#\s*-\s*name:\s*CALICO_IPV4POOL_CIDR\s*\n(\s*)#\s*value:\s*"[^"]*"`)
	if commented.MatchString(manifest) {
		replacement := fmt.Sprintf("${1}- name: CALICO_IPV4POOL_CIDR\n${1}  value: %q", podCIDR)
		return commented.ReplaceAllString(manifest, replacement), true
	}
	active := regexp.MustCompile(`(?m)^(\s*)-\s*name:\s*CALICO_IPV4POOL_CIDR\s*\n(\s*)value:\s*"[^"]*"`)
	if active.MatchString(manifest) {
		replacement := fmt.Sprintf("${1}- name: CALICO_IPV4POOL_CIDR\n${1}  value: %q", podCIDR)
		return active.ReplaceAllString(manifest, replacement), true
	}
	if podCIDR == "192.168.0.0/16" {
		return manifest, true
	}
	return manifest, false
}

func addonNames() string {
	names := make([]string, 0, len(addonDefs))
	for _, a := range addonDefs {
		names = append(names, a.Name)
	}
	return strings.Join(names, ", ")
}
