package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/netcheck"
	"github.com/makebox/makebox/internal/registry"
)

func (a *App) netCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "net",
		Short: "网络连通性检查",
	}
	cmd.AddCommand(a.netTestCmd())
	return cmd
}

func (a *App) netTestCmd() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "test",
		Short: "测试 Docker Hub、Kubernetes、GitHub、镜像站等端点",
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 {
				timeout = 5 * time.Second
				if a.Cfg != nil && a.Cfg.Network.CheckTimeoutMS > 0 {
					timeout = time.Duration(a.Cfg.Network.CheckTimeoutMS) * time.Millisecond
				}
			}
			logx.Header("网络连通性")
			results := netcheck.CheckAll(context.Background(), netcheck.DefaultEndpoints, timeout)
			ok := 0
			for _, r := range results {
				if r.OK {
					ok++
					logx.Raw("  %s %-24s %-8s HTTP %d", "✓", r.Name, registry.FormatLatency(r.Latency), r.StatusCode)
				} else {
					logx.Raw("  %s %-24s %-8s %s", "✗", r.Name, "-", r.Err)
				}
			}
			logx.Raw("")
			if ok == 0 {
				return fmt.Errorf("所有端点均不可达，请检查网络、DNS 或代理设置")
			}
			logx.Successf("%d/%d 个端点可达", ok, len(results))
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "单个端点超时（默认取 network.checkTimeoutMs 或 5s）")
	return cmd
}

func (a *App) registryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "registry",
		Aliases: []string{"reg"},
		Short:   "容器镜像加速器管理",
	}
	cmd.AddCommand(a.registryListCmd(), a.registryTestCmd())
	return cmd
}

func (a *App) registryListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出已配置的镜像加速器",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			logx.Header("镜像加速器")
			if len(cfg.Registry.Mirrors) == 0 {
				logx.Warnf("尚未配置镜像加速器，可运行 `mkbox docker mirror set <url>`")
				return nil
			}
			active := cfg.Registry.MaxMirrors
			if active > len(cfg.Registry.Mirrors) {
				active = len(cfg.Registry.Mirrors)
			}
			for i, m := range cfg.Registry.Mirrors {
				mark := " "
				if i < active {
					mark = "*"
				}
				logx.Raw("  %s %d. %s", mark, i+1, m)
			}
			logx.Raw("\n* 表示实际写入 Docker/containerd 的前 %d 个（registry.maxMirrors）；运行 `mkbox registry test` 检查可用性。", active)
			return nil
		},
	}
}

func (a *App) registryTestCmd() *cobra.Command {
	var (
		timeout time.Duration
		all     bool
	)
	cmd := &cobra.Command{
		Use:   "test [镜像地址...]",
		Short: "测试镜像加速器的 /v2/ 接口与延迟",
		Long:  "不带参数时测试配置中的镜像；也可直接传入一个或多个地址。",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			if timeout <= 0 {
				timeout = time.Duration(cfg.Registry.TimeoutSeconds) * time.Second
			}
			mirrors := a.registryMirrors(args, false)
			if len(args) == 0 && !all && len(mirrors) > cfg.Registry.MaxMirrors {
				mirrors = mirrors[:cfg.Registry.MaxMirrors]
			}
			if len(mirrors) == 0 {
				return fmt.Errorf("没有可测试的镜像地址")
			}
			return runRegistryTest(mirrors, timeout)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "单个镜像超时（默认取配置）")
	cmd.Flags().BoolVar(&all, "all", false, "测试全部镜像，不受 maxMirrors 限制")
	return cmd
}

func runRegistryTest(mirrors []string, timeout time.Duration) error {
	logx.Header("测试镜像加速器（%d 个）", len(mirrors))
	results := registry.CheckAll(context.Background(), mirrors, timeout, 6)
	working := 0
	for _, r := range results {
		if r.OK {
			working++
			logx.Raw("  %s %-46s %-8s HTTP %d", "✓", r.URL, registry.FormatLatency(r.Latency), r.StatusCode)
		} else {
			logx.Raw("  %s %-46s %-8s %s", "✗", r.URL, "-", r.Err)
		}
	}
	logx.Raw("")
	if working == 0 {
		return fmt.Errorf("没有可用的镜像加速器；请更换地址或检查网络/代理")
	}
	logx.Successf("%d/%d 个镜像可用", working, len(results))
	return nil
}
