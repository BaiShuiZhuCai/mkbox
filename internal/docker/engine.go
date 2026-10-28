// Package docker installs and configures Docker Engine across Linux distros.
package docker

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

const (
	// DaemonJSON is Docker's main configuration file.
	DaemonJSON = "/etc/docker/daemon.json"
	// APTKeyring is where mkbox stores the Docker repository signing key.
	APTKeyring = "/etc/apt/keyrings/docker.asc"
	// APTSource is the Docker apt source list managed by mkbox.
	APTSource = "/etc/apt/sources.list.d/docker.list"
	// YUMRepo is the Docker yum repository file.
	YUMRepo = "/etc/yum.repos.d/docker-ce.repo"
)

// Engine automates Docker Engine installation.
type Engine struct {
	OS     *system.OS
	R      *execx.Runner
	FS     *system.FS
	Mirror string // aliyun, tuna, ustc, huawei, official
	User   string // optional user to add to the docker group
}

// NewEngine creates an Engine helper.
func NewEngine(o *system.OS, r *execx.Runner, fs *system.FS, mirror, user string) *Engine {
	if mirror == "" {
		mirror = "aliyun"
	}
	return &Engine{OS: o, R: r, FS: fs, Mirror: mirror, User: user}
}

func (e *Engine) requireRoot() error {
	if !e.OS.IsRoot && !e.R.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if !e.OS.IsSupported() {
		return fmt.Errorf("暂不支持当前发行版 (%s)，请参考 README 手动安装", e.OS.DisplayName())
	}
	return nil
}

// ValidatePackageMirror accepts the Docker package repositories mkbox knows.
func ValidatePackageMirror(mirror string) error {
	switch mirror {
	case "", "aliyun", "tuna", "ustc", "huawei", "official":
		return nil
	}
	return fmt.Errorf("--mirror 必须是 aliyun、tuna、ustc、huawei 或 official")
}

// mirrorHost returns the base host for a Docker package mirror.
func mirrorHost(mirror string) string {
	switch mirror {
	case "aliyun":
		return "https://mirrors.aliyun.com/docker-ce"
	case "tuna":
		return "https://mirrors.tuna.tsinghua.edu.cn/docker-ce"
	case "ustc":
		return "https://mirrors.ustc.edu.cn/docker-ce"
	case "huawei":
		return "https://repo.huaweicloud.com/docker-ce"
	default:
		return "https://download.docker.com"
	}
}

func debDistro(o *system.OS) (string, error) {
	switch o.ID {
	case "ubuntu", "debian", "raspbian":
		return o.ID, nil
	}
	for _, like := range o.IDLike {
		if strings.Contains(like, "ubuntu") {
			return "ubuntu", nil
		}
		if strings.Contains(like, "debian") {
			return "debian", nil
		}
	}
	return "", fmt.Errorf("无法为 %s 确定 Docker apt 仓库路径", o.DisplayName())
}

func debArch(arch string) string {
	switch arch {
	case "amd64", "arm64":
		return arch
	case "arm":
		return "armhf"
	}
	return arch
}

func rpmArch(arch string) string {
	switch arch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i686"
	case "arm":
		return "armhfp"
	}
	return arch
}

func rpmDistro(o *system.OS) string {
	id := strings.ToLower(strings.TrimSpace(o.ID))
	if id != "" {
		if id == "fedora" {
			return "fedora"
		}
		return "centos"
	}
	for _, like := range o.IDLike {
		if strings.EqualFold(strings.TrimSpace(like), "fedora") {
			return "fedora"
		}
	}
	return "centos"
}

// releaseVersions lists candidate $releasever values, newest first. Building
// blocklists lag behind new distribution releases, so older values are tried
// before giving up.
func releaseVersions(versionID string) []string {
	fields := strings.SplitN(strings.TrimSpace(versionID), ".", 2)
	major, err := strconv.Atoi(fields[0])
	if err != nil || major <= 0 {
		return nil
	}
	const fallbacks = 3
	out := make([]string, 0, fallbacks)
	for i := 0; i < fallbacks && major-i > 0; i++ {
		out = append(out, strconv.Itoa(major-i))
	}
	return out
}

// Install installs Docker Engine and its CLI plugins.
func (e *Engine) Install() error {
	if err := e.requireRoot(); err != nil {
		return err
	}
	if err := ValidatePackageMirror(e.Mirror); err != nil {
		return err
	}
	logx.Header("安装 Docker Engine（软件源：%s）", e.Mirror)
	if e.OS.HasSystemd() == false && e.OS.Family != "alpine" {
		logx.Warnf("未检测到 systemd，服务将不会自动启动")
	}

	switch e.OS.Family {
	case "debian":
		if err := e.setupAptRepo(); err != nil {
			return err
		}
		if err := e.aptInstall("docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"); err != nil {
			return err
		}
	case "rhel":
		if err := e.setupRPMRepo(); err != nil {
			return err
		}
		if err := e.rpmInstall("docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"); err != nil {
			return err
		}
	case "suse":
		if err := e.setupZypperRepo(); err != nil {
			return err
		}
		if err := e.R.Run("zypper", "--non-interactive", "install", "docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"); err != nil {
			return fmt.Errorf("安装 Docker 软件包失败: %w", err)
		}
	case "arch":
		if err := e.R.Run("pacman", "-Sy", "--noconfirm", "docker", "docker-buildx", "docker-compose"); err != nil {
			return fmt.Errorf("安装 Docker 软件包失败: %w", err)
		}
	case "alpine":
		if err := e.R.Run("apk", "add", "--no-cache", "docker", "docker-cli-compose", "docker-cli-buildx"); err != nil {
			return fmt.Errorf("安装 Docker 软件包失败: %w", err)
		}
	default:
		return fmt.Errorf("暂不支持 %s 的自动安装", e.OS.DisplayName())
	}

	if err := e.Enable(); err != nil {
		return err
	}
	if e.User != "" {
		if err := e.R.Run("usermod", "-aG", "docker", e.User); err != nil {
			logx.Warnf("将用户 %s 加入 docker 组失败: %v", e.User, err)
		} else {
			logx.Infof("已将用户 %s 加入 docker 组（需重新登录生效）", e.User)
		}
	}
	if !e.R.DryRun {
		if out, err := e.R.Output("docker", "--version"); err == nil {
			logx.Successf("Docker 安装完成: %s", out)
		} else {
			return fmt.Errorf("Docker 安装后校验失败: %w", err)
		}
	}
	return nil
}

// InstallContainerd installs only containerd (used by kubeadm).
func (e *Engine) InstallContainerd() error {
	if err := e.requireRoot(); err != nil {
		return err
	}
	if execx.Has("containerd") {
		logx.Infof("containerd 已安装，跳过")
		return nil
	}
	logx.Header("安装 containerd")
	switch e.OS.Family {
	case "debian":
		if err := e.setupAptRepo(); err != nil {
			return err
		}
		return e.aptInstall("containerd.io")
	case "rhel":
		if err := e.setupRPMRepo(); err != nil {
			return err
		}
		return e.rpmInstall("containerd.io")
	case "suse":
		if err := e.setupZypperRepo(); err != nil {
			return err
		}
		return e.R.Run("zypper", "--non-interactive", "install", "containerd.io")
	case "arch":
		return e.R.Run("pacman", "-Sy", "--noconfirm", "containerd")
	case "alpine":
		return e.R.Run("apk", "add", "--no-cache", "containerd")
	}
	return fmt.Errorf("暂不支持在 %s 上自动安装 containerd", e.OS.DisplayName())
}

func (e *Engine) setupAptRepo() error {
	distro, err := debDistro(e.OS)
	if err != nil {
		return err
	}
	if e.OS.Codename == "" {
		return fmt.Errorf("无法读取发行版代号（VERSION_CODENAME），请检查 /etc/os-release")
	}
	base := mirrorHost(e.Mirror) + "/linux/" + distro
	keyURL := base + "/gpg"
	logx.Infof("配置 Docker apt 源: %s (%s)", base, e.OS.Codename)

	if err := e.R.Run("install", "-m", "0755", "-d", "/etc/apt/keyrings"); err != nil {
		return fmt.Errorf("创建 keyrings 目录失败: %w", err)
	}
	if err := e.R.Run("curl", "-fsSL", keyURL, "-o", APTKeyring); err != nil {
		return fmt.Errorf("下载 Docker GPG 密钥失败（%s）: %w", keyURL, err)
	}
	if !e.R.DryRun {
		_ = e.R.Run("chmod", "0644", APTKeyring)
	}

	line := fmt.Sprintf("deb [arch=%s signed-by=%s] %s %s stable\n", debArch(e.OS.Arch), APTKeyring, base, e.OS.Codename)
	if err := e.FS.Write(APTSource, []byte(line), 0o644); err != nil {
		return err
	}
	if err := e.aptUpdate(); err != nil {
		return err
	}
	return nil
}

func (e *Engine) setupRPMRepo() error {
	distro := rpmDistro(e.OS)
	base := mirrorHost(e.Mirror)
	repoURL := base + "/linux/" + distro + "/docker-ce.repo"
	logx.Infof("配置 Docker yum 源: %s", repoURL)

	if execx.Has("dnf") {
		if err := e.R.Run("dnf", "install", "-y", "dnf-plugins-core"); err != nil {
			logx.Warnf("安装 dnf-plugins-core 失败: %v", err)
		}
	}
	if e.R.DryRun {
		logx.Infof("[dry-run] 下载并写入 %s（源 %s）", YUMRepo, repoURL)
		return nil
	}
	content, err := e.R.Output("curl", "-fsSL", repoURL)
	if err != nil && e.Mirror != "official" {
		fallbackURL := mirrorHost("official") + "/linux/" + distro + "/docker-ce.repo"
		logx.Warnf("软件源 %s 不可用，回退到官方源 %s", repoURL, fallbackURL)
		base, repoURL = mirrorHost("official"), fallbackURL
		content, err = e.R.Output("curl", "-fsSL", repoURL)
	}
	if err != nil {
		return fmt.Errorf("下载 Docker yum 源失败（%s）: %w", repoURL, err)
	}
	if e.Mirror != "official" && strings.Contains(content, "download.docker.com") {
		content = strings.ReplaceAll(content, "https://download.docker.com", base)
	}
	if strings.Contains(content, "$releasever") {
		ver, err := e.repoReleaseVersion(base, distro)
		if err != nil {
			return err
		}
		if ver != e.OS.VersionID && ver != "" {
			logx.Warnf("软件源尚未提供 %s 的构建，改用 %s（已固定 $releasever=%s）", e.OS.DisplayName(), ver, ver)
			content = strings.ReplaceAll(content, "$releasever", ver)
		}
	}
	if err := e.FS.Write(YUMRepo, []byte(content), 0o644); err != nil {
		return err
	}
	logx.Successf("已写入 %s", YUMRepo)
	return nil
}

// repoReleaseVersion returns a $releasever value that the repository really
// publishes for this host, preferring the running release. Docker does not
// build RPMs for every distribution release, so a fresh Fedora/CentOS release
// would otherwise make dnf fail with HTTP 404.
func (e *Engine) repoReleaseVersion(base, distro string) (string, error) {
	candidates := releaseVersions(e.OS.VersionID)
	if len(candidates) == 0 {
		return "", nil
	}
	arch := rpmArch(e.OS.Arch)
	for _, ver := range candidates {
		url := fmt.Sprintf("%s/linux/%s/%s/%s/stable/repodata/repomd.xml", base, distro, ver, arch)
		if err := e.R.Run("curl", "-fsSIL", "-o", os.DevNull, url); err == nil {
			return ver, nil
		}
	}
	return "", fmt.Errorf("Docker 软件源 %s 中没有可用的 %s 构建（已尝试版本 %s），请换用 --mirror official 或安装发行版自带的 docker 包",
		base, distro, strings.Join(candidates, "/"))
}

func (e *Engine) setupZypperRepo() error {
	base := mirrorHost(e.Mirror)
	repoURL := base + "/linux/opensuse/docker-ce.repo"
	if e.R.DryRun {
		logx.Infof("[dry-run] 添加 zypper 源 %s", repoURL)
		return nil
	}
	content, err := e.R.Output("curl", "-fsSL", repoURL)
	if err != nil {
		return fmt.Errorf("下载 Docker zypper 源失败: %w", err)
	}
	if e.Mirror != "official" {
		content = strings.ReplaceAll(content, "https://download.docker.com", base)
	}
	if err := e.FS.Write("/etc/zypp/repos.d/docker-ce.repo", []byte(content), 0o644); err != nil {
		return err
	}
	return e.R.Run("zypper", "--gpg-auto-import-keys", "refresh")
}

func (e *Engine) aptUpdate() error {
	return e.R.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "update")
}

func (e *Engine) aptInstall(pkgs ...string) error {
	args := append([]string{"install", "-y", "--no-install-recommends"}, pkgs...)
	if err := e.R.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", args...); err != nil {
		return fmt.Errorf("安装软件包失败: %w", err)
	}
	return nil
}

func (e *Engine) rpmInstall(pkgs ...string) error {
	pm := "dnf"
	if e.OS.PkgMgr == "yum" || !execx.Has("dnf") {
		pm = "yum"
	}
	args := append([]string{"install", "-y"}, pkgs...)
	if err := e.R.Run(pm, args...); err != nil {
		return fmt.Errorf("安装软件包失败: %w", err)
	}
	return nil
}

// Enable enables and starts the Docker service.
func (e *Engine) Enable() error {
	if e.OS.HasSystemd() {
		// `systemctl enable --now` needs systemd >= 220 while CentOS/RHEL 7
		// ships 219, so enable and start are kept as separate steps.
		if err := e.R.Run("systemctl", "enable", "docker"); err != nil {
			return fmt.Errorf("设置 Docker 开机启动失败: %v", err)
		}
		if err := e.R.Run("systemctl", "start", "docker"); err != nil {
			return fmt.Errorf("启动 Docker 服务失败: %w", err)
		}
		return nil
	}
	if e.OS.Family == "alpine" {
		if err := e.R.Run("rc-update", "add", "docker", "default"); err != nil {
			logx.Warnf("设置 Docker 开机启动失败: %v", err)
		}
		if err := e.R.Run("rc-service", "docker", "start"); err != nil {
			return fmt.Errorf("启动 Docker 服务失败: %w", err)
		}
		return nil
	}
	if e.R.DryRun {
		logx.Infof("[dry-run] 未检测到受支持的服务管理器，跳过启动 Docker")
		return nil
	}
	return fmt.Errorf("未检测到受支持的服务管理器，请手动启动 Docker")
}

// Uninstall removes Docker packages. Purge also deletes data and configuration.
func (e *Engine) Uninstall(purge bool) error {
	if err := e.requireRoot(); err != nil {
		return err
	}
	logx.Header("卸载 Docker Engine")
	switch e.OS.Family {
	case "debian":
		_ = e.R.RunEnv([]string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "purge", "-y",
			"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin",
			"docker-compose-plugin", "docker-ce-rootless-extras")
		_ = e.FS.Remove(APTSource)
		_ = e.R.Run("apt-get", "update")
	case "rhel":
		_ = e.R.Run("dnf", "remove", "-y", "docker-ce", "docker-ce-cli", "containerd.io",
			"docker-buildx-plugin", "docker-compose-plugin")
		_ = e.FS.Remove(YUMRepo)
	case "suse":
		_ = e.R.Run("zypper", "--non-interactive", "remove", "docker-ce", "docker-ce-cli",
			"containerd.io", "docker-buildx-plugin", "docker-compose-plugin")
		_ = e.FS.Remove("/etc/zypp/repos.d/docker-ce.repo")
	case "arch":
		_ = e.R.Run("pacman", "-Rns", "--noconfirm", "docker", "docker-buildx", "docker-compose")
	case "alpine":
		_ = e.R.Run("apk", "del", "docker", "docker-cli-compose", "docker-cli-buildx")
	}
	if purge {
		logx.Warnf("正在删除 Docker 数据（不可恢复）")
		_ = e.FS.Remove("/etc/docker/daemon.json")
		// Do not remove /var/lib/containerd: it may be shared with
		// Kubernetes and containerd data is not owned by Docker alone.
		if err := e.R.Run("rm", "-rf", "/var/lib/docker"); err != nil {
			return err
		}
	}
	logx.Successf("Docker 已卸载")
	return nil
}

// Status describes the local Docker installation.
type Status struct {
	Installed bool
	Running   bool
	Version   string
	DataRoot  string
	Mirrors   []string
}

// GetStatus inspects the Docker installation.
func GetStatus(r *execx.Runner) Status {
	st := Status{}
	if !execx.Has("docker") {
		return st
	}
	st.Installed = true
	if out, err := r.Output("docker", "version", "--format", "{{.Server.Version}}"); err == nil && out != "" {
		st.Running = true
		st.Version = out
	} else if out, err := r.Output("docker", "--version"); err == nil {
		st.Version = strings.TrimSpace(strings.TrimPrefix(out, "Docker version "))
	}
	if out, err := r.Output("docker", "info", "--format", "{{.DockerRootDir}}"); err == nil {
		st.DataRoot = out
	}
	if cfg, err := ReadDaemon(DaemonJSON); err == nil {
		st.Mirrors = cfg.Mirrors()
	}
	return st
}

// IsInstalled reports whether the docker CLI exists.
func IsInstalled() bool { return execx.Has("docker") }
