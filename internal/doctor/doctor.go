// Package doctor inspects the local host and reports whether Docker and
// Kubernetes can be installed safely. All checks are read-only.
package doctor

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/makebox/makebox/internal/config"
	"github.com/makebox/makebox/internal/containerd"
	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/k8s"
	"github.com/makebox/makebox/internal/netcheck"
	"github.com/makebox/makebox/internal/registry"
	"github.com/makebox/makebox/internal/system"
)

// Level classifies a check result.
type Level string

const (
	OK   Level = "ok"
	Warn Level = "warn"
	Fail Level = "fail"
	Info Level = "info"
)

// Check is one diagnostic result.
type Check struct {
	Name   string
	Level  Level
	Detail string
	Hint   string
}

// Report is the result of Run.
type Report struct {
	OS     *system.OS
	Checks []Check
}

// Options controls optional network checks.
type Options struct {
	Net        bool
	Timeout    time.Duration
	ConfigPath string
}

var (
	hostnameRe      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	systemdCgroupRe = regexp.MustCompile(`(?m)^\s*SystemdCgroup\s*=\s*true\s*$`)
)

// Run performs the local diagnostic checks.
func Run(o *system.OS, r *execx.Runner, opts Options) *Report {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	rep := &Report{OS: o}
	add := func(name string, level Level, detail, hint string) {
		rep.Checks = append(rep.Checks, Check{Name: name, Level: level, Detail: detail, Hint: hint})
	}

	// Host and operating system.
	if o.IsSupported() {
		add("操作系统", OK, fmt.Sprintf("%s（%s / %s）", o.DisplayName(), o.Family, o.PkgMgr), "")
	} else {
		add("操作系统", Fail, fmt.Sprintf("%s（未识别的发行版）", o.DisplayName()),
			"mkbox 支持 Debian/Ubuntu、RHEL/Rocky/Alma/Fedora、openSUSE、Arch、Alpine")
	}
	if o.IsRoot {
		add("执行权限", OK, "root", "")
	} else {
		add("执行权限", Info, "当前不是 root 用户", "安装/修改配置时请使用 sudo；只读命令可直接运行")
	}
	if kmaj, kmin, ok := parseKernel(o.Kernel); ok {
		level := OK
		hint := ""
		if kmaj < 4 || (kmaj == 4 && kmin < 19) {
			level = Warn
			hint = "Kubernetes 建议使用 4.19 及以上内核"
		}
		add("内核版本", level, o.Kernel, hint)
	} else {
		add("内核版本", Warn, o.Kernel, "无法解析内核版本")
	}
	switch runtime.GOARCH {
	case "amd64", "arm64", "arm":
		add("CPU 架构", OK, runtime.GOARCH, "")
	default:
		add("CPU 架构", Warn, runtime.GOARCH, "推荐使用 amd64、arm64 或 armv7")
	}
	if o.HasSystemd() {
		add("服务管理", OK, "systemd", "")
	} else if o.ServiceMgr != "" {
		add("服务管理", Warn, o.ServiceMgr, "部分 systemd 专属功能不可用；Kubernetes 建议使用 k3s")
	} else {
		add("服务管理", Fail, "未检测到 systemd/openrc", "无法自动管理 Docker 服务")
	}

	// Capacity.
	if mb := system.MemoryMB(); mb > 0 {
		switch {
		case mb < 1024:
			add("内存", Fail, fmt.Sprintf("%d MiB", mb), "Kubernetes 控制面至少需要约 1.7 GiB")
		case mb < 2048:
			add("内存", Warn, fmt.Sprintf("%d MiB", mb), "测试环境可用；生产控制面建议 4 GiB 以上")
		default:
			add("内存", OK, fmt.Sprintf("%d MiB", mb), "")
		}
	}
	if gb := system.DiskFreeGB("/var"); gb > 0 {
		switch {
		case gb < 3:
			add("磁盘空间（/var）", Fail, fmt.Sprintf("%.1f GiB 可用", gb), "至少预留 10 GiB")
		case gb < 10:
			add("磁盘空间（/var）", Warn, fmt.Sprintf("%.1f GiB 可用", gb), "镜像和集群数据增长较快，建议 20 GiB 以上")
		default:
			add("磁盘空间（/var）", OK, fmt.Sprintf("%.1f GiB 可用", gb), "")
		}
	}
	if sw := swapEnabled(); sw {
		add("Swap", Warn, "已启用", "Kubernetes 要求关闭 swap；`mkbox k8s preflight` 会自动处理")
	} else {
		add("Swap", OK, "未启用", "")
	}

	// Kernel networking prerequisites.
	missing := make([]string, 0, 2)
	for _, mod := range []string{"overlay", "br_netfilter"} {
		if _, err := os.Stat("/sys/module/" + mod); err != nil {
			missing = append(missing, mod)
		}
	}
	if len(missing) > 0 {
		add("内核模块", Warn, "未加载: "+strings.Join(missing, ", "), "运行 `mkbox k8s preflight` 可自动加载并持久化")
	} else {
		add("内核模块", OK, "overlay, br_netfilter", "")
	}
	if v, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
		if strings.TrimSpace(string(v)) == "1" {
			add("IP 转发", OK, "已开启", "")
		} else {
			add("IP 转发", Warn, "未开启", "运行 `mkbox k8s preflight` 会自动开启")
		}
	}
	if v, err := os.ReadFile("/proc/sys/net/bridge/bridge-nf-call-iptables"); err == nil {
		if strings.TrimSpace(string(v)) != "1" {
			add("网桥过滤", Warn, "bridge-nf-call-iptables 未开启", "运行 `mkbox k8s preflight` 会自动修正")
		}
	}

	// Hostname.
	if host, err := os.Hostname(); err != nil {
		add("主机名", Warn, "无法读取", "请检查 /etc/hostname")
	} else if !hostnameRe.MatchString(strings.ToLower(host)) || len(host) > 253 {
		add("主机名", Warn, host, "主机名必须为小写字母、数字、'-' 或 '.'，Kubernetes 会拒绝不合规名称")
	} else {
		addrs, lookupErr := net.LookupHost(host)
		if lookupErr != nil || len(addrs) == 0 {
			add("主机名", Warn, host, "无法解析本机主机名，请将主机名加入 /etc/hosts")
		} else {
			add("主机名", OK, host, "")
		}
	}

	// Security and time.
	if o.HasSystemd() {
		if out, err := r.Output("timedatectl", "show", "-p", "NTPSynchronized", "--value"); err == nil && out != "" {
			if strings.TrimSpace(out) == "no" {
				add("时间同步", Warn, "NTP 未同步", "证书和 etcd 依赖准确时间，请启用 chrony/systemd-timesyncd")
			} else {
				add("时间同步", OK, "已同步", "")
			}
		}
	}
	if isActive(r, "firewalld") {
		add("防火墙", Warn, "firewalld 运行中", "`mkbox k8s preflight` 会开放 Kubernetes 所需端口")
	} else if ufwActive(r) {
		add("防火墙", Warn, "ufw 已启用", "`mkbox k8s preflight` 会开放 Kubernetes 所需端口")
	} else {
		add("防火墙", Info, "未检测到活动的 firewalld/ufw", "")
	}
	if out, err := r.Output("getenforce"); err == nil && strings.TrimSpace(out) == "Enforcing" {
		add("SELinux", Warn, "Enforcing", "如集群异常可将 /etc/selinux/config 设为 permissive，或使用 k3s")
	}

	// Container runtime.
	st := docker.GetStatus(r)
	switch {
	case st.Installed && st.Running:
		detail := strings.TrimSpace(st.Version)
		if len(st.Mirrors) > 0 {
			detail += fmt.Sprintf("，%d 个镜像加速", len(st.Mirrors))
		} else {
			detail += "，未配置镜像加速"
		}
		add("Docker", OK, detail, "")
	case st.Installed:
		add("Docker", Warn, "已安装但服务未运行", "运行 `sudo systemctl start docker` 或 `mkbox docker install`")
	default:
		add("Docker", Info, "未安装", "运行 `sudo mkbox docker install` 一键安装")
	}
	if st.Installed && st.Running {
		if out, err := r.Output("docker", "info", "--format", "{{.CgroupDriver}}"); err == nil && strings.TrimSpace(out) == "cgroupfs" {
			add("Docker Cgroup", Warn, "cgroupfs", "Kubernetes 更推荐 systemd cgroup driver")
		}
	}
	if execx.Has("containerd") {
		state := "已安装"
		if o.HasSystemd() && isActive(r, "containerd") {
			state = "运行中"
		}
		warn := false
		if data, err := os.ReadFile("/etc/containerd/config.toml"); err == nil {
			if !systemdCgroupRe.Match(data) {
				warn = true
			}
		}
		if warn {
			add("containerd", Warn, state+", SystemdCgroup 未启用", "`mkbox k8s install` 会自动修正")
		} else {
			add("containerd", OK, state, "")
		}
		// Packages often disable the CRI plugin, which leaves kubeadm without
		// a runtime.v1.RuntimeService to talk to.
		if data, err := os.ReadFile(containerd.ConfigPath); err == nil && containerd.DisablesCRI(string(data)) {
			add("containerd CRI", Warn, "disabled_plugins 含 cri",
				"kubeadm 会报 unknown service runtime.v1.RuntimeService；`mkbox k8s install` 会自动修正")
		}
	}

	// Kubernetes.
	kubeadmVersion := ""
	if execx.Has("kubeadm") {
		if out, err := r.Output("kubeadm", "version", "-o", "short"); err == nil {
			kubeadmVersion = firstLine(out)
		}
		if kubeadmVersion == "" {
			kubeadmVersion = "已安装"
		}
		add("kubeadm", OK, kubeadmVersion, "")
	} else {
		add("kubeadm", Info, "未安装", "运行 `sudo mkbox k8s install`")
	}
	if execx.Has("kubelet") {
		state := "已安装"
		if o.HasSystemd() && isActive(r, "kubelet") {
			state = "运行中"
		}
		add("kubelet", OK, state, "")
	}
	if execx.Has("kubectl") {
		if out, err := r.Output("kubectl", "version", "--client", "--short"); err == nil {
			add("kubectl", OK, firstLine(out), "")
		}
	}

	// Cluster.
	kc := k8s.DetectClusterKubeconfig()
	if kc == "" {
		if execx.Has("k3s") && isActive(r, "k3s") {
			add("集群", Warn, "k3s 服务运行中，但未找到 kubeconfig", "检查 /etc/rancher/k3s/k3s.yaml")
		} else {
			add("集群", Info, "未检测到运行中的集群", "运行 `sudo mkbox k8s up` 一键创建")
		}
	} else if !execx.Has("kubectl") {
		add("集群", Warn, "找到 kubeconfig: "+kc, "未安装 kubectl，无法查询节点状态")
	} else {
		out, err := r.Output("kubectl", "--kubeconfig", kc, "get", "nodes", "--no-headers")
		if err != nil {
			add("集群", Warn, "无法访问集群（kubeconfig: "+kc+"）", "确认控制面服务和证书状态")
		} else {
			total, ready := countNodes(out)
			level := OK
			if total == 0 || ready < total {
				level = Warn
			}
			add("集群", level, fmt.Sprintf("%d 个节点，%d 个 Ready", total, ready), "")
		}
	}

	// Ports only matter when no control plane is listening already.
	if kc == "" {
		busy := make([]string, 0, 2)
		for _, p := range []int{6443, 10250} {
			if system.PortInUse(p) {
				busy = append(busy, strconv.Itoa(p))
			}
		}
		if len(busy) > 0 {
			add("端口占用", Warn, strings.Join(busy, ", "), "Kubernetes 需要这些端口；请停止占用进程")
		}
	}

	// mkbox configuration.
	cfg, cfgErr := config.Load(opts.ConfigPath)
	if cfgErr != nil {
		add("mkbox 配置", Fail, cfgErr.Error(), "修正配置文件或删除后重新生成")
	} else {
		detail := fmt.Sprintf("k8s %s / %s，镜像源 %s", cfg.K8s.Version, cfg.K8s.Distro, cfg.K8s.RepoMirror)
		add("mkbox 配置", OK, detail, "")
		if cfg.Proxy.HTTP != "" || cfg.Proxy.HTTPS != "" {
			add("系统代理", Info, cfg.EffectiveProxy(), "代理仅用于下载，不会改变容器内流量")
		}
	}

	// Optional network checks.
	if opts.Net {
		results := netcheck.CheckAll(context.Background(), netcheck.DefaultEndpoints, opts.Timeout)
		ok := netcheck.OKCount(results)
		level := OK
		if ok < len(results) {
			level = Warn
		}
		add("外网连通性", level, fmt.Sprintf("%d/%d 个端点可达", ok, len(results)), "查看 `mkbox net test` 了解详情")
		if cfgErr == nil {
			mirrors := registry.Candidates(nil, cfg.Registry.Mirrors)
			if len(mirrors) > 0 {
				mr := registry.CheckAll(context.Background(), mirrors, opts.Timeout, 6)
				working := registry.Working(mr)
				level := OK
				if len(working) == 0 {
					level = Warn
				}
				add("镜像加速器", level, fmt.Sprintf("%d/%d 个可用", len(working), len(mr)), "运行 `mkbox registry test` 查看延迟")
			}
		}
	}

	return rep
}

// Counts returns the number of checks per level.
func (r *Report) Counts() map[Level]int {
	counts := map[Level]int{}
	for _, c := range r.Checks {
		counts[c.Level]++
	}
	return counts
}

func parseKernel(v string) (int, int, bool) {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(leadingDigits(parts[0]))
	minor, err2 := strconv.Atoi(leadingDigits(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func swapEnabled() bool {
	data, err := os.ReadFile("/proc/swaps")
	if err != nil {
		return false
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n")) > 1
}

func isActive(r *execx.Runner, service string) bool {
	if !execx.Has("systemctl") {
		return false
	}
	out, err := r.Output("systemctl", "is-active", service)
	return err == nil && strings.TrimSpace(out) == "active"
}

func ufwActive(r *execx.Runner) bool {
	if !execx.Has("ufw") {
		return false
	}
	out, err := r.Output("ufw", "status")
	return err == nil && strings.Contains(out, "Status: active")
}

func countNodes(out string) (total, ready int) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		total++
		if fields[1] == "Ready" || strings.HasPrefix(fields[1], "Ready,") {
			ready++
		}
	}
	return total, ready
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
