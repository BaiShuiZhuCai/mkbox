package k8s

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/makebox/makebox/internal/containerd"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/system"
)

const (
	// SysctlFile is the sysctl drop-in managed by mkbox.
	SysctlFile = "/etc/sysctl.d/99-mkbox-k8s.conf"
	// ModulesFile loads kernel modules required by Kubernetes.
	ModulesFile = "/etc/modules-load.d/mkbox-k8s.conf"
)

// Preflight prepares a Linux host for Kubernetes.
type Preflight struct {
	OS                *system.OS
	R                 *execx.Runner
	FS                *system.FS
	Distro            string // kubeadm or k3s
	CNI               string
	Mirrors           []string
	ImageRepository   string
	SkipFirewall      bool
	SELinuxPermissive bool
}

// Run executes all preflight steps.
func (p *Preflight) Run() error {
	if !p.OS.IsRoot && !p.R.DryRun {
		return fmt.Errorf("此操作需要 root 权限，请使用 sudo 运行")
	}
	if !p.OS.HasSystemd() {
		logx.Warnf("未检测到 systemd，部分操作可能无法自动完成")
	}
	logx.Header("Kubernetes 系统预检")

	if err := p.checkKernelAndHostname(); err != nil {
		return err
	}
	p.checkCgroup()
	if err := p.disableSwap(); err != nil {
		return err
	}
	if err := p.loadModules(); err != nil {
		return err
	}
	if err := p.applySysctl(); err != nil {
		return err
	}
	if err := p.configureRuntime(); err != nil {
		return err
	}
	if err := p.configureFirewall(); err != nil {
		return err
	}
	if err := p.configureSELinux(); err != nil {
		return err
	}
	p.checkTimeSync()

	logx.Successf("Kubernetes 预检完成")
	return nil
}

func (p *Preflight) checkKernelAndHostname() error {
	if p.OS.Kernel != "" {
		release := strings.SplitN(p.OS.Kernel, "-", 2)[0]
		parts := strings.Split(release, ".")
		if len(parts) >= 2 {
			major, _ := strconv.Atoi(parts[0])
			minor, _ := strconv.Atoi(parts[1])
			if major < 4 || (major == 4 && minor < 19) {
				logx.Warnf("内核版本 %s 较旧，建议使用 5.4 或更高版本", p.OS.Kernel)
			}
		}
	}
	hostname, err := os.Hostname()
	if err == nil {
		if ok, _ := regexp.MatchString(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`, hostname); !ok {
			logx.Warnf("主机名 %q 不是合法的小写 DNS 名称，请执行: hostnamectl set-hostname <name>", hostname)
		}
	}
	return nil
}

// CommentSwap comments active swap entries in /etc/fstab.
func CommentSwap(content string) (string, int) {
	lines := strings.Split(content, "\n")
	count := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 3 && fields[2] == "swap" {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + "# disabled by mkbox: " + trimmed
			count++
		}
	}
	return strings.Join(lines, "\n"), count
}

// checkCgroup warns when the host only ships cgroup v1, which Kubernetes 1.35
// refuses to use unless failCgroupV1 is overridden.
func (p *Preflight) checkCgroup() {
	if system.HasCgroupV2() {
		return
	}
	version := ""
	if p.R != nil {
		if out, err := p.R.Output("kubeadm", "version", "-o", "short"); err == nil {
			version = out
		}
	}
	if NeedsCgroupV1Fallback(version, true) {
		logx.Warnf("系统仅支持 cgroups v1，Kubernetes 1.35 起默认拒绝启动 kubelet；mkbox 会自动写入 failCgroupV1: false 并跳过 SystemVerification 预检，长期请迁移到 cgroups v2")
		return
	}
	logx.Warnf("系统仅支持 cgroups v1，升级到 Kubernetes 1.35 前需迁移到 cgroups v2，否则 kubelet 将无法启动")
}

func (p *Preflight) disableSwap() error {
	data, err := os.ReadFile("/proc/swaps")
	if err != nil {
		logx.Warnf("无法读取 /proc/swaps，跳过 swap 检查")
		return nil
	}
	if len(strings.Split(strings.TrimSpace(string(data)), "\n")) <= 1 {
		logx.Infof("swap 未启用")
	} else {
		logx.Infof("关闭 swap")
		if err := p.R.Run("swapoff", "-a"); err != nil {
			return fmt.Errorf("关闭 swap 失败: %w", err)
		}
		fstab, err := os.ReadFile("/etc/fstab")
		if err == nil {
			updated, n := CommentSwap(string(fstab))
			if n > 0 {
				if err := p.FS.Write("/etc/fstab", []byte(updated), 0o644); err != nil {
					return err
				}
				logx.Successf("已在 /etc/fstab 中注释 %d 个 swap 条目", n)
			}
		}
	}
	return nil
}

func (p *Preflight) loadModules() error {
	modules := []string{"overlay", "br_netfilter"}
	content := "# Managed by makebox (mkbox)\n" + strings.Join(modules, "\n") + "\n"
	if err := p.FS.Write(ModulesFile, []byte(content), 0o644); err != nil {
		return err
	}
	for _, m := range modules {
		if err := p.R.Run("modprobe", m); err != nil {
			return fmt.Errorf("加载内核模块 %s 失败: %w", m, err)
		}
	}
	logx.Successf("已加载内核模块: %s", strings.Join(modules, ", "))
	return nil
}

func (p *Preflight) applySysctl() error {
	content := strings.Join([]string{
		"# Managed by makebox (mkbox)",
		"net.bridge.bridge-nf-call-iptables = 1",
		"net.bridge.bridge-nf-call-ip6tables = 1",
		"net.ipv4.ip_forward = 1",
		"net.ipv4.conf.all.forwarding = 1",
		"fs.inotify.max_user_instances = 8192",
		"fs.inotify.max_user_watches = 524288",
		"vm.swappiness = 0",
		"",
	}, "\n")
	if err := p.FS.Write(SysctlFile, []byte(content), 0o644); err != nil {
		return err
	}
	if err := p.R.Run("sysctl", "--system"); err != nil {
		return fmt.Errorf("应用 sysctl 配置失败: %w", err)
	}
	logx.Successf("已配置 Kubernetes 所需 sysctl 参数")
	return nil
}

func (p *Preflight) configureRuntime() error {
	if p.Distro == "k3s" {
		if err := p.FS.Write(K3sRegistriesPath, []byte(K3sRegistriesYAML(p.Mirrors)), 0o644); err != nil {
			return err
		}
		if execx.Has("k3s") && p.OS.HasSystemd() {
			if err := p.R.Run("systemctl", "restart", "k3s"); err != nil {
				logx.Warnf("重启 k3s 使镜像配置生效失败: %v", err)
			}
		}
		logx.Successf("已写入 k3s 镜像加速配置")
		return nil
	}
	if !containerd.IsInstalled() {
		logx.Warnf("containerd 未安装，将在 mkbox k8s install 时安装并配置")
		return nil
	}
	mgr := containerd.NewManager(p.R, p.FS, p.OS)
	if err := mgr.Configure(p.Mirrors, p.ImageRepository); err != nil {
		return err
	}
	return mgr.Restart()
}

// RequiredPorts returns the firewall ports needed by Kubernetes.
func RequiredPorts(cni string, k3s bool) []string {
	ports := []string{
		"6443/tcp", "2379-2380/tcp", "10250/tcp", "10257/tcp", "10259/tcp",
		"30000-32767/tcp",
	}
	switch cni {
	case "calico":
		ports = append(ports, "179/tcp", "4789/udp", "5473/tcp", "9099/tcp")
	default:
		ports = append(ports, "8472/udp")
	}
	if k3s {
		ports = append(ports, "51820/udp", "51821/udp")
	}
	return ports
}

// UFWPort converts a range like 30000-32767/tcp into UFW's colon syntax.
func UFWPort(port string) string {
	proto := ""
	if i := strings.Index(port, "/"); i >= 0 {
		proto = port[i:]
		port = port[:i]
	}
	port = strings.ReplaceAll(port, "-", ":")
	return port + proto
}

func (p *Preflight) configureFirewall() error {
	if p.SkipFirewall {
		logx.Warnf("已跳过防火墙配置，请手动放行 Kubernetes 端口")
		return nil
	}
	ports := RequiredPorts(p.CNI, p.Distro == "k3s")

	if execx.Has("firewall-cmd") {
		if out, err := p.R.Output("firewall-cmd", "--state"); err == nil && strings.TrimSpace(out) == "running" {
			logx.Infof("检测到 firewalld，放行 Kubernetes 端口")
			for _, port := range ports {
				if err := p.R.Run("firewall-cmd", "--permanent", "--add-port="+port); err != nil {
					logx.Warnf("放行端口 %s 失败: %v", port, err)
				}
			}
			if err := p.R.Run("firewall-cmd", "--permanent", "--add-masquerade"); err != nil {
				logx.Warnf("启用 firewalld masquerade 失败: %v", err)
			}
			if err := p.R.Run("firewall-cmd", "--reload"); err != nil {
				logx.Warnf("重载 firewalld 失败: %v", err)
			}
			return nil
		}
	}

	if execx.Has("ufw") {
		if out, err := p.R.Output("ufw", "status"); err == nil && strings.Contains(out, "Status: active") {
			logx.Infof("检测到 ufw，放行 Kubernetes 端口")
			for _, port := range ports {
				if err := p.R.Run("ufw", "allow", UFWPort(port)); err != nil {
					logx.Warnf("放行端口 %s 失败: %v", port, err)
				}
			}
			if err := p.enableUFWForwarding(); err != nil {
				logx.Warnf("开启 ufw 转发失败: %v", err)
			}
			return nil
		}
	}
	logx.Infof("未检测到运行中的 firewalld/ufw，跳过防火墙配置")
	return nil
}

func (p *Preflight) enableUFWForwarding() error {
	data, err := os.ReadFile("/etc/default/ufw")
	if err != nil {
		return err
	}
	updated := strings.ReplaceAll(string(data), `DEFAULT_FORWARD_POLICY="DROP"`, `DEFAULT_FORWARD_POLICY="ACCEPT"`)
	if updated == string(data) {
		return nil
	}
	if err := p.FS.Write("/etc/default/ufw", []byte(updated), 0o644); err != nil {
		return err
	}
	return p.R.Run("ufw", "reload")
}

// SetSELinuxPermissive rewrites /etc/selinux/config and returns changed.
func SetSELinuxPermissive(content string) (string, bool) {
	updated := regexp.MustCompile(`(?m)^\s*SELINUX=enforcing\s*$`).ReplaceAllString(content, "SELINUX=permissive")
	return updated, updated != content
}

func (p *Preflight) configureSELinux() error {
	if !execx.Has("selinuxenabled") {
		return nil
	}
	if err := p.R.Run("selinuxenabled"); err != nil {
		return nil
	}
	if !p.SELinuxPermissive {
		logx.Warnf("SELinux 处于 enforcing 模式，如遇 kubelet 权限问题可运行: mkbox k8s preflight --selinux-permissive")
		return nil
	}
	logx.Infof("将 SELinux 切换为 permissive")
	if err := p.R.Run("setenforce", "0"); err != nil {
		logx.Warnf("setenforce 0 失败: %v", err)
	}
	if data, err := os.ReadFile("/etc/selinux/config"); err == nil {
		updated, changed := SetSELinuxPermissive(string(data))
		if changed {
			if err := p.FS.Write("/etc/selinux/config", []byte(updated), 0o644); err != nil {
				return err
			}
		}
	}
	logx.Warnf("SELinux permissive 仅在重启前生效状态，建议在生产环境使用合适的策略")
	return nil
}

func (p *Preflight) checkTimeSync() {
	if !p.OS.HasSystemd() || p.R.DryRun {
		return
	}
	out, err := p.R.Output("timedatectl", "show", "-p", "NTPSynchronized", "--value")
	if err == nil && strings.TrimSpace(out) != "yes" {
		logx.Warnf("系统时间未同步，请启用 chrony/systemd-timesyncd 以避免证书与令牌问题")
	}
}
