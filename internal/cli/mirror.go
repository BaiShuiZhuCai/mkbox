package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/makebox/makebox/internal/logx"
	"github.com/makebox/makebox/internal/mirror"
)

func (a *App) mirrorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mirror",
		Short: "切换 Linux 软件源与开发工具镜像",
	}
	cmd.AddCommand(a.mirrorOSCmd(), a.mirrorDevCmd(), a.mirrorListCmd(), a.mirrorStatusCmd())
	return cmd
}

func (a *App) mirrorOSCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "os [镜像站]",
		Short: "切换系统软件源（aliyun/tuna/ustc/huawei）",
		Long:  "支持 Ubuntu/Debian、Arch、Alpine。RHEL/SUSE 因仓库结构差异暂不自动切换。",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			id := cfg.Mirror.OS
			if len(args) == 1 {
				id = args[0]
			}
			src, ok := mirror.GetSource(id)
			if !ok {
				return fmt.Errorf("未知镜像站 %q，可选: aliyun, tuna, ustc, huawei", id)
			}
			okConfirm, err := a.confirm(fmt.Sprintf("即将把系统软件源切换到%s并执行包管理器更新，继续", src.Name))
			if err != nil {
				return err
			}
			if !okConfirm {
				logx.Warnf("已取消")
				return nil
			}
			if err := mirror.NewManager(a.R, a.FS, a.OS).SetOS(id); err != nil {
				return err
			}
			cfg.Mirror.OS = id
			return a.saveConfig(cfg)
		},
	}
}

func (a *App) mirrorDevCmd() *cobra.Command {
	var (
		pip   bool
		npm   bool
		gomod bool
		all   bool
	)
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "配置 pip / npm / Go 模块镜像",
		Long:  "默认同时配置三者；也可用 --pip、--npm、--go 精细选择。",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireRoot(); err != nil {
				return err
			}
			if all || (!pip && !npm && !gomod) {
				pip, npm, gomod = true, true, true
			}
			okConfirm, err := a.confirm("即将写入 pip/npm/Go 镜像配置，继续")
			if err != nil {
				return err
			}
			if !okConfirm {
				logx.Warnf("已取消")
				return nil
			}
			return mirror.NewManager(a.R, a.FS, a.OS).SetDev(pip, npm, gomod)
		},
	}
	cmd.Flags().BoolVar(&pip, "pip", false, "配置 pip（阿里云 PyPI）")
	cmd.Flags().BoolVar(&npm, "npm", false, "配置 npm（npmmirror）")
	cmd.Flags().BoolVar(&gomod, "go", false, "配置 Go（goproxy.cn）")
	cmd.Flags().BoolVar(&all, "all", false, "配置全部")
	return cmd
}

func (a *App) mirrorListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出支持的镜像站",
		RunE: func(cmd *cobra.Command, args []string) error {
			logx.Header("系统镜像站")
			for _, s := range mirror.Sources {
				logx.Raw("  %-10s %s", s.ID, s.Name)
			}
			current := mirror.DetectOSMirror(a.OS)
			if current != "" {
				logx.Raw("\n当前检测到: %s", current)
			}
			return nil
		},
	}
}

func (a *App) mirrorStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "显示当前软件源镜像站",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.cfgOrErr()
			if err != nil {
				return err
			}
			logx.Header("软件源状态")
			logx.Item("mkbox 配置", cfg.Mirror.OS)
			if current := mirror.DetectOSMirror(a.OS); current != "" {
				logx.Item("当前系统", current)
			} else {
				logx.Item("当前系统", "无法识别或未使用受支持的镜像站")
			}
			return nil
		},
	}
}
