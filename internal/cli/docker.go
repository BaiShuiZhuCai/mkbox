package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/config"
	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/proxy"
	"github.com/makebox/makebox/internal/registry"
)

func (a *App) dockerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docker",
		Short: "安装、卸载与配置 Docker Engine",
	}
	cmd.AddCommand(
		a.dockerInstallCmd(),
		a.dockerUninstallCmd(),
		a.dockerStatusCmd(),
		a.dockerMirrorCmd(),
		a.dockerProxyCmd(),
		a.dockerDataRootCmd(),
	)
	return cmd
}

func (a *App) dockerInstallCmd() *cobra.Command {
	var (
		mirror   string
		user     string
		mirrors  []string
		noMirror bool
		probe    bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "安装 Docker Engine 并配置镜像加速与日志轮转",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			mirror = pick(mirror, cfg.Docker.InstallMirror)
			if err := docker.ValidatePackageMirror(mirror); err != nil {
				return err
			}
			chosen, err := a.appliedMirrors(mirrors, noMirror)
			if err != nil {
				return err
			}
			if probe && len(chosen) > 0 {
				chosen = a.probeMirrors(chosen, cfg.Registry.MaxMirrors)
			}
			if user == "" && a.OS.SudoUser != nil {
				user = a.OS.SudoUser.Name
			}
			ok, err := a.confirm(fmt.Sprintf("即将安装 Docker（软件源 %s）并修改系统配置，继续", mirror))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}

			engine := docker.NewEngine(a.OS, a.R, a.FS, mirror, user)
			if err := engine.Install(); err != nil {
				return err
			}

			if err := a.applyDockerConfig(cfg, chosen, true); err != nil {
				return err
			}
			logx.Successf("Docker 安装与配置完成")
			logx.Infof("普通用户如未自动加入 docker 组，请执行: sudo usermod -aG docker $USER && newgrp docker")
			return nil
		},
	}
	cmd.Flags().StringVar(&mirror, "mirror", "", "Docker 软件源: aliyun/tuna/ustc/huawei/official")
	cmd.Flags().StringVar(&user, "user", "", "加入 docker 组的用户（默认 sudo 调用者）")
	cmd.Flags().StringArrayVar(&mirrors, "registry-mirror", nil, "容器镜像加速地址，可重复（默认取配置）")
	cmd.Flags().BoolVar(&noMirror, "no-registry-mirror", false, "不配置容器镜像加速")
	cmd.Flags().BoolVar(&probe, "probe", false, "先探测镜像加速器并只使用可用的")
	return cmd
}

// applyDockerConfig merges mirrors and log settings into daemon.json.
func (a *App) applyDockerConfig(cfg *config.Config, mirrors []string, restart bool) error {
	mgr := docker.NewManager(a.R, a.FS, a.OS)
	dc, err := mgr.Load()
	if err != nil {
		return err
	}
	if len(mirrors) > 0 {
		if err := dc.SetMirrors(mirrors); err != nil {
			return err
		}
	}
	dc.SetLogging(cfg.Docker.LogDriver, cfg.Docker.LogMaxSize, cfg.Docker.LogMaxFile)
	if cfg.Docker.DataRoot != "" {
		dc.SetDataRoot(cfg.Docker.DataRoot)
	}
	if err := mgr.Save(dc); err != nil {
		return err
	}
	if restart {
		if err := mgr.Restart(); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) probeMirrors(candidates []string, max int) []string {
	logx.Header("探测镜像加速器")
	timeout := 5 * time.Second
	if a.Cfg != nil && a.Cfg.Registry.TimeoutSeconds > 0 {
		timeout = time.Duration(a.Cfg.Registry.TimeoutSeconds) * time.Second
	}
	results := registry.CheckAll(context.Background(), candidates, timeout, 6)
	best := registry.BestURLs(results, max)
	if len(best) == 0 {
		logx.Warnf("没有探测到可用镜像，保留原配置")
		return candidates
	}
	logx.Infof("选择 %d 个可用镜像: %s", len(best), strings.Join(best, ", "))
	return best
}

func (a *App) dockerUninstallCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "卸载 Docker Engine",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			prompt := "即将卸载 Docker Engine"
			if purge {
				prompt += "，并永久删除镜像、容器与数据（不可恢复）"
			}
			ok, err := a.confirm(prompt + "，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			return docker.NewEngine(a.OS, a.R, a.FS, "", "").Uninstall(purge)
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "同时删除 /var/lib/docker 等数据（不可恢复）")
	return cmd
}

func (a *App) dockerStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "显示 Docker 安装与运行状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			st := docker.GetStatus(a.R)
			logx.Header("Docker 状态")
			if !st.Installed {
				logx.Warnf("未安装 Docker，运行 `sudo mkbox docker install`")
				return nil
			}
			logx.Item("版本", strings.TrimSpace(st.Version))
			if st.Running {
				logx.Item("服务", "运行中")
			} else {
				logx.Item("服务", "未运行")
			}
			if st.DataRoot != "" {
				logx.Item("数据目录", st.DataRoot)
			}
			if len(st.Mirrors) > 0 {
				logx.Item("镜像加速", strings.Join(st.Mirrors, ", "))
			} else {
				logx.Item("镜像加速", "未配置")
			}
			return nil
		},
	}
}

func (a *App) dockerMirrorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mirror",
		Aliases: []string{"mirrors"},
		Short:   "管理 Docker Hub 镜像加速器",
	}
	cmd.AddCommand(
		a.dockerMirrorSetCmd(),
		a.dockerMirrorUnsetCmd(),
		a.dockerMirrorListCmd(),
		a.dockerMirrorShowCmd(),
		a.dockerMirrorTestCmd(),
	)
	return cmd
}

func (a *App) dockerMirrorSetCmd() *cobra.Command {
	var (
		probe bool
	)
	cmd := &cobra.Command{
		Use:   "set [镜像地址...]",
		Short: "写入 registry-mirrors 并重启 Docker",
		Long:  "不带地址时重新应用配置中的镜像列表。地址会自动规范化并去重。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			mirrors, err := a.appliedMirrors(args, false)
			if err != nil {
				return err
			}
			if len(mirrors) == 0 {
				return fmt.Errorf("至少提供一个镜像地址")
			}
			if probe {
				mirrors = a.probeMirrors(mirrors, cfg.Registry.MaxMirrors)
			}
			ok, err := a.confirm("即将更新 Docker 镜像加速配置并重启 Docker，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			mgr := docker.NewManager(a.R, a.FS, a.OS)
			dc, err := mgr.Load()
			if err != nil {
				return err
			}
			if err := dc.SetMirrors(mirrors); err != nil {
				return err
			}
			if err := mgr.Save(dc); err != nil {
				return err
			}
			if err := mgr.Restart(); err != nil {
				return err
			}
			cfg.Registry.Mirrors = mirrors
			return a.saveConfig(cfg)
		},
	}
	cmd.Flags().BoolVar(&probe, "probe", false, "先探测并只使用可用的镜像")
	return cmd
}

func (a *App) dockerMirrorUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset",
		Short: "移除 Docker 镜像加速配置并重启 Docker",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			ok, err := a.confirm("即将清空 registry-mirrors 配置并重启 Docker，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			mgr := docker.NewManager(a.R, a.FS, a.OS)
			dc, err := mgr.Load()
			if err != nil {
				return err
			}
			if err := dc.SetMirrors(nil); err != nil {
				return err
			}
			if err := mgr.Save(dc); err != nil {
				return err
			}
			if err := mgr.Restart(); err != nil {
				return err
			}
			if a.Cfg != nil {
				a.Cfg.Registry.Mirrors = []string{}
				if err := a.saveConfig(a.Cfg); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func (a *App) dockerMirrorListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "显示配置与 daemon.json 中的镜像加速器",
		RunE: func(cmd *cobra.Command, args []string) error {
			logx.Header("镜像加速器")
			if a.Cfg != nil {
				logx.Raw("配置 (%s):", a.CfgPath)
				for _, m := range a.Cfg.Registry.Mirrors {
					logx.Raw("  - %s", m)
				}
			}
			dc, err := docker.ReadDaemon(docker.DaemonJSON)
			if err != nil {
				logx.Warnf("读取 %s 失败: %v", docker.DaemonJSON, err)
				return nil
			}
			logx.Raw("daemon.json:")
			actual := dc.Mirrors()
			if len(actual) == 0 {
				logx.Raw("  （未配置）")
			}
			for _, m := range actual {
				logx.Raw("  - %s", m)
			}
			return nil
		},
	}
}

func (a *App) dockerMirrorShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "显示完整的 daemon.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			dc, err := docker.ReadDaemon(docker.DaemonJSON)
			if err != nil {
				return err
			}
			logx.Header("%s", docker.DaemonJSON)
			logx.Raw("%s", dc.JSON())
			return nil
		},
	}
}

func (a *App) dockerMirrorTestCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "test",
		Short: "测试当前 daemon.json 中的镜像加速器",
		RunE: func(cmd *cobra.Command, args []string) error {
			dc, err := docker.ReadDaemon(docker.DaemonJSON)
			if err != nil {
				return err
			}
			mirrors := dc.Mirrors()
			if len(mirrors) == 0 && a.Cfg != nil {
				mirrors = a.Cfg.Registry.Mirrors
			}
			if len(mirrors) == 0 {
				return fmt.Errorf("没有可测试的镜像地址")
			}
			if timeout <= 0 {
				timeout = 5 * time.Second
			}
			return runRegistryTest(mirrors, timeout)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "单个镜像超时")
	return cmd
}

func (a *App) dockerProxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "为 Docker daemon 配置 HTTP(S) 代理",
	}
	cmd.AddCommand(a.dockerProxySetCmd(), a.dockerProxyUnsetCmd(), a.dockerProxyShowCmd())
	return cmd
}

func (a *App) dockerProxySetCmd() *cobra.Command {
	var noProxy string
	cmd := &cobra.Command{
		Use:   "set <代理地址>",
		Short: "通过 systemd drop-in 为 dockerd 设置代理",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			if err := proxy.Validate(args[0]); err != nil {
				return err
			}
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			np := cfg.Proxy.NoProxy
			if strings.TrimSpace(noProxy) != "" {
				np = strings.Split(noProxy, ",")
			}
			np = docker.CleanNoProxy(np)
			ok, err := a.confirm("即将写入 Docker 代理配置并重启 Docker，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			if err := docker.NewManager(a.R, a.FS, a.OS).SetProxy(args[0], np); err != nil {
				return err
			}
			cfg.Proxy.HTTP = args[0]
			cfg.Proxy.HTTPS = args[0]
			cfg.Proxy.NoProxy = np
			return a.saveConfig(cfg)
		},
	}
	cmd.Flags().StringVar(&noProxy, "no-proxy", "", "逗号分隔的 NO_PROXY 列表")
	return cmd
}

func (a *App) dockerProxyUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset",
		Short: "移除 Docker daemon 代理并重启",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			ok, err := a.confirm("即将移除 Docker 代理并重启 Docker，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			return docker.NewManager(a.R, a.FS, a.OS).ClearProxy()
		},
	}
}

func (a *App) dockerProxyShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "显示 Docker daemon 代理配置状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			content, ok := docker.NewManager(a.R, a.FS, a.OS).ProxyStatus()
			if !ok {
				logx.Infof("未配置 Docker daemon 代理")
				return nil
			}
			logx.Header("Docker daemon 代理")
			for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
				if strings.Contains(strings.ToUpper(line), "PROXY=") {
					if i := strings.Index(line, "="); i > 0 {
						val := strings.Trim(line[i+1:], `"`)
						logx.Item(line[:i], redactProxy(val))
						continue
					}
				}
				logx.Raw("  %s", line)
			}
			return nil
		},
	}
}

func (a *App) dockerDataRootCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "data-root <目录>",
		Short: "修改 Docker 数据目录（不会迁移已有数据）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			path := args[0]
			if _, err := docker.ValidateDataRoot(path); err != nil {
				return err
			}
			ok, err := a.confirm(fmt.Sprintf("即将把 Docker 数据目录改为 %s（不会自动迁移旧数据），继续", path))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			if err := docker.NewManager(a.R, a.FS, a.OS).ApplyDataRoot(path); err != nil {
				return err
			}
			if a.Cfg != nil {
				a.Cfg.Docker.DataRoot = path
				if err := a.saveConfig(a.Cfg); err != nil {
					return err
				}
			}
			logx.Infof("迁移完成后运行: sudo systemctl restart docker")
			return nil
		},
	}
}
