package cli

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/execx"
	"github.com/makebox/makebox/internal/k8s"
	"github.com/makebox/makebox/internal/logx"
)

type clusterFlags struct {
	distro          string
	version         string
	repoMirror      string
	imageRepository string
	mirrors         []string
	noRegistry      bool
	k3sMirror       bool

	cni               string
	podCIDR           string
	serviceCIDR       string
	apiAddress        string
	endpoint          string
	skipPreflight     bool
	skipFirewall      bool
	selinuxPermissive bool
	skipAddons        bool
	k3sClusterInit    bool
	ghProxy           string
	timeout           time.Duration
}

type clusterSettings struct {
	Distro          string
	Version         string
	RepoMirror      string
	ImageRepository string
	CNI             string
	PodCIDR         string
	ServiceCIDR     string
	Mirrors         []string
	K3sMirror       bool
}

func (f *clusterFlags) bindCommon(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.distro, "distro", "", "Kubernetes 发行版: kubeadm 或 k3s（默认取配置）")
	fl.StringVar(&f.version, "version", "", "Kubernetes 版本，如 1.35 或 v1.35.2（默认取配置）")
	fl.StringVar(&f.repoMirror, "repo-mirror", "", "软件源: official 或 aliyun（默认取配置）")
	fl.StringVar(&f.imageRepository, "image-repository", "", "Kubernetes 镜像仓库（默认取配置）")
	fl.StringArrayVar(&f.mirrors, "registry-mirror", nil, "Docker Hub 镜像加速地址，可重复")
	fl.BoolVar(&f.noRegistry, "no-registry-mirror", false, "不配置容器镜像加速")
	fl.BoolVar(&f.k3sMirror, "k3s-mirror", false, "使用 Rancher 中国镜像下载 k3s")
}

func (f *clusterFlags) bindInit(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.cni, "cni", "", "CNI 插件: flannel/calico/none（默认取配置）")
	fl.StringVar(&f.podCIDR, "pod-cidr", "", "Pod 网段（默认取配置）")
	fl.StringVar(&f.serviceCIDR, "service-cidr", "", "Service 网段（默认取配置）")
	fl.StringVar(&f.apiAddress, "apiserver-address", "", "API Server 监听/节点 IP（默认自动检测）")
	fl.StringVar(&f.endpoint, "control-plane-endpoint", "", "控制面 VIP/域名，如 vip:6443")
	fl.BoolVar(&f.skipPreflight, "skip-preflight", false, "跳过系统预检")
	fl.BoolVar(&f.skipFirewall, "skip-firewall", false, "不自动放行防火墙端口")
	fl.BoolVar(&f.selinuxPermissive, "selinux-permissive", false, "将 SELinux 切换为 permissive")
	fl.BoolVar(&f.skipAddons, "skip-addons", false, "不自动安装 CNI")
	fl.BoolVar(&f.k3sClusterInit, "k3s-cluster-init", false, "k3s 多控制面：启用 --cluster-init")
	fl.StringVar(&f.ghProxy, "gh-proxy", "", "GitHub 代理地址，用于下载插件清单")
	fl.DurationVar(&f.timeout, "timeout", 0, "等待节点就绪的超时（默认 5m/3m）")
}

func (f *clusterFlags) resolve(a *App) (clusterSettings, error) {
	cfg, err := a.cfgOrErr()
	if err != nil {
		return clusterSettings{}, err
	}
	mirrors, err := a.appliedMirrors(f.mirrors, f.noRegistry)
	if err != nil {
		return clusterSettings{}, err
	}
	s := clusterSettings{
		Distro:          pick(f.distro, cfg.K8s.Distro),
		Version:         pick(f.version, cfg.K8s.Version),
		RepoMirror:      pick(f.repoMirror, cfg.K8s.RepoMirror),
		ImageRepository: pick(f.imageRepository, cfg.K8s.ImageRepository),
		CNI:             pick(f.cni, cfg.K8s.CNI),
		PodCIDR:         pick(f.podCIDR, cfg.K8s.PodCIDR),
		ServiceCIDR:     pick(f.serviceCIDR, cfg.K8s.ServiceCIDR),
		Mirrors:         mirrors,
		K3sMirror:       f.k3sMirror,
	}
	if s.Distro != "kubeadm" && s.Distro != "k3s" {
		return s, fmt.Errorf("--distro 必须是 kubeadm 或 k3s")
	}
	if s.RepoMirror != "official" && s.RepoMirror != "aliyun" {
		return s, fmt.Errorf("--repo-mirror 必须是 official 或 aliyun")
	}
	if err := validateCNI(s.CNI); err != nil {
		return s, err
	}
	if s.Distro == "kubeadm" {
		if _, err := k8s.ReleaseSeries(s.Version); err != nil {
			return s, err
		}
	} else if err := k8s.ValidateK3sVersion(s.Version); err != nil {
		return s, err
	}
	for _, cidr := range []struct{ name, value string }{
		{"--pod-cidr", s.PodCIDR},
		{"--service-cidr", s.ServiceCIDR},
	} {
		if _, _, err := net.ParseCIDR(cidr.value); err != nil {
			return s, fmt.Errorf("%s 无效: %v", cidr.name, err)
		}
	}
	if f.apiAddress != "" && net.ParseIP(f.apiAddress) == nil {
		return s, fmt.Errorf("--apiserver-address 必须是合法 IP: %s", f.apiAddress)
	}
	if f.endpoint != "" {
		if _, err := k8s.NormalizeEndpoint(f.endpoint); err != nil {
			return s, err
		}
	}
	return s, nil
}

func (a *App) k8sCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "k8s",
		Aliases: []string{"kubernetes"},
		Short:   "安装、初始化与运维 Kubernetes 集群",
	}
	cmd.AddCommand(
		a.k8sPreflightCmd(),
		a.k8sInstallCmd(),
		a.k8sInitCmd(),
		a.k8sUpCmd(),
		a.k8sJoinCmd(),
		a.k8sJoinCommandCmd(),
		a.k8sAddonCmd(),
		a.k8sKubeconfigCmd(),
		a.k8sStatusCmd(),
		a.k8sResetCmd(),
	)
	return cmd
}

func (a *App) k8sPreflightCmd() *cobra.Command {
	var (
		distro     string
		cni        string
		imageRepo  string
		mirrors    []string
		noRegistry bool
		skipFW     bool
		selinux    bool
	)
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "关闭 swap、加载模块、配置 sysctl 与防火墙",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			distro = pick(distro, cfg.K8s.Distro)
			if distro != "kubeadm" && distro != "k3s" {
				return fmt.Errorf("--distro 必须是 kubeadm 或 k3s")
			}
			cni = pick(cni, cfg.K8s.CNI)
			if err := validateCNI(cni); err != nil {
				return err
			}
			mirrors, err = a.appliedMirrors(mirrors, noRegistry)
			if err != nil {
				return err
			}
			imageRepo = pick(imageRepo, cfg.K8s.ImageRepository)
			ok, err := a.confirm("即将修改 swap、内核参数、防火墙等系统配置，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			pf := &k8s.Preflight{
				OS:                a.OS,
				R:                 a.R,
				FS:                a.FS,
				Distro:            distro,
				CNI:               cni,
				Mirrors:           mirrors,
				ImageRepository:   imageRepo,
				SkipFirewall:      skipFW,
				SELinuxPermissive: selinux,
			}
			return pf.Run()
		},
	}
	cmd.Flags().StringVar(&distro, "distro", "", "kubeadm 或 k3s（默认取配置）")
	cmd.Flags().StringVar(&cni, "cni", "", "CNI 插件，用于计算防火墙端口")
	cmd.Flags().StringVar(&imageRepo, "image-repository", "", "pause 镜像仓库")
	cmd.Flags().StringArrayVar(&mirrors, "registry-mirror", nil, "Docker Hub 镜像加速地址")
	cmd.Flags().BoolVar(&noRegistry, "no-registry-mirror", false, "不改写镜像加速配置")
	cmd.Flags().BoolVar(&skipFW, "skip-firewall", false, "不自动放行防火墙端口")
	cmd.Flags().BoolVar(&selinux, "selinux-permissive", false, "将 SELinux 切换为 permissive")
	return cmd
}

func (a *App) k8sInstallCmd() *cobra.Command {
	var f clusterFlags
	cmd := &cobra.Command{
		Use:   "install",
		Short: "安装 kubeadm/kubelet/kubectl 或 k3s",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			s, err := f.resolve(a)
			if err != nil {
				return err
			}
			ok, err := a.confirm(fmt.Sprintf("即将安装 Kubernetes %s（%s），继续", s.Version, s.Distro))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			inst := &k8s.Installer{
				OS:              a.OS,
				R:               a.R,
				FS:              a.FS,
				Distro:          s.Distro,
				Version:         s.Version,
				RepoMirror:      s.RepoMirror,
				K3sMirror:       s.K3sMirror,
				Mirrors:         s.Mirrors,
				ImageRepository: s.ImageRepository,
			}
			return inst.Run()
		},
	}
	f.bindCommon(cmd)
	return cmd
}

func (a *App) k8sInitCmd() *cobra.Command {
	var f clusterFlags
	cmd := &cobra.Command{
		Use:   "init",
		Short: "初始化 Kubernetes 控制平面（组件需已安装）",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			s, err := f.resolve(a)
			if err != nil {
				return err
			}
			ok, err := a.confirm(fmt.Sprintf("即将初始化 Kubernetes 控制平面（%s），继续", s.Distro))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			return a.runInit(s, f)
		},
	}
	f.bindCommon(cmd)
	f.bindInit(cmd)
	return cmd
}

func (a *App) k8sUpCmd() *cobra.Command {
	var f clusterFlags
	cmd := &cobra.Command{
		Use:   "up",
		Short: "一键安装并初始化单节点 Kubernetes",
		Long:  "等价于 mkbox k8s install 后执行 mkbox k8s init，并自动安装 CNI。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			s, err := f.resolve(a)
			if err != nil {
				return err
			}
			ok, err := a.confirm(fmt.Sprintf("即将安装并初始化 Kubernetes %s（%s，CNI %s），继续", s.Version, s.Distro, s.CNI))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			inst := &k8s.Installer{
				OS:              a.OS,
				R:               a.R,
				FS:              a.FS,
				Distro:          s.Distro,
				Version:         s.Version,
				RepoMirror:      s.RepoMirror,
				K3sMirror:       s.K3sMirror,
				Mirrors:         s.Mirrors,
				ImageRepository: s.ImageRepository,
			}
			if err := inst.Run(); err != nil {
				return err
			}
			return a.runInit(s, f)
		},
	}
	f.bindCommon(cmd)
	f.bindInit(cmd)
	return cmd
}

func (a *App) runInit(s clusterSettings, f clusterFlags) error {
	init := &k8s.Initializer{
		OS: a.OS,
		R:  a.R,
		FS: a.FS,
		Opts: k8s.InitOptions{
			Distro:               s.Distro,
			Version:              s.Version,
			ImageRepository:      s.ImageRepository,
			PodCIDR:              s.PodCIDR,
			ServiceCIDR:          s.ServiceCIDR,
			CNI:                  s.CNI,
			APIAddress:           f.apiAddress,
			ControlPlaneEndpoint: f.endpoint,
			SkipPreflight:        f.skipPreflight,
			SkipFirewall:         f.skipFirewall,
			SELinuxPermissive:    f.selinuxPermissive,
			SkipAddons:           f.skipAddons,
			Mirrors:              s.Mirrors,
			GHProxy:              pick(f.ghProxy, a.Cfg.Network.GHProxy),
			K3sClusterInit:       f.k3sClusterInit,
			K3sMirror:            s.K3sMirror,
			Timeout:              f.timeout,
		},
	}
	res, err := init.Run()
	if err != nil {
		return err
	}
	logx.Header("初始化结果")
	if res.NodeIP != "" {
		logx.Item("节点 IP", res.NodeIP)
	}
	if res.Kubeconfig != "" {
		logx.Item("kubeconfig", res.Kubeconfig)
	}
	printWarnings(res.Warnings)
	if res.JoinCommand != "" {
		logx.Header("加入集群命令")
		logx.Raw("%s", res.JoinCommand)
		logx.Infof("控制面已将脚本保存在 %s 或 /etc/makebox/join-k3s.sh", k8s.JoinScriptPath)
	}
	logx.Successf("Kubernetes 初始化完成")
	return nil
}

type joinFlags struct {
	distro         string
	server         string
	token          string
	caHash         string
	certificateKey string
	criSocket      string
	fromFile       string
	nodeIP         string
	imageRepo      string
	cni            string
	mirrors        []string
	noRegistry     bool
	controlPlane   bool
	skipPreflight  bool
	k3sMirror      bool
}

func (a *App) k8sJoinCmd() *cobra.Command {
	var f joinFlags
	cmd := &cobra.Command{
		Use:   "join",
		Short: "将当前节点加入已有集群",
		Long: `kubeadm 节点需要 --server、--token、--ca-cert-hash，或使用 --from-file 执行控制面生成脚本。
k3s 节点使用 --distro k3s --server https://<server>:6443 --token <token>。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			distro := f.distro
			if distro != "" && distro != "kubeadm" && distro != "k3s" {
				return fmt.Errorf("--distro 必须是 kubeadm 或 k3s")
			}
			cni := pick(f.cni, cfg.K8s.CNI)
			if err := validateCNI(cni); err != nil {
				return err
			}
			mirrors, err := a.appliedMirrors(f.mirrors, f.noRegistry)
			if err != nil {
				return err
			}
			ok, err := a.confirm("即将修改系统并将当前节点加入集群，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			opts := k8s.JoinOptions{
				Distro:          distro,
				Server:          f.server,
				Token:           f.token,
				CACertHash:      f.caHash,
				ControlPlane:    f.controlPlane,
				CertificateKey:  f.certificateKey,
				CRISocket:       f.criSocket,
				FromFile:        f.fromFile,
				NodeIP:          f.nodeIP,
				Mirrors:         mirrors,
				ImageRepository: pick(f.imageRepo, cfg.K8s.ImageRepository),
				K3sMirror:       f.k3sMirror,
				SkipPreflight:   f.skipPreflight,
				CNI:             cni,
			}
			return k8s.Join(a.OS, a.R, a.FS, opts)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.distro, "distro", "", "kubeadm 或 k3s（默认自动检测）")
	fl.StringVar(&f.server, "server", "", "控制面地址 host:port 或 https://host:6443")
	fl.StringVar(&f.token, "token", "", "加入令牌")
	fl.StringVar(&f.caHash, "ca-cert-hash", "", "CA 证书哈希 sha256:...")
	fl.BoolVar(&f.controlPlane, "control-plane", false, "作为控制面节点加入")
	fl.StringVar(&f.certificateKey, "certificate-key", "", "控制面证书密钥（--control-plane 时使用）")
	fl.StringVar(&f.criSocket, "cri-socket", "", "CRI socket（默认自动检测）")
	fl.StringVar(&f.fromFile, "from-file", "", "执行控制面生成的 join 脚本")
	fl.StringVar(&f.nodeIP, "node-ip", "", "节点 IP（k3s）")
	fl.StringVar(&f.imageRepo, "image-repository", "", "Kubernetes 镜像仓库")
	fl.StringVar(&f.cni, "cni", "", "预检使用的 CNI（计算防火墙端口）")
	fl.StringArrayVar(&f.mirrors, "registry-mirror", nil, "Docker Hub 镜像加速地址")
	fl.BoolVar(&f.noRegistry, "no-registry-mirror", false, "不配置容器镜像加速")
	fl.BoolVar(&f.skipPreflight, "skip-preflight", false, "跳过系统预检")
	fl.BoolVar(&f.k3sMirror, "k3s-mirror", false, "使用 Rancher 中国镜像下载 k3s")
	return cmd
}

func (a *App) k8sJoinCommandCmd() *cobra.Command {
	var (
		distro string
		output string
	)
	cmd := &cobra.Command{
		Use:   "join-command",
		Short: "在控制面生成加入集群的命令",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			if distro != "" && distro != "kubeadm" && distro != "k3s" {
				return fmt.Errorf("--distro 必须是 kubeadm 或 k3s")
			}
			command, err := k8s.JoinCommand(a.OS, a.R, distro)
			if err != nil {
				return err
			}
			logx.Header("加入集群命令")
			logx.Raw("%s", command)
			if output != "" {
				if err := k8s.SaveJoinScript(a.FS, output, command); err != nil {
					return err
				}
				logx.Successf("已保存到 %s", output)
			} else {
				logx.Infof("将命令保存为脚本可加 --output /etc/makebox/join.sh")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&distro, "distro", "", "kubeadm 或 k3s（默认自动检测）")
	cmd.Flags().StringVarP(&output, "output", "o", "", "保存为可执行脚本")
	return cmd
}

func (a *App) k8sAddonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "addon",
		Short: "安装 Kubernetes 常用插件",
	}
	cmd.AddCommand(a.k8sAddonListCmd(), a.k8sAddonInstallCmd(), a.k8sAddonApplyCmd())
	return cmd
}

func (a *App) k8sAddonListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出内置插件",
		RunE: func(cmd *cobra.Command, args []string) error {
			logx.Header("内置插件")
			for _, addon := range k8s.Addons() {
				logx.Raw("  %-16s %s", addon.Name, addon.Description)
			}
			logx.Raw("\n安装: mkbox k8s addon install <名称>")
			return nil
		},
	}
}

func (a *App) k8sAddonInstallCmd() *cobra.Command {
	var (
		kubeconfig string
		source     string
		ghProxy    string
		podCIDR    string
		timeout    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "install <插件名>",
		Short: "下载并应用插件清单",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := a.cfgOrErr(); err != nil {
				return err
			}
			if podCIDR != "" {
				if _, _, err := net.ParseCIDR(podCIDR); err != nil {
					return fmt.Errorf("--pod-cidr 无效: %v", err)
				}
			}
			name := args[0]
			if _, ok := k8s.FindAddon(name); !ok {
				return fmt.Errorf("未知插件 %q，运行 `mkbox k8s addon list` 查看可用插件", name)
			}
			ok, err := a.confirm(fmt.Sprintf("即将向集群应用插件 %s，继续", name))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			installer := a.addonInstaller(kubeconfig, ghProxy, podCIDR, timeout)
			if source != "" {
				return installer.InstallSource(name, source)
			}
			return installer.Install(name)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig 路径（默认自动检测）")
	fl.StringVar(&source, "source", "", "自定义清单 URL 或本地文件")
	fl.StringVar(&ghProxy, "gh-proxy", "", "GitHub 代理地址")
	fl.StringVar(&podCIDR, "pod-cidr", "", "Calico pod 网段")
	fl.DurationVar(&timeout, "timeout", 3*time.Minute, "等待插件就绪的超时")
	return cmd
}

func (a *App) k8sAddonApplyCmd() *cobra.Command {
	var (
		kubeconfig string
		ghProxy    string
		podCIDR    string
		timeout    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "apply <文件|URL>",
		Short: "应用自定义 Kubernetes 清单",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := a.cfgOrErr(); err != nil {
				return err
			}
			if podCIDR != "" {
				if _, _, err := net.ParseCIDR(podCIDR); err != nil {
					return fmt.Errorf("--pod-cidr 无效: %v", err)
				}
			}
			ok, err := a.confirm(fmt.Sprintf("即将应用清单 %s，继续", args[0]))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			installer := a.addonInstaller(kubeconfig, ghProxy, podCIDR, timeout)
			return installer.InstallSource("custom", args[0])
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig 路径（默认自动检测）")
	fl.StringVar(&ghProxy, "gh-proxy", "", "GitHub 代理地址")
	fl.StringVar(&podCIDR, "pod-cidr", "", "Calico pod 网段")
	fl.DurationVar(&timeout, "timeout", 3*time.Minute, "等待插件就绪的超时")
	return cmd
}

func (a *App) addonInstaller(kubeconfig, ghProxy, podCIDR string, timeout time.Duration) *k8s.AddonInstaller {
	kc := pick(kubeconfig, k8s.DetectClusterKubeconfig())
	proxy := ghProxy
	if proxy == "" && a.Cfg != nil {
		proxy = a.Cfg.Network.GHProxy
	}
	pod := podCIDR
	if pod == "" && a.Cfg != nil {
		pod = a.Cfg.K8s.PodCIDR
	}
	installer := k8s.NewAddonInstaller(a.R, kc, proxy)
	installer.PodCIDR = pod
	installer.Timeout = timeout
	return installer
}

func (a *App) k8sKubeconfigCmd() *cobra.Command {
	var (
		source string
		output string
		server string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "导出 kubeconfig 并修正 API Server 地址",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := k8s.ExportKubeconfig(a.FS, source, output, server, force)
			if err != nil {
				return err
			}
			if path != "-" {
				logx.Successf("kubeconfig 已写入 %s", path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "源 kubeconfig（默认自动检测）")
	cmd.Flags().StringVarP(&output, "output", "o", "", "输出路径，- 表示标准输出")
	cmd.Flags().StringVar(&server, "server", "", "覆盖 API Server 地址，如 https://1.2.3.4:6443")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已存在的输出文件（原文件会备份）")
	return cmd
}

func (a *App) k8sStatusCmd() *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "查看集群节点与组件状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			kc := pick(kubeconfig, k8s.DetectClusterKubeconfig())
			if kc == "" {
				logx.Warnf("未找到本地 kubeconfig，当前节点可能尚未加入集群")
				return nil
			}
			if !execx.Has("kubectl") {
				return fmt.Errorf("未找到 kubectl，请先运行 `mkbox k8s install`")
			}
			logx.Header("Kubernetes 集群")
			logx.Item("kubeconfig", kc)
			if out, err := a.R.Output("kubectl", "--kubeconfig", kc, "cluster-info"); err == nil && strings.TrimSpace(out) != "" {
				logx.Raw("%s", strings.TrimSpace(out))
			}
			out, err := a.R.Output("kubectl", "--kubeconfig", kc, "get", "nodes", "-o", "wide")
			if err != nil {
				return fmt.Errorf("无法连接集群: %w", err)
			}
			logx.Header("节点")
			logx.Raw("%s", strings.TrimSpace(out))
			logx.Header("kube-system Pod")
			if out, err := a.R.Output("kubectl", "--kubeconfig", kc, "get", "pods", "-n", "kube-system", "-o", "wide"); err == nil && strings.TrimSpace(out) != "" {
				logx.Raw("%s", strings.TrimSpace(out))
			} else if err != nil {
				logx.Warnf("无法读取 kube-system Pod: %v", err)
			}
			logx.Header("关键组件")
			if out, err := a.R.Output("kubectl", "--kubeconfig", kc, "get", "--raw=/readyz?verbose"); err == nil && strings.TrimSpace(out) != "" {
				logx.Raw("%s", strings.TrimSpace(out))
			} else if err != nil {
				logx.Warnf("无法读取组件健康状态: %v", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig 路径（默认自动检测）")
	return cmd
}

func (a *App) k8sResetCmd() *cobra.Command {
	var (
		distro string
		all    bool
	)
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "重置本机 Kubernetes（删除配置与运行数据）",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			if distro != "" && distro != "kubeadm" && distro != "k3s" {
				return fmt.Errorf("--distro 必须是 kubeadm 或 k3s")
			}
			prompt := "即将重置本机 Kubernetes，集群配置与证书会被删除"
			if all {
				prompt += "，并删除 /etc/kubernetes、etcd 与 kubelet 数据（不可恢复）"
			}
			ok, err := a.confirm(prompt + "，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			return k8s.Reset(a.OS, a.R, a.FS, distro, all)
		},
	}
	cmd.Flags().StringVar(&distro, "distro", "", "kubeadm 或 k3s（默认自动检测）")
	cmd.Flags().BoolVar(&all, "all", false, "同时删除集群数据（不可恢复）")
	return cmd
}
