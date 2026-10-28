package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/containerd"
	"github.com/makebox/makebox/internal/docker"
	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/proxy"
)

const defaultProxyScopes = "system,shell,apt,docker,containerd"

func (a *App) proxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "配置系统、apt、Docker 与 containerd 代理",
	}
	cmd.AddCommand(a.proxySetCmd(), a.proxyUnsetCmd(), a.proxyShowCmd())
	return cmd
}

func (a *App) proxySetCmd() *cobra.Command {
	var (
		noProxy string
		scope   string
	)
	cmd := &cobra.Command{
		Use:   "set <代理地址>",
		Short: "设置 HTTP(S)/SOCKS5 代理",
		Long: `支持的 scope：system、shell、apt、docker、containerd、all。
默认全部启用；未安装的 Docker/containerd 会自动跳过。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			proxyURL := strings.TrimSpace(args[0])
			if err := proxy.Validate(proxyURL); err != nil {
				return err
			}
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			scopes, err := proxy.ParseScopes(scope)
			if err != nil {
				return err
			}
			np := cfg.Proxy.NoProxy
			if strings.TrimSpace(noProxy) != "" {
				np = strings.Split(noProxy, ",")
			}
			np = docker.CleanNoProxy(np)

			ok, err := a.confirm(fmt.Sprintf("即将为 %s 配置代理 %s，继续", scope, redactProxy(proxyURL)))
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}

			mgr := proxy.NewManager(a.R, a.FS, a.OS)
			if err := mgr.Apply(proxyURL, np, scopes); err != nil {
				return err
			}
			if scopes["docker"] {
				if docker.IsInstalled() {
					if err := docker.NewManager(a.R, a.FS, a.OS).SetProxy(proxyURL, np); err != nil {
						logx.Warnf("配置 Docker 代理失败: %v", err)
					}
				} else {
					logx.Infof("未安装 Docker，跳过 Docker 代理")
				}
			}
			if scopes["containerd"] {
				if containerd.IsInstalled() {
					if err := containerd.NewManager(a.R, a.FS, a.OS).SetProxy(proxyURL, np); err != nil {
						logx.Warnf("配置 containerd 代理失败: %v", err)
					}
				} else {
					logx.Infof("未安装 containerd，跳过 containerd 代理")
				}
			}

			cfg.Proxy.HTTP = proxyURL
			cfg.Proxy.HTTPS = proxyURL
			cfg.Proxy.NoProxy = np
			if err := a.saveConfig(cfg); err != nil {
				return err
			}
			applyProcessProxy(proxyURL, np)
			logx.Successf("代理配置完成（当前 shell 可执行 source /etc/profile.d/mkbox-proxy.sh 立即生效）")
			return nil
		},
	}
	cmd.Flags().StringVar(&noProxy, "no-proxy", "", "逗号分隔的 NO_PROXY 列表")
	cmd.Flags().StringVar(&scope, "scope", defaultProxyScopes, "应用范围: system,shell,apt,docker,containerd,all")
	return cmd
}

func (a *App) proxyUnsetCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "unset",
		Short: "移除系统、apt、Docker 与 containerd 代理",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			scopes, err := proxy.ParseScopes(scope)
			if err != nil {
				return err
			}
			ok, err := a.confirm("即将移除代理配置并重启相关服务，继续")
			if err != nil {
				return err
			}
			if !ok {
				logx.Warnf("已取消")
				return nil
			}
			if err := proxy.NewManager(a.R, a.FS, a.OS).Clear(scopes); err != nil {
				return err
			}
			if scopes["docker"] && docker.IsInstalled() {
				if err := docker.NewManager(a.R, a.FS, a.OS).ClearProxy(); err != nil {
					logx.Warnf("清理 Docker 代理失败: %v", err)
				}
			}
			if scopes["containerd"] && containerd.IsInstalled() {
				if err := containerd.NewManager(a.R, a.FS, a.OS).ClearProxy(); err != nil {
					logx.Warnf("清理 containerd 代理失败: %v", err)
				}
			}
			if a.Cfg != nil {
				a.Cfg.Proxy.HTTP = ""
				a.Cfg.Proxy.HTTPS = ""
				if err := a.saveConfig(a.Cfg); err != nil {
					return err
				}
			}
			applyProcessProxy("", nil)
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", defaultProxyScopes, "清理范围: system,shell,apt,docker,containerd,all")
	return cmd
}

func (a *App) proxyShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "显示当前代理配置",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			logx.Header("mkbox 代理配置")
			if cfg.EffectiveProxy() == "" {
				logx.Item("配置", "未设置")
			} else {
				logx.Item("HTTP", redactProxy(cfg.Proxy.HTTP))
				logx.Item("HTTPS", redactProxy(cfg.Proxy.HTTPS))
				logx.Item("NO_PROXY", strings.Join(cfg.Proxy.NoProxy, ","))
			}
			if v := envProxy(); v != "" {
				logx.Item("当前环境", redactProxy(v))
			}
			if _, err := os.Stat(proxy.EnvFile); err == nil {
				logx.Item("shell 文件", proxy.EnvFile)
			}
			if _, err := os.Stat(proxy.AptConf); err == nil {
				logx.Item("apt 文件", proxy.AptConf)
			}
			if _, err := os.Stat(docker.ProxyConf); err == nil {
				logx.Item("Docker", docker.ProxyConf)
			}
			if _, err := os.Stat(containerd.ProxyConf); err == nil {
				logx.Item("containerd", containerd.ProxyConf)
			}
			return nil
		},
	}
}
