package reconcile

import (
	"os"
	"strings"

	"github.com/makebox/makebox/internal/containerd"
	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/k8s"
	"github.com/makebox/makebox/internal/system"
)

// Observed is a read-only snapshot of the managed host.
type Observed struct {
	DockerInstalled       bool     `json:"dockerInstalled"`
	DockerRunning         bool     `json:"dockerRunning"`
	DockerVersion         string   `json:"dockerVersion,omitempty"`
	DockerMirrors         []string `json:"dockerMirrors,omitempty"`
	DockerDataRoot        string   `json:"dockerDataRoot,omitempty"`
	DockerDaemonValid     bool     `json:"dockerDaemonValid"`
	DockerProxyConfigured bool     `json:"dockerProxyConfigured"`

	ContainerdInstalled       bool   `json:"containerdInstalled"`
	ContainerdRunning         bool   `json:"containerdRunning"`
	ContainerdVersion         string `json:"containerdVersion,omitempty"`
	ContainerdSystemdCgroup   bool   `json:"containerdSystemdCgroup"`
	ContainerdRegistryConfig  string `json:"containerdRegistryConfig,omitempty"`
	ContainerdProxyConfigured bool   `json:"containerdProxyConfigured"`

	KubeletInstalled   bool   `json:"kubeletInstalled"`
	KubeletRunning     bool   `json:"kubeletRunning"`
	KubeadmInstalled   bool   `json:"kubeadmInstalled"`
	KubeadmVersion     string `json:"kubeadmVersion,omitempty"`
	K3sInstalled       bool   `json:"k3sInstalled"`
	K3sRunning         bool   `json:"k3sRunning"`
	ClusterInitialized bool   `json:"clusterInitialized"`
	ClusterKubeconfig  string `json:"clusterKubeconfig,omitempty"`
	SwapEnabled        bool   `json:"swapEnabled"`

	daemonJSON      string
	dockerProxy     string
	containerdCfg   string
	containerdHosts string
	containerdProxy string
}

// Observe inspects the host without mutating it. --dry-run does not suppress
// read-only probes; otherwise a plan would be empty by construction.
func Observe(r *execx.Runner, osInfo *system.OS) Observed {
	probe := execx.New(false, false)
	if r != nil {
		probe.Verbose = r.Verbose
	}
	o := Observed{}
	if osInfo != nil && osInfo.HasSystemd() {
		o.DockerRunning = active(probe, "docker")
		o.ContainerdRunning = active(probe, "containerd")
		o.KubeletRunning = active(probe, "kubelet")
		o.K3sRunning = active(probe, "k3s")
	}

	o.DockerInstalled = execx.Has("docker")
	if o.DockerInstalled {
		if out, err := probe.Output("docker", "--version"); err == nil {
			o.DockerVersion = strings.TrimSpace(strings.TrimPrefix(out, "Docker version "))
		}
		if !o.DockerRunning {
			if out, err := probe.Output("docker", "version", "--format", "{{.Server.Version}}"); err == nil && strings.TrimSpace(out) != "" {
				o.DockerRunning = true
			}
		}
		if out, err := probe.Output("docker", "info", "--format", "{{.DockerRootDir}}"); err == nil {
			o.DockerDataRoot = strings.TrimSpace(out)
		}
	}
	if data, err := os.ReadFile(docker.DaemonJSON); err == nil {
		o.daemonJSON = string(data)
		cfg, err := docker.ReadDaemon(docker.DaemonJSON)
		o.DockerDaemonValid = err == nil
		if err == nil {
			o.DockerMirrors = cfg.Mirrors()
		}
	} else if os.IsNotExist(err) {
		o.DockerDaemonValid = true
	}
	if data, err := os.ReadFile(docker.ProxyConf); err == nil {
		o.dockerProxy = string(data)
		o.DockerProxyConfigured = true
	}

	o.ContainerdInstalled = execx.Has("containerd")
	if o.ContainerdInstalled {
		if out, err := probe.Output("containerd", "--version"); err == nil {
			o.ContainerdVersion = strings.TrimSpace(out)
		}
		if data, err := os.ReadFile(containerd.ConfigPath); err == nil {
			o.containerdCfg = string(data)
			o.ContainerdSystemdCgroup, o.ContainerdRegistryConfig, _ = parseContainerd(string(data))
		}
		if data, err := os.ReadFile(containerd.CertsDir + "/docker.io/hosts.toml"); err == nil {
			o.containerdHosts = string(data)
		}
		if data, err := os.ReadFile(containerd.ProxyConf); err == nil {
			o.containerdProxy = string(data)
			o.ContainerdProxyConfigured = true
		}
	}

	o.KubeletInstalled = execx.Has("kubelet")
	o.KubeadmInstalled = execx.Has("kubeadm")
	if o.KubeadmInstalled {
		if out, err := probe.Output("kubeadm", "version", "-o", "short"); err == nil {
			o.KubeadmVersion = strings.TrimSpace(out)
		}
	}
	o.K3sInstalled = execx.Has("k3s") || fileExists("/etc/rancher/k3s/k3s.yaml")
	o.ClusterKubeconfig = k8s.DetectClusterKubeconfig()
	o.ClusterInitialized = o.ClusterKubeconfig != "" || fileExists(k8s.AdminKubeconfig) || fileExists("/etc/rancher/k3s/k3s.yaml")
	o.SwapEnabled = swapEnabled()
	return o
}

func active(r *execx.Runner, service string) bool {
	out, err := r.Output("systemctl", "is-active", service)
	return err == nil && strings.TrimSpace(out) == "active"
}

func swapEnabled() bool {
	data, err := os.ReadFile("/proc/swaps")
	if err != nil {
		return false
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n")) > 1
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func parseContainerd(content string) (systemdCgroup bool, configPath, sandbox string) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "SystemdCgroup") {
			_, val, ok := strings.Cut(trimmed, "=")
			if ok {
				systemdCgroup = strings.TrimSpace(val) == "true"
			}
		}
		if strings.HasPrefix(trimmed, "config_path") {
			if _, val, ok := strings.Cut(trimmed, "="); ok {
				configPath = strings.Trim(strings.TrimSpace(val), `"`)
			}
		}
		if strings.HasPrefix(trimmed, "sandbox_image") {
			if _, val, ok := strings.Cut(trimmed, "="); ok {
				sandbox = strings.Trim(strings.TrimSpace(val), `"`)
			}
		}
	}
	return
}
