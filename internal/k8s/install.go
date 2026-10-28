package k8s

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/makebox/makebox/internal/containerd"
	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

const (
	// K8sAPTKeyring is the Kubernetes apt signing key managed by mkbox.
	K8sAPTKeyring = "/etc/apt/keyrings/kubernetes-apt-keyring.asc"
	// K8sAPTSource is the Kubernetes apt source list managed by mkbox.
	K8sAPTSource = "/etc/apt/sources.list.d/kubernetes.list"
	// K8sYUMRepo is the Kubernetes yum repository managed by mkbox.
	K8sYUMRepo = "/etc/yum.repos.d/kubernetes.repo"
	// K8sRPMKey is the Kubernetes RPM signing key managed by mkbox.
	K8sRPMKey = "/etc/pki/rpm-gpg/kubernetes.asc"
)

// Installer installs Kubernetes components.
type Installer struct {
	OS              *system.OS
	R               *execx.Runner
	FS              *system.FS
	Distro          string
	Version         string
	RepoMirror      string
	K3sMirror       bool
	Mirrors         []string
	ImageRepository string
}

// Run installs the selected Kubernetes distribution.
func (i *Installer) Run() error {
	if !i.OS.IsRoot && !i.R.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if i.Distro == "" {
		i.Distro = "kubeadm"
	}
	if i.Version == "" {
		i.Version = "1.35"
	}
	logx.Header("安装 Kubernetes 组件（%s）", i.Distro)

	switch i.Distro {
	case "kubeadm":
		return i.installKubeadm()
	case "k3s":
		if !i.OS.HasSystemd() && !i.R.DryRun {
			return fmt.Errorf("mkbox 的 k3s 安装依赖 systemd")
		}
		if err := InstallK3s(i.R, i.OS, i.Version, i.K3sMirror, false); err != nil {
			return err
		}
		if err := i.FS.Write(K3sRegistriesPath, []byte(K3sRegistriesYAML(i.Mirrors)), 0o644); err != nil {
			return err
		}
		logx.Successf("k3s 已安装，下一步: mkbox k8s init --distro k3s")
		return nil
	default:
		return fmt.Errorf("未知的 Kubernetes 发行版 %q（可选 kubeadm/k3s）", i.Distro)
	}
}

func (i *Installer) installKubeadm() error {
	if i.OS.Family != "debian" && i.OS.Family != "rhel" {
		return fmt.Errorf("kubeadm 自动安装目前支持 Debian/Ubuntu 与 RHEL 系；%s 请使用 --distro k3s", i.OS.DisplayName())
	}

	engine := docker.NewEngine(i.OS, i.R, i.FS, dockerMirrorFor(i.RepoMirror), "")
	if !execx.Has("containerd") {
		if err := engine.InstallContainerd(); err != nil {
			return err
		}
	}
	cm := containerd.NewManager(i.R, i.FS, i.OS)
	if err := cm.Configure(i.Mirrors, i.ImageRepository); err != nil {
		return err
	}
	if err := cm.Restart(); err != nil {
		return err
	}

	series, err := ReleaseSeries(i.Version)
	if err != nil {
		return err
	}

	switch i.OS.Family {
	case "debian":
		if err := i.installAptPackages(series); err != nil {
			return err
		}
	case "rhel":
		if err := i.installRPMPackages(series); err != nil {
			return err
		}
	}

	if i.OS.HasSystemd() {
		if err := i.R.Run("systemctl", "enable", "kubelet"); err != nil {
			logx.Warnf("设置 kubelet 开机启动失败: %v", err)
		}
	}
	if !i.R.DryRun {
		if out, err := i.R.Output("kubeadm", "version", "-o", "short"); err == nil {
			logx.Successf("kubeadm 安装完成: %s", out)
		}
	}
	logx.Infof("下一步: sudo mkbox k8s init  （或 sudo mkbox k8s up 一键完成）")
	return nil
}

func dockerMirrorFor(repoMirror string) string {
	if repoMirror == "official" {
		return "official"
	}
	return "aliyun"
}

// ReleaseSeries extracts a Kubernetes minor release such as 1.35.
func ReleaseSeries(version string) (string, error) {
	re := regexp.MustCompile(`^v?(1\.\d+)`)
	m := re.FindStringSubmatch(strings.TrimSpace(version))
	if m == nil {
		return "", fmt.Errorf("无效的 Kubernetes 版本 %q（示例: 1.35 或 v1.35.2）", version)
	}
	return m[1], nil
}

// K8sRepoBase returns the package repository base URL.
func K8sRepoBase(series, mirror, format string) string {
	if mirror == "aliyun" {
		return fmt.Sprintf("https://mirrors.aliyun.com/kubernetes-new/core/stable/v%s/%s", series, format)
	}
	return fmt.Sprintf("https://pkgs.k8s.io/core:/stable:/v%s/%s", series, format)
}

func (i *Installer) installAptPackages(series string) error {
	if i.OS.Codename == "" {
		return fmt.Errorf("无法读取发行版代号，请检查 /etc/os-release")
	}
	base := K8sRepoBase(series, i.RepoMirror, "deb")
	officialBase := K8sRepoBase(series, "official", "deb")
	keyURL := base + "/Release.key"
	if i.RepoMirror == "aliyun" {
		keyURL = base + "/Release.key"
	}
	fallbackKey := officialBase + "/Release.key"

	if err := i.R.Run("install", "-m", "0755", "-d", "/etc/apt/keyrings"); err != nil {
		return err
	}
	if err := downloadKey(i.R, keyURL, fallbackKey, K8sAPTKeyring); err != nil {
		return err
	}
	source := fmt.Sprintf("deb [signed-by=%s] %s/ /\n", K8sAPTKeyring, base)
	if err := i.FS.Write(K8sAPTSource, []byte(source), 0o644); err != nil {
		return err
	}
	if err := i.R.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "update"); err != nil {
		return fmt.Errorf("更新 apt 索引失败: %w", err)
	}
	if err := i.R.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "install", "-y",
		"kubelet", "kubeadm", "kubectl"); err != nil {
		return fmt.Errorf("安装 kubelet/kubeadm/kubectl 失败: %w", err)
	}
	if err := i.R.Run("apt-mark", "hold", "kubelet", "kubeadm", "kubectl"); err != nil {
		logx.Warnf("apt-mark hold 失败: %v", err)
	}
	logx.Successf("已通过 apt 安装 kubelet/kubeadm/kubectl（版本系列 v%s）", series)
	return nil
}

func (i *Installer) installRPMPackages(series string) error {
	base := K8sRepoBase(series, i.RepoMirror, "rpm")
	keyURL := base + "/repodata/repomd.xml.key"
	fallbackKey := K8sRepoBase(series, "official", "rpm") + "/repodata/repomd.xml.key"
	if err := i.R.Run("install", "-m", "0755", "-d", "/etc/pki/rpm-gpg"); err != nil {
		return err
	}
	if err := downloadKey(i.R, keyURL, fallbackKey, K8sRPMKey); err != nil {
		return err
	}
	repo := strings.Join([]string{
		"[kubernetes]",
		"name=Kubernetes",
		"baseurl=" + base + "/",
		"enabled=1",
		"gpgcheck=1",
		"gpgkey=file://" + K8sRPMKey,
		"exclude=kubelet kubeadm kubectl cri-tools kubernetes-cni",
		"",
	}, "\n")
	if err := i.FS.Write(K8sYUMRepo, []byte(repo), 0o644); err != nil {
		return err
	}
	pm := "dnf"
	if !execx.Has("dnf") {
		pm = "yum"
	}
	if err := i.R.Run(pm, "install", "-y", "kubelet", "kubeadm", "kubectl", "--disableexcludes=kubernetes"); err != nil {
		return fmt.Errorf("安装 kubelet/kubeadm/kubectl 失败: %w", err)
	}
	logx.Successf("已通过 %s 安装 kubelet/kubeadm/kubectl（版本系列 v%s）", pm, series)
	return nil
}

func downloadKey(r *execx.Runner, primary, fallback, dest string) error {
	if r.DryRun {
		logx.Infof("[dry-run] 下载签名密钥 %s -> %s（失败时回退 %s）", primary, dest, fallback)
		return nil
	}
	if err := r.Run("curl", "-fsSL", primary, "-o", dest); err == nil {
		_ = r.Run("chmod", "0644", dest)
		return nil
	}
	_ = os.Remove(dest)
	logx.Warnf("主密钥地址不可用，尝试官方源: %s", fallback)
	if err := r.Run("curl", "-fsSL", fallback, "-o", dest); err != nil {
		return fmt.Errorf("下载 Kubernetes 签名密钥失败: %w", err)
	}
	_ = r.Run("chmod", "0644", dest)
	return nil
}

var (
	k3sVersionRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+\+k3s\d+$`)
	k3sChannelRe = regexp.MustCompile(`^v?\d+\.\d+(?:\.\d+)?$`)
)

// k3sVersionEnv maps --version onto INSTALL_K3S_VERSION or INSTALL_K3S_CHANNEL.
// Exact k3s builds use a version; plain Kubernetes versions select a release
// channel instead of silently falling back to stable.
func k3sVersionEnv(version string) ([]string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return []string{"INSTALL_K3S_CHANNEL=stable"}, nil
	}
	if k3sVersionRe.MatchString(version) {
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		return []string{"INSTALL_K3S_VERSION=" + version}, nil
	}
	if k3sChannelRe.MatchString(version) {
		channel := strings.TrimPrefix(version, "v")
		if strings.Count(channel, ".") == 2 {
			channel = channel[:strings.LastIndex(channel, ".")]
		}
		return []string{"INSTALL_K3S_CHANNEL=v" + channel}, nil
	}
	return nil, fmt.Errorf("k3s 版本必须形如 v1.35.0+k3s1 或 1.35（版本通道）")
}

// ValidateK3sVersion checks a version value before installation starts.
func ValidateK3sVersion(version string) error {
	_, err := k3sVersionEnv(version)
	return err
}

// InstallK3s downloads k3s via the official install script without starting it.
func InstallK3s(r *execx.Runner, osInfo *system.OS, version string, chinaMirror, agent bool) error {
	if agent {
		if _, err := os.Stat("/etc/systemd/system/k3s-agent.service"); err == nil && execx.Has("k3s") {
			logx.Infof("k3s agent 已安装，跳过")
			return nil
		}
	} else if execx.Has("k3s") {
		logx.Infof("k3s 已安装，跳过下载")
		return nil
	}

	scriptURL := "https://get.k3s.io"
	if chinaMirror {
		scriptURL = "https://rancher-mirror.rancher.cn/k3s/k3s-install.sh"
	}
	versionEnv, err := k3sVersionEnv(version)
	if err != nil {
		return err
	}
	env := []string{"INSTALL_K3S_SKIP_START=true"}
	if chinaMirror {
		env = append(env, "INSTALL_K3S_MIRROR=cn")
	}
	env = append(env, versionEnv...)
	if agent {
		env = append(env, "INSTALL_K3S_EXEC=agent")
	}

	logx.Infof("使用安装脚本 %s 下载 k3s（不会自动启动）", scriptURL)
	script := fmt.Sprintf("curl -fsSL %s | %s sh -", scriptURL, strings.Join(env, " "))
	if err := r.Shell(script); err != nil {
		return fmt.Errorf("安装 k3s 失败: %w", err)
	}
	if !r.DryRun && !execx.Has("k3s") {
		return fmt.Errorf("k3s 安装脚本执行完成，但未找到 k3s 可执行文件")
	}
	logx.Successf("k3s 已下载安装")
	return nil
}
