// Package coexist inspects a Linux host and explains how Docker, containerd,
// cri-dockerd and Kubernetes can safely share the same machine.
package coexist

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/makebox/makebox/internal/containerd"
	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/k8s"
	"github.com/makebox/makebox/internal/system"
)

// Facts is the observed runtime state of a host.
type Facts struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Kernel        string `json:"kernel"`
	CgroupVersion int    `json:"cgroupVersion"`

	DockerInstalled       bool     `json:"dockerInstalled"`
	DockerRunning         bool     `json:"dockerRunning"`
	DockerVersion         string   `json:"dockerVersion,omitempty"`
	DockerCgroupDriver    string   `json:"dockerCgroupDriver,omitempty"`
	DockerDataRoot        string   `json:"dockerDataRoot,omitempty"`
	DockerMirrors         []string `json:"dockerMirrors,omitempty"`
	DockerProxyConfigured bool     `json:"dockerProxyConfigured"`

	ContainerdInstalled       bool   `json:"containerdInstalled"`
	ContainerdRunning         bool   `json:"containerdRunning"`
	ContainerdVersion         string `json:"containerdVersion,omitempty"`
	ContainerdConfigPath      string `json:"containerdConfigPath,omitempty"`
	ContainerdSystemdCgroup   bool   `json:"containerdSystemdCgroup"`
	ContainerdRegistryConfig  string `json:"containerdRegistryConfig,omitempty"`
	ContainerdSandboxImage    string `json:"containerdSandboxImage,omitempty"`
	ContainerdProxyConfigured bool   `json:"containerdProxyConfigured"`

	CriDockerdInstalled bool `json:"criDockerdInstalled"`
	CriDockerdRunning   bool `json:"criDockerdRunning"`
	CRIOInstalled       bool `json:"crioInstalled"`

	KubeletInstalled bool   `json:"kubeletInstalled"`
	KubeletRunning   bool   `json:"kubeletRunning"`
	KubeadmInstalled bool   `json:"kubeadmInstalled"`
	KubeadmVersion   string `json:"kubeadmVersion,omitempty"`
	K3sInstalled     bool   `json:"k3sInstalled"`
	K3sRunning       bool   `json:"k3sRunning"`

	ClusterKubeconfig string       `json:"clusterKubeconfig,omitempty"`
	SwapEnabled       bool         `json:"swapEnabled"`
	IptablesBackend   string       `json:"iptablesBackend,omitempty"`
	HTTPProxy         string       `json:"httpProxy,omitempty"`
	HTTPSProxy        string       `json:"httpsProxy,omitempty"`
	Ports             map[int]bool `json:"ports,omitempty"`

	CRISockets []string `json:"criSockets,omitempty"`
}

// Severity of a finding.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warn"
	SeverityError Severity = "error"
)

// Finding is a single coexistence observation.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Action   string   `json:"action,omitempty"`
}

// Plan explains the recommended runtime topology.
type Plan struct {
	Topology  string    `json:"topology"`
	Summary   string    `json:"summary"`
	Findings  []Finding `json:"findings,omitempty"`
	NextSteps []string  `json:"nextSteps,omitempty"`
	Facts     Facts     `json:"facts"`
}

// HasErrors reports whether any finding is blocking.
func (p Plan) HasErrors() bool {
	for _, f := range p.Findings {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

var (
	systemdCgroupRe = regexp.MustCompile(`(?m)^\s*SystemdCgroup\s*=\s*(true|false)\s*$`)
	configPathRe    = regexp.MustCompile(`(?m)^\s*config_path\s*=\s*"([^"]*)"\s*$`)
	sandboxRe       = regexp.MustCompile(`(?m)^\s*sandbox_image\s*=\s*"([^"]*)"\s*$`)
)

// ParseContainerdConfig extracts the settings that matter for coexistence.
func ParseContainerdConfig(content string) (systemdCgroup bool, configPath, sandbox string) {
	if m := systemdCgroupRe.FindStringSubmatch(content); len(m) > 1 {
		systemdCgroup = m[1] == "true"
	}
	if m := configPathRe.FindStringSubmatch(content); len(m) > 1 {
		configPath = m[1]
	}
	if m := sandboxRe.FindStringSubmatch(content); len(m) > 1 {
		sandbox = m[1]
	}
	return
}

// Detect inspects the host. It is read-only and therefore ignores --dry-run.
func Detect(r *execx.Runner, osInfo *system.OS) Facts {
	probe := execx.New(false, false)
	if r != nil {
		probe.Verbose = r.Verbose
	}
	f := Facts{
		Ports: map[int]bool{},
	}
	if osInfo != nil {
		f.OS = osInfo.DisplayName()
		f.Arch = osInfo.Arch
		f.Kernel = osInfo.Kernel
	}
	f.CgroupVersion = cgroupVersion()
	f.SwapEnabled = swapEnabled()
	f.IptablesBackend = iptablesBackend(probe)
	f.HTTPProxy = firstEnv("HTTP_PROXY", "http_proxy")
	f.HTTPSProxy = firstEnv("HTTPS_PROXY", "https_proxy")

	f.DockerInstalled = execx.Has("docker")
	if f.DockerInstalled {
		f.DockerVersion = strings.TrimSpace(mustOutput(probe, "docker", "--version"))
		f.DockerVersion = strings.TrimPrefix(f.DockerVersion, "Docker version ")
		if out, err := probe.Output("docker", "info", "--format", "{{.CgroupDriver}}"); err == nil {
			f.DockerCgroupDriver = strings.TrimSpace(out)
		}
		if out, err := probe.Output("docker", "info", "--format", "{{.DockerRootDir}}"); err == nil {
			f.DockerDataRoot = strings.TrimSpace(out)
		}
		if cfg, err := docker.ReadDaemon(docker.DaemonJSON); err == nil {
			f.DockerMirrors = cfg.Mirrors()
		}
		if _, err := os.Stat(docker.ProxyConf); err == nil {
			f.DockerProxyConfigured = true
		}
		f.DockerRunning = serviceActive(probe, osInfo, "docker")
		if !f.DockerRunning {
			// A working `docker version` server section is stronger evidence.
			if out, err := probe.Output("docker", "version", "--format", "{{.Server.Version}}"); err == nil && strings.TrimSpace(out) != "" {
				f.DockerRunning = true
			}
		}
	}

	f.ContainerdInstalled = execx.Has("containerd")
	if f.ContainerdInstalled {
		f.ContainerdVersion = strings.TrimSpace(mustOutput(probe, "containerd", "--version"))
		f.ContainerdConfigPath = containerd.ConfigPath
		if data, err := os.ReadFile(containerd.ConfigPath); err == nil {
			f.ContainerdSystemdCgroup, f.ContainerdRegistryConfig, f.ContainerdSandboxImage = ParseContainerdConfig(string(data))
		}
		if _, err := os.Stat(containerd.ProxyConf); err == nil {
			f.ContainerdProxyConfigured = true
		}
		f.ContainerdRunning = serviceActive(probe, osInfo, "containerd")
	}

	f.CriDockerdInstalled = execx.Has("cri-dockerd") || fileExists("/usr/bin/cri-dockerd")
	f.CriDockerdRunning = serviceActive(probe, osInfo, "cri-docker")
	f.CRIOInstalled = execx.Has("crio")
	f.KubeletInstalled = execx.Has("kubelet")
	f.KubeletRunning = serviceActive(probe, osInfo, "kubelet")
	f.KubeadmInstalled = execx.Has("kubeadm")
	if f.KubeadmInstalled {
		if out, err := probe.Output("kubeadm", "version", "-o", "short"); err == nil {
			f.KubeadmVersion = strings.TrimSpace(out)
		}
	}
	f.K3sInstalled = execx.Has("k3s") || fileExists("/etc/rancher/k3s/k3s.yaml")
	f.K3sRunning = serviceActive(probe, osInfo, "k3s")
	f.ClusterKubeconfig = k8s.DetectClusterKubeconfig()

	for _, p := range []int{2375, 2376, 2379, 2380, 6443, 8472, 9099, 10250, 10257, 10259} {
		f.Ports[p] = system.PortInUse(p)
	}
	for _, sock := range []string{
		"/run/containerd/containerd.sock",
		"/var/run/containerd/containerd.sock",
		"/var/run/cri-dockerd.sock",
		"/run/crio/crio.sock",
	} {
		if st, err := os.Stat(sock); err == nil && st.Mode()&os.ModeSocket != 0 {
			f.CRISockets = append(f.CRISockets, sock)
		}
	}
	return f
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func mustOutput(r *execx.Runner, name string, args ...string) string {
	out, _ := r.Output(name, args...)
	return out
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func serviceActive(r *execx.Runner, osInfo *system.OS, name string) bool {
	if osInfo == nil || !osInfo.HasSystemd() {
		return false
	}
	out, err := r.Output("systemctl", "is-active", name)
	return err == nil && strings.TrimSpace(out) == "active"
}

func cgroupVersion() int {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return 2
	}
	if _, err := os.Stat("/sys/fs/cgroup/systemd"); err == nil {
		return 1
	}
	if _, err := os.Stat("/sys/fs/cgroup"); err == nil {
		return 1
	}
	return 0
}

func swapEnabled() bool {
	data, err := os.ReadFile("/proc/swaps")
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	return len(lines) > 1
}

func iptablesBackend(r *execx.Runner) string {
	out := mustOutput(r, "iptables", "--version")
	switch {
	case strings.Contains(out, "nf_tables"):
		return "nft"
	case strings.Contains(out, "legacy"):
		return "legacy"
	}
	if execx.Has("nft") {
		return "nft"
	}
	if execx.Has("iptables") {
		return "legacy"
	}
	return "unknown"
}

// BuildPlan recommends a topology from observed facts. It is a pure function.
func BuildPlan(f Facts) Plan {
	p := Plan{Facts: f}
	add := func(sev Severity, code, msg, action string) {
		p.Findings = append(p.Findings, Finding{Severity: sev, Code: code, Message: msg, Action: action})
	}
	hasK8s := f.KubeletInstalled || f.KubeadmInstalled || f.K3sInstalled || f.ClusterKubeconfig != ""
	hasDocker := f.DockerInstalled

	switch {
	case f.ClusterKubeconfig != "":
		p.Topology = "existing-cluster"
		p.Summary = "检测到已初始化的集群：保持现有运行时，变更前先 drain 节点。"
		add(SeverityInfo, "existing-cluster",
			"检测到已有 kubeconfig，mkbox 不会自动切换运行时。",
			"如需更换运行时，请先新建节点验证，再逐台迁移；不要在同一节点同时安装两个 CRI。")
		if hasDocker && f.KubeletRunning {
			add(SeverityWarn, "shared-runtime",
				"Docker 与 kubelet 同时运行，二者可能共享 containerd 或端口。",
				"修改 /etc/containerd/config.toml 会同时影响 Docker 和 Kubernetes；先在维护窗口验证。")
		}
	case hasDocker && f.CriDockerdInstalled:
		p.Topology = "cri-dockerd"
		p.Summary = "检测到 Docker + cri-dockerd：kubelet 通过 cri-dockerd 使用 Docker。"
		add(SeverityWarn, "cri-dockerd-maintenance",
			"cri-dockerd 是 dockershim 的替代适配器，维护成本高于原生 containerd。",
			"新集群优先使用 shared-containerd；已有 cri-dockerd 集群保持现状。")
	case hasDocker && hasK8s:
		p.Topology = "shared-containerd"
		p.Summary = "Docker 与 Kubernetes 共存：推荐让 kubelet 复用 Docker 自带的 containerd。"
		add(SeverityInfo, "shared-containerd",
			"Docker CE 已安装 containerd，kubelet 可使用同一个 CRI socket。",
			"只保留一个 containerd；设置 SystemdCgroup=true，kubelet 使用 /run/containerd/containerd.sock。")
		add(SeverityWarn, "restart-blast-radius",
			"重启 containerd 会同时影响 Docker 容器和 Kubernetes Pod。",
			"升级或改配置前先 drain 节点，并准备回滚窗口。")
	case hasDocker:
		p.Topology = "docker-only"
		p.Summary = "当前只检测到 Docker；如需 Kubernetes，建议复用其 containerd 而不是再装一套。"
		add(SeverityInfo, "docker-only",
			"Docker 自带 containerd，可作为 Kubernetes 的 CRI。",
			"安装 kubelet/kubeadm 后设置 SystemdCgroup=true，并使用 Docker 的 containerd socket。")
	case hasK8s:
		p.Topology = "containerd"
		p.Summary = "检测到 Kubernetes 运行时：保持原生 containerd，避免再引入 Docker。"
		add(SeverityInfo, "native-containerd",
			"Kubernetes 使用 containerd 是当前推荐路径。",
			"不要在已有集群节点上安装 Docker 后切换 CRI；新增 Docker 仅作为开发工具时也要注意 containerd 配置。")
	default:
		p.Topology = "containerd"
		p.Summary = "未检测到 Docker 或 Kubernetes：推荐 containerd + kubeadm/k3s。"
		add(SeverityInfo, "greenfield",
			"干净主机优先使用 containerd；只有需要 Docker CLI/Compose 时才安装 Docker。",
			"如需 Docker 与 Kubernetes 共存，先安装 Docker，再用 mkbox k8s install 复用其 containerd。")
	}

	if f.CgroupVersion == 2 {
		if f.DockerInstalled && f.DockerCgroupDriver != "" && f.DockerCgroupDriver != "systemd" {
			add(SeverityError, "docker-cgroup-driver",
				fmt.Sprintf("cgroup v2 下 Docker 的 cgroup driver 是 %q，应为 systemd。", f.DockerCgroupDriver),
				"在 /etc/docker/daemon.json 中设置 \"exec-opts\": [\"native.cgroupdriver=systemd\"] 并重启 Docker。")
		}
		if f.ContainerdInstalled && !f.ContainerdSystemdCgroup {
			add(SeverityError, "containerd-systemd-cgroup",
				"cgroup v2 下 containerd 的 SystemdCgroup 未启用。",
				"在 /etc/containerd/config.toml 中设置 SystemdCgroup = true 并重启 containerd。")
		}
	}
	if f.SwapEnabled && hasK8s {
		add(SeverityWarn, "swap-enabled",
			"检测到 swap 仍启用，kubelet 可能拒绝启动或行为不稳定。",
			"运行 mkbox k8s preflight，或手动 swapoff -a 并注释 /etc/fstab 中的 swap 条目。")
	}
	if f.Ports[2375] {
		add(SeverityError, "docker-api-plaintext",
			"2375/tcp 正在监听：这是未加密、未认证的 Docker API，存在严重安全风险。",
			"立即关闭 2375，改用 SSH 隧道或 2376 + TLS。")
	}
	if f.Ports[6443] && f.ClusterKubeconfig == "" {
		add(SeverityWarn, "port-6443",
			"6443/tcp 已被占用，但未检测到 kubeconfig。",
			"确认是否有其他 Kubernetes/API 服务占用；避免 kubeadm init 端口冲突。")
	}
	if f.Ports[10250] && !f.KubeletInstalled {
		add(SeverityWarn, "port-10250",
			"10250/tcp 已被占用，但未检测到 kubelet。",
			"确认端口占用进程；kubelet 启动时需要该端口。")
	}
	if f.IptablesBackend == "nft" && hasK8s {
		add(SeverityInfo, "nftables",
			"iptables 使用 nftables 后端；较新的 kube-proxy 支持该模式。",
			"如遇到 Service 规则异常，检查 kube-proxy 日志与内核版本。")
	}
	if (f.HTTPProxy != "" || f.HTTPSProxy != "") && f.DockerInstalled && !f.DockerProxyConfigured {
		add(SeverityWarn, "docker-proxy",
			"Docker 已安装但 daemon 没有代理 drop-in；dockerd 不会继承 shell 代理。",
			"运行 mkbox docker proxy set <代理地址>，或确认 Docker 可直连镜像仓库。")
	}
	if (f.HTTPProxy != "" || f.HTTPSProxy != "") && f.ContainerdInstalled && !f.ContainerdProxyConfigured {
		add(SeverityWarn, "containerd-proxy",
			"containerd 已安装但没有代理 drop-in；拉取镜像可能不会走代理。",
			"运行 mkbox proxy set <代理地址> --scope containerd。")
	}
	if f.K3sInstalled && f.DockerInstalled {
		add(SeverityWarn, "k3s-docker",
			"k3s 与 Docker 同时存在；k3s 默认使用内置 containerd，不要在同一节点混用运行时。",
			"保留一种运行时；已有 k3s 集群不要切换到 Docker，除非明确验证过。")
	}
	if f.ContainerdInstalled && !f.ContainerdSystemdCgroup && f.CgroupVersion != 2 {
		add(SeverityWarn, "containerd-cgroupfs",
			"containerd 未启用 SystemdCgroup；部分发行版下可能导致 kubelet 启动失败。",
			"建议设置 SystemdCgroup = true，尤其是使用 systemd 的发行版。")
	}
	if f.CriDockerdInstalled && !f.DockerRunning {
		add(SeverityWarn, "cri-dockerd-docker-down",
			"cri-dockerd 已安装但 Docker 未运行，kubelet 将无法创建容器。",
			"启动 Docker 并确认 cri-dockerd 可连接 /var/run/docker.sock。")
	}
	if f.ContainerdInstalled && f.ContainerdRegistryConfig == "" {
		add(SeverityInfo, "registry-config",
			"containerd 未设置 CRI registry config_path，镜像加速可能不生效。",
			"运行 mkbox k8s install 或 mkbox k8s preflight 写入 certs.d 配置。")
	}

	p.NextSteps = nextSteps(p.Topology)
	return p
}

func nextSteps(topology string) []string {
	switch topology {
	case "shared-containerd":
		return []string{
			"确认 Docker 正常：docker info",
			"确认 /etc/containerd/config.toml 中 SystemdCgroup = true",
			"kubelet 使用 unix:///run/containerd/containerd.sock",
			"变更前 drain 节点，避免重启 containerd 影响业务容器",
		}
	case "cri-dockerd":
		return []string{
			"确认 cri-dockerd 服务运行：systemctl status cri-docker",
			"kubelet 使用 unix:///var/run/cri-dockerd.sock",
			"评估迁移到原生 containerd 的成本",
		}
	case "containerd":
		return []string{
			"安装 containerd，并设置 SystemdCgroup = true",
			"运行 mkbox k8s install / k8s init",
			"如只需 Docker，不要额外安装第二个 containerd",
		}
	case "docker-only":
		return []string{
			"保留 Docker 自带的 containerd",
			"需要 Kubernetes 时安装 kubelet/kubeadm，不要重复安装 containerd",
			"设置 SystemdCgroup=true 并使用 /run/containerd/containerd.sock",
		}
	case "existing-cluster":
		return []string{
			"保持现有 CRI，不要在同一节点切换运行时",
			"变更前 kubectl drain，并准备 etcd/配置备份",
			"新节点验证通过后再逐台迁移",
		}
	}
	return nil
}

// Summarize renders a short one-line description.
func Summarize(p Plan) string {
	if p.Summary != "" {
		return p.Summary
	}
	return "运行时拓扑: " + p.Topology
}

// FormatPorts returns occupied ports in ascending order.
func FormatPorts(ports map[int]bool) string {
	var out []string
	for p, used := range ports {
		if used {
			out = append(out, strconv.Itoa(p))
		}
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			a, _ := strconv.Atoi(out[i])
			b, _ := strconv.Atoi(out[j])
			if a > b {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return strings.Join(out, ", ")
}
