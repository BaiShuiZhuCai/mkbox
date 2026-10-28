package k8s

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

const (
	// MakeboxDir stores mkbox-managed Kubernetes bootstrap files.
	MakeboxDir = "/etc/makebox"
	// KubeadmConfigPath is the generated kubeadm init configuration.
	KubeadmConfigPath = MakeboxDir + "/kubeadm-init.yaml"
	// JoinScriptPath is the saved kubeadm join script.
	JoinScriptPath = MakeboxDir + "/join.sh"
	// AdminKubeconfig is kubeadm's admin kubeconfig.
	AdminKubeconfig = "/etc/kubernetes/admin.conf"
)

// InitOptions controls cluster initialisation.
type InitOptions struct {
	Distro               string
	Version              string
	ImageRepository      string
	PodCIDR              string
	ServiceCIDR          string
	CNI                  string
	APIAddress           string
	ControlPlaneEndpoint string
	CRISocket            string
	SkipPreflight        bool
	SkipFirewall         bool
	SELinuxPermissive    bool
	SkipAddons           bool
	Mirrors              []string
	GHProxy              string
	K3sClusterInit       bool
	K3sMirror            bool
	Timeout              time.Duration
}

// InitResult summarises a successful initialisation.
type InitResult struct {
	Kubeconfig  string
	JoinCommand string
	NodeIP      string
	Warnings    []string
}

// Initializer bootstraps a Kubernetes control plane.
type Initializer struct {
	OS   *system.OS
	R    *execx.Runner
	FS   *system.FS
	Opts InitOptions
}

// Run initialises the cluster.
func (in *Initializer) Run() (*InitResult, error) {
	if !in.OS.IsRoot && !in.R.DryRun {
		return nil, fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if in.Opts.Distro == "" {
		in.Opts.Distro = "kubeadm"
	}
	if in.Opts.Distro == "k3s" {
		return in.initK3s()
	}
	return in.initKubeadm()
}

func (in *Initializer) initKubeadm() (*InitResult, error) {
	if !execx.Has("kubeadm") && !in.R.DryRun {
		return nil, fmt.Errorf("未找到 kubeadm，请先运行: sudo mkbox k8s install")
	}
	if _, err := os.Stat(AdminKubeconfig); err == nil {
		return nil, fmt.Errorf("检测到已初始化的集群（%s），如需重新初始化请先运行: sudo mkbox k8s reset --all", AdminKubeconfig)
	}

	if !in.Opts.SkipPreflight {
		pf := &Preflight{
			OS:                in.OS,
			R:                 in.R,
			FS:                in.FS,
			Distro:            "kubeadm",
			CNI:               in.Opts.CNI,
			Mirrors:           in.Opts.Mirrors,
			ImageRepository:   in.Opts.ImageRepository,
			SkipFirewall:      in.Opts.SkipFirewall,
			SELinuxPermissive: in.Opts.SELinuxPermissive,
		}
		if err := pf.Run(); err != nil {
			return nil, err
		}
	} else {
		logx.Warnf("已跳过系统预检，请确保 swap、sysctl、containerd 已正确配置")
	}

	nodeIP := strings.TrimSpace(in.Opts.APIAddress)
	if nodeIP == "" {
		ip, err := system.PrimaryIP()
		if err != nil {
			return nil, fmt.Errorf("无法自动检测节点 IP，请使用 --apiserver-address 指定: %w", err)
		}
		nodeIP = ip
	}
	criSocket := in.Opts.CRISocket
	if criSocket == "" {
		criSocket = DetectCRISocket()
	}

	apiVersion := KubeadmAPIVersion(in.detectedKubeadmVersion())
	kubeadmCfg, err := KubeadmConfig(KubeadmOptions{
		APIAddress:           nodeIP,
		ControlPlaneEndpoint: in.Opts.ControlPlaneEndpoint,
		ImageRepository:      in.Opts.ImageRepository,
		PodCIDR:              in.Opts.PodCIDR,
		ServiceCIDR:          in.Opts.ServiceCIDR,
		CRISocket:            criSocket,
		APIVersion:           apiVersion,
	})
	if err != nil {
		return nil, err
	}
	if err := in.FS.Write(KubeadmConfigPath, []byte(kubeadmCfg), 0o600); err != nil {
		return nil, err
	}
	logx.Successf("已生成 kubeadm 配置: %s", KubeadmConfigPath)

	if in.R.DryRun {
		logx.Infof("[dry-run] 将执行: kubeadm config images pull --config %s", KubeadmConfigPath)
		logx.Infof("[dry-run] 将执行: kubeadm init --config %s", KubeadmConfigPath)
		return &InitResult{NodeIP: nodeIP, Kubeconfig: AdminKubeconfig}, nil
	}

	logx.Header("拉取 Kubernetes 镜像")
	if err := in.R.Run("kubeadm", "config", "images", "pull", "--config", KubeadmConfigPath); err != nil {
		return nil, fmt.Errorf("拉取镜像失败，请检查 k8s.imageRepository 或网络代理: %w", err)
	}

	logx.Header("初始化控制平面")
	if err := in.R.Run("kubeadm", "init", "--config", KubeadmConfigPath); err != nil {
		return nil, fmt.Errorf("kubeadm init 失败，可运行 sudo mkbox k8s reset 后重试: %w", err)
	}

	result := &InitResult{NodeIP: nodeIP}
	kubeconfig, err := SetupKubeconfig(in.R, in.FS, in.OS, AdminKubeconfig)
	if err != nil {
		result.Warnings = append(result.Warnings, "写入用户 kubeconfig 失败: "+err.Error())
	} else {
		result.Kubeconfig = kubeconfig
		logx.Successf("kubeconfig 已写入 %s", kubeconfig)
	}

	if !in.Opts.SkipAddons && in.Opts.CNI != "" && in.Opts.CNI != "none" {
		installer := NewAddonInstaller(in.R, AdminKubeconfig, in.Opts.GHProxy)
		installer.PodCIDR = in.Opts.PodCIDR
		installer.Timeout = in.Opts.Timeout
		if err := installer.Install(in.Opts.CNI); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("CNI %s 安装失败: %v（可重试: sudo mkbox k8s addon install %s）", in.Opts.CNI, err, in.Opts.CNI))
		}
	}

	timeout := in.Opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if err := WaitForReady(in.R, AdminKubeconfig, timeout); err != nil {
		result.Warnings = append(result.Warnings, err.Error())
	} else {
		logx.Successf("节点已就绪")
	}

	join, err := SaveJoinCommand(in.R, in.FS, JoinScriptPath, criSocket)
	if err != nil {
		result.Warnings = append(result.Warnings, "生成 join 命令失败: "+err.Error())
	} else {
		result.JoinCommand = join
	}
	return result, nil
}

func (in *Initializer) detectedKubeadmVersion() string {
	if out, err := in.R.Output("kubeadm", "version", "-o", "short"); err == nil && out != "" {
		return out
	}
	return in.Opts.Version
}

// KubeadmAPIVersion picks the kubeadm config API version for a kubeadm release.
func KubeadmAPIVersion(version string) string {
	re := regexp.MustCompile(`^v?1\.(\d+)`)
	m := re.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return "v1beta4"
	}
	minor, _ := strconv.Atoi(m[1])
	if minor < 31 {
		return "v1beta3"
	}
	return "v1beta4"
}

// DetectCRISocket returns the CRI socket used by kubeadm.
func DetectCRISocket() string {
	candidates := []string{
		"/run/containerd/containerd.sock",
		"/var/run/containerd/containerd.sock",
		"/run/crio/crio.sock",
		"/var/run/cri-dockerd.sock",
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && st.Mode()&os.ModeSocket != 0 {
			return "unix://" + p
		}
	}
	return "unix:///run/containerd/containerd.sock"
}

// KubeadmOptions is the input for KubeadmConfig.
type KubeadmOptions struct {
	APIAddress           string
	ControlPlaneEndpoint string
	ImageRepository      string
	PodCIDR              string
	ServiceCIDR          string
	CRISocket            string
	APIVersion           string
}

func certSANs(opts KubeadmOptions) []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	if hostname, err := os.Hostname(); err == nil {
		add(hostname)
	}
	add(opts.APIAddress)
	if endpoint := opts.ControlPlaneEndpoint; endpoint != "" {
		host, _, err := net.SplitHostPort(endpoint)
		if err == nil {
			add(host)
		} else {
			add(endpoint)
		}
	}
	return out
}

// NormalizeEndpoint ensures a control plane endpoint has a port.
var hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?\.?$`)

func NormalizeEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", nil
	}
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		if strings.Contains(endpoint, ":") && net.ParseIP(endpoint) == nil {
			return "", fmt.Errorf("无效的 control-plane-endpoint %q，请使用 host:port", endpoint)
		}
		endpoint = net.JoinHostPort(endpoint, "6443")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return "", fmt.Errorf("无效的 control-plane-endpoint %q", endpoint)
	}
	if net.ParseIP(host) == nil && !hostnameRe.MatchString(host) {
		return "", fmt.Errorf("无效的 control-plane-endpoint 主机名 %q", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("无效的 control-plane-endpoint 端口 %q", port)
	}
	return endpoint, nil
}

// KubeadmConfig renders a kubeadm InitConfiguration + ClusterConfiguration.
func KubeadmConfig(opts KubeadmOptions) (string, error) {
	if net.ParseIP(opts.APIAddress) == nil {
		return "", fmt.Errorf("advertiseAddress %q 不是合法 IP", opts.APIAddress)
	}
	if opts.PodCIDR == "" || opts.ServiceCIDR == "" {
		return "", fmt.Errorf("podSubnet 和 serviceSubnet 不能为空")
	}
	if _, _, err := net.ParseCIDR(opts.PodCIDR); err != nil {
		return "", fmt.Errorf("无效的 podSubnet %q", opts.PodCIDR)
	}
	if _, _, err := net.ParseCIDR(opts.ServiceCIDR); err != nil {
		return "", fmt.Errorf("无效的 serviceSubnet %q", opts.ServiceCIDR)
	}
	if opts.ImageRepository == "" {
		return "", fmt.Errorf("imageRepository 不能为空")
	}
	if opts.CRISocket == "" {
		opts.CRISocket = "unix:///run/containerd/containerd.sock"
	}
	endpoint, err := NormalizeEndpoint(opts.ControlPlaneEndpoint)
	if err != nil {
		return "", err
	}
	apiVersion := opts.APIVersion
	if apiVersion == "" {
		apiVersion = "v1beta4"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: kubeadm.k8s.io/%s\n", apiVersion)
	b.WriteString("kind: InitConfiguration\n")
	b.WriteString("localAPIEndpoint:\n")
	fmt.Fprintf(&b, "  advertiseAddress: %q\n", opts.APIAddress)
	b.WriteString("  bindPort: 6443\n")
	b.WriteString("nodeRegistration:\n")
	fmt.Fprintf(&b, "  criSocket: %q\n", opts.CRISocket)
	b.WriteString("  imagePullPolicy: IfNotPresent\n")
	if apiVersion == "v1beta4" {
		b.WriteString("  kubeletExtraArgs:\n")
		b.WriteString("    - name: node-ip\n")
		fmt.Fprintf(&b, "      value: %q\n", opts.APIAddress)
	} else {
		b.WriteString("  kubeletExtraArgs:\n")
		fmt.Fprintf(&b, "    node-ip: %q\n", opts.APIAddress)
	}
	b.WriteString("---\n")
	fmt.Fprintf(&b, "apiVersion: kubeadm.k8s.io/%s\n", apiVersion)
	b.WriteString("kind: ClusterConfiguration\n")
	b.WriteString("clusterName: makebox\n")
	fmt.Fprintf(&b, "imageRepository: %q\n", strings.TrimRight(opts.ImageRepository, "/"))
	b.WriteString("networking:\n")
	fmt.Fprintf(&b, "  podSubnet: %q\n", opts.PodCIDR)
	fmt.Fprintf(&b, "  serviceSubnet: %q\n", opts.ServiceCIDR)
	if endpoint != "" {
		fmt.Fprintf(&b, "controlPlaneEndpoint: %q\n", endpoint)
	}
	sans := certSANs(opts)
	if len(sans) > 0 {
		b.WriteString("apiServer:\n")
		b.WriteString("  certSANs:\n")
		for _, san := range sans {
			fmt.Fprintf(&b, "    - %q\n", san)
		}
	}
	return b.String(), nil
}

// SetupKubeconfig copies a cluster admin kubeconfig to the invoking user and root.
func SetupKubeconfig(r *execx.Runner, fs *system.FS, osInfo *system.OS, source string) (string, error) {
	if r.DryRun {
		logx.Infof("[dry-run] 将 %s 复制到 <user>/.kube/config 与 /root/.kube/config", source)
		return filepath.Join(system.UserHome(), ".kube", "config"), nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("读取 %s: %w", source, err)
	}
	targets := []string{filepath.Join(system.UserHome(), ".kube", "config")}
	if osInfo.SudoUser != nil {
		rootPath := filepath.Join("/root", ".kube", "config")
		if targets[0] != rootPath {
			targets = append(targets, rootPath)
		}
	}
	var primary string
	for _, target := range targets {
		if err := fs.EnsureDir(filepath.Dir(target), 0o700); err != nil {
			return "", err
		}
		if err := fs.Write(target, data, 0o600); err != nil {
			return "", err
		}
		_ = os.Chmod(target, 0o600)
		if strings.HasPrefix(target, system.UserHome()) {
			_ = system.ChownToSudoUser(target)
			if primary == "" {
				primary = target
			}
		}
	}
	if primary == "" {
		primary = targets[0]
	}
	return primary, nil
}

// SaveJoinCommand generates and persists a kubeadm join script.
func SaveJoinCommand(r *execx.Runner, fs *system.FS, path, criSocket string) (string, error) {
	out, err := r.Output("kubeadm", "token", "create", "--print-join-command")
	if err != nil {
		return "", err
	}
	join := strings.TrimSpace(out)
	if join == "" {
		return "", fmt.Errorf("kubeadm 未返回 join 命令")
	}
	if !strings.Contains(join, "--cri-socket") {
		join += " --cri-socket " + criSocket
	}
	script := "#!/usr/bin/env bash\nset -euo pipefail\n" + join + "\n"
	if err := fs.Write(path, []byte(script), 0o700); err != nil {
		return "", err
	}
	return join, nil
}

// WaitForReady blocks until at least one node reports Ready.
func WaitForReady(r *execx.Runner, kubeconfig string, timeout time.Duration) error {
	if r.DryRun {
		return nil
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		out, err := r.Output("kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "--no-headers", "--request-timeout=5s")
		if err == nil && hasReadyNode(out) {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("等待节点 Ready 超时: %w", lastErr)
			}
			return fmt.Errorf("等待节点 Ready 超时（%s）", timeout)
		}
		time.Sleep(5 * time.Second)
	}
}

func hasReadyNode(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "Ready") {
			return true
		}
	}
	return false
}
