package reconcile

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/k8s"
	"github.com/makebox/makebox/internal/system"
)

// BuildPlan compares the desired profile with an observed host snapshot.
func BuildPlan(profile Profile, obs Observed, osInfo *system.OS, opts PlanOptions) (Plan, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	hostname, _ := os.Hostname()
	p := Plan{
		GeneratedAt: opts.Now.UTC().Format(time.RFC3339),
		Scope:       profile.Scope,
		Profile:     profile,
		Observed:    obs,
	}
	if osInfo != nil {
		p.Host = HostInfo{
			Hostname: hostname,
			OS:       osInfo.DisplayName(),
			Arch:     osInfo.Arch,
			Kernel:   osInfo.Kernel,
			IsRoot:   osInfo.IsRoot,
		}
	} else {
		p.Host = HostInfo{Hostname: hostname}
	}
	add := func(a Action) { p.Actions = append(p.Actions, a) }
	warn := func(format string, args ...any) { p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...)) }

	if profile.Scope.includesDocker() {
		installPlanned := false
		if !obs.DockerInstalled {
			add(Action{
				ID:         "docker.install",
				Title:      fmt.Sprintf("安装 Docker Engine（软件源 %s）", profile.Docker.InstallMirror),
				Reason:     "未检测到 docker CLI",
				Risk:       "medium",
				Reversible: false,
				Details:    []string{"安装 docker-ce、containerd.io、buildx、compose 插件", "启用并启动 docker 服务"},
			})
			installPlanned = true
		} else if !obs.DockerRunning {
			add(Action{
				ID:         "docker.start",
				Title:      "启动并启用 Docker",
				Reason:     "docker 已安装但服务未运行",
				Risk:       "low",
				Reversible: true,
			})
		}

		desiredDaemon, err := DesiredDaemonJSON(obs.daemonJSON, profile.Docker)
		if err != nil {
			return p, fmt.Errorf("无法计算 Docker 期望配置: %w", err)
		}
		if desiredDaemon != obs.daemonJSON && (obs.DockerInstalled || installPlanned) {
			add(Action{
				ID:         "docker.configure",
				Title:      "写入 Docker daemon 配置",
				Reason:     "daemon.json 与期望的镜像加速/日志/数据目录不一致",
				Risk:       "medium",
				Reversible: true,
				Details:    []string{"保留未知键，合并 registry-mirrors、log-opts、data-root", "重启 Docker 使配置生效"},
			})
			p.Diffs = append(p.Diffs, FileDiff{
				Path:    docker.DaemonJSON,
				Exists:  obs.daemonJSON != "",
				Changed: true,
				Unified: DiffUnified(docker.DaemonJSON, obs.daemonJSON, desiredDaemon),
			})
		}

		desiredProxy := DesiredDockerProxy(profile.Proxy)
		if desiredProxy != obs.dockerProxy && (obs.DockerInstalled || installPlanned) {
			id, title := "docker.proxy.set", "配置 Docker daemon 代理"
			if desiredProxy == "" {
				id, title = "docker.proxy.clear", "移除 Docker daemon 代理"
			}
			add(Action{
				ID:         id,
				Title:      title,
				Reason:     "Docker daemon 代理 drop-in 与期望状态不一致",
				Risk:       "medium",
				Reversible: true,
			})
			p.Diffs = append(p.Diffs, FileDiff{
				Path:    docker.ProxyConf,
				Exists:  obs.dockerProxy != "",
				Changed: true,
				Unified: DiffUnified(docker.ProxyConf, RedactProxyText(obs.dockerProxy), RedactProxyText(desiredProxy)),
				Note:    "代理凭证已脱敏",
			})
		}
	}

	if profile.Scope.includesK8s() {
		distro := profile.K8s.Distro
		if distro == "" {
			distro = "kubeadm"
		}
		installPlanned := false
		if !obs.ClusterInitialized {
			switch distro {
			case "kubeadm":
				if !obs.KubeadmInstalled || !obs.KubeletInstalled || !obs.ContainerdInstalled {
					add(Action{
						ID:         "k8s.install",
						Title:      fmt.Sprintf("安装 Kubernetes（kubeadm，版本 %s）", profile.K8s.Version),
						Reason:     "缺少 kubeadm/kubelet/containerd 组件",
						Risk:       "high",
						Reversible: false,
						Details:    []string{"安装并配置 containerd（SystemdCgroup=true）", "安装 kubeadm/kubelet/kubectl", "写入软件源 " + profile.K8s.RepoMirror},
					})
					installPlanned = true
				}
			case "k3s":
				if !obs.K3sInstalled {
					add(Action{
						ID:         "k8s.install",
						Title:      fmt.Sprintf("安装 k3s（版本 %s）", profile.K8s.Version),
						Reason:     "未检测到 k3s",
						Risk:       "high",
						Reversible: false,
						Details:    []string{"下载并安装 k3s（不会自动启动）", "写入镜像加速配置"},
					})
					installPlanned = true
				}
			default:
				return p, fmt.Errorf("未知 Kubernetes 发行版 %q（可选 kubeadm/k3s）", distro)
			}

			if distro == "kubeadm" && !installPlanned && obs.ContainerdInstalled {
				desiredCfg, err := DesiredContainerdConfig(obs.containerdCfg, profile.K8s)
				if err != nil {
					return p, fmt.Errorf("无法计算 containerd 期望配置: %w（请备份后删除 /etc/containerd/config.toml 再重试）", err)
				}
				cfgChanged := desiredCfg != obs.containerdCfg
				hostsDesired := DesiredContainerdHosts(profile.Docker)
				hostsChanged := hostsDesired != obs.containerdHosts
				if cfgChanged || hostsChanged {
					add(Action{
						ID:         "containerd.configure",
						Title:      "配置 containerd（SystemdCgroup/镜像加速）",
						Reason:     "containerd 配置与 Kubernetes 期望不一致",
						Risk:       "medium",
						Reversible: true,
						Details:    []string{"设置 SystemdCgroup=true", "写入 CRI registry config_path 与 Docker Hub hosts.toml", "重启 containerd"},
					})
					if cfgChanged {
						p.Diffs = append(p.Diffs, FileDiff{
							Path:    "/etc/containerd/config.toml",
							Exists:  obs.containerdCfg != "",
							Changed: true,
							Unified: DiffUnified("/etc/containerd/config.toml", obs.containerdCfg, desiredCfg),
						})
					}
					if hostsChanged {
						p.Diffs = append(p.Diffs, FileDiff{
							Path:    "/etc/containerd/certs.d/docker.io/hosts.toml",
							Exists:  obs.containerdHosts != "",
							Changed: true,
							Unified: DiffUnified("/etc/containerd/certs.d/docker.io/hosts.toml", obs.containerdHosts, hostsDesired),
						})
					}
				}
			}

			if distro == "kubeadm" && (obs.ContainerdInstalled || installPlanned) {
				desiredProxy := DesiredDockerProxy(profile.Proxy)
				if desiredProxy != obs.containerdProxy {
					id, title := "containerd.proxy.set", "配置 containerd 代理"
					if desiredProxy == "" {
						id, title = "containerd.proxy.clear", "移除 containerd 代理"
					}
					add(Action{
						ID:         id,
						Title:      title,
						Reason:     "containerd 代理 drop-in 与期望状态不一致",
						Risk:       "medium",
						Reversible: true,
					})
					p.Diffs = append(p.Diffs, FileDiff{
						Path:    "/etc/systemd/system/containerd.service.d/http-proxy.conf",
						Exists:  obs.containerdProxy != "",
						Changed: true,
						Unified: DiffUnified("/etc/systemd/system/containerd.service.d/http-proxy.conf", RedactProxyText(obs.containerdProxy), RedactProxyText(desiredProxy)),
						Note:    "代理凭证已脱敏",
					})
				}
			}

			if !opts.SkipPreflight && !profile.K8s.SkipPreflight {
				add(Action{
					ID:         "k8s.preflight",
					Title:      "执行 Kubernetes 系统预检",
					Reason:     "关闭 swap、加载内核模块、配置 sysctl 与防火墙",
					Risk:       "high",
					Reversible: false,
					Details:    []string{"swapoff -a 并注释 /etc/fstab 中的 swap", "加载 overlay/br_netfilter", "写入 sysctl 与模块配置", "放行 CNI/控制面端口"},
				})
			}
			add(Action{
				ID:         "k8s.init",
				Title:      fmt.Sprintf("初始化 Kubernetes 控制平面（%s）", distro),
				Reason:     "未检测到已初始化的集群",
				Risk:       "high",
				Reversible: false,
				Details:    []string{"生成 kubeadm/k3s 配置", "拉取控制面镜像", "初始化 etcd 与控制面", "安装 CNI " + profile.K8s.CNI},
			})
			if obs.SwapEnabled {
				warn("swap 当前已启用；预检会关闭 swap 并修改 /etc/fstab")
			}
		} else {
			running := obs.KubeletRunning
			if distro == "k3s" {
				running = obs.K3sRunning
			}
			if !running {
				add(Action{
					ID:         "k8s.start",
					Title:      "启动 Kubernetes 节点服务",
					Reason:     "检测到已初始化的集群，但节点服务未运行",
					Risk:       "low",
					Reversible: true,
				})
			}
			warn("已检测到初始化过的集群；plan/apply 不会重装或重新初始化它")
		}

		if obs.ClusterInitialized && profile.Scope.includesDocker() && obs.DockerInstalled {
			warn("Docker 与 Kubernetes 共享 containerd 时，重启 containerd 会同时影响容器与 Pod；变更前请 drain 节点")
		}
	}

	if profile.Proxy.URL != "" && !profile.Scope.includesDocker() && profile.Scope.includesK8s() {
		warn("Kubernetes scope 仅管理 containerd 代理；如需 Docker 代理请使用 --scope all")
	}
	return p, nil
}

// KubeadmInitOptions converts a profile into k8s.InitOptions.
func KubeadmInitOptions(p Profile) k8s.InitOptions {
	return k8s.InitOptions{
		Distro:               p.K8s.Distro,
		Version:              p.K8s.Version,
		ImageRepository:      p.K8s.ImageRepository,
		PodCIDR:              p.K8s.PodCIDR,
		ServiceCIDR:          p.K8s.ServiceCIDR,
		CNI:                  p.K8s.CNI,
		APIAddress:           p.K8s.APIAddress,
		ControlPlaneEndpoint: p.K8s.ControlPlaneEndpoint,
		SkipPreflight:        true, // preflight is an explicit action in the plan
		SkipFirewall:         p.K8s.SkipFirewall,
		SkipAddons:           p.K8s.SkipAddons,
		Mirrors:              p.Docker.Mirrors,
		GHProxy:              p.K8s.GHProxy,
		K3sMirror:            false,
	}
}

// ServiceForAction returns the systemd services affected by an action.
func ServiceForAction(id string) []string {
	switch {
	case strings.HasPrefix(id, "docker."):
		return []string{ServiceDocker}
	case strings.HasPrefix(id, "containerd."):
		return []string{ServiceContainerd}
	case strings.HasPrefix(id, "k8s."):
		return []string{ServiceContainerd, ServiceKubelet, ServiceK3s}
	}
	return nil
}
